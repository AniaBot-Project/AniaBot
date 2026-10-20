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

// 触发条数的取值范围：至少 2 条相同消息才谈得上「重复」，配置更小时
// 按最小有效值处理；配置键缺失时回落到默认值。
const (
	defaultTriggerCount = 3
	minTriggerCount     = 2
)

// 运行开关的配置键：/close repeat、/enable repeat 命令会把状态写回这里，
// 保证面板展示与重启后的实际行为一致。
const enableConfigKey = "plugin.repeat.enable"

type RepeatPlugin struct {
	plugin.Meta
	// cfg 插件配置，由框架在 Start 前自动填充（见 ConfigSchema）
	cfg        repeatConfig
	repeatGMap sync.Map
	enable     atomic.Bool
	// trigger 触发复读所需的连续相同消息条数（原子读，Start 写入后不再变更）
	trigger atomic.Int64
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
	p.HelpWords = "只会复读..., at我发送 /close(enable) repeat 可关闭(开启)复读机，触发条数与开关可在控制面板配置"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup
	p.Author = "jeanhua"
	p.Version = "1.1.0"
	p.Order = plugin.LevelNormal
	return p
}

func (p *RepeatPlugin) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {

	if cmd.Mention {
		if cmd.Name == "close" && len(cmd.Args) >= 1 && cmd.Args[0] == "repeat" {
			return p.handleToggle(bot, msg, false)
		} else if cmd.Name == "enable" && len(cmd.Args) >= 1 && cmd.Args[0] == "repeat" {
			return p.handleToggle(bot, msg, true)
		}
	}

	if !p.enable.Load() {
		return true, nil
	}
	if p.observe(msg.GroupId, msg.RawMessage) {
		builder := msgchain.Builder().Group()
		builder.Raw(msg.Message...)
		bot.SendGroupMsg(msg.GroupId, builder.Build())
	}
	return true, nil
}

// handleToggle 处理 /close repeat、/enable repeat 命令：仅管理员可操作，
// 开关同时写回配置中心，重启后保持同一状态。
func (p *RepeatPlugin) handleToggle(b bot.Bot, msg message.Message, on bool) (bool, error) {
	builder := msgchain.Builder().Group()
	if !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
		builder.Text("你没有权限哦")
		b.SendGroupMsg(msg.GroupId, builder.Build())
		return false, nil
	}
	p.setEnable(on)
	if on {
		builder.Text("已开启复读机")
	} else {
		builder.Text("已关闭复读机")
	}
	b.SendGroupMsg(msg.GroupId, builder.Build())
	return false, nil
}

// setEnable 更新运行时开关；配置中心可用时同步写回 plugin.repeat.enable，
// 不可用时只对本次运行生效。
func (p *RepeatPlugin) setEnable(on bool) {
	p.enable.Store(on)
	if p.ConfigEditor == nil {
		return
	}
	if err := p.ConfigEditor.Set(enableConfigKey, on); err != nil {
		p.Logger.Warn("复读机开关回写配置失败", "enable", on, "error", err)
		return
	}
	p.cfg.Enable = on
}

// observe 记录一条群消息并返回是否需要复读。
// 同一群内连续出现 trigger 条相同消息时触发一次，之后需要消息变化才会再次触发。
func (p *RepeatPlugin) observe(groupId message.QID, raw string) bool {
	if raw == "" {
		// 纯图片/表情等消息 RawMessage 为空串，无法区分内容，
		// 若参与比较会把所有此类消息判为"相同"而误触发复读，直接忽略
		return false
	}
	// LoadOrStore 原子初始化：群内两条消息并发到达时，Load+Store 组合会各自
	// Store 一个独立计数器，丢失其一（复读计数错乱）
	val, loaded := p.repeatGMap.LoadOrStore(groupId, &repeatCount{
		msg:   raw,
		count: 1,
	})
	if !loaded {
		// 本消息创建的计数器已自带 count=1，无需再比较
		return false
	}

	rc := val.(*repeatCount)
	trigger := p.triggerThreshold()
	needRepeat := false
	rc.lock.Lock()
	if rc.msg == raw {
		rc.count++
		if rc.count >= trigger && !rc.repeated {
			needRepeat = true
			rc.repeated = true
		}
	} else {
		rc.msg = raw
		rc.count = 1
		rc.repeated = false
	}
	rc.lock.Unlock()
	return needRepeat
}

// triggerThreshold 返回当前生效的触发条数，配置越界时回落到最小有效值。
func (p *RepeatPlugin) triggerThreshold() int {
	if n := int(p.trigger.Load()); n >= minTriggerCount {
		return n
	}
	return minTriggerCount
}

func (p *RepeatPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	// 配置键缺失（如配置中心初始化异常）时保持历史默认行为：开启 + 3 条触发
	enable := p.cfg.Enable
	trigger := p.cfg.TriggerCount
	if cfg == nil || !cfg.IsSet(enableConfigKey) {
		enable = true
	}
	if cfg == nil || !cfg.IsSet("plugin.repeat.trigger_count") {
		trigger = defaultTriggerCount
	}
	if trigger < minTriggerCount {
		p.Logger.Warn("复读触发条数配置非法，已按最小有效值处理",
			"configured", trigger, "effective", minTriggerCount)
		trigger = minTriggerCount
	}
	p.trigger.Store(int64(trigger))
	p.enable.Store(enable)
	p.Logger.Info("复读机插件初始化完成", "enable", enable, "trigger_count", trigger)
	return nil
}
