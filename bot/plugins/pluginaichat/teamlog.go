package pluginaichat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// Agent 运行记录：团队（team_run）与子代理每次执行在后台任务表（ania_agent_task）
// 落行，供面板「后台任务」页查看完整执行过程。两条路径共用同一套约定：
//
//   - team_run 是父记录（kind=team_run），Title/Prompt 为总体任务，Result 为汇总报告，
//     Tokens 为成员合计，Meta 存成员摘要（teamRunMeta）；
//   - 每个团队成员是一条子记录（kind=team_member，任务行主键即成员可寻址 ID
//     <runID>-mN，ParentID 指向父记录），Result 为成员产出，Meta 存角色降级说明、
//     LLM 轮数、耗时与工具调用步骤（subagentRunMeta）；
//   - 异步子代理沿用 kind=subagent/handoff/followup，Meta 同样补执行细节与工具步骤
//     （workflow 只记汇总行，步骤明细在汇总结果里）；追问行的 ParentID 指向原任务。
//
// 这是「旁路观测」：taskStore 未启用（SQL 不可用）时所有 helper 返回 nil 并静默跳过，
// 不影响团队/子代理执行；写入走 tasktrack.go 的 3 秒独立上下文，DB 卡住不阻塞回合。

// subagentProgressInterval 执行中增量落盘的最小间隔。工具观察回调是串行的，每次
// 工具完成都整行写一次 DB 代价偏高；按 3 秒节流后，面板「后台任务」页 10 秒轮询
// 仍能近乎实时地看到进度，写入量则封顶为每任务每 3 秒一次。首次工具调用不等间隔、
// 立即落一次，保证任务启动后很快能看到第一步。
const subagentProgressInterval = 3 * time.Second

// teamRunMeta team_run 父记录 Meta JSON 的约定：整体耗时与成员摘要，面板无需
// 关联子记录即可展示「每个成员是否成功、用了多少 token、耗时多久」。
type teamRunMeta struct {
	DurationMs int64               `json:"duration_ms,omitempty"`
	Success    int                 `json:"success"`
	Failed     int                 `json:"failed"`
	Members    []teamMemberSummary `json:"members,omitempty"`
}

// teamMemberSummary 团队运行中单个成员的结果摘要（写入 teamRunMeta.Members）。
type teamMemberSummary struct {
	ID         string `json:"id,omitempty"` // 成员可寻址 ID（<runID>-mN）
	Name       string `json:"name"`
	Status     string `json:"status"` // 任务状态（completed/failed/timeout/cancelled）
	Tokens     int64  `json:"tokens,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Degraded   string `json:"degraded,omitempty"` // 角色解析降级说明
	Error      string `json:"error,omitempty"`    // 失败/超时的可读原因（完整记录）
}

// subagentRunMeta 子代理/团队成员的 Meta JSON 约定：执行细节与工具调用步骤。
// 与 Query 日志口径一致：工具参数/结果完整记录，不做长度截断（面板前端负责折叠
// 与按需展开），保证后台任务详情可以作为完整执行日志复核。
type subagentRunMeta struct {
	Name           string                   `json:"name,omitempty"` // 团队成员名（普通子代理为空）
	Degraded       string                   `json:"degraded,omitempty"`
	Iterations     int                      `json:"iterations,omitempty"`
	DurationMs     int64                    `json:"duration_ms,omitempty"`
	ToolCalls      []tasklog.ToolCallRecord `json:"tool_calls,omitempty"`
	ToolCallsTotal int                      `json:"tool_calls_total,omitempty"` // 兼容旧记录：历史数据可能因上限截断而大于 len(ToolCalls)
}

// subagentTrace 收集一次子代理执行期间的工具调用明细。工具观察者由编排层串行
// 回调（同一轮并行工具也在互斥锁内串行调用），因此不需要额外加锁；Chat 返回后
// 由调用方读取，存在明确的 happens-before。
type subagentTrace struct {
	calls []tasklog.ToolCallRecord
	total int

	// progress 执行中增量落盘回调（nil 表示不落盘）：按 flushEvery 节流，把
	// 「已完成的工具步骤 + 总数 + 已耗时」交给调用方写进任务行 Meta。与 Query
	// 日志的 onToolCall 增量写同一思路，只是多了时间节流以控制写放大。
	progress   func(calls []tasklog.ToolCallRecord, total int, elapsed time.Duration)
	flushEvery time.Duration
	lastFlush  time.Time
	start      time.Time
	now        func() time.Time // 测试可注入；nil 用 time.Now
}

// newSubagentTrace 创建一个空的执行步骤收集器（nil 表示不收集）。
// 开始时间在此记录，执行中增量落盘用它算已耗时（与终态 Meta 的 duration_ms 同口径）。
func newSubagentTrace() *subagentTrace {
	return &subagentTrace{flushEvery: subagentProgressInterval, start: time.Now()}
}

// observe 工具调用观察回调：追加一条执行记录（参数/结果完整保留，与 Query 日志
// 一致，不做截断），并按节流间隔把已有进度增量写进任务行。
func (t *subagentTrace) observe(info aichat.ToolCallInfo) {
	if t == nil {
		return
	}
	t.total++
	rec := tasklog.ToolCallRecord{
		Name:       info.Name,
		Arguments:  info.Arguments,
		Result:     info.Result,
		DurationMs: info.DurationMs,
	}
	if info.Err != nil {
		rec.Error = info.Err.Error()
	}
	t.calls = append(t.calls, rec)
	t.flushProgress()
}

// flushProgress 按 flushEvery 节流，把已完成步骤增量写进任务行（无 progress 回调
// 时是空操作）。写入由回调同步完成（3 秒独立上下文），因此进度写严格有序，不存在
// 「旧快照覆盖新快照」；回调拿到切片副本，不会被后续 append 改写。
func (t *subagentTrace) flushProgress() {
	if t == nil || t.progress == nil {
		return
	}
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	if !t.lastFlush.IsZero() && now.Sub(t.lastFlush) < t.flushEvery {
		return
	}
	t.lastFlush = now
	calls, total := t.snapshot()
	var elapsed time.Duration
	if !t.start.IsZero() {
		elapsed = now.Sub(t.start)
	}
	t.progress(append([]tasklog.ToolCallRecord(nil), calls...), total, elapsed)
}

// snapshot 返回收集到的明细与总调用数（含被上限丢弃的）；nil 收集器返回空。
func (t *subagentTrace) snapshot() ([]tasklog.ToolCallRecord, int) {
	if t == nil {
		return nil, 0
	}
	return t.calls, t.total
}

// subagentProgressMeta 构造执行中的进度 Meta JSON（已耗时 + 已完成的工具步骤）。
// LLM 轮数只有收尾时才知道，进度里留空，面板按 duration_ms 展示「执行中」。
func subagentProgressMeta(elapsed time.Duration, calls []tasklog.ToolCallRecord, total int) string {
	return encodeRunMeta(subagentRunMeta{
		DurationMs:     elapsed.Milliseconds(),
		ToolCalls:      calls,
		ToolCallsTotal: total,
	})
}

// trackProgress 返回子代理/团队成员执行中的进度落盘回调（挂到 subagentTrace.progress），
// 把已完成的工具步骤与已耗时写进任务行 Meta。taskStore 不可用或任务行未建成（nil）
// 时返回 nil，trace 侧自动跳过。
//
// 与终态写入同一约定：走 taskDB 的 3 秒独立上下文，DB 卡住不阻塞工具循环；
// UpdateProgress 只改 meta/updated_at 且带 finished_at = 0 条件，任务收尾后迟到的
// 进度写不会覆盖终态（面板不会看到「已完成的任务又变回运行中」）。
func (p *AIChatPlugin) trackProgress(task *agentTask) func(calls []tasklog.ToolCallRecord, total int, elapsed time.Duration) {
	if p.taskStore == nil || task == nil {
		return nil
	}
	return func(calls []tasklog.ToolCallRecord, total int, elapsed time.Duration) {
		ctx, cancel := p.taskDB()
		defer cancel()
		if _, err := p.taskStore.UpdateProgress(ctx, task.ID, subagentProgressMeta(elapsed, calls, total)); err != nil {
			p.Logger.Debug("写入子代理执行进度失败", "task", task.ID, "error", err.Error())
		}
	}
}

// encodeRunMeta 编码运行记录 Meta JSON；结构不可能编码失败，失败时返回空串
// （任务行照常落盘，只是面板看不到结构化细节）。
func encodeRunMeta(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// beginTeamRunTask 创建一次团队运行的父记录（Status=running）。taskStore 未启用时
// 返回 nil，此时成员记录也一并跳过。runID 可选：异步团队运行由调用方预先生成 ID
// （成员 ID 以它为前缀），缺省时自动生成。
func (p *AIChatPlugin) beginTeamRunTask(id message.QID, isGroup bool, task string, runID ...string) *agentTask {
	if p.taskStore == nil {
		return nil
	}
	now := time.Now().Unix()
	taskID := newTaskID()
	if len(runID) > 0 && strings.TrimSpace(runID[0]) != "" {
		taskID = strings.TrimSpace(runID[0])
	}
	t := &agentTask{
		ID:         taskID,
		SessionKey: sessionKey(id, isGroup),
		Kind:       taskKindTeamRun,
		Status:     taskStatusRunning,
		Title:      truncateTaskTitle(task),
		Prompt:     task,
		CreatedAt:  now,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Create(ctx, t); err != nil {
		p.Logger.Warn("记录 Agent 团队运行失败", "error", err.Error())
		return nil
	}
	return t
}

// finishTeamRunTask 更新团队运行父记录的终态：汇总报告、成员合计 token、总耗时与
// 成员摘要。ctxErr 为父上下文状态（/stop 取消或回合硬截止），用于区分 cancelled/timeout。
func (p *AIChatPlugin) finishTeamRunTask(t *agentTask, report string, results []teamMemberResult, duration time.Duration, ctxErr error) {
	if p.taskStore == nil || t == nil {
		return
	}
	now := time.Now().Unix()
	t.Status, t.StopReason = teamRunOutcome(results, ctxErr)
	t.Result = report
	t.Tokens = teamRunTokens(results)
	t.Meta = encodeRunMeta(teamRunMetaFrom(results, duration))
	t.FinishedAt = now
	t.UpdatedAt = now
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Update(ctx, t); err != nil {
		p.Logger.Warn("更新 Agent 团队运行记录失败", "task", t.ID, "error", err.Error())
	}
}

// beginTeamMemberTask 创建一个团队成员的子记录；父记录未建成（nil）时不落库：
// 孤儿成员行没有归属，面板上也看不出属于哪次团队运行。
// memberID 可选：异步团队运行传入可寻址 ID（<runID>-mN）作为任务行主键，
// 使后台工作表、报告与面板任务行使用同一个句柄，followup 的父 ID 也能落库串联。
func (p *AIChatPlugin) beginTeamMemberTask(parent *agentTask, id message.QID, isGroup bool, spec teamMemberSpec, memberID ...string) *agentTask {
	if p.taskStore == nil || parent == nil {
		return nil
	}
	now := time.Now().Unix()
	taskID := newTaskID()
	if len(memberID) > 0 && strings.TrimSpace(memberID[0]) != "" {
		taskID = strings.TrimSpace(memberID[0])
	}
	t := &agentTask{
		ID:         taskID,
		SessionKey: sessionKey(id, isGroup),
		ParentID:   parent.ID,
		Kind:       taskKindTeamMember,
		Status:     taskStatusRunning,
		Title:      truncateTaskTitle(spec.label),
		Prompt:     spec.task,
		CreatedAt:  now,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Create(ctx, t); err != nil {
		p.Logger.Warn("记录团队成员运行失败", "member", spec.label, "error", err.Error())
		return nil
	}
	return t
}

// finishTeamMemberTask 更新成员子记录的终态、产出、token、耗时与执行步骤。
func (p *AIChatPlugin) finishTeamMemberTask(t *agentTask, spec teamMemberSpec, result string,
	usage aichat.TokenUsage, runErr error, duration time.Duration, trace *subagentTrace) {
	if p.taskStore == nil || t == nil {
		return
	}
	now := time.Now().Unix()
	t.Tokens = int64(usage.TotalTokens)
	t.Status, t.StopReason = taskRunStatus(runErr)
	switch {
	case runErr == nil:
		t.Result = result
	case t.Status == taskStatusTimeout:
		t.Error = runErr.Error()
	case t.Status == taskStatusFailed:
		t.Error = runErr.Error()
	}
	calls, total := trace.snapshot()
	t.Meta = encodeRunMeta(subagentRunMeta{
		Name:           spec.label,
		Degraded:       spec.degraded,
		Iterations:     usage.Iterations,
		DurationMs:     duration.Milliseconds(),
		ToolCalls:      calls,
		ToolCallsTotal: total,
	})
	t.FinishedAt = now
	t.UpdatedAt = now
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Update(ctx, t); err != nil {
		p.Logger.Warn("更新团队成员运行记录失败", "task", t.ID, "error", err.Error())
	}
}

// subagentTraceMeta 构造异步子代理的 Meta JSON（轮数、耗时与工具步骤）。
func subagentTraceMeta(usage aichat.TokenUsage, duration time.Duration, trace *subagentTrace) string {
	calls, total := trace.snapshot()
	return encodeRunMeta(subagentRunMeta{
		Iterations:     usage.Iterations,
		DurationMs:     duration.Milliseconds(),
		ToolCalls:      calls,
		ToolCallsTotal: total,
	})
}

// teamRunTokens 成员 token 用量合计（父记录的 Tokens）。
func teamRunTokens(results []teamMemberResult) int64 {
	var total int64
	for _, r := range results {
		total += int64(r.usage.TotalTokens)
	}
	return total
}

// teamRunMetaFrom 汇总成员摘要；成功/失败计数与报告头行口径一致（有产出即成功）。
func teamRunMetaFrom(results []teamMemberResult, duration time.Duration) teamRunMeta {
	meta := teamRunMeta{
		DurationMs: duration.Milliseconds(),
		Members:    make([]teamMemberSummary, 0, len(results)),
	}
	for _, r := range results {
		status, _ := taskRunStatus(r.err)
		if status == taskStatusCompleted {
			meta.Success++
		} else {
			meta.Failed++
		}
		sum := teamMemberSummary{
			ID:         r.id,
			Name:       r.label,
			Status:     status,
			Tokens:     int64(r.usage.TotalTokens),
			DurationMs: r.duration.Milliseconds(),
			Degraded:   r.degraded,
		}
		if r.err != nil {
			sum.Error = r.err.Error()
		}
		meta.Members = append(meta.Members, sum)
	}
	return meta
}

// teamRunOutcome 推断团队运行的终态与停止原因：父上下文取消/超时优先；全部成员
// 失败才算 failed，部分失败仍记为 completed（失败数在报告与 Meta 中体现）。
func teamRunOutcome(results []teamMemberResult, ctxErr error) (status, stopReason string) {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return taskStatusTimeout, taskStopTimeout
	case ctxErr != nil:
		return taskStatusCancelled, taskStopAborted
	case len(results) == 0:
		return taskStatusFailed, taskStopError
	}
	for _, r := range results {
		if r.err == nil {
			return taskStatusCompleted, taskStopCompleted
		}
	}
	return taskStatusFailed, taskStopError
}
