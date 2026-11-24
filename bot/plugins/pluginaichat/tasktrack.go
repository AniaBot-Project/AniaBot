package pluginaichat

import (
	"context"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 任务表写入是旁路观测：DB 卡住不能影响回合执行，故每次写操作都用独立
// 短超时上下文。
const taskDBTimeout = 3 * time.Second

// taskDB 返回任务表写操作的短超时上下文。
func (p *AIChatPlugin) taskDB() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), taskDBTimeout)
}

// beginTurnTask 记录一次主回合任务（taskStore 未启用时返回 nil，静默降级）。
func (p *AIChatPlugin) beginTurnTask(id message.QID, isGroup bool, title string) *agentTask {
	if p.taskStore == nil {
		return nil
	}
	now := time.Now().Unix()
	t := &agentTask{
		ID:         newTaskID(),
		SessionKey: sessionKey(id, isGroup),
		Kind:       taskKindTurn,
		Status:     taskStatusRunning,
		Title:      truncateTaskTitle(title),
		CreatedAt:  now,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Create(ctx, t); err != nil {
		p.Logger.Warn("记录主回合任务失败", "error", err.Error())
		return nil
	}
	return t
}

// finishTurnTask 更新主回合任务的终态（task 为 nil 时静默忽略）。
func (p *AIChatPlugin) finishTurnTask(t *agentTask, status, stopReason, errText string) {
	if p.taskStore == nil || t == nil {
		return
	}
	now := time.Now().Unix()
	t.Status = status
	t.StopReason = stopReason
	t.Error = errText
	t.FinishedAt = now
	t.UpdatedAt = now
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Update(ctx, t); err != nil {
		p.Logger.Warn("更新主回合任务失败", "task", t.ID, "error", err.Error())
	}
}

// beginSubagentTask 为一次异步子代理创建任务记录（taskStore 未启用时返回 nil）。
func (p *AIChatPlugin) beginSubagentTask(taskID string, id message.QID, isGroup bool, task string) *agentTask {
	if p.taskStore == nil {
		return nil
	}
	now := time.Now().Unix()
	t := &agentTask{
		ID:         taskID,
		SessionKey: sessionKey(id, isGroup),
		Kind:       taskKindSubagent,
		Status:     taskStatusRunning,
		Title:      truncateTaskTitle(task),
		Prompt:     truncateTaskTitle(task),
		CreatedAt:  now,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Create(ctx, t); err != nil {
		p.Logger.Warn("记录子代理任务失败", "error", err.Error())
		return nil
	}
	return t
}

// finishSubagentTask 更新子代理任务的终态、结果与 token 用量。
func (p *AIChatPlugin) finishSubagentTask(t *agentTask, result string, usage aichat.TokenUsage, runErr error) {
	if p.taskStore == nil || t == nil {
		return
	}
	now := time.Now().Unix()
	t.Tokens = int64(usage.TotalTokens)
	t.UpdatedAt = now
	t.FinishedAt = now
	switch {
	case runErr == nil:
		t.Status = taskStatusCompleted
		t.StopReason = taskStopCompleted
		t.Result = truncateTaskResult(result)
	case strings.Contains(runErr.Error(), "超时"):
		t.Status = taskStatusTimeout
		t.StopReason = taskStopTimeout
		t.Error = runErr.Error()
	case isContextCanceled(runErr):
		t.Status = taskStatusCancelled
		t.StopReason = taskStopAborted
	default:
		t.Status = taskStatusFailed
		t.StopReason = taskStopError
		t.Error = truncateTaskTitle(runErr.Error())
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Update(ctx, t); err != nil {
		p.Logger.Warn("更新子代理任务失败", "task", t.ID, "error", err.Error())
	}
}

// isContextCanceled 判断错误是否来自 context 取消（含包装后的取消原因）。
func isContextCanceled(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "context deadline exceeded")
}

// truncateTaskTitle 任务标题/提示的 rune 安全截断。
func truncateTaskTitle(s string) string {
	s = strings.TrimSpace(s)
	const maxRunes = 200
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// truncateTaskResult 任务结果入库前的长度限制：任务表只保留可复核摘要，
// 完整结果已由工具结果落盘治理保存（read_tool_output 可读）。
func truncateTaskResult(s string) string {
	const maxRunes = 4000
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "\n…（结果已截断，完整内容见会话侧落盘文件）"
}

// turnTaskTitle 从一批消息生成任务标题：取最后一条消息的文本段（rune 安全）。
func turnTaskTitle(batch []message.Message) string {
	for i := len(batch) - 1; i >= 0; i-- {
		var sb strings.Builder
		for _, seg := range batch[i].Message {
			if seg.Type != message.SegmentText {
				continue
			}
			var txt message.TextMessage
			if !message.ParseText(seg, &txt) {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(txt.Text)
			if sb.Len() > 512 {
				break
			}
		}
		if title := strings.TrimSpace(sb.String()); title != "" {
			return title
		}
	}
	return "AI 回合"
}
