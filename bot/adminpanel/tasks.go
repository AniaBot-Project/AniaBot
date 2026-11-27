package adminpanel

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
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

// TaskAdminSource 可选接口：在 TaskSource 之外提供后台任务日志的导出与清除
// （当前由 AI 对话插件实现）。清除只删除**已经结束**的任务行，运行中/排队中的
// 任务一律保留——它们是运行时状态机的记录，删掉会让进度写与收尾写落空。
type TaskAdminSource interface {
	// AgentTasksExport 返回满足筛选条件的全部任务（不分页，导出用）。
	AgentTasksExport(f plugininfo.AgentTaskFilter) []AgentTaskView
	// AgentTasksClear 删除满足筛选条件的已结束任务，返回删除条数。
	AgentTasksClear(f plugininfo.AgentTaskFilter) int
}

// AgentTaskView 后台任务注册表的一行（面板展示用，字段与 ania_agent_task 对齐）。
type AgentTaskView = plugininfo.AgentTaskView

// AgentTaskFilter 后台任务日志导出/清除的筛选条件（零值字段不过滤）。
type AgentTaskFilter = plugininfo.AgentTaskFilter

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

// parseAgentTaskFilter 解析后台任务导出/清除的筛选条件。字段与面板列表的本地
// 筛选一致；空值表示不过滤。非法组合（如带换行的关键词）不做额外限制——筛选只
// 影响命中的行，不会改变清除语义（仅删除已结束任务）。
func parseAgentTaskFilter(r *http.Request) plugininfo.AgentTaskFilter {
	q := r.URL.Query()
	return plugininfo.AgentTaskFilter{
		Status:  strings.TrimSpace(q.Get("status")),
		Kind:    strings.TrimSpace(q.Get("kind")),
		Keyword: strings.TrimSpace(q.Get("keyword")),
	}
}

// handleAgentTasksExport 按筛选条件导出全部后台任务日志（JSON 附件）。
// 与面板列表不同，导出不分页，覆盖列表未加载的更早记录；无数据源时返回空集合。
func (s *Server) handleAgentTasksExport(w http.ResponseWriter, r *http.Request) {
	if s.opt.AgentTasksExport == nil {
		writeError(w, http.StatusNotFound, "后台任务功能未启用")
		return
	}
	f := parseAgentTaskFilter(r)
	items := s.opt.AgentTasksExport(f)
	if items == nil {
		items = []AgentTaskView{}
	}
	oplog.Record(oplog.CategorySystem, "agent_task_export", fmt.Sprintf("面板导出后台任务日志 %d 条", len(items)))
	writeDownloadJSON(w, "agent-tasks-"+time.Now().Format("20060102-150405")+".json", map[string]any{
		"exported_at": time.Now().Format(time.RFC3339),
		"total":       len(items),
		"items":       items,
	})
}

// handleAgentTasksClear 按筛选条件清除已结束的后台任务日志（无筛选时清除全部
// 已结束任务）。运行中/排队中的任务保留，避免删除运行时仍在写的任务行。
func (s *Server) handleAgentTasksClear(w http.ResponseWriter, r *http.Request) {
	if s.opt.AgentTasksClear == nil {
		writeError(w, http.StatusNotFound, "后台任务功能未启用")
		return
	}
	f := parseAgentTaskFilter(r)
	n := s.opt.AgentTasksClear(f)
	s.opt.Logger.Info("后台任务日志已通过 Web 面板清除", "removed", n)
	oplog.Record(oplog.CategorySystem, "agent_task_clear", fmt.Sprintf("面板清除后台任务日志 %d 条", n))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
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
