package pluginsys

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/bot/component/sysrestart"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// rebootForceFlag 判断 /reboot 是否带强制重启参数（force / -f）。
func rebootForceFlag(args []string) bool {
	for _, a := range args {
		if strings.EqualFold(strings.TrimSpace(a), "force") || a == "-f" {
			return true
		}
	}
	return false
}

// rebootReplyText 重启命令的回复文案：默认走优雅重启（等待进行中的请求完成），
// 强制模式立即重启。
func rebootReplyText(force bool) string {
	if force {
		return "AniaBot 将立即强制重启，重启期间将短暂离线..."
	}
	return "AniaBot 即将重启：有正在处理的消息会先等待其完成，重启期间将短暂离线...（管理员可用 /reboot force 立即重启）"
}

type PluginSys struct {
	plugin.Meta

	// panicMu 保护 lastPanicTime：OnPanic 可能被多个 goroutine（如并发的
	// 定时任务）同时触发，无锁的读改写是数据竞争
	panicMu       sync.Mutex
	lastPanicTime *time.Time
}

func NewPluginSys() *PluginSys {
	return &PluginSys{
		Meta: plugin.Meta{
			Name:      "系统插件",
			HelpWords: "AniaBot系统插件",
			Order:     plugin.LevelLog - 100,
			ShowFor:   plugininfo.ShowForGroup | plugininfo.ShowForFriend,
			Author:    "jeanhua",
			Version:   "1.0.0",
		},
	}
}

func (p *PluginSys) OnFriendMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if cmd.Name == "help" {
		plugins := bot.GetPluginList()
		var pluginInfo strings.Builder
		pluginInfo.WriteString("欢迎使用AniaBot，已加载插件:")
		idx := 1
		for _, info := range plugins {
			if info.AdminOnly && !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
				continue
			}
			if !p.SystemConfig.IsAdmin(msg.Sender.UserId) && info.ShowFor&plugininfo.ShowForFriend == 0 {
				continue
			}
			pName := info.Name
			pHelpWords := info.HelpWords
			pluginInfo.WriteString(fmt.Sprintf("\n%d. %s: %s", idx, pName, pHelpWords))
			idx += 1
		}
		c := msgchain.Builder().Friend()
		c.Text(pluginInfo.String())
		_, ok := bot.SendFriendMsg(msg.Sender.UserId, c.Build())
		if !ok {
			p.Logger.Error("Bot消息发送失败，无法响应 /help")
		}
		return false, nil
	} else if cmd.Name == "exit" && p.SystemConfig.IsAdmin(msg.Sender.UserId) {
		builder := msgchain.Builder().Friend()
		builder.Text("AniaBot已退出")
		_, ok := bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		if !ok {
			p.Logger.Error("Bot消息发送失败，无法响应 /exit")
		}
		bot.Stop()
		os.Exit(0)
		return false, nil
	} else if cmd.Name == "reboot" {
		if !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
			builder := msgchain.Builder().Friend()
			builder.Text("没有权限，只有管理员才能重启 AniaBot")
			_, ok := bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
			if !ok {
				p.Logger.Error("Bot消息发送失败，无法响应 /reboot")
			}
			return false, nil
		}
		force := rebootForceFlag(cmd.Args)
		builder := msgchain.Builder().Friend()
		builder.Text(rebootReplyText(force))
		_, ok := bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		if !ok {
			p.Logger.Error("Bot消息发送失败，无法响应 /reboot")
		}
		detail := fmt.Sprintf("管理员 %s 通过 /reboot 命令重启 Bot", msg.Sender.UserId)
		if force {
			detail += "（强制立即重启）"
		}
		oplog.Record(oplog.CategorySystem, "restart", detail)
		sysrestart.RequestRestart(p.Logger, sysrestart.RestartOptions{
			Source: "command",
			Reason: "管理员 /reboot 命令",
			Force:  force,
		})
		return false, nil
	}
	return true, nil
}

func (p *PluginSys) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if cmd.Name == "help" && cmd.Mention {
		plugins := bot.GetPluginList()
		var pluginInfo strings.Builder
		pluginInfo.WriteString("\n欢迎使用AniaBot，已加载插件:")
		idx := 1
		for _, info := range plugins {
			if info.AdminOnly && !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
				continue
			}
			if info.ShowFor&plugininfo.ShowForGroup == 0 {
				continue
			}
			pName := info.Name
			pHelpWords := info.HelpWords
			pluginInfo.WriteString(fmt.Sprintf("\n%d. %s: %s", idx, pName, pHelpWords))
			idx += 1
		}
		c := msgchain.Builder().Group()
		c.Mention(msg.Sender.UserId)
		c.Text(pluginInfo.String())
		_, ok := bot.SendGroupMsg(msg.GroupId, c.Build())
		if !ok {
			p.Logger.Error("Bot消息发送失败，无法响应 /help")
		}
		return false, nil
	} else if cmd.Name == "reboot" && cmd.Mention {
		c := msgchain.Builder().Group()
		c.Mention(msg.Sender.UserId)
		if !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
			c.Text(" 没有权限，只有管理员才能重启 AniaBot")
			_, ok := bot.SendGroupMsg(msg.GroupId, c.Build())
			if !ok {
				p.Logger.Error("Bot消息发送失败，无法响应 /reboot")
			}
			return false, nil
		}
		force := rebootForceFlag(cmd.Args)
		c.Text(" " + rebootReplyText(force))
		_, ok := bot.SendGroupMsg(msg.GroupId, c.Build())
		if !ok {
			p.Logger.Error("Bot消息发送失败，无法响应 /reboot")
		}
		detail := fmt.Sprintf("管理员 %s 通过 /reboot 命令重启 Bot（群 %s）", msg.Sender.UserId, msg.GroupId)
		if force {
			detail += "（强制立即重启）"
		}
		oplog.Record(oplog.CategorySystem, "restart", detail)
		sysrestart.RequestRestart(p.Logger, sysrestart.RestartOptions{
			Source: "command",
			Reason: "管理员 /reboot 命令",
			Force:  force,
		})
		return false, nil
	}
	return true, nil
}

func (p *PluginSys) OnPanic(ctx context.Context, bot bot.Bot, name string, err any) {
	p.Logger.Error("插件运行时panic", "name", name, "err", err)
	now := time.Now()
	p.panicMu.Lock()
	shouldNotify := p.lastPanicTime == nil || now.Sub(*p.lastPanicTime) > time.Minute
	if shouldNotify {
		p.lastPanicTime = &now
	}
	p.panicMu.Unlock()
	if shouldNotify {
		for _, admin := range p.SystemConfig.Admins() {
			builder := msgchain.Builder().Friend()
			builder.Text(fmt.Sprintf("线程 %s 运行时panic: %v", name, err))
			if _, ok := bot.SendFriendMsg(admin, builder.Build()); !ok {
				p.Logger.Error("Bot消息发送失败，无法通知管理员", "admin", admin)
			}
		}
	}
}
