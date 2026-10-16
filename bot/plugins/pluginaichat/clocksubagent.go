package pluginaichat

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/bot"
)

const (
	// clockSubagentMaxRounds 子代理结果回喂的最大轮数：AI 拿到结果后可能再次委派，
	// 限制轮数防止「委派→等待→再委派」循环吃掉整个任务预算
	clockSubagentMaxRounds = 5
	// clockSubagentWaitReserve 等待子代理时为最终汇总回复预留的时间。
	// 参照 subagentParentReserve：子代理自身的超时在启动时已按父上下文预算压缩，
	// 等待时同样预留，保证最后一轮 LLM 有预算合成最终回复
	clockSubagentWaitReserve = 30 * time.Second
)

// clockSubagent 定时任务执行期间启动的一个异步子代理
type clockSubagent struct {
	id        string
	task      string
	startTime time.Time
	cancel    context.CancelFunc
	done      chan struct{} // markFinished 时关闭，等待方据此解除阻塞
	finished  bool
	result    string
	err       error
}

// clockSubagentSet 一次定时任务执行期间启动的异步子代理集合。
//
// 与会话级 asyncSubagentGroup 的区别：定时任务没有 pending 队列与会话锁，
// 子代理结果不入队、不触发会话消息，而是由 drainClockSubagents 在任务收尾时
// 统一等待全部完成后回喂给任务 AI——只有汇总后的最终回复才会推送给目标。
type clockSubagentSet struct {
	mu      sync.Mutex
	seq     int
	entries map[string]*clockSubagent
}

func newClockSubagentSet() *clockSubagentSet {
	return &clockSubagentSet{entries: map[string]*clockSubagent{}}
}

func (s *clockSubagentSet) nextID() string {
	s.mu.Lock()
	s.seq++
	id := strconv.Itoa(s.seq)
	s.mu.Unlock()
	return id
}

func (s *clockSubagentSet) add(e *clockSubagent) {
	s.mu.Lock()
	s.entries[e.id] = e
	s.mu.Unlock()
}

// markFinished 记录执行结果并关闭 done（等待方据此解除阻塞）
func (s *clockSubagentSet) markFinished(id string, result string, err error) {
	s.mu.Lock()
	if e, ok := s.entries[id]; ok && !e.finished {
		e.finished = true
		e.result = result
		e.err = err
		close(e.done)
	}
	s.mu.Unlock()
}

// hasPending 是否仍有运行中的子代理
func (s *clockSubagentSet) hasPending() bool {
	return s.pendingCount() > 0
}

// pendingCount 运行中子代理数量
func (s *clockSubagentSet) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.entries {
		if !e.finished {
			n++
		}
	}
	return n
}

// collectFinished 取出全部已完成的子代理（从集合中移除），按启动时间排序
func (s *clockSubagentSet) collectFinished() []*clockSubagent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*clockSubagent
	for id, e := range s.entries {
		if e.finished {
			out = append(out, e)
			delete(s.entries, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].startTime.Before(out[j].startTime) })
	return out
}

// snapshot 当前运行中子代理的快照，按启动时间排序（供 subagent_list）
func (s *clockSubagentSet) snapshot() []*clockSubagent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*clockSubagent, 0, len(s.entries))
	for _, e := range s.entries {
		if !e.finished {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].startTime.Before(out[j].startTime) })
	return out
}

// cancelOne 按 ID 取消一个运行中的子代理（供 subagent_cancel），返回是否命中
func (s *clockSubagentSet) cancelOne(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok && !e.finished {
		e.cancel()
		return true
	}
	return false
}

// cancelPending 取消所有运行中的子代理
func (s *clockSubagentSet) cancelPending() {
	s.mu.Lock()
	for _, e := range s.entries {
		if !e.finished {
			e.cancel()
		}
	}
	s.mu.Unlock()
}

// waitAll 阻塞等待当前所有运行中子代理完成；为最终汇总回复预留 reserve 时间，
// 剩余预算不足时提前返回（未完成的由调用方决定是否取消）
func (s *clockSubagentSet) waitAll(ctx context.Context, reserve time.Duration) {
	waitCtx := ctx
	cancel := context.CancelFunc(func() {})
	if deadline, ok := ctx.Deadline(); ok {
		budget := time.Until(deadline) - reserve
		if budget <= 0 {
			return
		}
		waitCtx, cancel = context.WithTimeout(ctx, budget)
	}
	defer cancel()
	for {
		s.mu.Lock()
		var done chan struct{}
		for _, e := range s.entries {
			if !e.finished {
				done = e.done
				break
			}
		}
		s.mu.Unlock()
		if done == nil {
			return
		}
		select {
		case <-done:
		case <-waitCtx.Done():
			return
		}
	}
}

// ---- 定时任务专用子代理工具 ----

// clockSubagentToolBase 定时任务子代理工具共享的执行上下文（每次任务执行创建时绑定）
type clockSubagentToolBase struct {
	plugin *AIChatPlugin
	bot    bot.Bot
	task   *ClockTask
	set    *clockSubagentSet
	// extra 本次任务执行的派生用量累计器（子代理 LLM 消耗），executeTask 收尾时
	// 并入任务总用量（tasklog 与配额同源）
	extra *usageAcc
}

// newClockSubagentTools 创建定时任务专用的子代理工具（注册到任务的一次性执行器）。
// 与会话版 subagent 工具的差异：子代理结果不入会话 pending 队列，而是在任务收尾时
// 统一等待并回喂给任务 AI 合成最终回复。
func newClockSubagentTools(p *AIChatPlugin, b bot.Bot, task *ClockTask, set *clockSubagentSet, extra *usageAcc) []llmtool.Tool {
	base := clockSubagentToolBase{plugin: p, bot: b, task: task, set: set, extra: extra}
	runDesc := "将一个复杂/耗时的子任务委派给一次性子代理在后台异步执行。子代理以全新上下文运行（看不到本次任务的对话过程），" +
		"拥有与你一致的工具能力（以其实际可用的工具列表为准），无法再委派子代理。" +
		"你可以连续启动多个子代理并行执行不同子任务，启动后立即返回、不会阻塞你当前的工作；" +
		"在你本轮工作收尾时，系统会自动等待所有子代理完成并把结果汇总返回给你，之后你再输出最终回复（只有最终回复会推送给目标）。" +
		fmt.Sprintf("子代理默认超时 %d 秒。", int(p.subagentTimeout().Seconds()))
	return []llmtool.Tool{
		&clockSubagentRunTool{
			BaseTool:              llmtool.MakeBaseTool("subagent_run", runDesc, subagentRunParams{}),
			clockSubagentToolBase: base,
		},
		&clockSubagentListTool{
			BaseTool:              llmtool.MakeBaseTool("subagent_list", "列出本次任务中正在运行的异步子代理及其详情（ID、任务摘要、运行时间等）", subagentListParams{}),
			clockSubagentToolBase: base,
		},
		&clockSubagentCancelTool{
			BaseTool:              llmtool.MakeBaseTool("subagent_cancel", "按 ID 取消本次任务中一个正在运行的异步子代理", subagentCancelParams{}),
			clockSubagentToolBase: base,
		},
	}
}

// launch 启动一个异步子代理并登记到集合，立即返回占位文本。
//
// 子代理的 ctx 派生自任务执行 ctx：任务超时或收尾取消时子代理一并取消；
// 其内部超时由 runSubagent 的 resolveSubagentTimeout 按任务剩余预算压缩。
func (t *clockSubagentToolBase) launch(ctx context.Context, taskText string, timeoutSec int, parentCbs llmtool.CallBackFuncs) string {
	p := t.plugin
	if _, err := resolveSubagentTimeout(p.subagentTimeout(), timeoutSec, ctx); err != nil {
		return fmt.Sprintf("无法启动子代理: %v", err)
	}

	id := t.set.nextID()
	subCtx, cancel := context.WithCancel(ctx)
	t.set.add(&clockSubagent{
		id:        id,
		task:      taskText,
		startTime: time.Now(),
		cancel:    cancel,
		done:      make(chan struct{}),
	})

	isGroup := t.task.TargetType == clockTargetGroup
	qid := parseQID(t.task.TargetID)

	t.bot.Go("clock-subagent:"+t.task.ID+":"+id, func() {
		result, usage, err := p.runSubagent(subCtx, t.bot, qid, isGroup, taskText, timeoutSec, parentCbs)
		// 子代理消耗计入本次任务的派生用量（executeTask 收尾时并入任务日志与配额）
		t.extra.add(usage)
		t.set.markFinished(id, result, err)
	})

	p.Logger.Info("定时任务子代理已启动", "task", t.task.ID, "subagent", id, "sub_task", taskText)
	return fmt.Sprintf("✅ 子代理已启动（ID: %s），正在后台执行；任务收尾时会自动等待其完成并把结果汇总给你", id)
}

// ---- subagent_run ----

type clockSubagentRunTool struct {
	llmtool.BaseTool[subagentRunParams]
	clockSubagentToolBase
}

func (t *clockSubagentRunTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*subagentRunParams)
	task := strings.TrimSpace(p.Task)
	if task == "" {
		return "", fmt.Errorf("task 不能为空")
	}
	return t.launch(ctx, task, p.TimeoutSec, callbacks), nil
}

// ---- subagent_list ----

type clockSubagentListTool struct {
	llmtool.BaseTool[subagentListParams]
	clockSubagentToolBase
}

func (t *clockSubagentListTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	running := t.set.snapshot()
	if len(running) == 0 {
		return "当前没有正在运行的子代理", nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "当前运行中的子代理（共 %d 个）:\n\n", len(running))
	for i, e := range running {
		elapsed := time.Since(e.startTime).Truncate(time.Second)
		// 按 rune 截断：任务描述多为中文，按字节切会产出非法 UTF-8
		taskPreview := tasklog.Truncate(e.task, 60)
		fmt.Fprintf(&sb, "%d. ID: %s\n   任务: %s\n   运行时间: %s\n",
			i+1, e.id, taskPreview, elapsed)
		if i < len(running)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

// ---- subagent_cancel ----

type clockSubagentCancelTool struct {
	llmtool.BaseTool[subagentCancelParams]
	clockSubagentToolBase
}

func (t *clockSubagentCancelTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*subagentCancelParams)
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	if ok := t.set.cancelOne(id); !ok {
		return fmt.Sprintf("未找到 ID 为 %s 的子代理（可能已完成或 ID 不正确）", id), nil
	}
	return fmt.Sprintf("已取消子代理 %s", id), nil
}

// ---- 收尾等待与结果回喂 ----

// drainClockSubagents 等待任务启动的全部异步子代理完成，将结果回喂给任务 AI
// 合成最终回复。返回最终回复与累加后的 token 用量。
//
// 定时任务的推送语义：只有所有子代理结束、AI 基于汇总结果输出的最后一轮文本
// 才会推送给目标——子代理的中间过程与未汇总的提前回复都不会发送（中间轮文本
// 由 makeClockCallback 的 SendText 丢弃）。
func (m *clockManager) drainClockSubagents(ctx context.Context, task *ClockTask, chat *aichat.ChatBot, cbs llmtool.CallBackFuncs, set *clockSubagentSet, resp string, usage aichat.TokenUsage) (string, aichat.TokenUsage) {
	p := m.plugin
	for round := 0; round < clockSubagentMaxRounds && set.hasPending(); round++ {
		set.waitAll(ctx, clockSubagentWaitReserve)
		finished := set.collectFinished()
		if len(finished) == 0 {
			// 预算耗尽仍无结果：取消剩余子代理，按已有回复收尾
			set.cancelPending()
			m.logger.Warn("定时任务等待子代理结果超时，取消剩余子代理并使用已有回复收尾",
				"task", task.ID, "title", task.Title)
			break
		}
		pending := set.pendingCount()
		m.logger.Info("定时任务子代理结果回喂", "task", task.ID, "finished", len(finished), "pending", pending, "round", round+1)
		r2, u2, err := chat.Chat(ctx, buildClockSubagentReport(finished, pending), cbs, p.buildChatOptions())
		usage.PromptTokens += u2.PromptTokens
		usage.CompletionTokens += u2.CompletionTokens
		usage.TotalTokens += u2.TotalTokens
		usage.CachedTokens += u2.CachedTokens
		usage.Iterations += u2.Iterations
		if err != nil {
			m.logger.Warn("定时任务子代理结果汇总失败，保留上一版回复",
				"task", task.ID, "error", err.Error())
			break
		}
		resp = r2
	}
	if set.hasPending() {
		// 超过最大回喂轮数仍未收尾：取消剩余子代理，推送当前最终回复
		m.logger.Warn("定时任务子代理回喂达到最大轮数，取消剩余子代理",
			"task", task.ID, "rounds", clockSubagentMaxRounds)
		set.cancelPending()
	}
	return resp, usage
}

// buildClockSubagentReport 把已完成子代理的结果组装成回喂给任务 AI 的消息。
// pending 为仍在运行的子代理数量（>0 时提示 AI 可继续等待或取消）。
func buildClockSubagentReport(finished []*clockSubagent, pending int) string {
	var sb strings.Builder
	sb.WriteString("【子代理执行结果汇总】\n以下是你委派的子代理的执行结果。请基于这些结果输出你的最终回复——只有最终回复会推送给目标。\n")
	for _, e := range finished {
		fmt.Fprintf(&sb, "\n--- 子代理（ID: %s）---\n任务: %s\n", e.id, e.task)
		if e.err != nil {
			sb.WriteString("执行失败: " + e.err.Error() + "\n")
		} else {
			sb.WriteString(e.result + "\n")
		}
	}
	if pending > 0 {
		fmt.Fprintf(&sb, "\n另有 %d 个子代理仍在执行中，其完成后的结果会继续汇总给你；如不再需要可用 subagent_cancel 取消。\n", pending)
	}
	return sb.String()
}
