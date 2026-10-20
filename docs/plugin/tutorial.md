# 从零开发一个完整插件（详细教程）

本教程带你完整开发一个 **「每日早报」插件**：管理员在群里设置早报内容，机器人每天定时推送到指定群，并在 Web 面板「任务日志」页展示每次推送记录。

它综合运用了插件开发的**全部核心能力**：

| 能力 | 本章节 |
| --- | --- |
| 插件骨架与元信息 | [第 1 步](#第-1-步插件骨架与元信息) |
| 群消息命令与权限控制 | [第 2 步](#第-2-步群命令与权限控制) |
| 配置结构体（面板自动渲染表单） | [第 3 步](#第-3-步声明配置-configschema) |
| 持久化存储（KV，按群隔离） | [第 4 步](#第-4-步持久化存储) |
| 定时任务（StartCron） | [第 5 步](#第-5-步定时任务) |
| **面板服务注册**（任务日志页数据源） | [第 6 步](#第-6-步注册面板服务) |
| 注册运行与调试 | [第 7 步](#第-7-步注册运行与调试) |

## 目录结构

在 `custom/plugins/` 下新建：

```
custom/plugins/plugindailybrief/
└── plugin.go
```

## 第 1 步：插件骨架与元信息

```go
package plugindailybrief

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/jeanhua/AniaBot/common/storage"
	"github.com/spf13/viper"
)

type DailyBriefPlugin struct {
	plugin.Meta
	cfg   dailyBriefConfig // 配置结构体，框架在 Start 前自动填充
	brief *briefStore      // 持久化存储封装
	log   *tasklog.Logger  // 执行日志（面板「任务日志」页数据源）
}

func NewPlugin() *DailyBriefPlugin {
	return &DailyBriefPlugin{
		Meta: plugin.Meta{
			Name:      "每日早报插件",
			HelpWords: "@我发送 /brief 查看早报，/brief 内容 设置早报（仅管理员）",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
			Author:    "you",
			Version:   "1.0.0",
			// Platforms 留空 = 支持全部平台（QQ / 飞书 / Telegram / Discord / 微信）
		},
	}
}
```

要点：

- 嵌入 `plugin.Meta` 后所有事件方法都有默认实现，只需重写关心的
- `Order: plugin.LevelNormal`（0）表示普通业务层；`ShowFor: plugininfo.ShowForGroup` 让插件只出现在群聊 `/help`
- `Platforms` 留空 = 全部平台；只写 `[]string{"qq"}` 则仅 QQ 收到事件

## 第 2 步：群命令与权限控制

```go
func (p *DailyBriefPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !cmd.Mention || cmd.Name != "brief" {
		return true, nil // 不是我们的命令，放行
	}

	groupID := msg.GroupId.String()

	// /brief：查看当前早报
	if len(cmd.Args) == 0 {
		content := p.brief.Get(ctx, groupID)
		if content == "" {
			content = "还没有设置早报内容，管理员发送 @机器人 /brief 内容 来设置吧~"
		}
		p.reply(b, msg, content)
		return false, nil // 已处理，阻断后续插件（如 AI 对话）
	}

	// /brief 内容：仅管理员可设置
	if !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
		p.reply(b, msg, "只有管理员才能设置早报内容哦")
		return false, nil
	}

	content := strings.Join(cmd.Args, " ")
	p.brief.Set(ctx, groupID, content)
	p.Logger.Info("已更新早报内容", "group", groupID, "content", content)
	p.reply(b, msg, "早报已更新 ✅")
	return false, nil
}

func (p *DailyBriefPlugin) reply(b bot.Bot, msg message.Message, text string) {
	chain := msgchain.Builder().Group()
	chain.Mention(msg.Sender.UserId)
	chain.Text(" " + text)
	b.SendGroupMsg(msg.GroupId, chain.Build())
}
```

要点：

- `cmd` 由框架预解析：`/brief 今天有雨` → `cmd.Name = "brief"`, `cmd.Args = ["今天","有雨"]`
- `p.SystemConfig.IsAdmin(msg.Sender.UserId)` 判断发送者是否为管理员（`bot.admin_ids` 可配置多个，ID 可能带平台前缀），用于权限控制；群管理员判断用 `msg.Sender.Role`
- 返回 `false` 阻断传播——消息不会继续走到 AI 对话插件
- `p.Logger` 是带插件名 group 的结构化日志，正式插件应使用它而不是 `fmt.Println`

## 第 3 步：声明配置（ConfigSchema）

定义带 `cfg` 标签的配置结构体，实现 `ConfigSchemaProvider`，框架会自动完成：**面板渲染表单 → 补齐默认值 → Start 前填充结构体**。

```go
type dailyBriefConfig struct {
	Enable bool     `cfg:"plugin.daily_brief.enable" label:"启用每日早报" group:"每日早报" default:"true"`
	Cron   string   `cfg:"plugin.daily_brief.cron" label:"推送时间(cron)" group:"每日早报" default:"30 8 * * *"`
	Groups []string `cfg:"plugin.daily_brief.groups" label:"推送群 ID" group:"每日早报" help:"每行一个，QQ 为 qq:群号，其他平台带前缀（如 fs:oc_xxx）"`
}

// ConfigSchema 返回配置结构体指针（框架在依赖注入前调用，必须每次返回同一指针）
func (p *DailyBriefPlugin) ConfigSchema() any {
	return &p.cfg
}
```

完整标签说明见 [第一个插件 · 声明自己的配置](/plugin/first-plugin#进阶-声明自己的配置)。面板「配置管理」页会自动出现「每日早报」分组。

## 第 4 步：持久化存储

早报内容要跨重启保留，放在**持久层**。框架注入的 `p.PersistentStorage` 已按插件名隔离命名空间，我们再 `Clone` 一层分类：

```go
type briefStore struct {
	store storage.PersistentStorage
}

func newBriefStore(root storage.PersistentStorage) *briefStore {
	return &briefStore{store: root.Clone("brief:")}
}

// Set 保存指定群的早报内容（群 ID 可能带平台前缀，按群天然隔离）
func (s *briefStore) Set(ctx context.Context, groupID, content string) {
	_ = s.store.Set(ctx, "content:"+groupID, content)
}

func (s *briefStore) Get(ctx context.Context, groupID string) string {
	var content string
	if !s.store.Get(ctx, "content:"+groupID, &content) {
		return ""
	}
	return content
}
```

在 `Start` 中初始化（依赖注入完成后）：

```go
func (p *DailyBriefPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	p.brief = newBriefStore(p.PersistentStorage)
	// 执行日志：面板「任务日志」页的数据源（见第 6 步）
	p.log = tasklog.New(p.PersistentStorage.Clone("brieflog:"), 100, p.Logger.WithGroup("brieflog"))
	p.Logger.Info("每日早报插件初始化完成", "cron", p.cfg.Cron, "groups", p.cfg.Groups)
	return nil
}
```

更多存储细节（TTL、列表、SQL 关系表）见 [数据存储](/plugin/storage)。

## 第 5 步：定时任务

在 `StartCron` 钩子中注册 cron 任务，到点后通过 `bot.Go` 启动安全协程执行：

```go
func (p *DailyBriefPlugin) StartCron(ctx context.Context, b bot.Bot, c plugin.CronManager) error {
	_, err := c.AddFunc(p.cfg.Cron, func() {
		// bot.Go：panic 自动恢复并通知所有插件 OnPanic，不要用裸 go
		b.Go("daily-brief", func() { p.pushAll(b) })
	})
	if err != nil {
		return fmt.Errorf("注册每日早报定时任务失败: %w", err)
	}
	return nil
}

func (p *DailyBriefPlugin) pushAll(b bot.Bot) {
	if !p.cfg.Enable {
		return
	}
	for _, g := range p.cfg.Groups {
		p.push(b, message.FromString(g))
	}
}

func (p *DailyBriefPlugin) push(b bot.Bot, groupID message.QID) {
	content := p.brief.Get(context.Background(), groupID.String())
	if content == "" {
		content = "早上好！今天是新的一天，愿你元气满满 ☀️"
	}
	chain := msgchain.Builder().Group()
	chain.Text("📰 每日早报\n" + content)

	start := time.Now()
	entry := p.log.Record(tasklog.Entry{
		TaskID:         "daily-brief",
		TaskTitle:      "每日早报推送",
		TargetType:     "group",
		TargetID:       groupID.String(),
		TriggerTime:    start,
		TriggerContent: tasklog.Truncate(content, tasklog.MaxContentRunes),
		Status:         tasklog.StatusRunning,
	})
	_, ok := b.SendGroupMsg(groupID, chain.Build())
	status := tasklog.StatusSuccess
	errMsg := ""
	if !ok {
		status = tasklog.StatusError
		errMsg = "消息发送失败"
	}
	p.log.Update(entry.ID, func(e *tasklog.Entry) {
		e.Status = status
		e.Error = errMsg
		e.DurationMs = time.Since(start).Milliseconds()
		e.Reply = tasklog.Truncate(content, tasklog.MaxReplyRunes)
		e.FinishedAt = time.Now()
	})
}
```

要点：

- `p.cfg.Cron` 是配置里的 cron 表达式（默认每天 08:30），启动时读取，改配置重启后生效
- 配置的群 ID 是多平台统一 ID（QQ 为 `qq:群号`、飞书 `fs:oc_xxx`、Telegram 带前缀），`message.FromString` 直接构造 `QID`，core 会自动路由到对应平台适配器
- `SendGroupMsg` 返回 `(msgId, success)`，失败只返回 false 不会 panic——推送失败要自行处理（这里记入任务日志）
- `tasklog.Logger` 负责执行日志的持久化与容量淘汰（这里保留 100 条）

## 第 6 步：注册面板服务

这是本教程的关键一步：让 Web 面板「任务日志」页展示本插件的推送记录。

实现 `adminpanel.TaskLogSource` 接口（一个方法）：

```go
// TaskLogQuery 按条件查询本插件的推送日志（实现 adminpanel.TaskLogSource）
func (p *DailyBriefPlugin) TaskLogQuery(f tasklog.Filter) []tasklog.Entry {
	if p.log == nil {
		return nil
	}
	return p.log.Query(f)
}
```

就这么简单——core 启动面板时会遍历插件、类型断言发现实现了 `TaskLogSource` 的插件，自动把「任务日志」页接到 `p.log` 上，面板零改动。

::: warning 单提供者约定
面板服务是**单提供者**：同一接口只会接一个插件，且内置的 AI 对话插件已经实现了 `TaskLogSource`（它与日志插件一起集齐了全部 9 个面板接口，导致之后注册的自定义插件不会被接线）。要让本插件的日志出现在「任务日志」页，需要在 `cmd/main.go` **移除 `pluginaichat.NewAIChatPlugin()`** 的注册（详见 [插件注册面板服务](/plugin/panel-services)）。
:::

## 第 7 步：注册运行与调试

在 `cmd/main.go` 注册插件（注意面板服务接管规则，见上一步警告）：

```go
import (
	_ "github.com/jeanhua/AniaBot/bot/adapter/napcat" // 平台适配器
	"github.com/jeanhua/AniaBot/bot/core"
	"github.com/jeanhua/AniaBot/custom/plugins/plugindailybrief"
)

func main() {
	bot := core.NewAniaBot()
	bot.AddPlugin(pluginsys.NewPluginSys())
	// ... 其余内置插件（按需保留）
	bot.AddPlugin(plugindailybrief.NewPlugin()) // ← 你的插件
	bot.Run()
}
```

```bash
go run ./cmd
```

调试技巧：

- 启动后私聊发送 `/help`，确认插件已注册
- 群内 `@机器人 /brief 今天降温，记得加衣` 设置早报；`@机器人 /brief` 查看
- 日志输出带插件名 group：`slog` 过滤 `plugindailybrief` 关键词即可只看本插件日志
- 面板「配置管理」→「每日早报」分组改群列表 / cron / 开关，重启生效；若接管了任务日志页，推送后可在「任务日志」页看到每次推送的状态、耗时与内容
- 想让定时任务立刻触发验证：临时把 `cron` 配成 `*/1 * * * *`（每分钟）跑一次，确认后再改回

## 完整代码

最终 `custom/plugins/plugindailybrief/plugin.go` 就是把上面各步骤的代码拼在一起（去掉重复的 import 与结构体定义）。项目内的完整参考实现：

- 内置插件：`bot/plugins/` 下六个插件（AI 对话、复读机、新闻……）
- 简单示例与模板：`custom/mvp/example.go`、`custom/plugins/pluginexample/plugin.go`

## 下一步

- [插件注册面板服务](/plugin/panel-services) —— 全部 9 个面板接口与接入规则
- [常见模式](/plugin/patterns) —— 管理员权限、并发安全、消息拦截等实战模式
- [数据存储](/plugin/storage) —— SQL 关系表、命名空间等进阶用法
- [API 参考](/api/events) —— 全部事件签名

