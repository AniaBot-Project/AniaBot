package pluginaichat

import (
	"context"
	"encoding/json"
	"errors"
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
//   - 每个团队成员是一条子记录（kind=team_member，ParentID 指向父记录），Result 为
//     成员产出，Meta 存角色降级说明、LLM 轮数、耗时与工具调用步骤（subagentRunMeta）；
//   - 异步子代理沿用 kind=subagent/handoff，Meta 同样补执行细节与工具步骤
//     （workflow 只记汇总行，步骤明细在汇总结果里）。
//
// 这是「旁路观测」：taskStore 未启用（SQL 不可用）时所有 helper 返回 nil 并静默跳过，
// 不影响团队/子代理执行；写入走 tasktrack.go 的 3 秒独立上下文，DB 卡住不阻塞回合。

// subagentTraceMaxCalls 单条运行记录保留的工具调用明细上限。后台任务接口一次返回
// 最近 100 行、Meta 随行返回，而面板展开详情时逐条渲染：步骤再多也不会提高可诊断性，
// 反而撑大响应体积，因此单独限制得比定时任务日志（200 条）更小。
const subagentTraceMaxCalls = 50

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
	Name       string `json:"name"`
	Status     string `json:"status"` // 任务状态（completed/failed/timeout/cancelled）
	Tokens     int64  `json:"tokens,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Degraded   string `json:"degraded,omitempty"` // 角色解析降级说明
	Error      string `json:"error,omitempty"`    // 失败/超时的可读原因（截断）
}

// subagentRunMeta 子代理/团队成员的 Meta JSON 约定：执行细节与工具调用步骤。
// 工具参数/结果按 tasklog 的字段上限截断（与定时任务执行日志保持一致）。
type subagentRunMeta struct {
	Name           string                   `json:"name,omitempty"` // 团队成员名（普通子代理为空）
	Degraded       string                   `json:"degraded,omitempty"`
	Iterations     int                      `json:"iterations,omitempty"`
	DurationMs     int64                    `json:"duration_ms,omitempty"`
	ToolCalls      []tasklog.ToolCallRecord `json:"tool_calls,omitempty"`
	ToolCallsTotal int                      `json:"tool_calls_total,omitempty"` // 明细被上限截断时大于 len(ToolCalls)
}

// subagentTrace 收集一次子代理执行期间的工具调用明细。工具观察者由编排层串行
// 回调（同一轮并行工具也在互斥锁内串行调用），因此不需要额外加锁；Chat 返回后
// 由调用方读取，存在明确的 happens-before。
type subagentTrace struct {
	calls []tasklog.ToolCallRecord
	total int
}

// newSubagentTrace 创建一个空的执行步骤收集器（nil 表示不收集）。
func newSubagentTrace() *subagentTrace {
	return &subagentTrace{}
}

// observe 工具调用观察回调：追加一条执行记录，明细最多保留 subagentTraceMaxCalls 条。
func (t *subagentTrace) observe(info aichat.ToolCallInfo) {
	if t == nil {
		return
	}
	t.total++
	if len(t.calls) >= subagentTraceMaxCalls {
		return
	}
	rec := tasklog.ToolCallRecord{
		Name:       info.Name,
		Arguments:  tasklog.Truncate(info.Arguments, tasklog.MaxArgsRunes),
		Result:     tasklog.Truncate(info.Result, tasklog.MaxResultRunes),
		DurationMs: info.DurationMs,
	}
	if info.Err != nil {
		rec.Error = info.Err.Error()
	}
	t.calls = append(t.calls, rec)
}

// snapshot 返回收集到的明细与总调用数（含被上限丢弃的）；nil 收集器返回空。
func (t *subagentTrace) snapshot() ([]tasklog.ToolCallRecord, int) {
	if t == nil {
		return nil, 0
	}
	return t.calls, t.total
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
// 返回 nil，此时成员记录也一并跳过。
func (p *AIChatPlugin) beginTeamRunTask(id message.QID, isGroup bool, task string) *agentTask {
	if p.taskStore == nil {
		return nil
	}
	now := time.Now().Unix()
	t := &agentTask{
		ID:         newTaskID(),
		SessionKey: sessionKey(id, isGroup),
		Kind:       taskKindTeamRun,
		Status:     taskStatusRunning,
		Title:      truncateTaskTitle(task),
		Prompt:     truncateTaskPrompt(task),
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
	t.Result = truncateTaskResult(report)
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
func (p *AIChatPlugin) beginTeamMemberTask(parent *agentTask, id message.QID, isGroup bool, spec teamMemberSpec) *agentTask {
	if p.taskStore == nil || parent == nil {
		return nil
	}
	now := time.Now().Unix()
	t := &agentTask{
		ID:         newTaskID(),
		SessionKey: sessionKey(id, isGroup),
		ParentID:   parent.ID,
		Kind:       taskKindTeamMember,
		Status:     taskStatusRunning,
		Title:      truncateTaskTitle(spec.label),
		Prompt:     truncateTaskPrompt(spec.task),
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
		t.Result = truncateTaskResult(result)
	case t.Status == taskStatusTimeout:
		t.Error = runErr.Error()
	case t.Status == taskStatusFailed:
		t.Error = truncateTaskTitle(runErr.Error())
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
			Name:       r.label,
			Status:     status,
			Tokens:     int64(r.usage.TotalTokens),
			DurationMs: r.duration.Milliseconds(),
			Degraded:   r.degraded,
		}
		if r.err != nil {
			sum.Error = truncateTaskTitle(r.err.Error())
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
