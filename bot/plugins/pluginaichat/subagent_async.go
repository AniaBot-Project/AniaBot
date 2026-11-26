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
	name      string // 可选展示名（subagent_run name 参数）；空则只用 ID
	cancel    context.CancelFunc
	task      string
	startTime time.Time
	// steer 运行中追加指令通道（subagent_send）：orchestrator 在每次 LLM 请求前
	// 排空它，把内容作为 [追加指令] 用户消息注入子代理上下文。
	steer chan string
}

// asyncSubagentGroup 单个会话的异步子代理集合（支持多子代理并发）
type asyncSubagentGroup struct {
	mu      sync.Mutex
	entries []asyncSubagentEntry
}

func (g *asyncSubagentGroup) add(id string, cancel context.CancelFunc, task string) {
	g.addEntry(asyncSubagentEntry{id: id, cancel: cancel, task: task})
}

// addEntry 加入一个条目（时间戳在此统一填充）。
func (g *asyncSubagentGroup) addEntry(e asyncSubagentEntry) {
	if e.startTime.IsZero() {
		e.startTime = time.Now()
	}
	g.mu.Lock()
	g.entries = append(g.entries, e)
	g.mu.Unlock()
}

// nameTaken 判断名字是否已被本会话运行中的子代理占用（空名不占位）。
func (g *asyncSubagentGroup) nameTaken(name string) bool {
	if name == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		if e.name == name {
			return true
		}
	}
	return false
}

// find 按 ID 或名字查找运行中的子代理条目。
func (g *asyncSubagentGroup) find(idOrName string) (asyncSubagentEntry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		if e.id == idOrName || (e.name != "" && e.name == idOrName) {
			return e, true
		}
	}
	return asyncSubagentEntry{}, false
}

// send 向运行中的子代理追加一条指令；返回是否找到目标。
func (g *asyncSubagentGroup) send(idOrName, msg string) bool {
	e, ok := g.find(idOrName)
	if !ok || e.steer == nil {
		return false
	}
	select {
	case e.steer <- msg:
		return true
	default:
		// 缓冲满：丢弃新指令而不是阻塞工具调用；调用方会得到"队列满"提示。
		return false
	}
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

// cancelSubagentByID 按 ID 或展示名取消单个运行中的异步子代理（供 subagent_cancel 工具使用）
func (p *AIChatPlugin) cancelSubagentByID(id message.QID, isGroup bool, idOrName string) bool {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return false
	}
	e, ok := g.find(idOrName)
	if !ok {
		return false
	}
	return g.cancelOne(e.id)
}

// sendSubagentMessage 向运行中的子代理追加一条指令（steering），返回是否送达。
// 指令由子代理的 aichat 编排层在下次 LLM 请求前注入为 user 消息。
func (p *AIChatPlugin) sendSubagentMessage(id message.QID, isGroup bool, idOrName, msg string) bool {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return false
	}
	return g.send(idOrName, msg)
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

// launchSubagentOptions 异步子代理启动参数。
type launchSubagentOptions struct {
	// Name 可选展示名：1-32 位小写字母/数字/连字符、以字母开头，会话内运行中唯一。
	Name string
	// Kind 任务类型：taskKindSubagent / taskKindHandoff / taskKindWorkflow。
	Kind string
	// TimeoutSec 单次超时覆盖（秒，0=默认）。
	TimeoutSec int
	// Callbacks 父会话工具回调（子代理显式发送消息/图片等能力沿用父会话）。
	Callbacks llmtool.CallBackFuncs
}

// validSubagentName 校验子代理展示名：1-32 位、小写字母开头，只含小写字母/数字/连字符。
func validSubagentName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && ((r >= '0' && r <= '9') || r == '-'):
		default:
			return false
		}
	}
	return true
}

// launchAsyncSubagent 在后台 goroutine 中启动子代理，立即返回占位文本。
//
// ctx 为主请求上下文：仅用于检查父回合剩余预算。
// 子代理的实际执行使用独立的 context.WithCancel(context.Background()) ——
// 无 deadline，让 runSubagent 内部的 resolveSubagentTimeout + WithTimeout 自行管理
// 超时；/stop 通过 asyncSubagentGroup 注册的 cancel 取消。
func (p *AIChatPlugin) launchAsyncSubagent(
	ctx context.Context,
	b bot.Bot,
	id message.QID,
	isGroup bool,
	task string,
	opts launchSubagentOptions,
) string {
	if !p.cfg.Subagent.Enable {
		return "子代理功能未启用（plugin.ai_chat_bot.subagent.enable=false）"
	}
	if opts.Kind == "" {
		opts.Kind = taskKindSubagent
	}
	// 展示名校验与会话内唯一性
	if opts.Name != "" {
		if !validSubagentName(opts.Name) {
			return fmt.Sprintf("子代理名字 %q 不合法：需 1-32 位、小写字母开头，只含小写字母/数字/连字符", opts.Name)
		}
		if p.getAsyncGroup(id, isGroup).nameTaken(opts.Name) {
			return fmt.Sprintf("名字 %q 已被当前会话运行中的子代理占用，请换一个名字或用 subagent_list 查看", opts.Name)
		}
	}
	// 检查父请求剩余预算（父回合有硬截止；不足时 resolve 会缩短而非拒绝）
	if _, err := resolveSubagentTimeout(p.subagentTimeout(), opts.TimeoutSec, ctx); err != nil {
		return fmt.Sprintf("无法启动子代理: %v", err)
	}
	// 每会话并发上限：超出直接拒绝（不静默排队，避免 AI 反复重试堆积）
	if maxPer := p.cfg.Subagent.MaxConcurrentPerSession; maxPer > 0 && len(p.listRunningSubagents(id, isGroup)) >= maxPer {
		return fmt.Sprintf("当前会话后台子代理已达上限（%d 个），请等待现有任务完成，或用 subagent_list 查看、subagent_cancel 取消", maxPer)
	}
	// 全局并发池：与对话回合的 rate_limit 分开，后台任务不挤占正常对话
	if p.subagentSem != nil {
		select {
		case p.subagentSem <- struct{}{}:
		default:
			return "后台子代理并发已满，请稍后再试（或等待现有任务完成）"
		}
	}

	launchID := newTaskID()
	subTask := p.beginSubagentTask(launchID, id, isGroup, task, opts.Kind)
	// steering 通道：subagent_send 写入，子代理每次 LLM 请求前排空注入
	steer := make(chan string, 8)

	// 无 deadline 的 cancel context：runSubagent 内部自行通过 resolveSubagentTimeout
	// 创建带 timeout 的子 context，外部 cancel（/stop）不影响超时计算，且
	// asyncCtx.Err() == nil 让 runSubagent 能正确区分「子代理自身超时」和「外部取消」
	asyncCtx, asyncCancel := context.WithCancel(context.Background())
	// 关联插件生命周期：卸载/关停时取消在跑的后台子代理
	var stopShutdown func() bool
	if p.runCtx != nil {
		stopShutdown = context.AfterFunc(p.runCtx, asyncCancel)
	}

	p.getAsyncGroup(id, isGroup).addEntry(asyncSubagentEntry{
		id:    launchID,
		name:  opts.Name,
		task:  task,
		steer: steer,
		cancel: func() {
			if stopShutdown != nil {
				stopShutdown()
			}
			asyncCancel()
		},
	})

	b.Go("subagent:"+sessionKey(id, isGroup)+":"+launchID, func() {
		if p.subagentSem != nil {
			defer func() { <-p.subagentSem }()
		}
		defer p.removeAsyncSubagent(id, isGroup, launchID)
		// 执行步骤观测：子代理的工具调用随任务记录落库，面板「后台任务」页可展开查看。
		// 执行中按 3 秒节流增量回写（面板轮询即可看到进度），结束时再写终态全量。
		trace := newSubagentTrace()
		trace.progress = p.trackProgress(subTask)
		started := time.Now()
		result, usage, runErr := p.runSubagentWithOptions(asyncCtx, b, id, isGroup, task,
			subagentRunOptions{timeoutSec: opts.TimeoutSec, steer: steer, trace: trace}, opts.Callbacks)
		p.finishSubagentTaskMeta(subTask, result, usage, runErr, subagentTraceMeta(usage, time.Since(started), trace))
		// 消耗计入会话与全局配额（发起方已做前置检查，这里只累加不重复拒绝）
		p.quotaManager.Add(sessionKey(id, isGroup), usage)
		// 异步子代理完成时主请求通常已结束，其消耗暂存到会话级累计器，
		// 由该会话下一次 finishQuery 并入 Query 日志（/stop 取消的也照常计入）
		p.addExtraUsage(sessionKey(id, isGroup), usage)
		p.onSubagentComplete(b, id, isGroup, launchID, opts.Name, task, result, runErr)
	})

	display := launchID
	if opts.Name != "" {
		display = opts.Name + "（ID: " + launchID + "）"
	}
	return fmt.Sprintf("✅ 子代理已启动（%s），正在后台执行任务", display)
}

// onSubagentComplete 子代理完成后的回调（在子代理 goroutine 中执行）。
//
// 1. 格式化结果文本（第一行是机器可读的状态/任务 ID/名称，便于主 AI 判断）
// 2. 构造合成消息并入队 pending 队列
// 3. 若会话空闲则自动触发 AI 处理
func (p *AIChatPlugin) onSubagentComplete(b bot.Bot, id message.QID, isGroup bool, launchID, name, task, result string, err error) {
	var text string
	nameLine := ""
	if name != "" {
		nameLine = "\n名称: " + name
	}
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "超时"):
			text = fmt.Sprintf("【子代理执行结束】状态=timeout\n任务ID: %s%s\n任务: %s\n错误: %s", launchID, nameLine, truncateTaskTitle(task), err.Error())
		case errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "canceled"):
			p.Logger.Info("异步子代理已被取消", "id", id, "is_group", isGroup, "launch_id", launchID, "task", task)
			return // 被 /stop 取消，不注入结果
		default:
			text = fmt.Sprintf("【子代理执行结束】状态=failed\n任务ID: %s%s\n任务: %s\n错误: %s", launchID, nameLine, truncateTaskTitle(task), err.Error())
		}
	} else {
		// 结果长度治理：超长结论落盘为会话私有文件，合成消息只带摘要 + handle，
		// 主 AI 用 read_tool_output 按需读取（避免超长子代理结果撑爆上下文；
		// 旧实现"结果不截断"会把数万字直接塞进 user 消息）
		result = p.toolResultLimiter.LimitAt(sessionKey(id, isGroup), "subagent_result", result, p.cfg.Subagent.ResultInlineRunes, false)
		text = fmt.Sprintf("【子代理执行完成】状态=completed\n任务ID: %s%s\n任务: %s\n\n%s", launchID, nameLine, truncateTaskTitle(task), result)
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
	p.runQueued(b, id, isGroup)
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
		// 每个会话独立协程接管：回合是长耗时操作，同步执行会让 10s 周期的
		// ticker 在接管期间不再触发，其余会话的滞留队列得不到兜底。
		// 会话锁保证同一会话不会被重复接管（拿不到锁的协程会立即返回）。
		pendingBot, pendingID, pendingGroup := b, id, isGroup
		b.Go("pending-sweep:"+k, func() {
			p.runQueued(pendingBot, pendingID, pendingGroup)
		})
		return true
	})
}

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
