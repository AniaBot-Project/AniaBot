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

防撤回插件（源码见官方插件仓库 `plugins/antiwithdrawal`）的模式 —— `sync.Map` + `LoadOrStore` 惰性初始化：

```go
queueI, _ := p.msg.LoadOrStore(msg.GroupId, NewMessageQueue[*message.Message](100))
queue := queueI.(*MessageQueue[*message.Message])
queue.Add(&msg)
```

## 安全的协程

插件内启动后台协程时，用 `bot.Go` 代替裸 `go`，享有崩溃恢复：

```go
bot.Go("my-background-task", func() {
    ticker := time.NewTicker(time.Minute)
    defer ticker.Stop()
    for range ticker.C {
        // 周期性工作；panic 不会拖垮进程，且会通知所有插件的 OnPanic
    }
})
```

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

func (p *MyPlugin) ConfigSchema() any { return &p.cfg }

func (p *MyPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.cfg.API == "" {
		// 初始化失败：fmt.Errorf 包装哨兵并附具体原因，框架统一记录错误日志
		return fmt.Errorf("%w: 未配置 plugin.myplugin.api", aniaerror.ParameterInitializeError)
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 30 // 防御性兜底
	}
	return nil
}
```

标签与类型推断的完整说明见[第一个插件 · 声明自己的配置](/plugin/first-plugin#进阶-声明自己的配置)。

## 发送失败的处理

所有 `SendXxx` 返回 `(msgId, bool)`，养成检查习惯：

```go
if _, ok := b.SendGroupMsg(msg.GroupId, chain.Build()); !ok {
    p.Logger.Error("发送消息失败", "group", msg.GroupId, "message", "[每日新闻]")
    return nil // 或重试
}
p.Logger.Info("发送消息", "group", msg.GroupId, "message", "[每日新闻]")
```

## 速率限制

高频调用外部 API 时，用带缓冲 channel 做信号量（AI 插件的做法）：

```go
type MyPlugin struct {
    plugin.Meta
    rateCh chan struct{}
}

func (p *MyPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
    limit := cfg.GetInt("plugin.myPlugin.rate_limit")
    p.rateCh = make(chan struct{}, limit)
    // 每秒补充令牌
    go func() {
        for range time.Tick(time.Second) {
            for i := 0; i < limit; i++ {
                select {
                case p.rateCh <- struct{}{}:
                default:
                }
            }
        }
    }()
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

QQ 的图片/文件链接约 3 分钟过期。防撤回插件（源码见官方插件仓库 `plugins/antiwithdrawal`）的两种应对：

1. 通过 QQ 平台专属能力 `bot.(bot.QQ).GetNCrkey()` 获取 rkey 改写 URL 续期（`utils.NewURLModifier`）——合并转发与 rkey 均属 QQ 能力，防撤回插件声明 `Meta.Platforms = []string{"qq"}` 并类型断言 `bot.QQ`
2. 无法续期时降级为文字占位：`[图片消息，已经超过3分钟过期时间]`

::: tip 多平台能力探测
`bot.Bot` 仅含公共能力；QQ 专属方法（合并转发/戳一戳/群签到/rkey/AI 语音等）在 `bot.QQ` 可选接口中，事件来源为 QQ 适配器时断言成功：
```go
if qb, ok := b.(bot.QQ); ok { ncrkey, ok := qb.GetNCrkey() }
```
其他平台断言失败，插件据此优雅退化。插件可用 `Meta.Platforms` 声明支持的平台（空 = 全部平台）。
:::

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

