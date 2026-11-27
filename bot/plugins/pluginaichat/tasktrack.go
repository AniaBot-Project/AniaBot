package pluginaichat

import (
	"context"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 任务表写入是旁路观测：DB 卡住不能影响回合执行，故每次写操作都用独立
// 短超时上下文。
const taskDBTimeout = 3 * time.Second

// taskDB 返回任务表写操作的短超时上下文。
func (p *AIChatPlugin) taskDB() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), taskDBTimeout)
}

// beginSubagentTask 为一次异步子代理创建任务记录（taskStore 未启用时返回 nil）。
// kind 为 taskKindSubagent / taskKindHandoff。
func (p *AIChatPlugin) beginSubagentTask(taskID string, id message.QID, isGroup bool, task, kind string) *agentTask {
	if p.taskStore == nil {
		return nil
	}
	if kind == "" {
		kind = taskKindSubagent
	}
	now := time.Now().Unix()
	t := &agentTask{
		ID:         taskID,
		SessionKey: sessionKey(id, isGroup),
		Kind:       kind,
		Status:     taskStatusRunning,
		Title:      truncateTaskTitle(task),
		Prompt:     task,
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

// finishSubagentTask 更新子代理任务的终态、结果与 token 用量（不写执行细节 Meta）。
func (p *AIChatPlugin) finishSubagentTask(t *agentTask, result string, usage aichat.TokenUsage, runErr error) {
	p.finishSubagentTaskMeta(t, result, usage, runErr, "")
}

// finishSubagentTaskMeta 更新子代理任务的终态、结果、token 用量与执行细节 Meta
// （轮数/耗时/工具调用步骤，由 subagentTraceMeta 构造；空串表示不写）。
// 结果与错误完整入库（与 Query 日志一致，不在写路径截断）。
func (p *AIChatPlugin) finishSubagentTaskMeta(t *agentTask, result string, usage aichat.TokenUsage, runErr error, meta string) {
	if p.taskStore == nil || t == nil {
		return
	}
	now := time.Now().Unix()
	t.Tokens = int64(usage.TotalTokens)
	t.Status, t.StopReason = taskRunStatus(runErr)
	switch {
	case runErr == nil:
		t.Result = result
	case t.Status == taskStatusTimeout:
		t.Error = runErr.Error()
	case t.Status == taskStatusFailed:
		t.Error = runErr.Error()
	}
	if meta != "" {
		t.Meta = meta
	}
	t.UpdatedAt = now
	t.FinishedAt = now
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Update(ctx, t); err != nil {
		p.Logger.Warn("更新子代理任务失败", "task", t.ID, "error", err.Error())
	}
}

// taskRunStatus 把子代理/团队成员/团队运行的执行错误映射为任务状态与停止原因：
// 超时（子代理封装成「执行超时」文案）、上下文取消、普通失败分别落到
// timeout / cancelled / failed 三个终态。
func taskRunStatus(runErr error) (status, stopReason string) {
	switch {
	case runErr == nil:
		return taskStatusCompleted, taskStopCompleted
	case strings.Contains(runErr.Error(), "超时"):
		return taskStatusTimeout, taskStopTimeout
	case isContextCanceled(runErr):
		return taskStatusCancelled, taskStopAborted
	default:
		return taskStatusFailed, taskStopError
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

// truncateTaskTitle 任务标题/提示的 rune 安全截断（标题是列表摘要，完整内容在
// Prompt / Result / Meta 里，均不截断）。
func truncateTaskTitle(s string) string {
	s = strings.TrimSpace(s)
	const maxRunes = 200
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// startTaskJanitor 启动任务记录清理器：按 task.retention_days 删除过期已结束
// 任务（每 6 小时一次，随插件生命周期退出）。
func (p *AIChatPlugin) startTaskJanitor() {
	if p.taskStore == nil || p.cfg.Task.RetentionDays <= 0 || p.runCtx == nil {
		return
	}
	retention := time.Duration(p.cfg.Task.RetentionDays) * 24 * time.Hour
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-p.runCtx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-retention).Unix()
				ctx, cancel := p.taskDB()
				n, err := p.taskStore.DeleteFinishedBefore(ctx, cutoff)
				cancel()
				if err != nil {
					p.Logger.Warn("清理过期任务记录失败", "error", err.Error())
				} else if n > 0 {
					p.Logger.Info("已清理过期任务记录", "count", n)
				}
			}
		}
	}()
}

// handleResumeCommand 处理 /resume <任务ID>：把进程中断的任务恢复为一个新的
// AI 回合（任务目标作为续接提示入队）。仅允许恢复本会话、且状态为 interrupted
// 的子代理/交接任务；恢复过程保留原任务记录，由新回合继续推进。
func (p *AIChatPlugin) handleResumeCommand(b bot.Bot, msg message.Message, taskID string) (bool, error) {
	id, isGroup := messageTarget(msg)
	if taskID == "" {
		p.sendPlainText(b, id, isGroup, "用法：/resume 任务ID（发送 /tasks 查看可恢复任务）")
		return false, nil
	}
	if p.taskStore == nil {
		p.sendPlainText(b, id, isGroup, "任务持久化未启用（缺少 SQL 存储），无法恢复中断任务")
		return false, nil
	}
	ctx, cancel := p.taskDB()
	t, ok, err := p.taskStore.Get(ctx, taskID)
	cancel()
	if err != nil || !ok {
		p.sendPlainText(b, id, isGroup, "未找到任务 "+taskID+"（发送 /tasks 查看当前任务）")
		return false, nil
	}
	if t.SessionKey != sessionKey(id, isGroup) {
		p.sendPlainText(b, id, isGroup, "只能恢复本会话的任务")
		return false, nil
	}
	if t.Status != taskStatusInterrupted {
		p.sendPlainText(b, id, isGroup, "任务 "+taskID+" 当前状态为「"+taskStatusLabel(t.Status)+"」，只有被中断（interrupted）的任务可以恢复")
		return false, nil
	}
	if t.Kind != taskKindSubagent && t.Kind != taskKindHandoff {
		p.sendPlainText(b, id, isGroup, "任务 "+taskID+" 不支持恢复（仅子代理/交接任务可恢复）")
		return false, nil
	}
	goal := strings.TrimSpace(t.Prompt)
	if goal == "" {
		goal = t.Title
	}
	// 标记为排队，避免重复 /resume
	t.Status = taskStatusQueued
	t.UpdatedAt = time.Now().Unix()
	updateCtx, updateCancel := p.taskDB()
	_ = p.taskStore.Update(updateCtx, t)
	updateCancel()

	// 作为一条系统来源的消息入队：主 AI 有完整会话历史，比冷启动子代理更能接上上下文
	hint := "【恢复中断任务】任务 ID：" + t.ID + "（原任务因进程重启被中断）。请先核对已完成的工作与现状，再继续完成以下目标，完成后向用户汇报结果：\n" + goal
	synthetic := message.Message{
		Sender:  message.MessageSender{Nickname: "任务恢复", UserId: message.FromUint64(0)},
		Message: []message.OB11Segment{{Type: message.SegmentText, Data: map[string]any{"text": hint}}},
	}
	p.sessionBots.Store(sessionKey(id, isGroup), b)
	if _, ok := p.enqueuePending(id, isGroup, synthetic); !ok {
		p.sendPlainText(b, id, isGroup, "排队消息已满，暂时无法恢复任务，请稍后再试")
		return false, nil
	}
	b.Go("runner:"+sessionKey(id, isGroup), func() { p.runQueued(b, id, isGroup) })
	p.sendPlainText(b, id, isGroup, "已恢复任务 "+taskID+"，继续处理中")
	return false, nil
}

// turnTaskTitle 从一批消息生成回合标题：取最后一条消息的文本段（rune 安全），
// 供 /tasks 的「最近回合」摘要展示。
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
