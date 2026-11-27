package pluginaichat

import (
	"log/slog"
	"strings"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// 面板「后台任务」页数据源（实现 adminpanel.TaskSource / TaskAdminSource /
// GoalSource）。
//
// 后台任务注册表（ania_agent_task）与长任务目标表（ania_agent_goal）都属于插件
// 的 SQL 持久层；面板是旁路视图：列表直接查最近 N 条，导出/清除把筛选条件下推，
// 不引入新的运行时状态。表结构见 taskstore.go / goalstore.go，本文件不改动它们。
// 行内同时承载 Agent 团队运行（team_run）与团队成员（team_member）记录，供面板
// 展示任务指令、成员产出与工具调用步骤。
//
// 降级约定：SQL 后端不可用（taskStore/goalStore 为 nil）或查询失败时返回空切片，
// 面板按空列表展示，不影响其它页面与插件运行。

// taskPanelDefaultLimit AgentTasks 未收到合法 limit 时使用的条数（与 adminpanel 默认一致）。
const taskPanelDefaultLimit = 100

// taskPanelColumns 面板查询列顺序，必须与下面的 Scan 顺序一致：与 taskstore.go 的
// taskColumns 相同（额外带 prompt，面板详情要展示任务指令）。
const taskPanelColumns = `id, session_key, parent_id, kind, status, title, prompt, result, ` +
	`error, stop_reason, tokens, meta, created_at, started_at, updated_at, finished_at`

// AgentTasks 返回最近的后台任务（updated_at 倒序），实现 adminpanel.TaskSource。
// 后台任务表没有「全局最近 N 条」的 store 方法，这里直接走一条只读 SQL：
// 两侧数据库方言通用，只用到 ? 占位符与 LIMIT，无需方言分支。
// 升级前遗留的 turn 行不返回：普通回合的完整过程在 Query 日志里，面板不再展示
// 这类历史行（任务表里仍保留到保留期结束，供 /tasks 兼容旧数据）。
func (p *AIChatPlugin) AgentTasks(limit int) []plugininfo.AgentTaskView {
	empty := []plugininfo.AgentTaskView{}
	if p.taskStore == nil || p.historyDB == nil {
		return empty
	}
	if limit <= 0 {
		limit = taskPanelDefaultLimit
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	rows, err := p.historyDB.QueryContext(ctx,
		`SELECT `+taskPanelColumns+` FROM ania_agent_task WHERE kind <> ? `+
			`ORDER BY updated_at DESC, id DESC LIMIT ?`, taskKindTurn, limit)
	if err != nil {
		p.taskPanelLogger().Warn("面板读取后台任务失败", "error", err.Error())
		return empty
	}
	defer rows.Close()
	views := make([]plugininfo.AgentTaskView, 0, limit)
	for rows.Next() {
		var v plugininfo.AgentTaskView
		if err := rows.Scan(&v.ID, &v.SessionKey, &v.ParentID, &v.Kind, &v.Status, &v.Title,
			&v.Prompt, &v.Result, &v.Error, &v.StopReason, &v.Tokens, &v.Meta,
			&v.CreatedAt, &v.StartedAt, &v.UpdatedAt, &v.FinishedAt); err != nil {
			p.taskPanelLogger().Warn("面板解析后台任务行失败", "error", err.Error())
			return empty
		}
		views = append(views, v)
	}
	if err := rows.Err(); err != nil {
		p.taskPanelLogger().Warn("面板遍历后台任务失败", "error", err.Error())
		return empty
	}
	return views
}

// AgentGoals 返回当前 active 的会话目标（新在前），实现 adminpanel.GoalSource。
// 目标功能未启用（goalStore 为 nil）或查询失败时返回空切片。
func (p *AIChatPlugin) AgentGoals() []plugininfo.AgentGoalView {
	empty := []plugininfo.AgentGoalView{}
	if p.goalStore == nil {
		return empty
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	goals, err := p.goalStore.ListActive(ctx)
	if err != nil {
		p.taskPanelLogger().Warn("面板读取会话目标失败", "error", err.Error())
		return empty
	}
	views := make([]plugininfo.AgentGoalView, 0, len(goals))
	for _, g := range goals {
		if g == nil {
			continue
		}
		views = append(views, plugininfo.AgentGoalView{
			SessionKey:     g.SessionKey,
			Objective:      g.Objective,
			Phase:          g.Phase,
			RoundsStarted:  g.RoundsStarted,
			MaxRounds:      g.MaxRounds,
			BlockedCode:    g.BlockedCode,
			BlockedMessage: g.BlockedMessage,
			UpdatedAt:      g.UpdatedAt,
		})
	}
	return views
}

// AgentTasksExport 返回满足筛选条件的全部后台任务（不分页），实现
// adminpanel.TaskAdminSource，供面板「后台任务」页导出日志。
func (p *AIChatPlugin) AgentTasksExport(f plugininfo.AgentTaskFilter) []plugininfo.AgentTaskView {
	empty := []plugininfo.AgentTaskView{}
	if p.taskStore == nil || p.historyDB == nil {
		return empty
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	tasks, err := p.taskStore.ListFiltered(ctx, taskFilterFromView(f), 0)
	if err != nil {
		p.taskPanelLogger().Warn("面板导出后台任务失败", "error", err.Error())
		return empty
	}
	views := make([]plugininfo.AgentTaskView, 0, len(tasks))
	for _, t := range tasks {
		views = append(views, agentTaskView(t))
	}
	return views
}

// AgentTasksClear 清除满足筛选条件的已结束任务，返回删除条数，实现
// adminpanel.TaskAdminSource。运行中/排队中的任务保留（见 taskStore.DeleteFinishedFiltered）。
func (p *AIChatPlugin) AgentTasksClear(f plugininfo.AgentTaskFilter) int {
	if p.taskStore == nil || p.historyDB == nil {
		return 0
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	n, err := p.taskStore.DeleteFinishedFiltered(ctx, taskFilterFromView(f))
	if err != nil {
		p.taskPanelLogger().Warn("面板清除后台任务失败", "error", err.Error())
		return 0
	}
	return int(n)
}

// taskFilterFromView 把面板筛选条件转换为任务表筛选条件。
func taskFilterFromView(f plugininfo.AgentTaskFilter) taskFilter {
	return taskFilter{
		Status:  strings.TrimSpace(f.Status),
		Kind:    strings.TrimSpace(f.Kind),
		Keyword: strings.TrimSpace(f.Keyword),
	}
}

// agentTaskView 把任务记录转换为面板视图（字段一一对应）。
func agentTaskView(t *agentTask) plugininfo.AgentTaskView {
	return plugininfo.AgentTaskView{
		ID:         t.ID,
		SessionKey: t.SessionKey,
		ParentID:   t.ParentID,
		Kind:       t.Kind,
		Status:     t.Status,
		Title:      t.Title,
		Prompt:     t.Prompt,
		Result:     t.Result,
		Error:      t.Error,
		StopReason: t.StopReason,
		Meta:       t.Meta,
		Tokens:     t.Tokens,
		CreatedAt:  t.CreatedAt,
		StartedAt:  t.StartedAt,
		UpdatedAt:  t.UpdatedAt,
		FinishedAt: t.FinishedAt,
	}
}

// taskPanelLogger 返回插件日志器；未注入时退回默认日志（部分测试直接构造插件）。
func (p *AIChatPlugin) taskPanelLogger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// 编译期断言见 adminpanel 侧：插件只依赖 common/*，不 import bot/adminpanel；
// AgentTasks / AgentTasksExport / AgentTasksClear / AgentGoals 由 core 在启动时
// 按接口探测注入面板。
