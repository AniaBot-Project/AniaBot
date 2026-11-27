package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// subagentToolBase 为子代理工具共享插件引用与当前会话信息（创建时绑定）。
type subagentToolBase struct {
	plugin  *AIChatPlugin
	bot     bot.Bot
	id      message.QID
	isGroup bool
}

// newSubagentTools 创建子代理相关工具，注册到当前会话的执行器中。
func newSubagentTools(p *AIChatPlugin, b bot.Bot, id message.QID, isGroup bool) []llmtool.Tool {
	base := subagentToolBase{plugin: p, bot: b, id: id, isGroup: isGroup}
	sessionDesc := "私聊（对方 ID " + id.String() + "）"
	if isGroup {
		sessionDesc = "群聊（会话 ID " + id.String() + "）"
	}
	// 子代理默认超时的展示文案（0=不限制）
	timeoutDesc := "不限制"
	if d := p.subagentTimeout(); d > 0 {
		timeoutDesc = fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	// 完成条目保留窗口（0=不保留，完成后不可追问）
	finishedDesc := "不保留完成条目（完成后不可追问）"
	if ttl := p.finishedSubagentTTL(); ttl > 0 {
		finishedDesc = fmt.Sprintf("完成后 %d 分钟内可追问", int(ttl.Minutes()))
	}
	runDesc := "将一个复杂/耗时的子任务委派给一次性子代理后台执行。子代理以全新上下文运行（看不到当前对话历史），" +
		"拥有与你一致的工具能力（以其实际可用的工具列表为准），执行完毕后结果会自动回到当前会话并触发你继续处理。" +
		"\n\n何时使用：预计需要 3 步以上工具调用、超过 1 分钟、或需要多轮搜索/调研/读写多文件的独立子任务，应该直接委派，" +
		"不要在主对话里逐步做完。多个互不依赖的子任务可以在同一条回复里并行启动多个 subagent_run（不要串行等待）。" +
		"简单问题、闲聊、需要与用户反复澄清的任务不要委派。" +
		"\n\n行为：启动后立即返回任务 ID，不阻塞本轮；委派后不要等待或轮询，继续处理其他问题或结束本轮。" +
		"子代理完成后请基于结果给用户最终答复。子代理无法再委派子代理。" +
		"\n\n完成后可用 subagent_followup 基于原任务+原结果继续追问，不必重新完整委派；" +
		"也可用 subagent_send 对已完成条目自动追问。" +
		fmt.Sprintf("\n\n默认超时 %s；可用 timeout_sec 覆盖；%s。当前会话为 %s。", timeoutDesc, finishedDesc, sessionDesc)
	followupDesc := "对一个已完成的后台任务追问（不必重新完整委派）。适用于：子代理/团队成员/工作流/团队运行已经返回结果，" +
		"你想基于已有结论再问一句、补充细节、核实某个点。系统会把「原任务 + 上一轮结果 + 你的追问」作为新任务，" +
		"启动一个新的异步子代理继续回答（团队成员沿用原角色提示词），立即返回新任务 ID。" +
		"\n\n仅对已完成且在保留窗口内的条目有效（见 subagent_list(include_finished=true)），运行中的任务请用 subagent_send。" +
		"团队运行可按成员 ID 单独追问；团队运行 ID 也可追问，此时会基于整个团队报告继续。"
	sendDesc := "向一个正在运行的后台子代理追加指令或补充信息（子代理会在下一步看到，不会打断它当前的工具调用）。" +
		"适用于：发现任务方向需要纠正、补充遗漏的背景信息、要求它调整输出格式。" +
		"\n\n对已完成的条目会自动按 subagent_followup 语义重启一个追问子代理，返回新任务 ID。" +
		"团队运行 ID 会广播给全部成员；成员 ID 只发给该成员。"
	return []llmtool.Tool{
		&subagentRunTool{
			BaseTool:         llmtool.MakeBaseTool("subagent_run", runDesc, subagentRunParams{}),
			subagentToolBase: base,
		},
		&subagentListTool{
			BaseTool: llmtool.MakeBaseTool("subagent_list",
				"列出当前会话的后台工作：运行中的异步子代理/工作流/团队运行/团队成员，以及保留窗口内已完成、可追问的条目"+
					"（ID、类型、任务摘要、运行时间/完成状态与剩余追问时间）。", subagentListParams{}),
			subagentToolBase: base,
		},
		&subagentFollowupTool{
			BaseTool:         llmtool.MakeBaseTool("subagent_followup", followupDesc, subagentFollowupParams{}),
			subagentToolBase: base,
		},
		&subagentSendTool{
			BaseTool:         llmtool.MakeBaseTool("subagent_send", sendDesc, subagentSendParams{}),
			subagentToolBase: base,
		},
		&subagentCancelTool{
			BaseTool:         llmtool.MakeBaseTool("subagent_cancel", "按 ID 或名字取消当前会话中一个正在运行的异步子代理（团队运行 ID 取消整队，成员 ID 只取消该成员）", subagentCancelParams{}),
			subagentToolBase: base,
		},
	}
}

// ---- subagent_run ----

type subagentRunParams struct {
	Task       string `json:"task" desc:"完整自洽的任务指令：子代理看不到当前对话，必须把背景、目标、期望的输出格式都写清楚"`
	Name       string `json:"name,omitempty" desc:"可选展示名（1-32 位小写字母/数字/连字符，字母开头），便于后续 subagent_send/subagent_cancel 引用；同一会话运行中不可重名"`
	TimeoutSec int    `json:"timeout_sec,omitempty" desc:"本次执行的超时秒数，不填用默认值（subagent.default_timeout_sec，0 表示不限制）；受 subagent.max_timeout_sec 约束"`
}

type subagentRunTool struct {
	llmtool.BaseTool[subagentRunParams]
	subagentToolBase
}

func (t *subagentRunTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*subagentRunParams)
	task := strings.TrimSpace(p.Task)
	if task == "" {
		return "", fmt.Errorf("task 不能为空")
	}
	return t.plugin.launchAsyncSubagent(ctx, t.bot, t.id, t.isGroup, task, launchSubagentOptions{
		Name:       strings.TrimSpace(p.Name),
		Kind:       taskKindSubagent,
		TimeoutSec: p.TimeoutSec,
		Callbacks:  callbacks,
	}), nil
}

// ---- subagent_list ----

type subagentListParams struct {
	IncludeFinished bool `json:"include_finished,omitempty" desc:"是否包含已完成、仍在追问保留窗口内的条目（默认 false 只看运行中）；准备 followup 时设为 true"`
}

type subagentListTool struct {
	llmtool.BaseTool[subagentListParams]
	subagentToolBase
}

func (t *subagentListTool) Execute(_ context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p, _ := params.(*subagentListParams)
	includeFinished := p != nil && p.IncludeFinished
	entries := t.plugin.listSessionWork(t.id, t.isGroup, includeFinished)
	if len(entries) == 0 {
		if includeFinished {
			return "当前会话没有运行中或可追问的后台任务（完成条目只在保留窗口内可追问，过期后请重新委派）", nil
		}
		return "当前没有正在运行的后台任务（已完成的子代理可用 subagent_list(include_finished=true) 查看并 followup）", nil
	}

	running, finished := 0, 0
	for _, e := range entries {
		if e.finished {
			finished++
		} else {
			running++
		}
	}
	ttl := t.plugin.finishedSubagentTTL()
	var sb strings.Builder
	if includeFinished {
		fmt.Fprintf(&sb, "当前会话后台工作（运行中 %d 个，可追问 %d 个；完成条目保留 %d 分钟）:\n\n", running, finished, int(ttl.Minutes()))
	} else {
		fmt.Fprintf(&sb, "当前运行中的后台工作（共 %d 个）:\n\n", running)
	}
	for i, e := range entries {
		kind := asyncEntryKindLabel(e.kind)
		nameLine := ""
		if e.name != "" {
			nameLine = "（名字: " + e.name + "）"
		}
		fmt.Fprintf(&sb, "%d. [%s] %s%s", i+1, kind, e.id, nameLine)
		if e.label != "" {
			fmt.Fprintf(&sb, " · %s", e.label)
		}
		if e.parentID != "" {
			fmt.Fprintf(&sb, " · 父任务: %s", e.parentID)
		}
		sb.WriteString("\n")
		if !e.finished {
			fmt.Fprintf(&sb, "   状态: 运行中（%s）\n", formatStatusDuration(time.Since(e.startTime)))
		} else {
			statusText := taskStatusLabel(taskStatusCompleted)
			if e.err != "" {
				status, _ := taskRunStatus(errors.New(e.err))
				statusText = taskStatusLabel(status) + "（" + tasklog.Truncate(e.err, 200) + "）"
			}
			remaining := "已过期"
			if ttl > 0 {
				if left := time.Until(e.finishedAt.Add(ttl)); left > 0 {
					remaining = "剩余 " + formatStatusDuration(left)
				}
			}
			fmt.Fprintf(&sb, "   状态: %s · 完成于 %s 前 · 可追问窗口: %s\n",
				statusText, formatStatusDuration(time.Since(e.finishedAt)), remaining)
		}
		fmt.Fprintf(&sb, "   任务: %s\n", tasklog.Truncate(e.task, 80))
		if e.finished && e.result != "" {
			fmt.Fprintf(&sb, "   结果摘要: %s\n", tasklog.Truncate(e.result, 300))
			fmt.Fprintf(&sb, "   追问: subagent_followup(id=\"%s\", message=\"...\")\n", e.id)
		}
		if i < len(entries)-1 {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n提示：运行中的任务可用 subagent_send 追加指令、subagent_cancel 取消；已完成的任务用 subagent_followup 追问；它们完成后会自动通知你，不需要轮询。")
	return sb.String(), nil
}

// ---- subagent_followup ----

type subagentFollowupParams struct {
	ID         string `json:"id" desc:"已完成的任务 ID（或名字/成员名）；通过 subagent_list(include_finished=true) 获取"`
	Message    string `json:"message" desc:"要追问的问题/新要求；会拼在原任务和上一轮结果之后，交给新子代理回答"`
	TimeoutSec int    `json:"timeout_sec,omitempty" desc:"追问执行的超时秒数，不填用默认值（subagent.default_timeout_sec，0 表示不限制）；受 subagent.max_timeout_sec 约束"`
}

type subagentFollowupTool struct {
	llmtool.BaseTool[subagentFollowupParams]
	subagentToolBase
}

func (t *subagentFollowupTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*subagentFollowupParams)
	idOrName := strings.TrimSpace(p.ID)
	if idOrName == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	msg := strings.TrimSpace(p.Message)
	if msg == "" {
		return "", fmt.Errorf("message 不能为空")
	}
	return t.plugin.followupSubagent(ctx, t.bot, t.id, t.isGroup, idOrName, msg, p.TimeoutSec, callbacks), nil
}

// ---- subagent_send ----

type subagentSendParams struct {
	ID      string `json:"id" desc:"运行中的任务 ID/名字，或已完成的任务 ID（已完成时自动按追问重启）"`
	Message string `json:"message" desc:"要追加/追问的内容；运行中会在下一次模型请求时注入，已完成时作为追问问题"`
}

type subagentSendTool struct {
	llmtool.BaseTool[subagentSendParams]
	subagentToolBase
}

func (t *subagentSendTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*subagentSendParams)
	idOrName := strings.TrimSpace(p.ID)
	if idOrName == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	msg := strings.TrimSpace(p.Message)
	if msg == "" {
		return "", fmt.Errorf("message 不能为空")
	}
	entry, ok := t.plugin.lookupSessionWorkEntry(t.id, t.isGroup, idOrName)
	if !ok {
		return fmt.Sprintf("未找到 %s 对应的后台任务（可能已完成且超过追问保留窗口，或 ID 不正确）", idOrName), nil
	}
	if !entry.finished {
		if t.plugin.sendSubagentMessage(t.id, t.isGroup, idOrName, msg) {
			return fmt.Sprintf("已向子代理 %s 追加指令，它会在下一步看到", idOrName), nil
		}
		// 查找与写入之间子代理可能刚好完成：重查一次，已完成则改走追问语义
		if latest, ok := t.plugin.lookupSessionWorkEntry(t.id, t.isGroup, entry.id); ok && latest.finished {
			return t.plugin.followupSubagent(ctx, t.bot, t.id, t.isGroup, latest.id, msg, 0, callbacks), nil
		}
		return fmt.Sprintf("已找到运行中的 %s，但它的指令队列已满，请稍后重试或等它完成后再追问", idOrName), nil
	}
	// 已完成：自动降级为 followup，避免模型先 send 失败再重新派一次完整任务
	return t.plugin.followupSubagent(ctx, t.bot, t.id, t.isGroup, entry.id, msg, 0, callbacks), nil
}

// ---- subagent_cancel ----

type subagentCancelParams struct {
	ID string `json:"id" desc:"要取消的任务 ID 或名字（通过 subagent_list 获取）"`
}

type subagentCancelTool struct {
	llmtool.BaseTool[subagentCancelParams]
	subagentToolBase
}

func (t *subagentCancelTool) Execute(_ context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*subagentCancelParams)
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	if ok := t.plugin.cancelSubagentByID(t.id, t.isGroup, id); !ok {
		return fmt.Sprintf("未找到 %s 对应的运行中任务（可能已完成或 ID 不正确）", id), nil
	}
	return fmt.Sprintf("已取消任务 %s", id), nil
}
