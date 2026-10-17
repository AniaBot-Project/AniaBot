package pluginaichat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
)

// lockRenewInterval 会话锁续期心跳间隔：取 TTL 的 1/3，一次续期失败仍有
// 两次机会在过期前补救。
const lockRenewInterval = LockExpTime / 3

// renewMissTolerance 续期连续未确认成功的容忍次数：键缺失与存储瞬时故障
// 在存储接口上无法区分，连续两次失败（间隔 2×lockRenewInterval）才判定
// 锁已丢失，避免一次抖动误杀正在进行的响应。
const renewMissTolerance = 2

// lockFail 会话锁获取失败原因。区分「会话正忙」与「并发槽位已满」：
// 前者消息应进入排队队列（锁持有者的 drain 循环会消费），后者没有
// 任何消费者（会话本身空闲，缺的是全局并发配额），排队只会沉淀——
// 调用方应直接拒绝并提示用户重试。
type lockFail int

const (
	lockOK lockFail = iota
	lockBusy
	lockRateLimited
)

// sessionLock 一次成功获取的会话锁句柄：
//   - 后台心跳周期性把 TTL 续回 LockExpTime。AI 响应耗时可配置到远超
//     LockExpTime（工具审批、子代理、多轮工具调用），若锁中途过期，后续
//     消息会拿到锁并与当前响应并发驱动同一个 ChatBot（messageWindow 等状态
//     非并发安全，会产生数据竞争与历史错乱）；
//   - 锁值携带随机令牌，release 时仅在令牌仍匹配时删除——即使锁意外过期被
//     他人接管，先到响应的 release 也不会误删新持有者的锁；
//   - 心跳确认锁丢失（令牌被换或键消失）时取消附着其上的请求（attachCancel）：
//     宁可让在跑请求以「已停止」收尾，也不让它与新持锁者并发驱动同一会话。
type sessionLock struct {
	p           *AIChatPlugin
	key         string
	token       string
	stopCh      chan struct{}
	stopped     chan struct{}
	releaseOnce sync.Once

	// cancelMu 保护 cancel：请求侧在每批处理开始前 attach，心跳线程并发读取
	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// tryLockDetailed 尝试获取会话锁：成功返回锁句柄（后台自动续期），失败返回原因。
// 同时占用一个并发槽位（rateCh），与并发限制共用语义。
func (p *AIChatPlugin) tryLockDetailed(id message.QID, isGroup bool) (*sessionLock, lockFail) {
	select {
	case p.rateCh <- struct{}{}:
	default:
		return nil, lockRateLimited
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	token := newLockToken()
	locked := p.lockStorage.SetString(ctx, sessionKey(id, isGroup), token, storage.WithCheckExist(), storage.WithTTL(LockExpTime))
	if !locked {
		select {
		case <-p.rateCh:
		default:
		}
		return nil, lockBusy
	}
	l := &sessionLock{
		p:       p,
		key:     sessionKey(id, isGroup),
		token:   token,
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go l.renewLoop()
	return l, lockOK
}

// tryLock 兼容包装：不关心失败原因（会话正忙 / 并发槽位已满）的调用方使用。
func (p *AIChatPlugin) tryLock(id message.QID, isGroup bool) *sessionLock {
	lock, _ := p.tryLockDetailed(id, isGroup)
	return lock
}

// release 释放锁（幂等，可安全 defer）：先停心跳并等其退出（否则删除后
// 心跳可能把 TTL 续回去，锁「复活」阻塞后续消息），再在令牌仍匹配时删除，
// 最后归还并发槽位。
//
// 删键先于归还槽位：反序会产生「槽位已空、锁键仍在」的窗口，窗口内到达的
// 消息抢到槽位却拿不到锁，被当作「会话正忙」排队，而此刻已无持锁者消费
// 队列；正序下同样存在「锁键已删、槽位未还」的窗口，但那种情况的失败原因是
// 并发槽位已满（直接拒绝、用户可重试，且有 kickPending 兜底唤醒），
// 不会静默沉淀消息。GetString 与 Del 之间的检查窗口（存储接口未暴露原子
// compare-and-delete）相比无条件删除已消除「误删他人锁」的实际风险。
func (l *sessionLock) release() {
	l.releaseOnce.Do(func() {
		close(l.stopCh)
		<-l.stopped
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		if cur, ok := l.p.lockStorage.GetString(ctx, l.key); ok && cur == l.token {
			l.p.lockStorage.Del(ctx, l.key)
		}
		select {
		case <-l.p.rateCh:
		default:
		}
	})
}

// attachCancel 附着当前请求的取消函数（锁确认丢失时由心跳调用）。
func (l *sessionLock) attachCancel(cancel context.CancelFunc) {
	l.cancelMu.Lock()
	l.cancel = cancel
	l.cancelMu.Unlock()
}

// renewResult 一次续期尝试的结果
type renewResult int

const (
	renewOK   renewResult = iota // 令牌匹配且 TTL 已续回
	renewMiss                    // 键缺失或存储报错：无法区分，计入容忍次数
	renewLost                    // 键存在但令牌已换：锁被他人接管，确定丢失
)

// renewLoop 心跳续期：锁持有期间周期性把 TTL 重置回 LockExpTime。
func (l *sessionLock) renewLoop() {
	defer close(l.stopped)
	ticker := time.NewTicker(lockRenewInterval)
	defer ticker.Stop()
	misses := 0
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			switch l.renewOnce() {
			case renewOK:
				misses = 0
			case renewLost:
				l.lostCancel()
				return
			default: // renewMiss
				misses++
				if misses >= renewMissTolerance {
					l.lostCancel()
					return
				}
			}
		}
	}
}

// renewOnce 执行一次续期尝试。
func (l *sessionLock) renewOnce() renewResult {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	cur, ok := l.p.lockStorage.GetString(ctx, l.key)
	if !ok {
		return renewMiss
	}
	if cur != l.token {
		return renewLost
	}
	if !l.p.lockStorage.Expire(ctx, l.key, LockExpTime) {
		return renewMiss
	}
	return renewOK
}

// lostCancel 锁确认丢失：记错误日志并取消附着的请求。
func (l *sessionLock) lostCancel() {
	l.p.Logger.Error("会话锁续期失败，判定锁已丢失，取消当前请求", "key", l.key)
	l.cancelMu.Lock()
	cancel := l.cancel
	l.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// newLockToken 生成锁持有者随机令牌
func newLockToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极罕见，退化用纳秒时间戳（配合 CheckExist 仍可工作）
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// bridgeRunContext 在框架的事件上下文之上建立插件自己的运行上下文，
// 返回 (运行上下文, 提前摘除父取消桥接的函数, 事件截止时刻, 取消函数)。
//
// 两个约束必须同时满足，而标准库没有直接对应的组合：
//   - **保留框架的关停/卸载取消**：core.Stop() 取消 ania.ctx 时必须能中止在跑的
//     AI 请求。context.WithoutCancel 会把取消一并剥离（其返回的 ctx 永不 Done），
//     导致关停/卸载/重启都无法中止在跑请求；
//   - **剥离事件 deadline**：事件总时长由插件按 eventBudget 结算，单批另有独立
//     预算。context.WithCancel(ctx) 会继承 deadline，让框架的固定预算提前掐断
//     一个即将完成的回复。
//
// 因此用 deadlineExemptContext 包装：Done() 只反映显式取消（/stop、锁丢失、
// 框架关停），忽略父 ctx 因自身 deadline 到期而关闭的情况；取值仍走父 ctx。
//
// detachParent 供事件收尾前调用：收尾阶段的父取消会以「AI 响应已被停止」覆盖
// 插件自己更准确的结论（预算耗尽 / 超时）。
func (p *AIChatPlugin) bridgeRunContext(ctx context.Context) (context.Context, time.Time, context.CancelFunc) {
	var parentDeadline time.Time
	if deadline, ok := ctx.Deadline(); ok {
		parentDeadline = deadline
	}
	// 先剥掉父 ctx 的 deadline（WithCancel 若直接包父 ctx，父因事件超时关闭时
	// 派生 ctx 也会被取消）；取值仍保留，父的显式取消由下面的桥接补回。
	base, cancelBase := context.WithCancel(context.WithoutCancel(ctx))
	runCtx := &deadlineExemptContext{
		Context:  base,
		parent:   ctx,
		deadline: parentDeadline,
		done:     make(chan struct{}),
	}

	stopBridge := context.AfterFunc(base, func() {
		// 调用方 /stop、锁丢失或 defer 取消
		runCtx.terminate(context.Canceled)
	})
	var stopParentWatch func() bool
	if parentDeadline.IsZero() {
		// 父未设 deadline：任何父取消都视为关停信号
		stopParentWatch = context.AfterFunc(ctx, func() { runCtx.terminate(parentCancelledCause(ctx)) })
	} else {
		// 父设了 deadline：仅桥接「非 deadline 类」取消（关停/卸载），
		// deadline 到期交给插件按 eventBudget 结算
		stopParentWatch = context.AfterFunc(ctx, func() {
			if parentCancelledByDeadline(ctx, parentDeadline) {
				return
			}
			// 父 deadline 到期与插件「预算耗尽」判定可能同刻：插件已声明自行收尾时
			// 不取消，否则框架统一的「AI 响应已被停止」会覆盖更准确的提示
			if runCtx.timeoutClaimedByPlugin() {
				return
			}
			runCtx.terminate(parentCancelledCause(ctx))
		})
	}

	detachParent := sync.OnceFunc(func() { stopParentWatch() })
	cancelRun := func() {
		detachParent()
		stopBridge()
		cancelBase()
	}
	return runCtx, parentDeadline, cancelRun
}

// claimTimeout 声明「总预算耗尽由插件自己收尾」：此后父 ctx 因自身 deadline 到期
// 关闭不再取消本上下文，从而让插件能发出比框架统一文案更准确的提示
// （“本次处理已达时间上限”），而不是被“AI 响应已被停止”覆盖。
//
// 与父 deadline 到期存在同刻竞争，因此判定在父 ctx 的取消回调里做（见
// bridgeRunContext）：先到者生效，调用方应在事件截止前尽早声明。
func (c *deadlineExemptContext) claimTimeout() { c.timeoutClaimed.Store(true) }

// timeoutClaimedByPlugin 插件是否已声明自行处理总预算耗尽收尾。
func (c *deadlineExemptContext) timeoutClaimedByPlugin() bool { return c.timeoutClaimed.Load() }

// parentCancelledByDeadline 判断父 ctx 的结束是否由自身 deadline 到期引起。
// 用完整 ctx.Err() 而非 errors.Is 判定：context 的取消原因本身可能就是
// DeadlineExceeded（WithTimeout 的子 ctx 被取消时），errors.Is 会误判。
func parentCancelledByDeadline(parent context.Context, deadline time.Time) bool {
	if parent.Err() != context.DeadlineExceeded {
		return false
	}
	own, ok := parent.Deadline()
	if !ok {
		return false
	}
	// 父的结束原因确实是自身 deadline（同一时刻）才算超时；否则是上层把
	// DeadlineExceeded 当作取消原因传递（例如 WithCancelCause），此时应视为关停
	return own.Equal(deadline)
}

// parentCancelledCause 父 ctx 的结束原因，取不到时退回 context.Canceled。
func parentCancelledCause(parent context.Context) error {
	if err := context.Cause(parent); err != nil {
		return err
	}
	return context.Canceled
}

// deadlineExemptContext 继承父 ctx 的取值，但 Done()/Err() 只反映**显式取消**：
//   - 框架关停/卸载取消父 ctx → 取消（经 bridgeRunContext 的保险丝桥接）；
//   - 父 ctx 因自身 deadline 到期关闭 → 不取消，事件时长由插件按 eventBudget
//     结算，避免框架的固定预算提前掐断一个即将完成的回复；
//   - 插件侧 /stop、锁丢失 → 取消。
//
// Deadline() 恒为未设置：单批预算由调用方按 min(msgEventTimeout, 剩余总预算)
// 显式派生，不该继承框架的事件 deadline。
type deadlineExemptContext struct {
	// Context 提供 Value 取值（追踪等）；其 Done 不被暴露
	context.Context
	parent   context.Context
	deadline time.Time

	// timeoutClaimed 插件已声明自行处理「总预算耗尽」收尾
	timeoutClaimed atomic.Bool

	doneOnce sync.Once
	done     chan struct{}
	errMu    sync.Mutex
	err      error
}

func (c *deadlineExemptContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *deadlineExemptContext) Done() <-chan struct{}       { return c.done }

func (c *deadlineExemptContext) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// terminate 记录结束原因并关闭 Done（幂等）。
func (c *deadlineExemptContext) terminate(err error) {
	c.doneOnce.Do(func() {
		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()
		close(c.done)
	})
}

// parentCause 父 ctx 的结束原因（未结束返回 nil），供测试与调试断言。
func (c *deadlineExemptContext) parentCause() error { return c.parent.Err() }

// parentExpiredByDeadline 运行上下文是否因「框架事件 deadline 到期」被取消
// （区别于 /stop、锁丢失等显式取消）。
func parentExpiredByDeadline(runCtx context.Context) bool {
	c, ok := runCtx.(*deadlineExemptContext)
	if !ok || c.deadline.IsZero() {
		return false
	}
	return parentCancelledByDeadline(c.parent, c.deadline)
}

// claimEventTimeout 若 runCtx 支持「预算耗尽收尾」声明则声明之（幂等）。
func claimEventTimeout(runCtx context.Context) {
	if c, ok := runCtx.(*deadlineExemptContext); ok {
		c.claimTimeout()
	}
}

// batchContext 为单批处理派生预算上下文。语义与 bridgeRunContext 一致：保留
// /stop、锁丢失、关停的取消（runCtx 被取消时本批立即结束），但**不**继承框架
// 事件的 deadline——否则框架的固定预算会在事件截止那一刻掐断本批，让用户看到
// 统一文案「请求超时」，而不是插件更准确的「本次处理已达时间上限」。
// 总预算的收口由排空循环按 eventBudget 统一负责。
func batchContext(runCtx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	batchCtx, cancel := context.WithTimeout(runCtx, budget)
	if exempt, ok := runCtx.(*deadlineExemptContext); ok && !exempt.deadline.IsZero() {
		return &batchDeadlineExemptContext{Context: batchCtx}, cancel
	}
	return batchCtx, cancel
}

// batchDeadlineExemptContext 屏蔽 batchCtx 的 deadline（其唯一来源是框架事件
// deadline，已由排空循环统一结算）；取消照常传递。Value 仍走 batchCtx。
type batchDeadlineExemptContext struct {
	context.Context
}

func (c *batchDeadlineExemptContext) Deadline() (time.Time, bool) { return time.Time{}, false }

// eventBudget 本次事件累计可用的总预算：以框架事件 deadline 为准（其值与
// p.msgEventTimeout 同源，见 Start 的读取逻辑），框架未设 deadline 时退回
// p.msgEventTimeout。排空循环用它结算，保证一次消息事件不会因「持续有新消息
// 入队」而无限延长（旧实现每批都重新派生一份完整预算，单事件时长无上界）。
func (p *AIChatPlugin) eventBudget(parentDeadline time.Time) time.Duration {
	if !parentDeadline.IsZero() {
		if budget := time.Until(parentDeadline); budget > 0 {
			return budget
		}
		return 0
	}
	return p.msgEventTimeout
}
