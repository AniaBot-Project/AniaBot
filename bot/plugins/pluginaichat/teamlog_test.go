package pluginaichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// newTeamLogTestPlugin 构造带后台任务表（内存 SQLite）的测试插件。
func newTeamLogTestPlugin(t *testing.T) *AIChatPlugin {
	t.Helper()
	store, db := newTaskTestStore(t)
	p := &AIChatPlugin{taskStore: store, historyDB: db}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return p
}

// ---- 团队运行终态 ----

func TestTeamRunOutcome(t *testing.T) {
	ok := teamMemberResult{label: "成功成员"}
	failed := teamMemberResult{label: "失败成员", err: errors.New("boom")}
	timeout := teamMemberResult{label: "超时成员", err: errors.New("子代理执行超时（1m0s），已中止")}
	cancelled := teamMemberResult{label: "取消成员", err: context.Canceled}

	cases := []struct {
		name       string
		results    []teamMemberResult
		ctxErr     error
		wantStatus string
		wantStop   string
	}{
		{"全部成功", []teamMemberResult{ok, ok}, nil, taskStatusCompleted, taskStopCompleted},
		{"部分失败仍算完成", []teamMemberResult{ok, failed}, nil, taskStatusCompleted, taskStopCompleted},
		{"全部失败", []teamMemberResult{failed, timeout}, nil, taskStatusFailed, taskStopError},
		{"无成员", nil, nil, taskStatusFailed, taskStopError},
		{"父上下文取消", []teamMemberResult{ok, cancelled}, context.Canceled, taskStatusCancelled, taskStopAborted},
		{"父上下文超时", []teamMemberResult{ok}, context.DeadlineExceeded, taskStatusTimeout, taskStopTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, stop := teamRunOutcome(c.results, c.ctxErr)
			if status != c.wantStatus || stop != c.wantStop {
				t.Fatalf("teamRunOutcome() = (%s, %s), want (%s, %s)", status, stop, c.wantStatus, c.wantStop)
			}
		})
	}
}

// ---- 工具调用步骤收集 ----

// TestSubagentTraceKeepsFullToolCalls 后台任务日志与 Query 日志同口径：工具参数/
// 结果完整保留、不做长度截断，也不限制明细条数（上限只在展示层折叠）。
func TestSubagentTraceKeepsFullToolCalls(t *testing.T) {
	tr := newSubagentTrace()
	args := strings.Repeat("参", tasklog.MaxArgsRunes+50)
	result := strings.Repeat("结", tasklog.MaxResultRunes+50)
	const calls = 60 // 超过历史实现的上限（50），验证不再丢弃
	for i := 0; i < calls; i++ {
		tr.observe(aichat.ToolCallInfo{Name: "bash", Arguments: args, Result: result, DurationMs: 7})
	}
	got, total := tr.snapshot()
	if len(got) != calls {
		t.Fatalf("应保留全部明细 %d 条, got %d", calls, len(got))
	}
	if total != calls {
		t.Fatalf("总调用数 %d, want %d", total, calls)
	}
	if got[0].Arguments != args {
		t.Fatalf("参数应完整保留: %d runes, want %d", len([]rune(got[0].Arguments)), len([]rune(args)))
	}
	if got[0].Result != result {
		t.Fatalf("结果应完整保留: %d runes, want %d", len([]rune(got[0].Result)), len([]rune(result)))
	}
	if got[0].DurationMs != 7 || got[0].Name != "bash" {
		t.Fatalf("工具明细字段丢失: %+v", got[0])
	}
}

// TestFinishSubagentTaskKeepsFullContent 任务指令 / 结果 / 错误完整入库，不做长度
// 截断（与 Query 日志同一口径）：面板详情与导出都能看到完整内容。
func TestFinishSubagentTaskKeepsFullContent(t *testing.T) {
	p := newTeamLogTestPlugin(t)
	longPrompt := strings.Repeat("任务说明", 3000) // 12000 rune，超过历史实现的 8000 上限
	longResult := strings.Repeat("结果", 3000)   // 6000 rune，超过历史实现的 4000 上限
	task := p.beginSubagentTask("t-full-0001", message.FromString("10001"), true, longPrompt, taskKindSubagent)
	if task == nil {
		t.Fatal("任务记录应创建")
	}
	p.finishSubagentTaskMeta(task, longResult, aichat.TokenUsage{TotalTokens: 10}, nil, "")

	got, ok, err := p.taskStore.Get(context.Background(), task.ID)
	if err != nil || !ok {
		t.Fatalf("读取任务失败: ok=%v err=%v", ok, err)
	}
	if got.Prompt != longPrompt {
		t.Fatalf("任务指令被截断: %d rune, want %d", len([]rune(got.Prompt)), len([]rune(longPrompt)))
	}
	if got.Result != longResult {
		t.Fatalf("任务结果被截断: %d rune, want %d", len([]rune(got.Result)), len([]rune(longResult)))
	}

	// 失败任务的错误同样完整保留
	longErr := errors.New(strings.Repeat("错误", 800))
	failed := p.beginSubagentTask("t-full-0002", message.FromString("10001"), true, "会失败的任务", taskKindSubagent)
	p.finishSubagentTaskMeta(failed, "", aichat.TokenUsage{}, longErr, "")
	got, ok, err = p.taskStore.Get(context.Background(), failed.ID)
	if err != nil || !ok {
		t.Fatalf("读取失败任务失败: ok=%v err=%v", ok, err)
	}
	if got.Status != taskStatusFailed || got.Error != longErr.Error() {
		t.Fatalf("失败错误应完整保留: status=%s len=%d", got.Status, len([]rune(got.Error)))
	}
}

func TestSubagentTraceNilSafe(t *testing.T) {
	var tr *subagentTrace
	tr.observe(aichat.ToolCallInfo{Name: "bash"}) // 不应 panic
	calls, total := tr.snapshot()
	if len(calls) != 0 || total != 0 {
		t.Fatalf("nil trace snapshot = (%v, %d), want (nil, 0)", calls, total)
	}
}

// flushRecord 一次执行中进度落盘回调的记录（测试用）。
type flushRecord struct {
	calls   []tasklog.ToolCallRecord
	total   int
	elapsed time.Duration
}

// TestSubagentTraceProgressThrottle 首次工具调用立即落盘（面板很快能看到进展），
// 间隔内的调用被节流，超过间隔后再次落盘；回调拿到的切片是副本。
func TestSubagentTraceProgressThrottle(t *testing.T) {
	base := time.Now()
	now := base
	var writes []flushRecord
	tr := newSubagentTrace()
	tr.start = base
	tr.now = func() time.Time { return now }
	tr.progress = func(calls []tasklog.ToolCallRecord, total int, elapsed time.Duration) {
		writes = append(writes, flushRecord{calls: calls, total: total, elapsed: elapsed})
	}

	tr.observe(aichat.ToolCallInfo{Name: "bash", Arguments: "ls", Result: "ok"})
	if len(writes) != 1 {
		t.Fatalf("首次调用应立即落盘, 实际 %d 次", len(writes))
	}
	if writes[0].total != 1 || len(writes[0].calls) != 1 || writes[0].calls[0].Name != "bash" {
		t.Fatalf("首次进度 = %+v, want 1 条 bash", writes[0])
	}

	now = base.Add(subagentProgressInterval - time.Millisecond)
	tr.observe(aichat.ToolCallInfo{Name: "bash"})
	if len(writes) != 1 {
		t.Fatalf("间隔内的调用应被节流, 实际落盘 %d 次", len(writes))
	}

	now = base.Add(subagentProgressInterval)
	tr.observe(aichat.ToolCallInfo{Name: "web_search"})
	if len(writes) != 2 {
		t.Fatalf("超过间隔应再次落盘, 实际 %d 次", len(writes))
	}
	if writes[1].total != 3 || len(writes[1].calls) != 3 || writes[1].elapsed != subagentProgressInterval {
		t.Fatalf("第二次进度 = %+v, want total=3 calls=3 elapsed=%s", writes[1], subagentProgressInterval)
	}
	// 副本：首次回调持有的切片不随后续 observe 增长
	if len(writes[0].calls) != 1 {
		t.Fatalf("进度回调拿到的是共享切片: 首次记录变成 %d 条", len(writes[0].calls))
	}
}

// TestSubagentProgressWrittenToTaskRow 执行中把工具步骤增量写进任务行 Meta：
// 面板轮询即可看到进度（状态仍 running、token 尚未回填）；收尾后终态覆盖，
// 迟到的进度写被 finished_at 条件拦下，不会把任务打回「运行中」。
func TestSubagentProgressWrittenToTaskRow(t *testing.T) {
	p := newTeamLogTestPlugin(t)
	qid := message.FromUint64(4004)
	task := p.beginSubagentTask(newTaskID(), qid, true, "调研任务", taskKindSubagent)
	if task == nil {
		t.Fatal("子代理任务行应已创建")
	}

	tr := newSubagentTrace()
	tr.progress = p.trackProgress(task)
	tr.observe(aichat.ToolCallInfo{Name: "web_search", Arguments: `{"q":"x"}`, Result: "命中 2 条", DurationMs: 12})

	ctx, cancel := p.taskDB()
	got, ok, err := p.taskStore.Get(ctx, task.ID)
	cancel()
	if err != nil || !ok {
		t.Fatalf("读回执行中任务失败: ok=%v err=%v", ok, err)
	}
	if got.Status != taskStatusRunning || got.Tokens != 0 || got.FinishedAt != 0 {
		t.Fatalf("执行中任务行状态异常: %+v", got)
	}
	var progress subagentRunMeta
	if err := json.Unmarshal([]byte(got.Meta), &progress); err != nil {
		t.Fatalf("进度 Meta 解析失败: %v (%q)", err, got.Meta)
	}
	if len(progress.ToolCalls) != 1 || progress.ToolCalls[0].Name != "web_search" || progress.ToolCallsTotal != 1 {
		t.Fatalf("执行中工具步骤 = %+v, want 1 条 web_search", progress)
	}

	// 面板查询路径（taskpanel.go 的 AgentTasks）同样能看到执行中的进度
	views := p.AgentTasks(10)
	var view *plugininfo.AgentTaskView
	for i := range views {
		if views[i].ID == task.ID {
			view = &views[i]
			break
		}
	}
	if view == nil {
		t.Fatalf("面板视图缺少执行中任务 %s", task.ID)
	}
	if view.Status != taskStatusRunning {
		t.Fatalf("面板视图状态 = %s, want running", view.Status)
	}
	var viewMeta subagentRunMeta
	if err := json.Unmarshal([]byte(view.Meta), &viewMeta); err != nil {
		t.Fatalf("面板视图 Meta 解析失败: %v (%q)", err, view.Meta)
	}
	if len(viewMeta.ToolCalls) != 1 || viewMeta.ToolCalls[0].Name != "web_search" {
		t.Fatalf("面板视图工具步骤 = %+v, want 1 条 web_search", viewMeta.ToolCalls)
	}

	// 收尾：终态写全量步骤与结果
	usage := aichat.TokenUsage{TotalTokens: 12, Iterations: 2}
	p.finishSubagentTaskMeta(task, "调研结论", usage, nil, subagentTraceMeta(usage, 2*time.Second, tr))

	// 迟到的进度写（收尾后仍有一次 flush）不得覆盖终态
	tr.progress([]tasklog.ToolCallRecord{{Name: "late"}}, 9, time.Minute)

	ctx, cancel = p.taskDB()
	final, ok, err := p.taskStore.Get(ctx, task.ID)
	cancel()
	if err != nil || !ok {
		t.Fatalf("读回终态任务失败: ok=%v err=%v", ok, err)
	}
	if final.Status != taskStatusCompleted || final.Result != "调研结论" || final.Tokens != 12 || final.FinishedAt == 0 {
		t.Fatalf("终态记录异常: %+v", final)
	}
	var finalMeta subagentRunMeta
	if err := json.Unmarshal([]byte(final.Meta), &finalMeta); err != nil {
		t.Fatalf("终态 Meta 解析失败: %v (%q)", err, final.Meta)
	}
	if len(finalMeta.ToolCalls) != 1 || finalMeta.ToolCalls[0].Name != "web_search" || finalMeta.Iterations != 2 {
		t.Fatalf("终态 Meta = %+v, want 1 条 web_search + iterations=2", finalMeta)
	}
}

// ---- 团队运行记录落库 ----

func TestTeamRunRecording(t *testing.T) {
	p := newTeamLogTestPlugin(t)
	qid := message.FromUint64(1001)

	parent := p.beginTeamRunTask(qid, true, "总体任务：调研并总结")
	if parent == nil {
		t.Fatal("团队运行父记录应已创建")
	}
	if parent.Kind != taskKindTeamRun || parent.Status != taskStatusRunning {
		t.Fatalf("父记录 = %s/%s, want %s/%s", parent.Kind, parent.Status, taskKindTeamRun, taskStatusRunning)
	}

	spec := teamMemberSpec{
		label:    "研究员",
		task:     "总体任务：调研并总结\n\n调研方向",
		degraded: "未识别的角色「研究员」，已按普通子代理执行",
	}
	member := p.beginTeamMemberTask(parent, qid, true, spec)
	if member == nil {
		t.Fatal("成员子记录应已创建")
	}
	if member.ParentID != parent.ID || member.Kind != taskKindTeamMember {
		t.Fatalf("成员记录 parent/kind = %q/%q, want %q/%q", member.ParentID, member.Kind, parent.ID, taskKindTeamMember)
	}

	trace := newSubagentTrace()
	trace.observe(aichat.ToolCallInfo{Name: "web_search", Arguments: `{"q":"AniaBot"}`, Result: "命中 3 条", DurationMs: 12})
	p.finishTeamMemberTask(member, spec, "成员产出正文",
		aichat.TokenUsage{TotalTokens: 100, Iterations: 2}, nil, 1500*time.Millisecond, trace)

	results := []teamMemberResult{
		{label: "研究员", result: "成员产出正文", usage: aichat.TokenUsage{TotalTokens: 100}, duration: 1500 * time.Millisecond},
		{label: "分析师", err: errors.New("模型调用失败"), usage: aichat.TokenUsage{TotalTokens: 50}, duration: 2 * time.Second},
	}
	p.finishTeamRunTask(parent, "汇总报告正文", results, 3500*time.Millisecond, nil)

	ctx, cancel := p.taskDB()
	defer cancel()

	got, ok, err := p.taskStore.Get(ctx, parent.ID)
	if err != nil || !ok {
		t.Fatalf("读回团队运行父记录失败: ok=%v err=%v", ok, err)
	}
	if got.Status != taskStatusCompleted || got.StopReason != taskStopCompleted {
		t.Fatalf("父记录状态 = %s/%s, want completed/completed", got.Status, got.StopReason)
	}
	if got.Tokens != 150 {
		t.Fatalf("父记录 token = %d, want 150（成员合计）", got.Tokens)
	}
	if !strings.Contains(got.Result, "汇总报告正文") {
		t.Fatalf("父记录结果未落库: %q", got.Result)
	}
	var runMeta teamRunMeta
	if err := json.Unmarshal([]byte(got.Meta), &runMeta); err != nil {
		t.Fatalf("父记录 Meta 解析失败: %v (%q)", err, got.Meta)
	}
	if runMeta.Success != 1 || runMeta.Failed != 1 || len(runMeta.Members) != 2 {
		t.Fatalf("成员摘要 = %+v, want success=1 failed=1 members=2", runMeta)
	}
	if runMeta.Members[1].Error == "" || runMeta.Members[1].Status != taskStatusFailed {
		t.Fatalf("失败成员摘要缺失: %+v", runMeta.Members[1])
	}

	gotMember, ok, err := p.taskStore.Get(ctx, member.ID)
	if err != nil || !ok {
		t.Fatalf("读回成员记录失败: ok=%v err=%v", ok, err)
	}
	if gotMember.Status != taskStatusCompleted || gotMember.Tokens != 100 || gotMember.ParentID != parent.ID {
		t.Fatalf("成员记录 = %s/%d/%q, want completed/100/%q", gotMember.Status, gotMember.Tokens, gotMember.ParentID, parent.ID)
	}
	if gotMember.Prompt != spec.task {
		t.Fatalf("成员任务指令 = %q, want %q", gotMember.Prompt, spec.task)
	}
	var memberMeta subagentRunMeta
	if err := json.Unmarshal([]byte(gotMember.Meta), &memberMeta); err != nil {
		t.Fatalf("成员 Meta 解析失败: %v (%q)", err, gotMember.Meta)
	}
	if memberMeta.Name != "研究员" || memberMeta.Iterations != 2 || memberMeta.DurationMs != 1500 {
		t.Fatalf("成员 Meta = %+v, want name=研究员 iterations=2 duration=1500", memberMeta)
	}
	if len(memberMeta.ToolCalls) != 1 || memberMeta.ToolCalls[0].Name != "web_search" || memberMeta.ToolCallsTotal != 1 {
		t.Fatalf("成员工具步骤 = %+v, want 1 条 web_search", memberMeta.ToolCalls)
	}
	if memberMeta.Degraded != spec.degraded {
		t.Fatalf("成员降级说明 = %q, want %q", memberMeta.Degraded, spec.degraded)
	}
}

// 成员记录不落库：父记录未创建（taskStore 降级）时不得产生孤儿行。
func TestTeamRunRecordingDegradesWithoutStore(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	if got := p.beginTeamRunTask(message.FromUint64(1), true, "任务"); got != nil {
		t.Fatalf("taskStore 不可用时父记录应为 nil, got %+v", got)
	}
	if got := p.beginTeamMemberTask(nil, message.FromUint64(1), true, teamMemberSpec{label: "成员"}); got != nil {
		t.Fatalf("父记录缺失时成员记录应为 nil, got %+v", got)
	}
}

// 成员记录并发写入：真实 team_run 的 N 个成员同时开始/结束，SQLite 单连接下
// 也必须按 3 秒独立上下文串行完成，不丢行、不卡住。
func TestTeamRunConcurrentMembers(t *testing.T) {
	p := newTeamLogTestPlugin(t)
	qid := message.FromUint64(3003)
	parent := p.beginTeamRunTask(qid, true, "并发任务")
	if parent == nil {
		t.Fatal("父记录应已创建")
	}

	const members = 5
	var wg sync.WaitGroup
	for i := 0; i < members; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spec := teamMemberSpec{label: fmt.Sprintf("成员%d", i), task: "并发任务\n\n分工"}
			member := p.beginTeamMemberTask(parent, qid, true, spec)
			trace := newSubagentTrace()
			trace.observe(aichat.ToolCallInfo{Name: "bash", Result: "ok"})
			p.finishTeamMemberTask(member, spec, "产出", aichat.TokenUsage{TotalTokens: 10}, nil, time.Second, trace)
		}(i)
	}
	wg.Wait()

	ctx, cancel := p.taskDB()
	defer cancel()
	rows, err := p.taskStore.ListBySession(ctx, sessionKey(qid, true), 50)
	if err != nil {
		t.Fatalf("列出并发成员记录失败: %v", err)
	}
	if len(rows) != members+1 {
		t.Fatalf("任务行数 = %d, want %d（父记录 + %d 个成员）", len(rows), members+1, members)
	}
	for _, row := range rows {
		if row.Kind == taskKindTeamMember && (row.Status != taskStatusCompleted || row.ParentID != parent.ID) {
			t.Fatalf("成员记录异常: %+v", row)
		}
	}
}

// ---- 面板视图字段 ----

func TestAgentTasksViewIncludesRunFields(t *testing.T) {
	p := newTeamLogTestPlugin(t)
	qid := message.FromUint64(2002)

	parent := p.beginTeamRunTask(qid, false, "团队总体任务")
	spec := teamMemberSpec{label: "编辑", task: "团队总体任务\n\n润色"}
	member := p.beginTeamMemberTask(parent, qid, false, spec)
	if parent == nil || member == nil {
		t.Fatal("运行记录应已创建")
	}
	p.finishTeamMemberTask(member, spec, "成稿", aichat.TokenUsage{TotalTokens: 10}, nil, time.Second, nil)

	views := p.AgentTasks(20)
	var gotParent, gotMember *plugininfo.AgentTaskView
	for i := range views {
		switch views[i].ID {
		case parent.ID:
			gotParent = &views[i]
		case member.ID:
			gotMember = &views[i]
		}
	}
	if gotParent == nil || gotMember == nil {
		t.Fatalf("面板视图缺少运行记录: parent=%v member=%v", gotParent, gotMember)
	}
	if gotParent.Prompt != "团队总体任务" || gotParent.ParentID != "" {
		t.Fatalf("父记录视图 = %+v, want prompt=团队总体任务 parent=空", gotParent)
	}
	if gotMember.Kind != taskKindTeamMember || gotMember.ParentID != parent.ID || gotMember.Prompt != spec.task {
		t.Fatalf("成员视图 = %+v, want kind=%s parent=%s prompt=%q", gotMember, taskKindTeamMember, parent.ID, spec.task)
	}
}
