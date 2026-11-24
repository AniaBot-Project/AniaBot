package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 后台工作流（DAG 多子代理编排）：模型把复杂任务拆成带依赖的步骤，工具同步
// 校验后立即返回，后台按拓扑顺序并发执行各步骤（每步复用异步子代理引擎），
// 全部结束后把「每步状态 + 结果摘要」作为合成消息回灌当前会话并唤醒主 AI。
//
// 与 subagent_run 的分工：subagent_run 一次委派一个独立子任务；workflow_run
// 编排多个有先后依赖的步骤，步骤间通过「前置步骤结果摘要」传递上下文，适合
// 调研 → 分析 → 汇总这类多阶段任务。步骤执行仍走 runSubagentWithOptions，
// 不另起子代理引擎。

const (
	// workflowMaxSteps 单次工作流的步骤上限：再多则调度/上下文成本不划算。
	workflowMaxSteps = 8
	// workflowStepIDMaxLen 步骤 ID 的字符数上限。
	workflowStepIDMaxLen = 32
	// workflowDefaultConcurrency 未指定（或非法）并发时同时执行的步骤数。
	workflowDefaultConcurrency = 2
	// workflowMaxConcurrency 同时执行的步骤数上限。
	workflowMaxConcurrency = 4
	// workflowDependencySummaryRunes 注入步骤 prompt 的单个前置步骤结果上限。
	workflowDependencySummaryRunes = 3000
	// workflowStepSummaryRunes 汇总消息中单个步骤结果/错误的上限。
	workflowStepSummaryRunes = 1500
)

// 工作流步骤状态（同时用于整体状态：completed / failed）。
const (
	workflowStepPending   = "pending"
	workflowStepRunning   = "running"
	workflowStepCompleted = "completed"
	workflowStepFailed    = "failed"
	workflowStepSkipped   = "skipped"
)

// workflowRunParams workflow_run 工具参数。
type workflowRunParams struct {
	Goal        string              `json:"goal,omitempty" desc:"整体目标（作为每一步的背景上下文，可省略）"`
	Steps       []workflowStepParam `json:"steps" desc:"按依赖编排的步骤（1~8 个）"`
	Concurrency int                 `json:"concurrency,omitempty" desc:"同时执行的步骤数（1~4，默认 2）"`
}

// workflowStepParam 工作流中的单个步骤。
type workflowStepParam struct {
	ID        string   `json:"id" desc:"步骤唯一 ID（1~32 位字母/数字/下划线/连字符）"`
	Task      string   `json:"task" desc:"该步骤要子代理完成的完整自包含任务"`
	DependsOn []string `json:"depends_on,omitempty" desc:"依赖的步骤 ID（这些步骤完成后才执行）"`
}

// workflowPlan 通过校验后的不可变执行计划。
type workflowPlan struct {
	goal        string
	steps       []workflowStepParam // 规范化后的步骤（保持定义顺序）
	order       []string            // 拓扑序（确定性：同层按定义顺序）
	byID        map[string]workflowStepParam
	deps        map[string][]string // 步骤 ID → 去重后的依赖 ID
	dependents  map[string][]string // 步骤 ID → 直接后继步骤 ID
	concurrency int
}

// normalizeWorkflowConcurrency 并发数归一化：<=0 用默认值，超过上限则收敛。
func normalizeWorkflowConcurrency(concurrency int) int {
	if concurrency <= 0 {
		return workflowDefaultConcurrency
	}
	if concurrency > workflowMaxConcurrency {
		return workflowMaxConcurrency
	}
	return concurrency
}

// validateWorkflowStepID 校验步骤 ID：1~32 位字母/数字/下划线/连字符。
func validateWorkflowStepID(id string) error {
	if id == "" {
		return fmt.Errorf("ID 不能为空")
	}
	if utf8.RuneCountInString(id) > workflowStepIDMaxLen {
		return fmt.Errorf("长度不能超过 %d 个字符", workflowStepIDMaxLen)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("只能包含字母、数字、下划线或连字符")
		}
	}
	return nil
}

// validateWorkflowSteps 校验步骤 DAG 并生成执行计划（纯函数，便于单测）：
// 数量、ID 合法性/唯一性、task 非空、依赖存在性/自依赖、无环（拓扑排序）。
func validateWorkflowSteps(steps []workflowStepParam) (*workflowPlan, error) {
	if len(steps) == 0 {
		return nil, fmt.Errorf("工作流至少需要 1 个步骤")
	}
	if len(steps) > workflowMaxSteps {
		return nil, fmt.Errorf("工作流最多支持 %d 个步骤（当前 %d 个），请合并或分多次调用", workflowMaxSteps, len(steps))
	}

	plan := &workflowPlan{
		steps:      make([]workflowStepParam, 0, len(steps)),
		byID:       make(map[string]workflowStepParam, len(steps)),
		deps:       make(map[string][]string, len(steps)),
		dependents: make(map[string][]string, len(steps)),
	}
	seen := make(map[string]bool, len(steps))
	for i := range steps {
		step := steps[i]
		step.ID = strings.TrimSpace(step.ID)
		step.Task = strings.TrimSpace(step.Task)
		if step.ID == "" {
			return nil, fmt.Errorf("第 %d 个步骤的 id 不能为空", i+1)
		}
		if err := validateWorkflowStepID(step.ID); err != nil {
			return nil, fmt.Errorf("第 %d 个步骤的 ID %q 不合法：%w", i+1, step.ID, err)
		}
		if seen[step.ID] {
			return nil, fmt.Errorf("步骤 ID 重复：%q（每个步骤的 id 必须唯一）", step.ID)
		}
		seen[step.ID] = true
		if step.Task == "" {
			return nil, fmt.Errorf("步骤 %q 的 task 不能为空（子代理看不到主对话，必须写清完整任务）", step.ID)
		}
		// depends_on 去重（重复依赖不报错，避免模型被无关细节卡住）
		deps := make([]string, 0, len(step.DependsOn))
		depSeen := make(map[string]bool, len(step.DependsOn))
		for _, dep := range step.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				return nil, fmt.Errorf("步骤 %q 的 depends_on 里有空 ID", step.ID)
			}
			if depSeen[dep] {
				continue
			}
			depSeen[dep] = true
			deps = append(deps, dep)
		}
		step.DependsOn = deps
		plan.steps = append(plan.steps, step)
		plan.byID[step.ID] = step
	}

	// 依赖存在性与自依赖（此时全部步骤已知）
	for _, step := range plan.steps {
		for _, dep := range step.DependsOn {
			if dep == step.ID {
				return nil, fmt.Errorf("步骤 %q 不能依赖自己", step.ID)
			}
			if _, ok := plan.byID[dep]; !ok {
				return nil, fmt.Errorf("步骤 %q 依赖了不存在的步骤 %q", step.ID, dep)
			}
		}
		plan.deps[step.ID] = step.DependsOn
	}
	// 反向依赖表（拓扑排序与失败传播共用）
	for _, step := range plan.steps {
		for _, dep := range step.DependsOn {
			plan.dependents[dep] = append(plan.dependents[dep], step.ID)
		}
	}
	order, err := plan.topologicalOrder()
	if err != nil {
		return nil, err
	}
	plan.order = order
	return plan, nil
}

// topologicalOrder Kahn 拓扑排序；同层就绪步骤按定义顺序处理，保证结果确定性。
// 存在环时返回环上（入度未清零）的步骤 ID。
func (plan *workflowPlan) topologicalOrder() ([]string, error) {
	indegree := make(map[string]int, len(plan.steps))
	ready := make([]string, 0, len(plan.steps))
	for _, step := range plan.steps {
		indegree[step.ID] = len(step.DependsOn)
		if indegree[step.ID] == 0 {
			ready = append(ready, step.ID)
		}
	}
	order := make([]string, 0, len(plan.steps))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, next := range plan.dependents[id] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(order) != len(plan.steps) {
		cyclic := make([]string, 0, len(plan.steps)-len(order))
		for _, step := range plan.steps {
			if indegree[step.ID] > 0 {
				cyclic = append(cyclic, step.ID)
			}
		}
		return nil, fmt.Errorf("工作流步骤依赖存在环，无法确定执行顺序（涉及步骤：%s）", strings.Join(cyclic, "、"))
	}
	return order, nil
}

// newWorkflowPlan 校验工具参数并生成执行计划。
func newWorkflowPlan(params *workflowRunParams) (*workflowPlan, error) {
	if params == nil {
		return nil, fmt.Errorf("工作流参数为空")
	}
	plan, err := validateWorkflowSteps(params.Steps)
	if err != nil {
		return nil, err
	}
	plan.goal = strings.TrimSpace(params.Goal)
	plan.concurrency = normalizeWorkflowConcurrency(params.Concurrency)
	return plan, nil
}

// ---- 运行状态机（纯内存，不依赖 LLM / goroutine，便于单测） ----

// workflowRunState 一次工作流执行的步骤状态机。调度器只在主 goroutine 中读写它，
// 步骤执行 goroutine 只通过 channel 回传结果。
type workflowRunState struct {
	plan    *workflowPlan
	status  map[string]string
	results map[string]string // completed 步骤的结果
	errs    map[string]string // failed 步骤的错误摘要
}

// newWorkflowRunState 基于计划创建初始状态（全部 pending）。
func newWorkflowRunState(plan *workflowPlan) *workflowRunState {
	status := make(map[string]string, len(plan.steps))
	for _, step := range plan.steps {
		status[step.ID] = workflowStepPending
	}
	return &workflowRunState{
		plan:    plan,
		status:  status,
		results: make(map[string]string, len(plan.steps)),
		errs:    make(map[string]string, len(plan.steps)),
	}
}

// readyStepIDs 返回当前可执行（pending 且全部依赖已完成）的步骤 ID，
// 按步骤定义顺序排列，保证调度确定性。
func (s *workflowRunState) readyStepIDs() []string {
	var ready []string
	for _, step := range s.plan.steps {
		if s.status[step.ID] != workflowStepPending {
			continue
		}
		ok := true
		for _, dep := range step.DependsOn {
			if s.status[dep] != workflowStepCompleted {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, step.ID)
		}
	}
	return ready
}

// startStep 把 pending 步骤置为 running；状态不符时返回 false。
func (s *workflowRunState) startStep(id string) bool {
	if s.status[id] != workflowStepPending {
		return false
	}
	s.status[id] = workflowStepRunning
	return true
}

// completeStep 记录步骤成功及其结果。
func (s *workflowRunState) completeStep(id, result string) {
	s.status[id] = workflowStepCompleted
	s.results[id] = result
}

// failStep 记录步骤失败，并递归跳过依赖它的后续步骤（它们永远不会执行）。
// 返回被跳过的步骤 ID 列表。
func (s *workflowRunState) failStep(id, errText string) []string {
	s.status[id] = workflowStepFailed
	s.errs[id] = errText
	return s.skipDependents(id)
}

// skipDependents 把依赖 root（失败/跳过）的 pending 步骤标记为 skipped，
// 并沿依赖链递归传播。
func (s *workflowRunState) skipDependents(root string) []string {
	var skipped []string
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range s.plan.dependents[cur] {
			if s.status[next] != workflowStepPending {
				continue
			}
			s.status[next] = workflowStepSkipped
			skipped = append(skipped, next)
			queue = append(queue, next)
		}
	}
	return skipped
}

// allDone 是否已无 pending/running 步骤。
func (s *workflowRunState) allDone() bool {
	for _, st := range s.status {
		if st == workflowStepPending || st == workflowStepRunning {
			return false
		}
	}
	return true
}

// counts 统计各终态步骤数量。
func (s *workflowRunState) counts() (completed, failed, skipped int) {
	for _, st := range s.status {
		switch st {
		case workflowStepCompleted:
			completed++
		case workflowStepFailed:
			failed++
		case workflowStepSkipped:
			skipped++
		}
	}
	return completed, failed, skipped
}

// overallStatus 整体状态：全部步骤成功才是 completed，否则 failed。
func (s *workflowRunState) overallStatus() string {
	completed, failed, skipped := s.counts()
	if failed == 0 && skipped == 0 && completed == len(s.plan.steps) {
		return workflowStepCompleted
	}
	return workflowStepFailed
}

// ---- prompt 与回灌文本构建 ----

// truncateWorkflowRunes rune 安全截断（含截断标记）。
func truncateWorkflowRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…（已截断）"
}

// buildWorkflowStepPrompt 构建单个步骤的任务提示词。子代理没有主对话上下文，
// 因此提示词必须自包含：整体目标 + 本步骤 ID/任务 + 已完成前置步骤的结果摘要。
func buildWorkflowStepPrompt(plan *workflowPlan, step workflowStepParam, results map[string]string) string {
	var sb strings.Builder
	sb.WriteString("【工作流步骤任务】\n")
	sb.WriteString("你是一个后台工作流中的执行者，只负责下面这一个步骤。你看不到主对话历史，")
	sb.WriteString("本提示词已包含完成任务所需的全部信息，不要假设存在其他上下文。\n\n")

	sb.WriteString("整体目标：\n")
	if plan.goal != "" {
		sb.WriteString(plan.goal + "\n\n")
	} else {
		sb.WriteString("（未显式指定，请根据前置步骤结果与本步骤任务推断）\n\n")
	}

	sb.WriteString("当前步骤 ID：" + step.ID + "\n")
	sb.WriteString("当前步骤任务：\n" + step.Task + "\n")

	if len(step.DependsOn) == 0 {
		sb.WriteString("\n前置步骤：无（这是工作流的起始步骤）\n")
	} else {
		sb.WriteString("\n已完成前置步骤的结果摘要（供你参考，不要重复它们的工作）：\n")
		for _, dep := range step.DependsOn {
			sb.WriteString("### " + dep + "\n")
			res := strings.TrimSpace(results[dep])
			if res == "" {
				res = "（该步骤没有返回文本结果）"
			}
			sb.WriteString(truncateWorkflowRunes(res, workflowDependencySummaryRunes) + "\n\n")
		}
	}

	sb.WriteString("\n输出要求：\n")
	sb.WriteString("- 最终回复必须是可直接被后续步骤和主 AI 使用的自包含结果：写明结论、关键数据、产出物位置；\n")
	sb.WriteString("- 不要寒暄，不要复述任务，只输出与任务相关的内容；\n")
	sb.WriteString("- 需要交接给后续步骤的信息（路径、ID、结论）要在结果中显式列出。")
	return sb.String()
}

// formatWorkflowSummary 拼接回灌会话的汇总文本：
// 整体状态 + 任务 ID + 整体目标 + 每步状态与结果摘要（每步 ≤ workflowStepSummaryRunes）。
func formatWorkflowSummary(taskID string, plan *workflowPlan, state *workflowRunState) string {
	var sb strings.Builder
	sb.WriteString("【工作流执行完成】状态=" + state.overallStatus() + "\n")
	sb.WriteString("任务ID: " + taskID + "\n")
	goal := plan.goal
	if goal == "" {
		goal = "（未指定）"
	}
	sb.WriteString("整体目标: " + goal + "\n")
	sb.WriteString("步骤结果:\n")
	for _, step := range plan.steps {
		status := state.status[step.ID]
		sb.WriteString("[" + step.ID + "] " + status + "\n")
		switch status {
		case workflowStepCompleted:
			result := strings.TrimSpace(state.results[step.ID])
			if result == "" {
				result = "（该步骤没有返回文本结果）"
			}
			sb.WriteString(truncateWorkflowRunes(result, workflowStepSummaryRunes) + "\n")
		case workflowStepFailed:
			errText := strings.TrimSpace(state.errs[step.ID])
			if errText == "" {
				errText = "（未知错误）"
			}
			sb.WriteString("错误: " + truncateWorkflowRunes(errText, workflowStepSummaryRunes) + "\n")
		case workflowStepSkipped:
			sb.WriteString("（依赖的步骤未成功，已跳过）\n")
		default:
			sb.WriteString("（未执行）\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// workflowTaskTitle 生成任务表标题/异步条目描述：优先整体目标，否则取前几个步骤。
func workflowTaskTitle(plan *workflowPlan) string {
	if plan.goal != "" {
		return plan.goal
	}
	parts := make([]string, 0, 3)
	for _, step := range plan.steps {
		parts = append(parts, step.Task)
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return "多步骤任务"
	}
	return strings.Join(parts, " → ")
}

// ---- 工具定义 ----

// workflowToolBase 工作流工具共享的插件引用与当前会话信息（创建时绑定）。
type workflowToolBase struct {
	plugin  *AIChatPlugin
	bot     bot.Bot
	id      message.QID
	isGroup bool
}

// newWorkflowTools 创建工作流工具，注册到主会话执行器中。
// 仅在 subagent.enable=true 时由主会话注册（步骤执行即后台子代理）。
func newWorkflowTools(p *AIChatPlugin, b bot.Bot, id message.QID, isGroup bool) []llmtool.Tool {
	sessionDesc := "私聊（对方 ID " + id.String() + "）"
	if isGroup {
		sessionDesc = "群聊（会话 ID " + id.String() + "）"
	}
	desc := "把一个复杂任务拆成多个有依赖关系的步骤（DAG），交给多个子代理按依赖顺序并" +
		"行执行，全部完成后结果自动回到当前会话并触发你继续处理。" +
		"\n\n何时使用：任务可以拆成 2~8 个阶段且阶段之间有明确先后依赖（如 调研→分析→汇总、" +
		"分别处理多个对象再合并），用 workflow_run 一次编排；步骤可以并行的会自动并行。" +
		"简单任务或只有一个子任务时直接用 subagent_run。" +
		"\n\n行为：同步只做校验并启动，立即返回任务 ID，不阻塞本轮；执行期间不要等待或轮询，" +
		"完成后会自动通知你。每个步骤的子代理看不到主对话历史，task 必须自包含；" +
		"后续步骤会自动收到前置步骤的结果摘要。" +
		fmt.Sprintf("\n\n限制：步骤 1~%d 个；并发 1~%d（默认 %d）；当前会话为 %s。",
			workflowMaxSteps, workflowMaxConcurrency, workflowDefaultConcurrency, sessionDesc)
	return []llmtool.Tool{
		&workflowRunTool{
			BaseTool:         llmtool.MakeBaseTool("workflow_run", desc, workflowRunParams{}),
			workflowToolBase: workflowToolBase{plugin: p, bot: b, id: id, isGroup: isGroup},
		},
	}
}

// workflowRunTool workflow_run：校验 DAG 后启动后台工作流，立即返回。
type workflowRunTool struct {
	llmtool.BaseTool[workflowRunParams]
	workflowToolBase
}

func (t *workflowRunTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*workflowRunParams)
	if !ok || p == nil {
		return "", fmt.Errorf("工作流参数解析失败")
	}
	plan, err := newWorkflowPlan(p)
	if err != nil {
		return "", err
	}
	if !t.plugin.cfg.Subagent.Enable {
		return "", fmt.Errorf("子代理功能未启用（plugin.ai_chat_bot.subagent.enable=false），无法启动工作流")
	}
	return t.plugin.launchWorkflow(t.bot, t.id, t.isGroup, plan, callbacks), nil
}

// launchWorkflow 登记任务表与异步子代理组，启动后台 goroutine 并立即返回占位文本。
//
// ctx 由 newTurnContext 构造：自带回合硬截止、随插件卸载取消；cancel 登记进
// 会话的异步子代理组，/stop、/tasks 与 OnUnload 都能管理到本工作流。
func (p *AIChatPlugin) launchWorkflow(b bot.Bot, id message.QID, isGroup bool, plan *workflowPlan, parentCbs llmtool.CallBackFuncs) string {
	taskID := newTaskID()
	title := "工作流：" + workflowTaskTitle(plan)
	task := p.beginSubagentTask(taskID, id, isGroup, title, taskKindWorkflow)

	ctx, cancel := p.newTurnContext(context.Background())
	p.getAsyncGroup(id, isGroup).addEntry(asyncSubagentEntry{
		id:     taskID,
		task:   title,
		cancel: cancel,
	})
	b.Go("workflow:"+taskID, func() {
		defer cancel()
		defer p.removeAsyncSubagent(id, isGroup, taskID)
		p.runWorkflow(ctx, b, id, isGroup, taskID, task, plan, parentCbs)
	})

	return fmt.Sprintf("✅ 工作流已启动（ID: %s，共 %d 步，并发 %d），完成后结果会回到会话",
		taskID, len(plan.steps), plan.concurrency)
}

// workflowStepOutcome 步骤执行 goroutine 回传的结果。
type workflowStepOutcome struct {
	id     string
	result string
	usage  aichat.TokenUsage
	err    error
}

// runWorkflow 后台执行工作流：严格按依赖调度，就绪步骤最多 concurrency 个并行；
// 单步失败时后续依赖步骤标记 skipped、其余无依赖步骤继续；全部结束后回灌汇总。
//
// 取消（/stop、插件卸载、回合硬截止）时静默收尾：任务表按 context 错误落
// cancelled/timeout，不向会话注入失败骚扰消息（与异步子代理一致）。
func (p *AIChatPlugin) runWorkflow(ctx context.Context, b bot.Bot, id message.QID, isGroup bool, taskID string, task *agentTask, plan *workflowPlan, parentCbs llmtool.CallBackFuncs) {
	state := newWorkflowRunState(plan)
	// 就绪步骤结果回传通道：容量=步骤数，保证步骤 goroutine 永不阻塞发送
	outcomes := make(chan workflowStepOutcome, len(plan.steps))
	var wg sync.WaitGroup
	var total aichat.TokenUsage
	running := 0
	canceled := false
	start := time.Now()

	p.Logger.Info("工作流开始执行", "task_id", taskID, "id", id, "is_group", isGroup,
		"steps", len(plan.steps), "concurrency", plan.concurrency)

	for !canceled && !state.allDone() {
		if ctx.Err() != nil {
			canceled = true
			break
		}

		// 领取并启动就绪步骤，直到并发上限；全局子代理池名额按步获取、执行完释放
		for running < plan.concurrency {
			ready := state.readyStepIDs()
			if len(ready) == 0 {
				break
			}
			stepID := ready[0]
			if p.subagentSem != nil {
				select {
				case p.subagentSem <- struct{}{}:
				case <-ctx.Done():
					canceled = true
				}
			}
			if canceled {
				break
			}
			step := plan.byID[stepID]
			// 依赖结果只在主 goroutine 读取（就绪时依赖必已完成），不存在数据竞争
			prompt := buildWorkflowStepPrompt(plan, step, state.results)
			state.startStep(stepID)
			running++
			wg.Add(1)
			go func(stepID, prompt string) {
				out := workflowStepOutcome{id: stepID}
				defer func() {
					if r := recover(); r != nil {
						out.result = ""
						out.err = fmt.Errorf("步骤执行异常: %v", r)
					}
					if p.subagentSem != nil {
						<-p.subagentSem
					}
					// 先回传结果再 Done：wg.Wait 返回后收尾排干不会漏掉用量
					outcomes <- out
					wg.Done()
				}()
				out.result, out.usage, out.err = p.runSubagentWithOptions(ctx, b, id, isGroup, prompt,
					subagentRunOptions{timeoutSec: 0}, parentCbs)
			}(stepID, prompt)
		}
		if canceled {
			break
		}
		if running == 0 {
			// 防御性兜底：DAG 校验保证存在就绪步骤，正常不会走到这里
			break
		}

		select {
		case out := <-outcomes:
			running--
			total = mergeTokenUsage(total, out.usage)
			switch {
			case out.err != nil && (ctx.Err() != nil || isContextCanceled(out.err)):
				// 父上下文被取消：整体按取消处理，不再继续调度
				canceled = true
			case out.err != nil:
				skipped := state.failStep(out.id, out.err.Error())
				p.Logger.Warn("工作流步骤失败", "task_id", taskID, "step", out.id, "error", out.err.Error())
				if len(skipped) > 0 {
					p.Logger.Info("工作流依赖步骤被跳过", "task_id", taskID, "step", out.id, "skipped", strings.Join(skipped, ","))
				}
			default:
				state.completeStep(out.id, out.result)
			}
		case <-ctx.Done():
			canceled = true
		}
	}

	// 收尾：等待仍在运行的步骤退出（取消时它们的子代理会随 ctx 快速返回），并排干用量
	wg.Wait()
	for draining := true; draining; {
		select {
		case out := <-outcomes:
			total = mergeTokenUsage(total, out.usage)
		default:
			draining = false
		}
	}
	duration := time.Since(start)

	if canceled {
		runErr := ctx.Err()
		if runErr == nil {
			runErr = context.Canceled
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			p.Logger.Warn("工作流执行超时被中止", "task_id", taskID, "id", id, "is_group", isGroup, "duration", duration)
			p.finishSubagentTask(task, "", total, fmt.Errorf("工作流执行超时（%v），已中止", runErr))
		} else {
			p.Logger.Info("工作流已被取消", "task_id", taskID, "id", id, "is_group", isGroup, "duration", duration)
			p.finishSubagentTask(task, "", total, runErr)
		}
		return
	}

	// 正常结束（可能部分步骤失败）：记录任务表终态、累计用量与配额，然后回灌会话
	summary := formatWorkflowSummary(taskID, plan, state)
	var runErr error
	status := state.overallStatus()
	if status != workflowStepCompleted {
		completed, failed, skipped := state.counts()
		runErr = fmt.Errorf("工作流执行失败：%d 个步骤完成、%d 个失败、%d 个跳过", completed, failed, skipped)
	}
	p.quotaManager.Add(sessionKey(id, isGroup), total)
	p.addExtraUsage(sessionKey(id, isGroup), total)
	p.finishSubagentTask(task, summary, total, runErr)
	p.Logger.Info("工作流执行完成", "task_id", taskID, "id", id, "is_group", isGroup,
		"status", status, "duration", duration, "tokens", total.TotalTokens)
	p.enqueueWorkflowResult(b, id, isGroup, summary)
}

// enqueueWorkflowResult 把工作流汇总作为合成消息入队并触发主会话处理
// （与 onSubagentComplete 的入队/kick 路径一致）。
func (p *AIChatPlugin) enqueueWorkflowResult(b bot.Bot, id message.QID, isGroup bool, text string) {
	p.sessionBots.Store(sessionKey(id, isGroup), b)
	synthetic := message.Message{
		Sender: message.MessageSender{
			Nickname: "工作流",
			UserId:   message.FromUint64(0),
		},
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": text}},
		},
	}
	if _, ok := p.enqueuePending(id, isGroup, synthetic); !ok {
		p.Logger.Warn("工作流结果入队失败（队列已满）", "id", id, "is_group", isGroup)
		return
	}
	b.Go("pending:"+sessionKey(id, isGroup), func() { p.kickPending(b, id, isGroup) })
}
