package pluginrepeat

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

type RepeatPlugin struct {
	plugin.Meta
	repeatGMap sync.Map
	enable     atomic.Bool
}

type repeatCount struct {
	lock     sync.Mutex
	msg      string
	count    int
	repeated bool
}

func NewPlugin() *RepeatPlugin {
	p := &RepeatPlugin{}
	p.Name = "复读机插件"
	p.HelpWords = "只会复读..., at我发送 /close(enable) repeat 可关闭(开启)复读机"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

func (p *RepeatPlugin) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {

	if cmd.Mention {
		if cmd.Name == "close" && len(cmd.Args) >= 1 && cmd.Args[0] == "repeat" {
			if p.SystemConfig.IsAdmin(msg.Sender.UserId) {
				p.enable.Store(false)
				builder := msgchain.Builder().Group()
				builder.Text("已关闭复读机")
				bot.SendGroupMsg(msg.GroupId, builder.Build())
				return false, nil
			} else {
				builder := msgchain.Builder().Group()
				builder.Text("你没有权限哦")
				bot.SendGroupMsg(msg.GroupId, builder.Build())
				return false, nil
			}
		} else if cmd.Name == "enable" && len(cmd.Args) >= 1 && cmd.Args[0] == "repeat" {
			if p.SystemConfig.IsAdmin(msg.Sender.UserId) {
				p.enable.Store(true)
				builder := msgchain.Builder().Group()
				builder.Text("已开启复读机")
				bot.SendGroupMsg(msg.GroupId, builder.Build())
				return false, nil
			} else {
				builder := msgchain.Builder().Group()
				builder.Text("你没有权限哦")
				bot.SendGroupMsg(msg.GroupId, builder.Build())
				return false, nil
			}
		}
	}

	if p.enable.Load() == false {
		return true, nil
	}
	// LoadOrStore 原子初始化：群内两条消息并发到达时，Load+Store 组合会各自
	// Store 一个独立计数器，丢失其一（复读计数错乱）
	val, loaded := p.repeatGMap.LoadOrStore(msg.GroupId, &repeatCount{
		msg:   msg.RawMessage,
		count: 1,
	})
	if !loaded {
		// 本消息创建的计数器已自带 count=1，无需再比较
		return true, nil
	}

	rc := val.(*repeatCount)
	needRepeat := false

	if msg.RawMessage == "" {
		// 纯图片/表情等消息 RawMessage 为空串，无法区分内容，
		// 若参与比较会把所有此类消息判为"相同"而误触发复读，直接忽略
		return true, nil
	}

	rc.lock.Lock()
	if rc.msg == msg.RawMessage {
		rc.count++
		if rc.count >= 3 && !rc.repeated {
			needRepeat = true
			rc.repeated = true
		}
	} else {
		rc.msg = msg.RawMessage
		rc.count = 1
		rc.repeated = false
	}
	rc.lock.Unlock()
	if needRepeat {
		builder := msgchain.Builder().Group()
		builder.Raw(msg.Message...)
		bot.SendGroupMsg(msg.GroupId, builder.Build())
	}
	return true, nil
}

func (p *RepeatPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	p.enable.Store(true)
	return nil
}
