package pluginaichat

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// handleClockCommand 处理 /clock 命令。返回 (next, error) 与插件事件方法语义一致。
//
// 用法：
//
//	/clock                查看当前会话的定时任务
//	/clock list [all]     列出任务（all 仅管理员，列出全部）
//	/clock add [--once] <cron> | <标题> | <内容>   新增任务（默认当前会话为触发对象，--once 表示单次任务）
//	/clock del <id>       删除任务
//	/clock on <id>        启用任务
//	/clock off <id>       停用任务
//	/clock info <id>      查看任务详情与最近执行记录
//	/clock run <id>       立即执行一次（仅管理员）
//	/clock log [n]        查看最近 n 条执行记录（默认 10）
//	/clock help           查看帮助
func (p *AIChatPlugin) handleClockCommand(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if p.clockManager == nil {
		p.replyClock(b, msg, "定时任务功能未启用")
		return false, nil
	}

	isGroup := msg.GroupId != ""
	targetType := clockTargetFriend
	targetID := msg.Sender.UserId.String()
	if isGroup {
		targetType = clockTargetGroup
		targetID = msg.GroupId.String()
	}
	isAdmin := p.SystemConfig.IsAdmin(msg.Sender.UserId)

	sub := ""
	if len(cmd.Args) > 0 {
		sub = cmd.Args[0]
	}
	rest := cmd.Args
	if len(rest) > 0 {
		rest = rest[1:]
	}

	switch sub {
	case "", "list":
		all := len(rest) > 0 && rest[0] == "all"
		if all && !isAdmin {
			p.replyClock(b, msg, "仅管理员可查看全部定时任务")
			return false, nil
		}
		p.replyClock(b, msg, p.cmdList(targetType, targetID, all))
	case "add":
		p.replyClock(b, msg, p.cmdAdd(rest, targetType, targetID, msg.Sender.UserId))
	case "del", "delete", "rm":
		p.replyClock(b, msg, p.cmdDelete(rest, targetType, targetID, isAdmin))
	case "on", "enable":
		p.replyClock(b, msg, p.cmdToggle(rest, true, targetType, targetID, isAdmin, msg.Sender.UserId))
	case "off", "disable":
		p.replyClock(b, msg, p.cmdToggle(rest, false, targetType, targetID, isAdmin, msg.Sender.UserId))
	case "info":
		p.replyClock(b, msg, p.cmdInfo(rest, targetType, targetID, isAdmin))
	case "timeout":
		p.replyClock(b, msg, p.cmdTimeout(rest, targetType, targetID, isAdmin, msg.Sender.UserId))
	case "run":
		if !isAdmin {
			p.replyClock(b, msg, "仅管理员可手动触发任务")
			return false, nil
		}
		p.replyClock(b, msg, p.cmdRun(rest))
	case "log":
		n := 10
		if len(rest) > 0 {
			if v, err := strconv.Atoi(rest[0]); err == nil && v > 0 {
				n = v
			}
		}
		p.replyClock(b, msg, p.cmdLog(targetType, targetID, n, isAdmin))
	case "help", "?":
		p.replyClock(b, msg, clockHelpText(isAdmin))
	default:
		p.replyClock(b, msg, "未知的子命令，发送 /clock help 查看用法")
	}
	return false, nil
}

func (p *AIChatPlugin) cmdList(targetType string, targetID string, all bool) string {
	var tasks []*ClockTask
	if all {
		tasks = p.clockManager.List()
	} else {
		tasks = p.clockManager.ListByTarget(targetType, targetID)
	}
	if len(tasks) == 0 {
		if all {
			return "当前没有任何定时任务"
		}
		return "当前会话没有定时任务，使用 /clock help 查看如何添加"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("定时任务（共 %d 条）：\n", len(tasks)))
	for _, t := range tasks {
		sb.WriteString(formatTaskLine(t))
		sb.WriteString("\n")
	}
	return sb.String()
}

func (p *AIChatPlugin) cmdAdd(args []string, targetType string, targetID string, sender message.QID) string {
	// 格式：[--once] <cron> | <标题> | <内容>，cron 可含空格，以 | 切分三段
	// 可选 --once 前缀标志：标记为单次任务，触发执行后自动销毁
	runOnce := false
	if len(args) > 0 && args[0] == "--once" {
		runOnce = true
		args = args[1:]
	}
	raw := strings.Join(args, " ")
	parts := strings.SplitN(raw, "|", 3)
	if len(parts) < 3 {
		return "用法：/clock add [--once] <cron表达式> | <标题> | <内容>\n示例：/clock add 0 8 * * * | 早安 | 大家早上好"
	}
	cronExpr := strings.TrimSpace(parts[0])
	title := strings.TrimSpace(parts[1])
	content := strings.TrimSpace(parts[2])
	if cronExpr == "" || content == "" {
		return "cron 表达式和任务内容不能为空"
	}
	task := &ClockTask{
		Cron:       cronExpr,
		Title:      title,
		Content:    content,
		TargetType: targetType,
		TargetID:   targetID,
		RunOnce:    runOnce,
		Enabled:    true,
		CreatedBy:  sender, // 默认 @ 创建人本人
		Creator:    sender.String(),
	}
	id, err := p.clockManager.Add(task)
	if err != nil {
		return "添加失败：" + err.Error()
	}
	next := ""
	if !task.NextRunAt.IsZero() {
		next = "，下次触发 " + task.NextRunAt.Local().Format("01-02 15:04")
	}
	mode := "重复"
	if runOnce {
		mode = "单次"
	}
	return fmt.Sprintf("已添加定时任务（ID: %s，模式: %s）%s\ncron: %s\n标题: %s", id, mode, next, cronExpr, title)
}

// checkTaskOwned 校验任务归属：任务属于当前会话（target 与本次命令的会话一致）
// 或操作者为管理员时返回 true；任务不存在返回 false。
// 任务 ID 为自增序号可枚举，若不校验归属，任意成员可删除/查看其他会话的任务。
func (p *AIChatPlugin) checkTaskOwned(t *ClockTask, targetType, targetID string, isAdmin bool) bool {
	if t == nil {
		return false
	}
	if isAdmin {
		return true
	}
	return t.TargetType == targetType && t.TargetID == targetID
}

func (p *AIChatPlugin) cmdDelete(args []string, targetType, targetID string, isAdmin bool) string {
	id := pickID(args)
	if id == "" {
		return "用法：/clock del <id>"
	}
	t, ok := p.clockManager.Get(id)
	if !ok {
		return "定时任务不存在: " + id
	}
	if !p.checkTaskOwned(t, targetType, targetID, isAdmin) {
		return "无权操作该定时任务（只能操作当前会话的任务）"
	}
	if p.clockManager.Delete(id) {
		return "已删除定时任务 " + id
	}
	return "定时任务不存在: " + id
}

func (p *AIChatPlugin) cmdToggle(args []string, enable bool, targetType, targetID string, isAdmin bool, sender message.QID) string {
	id := pickID(args)
	if id == "" {
		verb := "on"
		if !enable {
			verb = "off"
		}
		return "用法：/clock " + verb + " <id>"
	}
	t, ok := p.clockManager.Get(id)
	if !ok {
		return "定时任务不存在: " + id
	}
	if !p.checkTaskOwned(t, targetType, targetID, isAdmin) {
		return "无权操作该定时任务（只能操作当前会话的任务）"
	}
	f := ClockUpdateFields{Enabled: &enable}
	if _, err := p.clockManager.Update(id, f, sender.String()); err != nil {
		return err.Error()
	}
	if enable {
		return "已启用 定时任务 " + id
	}
	return "已停用 定时任务 " + id
}

func (p *AIChatPlugin) cmdInfo(args []string, targetType, targetID string, isAdmin bool) string {
	id := pickID(args)
	if id == "" {
		return "用法：/clock info <id>"
	}
	t, ok := p.clockManager.Get(id)
	if !ok {
		return "定时任务不存在: " + id
	}
	if !p.checkTaskOwned(t, targetType, targetID, isAdmin) {
		return "无权查看该定时任务（只能查看当前会话的任务）"
	}
	var sb strings.Builder
	sb.WriteString(formatTaskDetail(t))
	logs := p.clockManager.log.RecentForTask(id, 5)
	if len(logs) > 0 {
		sb.WriteString("\n最近执行记录：\n")
		for _, e := range logs {
			sb.WriteString(formatLogLine(e))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func (p *AIChatPlugin) cmdRun(args []string) string {
	id := pickID(args)
	if id == "" {
		return "用法：/clock run <id>"
	}
	if p.clockManager.RunNow(id) {
		return "已触发任务 " + id + "，执行结果见日志（/clock log）"
	}
	return "定时任务不存在: " + id
}

// cmdTimeout 设置任务超时：/clock timeout <id> <秒数>（秒数为0表示恢复默认）。
func (p *AIChatPlugin) cmdTimeout(args []string, targetType, targetID string, isAdmin bool, sender message.QID) string {
	if len(args) < 2 {
		return "用法：/clock timeout <id> <秒数>"
	}
	id := strings.TrimSpace(args[0])
	sec, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || sec < 0 {
		return "秒数必须是非负整数"
	}
	t, ok := p.clockManager.Get(id)
	if !ok {
		return "定时任务不存在: " + id
	}
	if !p.checkTaskOwned(t, targetType, targetID, isAdmin) {
		return "无权操作该定时任务（只能操作当前会话的任务）"
	}
	f := ClockUpdateFields{TimeoutSec: &sec}
	if _, err := p.clockManager.Update(id, f, sender.String()); err != nil {
		return err.Error()
	}
	if sec > 0 {
		return fmt.Sprintf("已设置任务 %s 超时为 %ds", id, sec)
	}
	return "已恢复任务 " + id + " 超时为默认值"
}

func (p *AIChatPlugin) cmdLog(targetType string, targetID string, n int, all bool) string {
	var logs []tasklog.Entry
	if all {
		logs = p.clockManager.log.Recent(n)
	} else {
		// 普通视角：仅本会话触发对象的日志，多取一些再过滤
		entries := p.clockManager.log.Recent(n * 5)
		for _, e := range entries {
			if e.TargetType == targetType && e.TargetID == targetID {
				logs = append(logs, e)
				if len(logs) >= n {
					break
				}
			}
		}
	}
	if len(logs) == 0 {
		return "暂无执行记录"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("最近 %d 条执行记录：\n", len(logs)))
	for _, e := range logs {
		sb.WriteString(formatLogLine(e))
		sb.WriteString("\n")
	}
	return sb.String()
}

// replyClock 在当前会话回复文本（群聊不 @，保持简洁）。
func (p *AIChatPlugin) replyClock(b bot.Bot, msg message.Message, text string) {
	if msg.GroupId != "" {
		builder := msgchain.Builder().Group()
		builder.Text(text)
		if _, ok := b.SendGroupMsg(msg.GroupId, builder.Build()); !ok {
			p.Logger.Error("clock 回复发送失败", "group", msg.GroupId)
		}
		return
	}
	builder := msgchain.Builder().Friend()
	builder.Text(text)
	if _, ok := b.SendFriendMsg(msg.Sender.UserId, builder.Build()); !ok {
		p.Logger.Error("clock 回复发送失败", "user", msg.Sender.UserId)
	}
}

// ---- 格式化辅助 ----

func pickID(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

func formatTaskLine(t *ClockTask) string {
	state := "✅"
	if !t.Enabled {
		state = "⏸️"
	}
	target := "群" + t.TargetID
	if t.TargetType == clockTargetFriend {
		target = "好友" + t.TargetID
	}
	mode := "重复"
	if t.RunOnce {
		mode = "单次"
	}
	return fmt.Sprintf("%s [%s] %s | %s | %s → %s", state, t.ID, truncStr(t.Title, 12), mode, t.Cron, target)
}

func formatTaskDetail(t *ClockTask) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("任务 ID: %s\n", t.ID))
	sb.WriteString(fmt.Sprintf("状态: %s\n", enabledText(t.Enabled)))
	sb.WriteString(fmt.Sprintf("模式: %s\n", runOnceText(t.RunOnce)))
	sb.WriteString(fmt.Sprintf("cron: %s\n", t.Cron))
	sb.WriteString(fmt.Sprintf("标题: %s\n", t.Title))
	sb.WriteString(fmt.Sprintf("内容: %s\n", t.Content))
	target := "群聊 " + t.TargetID
	if t.TargetType == clockTargetFriend {
		target = "好友 " + t.TargetID
	}
	sb.WriteString(fmt.Sprintf("触发对象: %s\n", target))
	if t.TimeoutSec > 0 {
		sb.WriteString(fmt.Sprintf("超时: %ds\n", t.TimeoutSec))
	} else {
		sb.WriteString("超时: 默认\n")
	}
	if t.Note != "" {
		sb.WriteString(fmt.Sprintf("备注: %s\n", t.Note))
	}
	if t.Creator != "" {
		sb.WriteString("创建人: " + actorText(t.Creator) + "\n")
	}
	if t.Updater != "" {
		sb.WriteString("更新人: " + actorText(t.Updater) + "\n")
	}
	if !t.CreatedAt.IsZero() {
		sb.WriteString("创建于: " + t.CreatedAt.Local().Format("2006-01-02 15:04") + "\n")
	}
	if !t.NextRunAt.IsZero() {
		sb.WriteString("下次触发: " + t.NextRunAt.Local().Format("2006-01-02 15:04") + "\n")
	}
	if !t.LastRunAt.IsZero() {
		sb.WriteString("上次触发: " + t.LastRunAt.Local().Format("2006-01-02 15:04") + "\n")
	}
	return sb.String()
}

func formatLogLine(e tasklog.Entry) string {
	t := e.TriggerTime.Local().Format("01-02 15:04:05")
	dur := fmt.Sprintf("%dms", e.DurationMs)
	tail := ""
	if e.Error != "" {
		tail = " " + truncStr(e.Error, 30)
	}
	tokens := ""
	if e.TotalTokens > 0 {
		tokens = fmt.Sprintf(" %dtok", e.TotalTokens)
	}
	// AI 调用 skip_output 的执行没有推送消息，标出来避免看起来像「没有输出」
	skipped := ""
	if e.OutputSkipped {
		skipped = " [已跳过输出"
		if e.SkipReason != "" {
			skipped += "：" + truncStr(e.SkipReason, 20)
		}
		skipped += "]"
	}
	return fmt.Sprintf("- %s [%s] %s %s%s%s%s", t, string(e.Status), dur, e.TaskTitle, tokens, skipped, tail)
}

func enabledText(on bool) string {
	if on {
		return "启用"
	}
	return "停用"
}

// actorText 把操作人标识转为可读文本：ai / panel 为固定标识，其余为用户 ID 原样返回。
func actorText(actor string) string {
	switch actor {
	case "ai":
		return "AI"
	case "panel":
		return "面板"
	}
	return actor
}

// runOnceText 返回任务模式的中文描述。
func runOnceText(once bool) string {
	if once {
		return "单次"
	}
	return "重复"
}

func truncStr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// clockHelpText 返回 /clock 帮助文本。
func clockHelpText(isAdmin bool) string {
	help := `定时任务命令：
/clock              查看当前会话的定时任务
/clock add [--once] <cron> | <标题> | <内容>   新增任务（--once 为单次任务，触发后自动销毁）
/clock del <id>     删除任务
/clock on <id>      启用任务
/clock off <id>     停用任务
/clock info <id>    查看任务详情与最近执行记录
/clock timeout <id> <秒数>  设置单次执行超时（0 表示默认）
/clock log [n]      查看最近 n 条执行记录（默认 10）
/clock help         查看此帮助
cron 示例：0 8 * * *（每天8点）、*/30 * * * *（每30分钟）、@every 1h（每小时）`
	if isAdmin {
		help += `
管理员命令：
/clock list all     查看全部任务
/clock run <id>     立即执行一次`
	}
	return help
}
