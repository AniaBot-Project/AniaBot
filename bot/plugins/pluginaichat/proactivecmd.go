package pluginaichat

// /proactive 指令：管理员在会话里快速开关「主动互动」。
//
// 用法：
//
//	/proactive                查看本会话状态
//	/proactive on             开启本会话的主动互动，并写回配置白名单（管理员）
//	/proactive off            关闭本会话，并从白名单中移除（管理员）
//	/proactive reload         从配置中心重新读取白名单（面板改完不必重启，管理员）
//	/proactive now [想说的话]  立即触发一次，跳过时段/间隔/上限等时间类闸门（管理员，用于调试）
//	/proactive list           列出所有启用主动互动的会话（管理员）
//	/proactive log [n]        查看本会话最近的主动发言记录（管理员）
//	/proactive help           查看帮助
//
// 开关直接改写配置项 plugin.ai_chat_bot.proactive.scopes：面板即时可见、重启后
// 保留，与面板手动编辑的是同一份名单，不存在另一份隐藏的状态。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
)

func (p *AIChatPlugin) handleProactiveCommand(_ context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	isGroup := msg.GroupId != ""
	id := msg.Sender.UserId
	if isGroup {
		id = msg.GroupId
	}
	scope := sessionKey(id, isGroup)
	isAdmin := p.SystemConfig.IsAdmin(msg.Sender.UserId)

	if p.proactiveManager == nil {
		p.sendPlainText(b, id, isGroup,
			"主动互动功能未启用：需要先在面板「AI 对话 · 主动互动」打开总开关并重启 Bot。")
		return false, nil
	}

	sub := ""
	if len(cmd.Args) > 0 {
		sub = cmd.Args[0]
	}
	rest := cmd.Args
	if len(rest) > 0 {
		rest = rest[1:]
	}

	switch sub {
	case "", "status":
		p.sendPlainText(b, id, isGroup, p.proactiveManager.statusText(scope, isGroup))
	case "on", "off":
		if !isAdmin {
			p.sendPlainText(b, id, isGroup, "只有管理员可以修改主动互动开关")
			return false, nil
		}
		changed, persisted := p.proactiveManager.setScopeEnabled(scope, sub == "on")
		msg := p.proactiveManager.statusText(scope, isGroup)
		switch {
		case !persisted:
			msg += "\n注意：配置中心不可用，本次改动只对当前运行生效。"
		case changed:
			msg += "\n已写入配置项，面板刷新即可看到。"
		}
		p.sendPlainText(b, id, isGroup, msg)
	case "auto", "reload":
		if !isAdmin {
			p.sendPlainText(b, id, isGroup, "只有管理员可以重新加载白名单")
			return false, nil
		}
		n, ok := p.proactiveManager.reloadScopes()
		if !ok {
			p.sendPlainText(b, id, isGroup, "配置中心不可用，无法从配置重新加载白名单")
			return false, nil
		}
		p.sendPlainText(b, id, isGroup, fmt.Sprintf("已从配置中心重新加载白名单（共 %d 个会话）。\n%s",
			n, p.proactiveManager.statusText(scope, isGroup)))
	case "now":
		if !isAdmin {
			p.sendPlainText(b, id, isGroup, "只有管理员可以手动触发主动发言")
			return false, nil
		}
		p.sendPlainText(b, id, isGroup, p.proactiveManager.triggerNow(scope, isGroup, strings.Join(rest, " ")))
	case "list":
		if !isAdmin {
			p.sendPlainText(b, id, isGroup, "只有管理员可以查看全部会话")
			return false, nil
		}
		p.sendPlainText(b, id, isGroup, p.proactiveManager.listText())
	case "log":
		if !isAdmin {
			p.sendPlainText(b, id, isGroup, "只有管理员可以查看主动发言记录")
			return false, nil
		}
		n := 10
		if len(rest) > 0 {
			if v, err := strconv.Atoi(rest[0]); err == nil && v > 0 {
				n = min(v, 50)
			}
		}
		p.sendPlainText(b, id, isGroup, p.proactiveManager.logText(scope, n))
	case "help", "?":
		p.sendPlainText(b, id, isGroup, proactiveHelpText(isAdmin))
	default:
		p.sendPlainText(b, id, isGroup, "未知的子命令，发送 /proactive help 查看用法")
	}
	return false, nil
}

// statusText 描述本会话的主动互动状态。
func (m *proactiveManager) statusText(scope string, isGroup bool) string {
	scene := "私聊"
	if isGroup {
		scene = "群聊"
	}
	state := m.scopeState(scope)
	enabled := state == proactiveScopeOn

	var mode string
	if enabled {
		mode = "已启用（在配置白名单内）"
	} else {
		mode = "未启用（不在配置白名单内）"
	}

	today := m.store.countSentSince(scope, startOfDay(time.Now()))
	pending := m.store.pendingByScope(scope)

	var sb strings.Builder
	sb.WriteString("【主动互动】")
	sb.WriteString(scene)
	sb.WriteString(" ")
	sb.WriteString(scope)
	sb.WriteString("\n状态：")
	sb.WriteString(mode)
	sb.WriteString("\n今日已主动发言：")
	sb.WriteString(strconv.Itoa(today))
	sb.WriteString(" 次；待执行意图：")
	sb.WriteString(strconv.Itoa(len(pending)))
	sb.WriteString(" 条")
	if m.quietSpec != "" {
		sb.WriteString("\n免打扰时段：")
		sb.WriteString(m.quietSpec)
	}
	if !enabled {
		sb.WriteString("\n提示：管理员发送 /proactive on 可为本会话开启")
	}
	return sb.String()
}

// triggerNow 立即触发一次主动发言（调试用）：时间类闸门全部跳过，
// 只保留「会话已启用」与配额检查，仍然走完整的生成与发送流程。
func (m *proactiveManager) triggerNow(scope string, isGroup bool, text string) string {
	if !m.enabled(scope) {
		return "本会话未启用主动互动，先发送 /proactive on"
	}
	now := time.Now()
	it := &ProactiveIntention{
		ID:        newIntentionID(),
		Scope:     scope,
		Source:    proactiveSourceManual,
		Prompt:    truncateRunes(strings.TrimSpace(text), proactiveMaxPromptRunes),
		DueAt:     now,
		ExpiresAt: now.Add(m.expire),
		Status:    proactiveStatusPending,
		CreatedBy: "admin",
		CreatedAt: now,
	}
	if !m.store.insert(it) {
		return "创建意图失败，请查看日志"
	}
	m.launch(it, true)
	return "已触发一次主动发言（ID: " + it.ID + "），生成需要一点时间，稍后看结果。"
}

// listText 列出所有启用主动互动的会话。
func (m *proactiveManager) listText() string {
	scopes := m.enabledScopes()
	if len(scopes) == 0 {
		return "当前没有任何会话启用主动互动。管理员可在会话中发送 /proactive on，或在面板配置白名单。"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("已启用主动互动的会话（共 %d 个）：\n", len(scopes)))
	for _, scope := range scopes {
		sb.WriteString("- ")
		sb.WriteString(scope)
		if n := len(m.store.pendingByScope(scope)); n > 0 {
			sb.WriteString(fmt.Sprintf("（待执行 %d 条）", n))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// logText 列出本会话最近的意图及其结果，便于观察「为什么说了/没说话」。
func (m *proactiveManager) logText(scope string, n int) string {
	items := m.store.recentByScope(scope, n)
	if len(items) == 0 {
		return "本会话还没有主动发言记录。"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("本会话最近的主动发言记录（%d 条，新在前）：\n", len(items)))
	for i := range items {
		it := &items[i]
		sb.WriteString("- ")
		sb.WriteString(it.CreatedAt.Local().Format("01-02 15:04"))
		sb.WriteString(" ")
		sb.WriteString(sourceLabel(it.Source))
		sb.WriteString(" · ")
		sb.WriteString(it.Status)
		if it.Outcome != "" {
			sb.WriteString(" / ")
			sb.WriteString(outcomeLabel(it.Outcome))
		}
		if it.Reason != "" {
			sb.WriteString(" · ")
			sb.WriteString(truncateRunes(strings.Join(strings.Fields(it.Reason), " "), 80))
		}
		if it.Reply != "" {
			sb.WriteString("\n  → ")
			sb.WriteString(truncateRunes(strings.Join(strings.Fields(it.Reply), " "), 80))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func sourceLabel(source string) string {
	switch source {
	case proactiveSourceIdle:
		return "冷场"
	case proactiveSourceAI:
		return "AI 预约"
	case proactiveSourceManual:
		return "手动"
	default:
		return source
	}
}

func outcomeLabel(outcome string) string {
	switch outcome {
	case proactiveOutcomeReplied:
		return "有回应"
	case proactiveOutcomeIgnored:
		return "无人理"
	default:
		return outcome
	}
}

func proactiveHelpText(isAdmin bool) string {
	sb := strings.Builder{}
	sb.WriteString("主动互动：让 AI 在合适的时候主动开口（默认关闭，按会话启用）\n")
	sb.WriteString("/proactive —— 查看本会话状态\n")
	if isAdmin {
		sb.WriteString("/proactive on|off —— 开启/关闭本会话（直接改写面板里的白名单）\n")
		sb.WriteString("/proactive reload —— 从配置中心重新读取白名单（面板改完不必重启）\n")
		sb.WriteString("/proactive now [想说的话] —— 立即触发一次（调试用，跳过时段/间隔/上限限制）\n")
		sb.WriteString("/proactive list —— 列出所有启用主动互动的会话\n")
		sb.WriteString("/proactive log [n] —— 查看本会话最近的主动发言记录（默认 10 条）\n")
	} else {
		sb.WriteString("（开启/关闭、立即触发、查看列表与记录仅管理员可用）\n")
	}
	sb.WriteString("/proactive help —— 查看帮助")
	return sb.String()
}
