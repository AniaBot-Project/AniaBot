package pluginaichat

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// teamToolBase 团队工具共享插件引用与当前会话信息（创建时绑定）。
type teamToolBase struct {
	plugin  *AIChatPlugin
	bot     bot.Bot
	id      message.QID
	isGroup bool
}

// newTeamTools 创建 Agent 团队工具（team_run / team_create / team_list / team_delete），
// 注册到主会话执行器。sessionDesc 与预置角色列表写入工具描述。
func newTeamTools(p *AIChatPlugin, b bot.Bot, id message.QID, isGroup bool) []llmtool.Tool {
	base := teamToolBase{plugin: p, bot: b, id: id, isGroup: isGroup}
	sessionDesc := "私聊（对方 ID " + id.String() + "）"
	if isGroup {
		sessionDesc = "群聊（会话 ID " + id.String() + "）"
	}
	roles := builtinTeamRoleNames()
	runDesc := "组建一个多代理团队并同步执行：把任务并行派发给多个成员代理（每个成员以全新上下文运行，" +
		"拥有与你一致的工具能力，以其实际可用的工具列表为准），全部完成后汇总各成员结果返回，由你综合出最终回复。" +
		"适用场景：需要多视角/多维度处理的任务（如同时调研与评审、交叉验证）。两种用法：①所有成员执行同一总体任务" +
		"（task 字段）；②作为 leader 分工，在不同成员的 task 字段填写各自专属任务（如让后端程序员审查后端代码、测试工程师执行测试），" +
		"成员会先收到总体任务作为背景再执行专属任务。" +
		"成员指定三种方式：① role 填内联自定义角色描述（优先级最高）；② name 填预置角色（" + roles + "）；" +
		"③ name 填当前会话已保存团队（team_list 查看）或全局团队（Web 面板管理的跨会话团队）中的成员名。未识别的 name 会按普通子代理执行。" +
		"当前会话为" + sessionDesc + "，总体任务会发给每个成员。" +
		fmt.Sprintf("单成员默认超时 %d 秒，最多并行 %d 个成员；成员无法再组建团队或委派子代理。",
			int(p.subagentTimeout().Seconds()), teamMaxMembers)
	return []llmtool.Tool{
		&teamRunTool{
			BaseTool:     llmtool.MakeBaseTool("team_run", runDesc, teamRunParams{}),
			teamToolBase: base,
		},
		&teamCreateTool{
			BaseTool:     llmtool.MakeBaseTool("team_create", "在当前会话保存一个自定义团队（成员可带角色描述），保存后即可通过 team_run 的 name 引用团队成员。团队按群聊/私聊隔离，仅当前会话可见，跨重启保留；跨会话共享的全局团队请通过 Web 面板管理", teamCreateParams{}),
			teamToolBase: base,
		},
		&teamListTool{
			BaseTool:     llmtool.MakeBaseTool("team_list", "列出当前会话已保存的自定义团队及其成员", teamListParams{}),
			teamToolBase: base,
		},
		&teamDeleteTool{
			BaseTool:     llmtool.MakeBaseTool("team_delete", "按名称删除当前会话已保存的一个自定义团队", teamDeleteParams{}),
			teamToolBase: base,
		},
	}
}

// teamScope 返回当前会话的持久化 scope（与 memory 工具一致：g:会话ID / f:用户ID）。
func (b teamToolBase) teamScope() string {
	if b.isGroup {
		return "g:" + b.id.String()
	}
	return "f:" + b.id.String()
}

// ---- team_run ----

// teamMemberParam 团队成员指定：name 为成员标识，role 为可选的内联角色描述，
// task 为可选的专属任务（leader 分工时按成员分别填写）。
type teamMemberParam struct {
	Name string `json:"name" desc:"成员标识：预置角色名（规划师/研究员/程序员/代码审查员/分析师/编辑）或当前会话已保存团队中的成员名；未命中时降级为普通子代理"`
	Role string `json:"role,omitempty" desc:"内联自定义角色描述（作为该成员的系统提示词），优先级最高；不填时按 name 解析"`
	Task string `json:"task,omitempty" desc:"该成员的专属任务（可选）：作为 leader 分工时填写，如让后端程序员审查后端代码、测试工程师执行测试；不填则只执行总体任务。填写后成员会先收到总体任务作为背景，再执行专属任务"`
}

type teamRunParams struct {
	Task       string            `json:"task" desc:"团队总体任务/背景（必填）：作为共享背景发给所有成员；成员未填专属任务时即为其执行内容。作为 leader 分工时，可在成员各自的 task 字段给出具体分工"`
	Members    []teamMemberParam `json:"members" desc:"要并行执行的成员列表（1 至上限个，上限见工具描述）"`
	TimeoutSec int               `json:"timeout_sec,omitempty" desc:"本次执行的超时秒数（上限 1800），不填用默认值"`
}

type teamRunTool struct {
	llmtool.BaseTool[teamRunParams]
	teamToolBase
}

func (t *teamRunTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*teamRunParams)
	task := strings.TrimSpace(p.Task)
	if task == "" {
		return "", fmt.Errorf("task 不能为空")
	}
	if len(p.Members) == 0 {
		return "", fmt.Errorf("members 不能为空（至少指定 1 个成员）")
	}
	if len(p.Members) > teamMaxMembers {
		return "", fmt.Errorf("成员数量 %d 超过上限 %d", len(p.Members), teamMaxMembers)
	}

	// 成员名去重校验（按 TrimSpace 后比较），重复会让汇总报告无法区分
	seen := make(map[string]bool, len(p.Members))
	specs := make([]teamMemberSpec, 0, len(p.Members))
	for i, mem := range p.Members {
		name := strings.TrimSpace(mem.Name)
		if name == "" {
			return "", fmt.Errorf("第 %d 个成员 name 不能为空", i+1)
		}
		if seen[name] {
			return "", fmt.Errorf("成员重复：%s", name)
		}
		seen[name] = true
		spec := t.plugin.resolveTeamMemberSpec(t.plugin.teamManager, t.teamScope(), name, mem.Role)
		spec.task = teamEffectiveTask(task, mem.Task)
		specs = append(specs, spec)
	}

	report, err := t.plugin.runTeam(ctx, t.bot, t.id, t.isGroup, p.TimeoutSec, specs, callbacks)
	if err != nil {
		return "", err
	}
	return report, nil
}

// ---- team_create ----

type teamCreateParams struct {
	Name    string            `json:"name" desc:"团队名（中文/字母/数字/下划线/连字符，1-20 字符），同一会话内唯一"`
	Desc    string            `json:"desc,omitempty" desc:"团队说明（用途、适合的任务类型等），便于后续识别"`
	Members []teamMemberParam `json:"members" desc:"团队成员列表（1 至 10 个）：name 填成员名，role 填该成员的角色描述；同名成员不可重复"`
}

type teamCreateTool struct {
	llmtool.BaseTool[teamCreateParams]
	teamToolBase
}

func (t *teamCreateTool) Execute(_ context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p := params.(*teamCreateParams)
	members := make([]teamMember, 0, len(p.Members))
	for _, mem := range p.Members {
		members = append(members, teamMember{
			Name: strings.TrimSpace(mem.Name),
			Role: strings.TrimSpace(mem.Role),
		})
	}
	def, err := t.plugin.teamManager.create(t.teamScope(), p.Name, p.Desc, members)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("团队「%s」已创建（成员 %d 人），可通过 team_run 的 name 引用其成员", def.Name, len(def.Members)), nil
}

// ---- team_list ----

type teamListParams struct{}

type teamListTool struct {
	llmtool.BaseTool[teamListParams]
	teamToolBase
}

func (t *teamListTool) Execute(_ context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	defs := t.plugin.teamManager.list(t.teamScope())
	if len(defs) == 0 {
		return "当前会话还没有保存的团队，可用 team_create 创建", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "当前会话已保存的团队（共 %d 个）：\n\n", len(defs))
	for i, def := range defs {
		names := make([]string, 0, len(def.Members))
		for _, mem := range def.Members {
			names = append(names, mem.Name)
		}
		fmt.Fprintf(&sb, "%d. %s（成员 %d 人）\n", i+1, def.Name, len(def.Members))
		if def.Desc != "" {
			sb.WriteString("   说明: " + def.Desc + "\n")
		}
		sb.WriteString("   成员: " + strings.Join(names, " / ") + "\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ---- team_delete ----

type teamDeleteParams struct {
	Name string `json:"name" desc:"要删除的团队名"`
}

type teamDeleteTool struct {
	llmtool.BaseTool[teamDeleteParams]
	teamToolBase
}

func (t *teamDeleteTool) Execute(_ context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p := params.(*teamDeleteParams)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return "", fmt.Errorf("name 不能为空")
	}
	if t.plugin.teamManager.delete(t.teamScope(), name) {
		return fmt.Sprintf("已删除团队「%s」", name), nil
	}
	return fmt.Sprintf("未找到团队「%s」，可先用 team_list 查看已保存的团队", name), nil
}
