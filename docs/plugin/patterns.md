# 常见模式

插件开发中反复出现的实用模式，摘自内置插件与官方插件仓库插件的真实做法。

## 权限控制：仅管理员

```go
if !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
    chain := msgchain.Builder().Group()
    chain.Text("你没有权限哦")
    b.SendGroupMsg(msg.GroupId, chain.Build())
    return false, nil
}
// 管理员逻辑...
```

`bot.admin_ids` 支持配置多个管理员，`IsAdmin` 对列表中任意一个都返回 true。

群管理员判断则通过 `msg.Sender.Role`（`owner` / `admin` / `member`）：

```go
if msg.Sender.Role == "member" {
    return true, nil // 普通成员不处理
}
```

## 并发安全的状态

插件会被多个群/用户的消息并发触发，共享状态需要保护：

```go
type MyPlugin struct {
    plugin.Meta
    counters sync.Map // key: groupId
}

// 原子布尔开关（复读机插件的做法）
type RepeatPlugin struct {
    plugin.Meta
    enable atomic.Bool
}
p.enable.Store(true)
if p.enable.Load() { /* ... */ }
```

## 按群隔离的缓存队列

防撤回插件（源码见 `custom/plugins/antiwithdrawal`）的模式 —— `sync.Map` + `LoadOrStore` 惰性初始化。`MessageQueue` 是该插件自带的泛型环形缓冲，不是框架公共类型，抄的时候要一并复制 `queue.go`：

```go
queueI, _ := p.msg.LoadOrStore(msg.GroupId, NewMessageQueue[*message.Message](100))
queue := queueI.(*MessageQueue[*message.Message])
queue.Add(&msg)
```

## 安全的协程

插件内启动后台协程时，用 `b.Go` 代替裸 `go`，享有崩溃恢复：

```go
// 在事件回调或 Awake 里拿到 b（bot.Bot）
func (p *MyPlugin) Awake(ctx context.Context, b bot.Bot) error {
	b.Go("my-background-task", func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 周期性工作；panic 不会拖垮进程，且会通知所有插件的 OnPanic
			}
		}
	})
	return nil
}
```

不要用裸 `go f()` 启动可能 panic 的后台任务，也不要在 `Start` 里用 `context.Background()` 起永不退出的循环——卸载时无法取消。

## 消息拦截器

利用 Order 实现前置拦截：

```go
Meta: plugin.Meta{
    Name:  "消息拦截器插件",
    Order: plugin.LevelLog + 1, // 仅次于日志层，早于所有业务插件
}

func (p *InterceptorPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if p.isBlocked(msg.GroupId, msg.Sender.UserId) {
        return false, nil // 阻断：后续插件（包括 AI）都收不到
    }
    return true, nil
}
```

## 配置声明与读取

推荐用结构体标签声明配置（实现 `ConfigSchemaProvider` 后框架自动注册面板字段、补默认值并在 Start 前填充）：

```go
type myConfig struct {
	API     string `cfg:"plugin.myplugin.api" label:"API 地址" group:"我的插件"`
	Timeout int    `cfg:"plugin.myplugin.timeout" label:"超时(秒)" group:"我的插件" default:"30"`
}

type MyPlugin struct {
	plugin.Meta
	cfg myConfig
}

func (p *MyPlugin) ConfigSchema() any { return &p.cfg }

func (p *MyPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.cfg.API == "" {
		// 初始化失败：fmt.Errorf 包装哨兵并附具体原因，框架统一记录错误日志。
		// 注意：返回 error 不会阻止插件加载，仍需要用自己的 ready/disabled 标志
		// 在事件入口阻止后续处理。
		return fmt.Errorf("%w: 未配置 plugin.myplugin.api", aniaerror.ParameterInitializeError)
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 30 // 防御性兜底
	}
	return nil
}
```

标签、嵌套结构体、`select`/`slider`/指针可选字段的完整说明见 [插件配置](/plugin/configuration)。

## 发送失败的处理

所有 `SendXxx` 返回 `(msgId, bool)`，养成检查习惯：

```go
if _, ok := b.SendGroupMsg(msg.GroupId, chain.Build()); !ok {
    p.Logger.Error("发送消息失败", "group", msg.GroupId, "message", "[每日新闻]")
    // 视业务决定：重试、降级或直接返回
}
p.Logger.Info("发送消息", "group", msg.GroupId, "message", "[每日新闻]")
```

## 速率限制

高频调用外部 API 时，用带缓冲 channel 做信号量（AI 插件的做法）：

```go
type MyPlugin struct {
    plugin.Meta
    rateCh chan struct{}
    bgCtx  context.Context
    cancel context.CancelFunc
}

type myConfig struct {
    RateLimit int `cfg:"plugin.myplugin.rate_limit" label:"每秒请求上限" group:"我的插件" default:"5"`
}

func (p *MyPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
    limit := p.cfg.RateLimit
    if limit <= 0 {
        limit = 1
    }
    p.rateCh = make(chan struct{}, limit)
    // Start 的 ctx 在返回后会被 cancel，常驻任务要自建 context，
    // 并在 OnUnload 中调用 p.cancel()。
    p.bgCtx, p.cancel = context.WithCancel(context.Background())
    go func() {
        ticker := time.NewTicker(time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-p.bgCtx.Done():
                return
            case <-ticker.C:
                for i := 0; i < limit; i++ {
                    select {
                    case p.rateCh <- struct{}{}:
                    default:
                    }
                }
            }
        }
    }()
    return nil
}

func (p *MyPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
    if p.cancel != nil {
        p.cancel()
    }
    return nil
}

// 使用时
select {
case <-p.rateCh:
    // 拿到令牌，执行
default:
    // 限流中，礼貌拒绝
}
```

## 资源过期处理

QQ 的图片/文件链接通常约 3 分钟过期。防撤回插件（源码见 `custom/plugins/antiwithdrawal`）的两种应对：

1. 通过 QQ 平台专属能力 `bot.(bot.QQ).GetNCrkey()` 获取 rkey 改写 URL 续期（`utils.NewURLModifier`）——合并转发与 rkey 均属 QQ 能力，防撤回插件声明 `Meta.Platforms = []string{"qq"}` 并类型断言 `bot.QQ`
2. 无法续期时降级为文字占位：`[图片消息，已经超过3分钟过期时间]`

持久化历史消息里的媒体，更推荐用 core 统一路由的 `bot.MediaRefresher` 重新申请地址，失败再显示占位；见 [平台能力与优雅降级](/plugin/platforms#媒体地址续期)。

::: tip 多平台能力探测
`bot.Bot` 仅含公共能力；QQ 专属方法（合并转发/戳一戳/群签到/rkey/AI 语音等）在 `bot.QQ` 可选接口中，事件来源为 QQ 适配器时断言成功：
```go
if qb, ok := b.(bot.QQ); ok { ncrkey, ok := qb.GetNCrkey() }
```
其他平台断言失败，插件据此优雅退化。注意：`GroupForwardSender` / `StreamSender` / `MediaRefresher` 等由 core 统一路由的接口在非支持平台也可能断言成功，必须检查调用返回值。插件可用 `Meta.Platforms` 声明支持的平台（空 = 全部平台）。
:::

## 卸载清理：先关闸门再清理

`OnUnload` 可能与运行期事件并发执行。仅 `cancel()` 后台 goroutine 不够：清理期间仍可能有事件把缓存写回去。推荐先置一个原子闸门，所有事件入口和后台循环都先检查：

```go
type MyPlugin struct {
    plugin.Meta
    closed atomic.Bool
}

func (p *MyPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if p.closed.Load() {
        return true, nil
    }
    // ...
}

func (p *MyPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
    p.closed.Store(true) // 先关闸门
    if p.cancel != nil {
        p.cancel()
    }
    if reason == plugin.UnloadUninstall {
        p.PersistentStorage.Clear(ctx)
    }
    return nil
}
```

`UnloadShutdown`（退出/重启）不要删除持久化数据，插件重启后会重新加载；只有 `UnloadUninstall`（市场卸载）才清理。

## 日志规范

使用注入的 `slog.Logger`，键值对风格：

```go
p.Logger.Info("播报群聊注册", "groupId", g)
p.Logger.Error("AI请求错误", "error", err.Error(), "group", msg.GroupId)
```

## 单元测试

参考 `bot/utils/commandparser_test.go` 与 `bot/plugins/pluginaichat/clock_test.go`，对解析逻辑与纯函数直接写表驱动测试：

```bash
go test ./...
go test -v -race ./...
```

