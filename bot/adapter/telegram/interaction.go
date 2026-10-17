package telegram

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// 本文件实现 Telegram 的内联按钮交互能力（adapter.InteractiveExt /
// adapter.InteractionAnswerer / adapter.MsgEditorExt 的适配器侧实现）：
// 出站 keyboard 段 → reply_markup.inline_keyboard；入站 callback_query →
// message.InteractionEvent 经 TriggerWrapper.OnInteraction 上报，
// core 按回调数据前缀路由给对应插件，返回后回调 AnswerInteraction 应答。

// 编译期断言：可选能力接口的方法集必须齐全——这些接口按断言探测，
// 漏实现不报编译错误、只会静默退化为文本模式（core 也会剥掉按钮段）。
var (
	_ adapter.InteractiveExt      = (*telegramAdapter)(nil)
	_ adapter.MsgEditorExt        = (*telegramAdapter)(nil)
	_ adapter.InteractionAnswerer = (*telegramAdapter)(nil)
)

// SupportsKeyboard 实现 adapter.InteractiveExt：支持在消息中渲染内联按钮。
func (a *telegramAdapter) SupportsKeyboard() bool { return true }

// handleCallbackQuery 内联按钮点击 → InteractionEvent。
// 按钮所在消息不可达（过旧/inline 模式）时无法定位会话，直接应答失效提示。
func (a *telegramAdapter) handleCallbackQuery(cq *CallbackQuery) {
	trig := a.triggerOf()
	if trig.OnInteraction == nil {
		return
	}
	if cq.Message == nil {
		a.answerCallback(cq.ID, "该按钮已失效")
		return
	}
	ev := message.InteractionEvent{
		Platform:   Platform,
		UserId:     message.QID(idPrefix + strconv.FormatInt(cq.From.ID, 10)),
		MessageId:  msgID(cq.Message.Chat.ID, cq.Message.MessageID),
		CallbackId: cq.ID,
		Data:       cq.Data,
		Raw:        cq,
	}
	if cq.Message.Chat.Type == "private" {
		ev.MessageType = "private"
	} else {
		ev.MessageType = "group"
		ev.GroupId = message.QID(idPrefix + chatIDRaw(cq.Message.Chat.ID))
	}
	trig.OnInteraction(ev)
}

// AnswerInteraction 实现 adapter.InteractionAnswerer：answerCallbackQuery
// 消除点击者客户端的等待转圈，text 非空时以 toast 形式展示（上限 200 字符）。
func (a *telegramAdapter) AnswerInteraction(callbackId, text string) bool {
	return a.answerCallback(callbackId, text)
}

// answerCallback 应答回调（best-effort：失败仅记日志，不影响主流程）。
func (a *telegramAdapter) answerCallback(callbackId, text string) bool {
	if a.client == nil || callbackId == "" {
		return false
	}
	params := map[string]any{"callback_query_id": callbackId}
	if text != "" {
		params["text"] = truncateText(text, 200)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.client.call(ctx, "answerCallbackQuery", params, nil); err != nil {
		a.logger.Warn("Telegram answerCallbackQuery 失败", "error", err)
		return false
	}
	return true
}

// tgInlineButton Telegram 内联按钮（callback_data 与 url 二选一）。
type tgInlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// tgReplyMarkup Bot API 的 reply_markup 参数（仅用 inline_keyboard）。
type tgReplyMarkup struct {
	InlineKeyboard [][]tgInlineButton `json:"inline_keyboard"`
}

// buildReplyMarkup 把框架键盘翻译为 reply_markup JSON；无有效按钮行返回空串
// （空串时不携带该参数）。回调数据超长（Telegram 上限 64 字节）的按钮被丢弃，
// 避免 Bot API 400 拒绝整个键盘。
func (a *telegramAdapter) buildReplyMarkup(kb *message.KeyboardMessage) string {
	if kb == nil || len(kb.Rows) == 0 {
		return ""
	}
	m := tgReplyMarkup{}
	dropped := 0
	for _, row := range kb.Rows {
		tr := make([]tgInlineButton, 0, len(row))
		for _, b := range row {
			switch {
			case b.URL != "":
				tr = append(tr, tgInlineButton{Text: b.Text, URL: b.URL})
			case b.Data != "" && len(b.Data) <= 64:
				tr = append(tr, tgInlineButton{Text: b.Text, CallbackData: b.Data})
			default:
				dropped++
			}
		}
		if len(tr) > 0 {
			m.InlineKeyboard = append(m.InlineKeyboard, tr)
		}
	}
	if dropped > 0 {
		a.logger.Warn("Telegram 按钮回调数据超 64 字节被丢弃", "count", dropped)
	}
	if len(m.InlineKeyboard) == 0 {
		return ""
	}
	bs, err := json.Marshal(&m)
	if err != nil {
		return ""
	}
	return string(bs)
}

// EditGroupMsg 实现 adapter.MsgEditor：editMessageText 更新已发送消息的文本
// 内容，chain 携带 keyboard 段时同时更换按钮（未携带则保持原按钮）。
// 媒体消息（无文本可编辑）编辑失败返回 false，由插件降级为发送新消息。
func (a *telegramAdapter) EditGroupMsg(msgId message.QID, chain msgchain.GroupChain) bool {
	return a.editMsg(msgId, chain.GetGroupMsg())
}

// EditFriendMsg 实现 adapter.MsgEditor：编辑已发送的私聊消息。
func (a *telegramAdapter) EditFriendMsg(msgId message.QID, chain msgchain.FriendChain) bool {
	return a.editMsg(msgId, chain.GetFriendMsg())
}

// editMsg 编辑消息正文：text/at 段拼接新内容（at 经 getChatMember 解析为
// @username，与发送路径一致）；reply 段忽略；keyboard 段提取为 reply_markup。
// 配置 parse_mode 时按模式渲染，解析失败（400）降级纯文本重发（同 sendText）；
// 内容与现状一致（"message is not modified"）视为成功。
func (a *telegramAdapter) editMsg(msgId message.QID, segs []message.OB11Segment) bool {
	chatID, mid, ok := parseMsgID(msgId.String())
	if !ok || a.client == nil {
		return false
	}
	rest, kb := message.ExtractKeyboard(segs)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var text strings.Builder
	for _, s := range rest {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				text.WriteString(t)
			}
		case message.SegmentMention:
			text.WriteString(a.resolveMention(ctx, chatID, s))
		}
	}
	if text.Len() == 0 && kb == nil {
		return false
	}
	params := map[string]any{
		"chat_id":    chatID,
		"message_id": mid,
	}
	if raw := text.String(); raw != "" {
		params["text"] = truncateText(raw, maxEditTextLen)
		if pm := a.parseMode(); pm != "" {
			params["parse_mode"] = pm
			params["text"] = a.renderText(raw)
		}
	}
	if kb != nil {
		if markup := a.buildReplyMarkup(kb); markup != "" {
			params["reply_markup"] = markup
		}
	}
	err := retryAPIError(ctx, params, func(p map[string]any) error {
		if _, has := p["parse_mode"]; !has {
			if raw := text.String(); raw != "" {
				p["text"] = truncateText(raw, maxEditTextLen) // 降级纯文本：还原原文
			}
		}
		callErr := a.client.call(ctx, "editMessageText", p, nil)
		if callErr != nil && strings.Contains(callErr.Error(), "message is not modified") {
			return nil // 内容已是现状，视为成功
		}
		return callErr
	})
	if err != nil {
		a.logSendFail("editMessageText", err, "chatId", chatID, "messageId", mid)
		return false
	}
	return true
}

// attachReplyMarkup 给已发送的消息附加/更换内联键盘（editMessageReplyMarkup，
// 对文本与媒体消息均有效）。sendChain 在链中携带 keyboard 段时对最后一条
// 已发出消息调用（best-effort：失败仅告警，消息本体已发出）。
func (a *telegramAdapter) attachReplyMarkup(chatID int64, messageID int, kb *message.KeyboardMessage) bool {
	markup := a.buildReplyMarkup(kb)
	if markup == "" || a.client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	params := map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": markup,
	}
	err := retryAPIError(ctx, params, func(p map[string]any) error {
		return a.client.call(ctx, "editMessageReplyMarkup", p, nil)
	})
	if err != nil {
		a.logSendFail("editMessageReplyMarkup", err, "chatId", chatID, "messageId", messageID)
		return false
	}
	return true
}
