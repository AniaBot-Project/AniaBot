package discord

import (
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// streamPatchInterval 流式更新节流间隔（同飞书/Telegram）：Discord 消息编辑也有
// 频率限制，过快的增量合并到最近一次编辑，End 时强制发送最终内容。
const streamPatchInterval = 600 * time.Millisecond

// SendGroupStream 实现 adapter.StreamSenderExt：以消息创建流式群聊回复，Patch 经
// ChannelMessageEdit 更新内容。
func (a *discordAdapter) SendGroupStream(groupId message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	channelID, ok := parseChannelID(groupId)
	if !ok {
		return nil, false
	}
	return a.sendStream(channelID, chain.GetGroupMsg())
}

// SendFriendStream 实现 adapter.StreamSenderExt：流式私聊回复（先打开 DM 频道）。
func (a *discordAdapter) SendFriendStream(userId message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	rawUserID, ok := parseChannelID(userId)
	if !ok || a.api == nil {
		return nil, false
	}
	dm, err := a.api.UserChannelCreate(rawUserID)
	if err != nil {
		a.logSendFail("UserChannelCreate", err, "userId", rawUserID)
		return nil, false
	}
	return a.sendStream(dm.ID, chain.GetFriendMsg())
}

// sendStream 创建流式消息：文本段拼接为初始内容，at 段展开为 prefix（"<@id> "
// 文本）；后续 Patch/End 时 prefix 始终重新带上（aichat 的 Patch 只传 AI 增量
// 文本，否则首条消息里的提及会在第一次编辑时消失）。reply 段作为引用回复。
func (a *discordAdapter) sendStream(channelID string, segs []message.OB11Segment) (bot.StreamHandle, bool) {
	if a.api == nil {
		return nil, false
	}
	var textSb, prefixSb strings.Builder
	var replyTo *discordgo.MessageReference
	atAll := false
	for _, s := range segs {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				textSb.WriteString(t)
			}
		case message.SegmentMention:
			prefixSb.WriteString(renderMention(s, &atAll))
		case message.SegmentReply:
			if replyTo == nil {
				if id, ok := s.Data["id"].(string); ok {
					if _, mid, ok2 := parseMsgID(id); ok2 {
						replyTo = &discordgo.MessageReference{MessageID: mid, ChannelID: channelID}
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
	m, err := a.api.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         truncateRunes(text, maxTextLen),
		Reference:       replyTo,
		AllowedMentions: allowedMentions(atAll),
	})
	if err != nil {
		a.logSendFail("ChannelMessageSend", err, "channelId", channelID)
		return nil, false
	}
	// lastPatch 以初始发送时间为起点：紧接初始消息的 Patch 同样受节流约束
	return &discordStreamHandle{a: a, channelID: channelID, msgID: m.ID, prefix: prefix, lastPatch: time.Now()}, true
}

// discordStreamHandle Discord 流式消息句柄：Patch 经 ChannelMessageEdit 更新内容
// （节流）；End 强制最终内容（幂等）。Discord 原生渲染 markdown，中间态编辑无需
// 像 Telegram 那样担心未闭合标记解析失败，无降级路径。
type discordStreamHandle struct {
	a         *discordAdapter
	channelID string
	msgID     string
	// prefix 初始消息中不可丢弃的提及文本：编辑替换整个消息内容，需重新带上
	prefix string

	mu      sync.Mutex
	content string
	// full 已提交的完整内容（prefix + content）：可能超过单条上限，
	// End 时据此补发被截断的尾部。
	full      string
	lastPatch time.Time
	// lastSent 最后一次成功编辑的文本：内容未变化时跳过冗余 API 调用
	lastSent string
	closed   bool
}

// Patch 更新消息内容：距上次成功编辑超过节流间隔时立即发送，否则仅记录最新内容
// （后续 Patch 或 End 时一并发送）。
func (h *discordStreamHandle) Patch(text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.content = text
	if time.Since(h.lastPatch) >= streamPatchInterval {
		return h.patchLocked()
	}
	return nil
}

// End 强制发送最终内容（幂等，结束后不可再 Patch）。
func (h *discordStreamHandle) End() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	_ = h.patchLocked()
	// 编辑无法追加内容：超出单条上限的尾部作为后续消息补发，避免长回复被静默截断
	h.sendOverflowLocked()
	h.closed = true
}

// patchLocked 以当前内容编辑消息；内容与上次成功编辑一致时跳过。调用方需持有 h.mu。
//
// 超出单条上限时只显示前 maxTextLen 个字符（编辑无法追加），剩余内容在 End 时
// 作为后续消息补发（见 sendOverflowLocked）——旧实现直接丢弃尾部，用户看到的是
// 静默截断的半截回答。
func (h *discordStreamHandle) patchLocked() error {
	if h.a == nil || h.a.api == nil || h.msgID == "" {
		return nil
	}
	h.full = h.prefix + h.content
	text := truncateRunes(h.full, maxTextLen)
	if text == h.lastSent {
		return nil
	}
	if _, err := h.a.api.ChannelMessageEdit(h.channelID, h.msgID, text); err != nil {
		h.a.logger.Warn("Discord 流式回复更新失败", "messageId", h.msgID, "error", err)
		return err
	}
	h.lastSent = text
	h.lastPatch = time.Now()
	return nil
}

// sendOverflowLocked 把超出单条上限的剩余内容作为后续消息补发（调用方需持有 h.mu）。
// End 时调用一次；补发失败只记日志（主消息已完整展示开头部分）。
func (h *discordStreamHandle) sendOverflowLocked() {
	if h.a == nil || h.a.api == nil {
		return
	}
	runes := []rune(h.full)
	if len(runes) <= maxTextLen {
		return
	}
	rest := string(runes[maxTextLen:])
	if strings.TrimSpace(rest) == "" {
		return
	}
	if _, ok := h.a.sendText(h.channelID, rest, nil, false); !ok {
		h.a.logger.Warn("Discord 流式回复超长部分补发失败", "channelId", h.channelID, "runes", len(runes))
	}
}
