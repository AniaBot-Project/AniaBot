package pluginaichat

import (
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// fakeStreamHandle 记录 Patch 内容与 End 调用的流式句柄；failPatches 令接下来
// N 次 Patch 失败（模拟消息被撤回/限流/编辑被拒）。
type fakeStreamHandle struct {
	mu          sync.Mutex
	patches     []string
	ended       bool
	failPatches int
}

func (h *fakeStreamHandle) Patch(text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failPatches > 0 {
		h.failPatches--
		return errors.New("patch failed")
	}
	h.patches = append(h.patches, text)
	return nil
}

func (h *fakeStreamHandle) End() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ended = true
}

func (h *fakeStreamHandle) patched() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.patches...)
}

func (h *fakeStreamHandle) isEnded() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ended
}

// fakeStreamSender 记录流式消息创建次数、初始消息链与 @ 提及数；
// ok=false 模拟平台不支持流式（Send*Stream 返回 false）。
type fakeStreamSender struct {
	ok       bool
	created  int
	handle   *fakeStreamHandle
	initial  []message.OB11Segment
	mentions int
}

func (s *fakeStreamSender) SendGroupStream(_ message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	s.created++
	s.initial = chain.GetGroupMsg()
	for _, seg := range s.initial {
		if seg.Type == "at" {
			s.mentions++
		}
	}
	if !s.ok {
		return nil, false
	}
	if s.handle == nil {
		s.handle = &fakeStreamHandle{}
	}
	return s.handle, true
}

func (s *fakeStreamSender) SendFriendStream(_ message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	s.created++
	s.initial = chain.GetFriendMsg()
	if !s.ok {
		return nil, false
	}
	if s.handle == nil {
		s.handle = &fakeStreamHandle{}
	}
	return s.handle, true
}

// streamBot 同时具备 bot.Bot（经 recordBot）与 bot.StreamSender 能力的测试 bot。
type streamBot struct {
	*recordBot
	sender *fakeStreamSender
}

func (b *streamBot) SendGroupStream(id message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	return b.sender.SendGroupStream(id, chain)
}

func (b *streamBot) SendFriendStream(id message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	return b.sender.SendFriendStream(id, chain)
}

// TestStreamReplyGate 两个开关共存：开启「按空行拆分回复」不再关闭流式（拆分只作用于
// 非流式发送）；流式开关关闭、或平台未实现 bot.StreamSender 时才不启用流式。
func TestStreamReplyGate(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.cfg.Stream.Enable = true
	p.cfg.Reply.SplitBlankLine = true

	// 平台不支持流式（QQ/OneBot v11）：走一次性发送，拆分在那里生效
	if s := p.newStreamReplyFor(&recordBot{}, message.FromUint64(1), true, nil); s != nil {
		t.Fatal("平台不支持流式能力时不应启用流式")
	}
	// 支持流式的平台：拆分开关开启也不影响流式启用
	sb := &streamBot{recordBot: &recordBot{}, sender: &fakeStreamSender{ok: true}}
	if s := p.newStreamReplyFor(sb, message.FromUint64(1), true, nil); s == nil {
		t.Fatal("拆分开关开启不应关闭流式（两者共存）")
	}
	// 流式开关关闭：不启用流式
	p.cfg.Stream.Enable = false
	if s := p.newStreamReplyFor(sb, message.FromUint64(1), true, nil); s != nil {
		t.Fatal("流式开关关闭时不应启用流式")
	}
}

// TestStreamReplyOverwritesSingleMessage 全程只创建一条流式消息：工具轮文本先展示，
// 轮次结束后缓冲清空，下一轮文本整体覆盖同一条消息（既不新建消息，也不拼接旧文本），
// 收尾后消息停留在最后一轮的最终回复上。
func TestStreamReplyOverwritesSingleMessage(t *testing.T) {
	sender := &fakeStreamSender{ok: true}
	s := newStreamReply(sender, slog.Default(), message.FromUint64(1), true, []message.QID{message.FromUint64(9)})

	s.OnDelta("草稿：先看看图")
	s.OnRoundEnd()
	s.OnDelta("最终")
	s.OnDelta("回答")
	s.End()

	if sender.created != 1 {
		t.Fatalf("全程应只创建一条流式消息, got %d", sender.created)
	}
	if sender.mentions != 1 {
		t.Fatalf("群聊首条消息应带 1 个 @ 提及, got %d", sender.mentions)
	}
	if got := chainText(sender.initial); got != " 草稿：先看看图" {
		t.Fatalf("初始内容不符: %q", got)
	}
	if got := sender.handle.patched(); len(got) != 2 || got[0] != "最终" || got[1] != "最终回答" {
		t.Fatalf("后续轮次应覆盖而非拼接, got %q", got)
	}
	if !sender.handle.isEnded() {
		t.Fatal("收尾应 End 流式消息")
	}
	if !s.Streaming() {
		t.Fatal("已流式发出内容时 Streaming 应为 true")
	}
}

// TestStreamReplyFriendNoMention 私聊不 @ 任何人，同样只维持一条消息。
func TestStreamReplyFriendNoMention(t *testing.T) {
	sender := &fakeStreamSender{ok: true}
	s := newStreamReply(sender, slog.Default(), message.FromUint64(1), false, []message.QID{message.FromUint64(9)})
	s.OnDelta("回复")
	s.End()
	if sender.mentions != 0 {
		t.Fatalf("私聊不应带 @ 提及, got %d", sender.mentions)
	}
	if got := chainText(sender.initial); got != "回复" {
		t.Fatalf("私聊内容不符: %q", got)
	}
}

// TestStreamReplyUnsupportedFallsBack 平台不支持流式：只尝试创建一次，后续增量全部
// 忽略，Streaming 保持 false，由调用方的一次性发送补全整段回复。
func TestStreamReplyUnsupportedFallsBack(t *testing.T) {
	sender := &fakeStreamSender{ok: false}
	s := newStreamReply(sender, slog.Default(), message.FromUint64(1), false, nil)
	s.OnDelta("第一段")
	s.OnRoundEnd()
	s.OnDelta("第二段")
	s.End()

	if sender.created != 1 {
		t.Fatalf("只应尝试创建一次, got %d", sender.created)
	}
	if s.Streaming() {
		t.Fatal("平台不支持时不应标记为已流式发出")
	}
}

// TestStreamReplyPatchFailsFallback Patch 连续失败达到阈值：结束消息并放弃流式
// （Streaming 置 false 交回一次性发送），后续增量不再创建新消息。
func TestStreamReplyPatchFailsFallback(t *testing.T) {
	sender := &fakeStreamSender{ok: true, handle: &fakeStreamHandle{failPatches: maxStreamPatchFails}}
	s := newStreamReply(sender, slog.Default(), message.FromUint64(1), false, nil)
	s.OnDelta("创建消息") // 首个增量创建消息，不 Patch
	for i := 0; i < maxStreamPatchFails; i++ {
		s.OnDelta("增量")
	}
	if s.Streaming() {
		t.Fatal("Patch 连续失败后应放弃流式")
	}
	if !sender.handle.isEnded() {
		t.Fatal("放弃流式时应结束消息")
	}
	s.OnDelta("放弃后的增量")
	if sender.created != 1 {
		t.Fatalf("放弃流式后不应再创建消息, got %d", sender.created)
	}
	if got := sender.handle.patched(); len(got) != 0 {
		t.Fatalf("失败的 Patch 不应计入成功内容, got %q", got)
	}
}

// TestBatchMentions 提及集去重、跳过无发送者的合成消息、保持发言顺序。
func TestBatchMentions(t *testing.T) {
	batch := []message.Message{
		{Sender: message.MessageSender{UserId: message.FromUint64(7)}},
		{Sender: message.MessageSender{UserId: message.FromUint64(0)}},
		{Sender: message.MessageSender{UserId: message.FromUint64(7)}},
		{Sender: message.MessageSender{UserId: message.FromUint64(8)}},
	}
	got := batchMentions(batch)
	if len(got) != 2 || got[0] != message.FromUint64(7) || got[1] != message.FromUint64(8) {
		t.Fatalf("提及集不符: %v", got)
	}
}
