package pluginaichat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// addFinishedForTest 在会话工作组里登记一个已完成条目（绕过 LLM 执行）。
func addFinishedForTest(p *AIChatPlugin, id message.QID, isGroup bool, entry asyncSubagentEntry, result string, err error) {
	p.getAsyncGroup(id, isGroup).addEntry(entry)
	p.finishAsyncSubagent(id, isGroup, entry.id, result, err)
}

// TestFinishedSubagentRetention 完成后条目在 TTL 内保留：不再算运行中，但
// subagent_list 可见、可按 ID/名字定位并追问；过期后自动摘除。
func TestFinishedSubagentRetention(t *testing.T) {
	p := newRunnerTestPlugin()
	p.cfg.Subagent.FinishedTTLMin = 30
	id := message.FromUint64(1101)

	steer := make(chan string, 1)
	p.getAsyncGroup(id, true).addEntry(asyncSubagentEntry{
		id: "t-done", name: "writer", kind: taskKindSubagent, task: "写一份调研报告", steer: steer,
	})
	if got := len(p.listRunningSubagents(id, true)); got != 1 {
		t.Fatalf("运行中条目应有 1 个, got %d", got)
	}

	p.finishAsyncSubagent(id, true, "t-done", "报告正文", nil)
	if got := len(p.listRunningSubagents(id, true)); got != 0 {
		t.Fatalf("完成后不算运行中, got %d", got)
	}
	entries := p.listSessionWork(id, true, true)
	if len(entries) != 1 || !entries[0].finished || entries[0].result != "报告正文" {
		t.Fatalf("完成条目未保留: %+v", entries)
	}
	if entries[0].cancel != nil || entries[0].steer != nil {
		t.Fatal("完成条目不应保留 cancel/steer")
	}
	// 按 ID 与名字都能定位；完成条目不再受 send/cancel 影响
	if e, ok := p.lookupSessionWorkEntry(id, true, "writer"); !ok || e.id != "t-done" {
		t.Fatalf("按名字定位完成条目失败: %+v ok=%v", e, ok)
	}
	if p.sendSubagentMessage(id, true, "t-done", "还能追加吗") {
		t.Fatal("完成条目不应接受运行中追加指令")
	}
	if p.cancelSubagentByID(id, true, "t-done") {
		t.Fatal("完成条目不应被取消")
	}

	// 过期后视为不存在，并顺手摘除
	g := p.lookupAsyncGroup(id, true)
	g.mu.Lock()
	g.entries[0].finishedAt = time.Now().Add(-31 * time.Minute)
	g.mu.Unlock()
	if _, ok := p.lookupSessionWorkEntry(id, true, "t-done"); ok {
		t.Fatal("超过 TTL 的完成条目不应可追问")
	}
	if g := p.lookupAsyncGroup(id, true); g != nil && len(g.snapshot()) != 0 {
		t.Fatalf("过期条目应被摘除: %+v", g.snapshot())
	}
}

// TestFinishedSubagentTTLDisabled TTL=0（关闭追问窗口）时完成后直接移除，
// 行为与旧版一致。
func TestFinishedSubagentTTLDisabled(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1102)
	p.getAsyncGroup(id, true).addEntry(asyncSubagentEntry{id: "t-gone", task: "任务"})
	p.finishAsyncSubagent(id, true, "t-gone", "结果", nil)
	if got := p.listSessionWork(id, true, true); len(got) != 0 {
		t.Fatalf("TTL=0 时不应保留完成条目: %+v", got)
	}
	if p.lookupAsyncGroup(id, true) != nil {
		t.Fatal("空组应被回收")
	}
}

// TestPruneFinishedSubagents 常驻清理器只清理过期完成条目，运行中条目保留。
func TestPruneFinishedSubagents(t *testing.T) {
	p := newRunnerTestPlugin()
	p.cfg.Subagent.FinishedTTLMin = 30
	id := message.FromUint64(1103)
	p.getAsyncGroup(id, true).addEntries([]asyncSubagentEntry{
		{id: "t-running", task: "运行中"},
		{id: "t-old", task: "旧任务"},
		{id: "t-new", task: "新任务"},
	})
	p.finishAsyncSubagent(id, true, "t-old", "旧结果", nil)
	p.finishAsyncSubagent(id, true, "t-new", "新结果", nil)

	g := p.lookupAsyncGroup(id, true)
	g.mu.Lock()
	for i := range g.entries {
		if g.entries[i].id == "t-old" {
			g.entries[i].finishedAt = time.Now().Add(-time.Hour)
		}
	}
	g.mu.Unlock()

	p.pruneFinishedSubagents(30 * time.Minute)
	entries := p.listSessionWork(id, true, true)
	if len(entries) != 2 {
		t.Fatalf("应保留运行中 + 未过期条目 2 个, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.id == "t-old" {
			t.Fatal("过期条目应被清理")
		}
	}
}

// TestFollowupMaxPerRoot 追问上限按追问链根任务统计：达到上限后拒绝，
// 启动失败可归还配额，根条目/子条目都清理后计数一并删除。
func TestFollowupMaxPerRoot(t *testing.T) {
	g := &asyncSubagentGroup{}
	if !g.reserveFollowup("root", 2) || !g.reserveFollowup("root", 2) {
		t.Fatal("前两次追问应放行")
	}
	if g.reserveFollowup("root", 2) {
		t.Fatal("达到上限后应拒绝")
	}
	if got := g.followupCount("root"); got != 2 {
		t.Fatalf("追问次数 = %d, want 2", got)
	}
	g.releaseFollowup("root")
	if !g.reserveFollowup("root", 2) {
		t.Fatal("归还配额后应可再次追问")
	}

	// 根与子条目都不存在后，计数不再占用内存
	g.addEntries([]asyncSubagentEntry{{id: "root"}, {id: "child", rootID: "root"}})
	g.removeIfEmpty("child")
	if got := g.followupCount("root"); got != 2 {
		t.Fatalf("根仍存在时计数应保留, got %d", got)
	}
	g.removeIfEmpty("root")
	if got := g.followupCount("root"); got != 0 {
		t.Fatalf("全部条目清理后计数应删除, got %d", got)
	}
}

// TestBuildSubagentFollowupTask 追问任务必须包含原任务、上一轮结果与追问，
// 并明确要求「不要整段重做」；空结果有占位说明。
func TestBuildSubagentFollowupTask(t *testing.T) {
	got := buildSubagentFollowupTask("分析舆情", "共 1700 字报告", "评论区提到哪个点位？")
	for _, want := range []string{"【子代理追问】", "原任务", "分析舆情", "上一轮结果", "共 1700 字报告", "追加追问", "评论区提到哪个点位", "不要整段重做"} {
		if !strings.Contains(got, want) {
			t.Fatalf("追问任务缺少 %q:\n%s", want, got)
		}
	}
	empty := buildSubagentFollowupTask("任务", "  ", "问题")
	if !strings.Contains(empty, "上一轮没有返回文本结果") {
		t.Fatalf("空结果应有占位说明:\n%s", empty)
	}
}

// TestFollowupSubagentRejects 未找到 / 运行中 / 无结果 / 功能关闭时给出可读
// 提示；功能关闭的启动失败会归还追问配额。
func TestFollowupSubagentRejects(t *testing.T) {
	p := newRunnerTestPlugin()
	p.cfg.Subagent.FinishedTTLMin = 30
	id := message.FromUint64(1104)

	out := p.followupSubagent(context.Background(), nil, id, true, "missing", "追问", 0, llmtool.CallBackFuncs{})
	if !strings.Contains(out, "未找到可追问") {
		t.Fatalf("不存在的 ID 应返回未找到, got %q", out)
	}

	p.getAsyncGroup(id, true).addEntry(asyncSubagentEntry{id: "t-running", task: "任务"})
	out = p.followupSubagent(context.Background(), nil, id, true, "t-running", "追问", 0, llmtool.CallBackFuncs{})
	if !strings.Contains(out, "仍在运行中") {
		t.Fatalf("运行中条目应提示用 subagent_send, got %q", out)
	}

	p.finishAsyncSubagent(id, true, "t-running", "", errors.New("模型失败"))
	out = p.followupSubagent(context.Background(), nil, id, true, "t-running", "追问", 0, llmtool.CallBackFuncs{})
	if !strings.Contains(out, "未产出可追问的结果") {
		t.Fatalf("无结果的完成条目应拒绝追问, got %q", out)
	}

	// 有结果但子代理功能关闭：启动失败并归还配额
	p.getAsyncGroup(id, true).addEntry(asyncSubagentEntry{id: "t-ok", task: "任务"})
	p.finishAsyncSubagent(id, true, "t-ok", "可用的结果", nil)
	p.cfg.Subagent.Enable = false
	out = p.followupSubagent(context.Background(), nil, id, true, "t-ok", "追问", 0, llmtool.CallBackFuncs{})
	if !strings.Contains(out, "子代理功能未启用") {
		t.Fatalf("功能关闭时应返回明确原因, got %q", out)
	}
	if got := p.lookupAsyncGroup(id, true).followupCount("t-ok"); got != 0 {
		t.Fatalf("启动失败应归还追问配额, got %d", got)
	}
}

// TestSubagentListShowsFinished subagent_list(include_finished=true) 展示完成
// 条目、剩余追问窗口与 followup 调用示例。
func TestSubagentListShowsFinished(t *testing.T) {
	p := newRunnerTestPlugin()
	p.cfg.Subagent.FinishedTTLMin = 30
	id := message.FromUint64(1105)
	addFinishedForTest(p, id, true, asyncSubagentEntry{
		id: "t-finished", label: "研究员", kind: taskKindTeamMember,
		task: "调研任务", parentID: "t-team",
	}, "调研结果", nil)

	tools := newSubagentTools(p, nil, id, true)
	var listTool llmtool.Tool
	for _, tl := range tools {
		if tl.Name() == "subagent_list" {
			listTool = tl
			break
		}
	}
	if listTool == nil {
		t.Fatal("未找到 subagent_list")
	}
	out, err := listTool.Execute(context.Background(), &subagentListParams{IncludeFinished: true}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("subagent_list err = %v", err)
	}
	for _, want := range []string{"可追问", "t-finished", "团队成员", "父任务: t-team", "剩余", "subagent_followup"} {
		if !strings.Contains(out, want) {
			t.Fatalf("subagent_list 输出缺少 %q:\n%s", want, out)
		}
	}
}

// TestSubagentSendDegradesToFollowup 对已完成条目调用 subagent_send 时自动走
// 追问语义（而非只回「未找到」）；功能关闭时返回明确原因。
func TestSubagentSendDegradesToFollowup(t *testing.T) {
	p := newRunnerTestPlugin()
	p.cfg.Subagent.FinishedTTLMin = 30
	id := message.FromUint64(1106)
	addFinishedForTest(p, id, true, asyncSubagentEntry{id: "t-done", task: "原任务"}, "原结果", nil)

	var sendTool llmtool.Tool
	for _, tl := range newSubagentTools(p, nil, id, true) {
		if tl.Name() == "subagent_send" {
			sendTool = tl
			break
		}
	}
	if sendTool == nil {
		t.Fatal("未找到 subagent_send")
	}
	out, err := sendTool.Execute(context.Background(), &subagentSendParams{ID: "t-done", Message: "再问一句"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("subagent_send err = %v", err)
	}
	if !strings.Contains(out, "子代理功能未启用") {
		t.Fatalf("已完成条目应自动转追问并返回启动结果, got %q", out)
	}
	if got := p.lookupAsyncGroup(id, true).followupCount("t-done"); got != 0 {
		t.Fatalf("启动失败应归还追问配额, got %d", got)
	}
}

// TestTeamMemberAddressingIDs 团队成员 ID 由团队运行 ID 派生且稳定可寻址；
// 汇总报告与运行 Meta 都带上成员 ID，便于 followup 精确定位。
func TestTeamMemberAddressingIDs(t *testing.T) {
	ids := teamMemberIDs("t-run", 3)
	want := []string{"t-run-m1", "t-run-m2", "t-run-m3"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("成员 ID = %v, want %v", ids, want)
	}

	report := buildTeamReport([]teamMemberResult{
		{id: "t-run-m1", label: "研究员", result: "结论"},
		{id: "t-run-m2", label: "分析师", err: errors.New("失败")},
	}, time.Second)
	for _, wantID := range []string{"ID: t-run-m1", "ID: t-run-m2", "研究员", "分析师"} {
		if !strings.Contains(report, wantID) {
			t.Fatalf("报告缺少 %q:\n%s", wantID, report)
		}
	}

	meta := teamRunMetaFrom([]teamMemberResult{{id: "t-run-m1", label: "研究员", usage: aichat.TokenUsage{TotalTokens: 10}}}, time.Second)
	if len(meta.Members) != 1 || meta.Members[0].ID != "t-run-m1" {
		t.Fatalf("成员摘要缺少 ID: %+v", meta.Members)
	}
}

// followupTestBot 只执行子代理 goroutine，忽略 pending kick（测试不需要运行器）。
type followupTestBot struct {
	*recordBot
	wg sync.WaitGroup
}

func (b *followupTestBot) Go(name string, f func()) {
	if strings.HasPrefix(name, "subagent:") {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			f()
		}()
	}
}

// TestFollowupReservationConcurrentSafe 多个 followup 同时抢占同一根任务的
// 配额时不会超发；并发 markFinished/removeIfEmpty 不破坏组状态。
func TestFollowupReservationConcurrentSafe(t *testing.T) {
	g := &asyncSubagentGroup{}
	g.addEntries([]asyncSubagentEntry{{id: "root"}, {id: "child", rootID: "root"}})

	const workers = 32
	const maxFollowups = 3
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.reserveFollowup("root", maxFollowups) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != maxFollowups {
		t.Fatalf("并发追问配额 = %d, want %d", got, maxFollowups)
	}
	if got := g.followupCount("root"); got != maxFollowups {
		t.Fatalf("追问计数 = %d, want %d", got, maxFollowups)
	}

	// 并发完成标记与移除：不 panic、不残留错误状态
	var wg2 sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg2.Add(2)
		go func() {
			defer wg2.Done()
			g.markFinished("child", "结果", "")
		}()
		go func() {
			defer wg2.Done()
			g.removeIfEmpty("child")
		}()
	}
	wg2.Wait()
	if got := g.followupCount("root"); got != maxFollowups {
		t.Fatalf("子条目清理后根仍存在，计数应保留, got %d", got)
	}
	g.removeIfEmpty("root")
	if got := g.followupCount("root"); got != 0 {
		t.Fatalf("根清理后计数应删除, got %d", got)
	}
}

// TestFollowupSubagentRunsWithParentContext 端到端：对一个已完成子代理发起追问，
// 新子代理收到的任务包含原任务/上一轮结果/追问，完成后以 followup 任务行记录
// 父任务 ID，并把带父 ID 的完成通知回灌会话。
func TestFollowupSubagentRunsWithParentContext(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		fakePlainChatReply(w, "评论区提到 3 号点位")
	}))
	defer srv.Close()

	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = testLogger()
	p.toolExecutor = llmtool.NewToolExecuter()
	p.cfg.Subagent.Enable = true
	p.cfg.Subagent.FinishedTTLMin = 30
	p.cfg.Subagent.MaxFollowupsPerTask = 3

	id := message.FromUint64(1201)
	p.getAsyncGroup(id, true).addEntry(asyncSubagentEntry{
		id: "t-base", kind: taskKindSubagent, task: "调研赞哦集市舆情",
	})
	p.finishAsyncSubagent(id, true, "t-base", "上一轮结论：共 1700 字报告", nil)

	rb := &followupTestBot{recordBot: &recordBot{}}
	out := p.followupSubagent(context.Background(), rb, id, true, "t-base", "补给帖评论区提到哪个点位？", 0, llmtool.CallBackFuncs{})
	if !strings.Contains(out, "已按追问重启") || !strings.Contains(out, "新任务 ID") {
		t.Fatalf("追问启动提示异常: %q", out)
	}
	rb.wg.Wait()

	// 新条目：kind=followup，父任务指向原条目，结果已保留
	entries := p.listSessionWork(id, true, true)
	var child asyncSubagentEntry
	for _, e := range entries {
		if e.kind == taskKindFollowup {
			child = e
		}
	}
	if child.id == "" || !child.finished || child.parentID != "t-base" || child.rootID != "t-base" {
		t.Fatalf("追问条目异常: %+v", child)
	}
	if !strings.Contains(child.result, "评论区提到 3 号点位") {
		t.Fatalf("追问结果未保留: %q", child.result)
	}
	if got := p.lookupAsyncGroup(id, true).followupCount("t-base"); got != 1 {
		t.Fatalf("追问计数 = %d, want 1", got)
	}

	// 子代理实际收到的任务包含原任务、上一轮结果与追问
	mu.Lock()
	allBodies := strings.Join(bodies, "\n")
	mu.Unlock()
	for _, want := range []string{"调研赞哦集市舆情", "上一轮结论：共 1700 字报告", "补给帖评论区提到哪个点位"} {
		if !strings.Contains(allBodies, want) {
			t.Fatalf("子代理请求缺少 %q:\n%s", want, allBodies)
		}
	}

	// 完成通知回灌会话，并带父任务 ID，便于主 AI 串起追问链
	pending := p.drainPending(id, true)
	if len(pending) != 1 {
		t.Fatalf("应回灌 1 条完成通知, got %d", len(pending))
	}
	text := pending[0].Message[0].Data["text"].(string)
	for _, want := range []string{"【子代理执行完成】", "状态=completed", "父任务ID: t-base", "评论区提到 3 号点位"} {
		if !strings.Contains(text, want) {
			t.Fatalf("完成通知缺少 %q:\n%s", want, text)
		}
	}
}

// TestOnTeamCompleteNotification 团队完成后回灌消息包含任务 ID、成员 ID/状态、
// 汇总报告与追问提示；部分成员失败时标题用「执行结束」。
func TestOnTeamCompleteNotification(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1302)
	specs := []teamMemberSpec{{label: "研究员"}, {label: "分析师"}}
	memberIDs := []string{"t-team-m1", "t-team-m2"}
	results := []teamMemberResult{
		{id: memberIDs[0], label: "研究员", result: "调研结论"},
		{id: memberIDs[1], label: "分析师", err: errors.New("模型失败")},
	}
	report := buildTeamReport(results, time.Second)
	p.onTeamComplete(noGoBot{&recordBot{}}, id, true, "t-team", "整体任务", specs, memberIDs, results, report)

	pending := p.drainPending(id, true)
	if len(pending) != 1 {
		t.Fatalf("应回灌 1 条团队完成通知, got %d", len(pending))
	}
	text := pending[0].Message[0].Data["text"].(string)
	for _, want := range []string{
		"【Agent 团队执行完成】", "状态=completed", "任务ID: t-team",
		"研究员（t-team-m1·完成）", "分析师（t-team-m2·失败）",
		"调研结论", "subagent_followup",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("团队完成通知缺少 %q:\n%s", want, text)
		}
	}

	// 全部失败：标题改为「执行结束」，状态 failed
	allFailed := []teamMemberResult{
		{id: memberIDs[0], label: "研究员", err: errors.New("失败")},
		{id: memberIDs[1], label: "分析师", err: errors.New("失败")},
	}
	p.onTeamComplete(noGoBot{&recordBot{}}, id, true, "t-team2", "任务", specs, memberIDs, allFailed,
		buildTeamReport(allFailed, time.Second))
	pending = p.drainPending(id, true)
	if len(pending) != 1 {
		t.Fatalf("应回灌 1 条团队失败通知, got %d", len(pending))
	}
	text = pending[0].Message[0].Data["text"].(string)
	for _, want := range []string{"【Agent 团队执行结束】", "状态=failed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("团队失败通知缺少 %q:\n%s", want, text)
		}
	}
}

// teamTestBot 执行团队/子代理 goroutine，忽略 pending kick（测试直接检查队列）。
type teamTestBot struct {
	*recordBot
	wg sync.WaitGroup
}

func (b *teamTestBot) Go(name string, f func()) {
	if strings.HasPrefix(name, "team:") || strings.HasPrefix(name, "subagent:") {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			f()
		}()
	}
}

// TestAsyncTeamCompletesAndMemberFollowup 端到端：team_run 异步启动后成员并行执行，
// 任务行使用可寻址 ID（团队行=runID，成员行=<runID>-mN），完成后回灌带成员 ID 的
// 汇总；对单个成员 followup 会生成 parent_id 指向成员任务行的 followup 任务行。
func TestAsyncTeamCompletesAndMemberFollowup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fakePlainChatReply(w, "成员产出")
	}))
	defer srv.Close()

	p := newTeamLogTestPlugin(t)
	p.toolExecutor = llmtool.NewToolExecuter()
	p.cfg.BaseURL = srv.URL
	p.cfg.APIKey = "k"
	p.cfg.Model = "m"
	p.cfg.MaxContextTokens = 128000
	p.cfg.Subagent.Enable = true
	p.cfg.Subagent.FinishedTTLMin = 30

	id := message.FromUint64(1401)
	specs := []teamMemberSpec{{label: "研究员", task: "调研"}, {label: "分析师", task: "分析"}}
	bot := &teamTestBot{recordBot: &recordBot{}}
	out, err := p.launchAsyncTeam(context.Background(), bot, id, true, "整体任务", 0, specs, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("launchAsyncTeam err = %v", err)
	}
	if !strings.Contains(out, "Agent 团队已启动") {
		t.Fatalf("启动提示异常: %q", out)
	}
	bot.wg.Wait()

	// 后台工作表：团队与成员均完成，成员 ID 可寻址
	entries := p.listSessionWork(id, true, true)
	var runID string
	finishedMembers := 0
	for _, e := range entries {
		switch e.kind {
		case taskKindTeamRun:
			runID = e.id
			if !e.finished || !strings.Contains(e.result, "成员产出") {
				t.Fatalf("团队完成条目异常: %+v", e)
			}
		case taskKindTeamMember:
			if !e.finished || e.parentID != runID {
				t.Fatalf("成员完成条目异常: %+v", e)
			}
			finishedMembers++
		}
	}
	if runID == "" || finishedMembers != 2 {
		t.Fatalf("团队/成员完成状态异常: runID=%q members=%d entries=%+v", runID, finishedMembers, entries)
	}

	// 任务表：父/成员行的主键与可寻址 ID 一致
	ctx, cancel := p.taskDB()
	rows, err := p.taskStore.ListBySession(ctx, sessionKey(id, true), 20)
	cancel()
	if err != nil {
		t.Fatalf("ListBySession err = %v", err)
	}
	seen := map[string]*agentTask{}
	for _, row := range rows {
		seen[row.ID] = row
	}
	parent := seen[runID]
	if parent == nil || parent.Kind != taskKindTeamRun || parent.Status != taskStatusCompleted {
		t.Fatalf("团队任务行异常: %+v", parent)
	}
	for _, mid := range []string{runID + "-m1", runID + "-m2"} {
		row := seen[mid]
		if row == nil || row.Kind != taskKindTeamMember || row.ParentID != runID || row.Status != taskStatusCompleted {
			t.Fatalf("成员任务行 %s 异常: %+v", mid, row)
		}
	}

	// 完成通知回灌，带团队与成员 ID
	pending := p.drainPending(id, true)
	if len(pending) != 1 {
		t.Fatalf("应回灌 1 条团队完成通知, got %d", len(pending))
	}
	text := pending[0].Message[0].Data["text"].(string)
	for _, want := range []string{"【Agent 团队执行完成】", "任务ID: " + runID, runID + "-m1", runID + "-m2", "subagent_followup"} {
		if !strings.Contains(text, want) {
			t.Fatalf("团队完成通知缺少 %q:\n%s", want, text)
		}
	}

	// 对单个成员追问：新任务行 kind=followup、parent_id 指向成员任务行
	memberID := runID + "-m1"
	out = p.followupSubagent(context.Background(), bot, id, true, memberID, "再补充一点", 0, llmtool.CallBackFuncs{})
	if !strings.Contains(out, "已按追问重启") {
		t.Fatalf("成员追问启动失败: %q", out)
	}
	bot.wg.Wait()

	ctx, cancel = p.taskDB()
	rows, err = p.taskStore.ListBySession(ctx, sessionKey(id, true), 30)
	cancel()
	if err != nil {
		t.Fatalf("ListBySession after followup err = %v", err)
	}
	var followupRow *agentTask
	for _, row := range rows {
		if row.Kind == taskKindFollowup {
			if followupRow != nil {
				t.Fatalf("单次追问应只有 1 条 followup 行, got %d", len(rows))
			}
			followupRow = row
		}
	}
	if followupRow == nil || followupRow.ParentID != memberID || followupRow.Status != taskStatusCompleted {
		t.Fatalf("追问任务行异常: %+v", followupRow)
	}
}

// TestLaunchAsyncTeamRegistersAddressableMembers 异步团队启动后，团队与每个成员
// 都在会话后台工作表里可寻址：团队 ID 广播指令，成员 ID/名字只发给该成员，
// /stop 团队 ID 可取消整队并从团队登记表移除。
func TestLaunchAsyncTeamRegistersAddressableMembers(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1301)
	specs := []teamMemberSpec{
		{label: "研究员", task: "调研"},
		{label: "分析师", task: "分析"},
	}
	out, err := p.launchAsyncTeam(context.Background(), noGoBot{&recordBot{}}, id, true,
		"整体任务", 0, specs, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("launchAsyncTeam err = %v", err)
	}
	if !strings.Contains(out, "Agent 团队已启动") || !strings.Contains(out, "成员 ID") {
		t.Fatalf("启动提示异常: %q", out)
	}

	entries := p.listRunningSubagents(id, true)
	if len(entries) != 3 {
		t.Fatalf("应登记团队 + 2 个成员, got %d: %+v", len(entries), entries)
	}
	var runEntry asyncSubagentEntry
	members := make([]asyncSubagentEntry, 0, 2)
	for _, e := range entries {
		switch e.kind {
		case taskKindTeamRun:
			runEntry = e
		case taskKindTeamMember:
			members = append(members, e)
		}
	}
	if runEntry.id == "" || len(members) != 2 {
		t.Fatalf("团队/成员条目异常: run=%+v members=%+v", runEntry, members)
	}
	if members[0].id != runEntry.id+"-m1" || members[1].id != runEntry.id+"-m2" {
		t.Fatalf("成员 ID 应由团队 ID 派生: %q / %q", members[0].id, members[1].id)
	}
	if members[0].parentID != runEntry.id || members[0].steer == nil {
		t.Fatalf("成员条目缺少父 ID/steer: %+v", members[0])
	}

	// 团队 ID 广播给全部成员
	if !p.sendSubagentMessage(id, true, runEntry.id, "全队注意") {
		t.Fatal("团队 ID 应可广播指令")
	}
	for _, m := range members {
		select {
		case got := <-m.steer:
			if got != "全队注意" {
				t.Fatalf("成员收到的广播 = %q", got)
			}
		default:
			t.Fatalf("成员 %s 未收到广播", m.id)
		}
	}
	// 成员 ID 与唯一展示名都只发给该成员
	if !p.sendSubagentMessage(id, true, "研究员", "只看这一项") {
		t.Fatal("成员展示名应可寻址（唯一时）")
	}
	select {
	case got := <-members[0].steer:
		if got != "只看这一项" {
			t.Fatalf("成员收到 = %q", got)
		}
	default:
		t.Fatal("研究员未收到指令")
	}
	select {
	case got := <-members[1].steer:
		t.Fatalf("其他成员不应收到定向指令: %q", got)
	default:
	}

	// /stop 团队 ID 取消整队并清理团队登记
	if !p.cancelSubagentByID(id, true, runEntry.id) {
		t.Fatal("团队 ID 应可取消")
	}
	if _, ok := p.teamRuns.Load(sessionKey(id, true)); ok {
		t.Fatal("取消后团队运行登记应被清理")
	}
	p.cancelAsyncSubagents(id, true)
}
