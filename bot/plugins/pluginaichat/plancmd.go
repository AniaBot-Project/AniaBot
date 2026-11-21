package pluginaichat

import (
	"context"
	"strings"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// handlePlanCommand 处理 /plan 命令：计划模式开关（返回 false 消费消息，同 /stop）。
//
// 用法：
//
//	/plan on    开启计划模式：AI 只做分析与规划，副作用操作（改文件、跑命令等）会被阻止
//	/plan off   退出计划模式（即批准计划，恢复正常执行）
//	/plan       查看当前状态
func (p *AIChatPlugin) handlePlanCommand(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	id, isGroup := messageTarget(msg)
	key := sessionKey(id, isGroup)

	sub := ""
	if len(cmd.Args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	}

	switch sub {
	case "on", "enable":
		if p.planManager.IsOn(key) {
			p.sendPlainText(b, id, isGroup, "已处于计划模式")
		} else {
			p.planManager.Set(key, true)
			p.sendPlainText(b, id, isGroup, "已进入计划模式：AI 将只做分析与规划，不会执行修改文件、运行命令等副作用操作。确认计划后发送 /plan off 退出并开始执行。")
		}
	case "off", "disable":
		if !p.planManager.IsOn(key) {
			p.sendPlainText(b, id, isGroup, "当前未处于计划模式")
		} else {
			p.planManager.Set(key, false)
			p.sendPlainText(b, id, isGroup, "已退出计划模式，可以开始执行了")
		}
	case "", "status":
		if p.planManager.IsOn(key) {
			p.sendPlainText(b, id, isGroup, "计划模式：开启中（发送 /plan off 退出）")
		} else {
			p.sendPlainText(b, id, isGroup, "计划模式：已关闭（发送 /plan on 开启）")
		}
	default:
		p.sendPlainText(b, id, isGroup, "用法：/plan on 开启计划模式，/plan off 退出，/plan 查看状态")
	}
	return false, nil
}
