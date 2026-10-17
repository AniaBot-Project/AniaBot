package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// asyncSubagentEntry 记录一个正在运行的异步子代理
type asyncSubagentEntry struct {
	id        string
	cancel    context.CancelFunc
	task      string
	startTime time.Time
}

// asyncSubagentGroup 单个会话的异步子代理集合（支持多子代理并发）
type asyncSubagentGroup struct {
	mu      sync.Mutex
	entries []asyncSubagentEntry
}

func (g *asyncSubagentGroup) add(id string, cancel context.CancelFunc, task string) {
	g.mu.Lock()
	g.entries = append(g.entries, asyncSubagentEntry{
		id:        id,
		cancel:    cancel,
		task:      task,
		startTime: time.Now(),
	})
	g.mu.Unlock()
}

// removeIfEmpty 移除指定 ID 的条目，返回 (removed, empty)：removed 表示该 ID
// 确实在组内（移除与判空在同一临界区内完成，避免「判空后又加入新条目」的竞态；
// ID 不存在时不报告 empty，杜绝误删他人刚创建、尚未入条目的新组）。
func (g *asyncSubagentGroup) removeIfEmpty(id string) (removed, empty bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	filtered := make([]asyncSubagentEntry, 0, len(g.entries))
	for _, e := range g.entries {
		if e.id == id {
			removed = true
			continue
		}
		filtered = append(filtered, e)
	}
	g.entries = filtered
	return removed, len(g.entries) == 0
}

func (g *asyncSubagentGroup) cancelAll() {
	g.mu.Lock()
	for _, e := range g.entries {
		e.cancel()
	}
	g.entries = nil
	g.mu.Unlock()
}

func (g *asyncSubagentGroup) cancelOne(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		if e.id == id {
			e.cancel()
			return true
		}
	}
	return false
}

// snapshot 返回当前运行中子代理的快照（副本）
func (g *asyncSubagentGroup) snapshot() []asyncSubagentEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := make([]asyncSubagentEntry, len(g.entries))
	copy(cp, g.entries)
	return cp
}

// getAsyncGroup 获取或创建会话的异步子代理组
func (p *AIChatPlugin) getAsyncGroup(id message.QID, isGroup bool) *asyncSubagentGroup {
	key := sessionKey(id, isGroup)
	v, _ := p.asyncSubagents.LoadOrStore(key, &asyncSubagentGroup{})
	return v.(*asyncSubagentGroup)
}

// cancelAsyncSubagents 取消指定会话所有运行中的异步子代理
func (p *AIChatPlugin) cancelAsyncSubagents(id message.QID, isGroup bool) {
	key := sessionKey(id, isGroup)
	if v, ok := p.asyncSubagents.LoadAndDelete(key); ok {
		v.(*asyncSubagentGroup).cancelAll()
	}
}

// lookupAsyncGroup 读取会话的异步子代理组（不做创建）：查询/取消路径使用，
// 避免用 getAsyncGroup 重建空组造成状态表随会话累积泄漏。
func (p *AIChatPlugin) lookupAsyncGroup(id message.QID, isGroup bool) *asyncSubagentGroup {
	v, ok := p.asyncSubagents.Load(sessionKey(id, isGroup))
	if !ok {
		return nil
	}
	g, _ := v.(*asyncSubagentGroup)
	return g
}

// listRunningSubagents 返回当前会话运行中的异步子代理列表（供 subagent_list 工具使用）
func (p *AIChatPlugin) listRunningSubagents(id message.QID, isGroup bool) []asyncSubagentEntry {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return nil
	}
	return g.snapshot()
}

// cancelSubagentByID 按 ID 取消单个运行中的异步子代理（供 subagent_cancel 工具使用）
func (p *AIChatPlugin) cancelSubagentByID(id message.QID, isGroup bool, subagentID string) bool {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return false
	}
	return g.cancelOne(subagentID)
}

// removeAsyncSubagent 子代理结束后从会话组中移除；组已空（或已被 /stop 的
// cancelAsyncSubagents 摘除）时清理 sync.Map 条目，避免空组滞留。
func (p *AIChatPlugin) removeAsyncSubagent(id message.QID, isGroup bool, subagentID string) {
	key := sessionKey(id, isGroup)
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return
	}
	if removed, empty := g.removeIfEmpty(subagentID); removed && empty {
		p.asyncSubagents.CompareAndDelete(key, g)
	}
}

// launchAsyncSubagent 在后台 goroutine 中启动子代理，立即返回占位文本。
//
// ctx 为主请求上下文：仅用于检查剩余时间预算是否 ≥ subagentParentReserve。
// 子代理的实际执行使用独立的 context.WithCancel(context.Background()) ——
// 无 deadline，让 runSubagent 内部的 resolveSubagentTimeout + WithTimeout 自行管理
// 超时；/stop 通过 asyncSubagentGroup 注册的 cancel 取消。
func (p *AIChatPlugin) launchAsyncSubagent(
	ctx context.Context,
	b bot.Bot,
	id message.QID,
	isGroup bool,
	task string,
	timeoutSec int,
	parentCbs llmtool.CallBackFuncs,
) string {
	// 检查父请求剩余预算
	if _, err := resolveSubagentTimeout(p.subagentTimeout(), timeoutSec, ctx); err != nil {
		return fmt.Sprintf("无法启动子代理: %v", err)
	}

	launchID := fmt.Sprintf("%d", time.Now().UnixNano())

	// 无 deadline 的 cancel context：runSubagent 内部自行通过 resolveSubagentTimeout
	// 创建带 timeout 的子 context，外部 cancel（/stop）不影响超时计算，且
	// asyncCtx.Err() == nil 让 runSubagent 能正确区分「子代理自身超时」和「外部取消」
	asyncCtx, asyncCancel := context.WithCancel(context.Background())

	p.getAsyncGroup(id, isGroup).add(launchID, asyncCancel, task)

	b.Go("subagent:"+sessionKey(id, isGroup)+":"+launchID, func() {
		defer p.removeAsyncSubagent(id, isGroup, launchID)
		result, usage, runErr := p.runSubagent(asyncCtx, b, id, isGroup, task, timeoutSec, parentCbs)
		// 消耗计入会话与全局配额（发起方已做前置检查，这里只累加不重复拒绝）
		p.quotaManager.Add(sessionKey(id, isGroup), usage)
		// 异步子代理完成时主请求通常已结束，其消耗暂存到会话级累计器，
		// 由该会话下一次 finishQuery 并入 Query 日志（/stop 取消的也照常计入）
		p.addExtraUsage(sessionKey(id, isGroup), usage)
		p.onSubagentComplete(b, id, isGroup, launchID, task, result, runErr)
	})

	return fmt.Sprintf("✅ 子代理已启动（ID: %s），正在后台执行任务", launchID)
}

// onSubagentComplete 子代理完成后的回调（在子代理 goroutine 中执行）。
//
// 1. 格式化结果文本（携带 launchID，便于 AI 与启动时分配的 ID 对应）
// 2. 构造合成消息并入队 pending 队列
// 3. 若会话空闲则自动触发 AI 处理
func (p *AIChatPlugin) onSubagentComplete(b bot.Bot, id message.QID, isGroup bool, launchID string, task string, result string, err error) {
	var text string
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "超时"):
			text = fmt.Sprintf("【子代理执行超时】\nID: %s\n任务: %s\n%s", launchID, task, err.Error())
		case errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "canceled"):
			p.Logger.Info("异步子代理已被取消", "id", id, "is_group", isGroup, "launch_id", launchID, "task", task)
			return // 被 /stop 取消，不注入结果
		default:
			text = fmt.Sprintf("【子代理执行失败】\nID: %s\n任务: %s\n错误: %s", launchID, task, err.Error())
		}
	} else {
		text = fmt.Sprintf("【子代理执行完成】\nID: %s\n任务: %s\n\n%s", launchID, task, result)
	}

	// 构造合成消息
	syntheticMsg := message.Message{
		Sender: message.MessageSender{
			Nickname: "子代理",
			UserId:   message.FromUint64(0),
		},
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": text}},
		},
	}

	// 入队
	first, ok := p.enqueuePending(id, isGroup, syntheticMsg)
	if !ok {
		p.Logger.Warn("异步子代理结果入队失败（队列已满）", "id", id, "is_group", isGroup, "launch_id", launchID)
		return
	}

	if first {
		p.Logger.Info("异步子代理结果已入队（队列首条）", "id", id, "is_group", isGroup, "launch_id", launchID, "task", task)
	} else {
		p.Logger.Info("异步子代理结果已入队", "id", id, "is_group", isGroup, "launch_id", launchID, "task", task)
	}

	// 尝试触发处理：若会话空闲（锁可用）则立即处理排队消息
	b.Go("pending:"+sessionKey(id, isGroup), func() { p.kickPending(b, id, isGroup) })
}

// pendingKickAttempts / pendingKickInterval 兜底唤醒的即时重试节奏：入队与持锁者
// 「最后一次 drain」交错时消息会错过所有消费者；这种交错发生在锁释放的临界区
// 附近，短退避重试即可跨过。更持久的情况（如并发槽位被其他会话长期占满）由
// 常驻清扫器 sweepPendingSessions 兜底，不在这里长等。
const (
	pendingKickAttempts = 3
	pendingKickInterval = 100 * time.Millisecond
)

// pendingSweepInterval 常驻清扫器的扫描周期：只处理「有排队消息且能拿到
// 会话锁」的会话，队列为空时零开销。它是排队消息不永久滞留的最终保证——
// 唤醒逻辑集中在入队路径时，限流饱和、锁释放临界区交错等情况都可能让消息
// 错过所有消费者。
const pendingSweepInterval = 10 * time.Second

// kickPending 后台兜底唤醒：确认会话仍有排队消息时尝试接管消费队列。
// 正常路径下持锁者仍在响应，短暂重试后放弃，开销可忽略（错过的会在
// 清扫器周期内被接管）。
func (p *AIChatPlugin) kickPending(b bot.Bot, id message.QID, isGroup bool) {
	key := sessionKey(id, isGroup)
	for attempt := 0; attempt < pendingKickAttempts; attempt++ {
		if !p.hasPending(key) {
			return // 队列已被其他消费者处理
		}
		if p.processPendingOnce(b, id, isGroup) {
			return
		}
		if attempt < pendingKickAttempts-1 {
			time.Sleep(pendingKickInterval)
		}
	}
}

// startPendingSweeper 启动常驻清扫器（与插件同生命周期，同 startChatJanitor 惯例）。
func (p *AIChatPlugin) startPendingSweeper() {
	go func() {
		ticker := time.NewTicker(pendingSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			p.sweepPendingSessions()
		}
	}()
}

// sweepPendingSessions 扫描所有会话的排队队列，尝试处理仍滞留的消息。
// 需要平台外观（chatEntry.bot）才能构造发送回调；拿不到外观的会话跳过，
// 其队列会在下次消息到达时由正常流程消费。
func (p *AIChatPlugin) sweepPendingSessions() {
	if !p.ready {
		return
	}
	p.pendingMsgs.Range(func(key, value any) bool {
		k, ok := key.(string)
		if !ok {
			return true
		}
		q, ok := value.(*pendingQueue)
		if !ok {
			return true
		}
		q.mu.Lock()
		pending := len(q.items) > 0
		q.mu.Unlock()
		if !pending {
			return true
		}
		id, isGroup, ok := parseSessionKey(k)
		if !ok {
			return true
		}
		v, ok := p.sessionBots.Load(k)
		if !ok {
			return true
		}
		b, ok := v.(bot.Bot)
		if !ok || b == nil {
			return true
		}
		// 每个会话独立协程接管：processChatBatch 是长耗时操作，同步执行会让
		// 10s 周期的 ticker 在接管期间不再触发，其余会话的滞留队列得不到兜底。
		// 会话锁保证同一会话不会被重复接管（拿不到锁的协程会立即返回）。
		pendingBot, pendingID, pendingGroup := b, id, isGroup
		b.Go("pending-sweep:"+k, func() {
			p.sweepSessionWithBudget(pendingBot, pendingID, pendingGroup)
		})
		return true
	})
}

// sweepSessionWithBudget 清扫器路径的单会话接管：给每次接管一个远小于消息处理
// 总预算的上限（pendingSweepPerSessionBudget），超时后由下一次 tick 继续，
// 避免单个繁忙会话长期占住锁与清扫器协程。
func (p *AIChatPlugin) sweepSessionWithBudget(b bot.Bot, id message.QID, isGroup bool) {
	deadline := time.Now().Add(pendingSweepPerSessionBudget)
	for {
		p.processPendingOnceWithDeadline(b, id, isGroup, deadline)
		if !p.hasPending(sessionKey(id, isGroup)) || time.Until(deadline) <= 0 {
			return
		}
	}
}

// pendingSweepPerSessionBudget 单次清扫里每个会话允许占用的最长时长。
const pendingSweepPerSessionBudget = 30 * time.Second

// parseSessionKey 解析 g:<id> / f:<id> 会话键（见 sessionKey）。
func parseSessionKey(key string) (message.QID, bool, bool) {
	if len(key) < 3 || key[1] != ':' {
		return "", false, false
	}
	switch key[0] {
	case 'g':
		return message.FromString(key[2:]), true, true
	case 'f':
		return message.FromString(key[2:]), false, true
	}
	return "", false, false
}

// processPendingOnce 尝试获取会话锁并处理排队消息；未拿到锁返回 false。
// 后台触发不属于任何消息事件，上下文按 p.msgEventTimeout 结算总预算（同主流程
// 的 eventBudget 语义：单次接管不会无限延长）；
// 队列为空时立即返回，不做任何事（等价于一次无消息触发的响应）。
func (p *AIChatPlugin) processPendingOnce(b bot.Bot, id message.QID, isGroup bool) bool {
	return p.processPendingOnceWithDeadline(b, id, isGroup, time.Now().Add(p.msgEventTimeout))
}

// processPendingOnceWithDeadline 按给定总预算截止时刻接管一次排队队列。
func (p *AIChatPlugin) processPendingOnceWithDeadline(b bot.Bot, id message.QID, isGroup bool, deadline time.Time) bool {
	lock := p.tryLock(id, isGroup)
	if lock == nil {
		return false
	}
	defer lock.release()

	chat, err := p.getChat(b, id, isGroup, p.getPromptForID(id, isGroup))
	if err != nil {
		p.drainPending(id, isGroup)
		p.Logger.Error("processPendingOnce: 无法获取 ChatBot", "error", err.Error(), "id", id, "is_group", isGroup)
		return true
	}

	// runCtx 承载 /stop 与锁丢失的取消信号；每批消息派生独立预算，累计受总预算约束
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	lock.attachCancel(cancelRun)

	batch := p.drainPending(id, isGroup)
	for len(batch) > 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			// 总预算耗尽：丢回队列交由清扫器下一次接管（避免占住锁与清扫器）
			p.Logger.Warn("后台接管总预算耗尽，剩余排队消息留待下次接管", "id", id, "is_group", isGroup, "pending", len(batch))
			break
		}
		p.setActiveContext(id, isGroup, &activeRequest{cancel: cancelRun, requester: batchRequester(batch)})
		batchCtx, cancelBatch := context.WithTimeout(runCtx, min(p.msgEventTimeout, remaining))
		ok := p.processChatBatch(batchCtx, b, id, isGroup, chat, batch)
		cancelBatch()
		p.clearActiveContext(id, isGroup)
		if !ok {
			p.drainPending(id, isGroup)
			return true
		}
		batch = p.drainPending(id, isGroup)
	}
	return true
}
