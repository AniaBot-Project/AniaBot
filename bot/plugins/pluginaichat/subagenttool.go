package pluginaichat

import (
	"context"
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
	runDesc := "将一个复杂/耗时的子任务委派给一次性子代理后台执行。子代理以全新上下文运行（看不到当前对话历史），" +
		"拥有与你一致的工具能力（以其实际可用的工具列表为准），执行完毕后结果会自动回到当前会话并触发你继续处理。" +
		"\n\n何时使用：预计需要 3 步以上工具调用、超过 1 分钟、或需要多轮搜索/调研/读写多文件的独立子任务，应该直接委派，" +
		"不要在主对话里逐步做完。多个互不依赖的子任务可以在同一条回复里并行启动多个 subagent_run（不要串行等待）。" +
		"简单问题、闲聊、需要与用户反复澄清的任务不要委派。" +
		"\n\n行为：启动后立即返回，不阻塞本轮；委派后不要等待或轮询，继续处理其他问题或结束本轮。" +
		"子代理完成后请基于结果给用户最终答复。子代理无法再委派子代理。" +
		fmt.Sprintf("\n\n默认超时 %s；可用 timeout_sec 覆盖。当前会话为 %s。", timeoutDesc, sessionDesc)
	return []llmtool.Tool{
		&subagentRunTool{
			BaseTool:         llmtool.MakeBaseTool("subagent_run", runDesc, subagentRunParams{}),
			subagentToolBase: base,
		},
		&subagentListTool{
			BaseTool:         llmtool.MakeBaseTool("subagent_list", "列出当前会话中正在运行的异步子代理（ID、名字、任务摘要、运行时间）", subagentListParams{}),
			subagentToolBase: base,
		},
		&subagentSendTool{
			BaseTool: llmtool.MakeBaseTool("subagent_send", "向一个正在运行的后台子代理追加指令或补充信息（子代理会在下一步看到，不会打断它当前的工具调用）。"+
				"适用于：发现任务方向需要纠正、补充遗漏的背景信息、要求它调整输出格式。子代理已完成或 ID 错误时返回未找到。", subagentSendParams{}),
			subagentToolBase: base,
		},
		&subagentCancelTool{
			BaseTool:         llmtool.MakeBaseTool("subagent_cancel", "按 ID 或名字取消当前会话中一个正在运行的异步子代理", subagentCancelParams{}),
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

type subagentListParams struct{}

type subagentListTool struct {
	llmtool.BaseTool[subagentListParams]
	subagentToolBase
}

func (t *subagentListTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	entries := t.plugin.listRunningSubagents(t.id, t.isGroup)
	if len(entries) == 0 {
		return "当前没有正在运行的子代理", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "当前运行中的子代理（共 %d 个）:\n\n", len(entries))
	for i, e := range entries {
		elapsed := time.Since(e.startTime).Truncate(time.Second)
		// 按 rune 截断：任务描述多为中文，按字节切会产出非法 UTF-8
		taskPreview := tasklog.Truncate(e.task, 60)
		nameLine := ""
		if e.name != "" {
			nameLine = "   名字: " + e.name + "\n"
		}
		fmt.Fprintf(&sb, "%d. ID: %s\n%s   任务: %s\n   运行时间: %s\n",
			i+1, e.id, nameLine, taskPreview, elapsed)
		if i < len(entries)-1 {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n提示：可用 subagent_send 追加指令、subagent_cancel 取消；它们完成后会自动通知你，不需要轮询。")
	return sb.String(), nil
}

// ---- subagent_send ----

type subagentSendParams struct {
	ID      string `json:"id" desc:"运行中的子代理 ID 或名字（通过 subagent_list 获取）"`
	Message string `json:"message" desc:"要追加给子代理的指令/补充信息；它会在子代理下一次模型请求时注入"`
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
	if !t.plugin.sendSubagentMessage(t.id, t.isGroup, idOrName, msg) {
		return fmt.Sprintf("未找到运行中的子代理 %s（可能已完成），或它的指令队列已满", idOrName), nil
	}
	return fmt.Sprintf("已向子代理 %s 追加指令，它会在下一步看到", idOrName), nil
}

// ---- subagent_cancel ----

type subagentCancelParams struct {
	ID string `json:"id" desc:"要取消的子代理 ID 或名字（通过 subagent_list 获取）"`
}

type subagentCancelTool struct {
	llmtool.BaseTool[subagentCancelParams]
	subagentToolBase
}

func (t *subagentCancelTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*subagentCancelParams)
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	if ok := t.plugin.cancelSubagentByID(t.id, t.isGroup, id); !ok {
		return fmt.Sprintf("未找到 %s 对应的运行中子代理（可能已完成或 ID 不正确）", id), nil
	}
	return fmt.Sprintf("已取消子代理 %s", id), nil
}
