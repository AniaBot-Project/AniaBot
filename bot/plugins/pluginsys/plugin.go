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

// rebootDelay /reboot 重启前的延迟，给回复消息留出发送时间
const rebootDelay = 2 * time.Second

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

func (p *PluginSys) Awake(ctx context.Context, bot bot.Bot) error {
	// 未配置管理员 ID（首次启动设置向导期间是正常状态）跳过启动通知，
	// 避免向空目标发送而刷错误日志
	admins := p.SystemConfig.Admins()
	if len(admins) == 0 {
		p.Logger.Info("未配置管理员 ID，跳过启动成功通知")
		return nil
	}
	for _, admin := range admins {
		builder := msgchain.Builder().Friend()
		builder.Text("AniaBot启动成功，发送 /help 查看插件加载信息")
		if _, ok := bot.SendFriendMsg(admin, builder.Build()); !ok {
			p.Logger.Warn("启动成功消息发送失败（管理员不在线或所在平台未启用？）", "admin", admin)
		}
	}
	return nil
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
		builder := msgchain.Builder().Friend()
		builder.Text("AniaBot即将重启，重启期间将短暂离线...")
		_, ok := bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		if !ok {
			p.Logger.Error("Bot消息发送失败，无法响应 /reboot")
		}
		oplog.Record(oplog.CategorySystem, "restart", fmt.Sprintf("管理员 %s 通过 /reboot 命令重启 Bot", msg.Sender.UserId))
		// 延迟重启：先让回复走完发送流程，再替换进程
		go func() {
			time.Sleep(rebootDelay)
			sysrestart.Self(p.Logger)
		}()
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
		c.Text(" AniaBot即将重启，重启期间将短暂离线...")
		_, ok := bot.SendGroupMsg(msg.GroupId, c.Build())
		if !ok {
			p.Logger.Error("Bot消息发送失败，无法响应 /reboot")
		}
		oplog.Record(oplog.CategorySystem, "restart", fmt.Sprintf("管理员 %s 通过 /reboot 命令重启 Bot（群 %s）", msg.Sender.UserId, msg.GroupId))
		// 延迟重启：先让回复走完发送流程，再替换进程
		go func() {
			time.Sleep(rebootDelay)
			sysrestart.Self(p.Logger)
		}()
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
