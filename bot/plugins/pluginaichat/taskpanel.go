package pluginaichat

import (
	"log/slog"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// 面板「后台任务」页数据源（实现 adminpanel.TaskSource / adminpanel.GoalSource）。
//
// 后台任务注册表（ania_agent_task）与长任务目标表（ania_agent_goal）都属于插件
// 的 SQL 持久层；面板是旁路只读视图，直接用一条全局查询取最近 N 条即可，不引入
// 新的写路径。表结构见 taskstore.go / goalstore.go，本文件不改动它们。
//
// 降级约定：SQL 后端不可用（taskStore/goalStore 为 nil）或查询失败时返回空切片，
// 面板按空列表展示，不影响其它页面与插件运行。

// taskPanelDefaultLimit AgentTasks 未收到合法 limit 时使用的条数（与 adminpanel 默认一致）。
const taskPanelDefaultLimit = 100

// taskPanelColumns 面板查询列顺序，必须与 scanAgentTaskRow 的扫描顺序一致
// （与 taskstore.go 的 taskColumns 相同，面板不返回 prompt）。
const taskPanelColumns = `id, session_key, kind, status, title, result, ` +
	`error, stop_reason, tokens, meta, created_at, started_at, updated_at, finished_at`

// AgentTasks 返回最近的后台任务（updated_at 倒序），实现 adminpanel.TaskSource。
// 后台任务表没有「全局最近 N 条」的 store 方法，这里直接走一条只读 SQL：
// 两侧数据库方言通用，只用到 ? 占位符与 LIMIT，无需方言分支。
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
		`SELECT `+taskPanelColumns+` FROM ania_agent_task ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		p.taskPanelLogger().Warn("面板读取后台任务失败", "error", err.Error())
		return empty
	}
	defer rows.Close()
	views := make([]plugininfo.AgentTaskView, 0, limit)
	for rows.Next() {
		var v plugininfo.AgentTaskView
		if err := rows.Scan(&v.ID, &v.SessionKey, &v.Kind, &v.Status, &v.Title,
			&v.Result, &v.Error, &v.StopReason, &v.Tokens, &v.Meta,
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

// taskPanelLogger 返回插件日志器；未注入时退回默认日志（部分测试直接构造插件）。
func (p *AIChatPlugin) taskPanelLogger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// 编译期断言：插件实现了面板后台任务页所需的两个可选接口。
