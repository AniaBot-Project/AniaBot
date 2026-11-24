package pluginaichat

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// /tasks 命令：展示当前会话的后台任务与运行中的子代理。
// 群聊需 @ 机器人（与其它指令一致），私聊直接可用。

// handleTasksCommand 处理 /tasks：运行中的子代理 + 任务表最近记录。
func (p *AIChatPlugin) handleTasksCommand(b bot.Bot, msg message.Message) (bool, error) {
	id, isGroup := messageTarget(msg)
	p.sendPlainText(b, id, isGroup, p.renderTasks(id, isGroup))
	return false, nil
}

// renderTasks 渲染任务列表文本。
func (p *AIChatPlugin) renderTasks(id message.QID, isGroup bool) string {
	var sb strings.Builder
	sb.WriteString("【后台任务】\n")

	// 运行中的异步子代理（内存态，必然实时）
	running := p.listRunningSubagents(id, isGroup)
	if len(running) > 0 {
		fmt.Fprintf(&sb, "运行中：%d 个\n", len(running))
		for i, e := range running {
			fmt.Fprintf(&sb, "%d. %s（子代理，运行 %s）\n   任务：%s\n",
				i+1, e.id, formatStatusDuration(time.Since(e.startTime)), truncateTaskTitle(e.task))
		}
	} else {
		sb.WriteString("运行中：无\n")
	}

	// 任务表最近记录（持久化；未启用 SQL 时为空）
	if p.taskStore != nil {
		ctx, cancel := p.taskDB()
		tasks, err := p.taskStore.ListBySession(ctx, sessionKey(id, isGroup), 8)
		cancel()
		if err == nil && len(tasks) > 0 {
			sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].CreatedAt > tasks[j].CreatedAt })
			sb.WriteString("\n最近任务：\n")
			for _, t := range tasks {
				sb.WriteString(renderTaskLine(t))
				sb.WriteString("\n")
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderTaskLine 单条任务的一行摘要：状态/类型/耗时/标题/停止原因。
func renderTaskLine(t *agentTask) string {
	kind := t.Kind
	switch kind {
	case taskKindSubagent:
		kind = "子代理"
	case taskKindHandoff:
		kind = "交接"
	case taskKindBashJob:
		kind = "后台命令"
	case taskKindTurn:
		kind = "回合"
	case taskKindClock:
		kind = "定时"
	}
	dur := ""
	if t.StartedAt > 0 {
		end := t.FinishedAt
		if end == 0 {
			end = time.Now().Unix()
		}
		if end > t.StartedAt {
			dur = " " + formatStatusDuration(time.Duration(end-t.StartedAt)*time.Second)
		}
	}
	reason := ""
	if t.StopReason != "" && t.StopReason != taskStopCompleted {
		reason = "（" + t.StopReason + "）"
	}
	tokens := ""
	if t.Tokens > 0 {
		tokens = fmt.Sprintf(" · %d tok", t.Tokens)
	}
	return fmt.Sprintf("- %s [%s]%s %s%s%s", t.ID, taskStatusLabel(t.Status), dur, kind, reason, tokens+"："+truncateTaskTitle(t.Title))
}

// taskStatusLabel 任务状态的中文展示。
func taskStatusLabel(status string) string {
	switch status {
	case taskStatusQueued:
		return "排队"
	case taskStatusRunning:
		return "运行中"
	case taskStatusWaiting:
		return "等待"
	case taskStatusCompleted:
		return "完成"
	case taskStatusFailed:
		return "失败"
	case taskStatusTimeout:
		return "超时"
	case taskStatusCancelled:
		return "已取消"
	case taskStatusInterrupted:
		return "已中断"
	default:
		return status
	}
}

// handleStopByID 处理 /stop <任务ID>：按 ID 取消运行中的异步子代理。
// 返回 (handled, text)：handled=false 表示 ID 未命中（调用方回退到整体停止提示）。
func (p *AIChatPlugin) handleStopByID(b bot.Bot, msg message.Message, taskID string) (bool, error) {
	id, isGroup := messageTarget(msg)
	if taskID == "" {
		return false, nil
	}
	if !p.cancelSubagentByID(id, isGroup, taskID) {
		p.sendPlainText(b, id, isGroup, "未找到任务 "+taskID+"（可能已完成，发送 /tasks 查看当前任务）")
		return true, nil
	}
	p.sendPlainText(b, id, isGroup, "已取消任务 "+taskID)
	return true, nil
}

// handoffTool 把当前回合的剩余工作交给后台子代理继续执行（task_handoff）。
//
// 这是"优雅超时"的落点：软截止提醒会引导模型调用本工具，把目标/已完成/待办
// 摘要交给后台，当前回合立即结束并给用户一句简短说明；后台完成后结果作为
// 合成消息回到会话，触发新一轮汇报。
type handoffTool struct {
	llmtool.BaseTool[handoffParams]
	subagentToolBase
}

// handoffParams task_handoff 参数。
type handoffParams struct {
	Summary    string `json:"summary" desc:"已完成的工作与当前结论摘要：主 AI 已确认的进展、产出位置/引用"`
	Remaining  string `json:"remaining" desc:"剩余要做的具体工作：目标、步骤、验收标准，必须自包含（后台子代理看不到当前对话）"`
	Acceptance string `json:"acceptance,omitempty" desc:"可选：完成标准，便于子代理自检"`
}

// newHandoffTools 创建交接工具（仅在启用了子代理的主会话注册）。
func newHandoffTools(p *AIChatPlugin, b bot.Bot, id message.QID, isGroup bool) []llmtool.Tool {
	base := subagentToolBase{plugin: p, bot: b, id: id, isGroup: isGroup}
	desc := "把当前回合尚未完成的剩余工作转入后台继续执行（交接）。适用场景：任务较长、本轮时间接近预算、或用户需要马上得到回应。" +
		"调用后立即返回任务 ID，本轮应马上结束并给用户一句简短说明（例如「已转入后台，完成后我把结果发这里」）。" +
		"交接内容必须自包含：已完成什么、还剩什么、验收标准；不要粘贴完整对话历史。子代理会以全新上下文执行剩余工作。"
	return []llmtool.Tool{
		&handoffTool{
			BaseTool:         llmtool.MakeBaseTool("task_handoff", desc, handoffParams{}),
			subagentToolBase: base,
		},
	}
}

func (t *handoffTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*handoffParams)
	summary := strings.TrimSpace(p.Summary)
	remaining := strings.TrimSpace(p.Remaining)
	if remaining == "" {
		return "", fmt.Errorf("remaining 不能为空：必须写明剩余要做的具体工作")
	}
	var b strings.Builder
	b.WriteString("【后台续接任务】你接手的是另一个 AI 已经开始的长期任务，必须在全新上下文中独立完成剩余工作。\n\n")
	b.WriteString("【此前已完成】\n")
	if summary == "" {
		b.WriteString("（未提供摘要，请先自行核对现状再继续）\n")
	} else {
		b.WriteString(summary + "\n")
	}
	b.WriteString("\n【剩余工作】\n")
	b.WriteString(remaining + "\n")
	if acceptance := strings.TrimSpace(p.Acceptance); acceptance != "" {
		b.WriteString("\n【验收标准】\n")
		b.WriteString(acceptance + "\n")
	}
	b.WriteString("\n【要求】\n- 独立完成剩余工作，不要重复已完成的部分；动手前先核对现状。\n")
	b.WriteString("- 最终给出完整、自包含的结果：结论 + 关键证据 + 未完成项/风险。\n")
	b.WriteString("- 若确实需要用户决策，在结果开头标注【需要决策】并给出选项。\n")

	return t.plugin.launchAsyncSubagent(ctx, t.bot, t.id, t.isGroup, b.String(), 0, callbacks) +
		"\n\n提示：交接已生效，本轮请立即收尾，用一两句话告诉用户剩余工作已转入后台以及任务 ID；不要继续调用工具等待结果。", nil
}
