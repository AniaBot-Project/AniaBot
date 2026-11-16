package core

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// SendMsgEventTimeout 单次发送事件分发的超时：发送前事件会阻塞真正的发送，
// 必须给插件留出改写 / 取消的时间，但不能让卡死的插件把消息发送永久挂起。
const SendMsgEventTimeout = 10 * time.Second

// eventContext 发送事件使用的上下文：测试里可能直接构造 &AniaBot{}（ctx 为
// nil），此时退回 Background，避免 context.WithTimeout 因 nil parent panic。
func (ania *AniaBot) eventContext() context.Context {
	if ania.ctx == nil {
		return context.Background()
	}
	return ania.ctx
}

// eventBot 发送事件传给插件的 bot 外观：优先用来源适配器的能力包装外观
// （与其他事件一致，插件可断言 bot.QQ 等平台能力），未包装时退回 core 自身。
func (ania *AniaBot) eventBot(e *adapterEntry) bot.Bot {
	if e != nil && e.evBot != nil {
		return e.evBot
	}
	return ania
}

// cloneSegments 复制消息段切片：发送前事件可以按需改写 ev.Segments，复制一份
// 避免插件 append 复用调用方消息链的底层数组造成意外修改（段内的 Data map
// 仍与调用方共享，改写内容请替换段本身）。
func cloneSegments(segs []message.OB11Segment) []message.OB11Segment {
	if len(segs) == 0 {
		return segs
	}
	return append([]message.OB11Segment(nil), segs...)
}

// dispatchBeforeSend 广播发送前事件（插件按 Order 从小到大，命中
// plugin.SendMsgInterceptor 的才调用）。返回 false 表示本次发送被插件取消。
func (ania *AniaBot) dispatchBeforeSend(e *adapterEntry, ev *message.OutgoingMessage) bool {
	if e == nil {
		return true
	}
	leave, ok := enterSendEvent()
	if !ok {
		// 当前 goroutine 正在分发发送事件（插件在回调里又发了消息）：只发消息，
		// 不再分发事件，避免无限递归。
		return true
	}
	defer leave()

	platform := e.def.Platform
	for _, p := range ania.plugins {
		if !ania.supportsPlatform(p, platform) {
			continue
		}
		h, ok := p.(plugin.SendMsgInterceptor)
		if !ok {
			continue
		}
		next, panicked := safeExecuteWithReturn("发送前事件", p, func(p plugin.Plugin) bool {
			ctx, cancel := context.WithTimeout(ania.eventContext(), SendMsgEventTimeout)
			defer cancel()
			next, err := h.OnBeforeSendMsg(ctx, ania.eventBot(e), ev)
			logError(err, p, "发送前事件")
			return next
		})
		if panicked {
			continue // panic 视为未取消，继续后续插件与发送（不因日志类插件崩溃丢消息）
		}
		if !next {
			return false
		}
	}
	return true
}

// dispatchAfterSend 广播发送后事件（广播制、不中断；成功与失败都会触发）。
func (ania *AniaBot) dispatchAfterSend(e *adapterEntry, ev message.SentMessage) {
	if e == nil {
		return
	}
	leave, ok := enterSendEvent()
	if !ok {
		return // 嵌套发送不再分发，避免递归
	}
	defer leave()

	platform := e.def.Platform
	for _, p := range ania.plugins {
		if !ania.supportsPlatform(p, platform) {
			continue
		}
		h, ok := p.(plugin.SendMsgObserver)
		if !ok {
			continue
		}
		safeExecute("发送后事件", p, func(p plugin.Plugin) {
			ctx, cancel := context.WithTimeout(ania.eventContext(), SendMsgEventTimeout)
			defer cancel()
			err := h.OnAfterSendMsg(ctx, ania.eventBot(e), ev)
			logError(err, p, "发送后事件")
		})
	}
}

// sendEventDepth 记录每个 goroutine 正在分发发送事件的层数：发送事件是同步
// 回调，插件在回调里再次调用 bot.Bot 发送消息时会重新进入分发；按 goroutine
// 屏蔽嵌套分发，让它只发消息不触发事件，避免「插件发消息 → 发送事件 → 插件
// 又发消息」的无限递归。键为 goroutine ID（Go 未公开 goroutine local storage，
// 这里用 runtime.Stack 头部解析，仅在每次发送事件的进入 / 退出时调用）。
var sendEventDepth sync.Map // uint64 -> *atomic.Int32

// enterSendEvent 尝试进入发送事件分发；返回的 leave 必须在分发结束时调用。
// 同一 goroutine 已在分发中时返回 ok=false（调用方跳过事件、照常发送）。
func enterSendEvent() (leave func(), ok bool) {
	id := currentGoroutineID()
	if id == 0 {
		return func() {}, true
	}
	v, _ := sendEventDepth.LoadOrStore(id, new(atomic.Int32))
	depth := v.(*atomic.Int32)
	if depth.Add(1) > 1 {
		depth.Add(-1)
		return func() {}, false
	}
	return func() {
		depth.Add(-1)
		sendEventDepth.Delete(id)
	}, true
}

// currentGoroutineID 解析当前 goroutine 的 ID（runtime.Stack 首行形如
// "goroutine 123 [running]:"）；解析失败返回 0，调用方退化为不屏蔽嵌套。
func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	s := buf[:n]
	const prefix = "goroutine "
	if len(s) <= len(prefix) || string(s[:len(prefix)]) != prefix {
		return 0
	}
	s = s[len(prefix):]
	var id uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
	}
	return id
}

// sendEventStreamHandle 包装适配器的流式消息句柄：发送前事件已在创建消息时
// 分发；流式过程中记录最终内容，End 时以最终文本触发一次发送后事件
// （流式消息无法在创建时就得知最终内容，因此发送后事件延后到结束）。
type sendEventStreamHandle struct {
	bot.StreamHandle

	ania  *AniaBot
	entry *adapterEntry

	mu    sync.Mutex
	ev    message.OutgoingMessage
	ended bool
}

func newSendEventStreamHandle(h bot.StreamHandle, ania *AniaBot, e *adapterEntry, ev message.OutgoingMessage) *sendEventStreamHandle {
	if ev.Segments == nil {
		ev.Segments = []message.OB11Segment{}
	}
	return &sendEventStreamHandle{StreamHandle: h, ania: ania, entry: e, ev: ev}
}

func (h *sendEventStreamHandle) Patch(text string) error {
	h.mu.Lock()
	h.ev.Segments = streamSegments(h.ev.Segments, text)
	h.mu.Unlock()
	return h.StreamHandle.Patch(text)
}

func (h *sendEventStreamHandle) End() {
	h.mu.Lock()
	if h.ended {
		h.mu.Unlock()
		h.StreamHandle.End()
		return
	}
	h.ended = true
	ev := h.ev
	h.mu.Unlock()

	h.StreamHandle.End()
	h.ania.dispatchAfterSend(h.entry, message.SentMessage{OutgoingMessage: ev, Success: true})
}

// streamSegments 用最终文本替换流式消息里的文本段，保留初始链里的 @ 等
// 非文本段（平台编辑通常只覆盖正文），供发送后事件 / 消息日志还原完整内容。
func streamSegments(base []message.OB11Segment, text string) []message.OB11Segment {
	segs := make([]message.OB11Segment, 0, len(base)+1)
	for _, s := range base {
		if s.Type != message.SegmentText {
			segs = append(segs, s)
		}
	}
	if text != "" {
		segs = append(segs, message.OB11Segment{
			Type: message.SegmentText,
			Data: message.TextMessage{Text: text}.Marshal(),
		})
	}
	return segs
}
