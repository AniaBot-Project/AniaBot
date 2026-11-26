package pluginaichat

import (
	"context"
	"errors"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 回合运行器（Session Runner）：消息事件只做去重/自过滤/命令/mention 门，
// 然后把消息入队并立即返回；实际回合在后台 goroutine 中串行执行。这样：
//   - 长回合不再占用框架事件 goroutine，也不受 bot.msg_event_timeout_sec 约束；
//   - 回合进行中到达的消息进入队列（不丢弃），下一次 LLM 请求前以「插话」注入
//     当前工具循环（见 interject.go）；没来得及注入的由本批结束后的 drain 继续处理；
//   - 同一会话由会话锁串行，跨会话并发受 rate_limit 槽位约束；
//   - /stop 取消活跃回合并清理队列，/status 依赖 activeContexts 展示进度。
//
// 回合自身的兜底是 newTurnContext 的硬截止（turnHardTimeout，默认 4 小时、
// 0=不限制）；软截止由 aichat 编排层的看门狗注入提醒，不在这里取消。

// submitTurn 把一条消息加入会话队列并唤醒运行器。队列满时直接提示用户。
// 会话已有回合在跑时不需要额外唤醒：运行中的请求会在下一次 LLM 请求前把队列
// 作为插话注入；会话空闲时本函数拉起的运行器会开新回合消费队列。
func (p *AIChatPlugin) submitTurn(b bot.Bot, id message.QID, isGroup bool, msg message.Message) {
	key := sessionKey(id, isGroup)
	p.touchChat(key)
	p.sessionBots.Store(key, b)
	if _, ok := p.enqueuePending(id, isGroup, msg); !ok {
		p.sendPlainText(b, id, isGroup, "排队消息太多啦，稍后再试试吧~")
		return
	}
	b.Go("runner:"+key, func() { p.runQueued(b, id, isGroup) })
}

// runQueued 后台接管会话队列。拿不到锁说明已有运行器在跑（它会在每批结束后
// 继续 drain），短暂重试后退出；锁释放与入队的交错由常驻清扫器兜底。
func (p *AIChatPlugin) runQueued(b bot.Bot, id message.QID, isGroup bool) {
	key := sessionKey(id, isGroup)
	for attempt := 0; attempt < pendingKickAttempts; attempt++ {
		if !p.hasPending(key) {
			return
		}
		if p.runSessionOnce(context.Background(), b, id, isGroup) {
			return
		}
		if attempt < pendingKickAttempts-1 {
			time.Sleep(pendingKickInterval)
		}
	}
}

// runSessionOnce 尝试获取会话锁并把队列处理到空。返回 true 表示本次接管成功
// （不论处理结果），false 表示没拿到锁/槽位（调用方可重试或交给清扫器）。
func (p *AIChatPlugin) runSessionOnce(parent context.Context, b bot.Bot, id message.QID, isGroup bool) bool {
	key := sessionKey(id, isGroup)
	lock, fail := p.tryLockDetailed(id, isGroup)
	if lock == nil {
		if fail == lockRateLimited {
			// 全局对话并发槽已满：可能是其他会话占满（本会话空闲，没有消费者，
			// 排队只会沉淀 → 直接拒绝并清空，让用户重发）；也可能是本会话自己的
			// 回合正在运行、只是槽位恰好占满——此时消息必须保留，由运行中回合的
			// 工具循环在下一次 LLM 请求前作为插话注入，不能丢弃。
			if p.sessionLockHeld(key) {
				return true
			}
			p.drainPending(id, isGroup)
			p.sendPlainText(b, id, isGroup, "当前请求太多啦，请稍后再试~")
			return true
		}
		return false
	}
	defer lock.release()
	p.touchChat(key)

	chat, err := p.getChat(b, id, isGroup, p.getPromptForID(id, isGroup))
	if err != nil {
		p.drainPending(id, isGroup)
		p.sendPlainText(b, id, isGroup, "无法创建对话："+aichat.SafeErrorText(err))
		return true
	}

	// 回合运行上下文：脱离框架事件 deadline、关联插件生命周期、叠加硬截止
	runCtx, cancelRun := p.newTurnContext(parent)
	defer cancelRun()
	lock.attachCancel(cancelRun)

	if isGroup {
		// 未@计数达到阈值时补清（与旧流程一致：会话重建后在本轮开始前处理）
		p.applyNoMentionClear(runCtx, id, chat)
	}

	start := time.Now()
	batch := p.drainPending(id, isGroup)
	turnTask := p.beginTurnTask(id, isGroup, turnTaskTitle(batch))
	taskID := ""
	if turnTask != nil {
		taskID = turnTask.ID
	}
	// 把任务 ID 传给 processChatBatch → Query 日志（stop_reason/task_id）
	runCtx = withTurnInfo(runCtx, taskID)
	for len(batch) > 0 {
		p.setActiveContext(id, isGroup, newActiveRequest(cancelRun, batchRequester(batch), start))
		next, ok := p.drainChatBatches(runCtx, b, id, isGroup, chat, batch)
		p.clearActiveContext(id, isGroup)
		if !ok {
			status, reason := taskStatusCancelled, taskStopAborted
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				status, reason = taskStatusTimeout, taskStopTimeout
			}
			p.finishTurnTask(turnTask, status, reason, "")
			return true
		}
		batch = next
	}
	p.finishTurnTask(turnTask, taskStatusCompleted, taskStopCompleted, "")
	// 长任务目标续跑：会话空闲且目标已武装时，自动开启下一轮
	p.maybeDriveGoal(b, id, isGroup)
	return true
}

// newTurnContext 构造一次回合的运行上下文：
//   - 脱离框架事件 ctx 的取消与 deadline（只保留 Value，如追踪/图片注册表），
//     避免 bot.msg_event_timeout_sec 掐断回合；
//   - 关联插件生命周期：OnUnload 取消 p.runCtx 时同步取消本回合；
//   - turnHardTimeout>0 时叠加硬截止（0=不限制，仅 /stop 与关停可停）。
func (p *AIChatPlugin) newTurnContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	runCtx, cancelRun := context.WithCancel(base)
	var stopShutdown func() bool
	if p.runCtx != nil {
		stopShutdown = context.AfterFunc(p.runCtx, cancelRun)
	}
	cancel := func() {
		if stopShutdown != nil {
			stopShutdown()
		}
		cancelRun()
	}
	if p.turnHardTimeout > 0 {
		hardCtx, cancelHard := context.WithTimeout(runCtx, p.turnHardTimeout)
		return hardCtx, func() {
			cancelHard()
			cancel()
		}
	}
	return runCtx, cancel
}

// stopSessionWork 停止会话的当前回合与全部后台子代理，返回（回合是否被停止,
// 取消的后台任务数, 权限拒绝原因）。即使没有活跃回合，也能停止主回合结束后
// 仍在后台运行的子代理（旧实现只有存在活跃请求时才取消，导致 /stop 无效）。
func (p *AIChatPlugin) stopSessionWork(id message.QID, isGroup bool, by message.QID, isAdmin bool) (stopped bool, subagents int, denied string) {
	stopped, denied = p.stopRequest(id, isGroup, by, isAdmin)
	if denied != "" {
		return false, 0, denied
	}
	subagents = len(p.listRunningSubagents(id, isGroup))
	if subagents > 0 {
		p.cancelAsyncSubagents(id, isGroup)
	}
	// 用户显式停止：丢弃待处理队列，避免运行器停止后清扫器又立刻消费
	if stopped || subagents > 0 {
		p.drainPending(id, isGroup)
	}
	// 停止会话时暂停目标自动续跑：用户显式停止不应被自动续跑立刻顶回来
	if stopped || subagents > 0 {
		p.pauseGoalOnStop(id, isGroup)
	}
	return stopped, subagents, ""
}

// turnInfoKey / turnInfo 回合元信息：写入 Query 日志的任务 ID 等。通过
// context 传递，测试直接调用 processChatBatch 时无该值（字段留空）。
type turnInfoKey struct{}

type turnInfo struct {
	taskID string
}

// withTurnInfo 给回合上下文挂上任务 ID。
func withTurnInfo(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, turnInfoKey{}, &turnInfo{taskID: taskID})
}

// turnInfoFrom 读取回合元信息（不存在返回 nil）。
func turnInfoFrom(ctx context.Context) *turnInfo {
	info, _ := ctx.Value(turnInfoKey{}).(*turnInfo)
	return info
}

// stopReasonFromError 把一次请求的结束错误映射为任务/Query 日志的停止原因。
func stopReasonFromError(err error) string {
	if err == nil {
		return taskStopCompleted
	}
	var wd *aichat.WatchdogAbortError
	switch {
	case errors.As(err, &wd):
		return taskStopWatchdog
	case errors.Is(err, context.DeadlineExceeded):
		return taskStopTimeout
	case errors.Is(err, context.Canceled):
		return taskStopUserStop
	default:
		return taskStopError
	}
}

// secondsDuration 把配置里的秒数转换为 time.Duration（含溢出与负数防护，
// 0/负数表示关闭）。
func secondsDuration(sec int) time.Duration {
	if sec <= 0 {
		return 0
	}
	const maxSec = int64(365 * 24 * 60 * 60)
	s := int64(sec)
	if s > maxSec {
		s = maxSec
	}
	return time.Duration(s) * time.Second
}
