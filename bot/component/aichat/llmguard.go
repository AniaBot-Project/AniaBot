package aichat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// 单次 LLM 请求保护（WithRequestTimeout / WithStreamIdleTimeout 的运行时实现）。
//
// 请求总超时统一在 LLMClient 出口派生带超时的 context：三种 API 格式后端
// 都经由同一套 Generate / GenerateStream 出口，放在出口最不容易出现某个
// 后端漏配。
//
// 流空闲超时不能同样只在出口做——它必须按「有没有新数据块」重置计时：
// 后端每从上游读到一个块（内容/思考/工具参数增量/usage）就调用
// notifyStreamActivity 报告一次活动，看门狗据此判断是否真的空闲；只要模型
// 还在出字，即使单个请求耗时超过空闲阈值也不会被误杀。因此三个流式后端
// 各在读取循环里加了一行 notifyStreamActivity(ctx)（这是实现该语义侵入性
// 最小的位置），其余逻辑全部集中在 LLMClient 层。

// streamActivityKey 流式活动通知的上下文键。LLMClient 在启用空闲看门狗时
// 把它注入请求 context，后端读取循环通过 notifyStreamActivity 取用。
type streamActivityKey struct{}

// notifyStreamActivity 报告一次流活动：存在空闲看门狗时重置其 idle 计时；
// 未启用看门狗时为空操作。必须在流读取 goroutine 内、每读到一个块调用一次。
func notifyStreamActivity(ctx context.Context) {
	if fn, ok := ctx.Value(streamActivityKey{}).(func()); ok && fn != nil {
		fn()
	}
}

// requestTimeoutError 单次 LLM 请求总超时错误：包裹 context.DeadlineExceeded，
// 上层可用 errors.Is 识别，并按普通可重试错误处理（重试/备用模型语义不变）。
func requestTimeoutError(d time.Duration) error {
	return fmt.Errorf("LLM request timed out after %s: %w", d, context.DeadlineExceeded)
}

// streamIdleTimeoutError 流式响应空闲超时错误（同样包裹 context.DeadlineExceeded）。
func streamIdleTimeoutError(d time.Duration) error {
	return fmt.Errorf("LLM stream idle timed out after %s without new data: %w", d, context.DeadlineExceeded)
}

// internalStreamer 标记「非流式 generate 内部走流式通道聚合」的格式后端。
//
// 目前只有 anthropic：官方要求大 max_tokens 的非流式请求必须走流式，
// 后端的 generate 实际调用 generateStream。这类后端在 Generate 路径也要
// 应用流空闲看门狗，否则上游半挂时只能等请求总超时；而 chat_completions /
// responses 的非流式请求在响应返回前没有任何「增量」可言，套用空闲看门狗
// 会把「生成较慢但正常」的请求误杀，故不适用。
type internalStreamer interface {
	streamsInternally() bool
}

// streamIdleWatchdog 流式空闲看门狗。
//
// 语义：从请求开始计时，每收到一个上游数据块就重置；连续 idle 时长没有任何
// 新数据块时取消本次请求的 context，并置 fired 供调用方把随后的 ctx 取消
// 错误改写成可识别的超时错误（后端只会看到 context.Canceled）。
type streamIdleWatchdog struct {
	idle   time.Duration
	parent context.Context // 外层请求 context：其取消/超时不是空闲导致，不做改写
	cancel context.CancelFunc
	// lastTick 最近一次流活动时间（UnixNano），与 touches 通道配合避免
	// 「数据块与计时器同时就绪」时被误判为空闲。
	lastTick atomic.Int64
	touches  chan struct{} // 容量 1，高频活动合并通知
	done     chan struct{}
	doneOnce sync.Once
	// stopped 调用方主动停止（流已结束）而非空闲超时
	stopped atomic.Bool
	fired   atomic.Bool
}

// newStreamIdleWatchdog 基于 parent 派生带空闲看门狗的 context 与触摸函数。
// idle<=0 时原样返回 parent、nil 看门狗（不启用）。
func newStreamIdleWatchdog(parent context.Context, idle time.Duration) (context.Context, context.CancelFunc, *streamIdleWatchdog) {
	if idle <= 0 {
		return parent, func() {}, nil
	}
	ctx, cancel := context.WithCancel(parent)
	wd := &streamIdleWatchdog{
		idle:    idle,
		parent:  parent,
		cancel:  cancel,
		touches: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	wd.lastTick.Store(time.Now().UnixNano())
	go wd.run()
	return context.WithValue(ctx, streamActivityKey{}, wd.touch), cancel, wd
}

// touch 记录一次流活动（非阻塞；看门狗 goroutine 处理合并后的通知）。
func (w *streamIdleWatchdog) touch() {
	if w == nil {
		return
	}
	w.lastTick.Store(time.Now().UnixNano())
	select {
	case w.touches <- struct{}{}:
	default: // 已有待处理通知，无需重复
	}
}

// run 看门狗主循环：收到活动通知就重置计时；计时到期后再以最后一次真实活动
// 时间复核一次，避免边界上的数据块被误杀。
func (w *streamIdleWatchdog) run() {
	timer := time.NewTimer(w.idle)
	defer timer.Stop()
	for {
		select {
		case <-w.touches:
			resetIdleTimer(timer, w.idle)
		case <-timer.C:
			// 计时器与活动通知可能同时就绪（select 随机选择），以最后一次
			// 真实活动时间为准：仍有剩余空闲额度就继续等待。
			idleFor := time.Since(time.Unix(0, w.lastTick.Load()))
			if idleFor < w.idle {
				resetIdleTimer(timer, w.idle-idleFor)
				continue
			}
			// 调用方已停止（流正常结束）或外层 ctx 已取消：不是空闲超时
			if w.stopped.Load() || w.parent.Err() != nil {
				return
			}
			w.fired.Store(true)
			w.cancel()
			return
		case <-w.done:
			return
		}
	}
}

// stop 停止看门狗（流已结束/失败），返回是否因空闲超时触发过取消。
func (w *streamIdleWatchdog) stop() bool {
	if w == nil {
		return false
	}
	w.stopped.Store(true)
	w.doneOnce.Do(func() { close(w.done) })
	w.cancel() // 释放派生 context 资源；流已结束时无副作用
	return w.fired.Load()
}

// resetIdleTimer 安全重置计时器：先抽干可能已触发但未消费的到期值。
func resetIdleTimer(timer *time.Timer, d time.Duration) {
	if d <= 0 {
		d = time.Nanosecond
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

// withRequestTimeout 为单次 HTTP 请求派生带总超时的 context；d<=0 时原样返回。
func withRequestTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}

// normalizeRequestError 若本次尝试的请求总超时已触发，把错误改写成可识别的
// 超时错误。调用方的 ctx 已取消/超时时保持原样（由外层按 ctx.Err() 处理）。
func (c *LLMClient) normalizeRequestError(parent, reqCtx context.Context, err error) error {
	if err == nil || c.requestTimeout <= 0 || parent.Err() != nil || reqCtx.Err() == nil {
		return err
	}
	if errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
		return requestTimeoutError(c.requestTimeout)
	}
	return err
}
