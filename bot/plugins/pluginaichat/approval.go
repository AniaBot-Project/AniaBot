package pluginaichat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 工具审批：危险工具（默认 file，bash 另有命令级审批）执行前向会话
// 发送确认消息，由请求发送者或机器人管理员回复「允许/拒绝」授权；超时自动拒绝。
// 配置修改类工具（adminApprovalTools）为管理员审批：requester 强制为 0，
// 仅管理员可批，且与 approval.enable 开关无关——启用配置管理工具即生效；
// 审批提示优先直接私聊发给管理员（requestAdminOnly），管理员在私聊中回复。

// approvalVerdict 审批结论（含操作者，供审计）
type approvalVerdict struct {
	allow bool
	by    message.QID
}

// approvalRequest 一次待批请求（每会话同时只有一个）
type approvalRequest struct {
	tool      string
	summary   string
	requester message.QID // message.FromUint64(0) = 仅管理员可批（子代理/定时任务路径）
	resultCh  chan approvalVerdict
}

// approvalManager 工具审批管理器。并发安全。
type approvalManager struct {
	tools   map[string]struct{} // 需审批的工具名（工具级门）
	timeout time.Duration
	admin   message.QID
	logger  *slog.Logger

	mu           sync.Mutex
	pending      map[string]*approvalRequest // sessionKey → 当前待批
	adminPending map[string]*approvalRequest // 管理员私聊会话键 → 正在等待管理员确认的请求（requestAdminOnly 登记）
	locks        sync.Map                    // sessionKey → *sync.Mutex：同会话多个审批串行（并行工具调用逐个提示，不并发刷屏）
}

func newApprovalManager(tools []string, timeoutSec int, admin message.QID, logger *slog.Logger) *approvalManager {
	set := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		if t = strings.TrimSpace(t); t != "" {
			set[t] = struct{}{}
		}
	}
	// 限幅：过短会来不及回复，过长会吃光消息处理预算（bot.msg_event_timeout_sec）
	timeoutSec = min(max(timeoutSec, 10), 240)
	return &approvalManager{
		tools:        set,
		timeout:      time.Duration(timeoutSec) * time.Second,
		admin:        admin,
		logger:       logger,
		pending:      make(map[string]*approvalRequest),
		adminPending: make(map[string]*approvalRequest),
	}
}

func (m *approvalManager) needsApproval(tool string) bool {
	_, ok := m.tools[tool]
	return ok
}

// adminApprovalTools 配置修改类工具：始终需要管理员审批（仅管理员可批，
// 请求者不能自己批准），与 approval.enable 开关无关。门禁中本集合优先于
// approval.tools（命中后不再走「请求者或管理员」的普通审批腿）。
var adminApprovalTools = map[string]struct{}{
	"config_set":      {},
	"config_file_set": {},
}

func (m *approvalManager) needsAdminApproval(tool string) bool {
	_, ok := adminApprovalTools[tool]
	return ok
}

// approvalWaitBudget 审批实际等待时长：取配置超时与「父上下文剩余预算减去
// approvalFinishReserve」中的较小值。审批等待发生在请求处理链路内（同轮多个
// 工具审批还会串行），等待时长必须给请求收尾留出空间：吃光预算会让整单请求以
// 「请求超时」中止，用户既收不到回复也看不到审批结论。
func (m *approvalManager) approvalWaitBudget(ctx context.Context) time.Duration {
	wait := m.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if budget := time.Until(deadline) - approvalFinishReserve; budget < wait {
			wait = budget
		}
	}
	return wait
}

// approvalFinishReserve 审批等待为请求收尾预留的时间：超时被拒后模型仍需一轮
// LLM 调用把结论告诉用户。
const approvalFinishReserve = 20 * time.Second

// waitSeconds 等待时长的展示秒数：四舍五入而非截断——预算由上下文截止时间反推
// （60s 预算实为 39.99…s），截断会把提示里的「40 秒」显示成「39 秒」。
func waitSeconds(wait time.Duration) int {
	return int(wait.Round(time.Second).Seconds())
}

// request 发起一次审批：发送提示消息后阻塞等待回复/取消/超时。
// 返回 (allowed, reason)：reason 在拒绝时说明原因（拒绝/超时/取消）。
// 会话级互斥锁保证同会话并行工具调用的多个审批逐个提示。
// 注意：本方法可能阻塞至超时（默认 120s，且按父上下文剩余预算收缩），调用方不得持锁调用。
func (m *approvalManager) request(ctx context.Context, sKey, tool, summary string, requester message.QID, sendPrompt func(text string)) (bool, string) {
	lockAny, _ := m.locks.LoadOrStore(sKey, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	wait := m.approvalWaitBudget(ctx)
	if wait <= 0 {
		return false, "当前请求剩余时间不足，未发起审批（请稍后重试）"
	}

	req := &approvalRequest{tool: tool, summary: summary, requester: requester, resultCh: make(chan approvalVerdict, 1)}
	m.mu.Lock()
	m.pending[sKey] = req
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, sKey)
		m.mu.Unlock()
	}()

	sendPrompt(m.formatPrompt(tool, summary, requester, wait))
	m.logger.Info("发起工具审批", "session", sKey, "tool", tool, "requester", requester, "wait_sec", waitSeconds(wait))

	// 定时器显式 Stop：回复/取消分支胜出后不再驻留 (wait 最长 240s)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case v := <-req.resultCh:
		if v.allow {
			m.audit(sKey, tool, summary, "允许", v.by)
			return true, ""
		}
		m.audit(sKey, tool, summary, "拒绝", v.by)
		return false, "用户拒绝了本次操作"
	case <-ctx.Done():
		// /stop 取消请求（同一 chatCtx）：不记审计（未形成决定）
		return false, "审批等待已取消（请求被停止）"
	case <-timer.C:
		m.audit(sKey, tool, summary, "超时自动拒绝", message.FromUint64(0))
		return false, fmt.Sprintf("审批超时（%d 秒无回复），已自动拒绝", waitSeconds(wait))
	}
}

// tryHandleReply 尝试把一条新消息当作审批回复处理。返回 (consumed, hint)：
// consumed 表示消息已按审批回复处理（调用方应停止后续插件传播与正常聊天流程）；
// hint 非空表示消息是审批回复但发送者无权批准，hint 为应回给发送者的提示文本。
// 权限：仅请求发送者或机器人管理员可批；requester 为 0（管理员审批路径）时
// 仅管理员。管理员私聊中的回复按 adminPending 索引匹配正在等待管理员确认的
// 请求（管理员审批提示默认发到其私聊）。非审批内容的消息返回 (false, "")，
// 走正常流程。
func (m *approvalManager) tryHandleReply(id message.QID, isGroup bool, sender message.QID, text string) (bool, string) {
	sKey := sessionKey(id, isGroup)
	m.mu.Lock()
	req, ok := m.pending[sKey]
	if !ok && sender == m.admin && sKey == adminSessionKey(m.admin) {
		// 管理员在其私聊中回复：按索引查找等待管理员确认的请求
		req, ok = m.adminPending[adminSessionKey(m.admin)]
	}
	m.mu.Unlock()
	if !ok {
		return false, "" // 快速路径：无待批请求，正常消息零干扰
	}
	allow, isReply := parseApprovalReply(text)
	if !isReply {
		return false, ""
	}
	if sender != req.requester && sender != m.admin {
		// 无权发送者的审批回复：消费掉并提示，避免落入正常聊天流程让 AI 误答
		if req.requester == message.FromUint64(0) {
			return true, "该操作需要管理员审批，只有管理员可以回复「允许/拒绝」"
		}
		return true, "你没有权限批准本次操作（需请求发送者或管理员）"
	}
	// resultCh 带缓冲且非阻塞发送：等待方可能已从超时/取消分支返回（pending 尚未
	// 清除的窗口内），此时若缓冲区已被前一条回复占满，阻塞发送会永久挂住消息
	// 分发协程（该用户后续消息都不再被处理）。
	select {
	case req.resultCh <- approvalVerdict{allow: allow, by: sender}:
	default:
		// 已有结论未取走或等待方已退出：本次回复丢弃（不阻断消息流程）
		if m.logger != nil {
			m.logger.Warn("审批回复未送达（等待方已结束或已有结论）", "session", sKey, "tool", req.tool)
		}
	}
	m.logger.Info("审批回复", "session", sKey, "tool", req.tool, "allow", allow, "by", sender)
	return true, ""
}

// adminSessionKey 管理员私聊会话键；管理员 ID 未设置时为空。
func adminSessionKey(admin message.QID) string {
	if admin == message.FromUint64(0) {
		return ""
	}
	return sessionKey(admin, false)
}

// requestAdminOnly 发起配置修改类工具的审批（仅管理员可批）：提示优先直接
// 私聊发给管理员，管理员在私聊中回复「允许/拒绝」（管理员在发起会话中的回复
// 同样有效，因待批请求同时登记在发起会话键下）；管理员 ID 未设置、发起会话
// 即管理员私聊、或私聊发送失败时，提示改发到发起会话。
// sendAdminPrompt 把提示发给管理员私聊并返回是否成功（nil 视为无管理员通道）；
// sendOriginPrompt 在发起会话发送提示/通知。originSKey 用于审计与发起会话的
// 回复识别。
func (m *approvalManager) requestAdminOnly(ctx context.Context, originSKey, tool, summary string, sendAdminPrompt func(text string) bool, sendOriginPrompt func(text string)) (bool, string) {
	adminSKey := adminSessionKey(m.admin)
	if sendAdminPrompt == nil || adminSKey == "" || adminSKey == originSKey {
		return m.request(ctx, originSKey, tool, summary, message.FromUint64(0), sendOriginPrompt)
	}

	// 锁顺序固定为 管理员私聊会话 → 发起会话：多个会话并发发起的管理员审批
	// 按序逐个提示；与发起会话锁同取可避免同会话并行工具调用覆盖待批登记
	lockAny, _ := m.locks.LoadOrStore(adminSKey, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	originLockAny, _ := m.locks.LoadOrStore(originSKey, &sync.Mutex{})
	originLock := originLockAny.(*sync.Mutex)
	originLock.Lock()
	defer originLock.Unlock()

	// 等待时长同样按父上下文剩余预算收缩（等锁期间预算可能已被前一个审批消耗）
	wait := m.approvalWaitBudget(ctx)
	if wait <= 0 {
		return false, "当前请求剩余时间不足，未发起审批（请稍后重试）"
	}

	// 待批请求登记在发起会话键（管理员在发起会话回复也可批），
	// 另在管理员私聊键登记索引（管理员私聊回复时查找）
	req := &approvalRequest{tool: tool, summary: summary, requester: message.FromUint64(0), resultCh: make(chan approvalVerdict, 1)}
	m.mu.Lock()
	m.pending[originSKey] = req
	m.adminPending[adminSKey] = req
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if cur, ok := m.adminPending[adminSKey]; ok && cur == req {
			delete(m.adminPending, adminSKey)
		}
		if cur, ok := m.pending[originSKey]; ok && cur == req {
			delete(m.pending, originSKey)
		}
		m.mu.Unlock()
	}()

	prompt := m.formatPrompt(tool, summary, message.FromUint64(0), wait)
	if !sendAdminPrompt(prompt) {
		// 管理员私聊发送失败（如未加好友）：改在发起会话提示，等待管理员在该会话回复
		m.mu.Lock()
		if cur, ok := m.adminPending[adminSKey]; ok && cur == req {
			delete(m.adminPending, adminSKey)
		}
		m.mu.Unlock()
		sendOriginPrompt(prompt)
	} else {
		sendOriginPrompt(fmt.Sprintf("「%s」需要管理员审批，已通知管理员；仅管理员可批准，操作会在管理员确认后执行（超时自动拒绝）", tool))
	}
	m.logger.Info("发起管理员审批", "session", originSKey, "tool", tool)

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case v := <-req.resultCh:
		if v.allow {
			m.audit(originSKey, tool, summary, "允许", v.by)
			return true, ""
		}
		m.audit(originSKey, tool, summary, "拒绝", v.by)
		return false, "管理员拒绝了本次操作"
	case <-ctx.Done():
		// /stop 取消请求（同一 chatCtx）：不记审计（未形成决定）
		return false, "审批等待已取消（请求被停止）"
	case <-timer.C:
		m.audit(originSKey, tool, summary, "超时自动拒绝", message.FromUint64(0))
		return false, fmt.Sprintf("审批超时（%d 秒无回复），已自动拒绝", waitSeconds(wait))
	}
}

// formatPrompt 审批提示消息文本（纯文本路径发送；群聊中 requester 非 0 时
// 文本注明其 ID——发送闭包是 @ 无关的纯发送，避免与流式消息/丢弃桩冲突）。
// wait 为本次实际等待时长（已按剩余预算收缩），提示文案与实际一致。
func (m *approvalManager) formatPrompt(tool, summary string, requester message.QID, wait time.Duration) string {
	var sb strings.Builder
	sb.WriteString("【工具审批】AI 请求执行工具：" + tool)
	if summary != "" {
		sb.WriteString("\n参数摘要：" + summary)
	}
	if requester == message.FromUint64(0) {
		fmt.Fprintf(&sb, "\n请管理员在 %d 秒内回复「允许」或「拒绝」（超时自动拒绝）", waitSeconds(wait))
	} else {
		fmt.Fprintf(&sb, "\n请消息发送者（%s）或管理员在 %d 秒内回复「允许」或「拒绝」（超时自动拒绝）",
			requester.String(), waitSeconds(wait))
	}
	return sb.String()
}

func (m *approvalManager) audit(sKey, tool, summary, verdict string, by message.QID) {
	detail := fmt.Sprintf("工具 %s %s（会话 %s）", tool, verdict, sKey)
	if by != message.FromUint64(0) {
		detail += "，操作者 " + by.String()
	}
	oplog.Record(oplog.CategoryAI, "tool_approval", detail)
}

// parseApprovalReply 解析审批回复：ok=false 表示不是审批回复。
func parseApprovalReply(text string) (allow, ok bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "允许", "同意", "批准", "allow", "approve", "yes", "y":
		return true, true
	case "拒绝", "deny", "reject", "no", "n":
		return false, true
	}
	return false, false
}
