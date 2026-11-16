# 定时任务

框架内置基于 [robfig/cron/v3](https://github.com/robfig/cron) 的调度器，插件在 `StartCron` 钩子中注册任务。

## 基本用法

重写 `StartCron`，通过注入的 `CronManager` 添加任务：

```go
func (p *NewsPlugin) StartCron(ctx context.Context, b bot.Bot, c plugin.CronManager) error {
	_, err := c.AddFunc("0 18 * * *", func() {
		// 每天 18:00 执行
		p.sendNews(b)
	})
	return err
}
```

`AddFunc(spec, cmd)` 返回 `(cron.EntryID, error)`。`EntryID` 可以记录到日志或用于排查，但 `CronManager` 只暴露 `AddFunc`，**没有移除方法**；任务随 Bot 进程生命周期存在，退出/重启时 cron 整体停止，插件被市场卸载也会在重启后消失。

`StartCron` 在全部插件 `Start` 之后执行，单插件超时 1 分钟，按插件 `Order` 串行调用。

## cron 表达式

标准 5 段格式：`分 时 日 月 周`

| 表达式 | 含义 |
| --- | --- |
| `0 18 * * *` | 每天 18:00 |
| `*/5 * * * *` | 每 5 分钟 |
| `0 9 * * 1-5` | 工作日 9:00 |
| `0 0 1 * *` | 每月 1 号 0:00 |

非法表达式会让 `AddFunc` 返回 error；建议记录日志并返回，不要忽略。

## 完整示例：整点报时插件

```go
package pluginhourly

import (
	"context"
	"fmt"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

type hourlyConfig struct {
	// 多平台下群 ID 不一定是数字（如飞书 fs:oc_xxx），用 []string 承载
	Groups []string `cfg:"plugin.hourly.groups" label:"报时群 ID 列表" group:"整点报时" help:"每行一个群 ID（NapCat 为 qq:群号，Luckylilia 为 lil:群号，其他平台带前缀）"`
}

type HourlyPlugin struct {
	plugin.Meta
	cfg hourlyConfig // 由框架在 Start 前自动填充
}

func NewPlugin() *HourlyPlugin {
	return &HourlyPlugin{
		Meta: plugin.Meta{
			Name:      "整点报时插件",
			HelpWords: "整点在群里报时",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
		},
	}
}

// ConfigSchema 声明配置结构体，框架自动注册面板字段并填充
func (p *HourlyPlugin) ConfigSchema() any { return &p.cfg }

// StartCron 注册定时任务
func (p *HourlyPlugin) StartCron(ctx context.Context, b bot.Bot, c plugin.CronManager) error {
	_, err := c.AddFunc("0 * * * *", func() {
		// cron 回调不在框架的 panic 恢复里，用 b.Go 执行实际逻辑；
		// 否则回调 panic 会直接拖垮进程。
		b.Go("hourly-broadcast", func() {
			text := fmt.Sprintf("⏰ 现在是 %s", time.Now().Format("15:04"))
			for _, g := range p.cfg.Groups {
				chain := msgchain.Builder().Group()
				chain.Text(text)
				if _, ok := b.SendGroupMsg(message.FromString(g), chain.Build()); !ok {
					p.Logger.Error("报时发送失败", "group", g)
				}
			}
		})
	})
	return err
}
```

配置项（`plugin.hourly.groups`）会在 Web 控制面板「配置管理 → 整点报时」分组中自动出现，可视化编辑，改完重启生效。

## 注意事项

::: warning 任务函数中的 panic
cron 回调由 cron 自己的 goroutine 执行，**没有** `safeExecute` 的 panic 恢复。任务内必须用 `b.Go` 包裹实际工作，或自行 `recover`；发送消息等操作建议检查返回值并记录日志。
:::

::: tip 静态 vs 动态任务
`StartCron` 适合**代码中预先定义好**的静态任务。如果你需要**用户/AI 在运行时动态创建**的任务，AI 对话插件的 clock 系统已实现完整方案（持久化 + 命令管理），可直接参考 `bot/plugins/pluginaichat/clock.go`，或在自己的插件中基于 `PersistentStorage` + cron 实现类似机制。
:::

## 相关

- [插件生命周期与依赖注入](/plugin/lifecycle) —— `StartCron` 在生命周期中的位置与超时
- [插件配置](/plugin/configuration) —— 把 cron 表达式做成可配置项
- [后台推送模式](/plugin/patterns) —— 定时任务 + 目标群列表的写法
