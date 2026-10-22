package pluginaichat

// 主动互动的 AI 工具：让模型自己「预约一会儿要说的话」。
//
// 与 clock_* 的分工：
//   - clock_create 是到点必须执行的任务（提醒、定时汇报），无条件推送结果；
//   - proactive_create 只是「我想过一会儿说句话」，到点后仍要过闸门（免打扰、
//     每日上限、现场是否已恢复活跃），AI 在执行时还可以用 skip_output 放弃。
//
// 典型用法：问了一个问题暂时没人答、约好稍后跟进某件事、想到一件事值得
// 过一会儿再提。工具只能在当前会话内创建，不能跨会话安排发言。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// proactiveToolBase 所有主动互动工具共享的会话绑定。
type proactiveToolBase struct {
	mgr   *proactiveManager
	scope string // 绑定当前会话（g:群ID / f:用户ID），杜绝跨会话越权
}

// ---- proactive_create ----

type proactiveCreateParams struct {
	DelayMinutes int    `json:"delay_minutes" desc:"多少分钟后尝试主动发言，范围 1~10080（最长 7 天）"`
	Prompt       string `json:"prompt" desc:"到点后你想做的事 / 想说的话的意图描述，例如「问问小明昨天说的面试结果怎么样」。写成给自己看的指令，不是直接发送的文本"`
	Note         string `json:"note,omitempty" desc:"可选备注，仅记录在意图里方便排查"`
}

type proactiveCreateTool struct {
	llmtool.BaseTool[proactiveCreateParams]
	proactiveToolBase
}

func newProactiveCreateTool(mgr *proactiveManager, scope string, isGroup bool) llmtool.Tool {
	desc := "预约一次「过一会儿主动说句话」：到点后会结合当时的现场决定说不说、说什么，" +
		"并受免打扰时段、每日上限、现场是否已恢复活跃等约束（不像 clock_create 那样无条件执行）。" +
		"当前会话为"
	if isGroup {
		desc += "群聊，只能为这个群预约，不能发到别的会话。"
	} else {
		desc += "私聊，只能为这个私聊预约，不能发到别的会话。"
	}
	desc += "适用场景：你提的问题暂时没人回答想稍后跟进、约好稍后提醒/关心某件事、想到一个过一会儿值得再说的话题。" +
		"不要用它做用户明确要求的定时提醒（用 clock_create），也不要用来对自己不能确认的事瞎猜。"
	return &proactiveCreateTool{
		BaseTool:          llmtool.MakeBaseTool("proactive_create", desc, proactiveCreateParams{}),
		proactiveToolBase: proactiveToolBase{mgr: mgr, scope: scope},
	}
}

func (t *proactiveCreateTool) Execute(_ context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*proactiveCreateParams)
	if !ok || p == nil {
		return "", fmt.Errorf("参数解析失败")
	}
	if t.mgr == nil {
		return "", fmt.Errorf("主动互动未启用")
	}
	if !t.mgr.enabled(t.scope) {
		return "当前会话未开启主动互动，这次想法没有保存。需要管理员发送 /proactive on（群聊里要先 @ 机器人；或在面板「AI 对话 · 主动互动」里把本会话加入白名单）后才会生效。", nil
	}
	if p.DelayMinutes < 1 || p.DelayMinutes > 10080 {
		return "", fmt.Errorf("delay_minutes 需要在 1~10080 之间（分钟）")
	}
	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt 不能为空，请写清到点后你想做什么")
	}
	if pending := t.mgr.store.pendingByScope(t.scope); len(pending) >= proactiveMaxPendingScope {
		return fmt.Sprintf("当前会话已有 %d 条待执行的主动发言意图（上限 %d），先等它们执行完或取消掉一些再预约。",
			len(pending), proactiveMaxPendingScope), nil
	}

	now := time.Now()
	due := now.Add(time.Duration(p.DelayMinutes) * time.Minute)
	it := &ProactiveIntention{
		ID:        newIntentionID(),
		Scope:     t.scope,
		Source:    proactiveSourceAI,
		Prompt:    truncateRunes(prompt, proactiveMaxPromptRunes),
		Note:      truncateRunes(strings.TrimSpace(p.Note), 300),
		DueAt:     due,
		ExpiresAt: due.Add(t.mgr.expire),
		Status:    proactiveStatusPending,
		CreatedBy: "ai",
		CreatedAt: now,
	}
	if !t.mgr.store.insert(it) {
		return "", fmt.Errorf("保存意图失败")
	}
	t.mgr.logger.Info("AI 创建主动发言意图", "id", it.ID, "scope", t.scope,
		"delay_minutes", p.DelayMinutes, "prompt", truncateRunes(prompt, 100))
	return fmt.Sprintf("已记下（ID: %s），约 %d 分钟后（%s）看情况主动开口。到点后还会看现场气氛，可能放弃；如需取消用 proactive_cancel。",
		it.ID, p.DelayMinutes, due.Format("01-02 15:04")), nil
}

// ---- proactive_list ----

type proactiveListParams struct{}

type proactiveListTool struct {
	llmtool.BaseTool[proactiveListParams]
	proactiveToolBase
}

func (t *proactiveListTool) Execute(_ context.Context, _ any, _ llmtool.CallBackFuncs) (string, error) {
	if t.mgr == nil {
		return "", fmt.Errorf("主动互动未启用")
	}
	items := t.mgr.store.pendingByScope(t.scope)
	if len(items) == 0 {
		return "当前会话没有待执行的主动发言意图。", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("当前会话待执行的主动发言意图（%d 条）：\n", len(items)))
	for i := range items {
		sb.WriteString(formatIntentionLine(&items[i]))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ---- proactive_cancel ----

type proactiveCancelParams struct {
	ID string `json:"id" desc:"要取消的意图 ID（来自 proactive_list 或 proactive_create 的返回值）"`
}

type proactiveCancelTool struct {
	llmtool.BaseTool[proactiveCancelParams]
	proactiveToolBase
}

func (t *proactiveCancelTool) Execute(_ context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*proactiveCancelParams)
	if !ok || p == nil || strings.TrimSpace(p.ID) == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	if t.mgr == nil {
		return "", fmt.Errorf("主动互动未启用")
	}
	id := strings.TrimSpace(p.ID)
	// 正在执行的意图取消不到：执行结束会写回 sent，这里先明确拒绝
	if t.mgr.isRunning(id) {
		return "这条意图正在执行，无法取消；执行结束后可用 proactive_list 确认结果。", nil
	}
	for _, it := range t.mgr.store.pendingByScope(t.scope) {
		if it.ID != id {
			continue
		}
		it.Status = proactiveStatusCancelled
		it.Reason = "AI 主动取消"
		if !t.mgr.store.update(&it) {
			return "", fmt.Errorf("取消失败")
		}
		return "已取消意图 " + id + "。", nil
	}
	return "当前会话没有这条待执行的意图（可能已经执行或属于其他会话）。", nil
}

// newProactiveTools 创建绑定当前会话的主动互动工具集。
func newProactiveTools(mgr *proactiveManager, scope string, isGroup bool) []llmtool.Tool {
	base := proactiveToolBase{mgr: mgr, scope: scope}
	return []llmtool.Tool{
		newProactiveCreateTool(mgr, scope, isGroup),
		&proactiveListTool{
			BaseTool: llmtool.MakeBaseTool("proactive_list",
				"列出当前会话里待执行的主动发言意图（AI 自行预约的、以及冷场自动生成的），用于确认自己约过什么、什么时候到点。",
				proactiveListParams{}),
			proactiveToolBase: base,
		},
		&proactiveCancelTool{
			BaseTool: llmtool.MakeBaseTool("proactive_cancel",
				"取消当前会话里一条尚未执行的主动发言意图（例如话题已经聊过了、用户已经回答了）。只能取消本会话的意图。",
				proactiveCancelParams{}),
			proactiveToolBase: base,
		},
	}
}

// formatIntentionLine 把一条意图格式化成单行摘要（命令与工具共用）。
func formatIntentionLine(it *ProactiveIntention) string {
	source := map[string]string{
		proactiveSourceIdle:   "冷场",
		proactiveSourceAI:     "AI 预约",
		proactiveSourceManual: "手动",
	}[it.Source]
	if source == "" {
		source = it.Source
	}
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("[%s] %s · %s · 到点 %s",
		it.ID, source, it.Status, it.DueAt.Local().Format("01-02 15:04")))
	if it.Prompt != "" {
		sb.WriteString(" · ")
		sb.WriteString(truncateRunes(strings.Join(strings.Fields(it.Prompt), " "), 60))
	}
	return sb.String()
}
