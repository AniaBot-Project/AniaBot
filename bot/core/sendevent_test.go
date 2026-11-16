package core

import (
	"context"
	"sync"
	"testing"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// sendEventPlugin 同时实现发送前拦截与发送后观察事件的测试插件。
type sendEventPlugin struct {
	plugin.Meta

	before  func(ev *message.OutgoingMessage) (bool, error)
	after   func(ev message.SentMessage) error
	beforeN int
	afterN  int
	lastIn  *message.OutgoingMessage
	lastOut message.SentMessage
	mu      sync.Mutex
}

func (p *sendEventPlugin) OnBeforeSendMsg(ctx context.Context, b bot.Bot, ev *message.OutgoingMessage) (bool, error) {
	p.mu.Lock()
	p.beforeN++
	p.lastIn = ev
	fn := p.before
	p.mu.Unlock()
	if fn != nil {
		return fn(ev)
	}
	return true, nil
}

func (p *sendEventPlugin) OnAfterSendMsg(ctx context.Context, b bot.Bot, ev message.SentMessage) error {
	p.mu.Lock()
	p.afterN++
	p.lastOut = ev
	fn := p.after
	p.mu.Unlock()
	if fn != nil {
		return fn(ev)
	}
	return nil
}

func (p *sendEventPlugin) counts() (before, after int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.beforeN, p.afterN
}

// sendEventAdapter 记录发送内容并返回固定结果的最小适配器。
type sendEventAdapter struct {
	fakeAdapter
	fail        bool
	groupSent   [][]message.OB11Segment
	friendSent  [][]message.OB11Segment
	forwardSent []message.ForwardMessageSegment
	streamCalls int
	handle      bot.StreamHandle
}

func (a *sendEventAdapter) SendGroupMsg(_ message.QID, chain msgchain.GroupChain) (message.QID, bool) {
	a.groupSent = append(a.groupSent, append([]message.OB11Segment(nil), chain.GetGroupMsg()...))
	if a.fail {
		return "", false
	}
	return "mid:g1", true
}

func (a *sendEventAdapter) SendFriendMsg(_ message.QID, chain msgchain.FriendChain) (message.QID, bool) {
	a.friendSent = append(a.friendSent, append([]message.OB11Segment(nil), chain.GetFriendMsg()...))
	if a.fail {
		return "", false
	}
	return "mid:f1", true
}

// forwardSendAdapter 支持合并转发的测试适配器（嵌入 QQExt 接口桩满足类型断言）。
type forwardSendAdapter struct {
	sendEventAdapter
	adapter.QQExt
}

func (a *forwardSendAdapter) SendGroupForwardMsg(_ message.QID, chain msgchain.GroupForwardChain) (message.QID, bool) {
	a.forwardSent = append(a.forwardSent, chain.GetForwardMsg())
	return "mid:fw", true
}

func (a *forwardSendAdapter) SendFriendForwardMsg(_ message.QID, chain msgchain.FriendForwardChain) (message.QID, bool) {
	a.forwardSent = append(a.forwardSent, chain.GetForwardMsg())
	return "mid:fw", true
}

// streamSendAdapter 支持流式发送的测试适配器。
type streamSendAdapter struct {
	sendEventAdapter
	adapter.StreamSenderExt
}

func (a *streamSendAdapter) SendGroupStream(_ message.QID, _ msgchain.GroupChain) (bot.StreamHandle, bool) {
	a.streamCalls++
	return a.handle, a.handle != nil
}

func (a *streamSendAdapter) SendFriendStream(_ message.QID, _ msgchain.FriendChain) (bot.StreamHandle, bool) {
	a.streamCalls++
	return a.handle, a.handle != nil
}

// fakeStreamHandle 记录 Patch / End 调用的流式句柄。
type fakeStreamHandle struct {
	patches []string
	ended   int
}

func (h *fakeStreamHandle) Patch(text string) error {
	h.patches = append(h.patches, text)
	return nil
}

func (h *fakeStreamHandle) End() { h.ended++ }

func newSendEventBot(a adapter.Adapter, plugins ...plugin.Plugin) *AniaBot {
	ania := NewAniaBot()
	ania.adapters = []*adapterEntry{{
		def:     adapter.Definition{Name: "test", Platform: "test", IDPrefix: "ts:"},
		adapter: a,
	}}
	ania.plugins = plugins
	return ania
}

// TestSendGroupMsgEvents 发送前事件可改写消息段，发送后事件拿到最终内容与平台
// 返回的消息 ID。
func TestSendGroupMsgEvents(t *testing.T) {
	a := &sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}}
	p := &sendEventPlugin{}
	p.Name = "发送钩子"
	p.before = func(ev *message.OutgoingMessage) (bool, error) {
		if ev.Kind != message.SendKindGroup || ev.Target != "ts:1" {
			t.Errorf("发送前事件目标错误: %+v", ev)
		}
		ev.Segments = append(ev.Segments, message.OB11Segment{
			Type: message.SegmentText,
			Data: message.TextMessage{Text: "（插件追加）"}.Marshal(),
		})
		return true, nil
	}
	ania := newSendEventBot(a, p)
	chain := msgchain.Builder().Group().Text("原文").Build()

	msgID, ok := ania.SendGroupMsg("ts:1", chain)
	if !ok || msgID != "mid:g1" {
		t.Fatalf("发送结果错误: id=%q ok=%v", msgID, ok)
	}
	if len(a.groupSent) != 1 || len(a.groupSent[0]) != 2 {
		t.Fatalf("适配器应收到改写后的两段消息: %+v", a.groupSent)
	}
	if text, _ := a.groupSent[0][1].Data["text"].(string); text != "（插件追加）" {
		t.Fatalf("改写内容未生效: %+v", a.groupSent[0][1])
	}
	if p.lastOut.Success != true || p.lastOut.MessageId != "mid:g1" || len(p.lastOut.Segments) != 2 {
		t.Fatalf("发送后事件载荷错误: %+v", p.lastOut)
	}
	if p.lastOut.Target != "ts:1" || p.lastOut.Kind != message.SendKindGroup {
		t.Fatalf("发送后事件目标错误: %+v", p.lastOut)
	}
	if before, after := p.counts(); before != 1 || after != 1 {
		t.Fatalf("事件次数错误: before=%d after=%d", before, after)
	}
}

// TestSendFriendMsgFailureEvent 发送失败时仍触发发送后事件，Success=false。
func TestSendFriendMsgFailureEvent(t *testing.T) {
	a := &sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}, fail: true}
	p := &sendEventPlugin{}
	p.Name = "发送钩子"
	ania := newSendEventBot(a, p)

	if _, ok := ania.SendFriendMsg("ts:2", msgchain.Builder().Friend().Text("私聊").Build()); ok {
		t.Fatal("适配器返回失败时发送应为失败")
	}
	if _, after := p.counts(); after != 1 {
		t.Fatalf("失败也应触发发送后事件, after=%d", after)
	}
	if p.lastOut.Success || p.lastOut.MessageId != "" {
		t.Fatalf("失败结果错误: %+v", p.lastOut)
	}
}

// TestSendGroupMsgCanceled 发送前事件返回 false：取消发送，适配器不被调用，
// 发送后事件也不触发。
func TestSendGroupMsgCanceled(t *testing.T) {
	a := &sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}}
	p := &sendEventPlugin{}
	p.Name = "发送钩子"
	p.before = func(ev *message.OutgoingMessage) (bool, error) { return false, nil }
	ania := newSendEventBot(a, p)

	if _, ok := ania.SendGroupMsg("ts:1", msgchain.Builder().Group().Text("不该发出").Build()); ok {
		t.Fatal("被取消的发送应返回失败")
	}
	if len(a.groupSent) != 0 {
		t.Fatalf("被取消的消息不应提交平台: %+v", a.groupSent)
	}
	if before, after := p.counts(); before != 1 || after != 0 {
		t.Fatalf("被取消时只应触发发送前事件: before=%d after=%d", before, after)
	}
}

// TestSendEventsPlatformFilter 插件声明了 Meta.Platforms 时不接收其他平台的发送事件。
func TestSendEventsPlatformFilter(t *testing.T) {
	a := &sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}}
	p := &sendEventPlugin{}
	p.Name = "QQ 专用钩子"
	p.Platforms = []string{"qq"}
	ania := newSendEventBot(a, p)

	if _, ok := ania.SendGroupMsg("ts:1", msgchain.Builder().Group().Text("其他平台").Build()); !ok {
		t.Fatal("发送应成功")
	}
	if before, after := p.counts(); before != 0 || after != 0 {
		t.Fatalf("平台不匹配的插件不应收到事件: before=%d after=%d", before, after)
	}
}

// TestSendForwardEvents 合并转发的发送前事件可改写转发内容，发送后事件载荷
// 保留 Forward 而不是消息段。
func TestSendForwardEvents(t *testing.T) {
	a := &forwardSendAdapter{sendEventAdapter: sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}}}
	p := &sendEventPlugin{}
	p.Name = "发送钩子"
	p.before = func(ev *message.OutgoingMessage) (bool, error) {
		if ev.Forward == nil {
			t.Fatal("合并转发事件应携带 Forward 内容")
		}
		extra := msgchain.Builder().GroupForward()
		extra.Message("ts:2", "追加节点", msgchain.Builder().Group().Text("追加").Build())
		ev.Forward.Messages = append(ev.Forward.Messages, extra.Build().GetForwardMsg().Messages...)
		return true, nil
	}
	ania := newSendEventBot(a, p)

	fwd := msgchain.Builder().GroupForward()
	fwd.Message("ts:1", "小明", msgchain.Builder().Group().Text("一").Build())
	if _, ok := ania.SendGroupForwardMsg("ts:1", fwd.Build()); !ok {
		t.Fatal("合并转发应成功")
	}
	if len(a.forwardSent) != 1 || len(a.forwardSent[0].Messages) != 2 {
		t.Fatalf("适配器应收到改写后的转发内容: %+v", a.forwardSent)
	}
	if p.lastOut.Forward == nil || len(p.lastOut.Forward.Messages) != 2 || p.lastOut.Segments != nil {
		t.Fatalf("发送后事件载荷错误: %+v", p.lastOut)
	}

	// 私聊合并转发走 bot.FriendForwardSender 路径
	if _, ok := ania.SendFriendForwardMsg("ts:2", msgchain.Builder().FriendForward().Build()); !ok {
		t.Fatal("私聊合并转发应成功")
	}
	if len(a.forwardSent) != 2 || p.lastOut.Kind != message.SendKindFriend {
		t.Fatalf("私聊合并转发事件错误: %+v", p.lastOut)
	}
}

// TestSendStreamEvents 流式回复创建时触发发送前事件，结束时以最终文本触发一次
// 发送后事件；重复 End 不重复触发。
func TestSendStreamEvents(t *testing.T) {
	handle := &fakeStreamHandle{}
	a := &streamSendAdapter{
		sendEventAdapter: sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}},
	}
	a.handle = handle
	p := &sendEventPlugin{}
	p.Name = "发送钩子"
	ania := newSendEventBot(a, p)

	h, ok := ania.SendGroupStream("ts:1", msgchain.Builder().Group().Mention("ts:9").Text("首").Build())
	if !ok || h == nil {
		t.Fatal("流式消息应创建成功")
	}
	if before, after := p.counts(); before != 1 || after != 0 {
		t.Fatalf("创建时只应触发发送前事件: before=%d after=%d", before, after)
	}
	if err := h.Patch("最终回复"); err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	h.End()
	h.End()
	if handle.ended != 2 || len(handle.patches) != 1 {
		t.Fatalf("底层句柄调用次数错误: %+v", handle)
	}
	if before, after := p.counts(); before != 1 || after != 1 {
		t.Fatalf("结束时应触发一次发送后事件: before=%d after=%d", before, after)
	}
	// 最终文本段 + 初始的 @ 段（编辑只覆盖正文）
	if p.lastOut.Success != true || len(p.lastOut.Segments) != 2 {
		t.Fatalf("流式发送后事件载荷错误: %+v", p.lastOut)
	}
	if p.lastOut.Segments[0].Type != message.SegmentMention ||
		p.lastOut.Segments[1].Type != message.SegmentText {
		t.Fatalf("流式最终内容错误: %+v", p.lastOut.Segments)
	}
}

// TestSendEventNestedNoRecursion 插件在发送事件回调里再次发消息时，嵌套发送
// 不再分发事件（避免无限递归），但消息本身照常发出。
func TestSendEventNestedNoRecursion(t *testing.T) {
	a := &sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}}
	p := &sendEventPlugin{}
	p.Name = "发送钩子"
	var ania *AniaBot
	nested := false
	p.before = func(ev *message.OutgoingMessage) (bool, error) {
		if !nested {
			nested = true
			ania.SendGroupMsg("ts:1", msgchain.Builder().Group().Text("嵌套发送").Build())
		}
		return true, nil
	}
	ania = newSendEventBot(a, p)

	if _, ok := ania.SendGroupMsg("ts:1", msgchain.Builder().Group().Text("外层").Build()); !ok {
		t.Fatal("外层发送应成功")
	}
	if len(a.groupSent) != 2 {
		t.Fatalf("嵌套发送应照常发出: %+v", a.groupSent)
	}
	if before, after := p.counts(); before != 1 || after != 1 {
		t.Fatalf("嵌套发送不应重复分发事件: before=%d after=%d", before, after)
	}
}

// TestSendEventPanicDoesNotBlock 发送前事件 panic 不应阻止消息发出（与其他
// 事件「插件崩溃不影响主流程」的约定一致）。
func TestSendEventPanicDoesNotBlock(t *testing.T) {
	a := &sendEventAdapter{fakeAdapter: fakeAdapter{name: "test", platform: "test"}}
	p := &sendEventPlugin{}
	p.Name = "发送钩子"
	p.before = func(ev *message.OutgoingMessage) (bool, error) { panic("boom") }
	ania := newSendEventBot(a, p)

	if _, ok := ania.SendGroupMsg("ts:1", msgchain.Builder().Group().Text("照常发").Build()); !ok {
		t.Fatal("插件 panic 不应阻止发送")
	}
	if len(a.groupSent) != 1 {
		t.Fatalf("消息应照常发出: %+v", a.groupSent)
	}
}

var (
	_ plugin.SendMsgInterceptor = (*sendEventPlugin)(nil)
	_ plugin.SendMsgObserver    = (*sendEventPlugin)(nil)
	_ bot.GroupForwardSender    = (*AniaBot)(nil)
	_ bot.FriendForwardSender   = (*AniaBot)(nil)
	_ bot.StreamSender          = (*AniaBot)(nil)
)
