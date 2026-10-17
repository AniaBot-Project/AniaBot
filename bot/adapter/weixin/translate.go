package weixin

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// translateMessage 将 iLink 消息翻译为框架通用消息。
// 在独立 goroutine 内调用（图片下载等 I/O 不阻塞长轮询）。
// iLink bot 仅 bot↔用户 1:1 会话，统一映射为 private/friend。
func (a *weixinAdapter) translateMessage(m *WeixinMessage) *message.Message {
	segs := a.itemsToSegments(m)
	if len(segs) == 0 {
		return nil
	}
	from := strings.TrimSpace(m.FromUserID)
	now := uint(time.Now().Unix())
	if m.CreateTimeMs > 0 {
		now = uint(m.CreateTimeMs / 1000)
	}
	rawID := frameMsgID(from, msgSeqText(m))
	return &message.Message{
		Time:        now,
		PostType:    "message",
		MessageType: "private",
		SubType:     "friend",
		MessageId:   rawID,
		UserId:      frameUserID(from),
		GroupId:     frameUserID(from),
		Message:     segs,
		RawMessage:  segmentsPlainText(segs),
		Sender: message.MessageSender{
			UserId:   frameUserID(from),
			Nickname: from, // iLink 无昵称查询 API，以原始 ID 充当显示名
		},
		SelfId:   a.SelfID(),
		Platform: Platform,
	}
}

// itemsToSegments 消息条目 → 通用消息段：
// 引用（reply 段 + 引用摘要文本）→ 文本/图片/语音/文件/视频；全部不可用时返回 nil。
func (a *weixinAdapter) itemsToSegments(m *WeixinMessage) []message.OB11Segment {
	var segs []message.OB11Segment
	for _, item := range m.ItemList {
		if item == nil {
			continue
		}
		// 引用消息先于正文（reply 段居首的框架惯例）
		if item.RefMsg != nil {
			segs = a.appendRefSegments(segs, m.FromUserID, item.RefMsg)
		}
		switch item.Type {
		case ItemText:
			if item.TextItem != nil && item.TextItem.Text != "" {
				segs = appendTextSeg(segs, item.TextItem.Text)
			}
		case ItemImage:
			seg, ok := a.imageSegment(item.ImageItem)
			if ok {
				segs = append(segs, seg)
			} else {
				segs = appendTextSeg(segs, "[图片]")
			}
		case ItemVoice:
			if item.VoiceItem != nil {
				// 平台侧自带语音转写时直接采用（无需 ASR）；否则无法消费 silk 语音
				if t := strings.TrimSpace(item.VoiceItem.Text); t != "" {
					segs = appendTextSeg(segs, t)
				} else {
					segs = appendTextSeg(segs, "[语音]")
				}
			}
		case ItemFile:
			if item.FileItem != nil {
				segs = append(segs, message.OB11Segment{
					Type: message.SegmentFile,
					Data: message.FileMessage{Name: item.FileItem.FileName}.Marshal(),
				})
			}
		case ItemVideo:
			segs = appendTextSeg(segs, "[视频]")
		}
	}
	return segs
}

// appendRefSegments 引用消息：reply 段（引用条目带稳定 ID 时）+ 引用内容摘要文本。
// 微信回复出站无引用 API，reply 段仅入站表达上下文。
func (a *weixinAdapter) appendRefSegments(segs []message.OB11Segment, fromUserID string, ref *RefMessage) []message.OB11Segment {
	if ref == nil {
		return segs
	}
	if inner := ref.MessageItem; inner != nil && inner.MsgID != "" {
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentReply,
			Data: message.ReplyMessage{Id: frameMsgID(fromUserID, inner.MsgID)}.Marshal(),
		})
	}
	var sb strings.Builder
	if t := strings.TrimSpace(ref.Title); t != "" {
		sb.WriteString(t)
	}
	if sb.Len() > 0 {
		segs = appendTextSeg(segs, "【引用】"+sb.String())
	}
	return segs
}

// imageSegment 图片条目 → image 段：CDN 下载解密为 data URI 写入 url 键
// （AI 插件的 load_images 只认 url 键；与飞书/Telegram 策略一致，失败保留 file 键）。
func (a *weixinAdapter) imageSegment(img *ImageItem) (message.OB11Segment, bool) {
	if img == nil {
		return message.OB11Segment{}, false
	}
	seg := message.OB11Segment{
		Type: message.SegmentImage,
		Data: message.ImageMessage{File: "weixin_image"}.Marshal(),
	}
	media := img.Media
	if media == nil || (media.EncryptQueryParam == "" && media.FullURL == "") {
		media = img.ThumbMedia // 原图不可用时退缩略图
	}
	if media == nil || (media.EncryptQueryParam == "" && media.FullURL == "") {
		return message.OB11Segment{}, false
	}
	// 解密 key：image_item.aeskey（hex）优先，回退 media.aes_key（base64）
	aesKeyB64 := media.AesKey
	if img.Aeskey != "" {
		aesKeyB64 = base64.StdEncoding.EncodeToString([]byte(img.Aeskey))
	}
	c := a.currentClient()
	data, err := a.downloadCdnMedia(c, context.Background(), media, aesKeyB64)
	if err != nil {
		a.logger.Debug("微信图片下载/解密失败", "error", err)
		return message.OB11Segment{}, false
	}
	// 按字节魔数标注 MIME：手机端微信图片绝大多数是 JPEG，一律写成 image/png
	// 会让多模态/OCR 端按内容与标签不符解码失败（飞书侧已有同因教训）。
	seg.Data["url"] = imageDataURI(data)
	return seg, true
}

// imageDataURI 把解密后的图片字节编码为 data URI，MIME 按字节魔数推断；
// 无法识别时退回 image/png（保持历史行为，至少不改变旧有的报错形态）。
// 单独成函数便于测试锁定 MIME 判定（JPEG 曾被硬编码标成 PNG）。
func imageDataURI(data []byte) string {
	mime, ok := message.SniffImageMIME(data)
	if !ok {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
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
