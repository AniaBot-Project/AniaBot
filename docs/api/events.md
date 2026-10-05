# 事件接口参考

插件通过重写 `plugin.Meta` 的对应方法接收事件。所有事件方法签名中的 `bot bot.Bot` 为框架注入的机器人操作接口（见 [Bot 接口](/api/bot)）。

## 平台作用域

框架支持多平台并存（QQ、飞书、Telegram、Discord、微信……），插件收到的事件来自哪个平台，由 `message.Message.Platform` / `BasicNotice.Platform` 标识。

- **`Meta.Platforms []string`**：插件声明支持的平台（如 `[]string{"qq"}`、`[]string{"qq","feishu"}`、`[]string{"qq","feishu","telegram"}`），空 = 支持全部平台（默认）。core 按事件来源平台过滤插件，不匹配的插件收不到该平台事件。
- **`bot.QQ` 断言**：事件回调里的 `bot.Bot` 是来源平台能力包装后的外观，QQ 平台可断言为 `bot.QQ`（见 [Bot 接口](/api/bot#qq-专属能力-bot-qq-可选接口)）。
- **`OnPlatformEvent`（可选接口）**：无法映射为公共事件（消息/通知）的平台自有事件（如飞书卡片回调、机器人入群、Telegram 机器人被拉群/移出），通过实现 `plugin.PlatformEventHandler` 的 `OnPlatformEvent(ctx, bot, message.PlatformEvent)` 接收，广播制、按 `Meta.Platforms` 过滤：
- **`OnInteraction`（可选接口）**：内联按钮点击回调。插件在消息链中附加 keyboard 段（`bot.Interactive` 断言探测）后，用户点击按钮经实现 `plugin.InteractionHandler` 的 `OnInteraction(ctx, bot, *message.InteractionEvent)` 送回，按回调数据中的插件名前缀精确路由（非广播），详见 [Bot 接口 · 内联按钮交互](/api/bot#内联按钮交互-bot-interactive-可选接口)。

```go
type PlatformEvent struct {
    Platform string // 平台标识（"qq" / "feishu" / "telegram"）
    Type     string // 事件类型（如 "feishu.card_action"、"feishu.bot_added"、"telegram.bot_added"）
    Data     any    // 平台原始事件数据，由各平台适配器包定义
}
```

::: tip QQ 专属通知
戳一戳 / 运气王 / 群荣誉 / 精华 / 群名片 / 禁言 / 群文件上传等 QQ 专属通知在非 QQ 平台**永远不会触发**（飞书 / Telegram / Discord 无对应事件源），依赖它们的插件（如日志插件）在这些平台上保持静默。公共通知（群成员进出、表情回应）飞书与 Telegram 会映射触发（Discord 仅映射表情回应，成员进出走平台事件）；消息撤回飞书与 Discord 可映射（Telegram 无撤回事件）。
:::

## 消息事件（中间件链）

按 `Order` 从小到大依次执行，返回 `false` 阻断传播。

```go
// OnGroupMsg 收到群聊消息触发
OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error)

// OnFriendMsg 收到私聊消息触发
OnFriendMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error)
```

### message.Message

```go
type Message struct {
    Time        uint            // 消息时间戳
    PostType    string          // 上报类型，"message"
    MessageType string          // "group" / "private"（QQ 与 Telegram）；飞书 p2p 私聊也映射为 "private"
    SubType     string          // 子类型
    MessageId   QID             // 消息 ID（平台前缀 + 平台原始 ID）
    MessageSeq  int             // 消息序号
    UserId      QID             // 发送者 ID
    GroupId     QID             // 群 ID（私聊为空字符串）
    Message     []OB11Segment   // 通用消息段（规范格式）
    RawMessage  string          // 纯文本
    Sender      MessageSender   // 发送者信息
    SelfId      QID             // 机器人自身 ID
    Platform    string          // 平台标识（"qq" / "feishu" / "telegram"）
}

type MessageSender struct {
    UserId   QID
    Nickname string
    Sex      string
    Card     string  // 群名片（QQ）
    Role     string  // "owner" / "admin" / "member"（QQ）
}

type OB11Segment struct {
    Type string         // "text" / "at" / "image" / "face" / ...
    Data map[string]any
}
```

`message.QID` 是 `string` 的封装，提供 `String()` / `Uint64()` 方法与 `FromString()` / `FromUint64()` 构造函数。**多平台下 ID 采用前缀体系**：QQ 为 `qq:数字`（如 `qq:123456`，旧版裸数字会在升级时自动迁移），其他平台带前缀（如飞书 `fs:oc_xxx`、Telegram `tg:123456`，Telegram 消息 ID 为 `tg:<chat_id>:<message_id>`）；core 按前缀路由到对应适配器。`Uint64()` 对 QQ ID（`qq:数字` 或旧版裸数字）有效，其他平台返回 0。用整数构造 QID 时**不要**使用 `message.QID(x)`（这会把 int 转成 Unicode 码点），应使用 `message.FromUint64(uint64(x))`。

### command.Command

```go
type Command struct {
    Name    string   // / 后的命令名
    Args    []string // 空白切分的参数
    Mention bool     // 是否 @ 了机器人
}
```

解析规则见 [命令解析](/plugin/commands)。

## 通知事件（广播制）

全部 14 种通知广播给**所有**插件，无阻断、无顺序影响。返回的 `error` 仅用于日志记录。

所有通知结构体内嵌：

```go
type BasicNotice struct {
    Time       uint
    PostType   string  // "notice"
    SelfId     QID
    NoticeType string
    Platform   string  // 平台标识（"qq" / "feishu" / "telegram"）
}
```

### OnGroupUpload —— 群文件上传

```go
type GroupUploadNotice struct {
    BasicNotice
    GroupId QID
    UserId  QID
    File    struct {
        Id    QID
        Name  string
        Size  uint
        Busid uint
    }
}
```

### OnGroupAdmin —— 群管理员变动

```go
type GroupAdminNotice struct {
    BasicNotice
    SubType string // "set" / "unset"
    GroupId QID
    UserId  QID
}
```

### OnGroupDecrease —— 群成员减少

```go
type GroupDecreaseNotice struct {
    BasicNotice
    SubType    string // "leave" / "kick" / "kick_me"
    GroupId    QID
    OperatorId QID    // 操作者（主动退群时 = UserId）
    UserId     QID
}
```

### OnGroupIncrease —— 群成员增加

```go
type GroupIncreaseNotice struct {
    BasicNotice
    SubType    string // "approve"（审批入群）/ "invite"（邀请入群）
    GroupId    QID
    OperatorId QID
    UserId     QID
}
```

### OnGroupBan —— 群禁言

```go
type GroupBanNotice struct {
    BasicNotice
    SubType    string // "ban" / "lift_ban"
    GroupId    QID
    OperatorId QID
    UserId     QID    // 为 0 表示全员禁言
    Duration   uint   // 禁言时长（秒），解禁时为 0
}
```

### OnFriendAdd —— 好友添加

```go
type FriendAddNotice struct {
    BasicNotice
    UserId QID
}
```

### OnGroupRecall —— 群消息撤回

```go
type GroupRecallNotice struct {
    BasicNotice
    GroupId    QID
    UserId     QID   // 消息发送者
    OperatorId QID   // 操作者（自己撤回时 = UserId）
    MessageId  uint
}
```

### OnFriendRecall —— 好友消息撤回

```go
type FriendRecallNotice struct {
    BasicNotice
    UserId    QID
    MessageId uint
}
```

### OnPoke —— 戳一戳

```go
type PokeNotice struct {
    BasicNotice
    SubType  string  // "poke"
    GroupId  *QID    // 群内戳一戳才有值，私聊为 nil
    UserId   QID     // 发起者
    TargetId QID     // 被戳者
}
```

### OnLuckyKing —— 群红包运气王

```go
type LuckyKingNotice struct {
    BasicNotice
    SubType  string // "lucky_king"
    GroupId  QID
    UserId   QID    // 发红包者
    TargetId QID    // 运气王
}
```

### OnHonor —— 群荣誉变更

```go
type HonorNotice struct {
    BasicNotice
    SubType   string // "honor"
    GroupId   QID
    HonorType string // "talkative"（龙王）/ "performer"（群聊之火）/ ...
    UserId    QID
}
```

### OnGroupMsgEmojiLike —— 群消息表情回应

```go
type GroupMsgEmojiLikeNotice struct {
    BasicNotice
    GroupId   QID
    UserId    QID // 贴/取消贴表情的操作者
    MessageId QID
    Likes     []struct {
        EmojiId string // 表情 ID
        Count   int    // 数量
    }
}
```

### OnEssence —— 群精华消息变更

```go
type EssenceNotice struct {
    BasicNotice
    SubType    string // "add" / "delete"
    GroupId    QID
    MessageId  QID
    SenderId   QID    // 消息发送者
    OperatorId QID    // 操作者
}
```

### OnGroupCard —— 群名片变更

```go
type GroupCardNotice struct {
    BasicNotice
    GroupId QID
    UserId  QID
    CardNew string
    CardOld string
}
```

## 生命周期事件

```go
// Start 插件初始化，读取配置。返回错误将标记插件初始化失败
Start(ctx context.Context, cfg *viper.Viper) error

// StartCron 注册定时任务，c 为框架共享的 cron 管理器
StartCron(ctx context.Context, bot bot.Bot, c plugin.CronManager) error

// Awake Bot 完全启动完成，此时可以安全发送消息
Awake(ctx context.Context, bot bot.Bot) error
```

`CronManager` 接口：

```go
type CronManager interface {
    AddFunc(spec string, cmd func()) (cron.EntryID, error)
}
```

## 卸载事件（可选接口）

```go
// UnloadEvent 插件实现后，框架在插件被卸载前调用一次其清理钩子
type UnloadEvent interface {
    OnUnload(ctx context.Context, reason UnloadReason) error
}

type UnloadReason string

const (
    // UnloadShutdown Bot 退出或重启（/exit、/reboot、面板重启、自动更新）
    UnloadShutdown UnloadReason = "shutdown"
    // UnloadUninstall 插件被插件市场卸载
    UnloadUninstall UnloadReason = "uninstall"
)
```

- 触发时机：`UnloadShutdown` 在 Bot 退出或重启前对全部插件触发（每个插件一次，按启动逆序）；`UnloadUninstall` 仅在插件市场卸载该插件时对其单独触发
- 用途：释放资源（连接、文件句柄）、落盘、按 `reason` 清理插件数据等；插件重启后会重新加载，`UnloadShutdown` 中不要删除持久化数据
- 钩子执行超时 1 分钟；出错或 panic 只记日志，不影响其它插件与卸载流程；钩子可能与运行期事件并发，实现需保证并发安全

## 异常事件

```go
// OnPanic 任何插件（或 bot.Go 协程）运行时 panic 时触发
OnPanic(ctx context.Context, bot bot.Bot, name string, err any)
```

- `name` 为 panic 发生的上下文标签（如「群聊消息事件」或 `bot.Go` 的任务名）
- 系统插件默认实现：私聊通知管理员，1 分钟内防抖

## 执行顺序常量

```go
const (
    LevelLog        = -1000 // 日志层
    LevelNormal     = 0     // 普通插件
    LevelPostHandle = 1000  // 后置处理层
)
```

