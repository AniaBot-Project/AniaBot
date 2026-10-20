package pluginnews

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeanhua/AniaBot/common/aniaerror"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

type NewsPlugin struct {
	plugin.Meta
	cfg         newsConfig
	cronExpress string
	api         string
	groups      []message.QID
}

func NewNewsPlugin() *NewsPlugin {
	return &NewsPlugin{
		Meta: plugin.Meta{
			Name:      "每日新闻插件",
			HelpWords: "每日准点在指定群里新闻播报，发送 /news 立即获取，管理员发送 /news force 强制执行发送任务",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
			Author:    "jeanhua",
			Version:   "1.0.0",
		},
	}
}

func (p *NewsPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	// 配置已由框架自动填充到 p.cfg（见 ConfigSchema）
	if !p.cfg.Enable {
		p.Logger.Info("每日新闻插件已加载（未启用，跳过初始化）")
		return nil
	}
	p.cronExpress = p.cfg.Cron
	if p.cronExpress == "" {
		return fmt.Errorf("%w: 未配置 cron 表达式（plugin.dailynews.cron）", aniaerror.ParameterInitializeError)
	}
	p.api = p.cfg.API
	if p.api == "" {
		return fmt.Errorf("%w: 未配置 API 端点（plugin.dailynews.api）", aniaerror.ParameterInitializeError)
	}
	for _, g := range p.cfg.Groups {
		gid := message.FromString(strings.TrimSpace(g))
		p.Logger.Info("播报群聊注册", "groupId", gid)
		p.groups = append(p.groups, gid)
	}
	return nil
}

func (p *NewsPlugin) StartCron(ctx context.Context, bot bot.Bot, c plugin.CronManager) error {
	if !p.cfg.Enable {
		return nil
	}
	if _, err := c.AddFunc(p.cronExpress, func() {
		p.sendNews(bot)
	}); err != nil {
		// cron 表达式非法时任务会静默不注册，必须显式报错避免"看似启动成功但从不播报"
		return fmt.Errorf("注册每日新闻定时任务失败（cron 表达式非法）: %w", err)
	}
	return nil
}

func (p *NewsPlugin) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if cmd.Mention && cmd.Name == "news" {
		builder := msgchain.Builder().Group()
		builder.ImageUrl(p.api)
		_, ok := bot.SendGroupMsg(msg.GroupId, builder.Build())
		if ok {
			p.Logger.Info("发送消息", "groupId", msg.GroupId, "message", "[每日新闻]")
		} else {
			p.Logger.Error("发送消息失败", "groupId", msg.GroupId, "message", "[每日新闻] 发送失败...")
		}
		return false, nil
	}
	return true, nil
}

func (p *NewsPlugin) OnFriendMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if p.SystemConfig.IsAdmin(msg.Sender.UserId) && cmd.Name == "news" && len(cmd.Args) > 0 && cmd.Args[0] == "force" {
		p.sendNews(bot)
		return false, nil
	}
	return true, nil
}

func (p *NewsPlugin) sendNews(bot bot.Bot) {
	for _, group := range p.groups {
		builder := msgchain.Builder().Group()
		builder.ImageUrl(p.api)
		_, ok := bot.SendGroupMsg(group, builder.Build())
		if ok {
			p.Logger.Info("发送消息", "groupId", group, "message", "[每日新闻]")
		} else {
			p.Logger.Error("发送消息失败", "groupId", group, "message", "[每日新闻] 发送失败...")
		}
	}
}
