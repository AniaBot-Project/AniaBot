# 完整示例

三个由浅入深的完整插件实现，全部可直接编译运行。它们综合运用了事件响应、消息构造、存储与通知事件。

## 示例一：关键词回复插件

被动监听群消息（不需要 @），命中关键词就回复。

```go
package pluginkeyword

import (
	"context"
	"strings"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

type KeywordPlugin struct {
	plugin.Meta
}

func NewPlugin() *KeywordPlugin {
	return &KeywordPlugin{
		Meta: plugin.Meta{
			Name:      "关键词回复插件",
			HelpWords: "说「早上好」我会回应你哦",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
		},
	}
}

func (p *KeywordPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	var reply string
	switch {
	case strings.Contains(msg.RawMessage, "早上好"):
		reply = "早上好呀 ☀️"
	case strings.Contains(msg.RawMessage, "晚安"):
		reply = "晚安，做个好梦 🌙"
	default:
		return true, nil // 未命中，放行
	}

	chain := msgchain.Builder().Group()
	chain.Mention(msg.Sender.UserId)
	chain.Text(" " + reply)
	b.SendGroupMsg(msg.GroupId, chain.Build())

	// 返回 true 继续放行：聊天类回复不该阻断后续插件（如 AI 对话）
	return true, nil
}
```

**要点**：`msg.RawMessage` 是消息纯文本；轻量回应后返回 `true` 让消息继续传播。

## 示例二：签到插件

综合运用命令解析、持久化存储与每日 TTL 缓存。

```go
package pluginsignin

import (
	"context"
	"fmt"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/jeanhua/AniaBot/common/storage"
)

type SignInPlugin struct {
	plugin.Meta
}

func NewPlugin() *SignInPlugin {
	return &SignInPlugin{
		Meta: plugin.Meta{
			Name:      "签到插件",
			HelpWords: "@我发送 /sign 每日签到，/sign rank 查看我的积分",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
		},
	}
}

func (p *SignInPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !cmd.Mention || cmd.Name != "sign" {
		return true, nil
	}

	uid := msg.Sender.UserId.String()
	today := time.Now().Format("2006-01-02")

	if len(cmd.Args) > 0 && cmd.Args[0] == "rank" {
		p.replyRank(ctx, b, msg, uid)
		return false, nil
	}

	// 缓存层：当天签到标记，TTL 到期自然清除
	signKey := "signed:" + today
	if _, signed := p.Storage.GetString(ctx, signKey+":"+uid); signed {
		p.reply(ctx, b, msg, "今天已经签过到了，明天再来吧~")
		return false, nil
	}

	// 持久层：累计积分（按用户隔离命名空间）
	scores := p.PersistentStorage.Clone("score")
	var score int
	scores.Get(ctx, uid, &score)
	score += 10
	scores.Set(ctx, uid, score)

	p.Storage.SetString(ctx, signKey+":"+uid, "1", storage.WithTTL(48*time.Hour))
	p.reply(ctx, b, msg, fmt.Sprintf("签到成功！积分 +10，当前累计 %d 分 🎉", score))
	return false, nil
}

func (p *SignInPlugin) replyRank(ctx context.Context, b bot.Bot, msg message.Message, uid string) {
	scores := p.PersistentStorage.Clone("score")
	var score int
	scores.Get(ctx, uid, &score)
	p.reply(ctx, b, msg, fmt.Sprintf("你当前的积分是 %d 分", score))
}

func (p *SignInPlugin) reply(ctx context.Context, b bot.Bot, msg message.Message, text string) {
	chain := msgchain.Builder().Group()
	chain.Mention(msg.Sender.UserId)
	chain.Text(" " + text)
	if _, ok := b.SendGroupMsg(msg.GroupId, chain.Build()); !ok {
		p.Logger.Error("签到回复发送失败", "group", msg.GroupId)
	}
}
```

**要点**：

- 缓存层（Redis/内存）负责「今天是否已签到」—— TTL 到期自动清除
- 持久层（SQLite/MySQL）负责积分 —— 重启不丢
- `Clone("score")` 在插件命名空间内再做子隔离

## 示例三：新人欢迎插件

使用**通知事件**而非消息事件 —— 群成员增加时自动欢迎。

```go
package pluginwelcome

import (
	"context"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

type WelcomePlugin struct {
	plugin.Meta
}

func NewPlugin() *WelcomePlugin {
	return &WelcomePlugin{
		Meta: plugin.Meta{
			Name:      "新人欢迎插件",
			HelpWords: "自动欢迎新入群的成员",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
		},
	}
}

// OnGroupIncrease 群成员增加通知（广播制，所有插件都会收到）
func (p *WelcomePlugin) OnGroupIncrease(ctx context.Context, b bot.Bot, notice message.GroupIncreaseNotice) error {
	chain := msgchain.Builder().Group()
	chain.Mention(notice.UserId)
	chain.Text(" 欢迎新同学加入！🎉 发送 @我 /help 可以查看我会的技能哦")

	if _, ok := b.SendGroupMsg(notice.GroupId, chain.Build()); !ok {
		p.Logger.Error("欢迎消息发送失败", "group", notice.GroupId, "user", notice.UserId)
	}
	return nil
}

// OnGroupDecrease 成员离开时也可以做点什么
func (p *WelcomePlugin) OnGroupDecrease(ctx context.Context, b bot.Bot, notice message.GroupDecreaseNotice) error {
	p.Logger.Info("成员离开",
		"group", notice.GroupId,
		"user", notice.UserId,
		"subType", notice.SubType, // leave / kick / kick_me
	)
	return nil
}
```

**要点**：

- 通知事件共 14 种，全部广播给所有插件，返回 `error` 不影响其他插件
- `notice.SubType` 区分入群方式：`approve`（审批）/ `invite`（邀请）

## 更多真实示例

- 内置插件源码：`bot/plugins/` 下的六个插件（系统、日志、请求拦截、复读机、AI 对话、每日新闻）都是优秀的学习样本；官方插件仓库（AniaBot-Plugins）里的插件（如防撤回、白名单管理）同样是很好的参考
- 本仓库 `custom/plugins/` 还内置了一批可直接运行的完整插件：`checkin`（签到 + 持久化 + 排行）、`eew`（WebSocket/HTTP 轮询 + 定时推送）、`groupdigest`（后台任务 + AI 调用）、`whitelist`（拦截器 + 管理员鉴权）、`antiwithdrawal`（QQ 合并转发 + rkey 续期）
- 自定义插件模板：仓库 `custom/plugins/example`（带 `plugin.json` 的市场骨架）、`custom/plugins/pluginexample`（注释版骨架）与 `custom/mvp`（最小可运行 main）

## 相关

- [插件配置](/plugin/configuration) —— 给示例插件加上面板可配置项
- [插件生命周期与依赖注入](/plugin/lifecycle) —— 后台任务、卸载与测试
- [平台能力与优雅降级](/plugin/platforms) —— 让示例适配多平台
- [常见模式](/plugin/patterns) —— 权限、限流、拦截、资源过期等实战写法
