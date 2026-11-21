package telegram

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// ---------- 消息翻译（入站） ----------

// updateToMessage 将一条 Telegram 消息翻译为框架通用消息。
// 在异步分发 goroutine 内调用（图片下载等 I/O 不阻塞长轮询）。
func (a *telegramAdapter) updateToMessage(m *Message) *message.Message {
	segs := a.messageToSegments(m)
	if len(segs) == 0 {
		return nil
	}
	// 对齐 OneBot v11 消息类型语义：private = 私聊 + sub_type=friend。
	// 私聊不写 GroupId：它是框架里的「群聊」标志（插件据此决定会话键与发送
	// 路由），而 Telegram 私聊的 chat_id 恒等于对端 user_id，按 UserId 即可
	// 寻址；填进去会让私聊被当成群聊（消息日志拆会话、指令按群聊回复）。
	msgType, subType := "group", ""
	var groupID message.QID
	if m.Chat.Type == "private" {
		msgType, subType = "private", "friend"
	} else {
		groupID = message.QID(idPrefix + chatIDRaw(m.Chat.ID))
	}
	userID := ""
	if m.From != nil {
		userID = idPrefix + strconv.FormatInt(m.From.ID, 10)
	} else {
		// 频道消息无发送者：以频道自身为用户（与 GroupId 一致）
		userID = idPrefix + chatIDRaw(m.Chat.ID)
	}
	msg := &message.Message{
		Time:        uint(m.Date),
		PostType:    "message",
		MessageType: msgType,
		SubType:     subType,
		MessageId:   msgID(m.Chat.ID, m.MessageID),
		GroupId:     groupID,
		UserId:      message.QID(userID),
		Message:     segs,
		RawMessage:  segmentsPlainText(segs),
		Sender: message.MessageSender{
			UserId:   message.QID(userID),
			Nickname: nicknameOf(m.From),
		},
		SelfId:   a.selfID(),
		Platform: Platform,
	}
	return msg
}

// messageToSegments 将消息内容翻译为通用消息段：
// reply 段（首段）→ 文本/实体 → 媒体段；未知类型返回 nil 由调用方丢弃。
// 图片段下载为 data URI 写入 url 键（AI 插件的 load_images 只认 url 键）。
func (a *telegramAdapter) messageToSegments(m *Message) []message.OB11Segment {
	if m == nil {
		return nil
	}
	var segs []message.OB11Segment
	if m.ReplyToMessage != nil {
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentReply,
			Data: message.ReplyMessage{Id: msgID(m.ReplyToMessage.Chat.ID, m.ReplyToMessage.MessageID)}.Marshal(),
		})
	}
	// 文本优先取 text，媒体消息取 caption（实体也对应 caption_entities）
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}
	segs = append(segs, a.splitEntities(text, entities)...)
	switch {
	case len(m.Photo) > 0:
		// 同 file_id 的多尺寸中取最大者
		fileID, maxSize := "", -1
		for _, p := range m.Photo {
			if p.FileSize > maxSize {
				fileID, maxSize = p.FileID, p.FileSize
			}
		}
		if fileID != "" {
			segs = append(segs, message.OB11Segment{
				Type: message.SegmentImage,
				Data: message.ImageMessage{File: fileID}.Marshal(),
			})
			// 下载为 data URI 补齐 url 键（失败保留 file 键静默，同飞书策略）
			if uri := a.downloadResource(context.Background(), fileID); uri != "" {
				segs[len(segs)-1].Data["url"] = uri
			}
		}
	case m.Document != nil:
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentFile,
			Data: message.FileMessage{File: m.Document.FileID, FileId: m.Document.FileID, Name: m.Document.FileName}.Marshal(),
		})
	case m.Voice != nil:
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentRecord,
			Data: message.RecordMessage{URL: m.Voice.FileID}.Marshal(),
		})
	case m.Audio != nil:
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentRecord,
			Data: message.RecordMessage{URL: m.Audio.FileID}.Marshal(),
		})
	case m.Video != nil:
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentVideo,
			Data: message.VideoMessage{URL: m.Video.FileID}.Marshal(),
		})
	case m.Sticker != nil:
		segs = appendTextSeg(segs, "[贴纸]")
	case m.Animation != nil:
		segs = appendTextSeg(segs, "[动画]")
	case m.VideoNote != nil:
		segs = appendTextSeg(segs, "[视频消息]")
	}
	return segs
}

// splitEntities 按实体拆分文本：
//   - mention 且 @username 与 bot 自身 username 大小写不敏感匹配 → at 段（qq=SelfId），
//     使 aichat 群聊的 @ 触发（at 段 qq == msg.SelfId）与自提及识别生效——Telegram
//     无 API 由 username 反查 user_id，这是 @ 提及映射为 at 段的唯一途径；
//     其他 @username 保留文本（Telegram 原生渲染为可点击提及）
//   - text_mention（携带 user 对象）→ at 段（qq=tg:<user_id>）
//   - 其余格式实体（bold/code/url/...）保留原文
//
// 实体偏移为 UTF-16 code unit，先转换为字节偏移再切分（CJK/emoji 下字节与 UTF-16 不一致）。
func (a *telegramAdapter) splitEntities(text string, entities []MessageEntity) []message.OB11Segment {
	var segs []message.OB11Segment
	if text == "" {
		return segs
	}
	offs := utf16Offsets(text)
	pos := 0    // 字节偏移游标，仅用于切字符串
	posU16 := 0 // UTF-16 code unit 游标，用于实体重叠/越界检查；
	// 与 e.Offset 单位必须一致——此前复用字节游标 pos 比较，前一个实体含
	// 非 ASCII 字符（CJK/emoji，字节数 > UTF-16 单位数）时，其后所有实体被
	// 误判为重叠而跳过，@bot 提及丢失、群聊 at 触发失效
	for _, e := range entities {
		if e.Offset < posU16 || e.Offset+e.Length > len(offs)-1 {
			continue // 越界/重叠实体（异常数据）跳过
		}
		start, end := offs[e.Offset], offs[e.Offset+e.Length]
		if start > pos {
			segs = appendTextSeg(segs, text[pos:start])
		}
		span := text[start:end]
		switch e.Type {
		case "mention":
			if username := a.selfUsername(); username != "" &&
				strings.EqualFold(span, "@"+username) {
				segs = append(segs, message.OB11Segment{
					Type: message.SegmentMention,
					Data: message.MentionMessage{QQ: a.selfID()}.Marshal(),
				})
			} else {
				segs = appendTextSeg(segs, span)
			}
		case "text_mention":
			if e.User != nil {
				segs = append(segs, message.OB11Segment{
					Type: message.SegmentMention,
					Data: message.MentionMessage{QQ: message.QID(idPrefix + strconv.FormatInt(e.User.ID, 10))}.Marshal(),
				})
			} else {
				segs = appendTextSeg(segs, span)
			}
		default:
			segs = appendTextSeg(segs, span)
		}
		pos = end
		posU16 = e.Offset + e.Length
	}
	return appendTextSeg(segs, text[pos:])
}

// utf16Offsets 计算文本每个 UTF-16 code unit 边界对应的字节偏移。
// Telegram 实体 offset/length 以 UTF-16 code unit 计数（CJK 一字符一单位，
// 增补平面字符一字符两单位），与 Go 字符串的字节下标不一致，需转换。
// 返回 len(utf16units)+1 个偏移：offs[k] 为第 k 个单位起始的字节偏移。
func utf16Offsets(text string) []int {
	offs := []int{0}
	for _, r := range text {
		last := offs[len(offs)-1]
		l := utf8.RuneLen(r)
		offs = append(offs, last+l)
		if r > 0xFFFF {
			offs = append(offs, last+l) // 代理对第二个单位与第一个同字节边界
		}
	}
	return offs
}

// downloadResource 通过 getFile 下载消息文件并转为 data URI（供 AI 插件直接加载）。
func (a *telegramAdapter) downloadResource(ctx context.Context, fileID string) string {
	if a.client == nil || fileID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var f File
	if err := a.client.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		a.logger.Debug("Telegram getFile 失败", "fileId", fileID, "error", err)
		return ""
	}
	if f.FilePath == "" {
		return ""
	}
	// 文件下载端点：{apiBase}/file/bot{token}/{file_path}（与 getFile 同 20s 预算）
	resp, err := a.client.http.R().SetContext(ctx).
		Get(a.client.apiBase + "/file/bot" + a.client.token + "/" + f.FilePath)
	if err != nil || resp.StatusCode() != http.StatusOK {
		a.logger.Debug("Telegram 文件下载失败", "filePath", f.FilePath, "error", err)
		return ""
	}
	// 按实际字节识别 MIME（标签与内容不一致时模型端解码会报 unsupported image），
	// 识别失败再回退 image/png
	mime := "image/png"
	if m, ok := message.SniffImageMIME(resp.Body()); ok {
		mime = m
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(resp.Body())
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

// nicknameOf 用户显示名（first_name + last_name，update 自带，无需 API 查询）。
func nicknameOf(u *User) string {
	if u == nil {
		return ""
	}
	name := u.FirstName
	if u.LastName != "" {
		if name != "" {
			name += " "
		}
		name += u.LastName
	}
	return name
}
