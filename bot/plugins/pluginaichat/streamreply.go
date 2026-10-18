package pluginaichat

import (
	"log/slog"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// maxStreamPatchFails 连续 Patch 失败多少次后放弃流式、退化为一次性发送。
// 平台撤回消息/限流/编辑被拒时 Patch 会持续失败，而成功路径只以「是否已流式
// 发出」为条件直接返回，不再走一次性兜底——用户会只看到半截内容。
const maxStreamPatchFails = 3

// streamReply 一次 AI 请求的流式回复状态机（平台支持「先发后改」时启用）。
//
// 全程只维持一条流式消息：每个工具轮的文本各自从零累积，整体覆盖这条消息
// （Patch 为覆盖语义，打字机式重写），不按轮新建消息——否则中间轮的草稿会与
// 最终回复各占一条消息，看起来像「回复了两次」。工具边界只清空累积缓冲
// （OnRoundEnd），消息本身留着继续被下一轮覆盖，收尾时再 End。
//
// 平台不支持或连续 Patch 失败时置 unsupported，后续增量全部忽略，由调用方走
// 一次性发送补全整段回复。
type streamReply struct {
	sender   bot.StreamSender
	logger   *slog.Logger
	target   message.QID
	isGroup  bool
	mentions []message.QID

	handle bot.StreamHandle
	// buf 当前轮已累积的文本：工具边界清空，让下一轮从头覆盖消息内容
	buf strings.Builder
	// streaming 已成功创建流式消息并处于流式展示中（收尾据此跳过一次发送）
	streaming bool
	// unsupported 平台不支持流式或 Patch 连续失败：后续增量忽略
	unsupported bool
	// patchFails 累计 Patch 失败次数（达到 maxStreamPatchFails 放弃流式）
	patchFails int
	// mentionPending 群聊首条消息尚未创建：创建时携带本批全部发言者的 @ 提及
	mentionPending bool
}

// newStreamReply 构造流式回复状态机：mentions 为本批发言者去重后的 @ 提及列表
// （群聊生效，私聊忽略）。
func newStreamReply(sender bot.StreamSender, logger *slog.Logger, target message.QID, isGroup bool, mentions []message.QID) *streamReply {
	return &streamReply{
		sender:         sender,
		logger:         logger,
		target:         target,
		isGroup:        isGroup,
		mentions:       mentions,
		mentionPending: isGroup,
	}
}

// newStreamReplyFor 构造本次请求的流式状态机：流式开关关闭、或平台未实现
// bot.StreamSender（QQ/OneBot v11 无消息编辑 API）时返回 nil，调用方走一次性发送。
//
// 与「按空行拆分回复」无关：拆分只作用于非流式发送（流式是单条消息逐字更新，
// 本身无法拆成多条），两个开关可同时开启、互不影响。
func (p *AIChatPlugin) newStreamReplyFor(b bot.Bot, id message.QID, isGroup bool, batch []message.Message) *streamReply {
	if !p.cfg.Stream.Enable {
		return nil
	}
	ss, ok := b.(bot.StreamSender)
	if !ok {
		return nil
	}
	return newStreamReply(ss, p.Logger, id, isGroup, batchMentions(batch))
}

// batchMentions 本批消息发言者去重后的 @ 提及列表（保持发言先后顺序）。
// 跳过无发送者的合成消息（如子代理结果回灌），避免 @ 到无效用户。
// 流式首条消息与一次性发送路径共用同一份提及集。
func batchMentions(batch []message.Message) []message.QID {
	seen := make(map[message.QID]struct{}, len(batch))
	mentions := make([]message.QID, 0, len(batch))
	for i := range batch {
		uid := batch[i].Sender.UserId
		if uid == message.FromUint64(0) {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		mentions = append(mentions, uid)
	}
	return mentions
}

// OnDelta 收到一个流式文本增量：累积后 Patch 到消息上；首轮首个增量负责创建
// 消息（群聊携带 @ 提及）。未去除  thinking 块的原始增量在缓冲上统一处理。
func (s *streamReply) OnDelta(delta string) {
	if delta == "" || s.unsupported {
		return
	}
	s.buf.WriteString(delta)
	text := aichat.RemoveThinkContent(s.buf.String())
	if s.handle != nil {
		if err := s.handle.Patch(text); err != nil {
			s.patchFails++
			if s.patchFails >= maxStreamPatchFails {
				// 放弃流式：结束当前消息并标记为不支持，后续增量忽略，
				// 由调用方的一次性发送路径补全完整回复
				s.logger.Warn("流式回复更新连续失败，退化为一次性发送",
					"id", s.target, "is_group", s.isGroup, "fails", s.patchFails, "error", err.Error())
				s.handle.End()
				s.handle = nil
				s.streaming = false
				s.unsupported = true
			}
		}
		return
	}
	// 首个增量：创建流式消息
	if s.isGroup {
		builder := msgchain.Builder().Group()
		if s.mentionPending {
			s.mentionPending = false
			for _, uid := range s.mentions {
				builder.Mention(uid)
			}
			text = " " + text
		}
		builder.Text(text)
		s.handle, s.streaming = s.sender.SendGroupStream(s.target, builder.Build())
	} else {
		builder := msgchain.Builder().Friend()
		builder.Text(text)
		s.handle, s.streaming = s.sender.SendFriendStream(s.target, builder.Build())
	}
	if !s.streaming {
		// 平台不支持流式：忽略后续增量，走一次性兜底
		s.unsupported = true
		s.handle = nil
	}
}

// OnRoundEnd 工具调用轮结束：只清空累积缓冲，保持同一条流式消息——下一轮文本
// 从零累积并整体覆盖该消息内容（中间轮文本先展示，随后被后续轮次覆盖）。
func (s *streamReply) OnRoundEnd() {
	s.buf.Reset()
}

// OnRestart 流式中途失败后重试/切换备用模型前：清空已展示缓冲，让重试从头生成
// 的内容整体覆盖旧输出（Patch 为覆盖语义，不会拼接重复文本）。
func (s *streamReply) OnRestart() {
	s.buf.Reset()
}

// End 请求收尾：结束流式消息（幂等）。是否已流式发出内容由 Streaming 判断。
func (s *streamReply) End() {
	if s.handle != nil {
		s.handle.End()
		s.handle = nil
	}
}

// Streaming 是否已把回复流式展示在消息上：为 true 时调用方不再走一次性发送。
func (s *streamReply) Streaming() bool {
	return s.streaming
}
