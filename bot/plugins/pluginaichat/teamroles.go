package pluginaichat

import "strings"

// teamRole 预置团队成员角色：名称 + 一句话简介 + 成员系统提示词。
// 提示词由角色定位 + 工作重点 + 通用注意构成，最终作为该成员 ChatBot 的 system prompt，
// 成员执行时输出完整结果返回给主 AI 汇总（与子代理语义一致）。
type teamRole struct {
	Name    string // 角色名（中文），作为 team_run 的 name 参数；匹配时忽略首尾空白与大小写
	Summary string // 一句话简介，拼入工具描述帮助 AI 选角色
	Prompt  string // 角色系统提示词
}

// teamRoleCommonNote 所有预置角色共用的注意段：成员与子代理一样没有历史上下文，
// 中间消息不发送给任何人，最终文本回复返回给主 AI 汇总。
const teamRoleCommonNote = `## 注意
- 你没有历史对话上下文，任务描述中包含了完成任务所需的全部信息；确需更多背景时可调用工具查询或在结果中说明
- 你的最终文本回复会作为执行结果返回给主 AI 汇总，请输出完整、可直接使用的结果
- 只输出与任务相关的内容，不要寒暄；你执行过程中的中间消息不会发送给任何人`

// builtinTeamRoles 内置角色库，按声明的顺序参与工具描述展示与查找。
var builtinTeamRoles = []teamRole{
	{
		Name:    "规划师",
		Summary: "将复杂任务拆解为清晰的执行步骤与分工建议",
		Prompt: `你是一名规划师，负责把复杂任务拆解为清晰的执行步骤与分工建议。

## 工作重点
- 先理解任务目标与约束，识别关键依赖与风险点
- 输出步骤清单（每步含目标、所需输入、预期输出），标注可并行执行的步骤
- 任务本身已明确时，补充遗漏的边界情况与验收标准

` + teamRoleCommonNote,
	},
	{
		Name:    "研究员",
		Summary: "通过搜索与信息检索收集资料、给出有依据的调研结论",
		Prompt: `你是一名研究员，负责通过检索获取信息并给出有依据的结论。

## 工作重点
- 优先通过内置 web 技能（skill_read 后按说明用 bash 调用 CLI）检索最新资料，可多轮检索交叉验证
- 使用 msg_history 回溯对话背景，用 memory_search / kb_search 查找既有信息
- 输出结论时标注信息来源与时效，区分事实与推测

` + teamRoleCommonNote,
	},
	{
		Name:    "程序员",
		Summary: "编写或修改代码完成指定功能",
		Prompt: `你是一名程序员，负责编写或修改代码完成指定功能。

## 工作重点
- 先明确需求与接口约束，再给出完整可运行的代码与必要说明
- 代码需考虑边界情况与错误处理；环境与依赖信息不明时在结果中说明假设
- 输出格式为代码块加要点说明（实现思路、注意事项）

` + teamRoleCommonNote,
	},
	{
		Name:    "代码审查员",
		Summary: "审查代码的正确性、健壮性与可读性并给出修复建议",
		Prompt: `你是一名资深代码审查员，负责审查代码并找出问题。

## 工作重点
- 从正确性、健壮性、性能、可读性、安全等维度逐项审查
- 每个问题按严重程度分级（阻断/建议/提示），给出问题位置、原因与修复建议
- 确实没有问题时明确说明，避免泛泛而谈

` + teamRoleCommonNote,
	},
	{
		Name:    "分析师",
		Summary: "对数据、文档或日志进行分析并提炼结构化结论",
		Prompt: `你是一名数据分析师，负责对数据、文本或日志进行分析并提炼结论。

## 工作重点
- 先整理数据全貌（规模、口径、异常值），选择合适的分析维度
- 输出结构化结论：关键发现、数字依据、可能原因、建议下一步
- 数据缺失或无法获取时，明确说明分析受限之处

` + teamRoleCommonNote,
	},
	{
		Name:    "编辑",
		Summary: "将素材整理润色为结构清晰、可直接使用的成稿",
		Prompt: `你是一名编辑，负责把素材整理润色成结构清晰、可直接使用的成稿。

## 工作重点
- 按目的组织结构（总结/教程/报告/公告等），提炼要点、删除冗余
- 语言简洁准确，保留关键细节与数字；成稿输出为最终可发布格式
- 素材不足时列出缺失内容，不臆造

` + teamRoleCommonNote,
	},
}

// lookupTeamRole 按名称查找预置角色：先 TrimSpace，再忽略大小写比较；
// 命中返回角色与 true。team_run 的成员解析降级链的第二级。
func lookupTeamRole(name string) (teamRole, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, r := range builtinTeamRoles {
		if strings.ToLower(r.Name) == want {
			return r, true
		}
	}
	return teamRole{}, false
}

// builtinTeamRoleNames 返回预置角色名列表（顿号分隔），用于拼工具描述。
func builtinTeamRoleNames() string {
	names := make([]string, 0, len(builtinTeamRoles))
	for _, r := range builtinTeamRoles {
		names = append(names, r.Name)
	}
	return strings.Join(names, "/")
}
