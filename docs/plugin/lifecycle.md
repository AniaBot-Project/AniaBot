# 插件生命周期、依赖注入与卸载

一个插件从注册到卸载会经历若干固定阶段。理解这些阶段的顺序与超时，能避免「配置还没填就读」「Start 里拿不到别的插件数据」「卸载后又把数据写回去」之类的坑。

## 生命周期总览

```
AddPlugin()
  │  检查 ShowFor 非零、Meta.Name 全局唯一；按 Order 稳定排序
  ▼
ConfigSchema() / ConfigFields()      ← 注册配置字段（依赖注入之前）
  │
  ▼
配置中心初始化 / 补齐默认值 / 构建 viper
  │
  ▼
pluginconfig.Load()                  ← 把配置填充进声明的结构体
  │
  ▼
依赖注入（Storage / PersistentStorage / RestyClient / Logger / SystemConfig / ConfigEditor）
  │
  ▼
Start(ctx, cfg)                      ← 初始化，单插件超时 1 分钟
  │
  ▼
StartCron(ctx, bot, c)               ← 注册定时任务，单插件超时 1 分钟
  │
  ▼
cron.Start()
  │
  ▼
Awake(ctx, bot)                      ← 启动 1 秒后；首次设置向导未完成时跳过
  │
  ▼
运行期事件（消息 / 通知 / 平台事件 / 按钮回调 / 发送事件）
  │
  ▼
Stop() / 重启 → OnUnload(ctx, reason) ← 可选接口，逆序执行，每个插件最多一次
```

::: tip 顺序要点
`ConfigSchema` 早于依赖注入，`Start` 早于 `StartCron`，全部插件的 `Start` 完成后才会执行 `Awake`。因此：

- 配置字段注册时不能使用 `p.Logger` 等注入字段；
- `Start` 里可以读自己的配置和注入依赖，但读别的插件的数据可能为空（对方还没 `Start` 或尚未就绪）；
- 依赖其他插件初始化结果的逻辑放到 `Awake`（例如白名单插件在 `Awake` 读取拦截器配置）。
:::

## 注册：AddPlugin

```go
bot := core.NewAniaBot()
bot.AddPlugin(pluginsys.NewPluginSys())
bot.AddPlugin(myplugin.NewPlugin())
bot.Run()
```

`AddPlugin` 会立即做两项校验：

- `Meta.ShowFor` 是显示范围位掩码（`ShowForGroup=2` / `ShowForFriend=4` / `ShowForNone=8`），应显式设置。零值虽然不会触发 `AddPlugin` 的校验，但插件不会出现在 `/help`（相当于隐藏）；拦截器类插件显式用 `ShowForNone` 更清晰；
- `Meta.Name` 全局唯一，重复加载会 panic。`Name` 同时用于存储命名空间隔离。

注册顺序不重要，框架会按 `Meta.Order` 从小到大稳定排序；`Order` 相同的插件保持注册顺序。

## Start：初始化

```go
func (p *MyPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	p.store = p.PersistentStorage.Clone("myplugin:")
	p.http = p.RestyClient
	// 注意：Start 的 ctx 只用于本次调用，Start 返回后框架会 cancel。
	// 需要常驻的后台任务要自己创建 context，并在 OnUnload 里取消。
	p.bgCtx, p.bgCancel = context.WithCancel(context.Background())
	go p.loop(p.bgCtx)
	return nil
}
```

- `ctx` 带有 1 分钟超时，且 `Start` 返回后会被 cancel；**不能**把这个 `ctx` 传给常驻 goroutine。后台任务用自建 context，在 `OnUnload` 中 `cancel()`；
- 不要在 `Start` 里做长时间阻塞操作（下载大文件、等待网络连接、轮询外部服务）。需要常驻的任务放到后台 goroutine；
- **返回非 nil 错误不会阻止插件加载**：框架只记录错误日志，插件依然会收到后续事件。若初始化失败后不希望响应事件，需要自己置一个 `disabled` / `ready` 标志并在事件入口判断；
- `Start` 按 `Order` 顺序**串行**执行，某个插件阻塞会拖慢后续插件启动；
- `p.PersistentStorage` 始终可用；`p.ConfigEditor` 可能为 `nil`，使用前判空。

## StartCron：定时任务

```go
func (p *MyPlugin) StartCron(ctx context.Context, b bot.Bot, c plugin.CronManager) error {
	_, err := c.AddFunc("0 8 * * *", func() {
		// cron 回调不在插件安全执行器里，panic 会拖垮进程；
		// 用 b.Go 启动受管协程，崩溃会自动恢复并通知 OnPanic。
		b.Go("myplugin-daily", func() { p.daily(b) })
	})
	return err
}
```

- `CronManager` 只暴露 `AddFunc(spec string, cmd func()) (cron.EntryID, error)`，**没有移除方法**；定时任务随进程生命周期存在，Bot 退出/重启时 cron 整体停止；
- `StartCron` 的 `ctx` 同样在返回后 cancel，只用于注册本身，不要传给常驻任务；
- cron 表达式为标准 5 段格式（`分 时 日 月 周`），非法表达式会返回 error；建议记日志并返回；
- 回调由 cron 自己的 goroutine 执行，**没有**框架的 panic 恢复。回调内用 `b.Go` 包裹实际工作，或自行 `recover`；
- `StartCron` 同样是串行执行，单插件超时 1 分钟；
- 启动期（`Start` 失败、平台未连接）注册的任务仍会按时间触发，回调内要自行判断配置与依赖是否就绪。

## Awake：启动完成

```go
func (p *MyPlugin) Awake(ctx context.Context, b bot.Bot) error {
	// 全部插件 Start 已完成；适合做依赖其他插件数据的初始化、打「就绪」日志
	p.Logger.Info("插件已就绪")
	return nil
}
```

- 在启动约 1 秒后调用，全部插件的 `Start` 已完成；
- **首次启动设置向导未完成时会跳过 Awake**，向导完成并重启后才正常执行；
- Awake 触发时平台适配器已开始连接，但不保证连接已经建立；发送消息仍要检查 `(msgId, success)` 返回值并降级；
- 单插件超时 1 分钟。

## 运行期事件

消息事件（`OnGroupMsg` / `OnFriendMsg`）：

- 按 `Order` 从小到大串行执行；返回 `false` 截断，后续插件收不到；返回 `true` 放行；
- 单个插件 panic 被捕获后**视为放行**，不会中断整条链；
- 每次调用默认 5 分钟超时，可通过 `bot.msg_event_timeout_sec` 调整（上限 86400 秒）。`ctx` 会取消，长任务请放后台；
- 自己发出的消息不会回流成事件（框架过滤 `Sender == SelfId`）。

通知事件（14 种）：

- 广播制，不截断、不排序影响；某个插件 panic 不影响其他插件；
- 每次调用超时 5 分钟，返回的 `error` 只记日志；
- QQ 专属通知（戳一戳、运气王、荣誉、精华、群名片、禁言、群文件上传等）在非 QQ 平台不会触发。

平台事件（`OnPlatformEvent`）：

- 广播制、按 `Meta.Platforms` 过滤；`event.Data` 类型由适配器定义，插件自行断言；
- 回调内不要假设 `Data` 一定是某个类型，断言失败直接返回。

按钮回调（`OnInteraction`）：

- 非广播，按回调数据里的「插件名:」前缀路由到对应插件；
- 在回调里设置 `ev.AnswerText` 可让平台给点击者展示一条提示；handler 返回后框架自动应答平台（消除客户端转圈）；
- 回调数据用 `p.CallbackData("载荷")` 打包，整体长度受平台限制（Telegram 64 字节）。

Bot 发送事件（`OnBeforeSendMsg` / `OnAfterSendMsg`）：

- 发送前按 `Order` 正向执行，返回 `false` 取消本次发送；发送后为广播制；
- 单次分发超时 10 秒；发送前事件会阻塞真正的发送，回调里不要做耗时操作；
- 在回调里再次发送消息不会触发发送事件（框架按 goroutine 屏蔽嵌套分发）。

## 卸载：OnUnload

实现可选接口 `plugin.UnloadEvent`：

```go
func (p *MyPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	// 先置闸门，阻止并发中的事件/后台循环继续写入
	p.closed.Store(true)
	if p.cancel != nil {
		p.cancel() // 取消 Start 里启动的后台 goroutine
	}
	if reason == plugin.UnloadUninstall {
		// 只有被插件市场卸载时才删除持久化数据
		p.PersistentStorage.Clear(ctx)
	}
	return nil
}
```

两种 `reason`：

| 值 | 触发场景 | 建议 |
| --- | --- | --- |
| `plugin.UnloadShutdown` | Bot 退出/重启（`/exit`、`/reboot`、面板重启、自动更新、市场流水线完成后的重启） | 释放连接、取消 goroutine、落盘；**不要**删除持久化数据，重启后插件会重新加载 |
| `plugin.UnloadUninstall` | 插件市场卸载该插件 | 清理插件自己的持久化数据、缓存、外部资源 |

执行规则：

- 只在插件完成 `Start` 后才会触发；`Start` 之前 `Stop` 不会调用；
- 按插件 `Order` **逆序**执行（后启动的先清理），每个插件最多一次；
- 单插件超时 1 分钟，出错或 panic 只记日志，不阻断其他插件与卸载流程；
- 钩子可能与运行期事件**并发执行**：仅靠 `cancel()` 不够，清理后仍可能有事件正在写数据。推荐先置 `closed atomic.Bool`，所有事件入口与后台循环先检查它；清理完成后保持关闭；
- 不要在这里做无限等待；`ctx` 会取消，超时后框架继续卸载；
- 用到 `UnloadEvent` 时，插件市场的 `min_framework` 要声明到包含该 API 的版本。

## 依赖注入

`Start` 之前框架向每个插件注入以下字段（都在 `plugin.Meta` 上，开箱即用）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `p.Storage` | `storage.Storage` | 缓存层（memory/redis），支持 TTL、列表；已按插件名 `base64(name)` 隔离 |
| `p.PersistentStorage` | `storage.PersistentStorage` | 持久化 KV（sqlite/mysql），重启不丢；同样按插件名隔离 |
| `p.RestyClient` | `*resty.Client` | 统一 HTTP 客户端，优先用它而不是 `http.DefaultClient` |
| `p.Logger` | `*slog.Logger` | 结构化日志，已带插件名 group |
| `p.SystemConfig` | `plugin.SystemConfig` | `AdminIds []message.QID` / `AdminId`；鉴权用 `IsAdmin(qid)` |
| `p.ConfigEditor` | `plugin.ConfigEditor` | 配置中心读写；可能为 `nil`，用前判空 |

存储注入时使用 `base64(插件名)` 作为命名空间前缀，不同插件同名 key 不会互相覆盖。插件内部还可以继续 `Clone("子命名空间")` 分类。

`SystemConfig.IsAdmin` 接受带平台前缀的 `message.QID`，与 `bot.admin_ids` 中的值直接比较；群管理员（`owner` / `admin`）用 `msg.Sender.Role` 判断，两者不是一回事。

## Panic 与协程

- 每次插件调用都被 `safeExecute` 包裹：panic 只影响当前插件，消息链视为放行，通知继续广播；框架只记录错误日志，**不会**因此调用 `OnPanic`；
- `b.Go(name, f)` 启动的协程有自己的 recover：panic 后会回调所有插件的 `OnPanic`（系统插件默认私聊告警管理员，1 分钟防抖），并维护协程计数供面板展示；
- 裸 `go` 没有这些保护；事件回调里如果确实需要「崩溃即告警」，把工作放进 `b.Go`；
- cron 回调不在 `safeExecute` 内，也不在 `b.Go` 内，务必用 `b.Go` 或自行 recover。

## 调试与测试

- 保留 `pluginlog` 插件，控制台会打印收发消息与通知，便于确认事件是否到达；
- 单元测试优先测纯逻辑：把命令解析、参数校验、格式化抽成普通函数，用表驱动测试，不要为了测试启动整个框架；
- 需要并发安全的状态用 `sync.Mutex` / `sync.Map` / `atomic`，并用 `go test -race ./...` 检查；
- 调试配置时看日志中的「注册插件配置结构体失败」「加载插件配置失败」，通常是标签写错或字段类型不支持；
- 插件「不触发」先按以下顺序排查：是否 `AddPlugin`、`ShowFor` 是否设置、`Meta.Platforms` 是否包含来源平台、`Order` 更小的插件是否返回了 `false`、群聊命令是否 @ 了机器人、命令是否以 `/` 开头。

```bash
go test ./...
go test -v -race ./...
```

## 相关

- [插件配置](/plugin/configuration) —— `ConfigSchema` 全量标签与默认值机制
- [插件系统概览](/plugin/overview) —— 执行顺序与中间件链
- [平台能力与优雅降级](/plugin/platforms) —— 类型断言探测平台能力
- [API · 事件接口](/api/events) —— 全部事件签名
