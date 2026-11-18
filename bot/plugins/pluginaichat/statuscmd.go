package pluginaichat

import (
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 会话状态（/status 指令）：展示当前会话的 AI 请求状态、最近处理阶段、
// 排队消息数、运行中的异步子代理等，用于用户自助判断「机器人是不是在忙、
// 我的消息有没有排上队」。命令只读内存态，不获取会话锁，因此在 AI 响应
// 期间也能立即回复。

// 处理阶段文案（/status 展示最近一步在做什么）。
const (
	// stageBusy 锁已占用但尚未登记活跃请求的阶段：建会话/回放历史，或两批之间的
	// 收尾窗口。两种情况下都不宜给出更具体的猜测。
	stageBusy         = "处理中（准备会话或收尾）"
	stageWaitingModel = "等待模型回复"
	stageSendingReply = "发送回复"
)

// sessionStatusSnapshot 某一时刻的会话级 AI 状态快照（与渲染解耦，便于测试）。
type sessionStatusSnapshot struct {
	IsGroup bool
	Target  message.QID

	Active  bool          // 是否有正在处理的请求（会话锁被持有）
	Elapsed time.Duration // 当前请求已持续时长
	Stage   string        // 最近一次处理阶段（如「等待模型回复」）
	StageAt time.Time     // 阶段更新时刻

	Queued   int // 排队消息数
	QueueCap int // 排队上限

	Subagents int  // 运行中的异步子代理数
	PlanOn    bool // 是否处于计划模式

	SlotsUsed int // 全局并发槽位占用
	SlotsCap  int // 全局并发槽位上限
}

// handleStatusCommand 处理 /status 命令：回复当前会话的 AI 状态（返回 false
// 消费消息，同 /stop；群聊中已由 mention 门保证只有 @ 机器人才会走到这里）。
func (p *AIChatPlugin) handleStatusCommand(b bot.Bot, msg message.Message) (bool, error) {
	isGroup := msg.GroupId != ""
	id := msg.Sender.UserId
	if isGroup {
		id = msg.GroupId
	}
	p.sendPlainText(b, id, isGroup, renderSessionStatus(p.collectSessionStatus(id, isGroup)))
	return false, nil
}

// collectSessionStatus 采集会话状态快照。
func (p *AIChatPlugin) collectSessionStatus(id message.QID, isGroup bool) sessionStatusSnapshot {
	key := sessionKey(id, isGroup)
	st := sessionStatusSnapshot{
		IsGroup:  isGroup,
		Target:   id,
		QueueCap: maxPendingMessages,
	}

	if v, ok := p.activeContexts.Load(key); ok {
		if ar, ok := v.(*activeRequest); ok && ar != nil {
			stage, stageAt := ar.stageSnapshot()
			st.Active = true
			st.Stage = stage
			st.StageAt = stageAt
			if !ar.start.IsZero() {
				st.Elapsed = time.Since(ar.start)
			}
		}
	}
	// 活跃请求登记晚于会话锁获取（建会话/回放历史可能耗时数秒），期间锁已占用
	// 但 activeContexts 尚为空；补探一次锁键，让 /status 此时也能显示「响应中」
	if !st.Active && p.sessionLockHeld(key) {
		st.Active = true
		st.Stage = stageBusy
	}
	st.Queued = p.pendingCount(key)
	st.Subagents = len(p.listRunningSubagents(id, isGroup))
	if p.planManager != nil {
		st.PlanOn = p.planManager.IsOn(key)
	}
	if p.rateCh != nil {
		st.SlotsCap = cap(p.rateCh)
		st.SlotsUsed = len(p.rateCh)
	}
	return st
}

// renderSessionStatus 把状态快照渲染成用户可读的文本。
//
// 状态优先级：响应中 > 等待子代理结果 > 排队中 > 空闲。异步子代理在主请求
// 结束后仍在后台执行，其完成结果会自动入队并触发新一轮回复，因此单独成一档。
func renderSessionStatus(st sessionStatusSnapshot) string {
	var sb strings.Builder
	kind := "私聊"
	if st.IsGroup {
		kind = "群聊"
	}
	fmt.Fprintf(&sb, "【会话状态】%s %s\n", kind, st.Target.String())

	switch {
	case st.Active:
		if st.Elapsed > 0 {
			fmt.Fprintf(&sb, "AI 状态：响应中（已持续 %s）\n", formatStatusDuration(st.Elapsed))
		} else {
			sb.WriteString("AI 状态：响应中\n")
		}
		if st.Stage != "" {
			if age := time.Since(st.StageAt); !st.StageAt.IsZero() && age >= time.Second {
				fmt.Fprintf(&sb, "最近阶段：%s（%s 前）\n", st.Stage, formatStatusDuration(age))
			} else {
				fmt.Fprintf(&sb, "最近阶段：%s\n", st.Stage)
			}
		}
	case st.Subagents > 0:
		sb.WriteString("AI 状态：等待子代理结果（后台执行中，完成后会自动回复）\n")
	case st.Queued > 0:
		sb.WriteString("AI 状态：排队中（等待调度）\n")
	default:
		sb.WriteString("AI 状态：空闲\n")
	}

	if st.Queued > 0 {
		fmt.Fprintf(&sb, "排队消息：%d 条（上限 %d）\n", st.Queued, st.QueueCap)
	} else {
		sb.WriteString("排队消息：无\n")
	}
	if st.Subagents > 0 {
		fmt.Fprintf(&sb, "异步子代理：%d 个运行中\n", st.Subagents)
	}
	if st.PlanOn {
		sb.WriteString("计划模式：开启（发送 /plan off 退出）\n")
	}
	// 只在全局并发槽位已满时提示：这是排队消息「迟迟不动」的常见原因之一
	if st.SlotsCap > 0 && st.SlotsUsed >= st.SlotsCap {
		fmt.Fprintf(&sb, "并发槽位：已占满（%d/%d），新消息需等待空闲名额\n", st.SlotsUsed, st.SlotsCap)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatStatusDuration 状态展示用的时长文案（秒级精度，最多两级单位；
// 整分/整时不带零头，避免「1 分 0 秒」这种别扭的写法）。
func formatStatusDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int64(d / time.Second)
	switch {
	case sec < 1:
		return "不到 1 秒"
	case sec < 60:
		return fmt.Sprintf("%d 秒", sec)
	case sec < 3600:
		if sec%60 == 0 {
			return fmt.Sprintf("%d 分", sec/60)
		}
		return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
	default:
		if (sec%3600)/60 == 0 {
			return fmt.Sprintf("%d 小时", sec/3600)
		}
		return fmt.Sprintf("%d 小时 %d 分", sec/3600, (sec%3600)/60)
	}
}
