package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

const (
	// streamPatchInterval 流式更新节流间隔（同飞书）：Telegram 消息编辑也有频率限制，
	// 过快的增量合并到最近一次编辑，End 时强制发送最终内容。
	streamPatchInterval = 600 * time.Millisecond
	// maxEditTextLen Telegram 单条消息内容上限（编辑与发送相同）。
	maxEditTextLen = 4096
)

// SendGroupStream 实现 adapter.StreamSenderExt：以消息创建流式群聊回复，Patch 经
// editMessageText 更新内容。
func (a *telegramAdapter) SendGroupStream(groupId message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	return a.sendStream(groupId, chain.GetGroupMsg())
}

// SendFriendStream 实现 adapter.StreamSenderExt：流式私聊回复。
func (a *telegramAdapter) SendFriendStream(userId message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	return a.sendStream(userId, chain.GetFriendMsg())
}

// sendStream 创建流式消息：文本段拼接为初始内容，@ 段经 resolveMention 展开为
// prefix（"@username " 文本）；后续 Patch/End 时 prefix 始终重新带上（aichat 的
// Patch 只传 AI 增量文本，否则首条消息里的 @username 会在第一次编辑时消失）。
// reply 段作为回复目标（reply_parameters）。
func (a *telegramAdapter) sendStream(target message.QID, segs []message.OB11Segment) (bot.StreamHandle, bool) {
	chatID, ok := parseChatID(target)
	if !ok || a.client == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var textSb, prefixSb strings.Builder
	var replyTo *int
	for _, s := range segs {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				textSb.WriteString(t)
			}
		case message.SegmentMention:
			prefixSb.WriteString(a.resolveMention(ctx, chatID, s))
		case message.SegmentReply:
			if replyTo == nil {
				if id, ok := s.Data["id"].(string); ok {
					if _, mid, ok2 := parseMsgID(id); ok2 {
						replyTo = &mid
					}
				}
			}
		}
	}
	prefix := prefixSb.String()
	text := prefix + textSb.String()
	if text == "" {
		return nil, false
	}
	text = truncateText(text, maxEditTextLen)
	id, ok := a.sendText(ctx, chatID, text, replyTo)
	if !ok {
		return nil, false
	}
	return &telegramStreamHandle{a: a, chatID: chatID, msgID: id, prefix: prefix}, true
}

// telegramStreamHandle Telegram 流式消息句柄：Patch 经 editMessageText 更新内容
// （节流）；End 强制最终内容（幂等），配置 parse_mode 时最终编辑尝试渲染，
// 失败（未转义特殊字符等 400）降级纯文本重发保证最终内容落地。
type telegramStreamHandle struct {
	a      *telegramAdapter
	chatID int64
	msgID  int
	// prefix 初始消息中不可丢弃的 @username 文本：编辑替换整个消息内容，需重新带上
	prefix string

	mu        sync.Mutex
	content   string
	lastPatch time.Time
	// lastSent 最后一次成功编辑的文本：纯文本编辑且内容未变化时跳过
	// （Telegram 拒绝未变化的编辑，400 "message is not modified"）；
	// 带 parse_mode 的最终编辑不适用——渲染生成的实体使消息内容变化
	lastSent string
	closed   bool
}

// Patch 更新消息内容：距上次成功编辑超过节流间隔时立即发送，否则仅记录最新内容
// （后续 Patch 或 End 时一并发送）。流式中间编辑始终纯文本（流式过程标记不完整，
// 带 parse_mode 会因未闭合标记被拒）。
func (h *telegramStreamHandle) Patch(text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.content = text
	if time.Since(h.lastPatch) >= streamPatchInterval {
		return h.patchLocked(false)
	}
	return nil
}

// End 强制发送最终内容（幂等，结束后不可再 Patch）。
func (h *telegramStreamHandle) End() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	_ = h.patchLocked(true)
	h.closed = true
}

// errAlreadyShown 降级重发的内容与已展示的纯文本一致：重发必被拒（400
// "message is not modified"），消息本就完整，跳过无意义的重发。
var errAlreadyShown = errors.New("telegram: 内容已展示，跳过重发")

// patchLocked 以当前内容编辑消息；final 为 true 时按配置的 parse_mode 渲染
// （html 模式先经 renderText 把 AI markdown 转换为 Telegram HTML；仅最终内容
// 完整时才可能渲染成功，参考 aiogram 流式写法：流式阶段纯文本、最终编辑无条件
// 应用渲染——即使内容与最后一条纯文本一致也要尝试，渲染生成的实体使消息内容
// 变化），解析失败降级纯文本重发（还原未转换的原文），网关异常响应（code 0）
// 原样重试一次。纯文本编辑且内容与上次成功编辑一致时跳过。
// 调用方需持有 h.mu。
func (h *telegramStreamHandle) patchLocked(final bool) error {
	if h.a == nil || h.a.client == nil || h.msgID == 0 {
		return nil
	}
	raw := truncateText(h.prefix+h.content, maxEditTextLen)
	pm := ""
	text := raw
	if final {
		pm = h.a.parseMode()
		text = h.a.renderText(raw)
	}
	// 纯文本且内容未变化：跳过（必然 "message is not modified"）。
	// 带 parse_mode 的最终编辑不在此列——实体变化时 Telegram 接受编辑。
	if pm == "" && raw == h.lastSent {
		return nil
	}
	params := map[string]any{
		"chat_id":    h.chatID,
		"message_id": h.msgID,
		"text":       text,
	}
	if pm != "" {
		params["parse_mode"] = pm
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 400 解析失败→纯文本重发（还原原文）；网关异常响应（code 0）→原样重试一次。
	// 降级重发的内容若已以纯文本展示过则跳过（重发必被拒，消息本就完整）。
	err := retryAPIError(ctx, params, func(p map[string]any) error {
		if _, has := p["parse_mode"]; !has {
			if raw == h.lastSent {
				return errAlreadyShown
			}
			p["text"] = raw // 降级纯文本：还原未转换的原文（避免发出 HTML 标签）
		}
		return h.a.client.call(ctx, "editMessageText", p, nil)
	})
	if errors.Is(err, errAlreadyShown) {
		return nil
	}
	if err != nil {
		h.a.logger.Warn("Telegram 流式回复更新失败", "messageId", h.msgID, "error", err)
		return err
	}
	h.lastSent = raw
	h.lastPatch = time.Now()
	return nil
}

// truncateText 按 Telegram 的计量单位（UTF-16 code unit）截断，不切断多字节字符。
//
// Telegram 对消息文本的上限是 4096 个 UTF-16 单元（emoji 等补充平面字符占 2 个），
// 因此不能按字节截断：中文每字 3 字节，按字节计只保留约 1365 字，中文长回复会在
// 远未达到平台上限时被静默砍掉。计量口径与发送路径的 splitText 保持一致。
func truncateText(s string, maxUnits int) string {
	if maxUnits <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		u := 1
		if r > 0xFFFF {
			u = 2
		}
		if units+u > maxUnits {
			return s[:i]
		}
		units += u
	}
	return s
}
