package discord

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// mentionPattern Discord 内容标记：用户提及 <@id>/<@!id>（旧形态）、角色提及 <@&id>、
// 频道提及 <#id>、自定义表情 <a?:name:id>（a 前缀为动图）。
var mentionPattern = regexp.MustCompile(`<@!?(\d+)>|<@&(\d+)>|<#(\d+)>|<a?:(\w+):\d+>`)

// ---------- 消息翻译（入站） ----------

// translateMessage 将一条 Discord 消息翻译为框架通用消息。
// 在异步分发 goroutine 内调用（附件下载等 I/O 不阻塞网关）。
func (a *discordAdapter) translateMessage(m *discordgo.Message) *message.Message {
	segs := a.messageToSegments(m)
	if len(segs) == 0 {
		return nil
	}
	// 对齐 OneBot v11 消息类型语义：DM（无 GuildID）= private + sub_type=friend。
	// DM 的频道 ID 不是群号，私聊不写 GroupId：框架用它判断群聊（插件据此选
	// 会话键与发送路由），私聊按 UserId 即可寻址，DM 频道由适配器按需打开。
	msgType, subType := "group", ""
	var groupID message.QID
	if m.GuildID == "" {
		msgType, subType = "private", "friend"
	} else {
		groupID = message.QID(idPrefix + m.ChannelID)
	}
	userID := userQID(m.Author.ID)
	msg := &message.Message{
		Time:        uint(m.Timestamp.Unix()),
		PostType:    "message",
		MessageType: msgType,
		SubType:     subType,
		MessageId:   msgID(m.ChannelID, m.ID),
		GroupId:     groupID,
		UserId:      userID,
		Message:     segs,
		RawMessage:  segmentsPlainText(segs),
		Sender: message.MessageSender{
			UserId:   userID,
			Nickname: nicknameOf(m),
		},
		SelfId:   a.SelfID(),
		Platform: Platform,
	}
	return msg
}

// messageToSegments 将消息内容翻译为通用消息段：
// reply 段（首段）→ 文本/at 交错段 → 附件段；无有效内容返回 nil 由调用方丢弃。
// 图片附件下载为 data URI 写入 url 键（AI 插件的 load_images 只认 url 键）。
func (a *discordAdapter) messageToSegments(m *discordgo.Message) []message.OB11Segment {
	if m == nil {
		return nil
	}
	var segs []message.OB11Segment

	// 引用回复：优先 ReferencedMessage（网关已内联），退化为 MessageReference
	// （跨服务器引用/内联缺失时 ChannelID 可能为空，回落到本消息频道）
	if ref := replyTarget(m); ref != nil {
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentReply,
			Data: message.ReplyMessage{Id: msgID(ref[0], ref[1])}.Marshal(),
		})
	}

	segs = append(segs, splitContent(m.Content)...)
	if m.MentionEveryone {
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentMention,
			Data: message.MentionMessage{QQ: "all", IsAll: true}.Marshal(),
		})
	}

	for _, att := range m.Attachments {
		segs = append(segs, a.attachmentToSegment(att))
	}
	if len(m.StickerItems) > 0 {
		segs = appendTextSeg(segs, "[贴纸]")
	}
	return segs
}

// replyTarget 提取引用回复目标 [channelID, messageID]；无引用返回 nil。
func replyTarget(m *discordgo.Message) []string {
	if m.ReferencedMessage != nil {
		return []string{m.ReferencedMessage.ChannelID, m.ReferencedMessage.ID}
	}
	if r := m.MessageReference; r != nil && r.MessageID != "" {
		channelID := r.ChannelID
		if channelID == "" {
			channelID = m.ChannelID
		}
		return []string{channelID, r.MessageID}
	}
	return nil
}

// splitContent 按标记拆分消息文本为用户 at 段与文本段（保持原位交错）：
//   - <@id>/<@!id> → at 段（qq=dc:<id>）——@bot 提及由此产出 qq==SelfId 的 at 段，
//     core 的提及检测（at 段 qq 与 SelfId 精确比较）与 aichat @ 触发依赖它
//   - 角色提及 <@&id> → 字面 "@role"（无额外 API 查询角色名，保留文本形态不静默丢内容）
//   - 频道提及 <#id> → 字面 "#channel"
//   - 自定义表情 <a?:name:id> → 字面 ":name:"
func splitContent(content string) []message.OB11Segment {
	var segs []message.OB11Segment
	if content == "" {
		return segs
	}
	pos := 0
	for _, loc := range mentionPattern.FindAllStringSubmatchIndex(content, -1) {
		if loc[0] > pos {
			segs = appendTextSeg(segs, content[pos:loc[0]])
		}
		switch {
		case loc[2] >= 0: // 用户提及
			segs = append(segs, message.OB11Segment{
				Type: message.SegmentMention,
				Data: message.MentionMessage{QQ: userQID(content[loc[2]:loc[3]])}.Marshal(),
			})
		case loc[4] >= 0: // 角色提及
			segs = appendTextSeg(segs, "@role")
		case loc[6] >= 0: // 频道提及
			segs = appendTextSeg(segs, "#channel")
		case loc[8] >= 0: // 自定义表情
			segs = appendTextSeg(segs, ":"+content[loc[8]:loc[9]]+":")
		}
		pos = loc[1]
	}
	return appendTextSeg(segs, content[pos:])
}

// attachmentToSegment 附件 → 通用段：按 ContentType 分派（image→图片段并下载转
// data URI，video→视频段，audio→语音段，其余→文件段）。
func (a *discordAdapter) attachmentToSegment(att *discordgo.MessageAttachment) message.OB11Segment {
	ct := att.ContentType
	switch {
	case strings.HasPrefix(ct, "image/"):
		seg := message.OB11Segment{
			Type: message.SegmentImage,
			Data: message.ImageMessage{File: att.URL, Summary: "[图片]"}.Marshal(),
		}
		// 下载为 data URI 补齐 url 键（失败保留 CDN URL——注意 Discord CDN 链接
		// 带签名约 24h 过期，过期后 AI 插件无法加载，与 QQ 临时链接同命运）
		if uri := a.downloadAttachment(att.URL, ct); uri != "" {
			seg.Data["url"] = uri
		}
		return seg
	case strings.HasPrefix(ct, "video/"):
		return message.OB11Segment{
			Type: message.SegmentVideo,
			Data: message.VideoMessage{URL: att.URL}.Marshal(),
		}
	case strings.HasPrefix(ct, "audio/"):
		return message.OB11Segment{
			Type: message.SegmentRecord,
			Data: message.RecordMessage{URL: att.URL}.Marshal(),
		}
	default:
		return message.OB11Segment{
			Type: message.SegmentFile,
			Data: message.FileMessage{File: att.URL, Name: att.Filename}.Marshal(),
		}
	}
}

// downloadAttachment 下载附件并转为 data URI（供 AI 插件直接加载）。
// 走会话的 HTTP 客户端（代理配置对下载同样生效）。
func (a *discordAdapter) downloadAttachment(url, contentType string) string {
	if a.session == nil || url == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := a.session.Client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		a.logger.Debug("Discord 附件下载失败", "url", url, "error", err)
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil || len(body) == 0 {
		return ""
	}
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body)
}

// nicknameOf 发送者显示名：服务器昵称 > 全局显示名 > 用户名（事件内联，无需 API 查询）。
func nicknameOf(m *discordgo.Message) string {
	if m.Member != nil && m.Member.Nick != "" {
		return m.Member.Nick
	}
	if m.Author == nil {
		return ""
	}
	if m.Author.GlobalName != "" {
		return m.Author.GlobalName
	}
	return m.Author.Username
}

func appendTextSeg(segs []message.OB11Segment, text string) []message.OB11Segment {
	if text == "" {
		return segs
	}
	if len(segs) > 0 && segs[len(segs)-1].Type == message.SegmentText {
		if prev, ok := segs[len(segs)-1].Data["text"].(string); ok {
			segs[len(segs)-1].Data["text"] = prev + text
			return segs
		}
	}
	return append(segs, message.OB11Segment{Type: message.SegmentText, Data: message.TextMessage{Text: text}.Marshal()})
}

// segmentsPlainText 消息段的纯文本（供 RawMessage 复读判等）。
func segmentsPlainText(segs []message.OB11Segment) string {
	var sb strings.Builder
	for _, s := range segs {
		if s.Type == message.SegmentText {
			if t, ok := s.Data["text"].(string); ok {
				sb.WriteString(t)
			}
		}
	}
	return sb.String()
}
