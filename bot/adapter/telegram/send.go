package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// SendGroupMsg 发送群聊消息（chat_id 直发，Telegram 群组/频道 ID 均为负数）。
func (a *telegramAdapter) SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (message.QID, bool) {
	return a.sendToChat(groupId, chain.GetGroupMsg())
}

// SendFriendMsg 发送私聊消息（私聊 chat_id 恒等于对端 user_id，直接使用）。
func (a *telegramAdapter) SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (message.QID, bool) {
	return a.sendToChat(userId, chain.GetFriendMsg())
}

func (a *telegramAdapter) sendToChat(target message.QID, segs []message.OB11Segment) (message.QID, bool) {
	chatID, ok := parseChatID(target)
	if !ok || a.client == nil {
		return "", false
	}
	return a.sendChain(context.Background(), chatID, segs)
}

// parseChatID 解析 "tg:<chat_id>"；非 tg: 前缀（如 QQ 的 qq: 数字 ID）返回 ok=false，
// 避免把其他平台/默认平台的 ID 误当作本平台目标。
func parseChatID(q message.QID) (int64, bool) {
	s := q.String()
	if !strings.HasPrefix(s, idPrefix) {
		return 0, false
	}
	raw := strings.TrimPrefix(s, idPrefix)
	if raw == "" {
		return 0, false
	}
	chatID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return chatID, true
}

// sendChain 把通用消息段翻译为 Telegram 消息序列并发送。
// 文本按段顺序累积，媒体穿插发送；与媒体相邻的短文本（≤1024 字节）作媒体 caption
// （Telegram 媒体说明上限），其余单独发消息；首条消息携带 reply_parameters。
// keyboard 段（至多取首个）翻译为 reply_markup，在正文发送完成后经
// editMessageReplyMarkup 附加到最后一条消息（对文本/媒体消息均有效）。
// 返回最后成功消息的框架内 ID（tg:<chat>:<msgid>）。
func (a *telegramAdapter) sendChain(ctx context.Context, chatID int64, segs []message.OB11Segment) (message.QID, bool) {
	if a.client == nil {
		return "", false
	}
	// 键盘段先提取（不进入正文发送分支），正文发送完再附加
	segs, kb := message.ExtractKeyboard(segs)
	// 提取回复目标（首条 reply 段，非 tg: 前缀忽略），其余段作为正文
	var replyTo *int
	body := make([]message.OB11Segment, 0, len(segs))
	for _, s := range segs {
		if s.Type == message.SegmentReply && replyTo == nil {
			if id, ok := s.Data["id"].(string); ok {
				if _, mid, ok2 := parseMsgID(id); ok2 {
					replyTo = &mid
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
	var lastMsgID int
	sentAny := false
	// 发送一条文本消息（首条携带 reply，之后清空）
	sendBuffered := func() {
		if text.Len() == 0 {
			return
		}
		t := text.String()
		text.Reset()
		id, ok := a.sendText(ctx, chatID, t, replyTo)
		if ok {
			lastMsgID = id
			sentAny = true
			replyTo = nil
		}
	}

	for _, s := range body {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				text.WriteString(t)
			}
		case message.SegmentMention:
			text.WriteString(a.resolveMention(ctx, chatID, s))
		case message.SegmentImage, message.SegmentFile, message.SegmentRecord, message.SegmentVideo:
			caption := text.String()
			text.Reset()
			// 短文本作 caption（≤1024 字节，Telegram 媒体说明上限），
			// 长文本先单独发送，媒体不带 caption
			if caption != "" && len(caption) <= 1024 {
				if id, ok := a.sendMediaSegment(ctx, chatID, s, caption, replyTo); ok {
					lastMsgID = id
					sentAny = true
					replyTo = nil
				}
			} else {
				if caption != "" {
					if id, ok := a.sendText(ctx, chatID, caption, replyTo); ok {
						lastMsgID = id
						sentAny = true
						replyTo = nil
					}
				}
				if id, ok := a.sendMediaSegment(ctx, chatID, s, "", nil); ok {
					lastMsgID = id
					sentAny = true
				}
			}
		default:
			// 不支持的段（face/json/music/forward）：有 text 键时退化为文本
			if t, ok := s.Data["text"].(string); ok {
				text.WriteString(t)
			} else {
				a.logger.Debug("忽略 Telegram 不支持的通用消息段", "segment", s.Type)
			}
		}
	}
	sendBuffered()
	if !sentAny {
		return "", false
	}
	if kb != nil {
		// 附加键盘（best-effort：失败仅告警，正文已发出）
		a.attachReplyMarkup(chatID, lastMsgID, kb)
	}
	a.cacheSent(chatID, lastMsgID, body)
	return msgID(chatID, lastMsgID), true
}

// sendText 发送文本消息。默认 plain text（不设 parse_mode——AI 输出为 markdown
// 源码，HTML/MarkdownV2 解析会在不完整标记时失败，纯文本最稳）；
// 配置 bot.telegram.parse_mode=html/markdown/markdownv2 时按模式转换/携带
// parse_mode 发送（html 模式先经 renderText 转换），解析失败（400，未转义特殊
// 字符/截断切断标记等）自动降级纯文本重发（还原未转换的原文）。
// 超过单条上限分包发送。
func (a *telegramAdapter) sendText(ctx context.Context, chatID int64, text string, replyTo *int) (int, bool) {
	if a.client == nil {
		return 0, false
	}
	parts := splitText(text)
	if len(parts) == 0 {
		return 0, false
	}
	last := 0
	for i, part := range parts {
		params := map[string]any{"chat_id": chatID, "text": part}
		if replyTo != nil && i == 0 {
			params["reply_parameters"] = map[string]any{"message_id": *replyTo}
		}
		if pm := a.parseMode(); pm != "" {
			params["parse_mode"] = pm
			params["text"] = a.renderText(part)
		}
		var res messageSendResult
		send := func(p map[string]any) error {
			// 降级纯文本重发：还原未转换的原文（避免把 HTML 标签当文本发出）
			if _, has := p["parse_mode"]; !has {
				p["text"] = part
			}
			// 429 限流在调用内重试一次
			_, err := retryOnce(ctx, func() (int, error) {
				if err := a.client.call(ctx, "sendMessage", p, &res); err != nil {
					return 0, err
				}
				return res.MessageID, nil
			})
			return err
		}
		// 400 解析失败→纯文本重发；网关异常响应（code 0）→原样重试一次
		if err := retryAPIError(ctx, params, send); err != nil {
			a.logSendFail("sendMessage", err, "chatId", chatID)
			return last, last > 0 // 部分成功也上报最后成功消息
		}
		last = res.MessageID
	}
	return last, true
}

// sendMediaSegment 发送一个媒体段：按段型映射方法名与文件键。
func (a *telegramAdapter) sendMediaSegment(ctx context.Context, chatID int64, s message.OB11Segment, caption string, replyTo *int) (int, bool) {
	method, fileKey, fileName := mediaMethodOf(s)
	if method == "" || fileKey == "" {
		return 0, false
	}
	return a.sendMedia(ctx, method, chatID, fileKey, fileName, caption, replyTo)
}

// mediaMethodOf 段型 → (Telegram 方法名, 文件源, 上传文件名)。
// file 段指向图片文件时（file 工具等来源）转 sendPhoto 走图片通道内联展示，
// 而不是 sendDocument 附件。
func mediaMethodOf(s message.OB11Segment) (method, fileKey, fileName string) {
	switch s.Type {
	case message.SegmentImage:
		method, fileName = "sendPhoto", "photo.jpg"
		fileKey, _ = s.Data["url"].(string)
		if fileKey == "" {
			fileKey, _ = s.Data["file"].(string)
		}
	case message.SegmentFile:
		if imgData, ok := message.FileSegmentAsImage(s); ok {
			method, fileName = "sendPhoto", "photo.jpg"
			if n, ok := s.Data["name"].(string); ok && n != "" {
				fileName = n
			}
			fileKey, _ = imgData["file"].(string)
			return method, fileKey, fileName
		}
		method, fileName = "sendDocument", "file"
		fileKey, _ = s.Data["file"].(string)
		if n, ok := s.Data["name"].(string); ok && n != "" {
			fileName = n
		}
	case message.SegmentRecord:
		// RecordMessage.Marshal 只写 file 键（见 messagesegment.go）
		method, fileName = "sendVoice", "voice.ogg"
		fileKey, _ = s.Data["file"].(string)
	case message.SegmentVideo:
		method, fileName = "sendVideo", "video.mp4"
		fileKey, _ = s.Data["url"].(string)
		if fileKey == "" {
			fileKey, _ = s.Data["file"].(string)
		}
	}
	return method, fileKey, fileName
}

// mediaFields 各媒体方法的文件字段名（Telegram 要求方法专属字段名）。
var mediaFields = map[string]string{
	"sendPhoto":    "photo",
	"sendDocument": "document",
	"sendVoice":    "voice",
	"sendAudio":    "audio",
	"sendVideo":    "video",
}

// sendMedia 发送媒体消息。fileKey 为文件源：http(s) URL → 表单直发（Telegram
// 服务端抓取，无需上传）；base64:// / data: / file:// → multipart 上传；其余视为
// file_id 原样透传。caption 为空串时不带该参数。
func (a *telegramAdapter) sendMedia(ctx context.Context, method string, chatID int64, fileKey, fileName, caption string, replyTo *int) (int, bool) {
	if a.client == nil {
		return 0, false
	}
	field, ok := mediaFields[method]
	if !ok {
		return 0, false
	}
	form := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if replyTo != nil {
		form["reply_parameters"] = `{"message_id":` + strconv.Itoa(*replyTo) + `}`
	}
	if caption != "" {
		form["caption"] = caption
	}
	var upload *telegramUpload
	if isUploadSource(fileKey) {
		data, ok := resolveSegmentBytes(ctx, a.client.http, fileKey)
		if !ok {
			a.logger.Warn("Telegram 媒体资源解析失败", "method", method)
			return 0, false
		}
		upload = &telegramUpload{Field: field, FileName: fileName, Reader: bytes.NewReader(data)}
	} else {
		form[field] = fileKey
	}
	var res messageSendResult
	id, err := retryOnce(ctx, func() (int, error) {
		if err := a.client.callMultipart(ctx, method, form, upload, &res); err != nil {
			return 0, err
		}
		return res.MessageID, nil
	})
	if err != nil {
		a.logSendFail(method, err, "chatId", chatID)
		return 0, false
	}
	return id, true
}

// resolveMention at 段 → "@username " 文本：经 getChatMember 解析（结果缓存 10 分钟）。
// Telegram 无按 ID @ 的 API，@ 只能以 @username 文本形式；私聊（chat_id 为正数）
// 无群成员概念、解析失败或无 username 时静默丢弃（回复仍以 reply 线程关联）。
func (a *telegramAdapter) resolveMention(ctx context.Context, chatID int64, s message.OB11Segment) string {
	qq, _ := s.Data["qq"].(string)
	if qq == "" || qq == "all" || chatID > 0 {
		return ""
	}
	userID, err := strconv.ParseInt(strings.TrimPrefix(qq, idPrefix), 10, 64)
	if err != nil {
		return ""
	}
	key := chatIDRaw(chatID) + ":" + strconv.FormatInt(userID, 10)
	if v, ok := a.chatMemberCache.Load(key); ok {
		if mc := v.(mentionCache); time.Since(mc.at) < 10*time.Minute {
			if mc.username != "" {
				return "@" + mc.username + " "
			}
			return ""
		}
	}
	if a.client == nil {
		return ""
	}
	var res ChatMember
	if err := a.client.call(ctx, "getChatMember", map[string]any{"chat_id": chatID, "user_id": userID}, &res); err != nil {
		a.logger.Debug("Telegram getChatMember 失败", "chatId", chatID, "userId", userID, "error", err)
		return ""
	}
	mc := mentionCache{username: res.User.Username, at: time.Now()}
	a.chatMemberCache.Store(key, mc)
	if mc.username != "" {
		return "@" + mc.username + " "
	}
	return ""
}

// splitText 按 Telegram 单条消息上限（4096 字符）分包。
// 按 UTF-16 code unit 计数（与 Telegram 的字符计数一致，CJK 一字符一单位、
// 增补平面一字符两单位），保留 rune 完整性。
func splitText(text string) []string {
	const limit = 4090 // 留余量
	runes := []rune(text)
	var parts []string
	start, units := 0, 0
	for i, r := range runes {
		u := 1
		if r > 0xFFFF {
			u = 2
		}
		if units+u > limit {
			parts = append(parts, string(runes[start:i]))
			start = i
			units = 0
		}
		units += u
	}
	return append(parts, string(runes[start:]))
}

// isUploadSource 判断文件源是否需要本地上传（否则按 file_id/URL 透传）。
func isUploadSource(s string) bool {
	return strings.HasPrefix(s, "base64://") || strings.HasPrefix(s, "data:") || strings.HasPrefix(s, "file://")
}

// resolveSegmentBytes 从 base64://、file://、data: URI 解析出字节内容。
// http(s) URL 不在此路径——直接作为 URL 交给 Telegram 服务端抓取。
func resolveSegmentBytes(ctx context.Context, rc *resty.Client, fileStr string) ([]byte, bool) {
	switch {
	case strings.HasPrefix(fileStr, "base64://"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(fileStr, "base64://"))
		return b, err == nil && len(b) > 0
	case strings.HasPrefix(fileStr, "file://"):
		b, err := os.ReadFile(strings.TrimPrefix(fileStr, "file://"))
		return b, err == nil && len(b) > 0
	case strings.HasPrefix(fileStr, "data:"):
		if _, after, ok := strings.Cut(fileStr, ","); ok {
			b, err := base64.StdEncoding.DecodeString(after)
			return b, err == nil && len(b) > 0
		}
	}
	return nil, false
}

// cacheSent 记录出站消息到内存缓存（GetMsgDetail/历史兜底）。
// 出站消息先剔除内联 base64/data 负载再入缓存，避免大图常驻内存。
func (a *telegramAdapter) cacheSent(chatID int64, messageID int, segs []message.OB11Segment) {
	msgType := "group"
	if chatID > 0 {
		msgType = "private"
	}
	// 与入站保持一致：私聊（chat_id 即 user_id）不写 GroupId，避免下游把私聊
	// 当成群聊消息
	var groupID message.QID
	if msgType != "private" {
		groupID = message.QID(idPrefix + chatIDRaw(chatID))
	}
	msg := message.Message{
		Time:        uint(time.Now().Unix()),
		PostType:    "message",
		MessageType: msgType,
		MessageId:   msgID(chatID, messageID),
		GroupId:     groupID,
		Message:     message.StripInlinePayloadSegments(segs),
		RawMessage:  segmentsPlainText(segs),
		SelfId:      a.selfID(),
		Platform:    Platform,
	}
	a.msgCache.Push(chatIDRaw(chatID), msg)
}

// logSendFail 记录 Telegram API 调用失败详情。
func (a *telegramAdapter) logSendFail(op string, err error, kv ...any) {
	args := append([]any{"op", op, "error", err}, kv...)
	a.logger.Warn("Telegram "+op+" 失败", args...)
}
