package pluginaichat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// workflowTestPlan 构造一个固定的小型 DAG：a、b 无依赖，c 依赖 a+b，d 依赖 c。
func workflowTestPlan(t *testing.T) *workflowPlan {
	t.Helper()
	plan, err := validateWorkflowSteps([]workflowStepParam{
		{ID: "a", Task: "步骤 A"},
		{ID: "b", Task: "步骤 B"},
		{ID: "c", Task: "步骤 C", DependsOn: []string{"a", "b"}},
		{ID: "d", Task: "步骤 D", DependsOn: []string{"c"}},
	})
	if err != nil {
		t.Fatalf("构造测试计划失败: %v", err)
	}
	return plan
}

// ---- DAG 校验 ----

func TestWorkflowValidateSteps(t *testing.T) {
	nineSteps := func() []workflowStepParam {
		steps := make([]workflowStepParam, 0, 9)
		for i := 0; i < 9; i++ {
			steps = append(steps, workflowStepParam{ID: fmt.Sprintf("s%d", i), Task: "任务"})
		}
		return steps
	}
	tests := []struct {
		name    string
		steps   []workflowStepParam
		wantErr string
	}{
		{name: "空步骤", steps: nil, wantErr: "至少需要 1 个步骤"},
		{name: "超过 8 步", steps: nineSteps(), wantErr: "最多支持 8 个步骤"},
		{name: "空 ID", steps: []workflowStepParam{{ID: " ", Task: "x"}}, wantErr: "id 不能为空"},
		{name: "非法 ID 字符", steps: []workflowStepParam{{ID: "a b", Task: "x"}}, wantErr: "不合法"},
		{name: "超长 ID", steps: []workflowStepParam{{ID: strings.Repeat("a", workflowStepIDMaxLen+1), Task: "x"}}, wantErr: "不合法"},
		{name: "重复 ID", steps: []workflowStepParam{{ID: "a", Task: "x"}, {ID: "a", Task: "y"}}, wantErr: "重复"},
		{name: "空 task", steps: []workflowStepParam{{ID: "a", Task: "  "}}, wantErr: "task 不能为空"},
		{name: "空依赖 ID", steps: []workflowStepParam{
			{ID: "a", Task: "x"},
			{ID: "b", Task: "y", DependsOn: []string{" "}},
		}, wantErr: "空 ID"},
		{name: "未知依赖", steps: []workflowStepParam{
			{ID: "a", Task: "x", DependsOn: []string{"ghost"}},
		}, wantErr: "不存在的步骤"},
		{name: "自依赖", steps: []workflowStepParam{
			{ID: "a", Task: "x", DependsOn: []string{"a"}},
		}, wantErr: "不能依赖自己"},
		{name: "依赖环", steps: []workflowStepParam{
			{ID: "a", Task: "x", DependsOn: []string{"b"}},
			{ID: "b", Task: "y", DependsOn: []string{"a"}},
		}, wantErr: "存在环"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := validateWorkflowSteps(tt.steps)
			if err == nil {
				t.Fatalf("期望校验失败（%s），实际通过: plan=%+v", tt.wantErr, plan)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("错误文案 = %q，应包含 %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestWorkflowValidateStepsValid 合法 DAG 应产出拓扑序与依赖关系；重复依赖去重。
func TestWorkflowValidateStepsValid(t *testing.T) {
	plan, err := validateWorkflowSteps([]workflowStepParam{
		{ID: "collect", Task: "收集资料"},
		{ID: "review", Task: "复核资料", DependsOn: []string{"collect", "collect"}},
		{ID: "write", Task: "撰写报告", DependsOn: []string{"review"}},
		{ID: "extra", Task: "补充数据"},
	})
	if err != nil {
		t.Fatalf("合法 DAG 校验失败: %v", err)
	}
	if got, want := len(plan.steps), 4; got != want {
		t.Fatalf("步骤数 = %d, want %d", got, want)
	}
	if got, want := len(plan.deps["review"]), 1; got != want {
		t.Fatalf("重复依赖应去重为 1 个, got %d", got)
	}
	// 同层按定义顺序：collect/extra 就绪 → review → write
	wantOrder := []string{"collect", "extra", "review", "write"}
	if strings.Join(plan.order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("拓扑序 = %v, want %v", plan.order, wantOrder)
	}
	if got := plan.dependents["collect"]; len(got) != 1 || got[0] != "review" {
		t.Fatalf("collect 的后继 = %v, want [review]", got)
	}
}

// ---- 并发参数归一化 ----

func TestWorkflowNormalizeConcurrency(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{in: -5, want: workflowDefaultConcurrency},
		{in: 0, want: workflowDefaultConcurrency},
		{in: 1, want: 1},
		{in: 2, want: 2},
		{in: workflowMaxConcurrency, want: workflowMaxConcurrency},
		{in: 9, want: workflowMaxConcurrency},
	}
	for _, tt := range tests {
		if got := normalizeWorkflowConcurrency(tt.in); got != tt.want {
			t.Fatalf("normalizeWorkflowConcurrency(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestWorkflowNewPlanConcurrency 工具参数入口同样应用默认值与上限。
func TestWorkflowNewPlanConcurrency(t *testing.T) {
	steps := []workflowStepParam{{ID: "a", Task: "x"}}
	for _, tt := range []struct{ in, want int }{{0, 2}, {3, 3}, {99, 4}} {
		plan, err := newWorkflowPlan(&workflowRunParams{Steps: steps, Concurrency: tt.in})
		if err != nil {
			t.Fatalf("newWorkflowPlan 失败: %v", err)
		}
		if plan.concurrency != tt.want {
			t.Fatalf("concurrency(%d) = %d, want %d", tt.in, plan.concurrency, tt.want)
		}
	}
}

// ---- 拓扑调度与失败传播 ----

// TestWorkflowReadyStepIDs 就绪集合随完成状态推进，失败会递归跳过后继步骤。
func TestWorkflowReadyStepIDs(t *testing.T) {
	plan := workflowTestPlan(t)
	state := newWorkflowRunState(plan)

	if got := state.readyStepIDs(); strings.Join(got, ",") != "a,b" {
		t.Fatalf("初始就绪步骤 = %v, want [a b]", got)
	}
	if !state.startStep("a") {
		t.Fatal("startStep(a) 应成功")
	}
	if got := state.readyStepIDs(); strings.Join(got, ",") != "b" {
		t.Fatalf("a 运行中就绪步骤 = %v, want [b]", got)
	}
	state.completeStep("a", "A 的结果")
	if got := state.readyStepIDs(); strings.Join(got, ",") != "b" {
		t.Fatalf("a 完成后就绪步骤 = %v, want [b]（c 还缺 b）", got)
	}
	state.startStep("b")
	state.completeStep("b", "B 的结果")
	if got := state.readyStepIDs(); strings.Join(got, ",") != "c" {
		t.Fatalf("a、b 完成后就绪步骤 = %v, want [c]", got)
	}
	if state.allDone() {
		t.Fatal("c、d 未完成时 allDone 应为 false")
	}

	// c 失败 → d 被递归跳过，整体 failed
	state.startStep("c")
	skipped := state.failStep("c", "模拟失败")
	if strings.Join(skipped, ",") != "d" {
		t.Fatalf("跳过步骤 = %v, want [d]", skipped)
	}
	if !state.allDone() {
		t.Fatal("c 失败、d 跳过后应全部结束")
	}
	completed, failed, skippedCount := state.counts()
	if completed != 2 || failed != 1 || skippedCount != 1 {
		t.Fatalf("counts = (%d,%d,%d), want (2,1,1)", completed, failed, skippedCount)
	}
	if state.overallStatus() != workflowStepFailed {
		t.Fatalf("整体状态 = %q, want failed", state.overallStatus())
	}
	if got := state.readyStepIDs(); len(got) != 0 {
		t.Fatalf("结束后不应再有就绪步骤, got %v", got)
	}
}

// TestWorkflowAllCompleted 全部步骤成功时整体状态为 completed。
func TestWorkflowAllCompleted(t *testing.T) {
	plan := workflowTestPlan(t)
	state := newWorkflowRunState(plan)
	for _, id := range []string{"a", "b", "c", "d"} {
		if got := state.readyStepIDs(); !containsString(got, id) {
			t.Fatalf("步骤 %q 应就绪, got %v", id, got)
		}
		state.startStep(id)
		state.completeStep(id, id+" 结果")
	}
	if !state.allDone() {
		t.Fatal("全部完成后 allDone 应为 true")
	}
	if state.overallStatus() != workflowStepCompleted {
		t.Fatalf("整体状态 = %q, want completed", state.overallStatus())
	}
}

// ---- 步骤 prompt 构建 ----

// TestWorkflowStepPromptIncludesDependencyResults 步骤 prompt 必须自包含：
// 整体目标、步骤 ID/任务、前置步骤结果摘要（超长截断），且不依赖主对话上下文。
func TestWorkflowStepPromptIncludesDependencyResults(t *testing.T) {
	plan, err := validateWorkflowSteps([]workflowStepParam{
		{ID: "collect", Task: "收集资料"},
		{ID: "write", Task: "撰写报告", DependsOn: []string{"collect"}},
	})
	if err != nil {
		t.Fatalf("构造计划失败: %v", err)
	}
	plan.goal = "写一份调研报告"

	longResult := strings.Repeat("资料", workflowDependencySummaryRunes) // 6000 rune，应被截断
	prompt := buildWorkflowStepPrompt(plan, plan.byID["write"], map[string]string{"collect": longResult})

	for _, want := range []string{"写一份调研报告", "write", "撰写报告", "collect", "自包含"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("步骤 prompt 应包含 %q，实际：%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "（已截断）") {
		t.Fatal("超长前置结果应带截断标记")
	}
	// 截断后 prompt 不应保留完整 6000 rune 的依赖结果
	if utf8.RuneCountInString(prompt) > workflowDependencySummaryRunes+1000 {
		t.Fatalf("prompt 过长（%d rune），前置结果未被截断", utf8.RuneCountInString(prompt))
	}

	// 起始步骤没有前置结果，应明确标注
	initial := buildWorkflowStepPrompt(plan, plan.byID["collect"], nil)
	if !strings.Contains(initial, "看不到主对话历史") || !strings.Contains(initial, "起始步骤") {
		t.Fatalf("起始步骤 prompt 应说明无前置步骤与无主对话上下文，实际：%s", initial)
	}
}

// ---- 汇总结论（回灌文本） ----

// TestWorkflowSummary 汇总包含整体状态/任务 ID/目标/每步状态与结果，结果按 rune 截断。
func TestWorkflowSummary(t *testing.T) {
	plan := workflowTestPlan(t)
	plan.goal = "整体目标示例"
	state := newWorkflowRunState(plan)

	// a 完成（超长结果），b 失败，c/d 因依赖 b/c 被跳过
	state.startStep("a")
	state.completeStep("a", strings.Repeat("结", workflowStepSummaryRunes*2))
	state.startStep("b")
	state.failStep("b", "网络错误")
	state.startStep("c") // 实际调度器不会启动 c，这里直接置状态以覆盖 skipped 分支
	state.failStep("c", "连带失败")

	summary := formatWorkflowSummary("t-abc", plan, state)
	for _, want := range []string{
		"【工作流执行完成】状态=failed",
		"任务ID: t-abc",
		"整体目标: 整体目标示例",
		"[a] completed",
		"[b] failed",
		"[c] failed",
		"[d] skipped",
		"错误: 网络错误",
		"（已截断）",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("汇总应包含 %q，实际：\n%s", want, summary)
		}
	}
	// 单步结果截断到 1500 rune：汇总总长不应被超长结果撑爆
	if n := utf8.RuneCountInString(summary); n > workflowStepSummaryRunes*2+800 {
		t.Fatalf("汇总过长（%d rune），单步结果未按上限截断", n)
	}

	// 全部完成 → completed
	okState := newWorkflowRunState(plan)
	okState.fullSuccessForTest()
	if got := formatWorkflowSummary("t-ok", plan, okState); !strings.Contains(got, "状态=completed") {
		t.Fatalf("全部成功时状态应为 completed，实际：\n%s", got)
	}
}

// fullSuccessForTest 测试辅助：把所有步骤标记为 completed。
func (s *workflowRunState) fullSuccessForTest() {
	for _, step := range s.plan.steps {
		s.completeStep(step.ID, "ok")
	}
}

// ---- 工具定义与参数校验 ----

func TestNewWorkflowTools(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = testLogger()

	tools := newWorkflowTools(p, nil, message.FromUint64(12345), true)
	if len(tools) != 1 {
		t.Fatalf("应返回 1 个工作流工具, got %d", len(tools))
	}
	tool := tools[0]
	if tool.Name() != "workflow_run" {
		t.Fatalf("工具名 = %q, want workflow_run", tool.Name())
	}
	desc := tool.Description()
	for _, want := range []string{"依赖", "并行", "步骤", "群聊（会话 ID qq:12345）"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("工具描述应包含 %q，实际：%s", want, desc)
		}
	}
	if _, ok := tool.Params().(*workflowRunParams); !ok {
		t.Fatalf("参数类型 = %T, want *workflowRunParams", tool.Params())
	}
}

// TestWorkflowRunToolInvalidParams 非法参数应返回可读 error（不启动后台任务）。
func TestWorkflowRunToolInvalidParams(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = testLogger()
	tool := newWorkflowTools(p, nil, message.FromUint64(1), true)[0]

	tests := []struct {
		name    string
		params  any
		wantErr string
	}{
		{name: "nil 参数", params: nil, wantErr: "参数解析失败"},
		{name: "空步骤", params: &workflowRunParams{}, wantErr: "至少需要 1 个步骤"},
		{name: "空 task", params: &workflowRunParams{Steps: []workflowStepParam{{ID: "a", Task: ""}}}, wantErr: "task 不能为空"},
		{name: "非法 ID", params: &workflowRunParams{Steps: []workflowStepParam{{ID: "坏 ID", Task: "x"}}}, wantErr: "不合法"},
		{name: "重复 ID", params: &workflowRunParams{Steps: []workflowStepParam{
			{ID: "a", Task: "x"}, {ID: "a", Task: "y"},
		}}, wantErr: "重复"},
		{name: "未知依赖", params: &workflowRunParams{Steps: []workflowStepParam{
			{ID: "a", Task: "x", DependsOn: []string{"ghost"}},
		}}, wantErr: "不存在"},
		{name: "依赖环", params: &workflowRunParams{Steps: []workflowStepParam{
			{ID: "a", Task: "x", DependsOn: []string{"b"}},
			{ID: "b", Task: "y", DependsOn: []string{"a"}},
		}}, wantErr: "存在环"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), tt.params, llmtool.CallBackFuncs{})
			if err == nil {
				t.Fatalf("期望返回错误（%s），实际通过", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("错误文案 = %q，应包含 %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestWorkflowRunToolSubagentDisabled 子代理未启用时合法参数也不启动后台任务。
func TestWorkflowRunToolSubagentDisabled(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = testLogger()
	tool := newWorkflowTools(p, nil, message.FromUint64(1), true)[0]

	_, err := tool.Execute(context.Background(), &workflowRunParams{
		Steps: []workflowStepParam{{ID: "a", Task: "做点事"}},
	}, llmtool.CallBackFuncs{})
	if err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("子代理未启用时应返回错误, got %v", err)
	}
}

// TestWorkflowTaskTitle 任务表标题：优先整体目标，否则用步骤任务拼接。
func TestWorkflowTaskTitle(t *testing.T) {
	plan := workflowTestPlan(t)
	if got := workflowTaskTitle(plan); !strings.Contains(got, "步骤 A") {
		t.Fatalf("无 goal 时标题应取步骤任务, got %q", got)
	}
	plan.goal = "整体目标"
	if got := workflowTaskTitle(plan); got != "整体目标" {
		t.Fatalf("有 goal 时标题 = %q, want 整体目标", got)
	}
}
