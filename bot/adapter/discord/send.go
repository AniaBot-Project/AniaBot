package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

const (
	// maxTextLen Discord 单条消息内容上限为 2000 字符，留余量分包。
	maxTextLen = 1990
	// maxFilesPerMessage Discord 单条消息附件数上限。
	maxFilesPerMessage = 10
	// maxUploadSize Discord Bot 附件大小上限（约 25 MiB，超出跳过该附件不拖累整条链）。
	maxUploadSize = 25 << 20
)

// SendGroupMsg 发送群聊消息（Discord 向频道发消息，groupId 为 dc:<channel_id>）。
func (a *discordAdapter) SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (message.QID, bool) {
	channelID, ok := parseChannelID(groupId)
	if !ok || a.api == nil {
		return "", false
	}
	return a.sendChain(channelID, chain.GetGroupMsg(), false)
}

// SendFriendMsg 发送私聊消息：Discord 需先经 UserChannelCreate 打开 DM 频道再发送。
func (a *discordAdapter) SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (message.QID, bool) {
	rawUserID, ok := parseChannelID(userId)
	if !ok || a.api == nil {
		return "", false
	}
	dm, err := a.api.UserChannelCreate(rawUserID)
	if err != nil {
		a.logSendFail("UserChannelCreate", err, "userId", rawUserID)
		return "", false
	}
	return a.sendChain(dm.ID, chain.GetFriendMsg(), true)
}

// sendChain 把通用消息段翻译为 Discord 消息序列并发送。
// 文本/提及累积为 Content（Discord 原生渲染 markdown，无需转换）；媒体段携带
// 已累积文本作为同条消息 Content 合批上传（≤10 文件/条）；首条消息携带引用回复。
// 返回最后成功消息的框架内 ID（dc:<channel>:<msgid>）。
func (a *discordAdapter) sendChain(channelID string, segs []message.OB11Segment, private bool) (message.QID, bool) {
	if a.api == nil {
		return "", false
	}
	// 提取回复目标（首条 reply 段，非 dc: 前缀忽略），其余段作为正文
	var replyTo *discordgo.MessageReference
	atAll := false
	body := make([]message.OB11Segment, 0, len(segs))
	for _, s := range segs {
		if s.Type == message.SegmentReply && replyTo == nil {
			if id, ok := s.Data["id"].(string); ok {
				if _, mid, ok2 := parseMsgID(id); ok2 {
					replyTo = &discordgo.MessageReference{MessageID: mid, ChannelID: channelID}
					continue
				}
			}
		}
		body = append(body, s)
	}
	if len(body) == 0 {
		return "", false
	}

	var text strings.Builder
	var files []*discordgo.File
	var lastMsgID string
	sentAny := false

	// flush 发送当前累积（文本单独发 / 文本+文件合批发），首条携带引用回复
	flush := func() {
		if text.Len() == 0 && len(files) == 0 {
			return
		}
		content := text.String()
		text.Reset()
		var id string
		var ok bool
		if len(files) > 0 {
			id, ok = a.sendFiles(channelID, content, files, replyTo, atAll)
			files = nil
		} else {
			id, ok = a.sendText(channelID, content, replyTo, atAll)
		}
		if ok {
			lastMsgID = id
			sentAny = true
			replyTo = nil // 引用回复仅首条成功消息携带
		}
	}

	for _, s := range body {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				text.WriteString(t)
			}
		case message.SegmentMention:
			text.WriteString(renderMention(s, &atAll))
		case message.SegmentImage, message.SegmentFile, message.SegmentRecord, message.SegmentVideo:
			f := a.resolveSegmentFile(s)
			if f == nil {
				continue // 资源解析失败/超限：跳过该附件不拖累整条链
			}
			files = append(files, f)
			if len(files) >= maxFilesPerMessage {
				flush()
			}
		default:
			// 不支持的段（face/json/music/forward）：有 text 键时退化为文本
			if t, ok := s.Data["text"].(string); ok {
				text.WriteString(t)
			} else {
				a.logger.Debug("忽略 Discord 不支持的通用消息段", "segment", s.Type)
			}
		}
	}
	flush()
	if !sentAny {
		return "", false
	}
	a.cacheSent(channelID, lastMsgID, body, private)
	return msgID(channelID, lastMsgID), true
}

// allowedMentions 出站 @ 权限收敛：默认只允许用户提及，防止 AI 文本中的字面
// @everyone/@here 误触全服通知；仅显式 at-all 段渲染时追加 everyone。
// 引用回复不 ping 被回复者（RepliedUser=false）。
func allowedMentions(atAll bool) *discordgo.MessageAllowedMentions {
	parse := []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}
	if atAll {
		parse = append(parse, discordgo.AllowedMentionTypeEveryone)
	}
	return &discordgo.MessageAllowedMentions{Parse: parse}
}

// renderMention at 段 → Discord 提及文本：<@id> / @everyone（后者翻转 atAll 标记）。
// 非 dc: 前缀（其他平台 ID）静默丢弃。
func renderMention(s message.OB11Segment, atAll *bool) string {
	qq, _ := s.Data["qq"].(string)
	if qq == "all" {
		*atAll = true
		return "@everyone "
	}
	raw := strings.TrimPrefix(qq, idPrefix)
	if raw == "" || raw == qq {
		return ""
	}
	return "<@" + raw + "> "
}

// sendText 发送文本消息（超上限分包，仅首包携带引用回复）。
func (a *discordAdapter) sendText(channelID, content string, replyTo *discordgo.MessageReference, atAll bool) (string, bool) {
	parts := splitText(content, maxTextLen)
	if len(parts) == 0 {
		return "", false
	}
	last := ""
	for i, part := range parts {
		var ref *discordgo.MessageReference
		if i == 0 {
			ref = replyTo
		}
		m, err := a.api.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Content:         part,
			Reference:       ref,
			AllowedMentions: allowedMentions(atAll),
		})
		if err != nil {
			a.logSendFail("ChannelMessageSend", err, "channelId", channelID)
			return last, last != "" // 部分成功也上报最后成功消息
		}
		last = m.ID
	}
	return last, true
}

// sendFiles 发送附件消息（文本作为同条 Content，与 Telegram 的 1024 字节 caption
// 限制不同——Discord 允许完整 2000 字符内容与附件同条发送）。
func (a *discordAdapter) sendFiles(channelID, content string, files []*discordgo.File, replyTo *discordgo.MessageReference, atAll bool) (string, bool) {
	m, err := a.api.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         truncateRunes(content, maxTextLen),
		Files:           files,
		Reference:       replyTo,
		AllowedMentions: allowedMentions(atAll),
	})
	if err != nil {
		a.logSendFail("ChannelMessageSendComplex", err, "channelId", channelID, "files", len(files))
		return "", false
	}
	return m.ID, true
}

// resolveSegmentFile 媒体段 → 上传文件：base64://、data:、file:// 本地解码；
// http(s) URL 下载后重传（Discord 不抓取外链附件，与 Telegram 服务端抓取不同）。
// 超过大小上限返回 nil（调用方跳过）。
func (a *discordAdapter) resolveSegmentFile(s message.OB11Segment) *discordgo.File {
	var src, name string
	switch s.Type {
	case message.SegmentImage:
		name = "image.png"
		src, _ = s.Data["url"].(string)
		if src == "" {
			src, _ = s.Data["file"].(string)
		}
	case message.SegmentFile:
		name = "file"
		src, _ = s.Data["file"].(string)
		if n, ok := s.Data["name"].(string); ok && n != "" {
			name = n
		}
	case message.SegmentRecord:
		// RecordMessage.Marshal 只写 file 键（见 messagesegment.go）
		name = "voice.ogg"
		src, _ = s.Data["file"].(string)
		if src == "" {
			src, _ = s.Data["url"].(string)
		}
	case message.SegmentVideo:
		name = "video.mp4"
		src, _ = s.Data["url"].(string)
		if src == "" {
			src, _ = s.Data["file"].(string)
		}
	}
	if src == "" {
		return nil
	}
	data, ok := a.resolveBytes(src)
	if !ok {
		a.logger.Warn("Discord 媒体资源解析失败", "segment", s.Type)
		return nil
	}
	if len(data) > maxUploadSize {
		a.logger.Warn("Discord 附件超过大小上限，跳过", "segment", s.Type, "size", len(data))
		return nil
	}
	return &discordgo.File{Name: name, Reader: bytes.NewReader(data)}
}

// resolveBytes 解析文件源字节：base64:// / file:// / data: 本地解码；
// http(s) URL 经会话 HTTP 客户端下载（代理生效，20s 超时，32MiB 限读）。
func (a *discordAdapter) resolveBytes(src string) ([]byte, bool) {
	switch {
	case strings.HasPrefix(src, "base64://"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(src, "base64://"))
		return b, err == nil && len(b) > 0
	case strings.HasPrefix(src, "file://"):
		b, err := os.ReadFile(strings.TrimPrefix(src, "file://"))
		return b, err == nil && len(b) > 0
	case strings.HasPrefix(src, "data:"):
		if _, after, ok := strings.Cut(src, ","); ok {
			b, err := base64.StdEncoding.DecodeString(after)
			return b, err == nil && len(b) > 0
		}
		return nil, false
	case strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://"):
		return a.downloadBytes(src)
	}
	return nil, false
}

// downloadBytes 下载远程资源（出站附件重传用）。
func (a *discordAdapter) downloadBytes(url string) ([]byte, bool) {
	client := http.DefaultClient
	if a.session != nil && a.session.Client != nil {
		client = a.session.Client
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		a.logger.Debug("Discord 出站附件下载失败", "url", url, "error", err)
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadSize+1))
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// splitText 按 Discord 单条消息上限分包（按 rune 计数，与 Discord 的字符计数一致）。
func splitText(text string, limit int) []string {
	runes := []rune(text)
	var parts []string
	for start := 0; start < len(runes); start += limit {
		end := min(start+limit, len(runes))
		parts = append(parts, string(runes[start:end]))
	}
	return parts
}

// truncateRunes 按 rune 截断（不切断多字节字符）。
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// cacheSent 记录出站消息到内存缓存（GetMsgDetail/历史兜底）。
// 出站消息先剔除内联 base64/data 负载再入缓存，避免大图常驻内存。
func (a *discordAdapter) cacheSent(channelID, messageID string, segs []message.OB11Segment, private bool) {
	msgType := "group"
	if private {
		msgType = "private"
	}
	// 与入站保持一致：DM 的频道 ID 不是群号，私聊不写 GroupId
	var groupID message.QID
	if !private {
		groupID = message.QID(idPrefix + channelID)
	}
	msg := message.Message{
		Time:        uint(time.Now().Unix()),
		PostType:    "message",
		MessageType: msgType,
		MessageId:   msgID(channelID, messageID),
		GroupId:     groupID,
		Message:     message.StripInlinePayloadSegments(segs),
		RawMessage:  segmentsPlainText(segs),
		SelfId:      a.SelfID(),
		Platform:    Platform,
	}
	a.msgCache.Push(channelID, msg)
}

// logSendFail 记录 Discord API 调用失败详情。
func (a *discordAdapter) logSendFail(op string, err error, kv ...any) {
	args := append([]any{"op", op, "error", err}, kv...)
	a.logger.Warn("Discord "+op+" 失败", args...)
}
