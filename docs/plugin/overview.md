# 插件系统概览

AniaBot 中一切功能都是插件。本章介绍插件系统的核心概念，读完你将理解插件如何被加载、执行与管理。

## 插件是什么

一个插件就是一个嵌入了 `plugin.Meta` 的结构体：

```go
type MyPlugin struct {
    plugin.Meta
}

func NewPlugin() *MyPlugin {
    return &MyPlugin{
        Meta: plugin.Meta{
            Name:      "我的插件",
            HelpWords: "这是 /help 中显示的介绍",
            Order:     plugin.LevelNormal,
            ShowFor:   plugininfo.ShowForGroup | plugininfo.ShowForFriend,
            Author:    "you",
            Version:   "1.0.0",
        },
    }
}
```

`Meta` 已经提供了 `Plugin` 接口全部方法的默认实现 —— 你只需**按需重写**关心的方法。

## Meta 字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `Name` | `string` | 插件名，也用于存储命名空间隔离 |
| `HelpWords` | `string` | `/help` 命令显示的帮助文本 |
| `AdminOnly` | `bool` | 为 `true` 时仅管理员能在 `/help` 中看到 |
| `ShowFor` | `ShowFor` | 显示范围：`ShowForGroup` / `ShowForFriend` / `ShowForNone`，可按位或 |
| `Order` | `int` | 执行顺序，从小到大 |
| `Platforms` | `[]string` | 插件支持的平台列表（如 `[]string{"qq"}`、`[]string{"qq","feishu"}`、`[]string{"qq","feishu","telegram"}`）；**空 = 支持全部平台**（默认）。core 按事件来源平台过滤，不匹配的插件收不到该平台事件 |
| `Author` / `Version` | `string` | 作者与版本信息 |

## 执行顺序（Order）

框架启动时按 `Order` 从小到大排序插件。预定义三档参考值：

```go
plugin.LevelLog        = -1000  // 日志层：最先执行，如日志插件、消息拦截器
plugin.LevelNormal     = 0      // 普通业务插件（默认值）
plugin.LevelPostHandle = 1000   // 后置处理：最后执行，如 AI 对话兜底
```

## 中间件链

群聊与私聊消息事件返回 `(bool, error)`：

- 返回 `true` —— **继续**传播给后续插件
- 返回 `false` —— **阻断**传播，后续插件收不到这条消息

```go
func (p *MyPlugin) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if cmd.Name == "hello" {
        // 处理完毕，不再传递给其他插件
        return false, nil
    }
    return true, nil // 放行
}
```

::: warning 注意
**通知事件（Notice）不走中间件链** —— 全部 14 种通知会广播给所有插件，无法被阻断。某个插件 panic 也不会影响其他插件收到通知。
:::

## 生命周期

```
AddPlugin() 注册
    │
    ▼
Start(ctx, cfg)        ← 插件初始化（声明了 ConfigSchema 的配置结构体此时已自动填充完毕）
    │
    ▼
StartCron(ctx, bot, c) ← 注册 cron 定时任务
    │
    ▼
Awake(ctx, bot)        ← Bot 完全启动完成（此时可以发消息）
    │
    ▼
OnGroupMsg / OnFriendMsg / OnXxxNotice ...   ← 运行期事件
    │
    ▼
OnPanic(ctx, bot, name, err)  ← 任何插件 panic 时触发
    │
    ▼
OnUnload(ctx, reason)  ← 插件被卸载前触发一次（可选接口 UnloadEvent）
```

## 卸载钩子（可选）

插件可以实现 `plugin.UnloadEvent` 接口，在**被卸载前**执行清理（关闭连接、落盘数据、删除自身数据等）：

```go
type UnloadEvent interface {
    OnUnload(ctx context.Context, reason plugin.UnloadReason) error
}
```

`reason` 区分两种场景：

- `plugin.UnloadShutdown` —— Bot 退出或重启（`/exit`、`/reboot`、面板重启、自动更新），全部插件都会收到。适合落盘、释放资源；插件重启后会重新加载，**不要**在这里删除持久化数据
- `plugin.UnloadUninstall` —— 插件被插件市场卸载，仅被卸载的插件收到。适合清理插件自身的数据

钩子按插件**执行顺序（Order）的逆序**执行（后执行的先清理），每个插件最多触发一次；执行超时 1 分钟，出错或 panic 只记日志、不阻断其它插件。钩子可能与运行期事件并发，实现需保证并发安全。

## 依赖注入（DI）

在 `Start()` 之前，框架向每个插件注入以下依赖，直接通过 `Meta` 的字段使用：

| 字段 | 类型 | 用途 |
| --- | --- | --- |
| `Storage` | `storage.Storage` | 缓存层（TTL / 列表语义），已按插件名隔离 |
| `PersistentStorage` | `storage.PersistentStorage` | 持久化 KV 存储，已按插件名隔离 |
| `RestyClient` | `*resty.Client` | HTTP 客户端 |
| `Logger` | `*slog.Logger` | 结构化日志 |
| `SystemConfig` | `SystemConfig` | 系统级配置（如 `AdminId`） |

## 事件一览

| 类别 | 方法 | 说明 |
| --- | --- | --- |
| 消息 | `OnGroupMsg` / `OnFriendMsg` | 群聊 / 私聊消息，中间件链 |
| 通知 | `OnGroupUpload` `OnGroupAdmin` `OnGroupDecrease` `OnGroupIncrease` `OnGroupBan` `OnFriendAdd` `OnGroupRecall` `OnFriendRecall` `OnPoke` `OnLuckyKing` `OnHonor` `OnGroupMsgEmojiLike` `OnEssence` `OnGroupCard` | 14 种通知，广播制；戳一戳/运气王/荣誉/精华/名片/禁言/上传等为 QQ 专属，非 QQ 平台不触发 |
| 平台特定 | `OnPlatformEvent`（可选接口 `plugin.PlatformEventHandler`） | 无法映射为公共事件/通知的平台自有事件（如飞书卡片回调、机器人入群），广播制 |
| 交互 | `OnInteraction`（可选接口 `plugin.InteractionHandler`） | 内联按钮点击回调 |
| 启动 | `Start` / `StartCron` / `Awake` | 生命周期钩子 |
| 卸载 | `OnUnload`（可选接口 `plugin.UnloadEvent`） | 退出/重启或市场卸载前的清理钩子 |
| 异常 | `OnPanic` | panic 通知 |

事件字段详见 [API · 事件接口](/api/events)。

## 多平台能力探测

事件回调里的 `bot.Bot` 是**事件来源平台能力包装**后的外观。公共能力（发消息、查消息/群/历史）所有平台可用；QQ 专属能力（合并转发、戳一戳、rkey 等）在可选接口 `bot.QQ` 中，类型断言探测：

```go
func (p *MyPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if qb, ok := b.(bot.QQ); ok { // 仅事件来源为 QQ 时断言成功
        qb.SendPokeMsg(msg.Sender.UserId, nil)
    }
    return true, nil
}
```

依赖 QQ 专属能力的插件应在 `Meta.Platforms` 声明只支持 QQ（如防撤回插件），其余平台自动跳过。

## 并发模型与 panic 恢复

- 每个插件的每次调用都被 `safeExecute` 包裹 —— 单个插件 panic **不会**影响其他插件或主进程
- 插件内需要起协程时，使用 `bot.Go(name, f)` 代替裸 `go f()`，同样享有崩溃恢复
- panic 发生后，所有插件的 `OnPanic` 会被回调，系统插件默认私聊告警管理员

## 注册插件

在 `cmd/main.go` 中（平台适配器由各平台包的 `init()` 自动注册，空白导入即可）：

```go
import (
    _ "github.com/jeanhua/AniaBot/bot/adapter/napcat"    // QQ 平台
    _ "github.com/jeanhua/AniaBot/bot/adapter/feishu"    // 飞书平台（可选）
    _ "github.com/jeanhua/AniaBot/bot/adapter/telegram"  // Telegram 平台（可选）
    _ "github.com/jeanhua/AniaBot/bot/adapter/discord"   // Discord 平台（可选）
    _ "github.com/jeanhua/AniaBot/bot/adapter/weixin"    // 微信平台（可选）
    "github.com/jeanhua/AniaBot/bot/core"
    "github.com/jeanhua/AniaBot/bot/plugins/pluginsys"
    "github.com/jeanhua/AniaBot/custom/plugins/myplugin"
)

func main() {
    bot := core.NewAniaBot()

    bot.AddPlugin(pluginsys.NewPluginSys())
    bot.AddPlugin(myplugin.NewPlugin())   // ← 你的插件

    bot.Run()
}
```

启用的平台由配置键 `bot.platform.<name>.enable` 决定（默认仅 QQ），多平台可并存。新增平台只需在 `bot/adapter/` 实现一个 `Adapter` 并在 `init()` 里 `adapter.Register(...)`，然后在此加一行空白导入，框架核心零改动。

## 下一步

- [第一个插件](/plugin/first-plugin) —— 动手写代码
- [从零开发完整插件](/plugin/tutorial) —— 配置、存储、定时任务与面板服务全流程
- [面板服务注册](/plugin/panel-services) —— 让你的插件成为面板页面数据源
- [完整示例](/plugin/examples) —— 看几个真实插件的完整实现

## 发布到插件市场

想把自己写的插件分享给所有用户？AniaBot 提供独立插件市场仓库 [AniaBot-Plugins](https://github.com/AniaBot-Project/AniaBot-Plugins)：

- 把插件源码 + `plugin.json` 元信息 + `README.md` 放进 `plugins/<id>/` 目录，提交 Pull Request 即可
- 规范见[插件规范](https://github.com/AniaBot-Project/AniaBot-Plugins/blob/main/docs/plugin-spec.md)，提交要求见[贡献指南](https://github.com/AniaBot-Project/AniaBot-Plugins/blob/main/CONTRIBUTING.md)
- 合并后即可在面板「插件市场」被所有用户在线安装；CI 会自动同步插件索引，无需手动维护

