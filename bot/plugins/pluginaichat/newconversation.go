package pluginaichat

import (
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 开新对话（#新对话）时的会话级清理语义。
//
// 历史上下文在批次开始时清空（见 processChatBatch），但后台工作不跟着消息队列
// 走：异步子代理 / 工作流在回合结束后仍在跑，Agent 团队运行则是当前回合里的同步
// 工具调用。用户发送 #新对话 表示「从这一刻起开新话题」，旧任务若继续跑，完成
// 通知会在之后回灌，把旧上下文重新带进新对话（还可能继续消耗 token）。因此：
//   - 消息到达时立即取消该会话运行中的异步子代理 / 工作流与团队运行（本文件）；
//   - 批次开始时再兜底取消一次，覆盖排队与竞态场景；
//   - 随后照常清空历史、动态 MCP 工具、落盘工具结果并换新会话 token。
//
// 长任务目标同样属于「旧任务」：它由运行时在回合结束后自动续跑，若不停下来，
// 新对话会被旧目标立刻顶回来。这里只暂停（可用 /goal resume 恢复），不删除。
//
// 取消的子代理不会回灌结果（onSubagentComplete 对 context.Canceled 直接返回），
// 任务表里由收尾写入标记为 cancelled，面板仍可查到这次被停止的后台工作。

// stopSessionBackground 停止会话的全部后台工作：取消运行中的异步子代理/工作流与
// Agent 团队运行，并暂停长任务目标的自动续跑。返回取消的异步子代理数量与取消的
// 团队运行数量。
func (p *AIChatPlugin) stopSessionBackground(id message.QID, isGroup bool) (subagents, teams int) {
	subagents = len(p.listRunningSubagents(id, isGroup))
	if subagents > 0 {
		p.cancelAsyncSubagents(id, isGroup)
	}
	teams = p.cancelTeamRuns(id, isGroup)
	// 目标续跑是回合结束后的自动行为：不暂停的话，新对话的清空会被旧目标立刻续跑
	// 顶回来。暂停可恢复（/goal resume），不删除目标本身。
	p.pauseGoalOnStop(id, isGroup)
	if (subagents > 0 || teams > 0) && p.Logger != nil {
		p.Logger.Info("开新对话：已停止会话后台工作", "id", id, "is_group", isGroup,
			"subagents", subagents, "team_runs", teams)
	}
	return subagents, teams
}

// noteNewConversationRequest 在消息入队前处理 #新对话 的即时副作用：一到达就停止
// 旧的后台工作。历史清空仍留到批次开始（在途请求带着旧历史，中途清会让本批后续
// 请求上下文不一致，见 interject.go）。返回是否识别为开新对话请求。
func (p *AIChatPlugin) noteNewConversationRequest(id message.QID, isGroup bool, msg message.Message) bool {
	if !newConversationRequested([]message.Message{msg}) {
		return false
	}
	p.stopSessionBackground(id, isGroup)
	return true
}
