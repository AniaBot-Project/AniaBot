package adminpanel

import (
	"net/http"
	"strconv"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// 面板「后台任务」页的数据来源（可选接口）。
//
// 后台任务注册表由 AI 对话插件维护（表 ania_agent_task）：每个离开当前回合继续
// 存在的工作（回合 / 子代理 / 交接 / 后台命令 / 定时 / 主动互动 / 目标轮次）都落
// 一行。面板只做只读展示与筛选，不提供任何增删改入口——任务状态机由插件掌握，
// 面板改动会与运行时状态冲突。
//
// 视图类型定义在 common/plugininfo（插件只依赖 common），这里用别名引用，
// 插件实现接口时无需 import bot/adminpanel。

// TaskSource 可选接口：插件实现后，面板「后台任务」页可查看 AI 后台任务注册表
// （当前由 AI 对话插件实现）。limit 为期望返回的最多条数（新在前）。
type TaskSource interface {
	AgentTasks(limit int) []AgentTaskView
}

// AgentTaskView 后台任务注册表的一行（面板展示用，字段与 ania_agent_task 对齐）。
type AgentTaskView = plugininfo.AgentTaskView

// GoalSource 可选接口：插件实现后，面板「后台任务」页可查看当前处于 active
// 阶段的长任务目标（跨回合自动续跑的会话目标，表 ania_agent_goal）。
type GoalSource interface {
	AgentGoals() []AgentGoalView
}

// AgentGoalView 一个 active 会话目标（面板展示用）。
type AgentGoalView = plugininfo.AgentGoalView

const (
	// agentTasksDefaultLimit 「后台任务」接口未显式指定 limit 时的默认条数。
	agentTasksDefaultLimit = 100
	// agentTasksMaxLimit 「后台任务」接口单次最多返回的条数（面板表格无需更多）。
	agentTasksMaxLimit = 500
)

// handleAgentTasks 返回最近的后台任务（新在前）。
// 支持的查询参数：limit（默认 100，取值 1~500，超过上限按上限截断）。
// 无数据源（插件未实现 TaskSource）时返回空数组，前端按空列表展示。
// 响应直接是任务数组 JSON（不分页，面板只展示最近 N 条）。
func (s *Server) handleAgentTasks(w http.ResponseWriter, r *http.Request) {
	limit := agentTasksDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit 格式错误（应为整数）")
			return
		}
		if n < 1 {
			writeError(w, http.StatusBadRequest, "limit 取值范围为 1~500")
			return
		}
		limit = n
	}
	if limit > agentTasksMaxLimit {
		limit = agentTasksMaxLimit
	}
	items := []AgentTaskView{}
	if s.opt.AgentTasks != nil {
		if got := s.opt.AgentTasks(limit); got != nil {
			items = got
		}
	}
	writeJSON(w, http.StatusOK, items)
}

// handleAgentGoals 返回当前 active 的会话目标（新在前）。
// 无数据源或没有 active 目标时返回空数组。
func (s *Server) handleAgentGoals(w http.ResponseWriter, _ *http.Request) {
	items := []AgentGoalView{}
	if s.opt.AgentGoals != nil {
		if got := s.opt.AgentGoals(); got != nil {
			items = got
		}
	}
	writeJSON(w, http.StatusOK, items)
}
