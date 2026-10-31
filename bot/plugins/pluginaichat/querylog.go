package pluginaichat

import (
	"context"
	"errors"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// Query 日志：记录每次 AI 回复（群 @ / 私聊触发）从收到消息到最终响应的完整过程，
// 持久化到 PersistentStorage（querylog: 命名空间），供 Web 面板「Query 日志」页展示。
// 日志默认启用、全量保留（不淘汰、不截断），由管理员在面板导出或清除。

// initQueryLogger 初始化 Query 日志记录器。
func (p *AIChatPlugin) initQueryLogger() {
	p.queryLogger = querylog.New(p.PersistentStorage, p.Logger.WithGroup("querylog"))
	// 重启前未正常收尾的执行中记录（如等待工具审批时进程退出）统一标记为中断，
	// 避免面板一直显示「执行中」；内存中的审批/会话状态已随进程消失，无法恢复
	if n := p.queryLogger.MarkRunningInterrupted(); n > 0 {
		p.Logger.Info("已将重启前遗留的执行中 Query 日志标记为中断", "count", n)
	}
}

// QueryLogRecent 按条件查询 Query 日志（新在前），实现 adminpanel.QueryLogSource。
func (p *AIChatPlugin) QueryLogRecent(f querylog.Filter) []querylog.Entry {
	if p.queryLogger == nil {
		return nil
	}
	return p.queryLogger.Query(f)
}

// QueryLogStats 返回 Query 日志的轻量统计行（全量留存记录），
// 实现 adminpanel.QueryLogSource，供 Token 统计聚合。
func (p *AIChatPlugin) QueryLogStats(f querylog.Filter) []querylog.StatRow {
	if p.queryLogger == nil {
		return nil
	}
	return p.queryLogger.Stats(f)
}

// QueryLogClear 清除满足条件的 Query 日志（零值条件清除全部），实现 adminpanel.QueryLogSource。
func (p *AIChatPlugin) QueryLogClear(f querylog.Filter) int {
	if p.queryLogger == nil {
		return 0
	}
	return p.queryLogger.Clear(f)
}

// queryRecorder 一次 Query 的日志记录上下文。processChatBatch 在会话锁内串行执行，
// 且工具观测回调与 Chat 同 goroutine，因此 toolCalls 无需额外加锁。
type queryRecorder struct {
	entry          querylog.Entry
	toolCalls      []querylog.ToolCallRecord
	toolCallsTotal int // 工具调用总数
	start          time.Time
	key            string // 会话 key（takeExtraUsage 用）
}

// beginQuery 开始记录一次 Query：写入 running 状态的日志并挂载工具调用观察者。
// 观察者把每次工具调用增量落盘（明细 + 已耗时），面板执行中即可见进度。
// 日志记录器不可用（如持久化不支持 SQL）时返回 nil，调用方直接跳过。
func (p *AIChatPlugin) beginQuery(chat *aichat.ChatBot, id message.QID, isGroup bool, batch []message.Message, query string) *queryRecorder {
	if p.queryLogger == nil {
		return nil
	}
	chatType := "friend"
	if isGroup {
		chatType = "group"
	}
	senders := make([]string, 0, len(batch))
	seen := make(map[message.QID]struct{}, len(batch))
	for i := range batch {
		uid := batch[i].Sender.UserId
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		senders = append(senders, uid.String())
	}
	r := &queryRecorder{start: time.Now(), key: sessionKey(id, isGroup)}
	r.entry = p.queryLogger.Record(querylog.Entry{
		ChatType: chatType,
		TargetID: id.String(),
		Senders:  senders,
		Query:    query,
		Status:   querylog.StatusRunning,
	})
	chat.SetToolObserver(func(info aichat.ToolCallInfo) {
		p.onToolCall(r, info)
	})
	return r
}

// onToolCall 工具调用观察者回调：累计明细并即时增量落盘——面板按 running 状态
// 轮询时能看到执行中的工具调用进度与已耗时，无需等 finishQuery 才可见中间流程。
// 观察者由 orchestrator 串行调用（无需额外加锁），Logger.Update 内部有互斥。
func (p *AIChatPlugin) onToolCall(r *queryRecorder, info aichat.ToolCallInfo) {
	r.toolCallsTotal++
	rec := querylog.ToolCallRecord{
		Name:       info.Name,
		Arguments:  info.Arguments, // 模型生成的执行意图，完整记录便于审计
		Result:     info.Result,    // 完整记录，不做截断（前端按需折叠展示）
		DurationMs: info.DurationMs,
	}
	if info.Err != nil {
		rec.Error = info.Err.Error()
	}
	r.toolCalls = append(r.toolCalls, rec)
	p.queryLogger.Update(r.entry.ID, func(e *querylog.Entry) {
		// 复制 slice 再存入，避免与后续 append 共享底层数组
		e.ToolCalls = append([]querylog.ToolCallRecord(nil), r.toolCalls...)
		e.ToolCallsTotal = r.toolCallsTotal
		e.DurationMs = time.Since(r.start).Milliseconds()
	})
}

// finishQuery 结束记录：回填状态、耗时、token 用量、工具调用明细与最终回复/错误。
func (p *AIChatPlugin) finishQuery(r *queryRecorder, chat *aichat.ChatBot, usage aichat.TokenUsage, reply string, err error) {
	if r == nil {
		return
	}
	chat.SetToolObserver(nil)
	status := querylog.StatusSuccess
	errText := ""
	if err != nil {
		errText = err.Error()
		switch {
		case errors.Is(err, context.Canceled):
			status = querylog.StatusStopped
		case errors.Is(err, context.DeadlineExceeded):
			status = querylog.StatusTimeout
		default:
			status = querylog.StatusError
		}
	}
	// 并入主请求循环之外派生的消耗（异步子代理 / team_run 成员 / 备用图片识别，
	// 见 usageacc.go），使统计反映会话的完整成本（配额在各派生调用点单独累加）
	extra := p.takeExtraUsage(r.key)
	p.writeQueryResult(r, usage, extra, status, reply, "", errText)
}

// beginProactiveQuery 开始记录一次主动互动执行。与普通 Query 的区别：Kind 标记
// 为 proactive（面板据此区分筛选与统计），Query 记录触发说明（来源、意图内容、
// 现场近况）而不是用户输入；工具调用沿用同一套逐条落盘逻辑。
func (p *AIChatPlugin) beginProactiveQuery(chat *aichat.ChatBot, id message.QID, isGroup bool, trigger string) *queryRecorder {
	if p.queryLogger == nil {
		return nil
	}
	chatType := "friend"
	if isGroup {
		chatType = "group"
	}
	r := &queryRecorder{start: time.Now(), key: sessionKey(id, isGroup)}
	r.entry = p.queryLogger.Record(querylog.Entry{
		Kind:     querylog.KindProactive,
		ChatType: chatType,
		TargetID: id.String(),
		Query:    trigger,
		Status:   querylog.StatusRunning,
	})
	chat.SetToolObserver(func(info aichat.ToolCallInfo) {
		p.onToolCall(r, info)
	})
	return r
}

// finishProactiveQuery 收尾一次主动互动记录：回填状态、耗时、token 用量、工具
// 明细与结果。status 为空时按错误推断；保持安静（skip_output）时传
// StatusSkipped，note 记原因、reply 为空。主动互动的图片识别等派生消耗已在
// chat 内部并入 usage，因此不再取会话级 extraUsage（那是主对话的）。
func (p *AIChatPlugin) finishProactiveQuery(r *queryRecorder, usage aichat.TokenUsage, status querylog.Status, reply, note string, execErr error) {
	if r == nil {
		return
	}
	errText := ""
	if execErr != nil {
		errText = execErr.Error()
		if status == "" {
			switch {
			case errors.Is(execErr, context.Canceled):
				status = querylog.StatusStopped
			case errors.Is(execErr, context.DeadlineExceeded):
				status = querylog.StatusTimeout
			default:
				status = querylog.StatusError
			}
		}
	} else if status == "" {
		status = querylog.StatusSuccess
	}
	p.writeQueryResult(r, usage, aichat.TokenUsage{}, status, reply, note, errText)
}

// writeQueryResult 把一次 Query 的终态写回记录：状态、耗时、token 用量、
// 工具调用明细与结果说明。普通 Query 与主动互动共用，保证两边字段回填一致。
func (p *AIChatPlugin) writeQueryResult(r *queryRecorder, usage, extra aichat.TokenUsage, status querylog.Status, reply, note, errText string) {
	toolCalls := r.toolCalls
	p.queryLogger.Update(r.entry.ID, func(e *querylog.Entry) {
		e.Status = status
		e.DurationMs = time.Since(r.start).Milliseconds()
		e.Iterations = usage.Iterations + extra.Iterations
		e.PromptTokens = usage.PromptTokens + extra.PromptTokens
		e.CompletionTokens = usage.CompletionTokens + extra.CompletionTokens
		e.TotalTokens = usage.TotalTokens + extra.TotalTokens
		e.CachedTokens = usage.CachedTokens + extra.CachedTokens
		e.ToolCalls = toolCalls
		e.ToolCallsTotal = r.toolCallsTotal
		e.Reply = reply
		e.Note = note
		e.Error = errText
	})
}
