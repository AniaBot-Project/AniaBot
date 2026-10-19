# Bot 接口参考

`bot.Bot` 是插件与机器人交互的唯一入口，每个事件方法都会注入。它聚合了**公共**消息收发、信息查询、系统能力与协程管理；平台专属能力（合并转发、戳一戳、rkey 等）不在 `bot.Bot`，而是通过可选的 `bot.QQ` 等扩展接口暴露。

::: info 错误处理约定
所有方法返回 `(value, bool)` —— 失败时内部已记录日志，以 `false` 返回，不暴露 error。
:::

## 公共能力（所有平台）

以下方法任何平台（QQ、飞书……）都可使用，适配器在内部完成协议翻译。

### 发送消息

```go
// SendGroupMsg 发送群聊消息
SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (msgId message.QID, success bool)

// SendFriendMsg 发送私聊消息
SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (msgId message.QID, success bool)
```

消息链的构造见 [消息构造器](/plugin/message-builder)。

### 查询消息与资料

```go
// GetMsgDetail 获取单条消息详情
GetMsgDetail(msgId message.QID) (msg *message.Message, success bool)

// GetGroupDetail 获取群详情
GetGroupDetail(groupId message.QID) (info *message.GroupInfo, success bool)

// GetGroupMsgHistory 获取群消息历史。message_seq 传 0 从最新开始
GetGroupMsgHistory(groupId message.QID, count int, messageSeq int) (*[]message.Message, bool)

// GetFriendMsgHistory 获取私聊消息历史
GetFriendMsgHistory(userId message.QID, count int, messageSeq int) (*[]message.Message, bool)
```

### 系统能力

```go
// GetPluginList 获取已注册插件信息（/help 的数据来源）
GetPluginList() []plugininfo.PluginInfo

// Stop 停止机器人
Stop()
```

`plugininfo.PluginInfo` 包含 `Name` / `HelpWords` / `AdminOnly` / `ShowFor` / `Author` / `Version`。

### 协程管理（Tracer）

```go
// Go 启动一个受管协程：panic 自动恢复并通知所有插件的 OnPanic
Go(name string, f func())
```

`name` 用于日志与 panic 告警中标识任务来源。插件内的任何后台协程都应该用 `bot.Go` 启动，而不是裸 `go`。

## QQ 专属能力（bot.QQ，可选接口）

合并转发、戳一戳、群签到、rkey、AI 语音、表情回应、好友/群列表等是 **QQ（NapCat/OneBot v11）平台专属**能力。它们在 `bot.QQ` 接口中，事件来源为 QQ 适配器时，事件回调收到的 `bot.Bot` 可类型断言为 `bot.QQ`：

```go
func (p *MyPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if qb, ok := b.(bot.QQ); ok {
        ncrkey, _ := qb.GetNCrkey() // QQ 专属：仅 QQ 事件可调用
    }
    // 非 QQ 平台（如飞书）断言失败，插件据此优雅退化
    return true, nil
}
```

```go
type QQ interface {
    // —— 发送 ——
    // SendGroupAIVoiceMsg 发送群聊 AI 语音（character 为角色 ID，可用 GetAIChatacter 查询）
    SendGroupAIVoiceMsg(groupId message.QID, character, msg string) (msgId message.QID, success bool)
    // SendPokeMsg 戳一戳。groupId 传 nil 表示私聊戳一戳
    SendPokeMsg(userId message.QID, groupId *message.QID) (success bool)
    // SendGroupForwardMsg 发送群聊合并转发
    SendGroupForwardMsg(groupId message.QID, chain msgchain.GroupForwardChain) (msgId message.QID, success bool)
    // SendFriendForwardMsg 发送私聊合并转发
    SendFriendForwardMsg(userId message.QID, chain msgchain.FriendForwardChain) (msgId message.QID, success bool)
    // SetMsgEmojiLike 给消息贴/取消表情回应
    SetMsgEmojiLike(msgId message.QID, emojiId int, like bool) (success bool)
    // SendGroupSign 群打卡
    SendGroupSign(groupId message.QID) (success bool)

    // —— 查询 ——
    // GetForwardMsg 获取合并转发消息内容
    GetForwardMsg(msgId message.QID) (msgs *[]message.Message, success bool)
    // GetGroupUserInfo 获取群成员信息
    GetGroupUserInfo(groupId, userId message.QID) (info *message.GroupUserInfo, success bool)
    // GetGroupMemberList 获取群成员列表
    GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool)
    // GetFriendList 获取好友列表
    GetFriendList() (*[]message.Friend, bool)
    // GetGroupList 获取群聊列表
    GetGroupList() (*[]message.GroupInfo, bool)
    // GetAIChatacter 获取可用的 AI 语音角色列表
    GetAIChatacter() (*[]message.AIChatacter, bool)
    // GetPrivateFileURL 获取私聊文件的下载地址
    GetPrivateFileURL(userId message.QID, fileId string) (string, bool)

    // —— 系统 ——
    // GetNCrkey 获取 NapCat rkey（用于图片/文件 URL 续期）
    GetNCrkey() ([]message.NCrkey, bool)
}
```

::: tip 多平台能力探测
事件回调收到的 `bot.Bot` 是**事件来源平台能力包装**后的外观：来源适配器实现了对应能力接口时，断言 `bot.QQ` 成功，否则失败。依赖 QQ 专属能力的插件应在 `Meta.Platforms` 声明只支持 QQ（见 [事件接口](/api/events#平台作用域)），并始终先做断言。
:::

### 常用返回结构

```go
type GroupUserInfo struct {
    GroupID      QID
    UserID       QID
    Nickname     string
    Card         string  // 群名片
    Sex          string
    Age          int
    JoinTime     uint    // 入群时间
    LastSentTime uint    // 最后发言时间
    Level        string  // 活跃等级
    Role         string  // "owner" / "admin" / "member"
    Title        string  // 专属头衔
    Unfriendly   bool
    IsRobot      bool
}
```

## 群聊合并转发（bot.GroupForwardSender，可选接口）

合并转发同样是 QQ 专属能力，但单独成接口 `bot.GroupForwardSender`：事件回调里断言成功表示当前平台支持；**后台路径**（没有事件来源，如定时任务、面板触发的推送）持有的 core 也能断言成功——调用时按目标群 ID 前缀路由到对应适配器。适配器不支持（非 QQ 平台）时返回 `("", false)`，插件应退化为逐条发送：

```go
if fs, ok := b.(bot.GroupForwardSender); ok {
    fwd := msgchain.Builder().GroupForward()
    // 每个节点一条记录：node 的 user_id/nickname 是记录里显示的发送者（机器人自身）
    fwd.Message(msg.SelfId, "AniaBot", msgchain.Builder().Group().Text("第一段").Build())
    fwd.Message(msg.SelfId, "AniaBot", msgchain.Builder().Group().Text("第二段").Build())
    if _, success := fs.SendGroupForwardMsg(msg.GroupId, fwd.Build()); !success {
        // 平台不支持或发送失败 → 逐条发送
    }
}
```

节点内容支持普通消息段（文本/图片/@ 等），QQ 客户端展开「聊天记录」后可见。

## 内联按钮交互（bot.Interactive，可选接口）

Telegram 等平台支持在消息上渲染可点击按钮（inline keyboard）：点击不产生新消息，而是回调事件送回插件，适合翻页、菜单等交互。插件先断言 `bot.Interactive` 探测平台能力，支持时在消息链中附加 keyboard 段：

```go
func (p *MyPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if iv, ok := b.(bot.Interactive); ok && iv.SupportsKeyboard() {
        // 带按钮发送：列表 + 翻页按钮（回调数据经 Meta.CallbackData 打包插件前缀）
        b.SendGroupMsg(msg.GroupId, msgchain.Builder().Group().
            Text("搜索结果（第 1 页）").
            Keyboard(msgchain.Row(
                msgchain.Button("▶️ 下一页", p.CallbackData("pg:2")),
            )).Build())
    } else {
        // 不支持按钮的平台退化为文本指令（"回复 下一页 翻页"）
    }
    return false, nil
}
```

回调数据约定为 `插件名:载荷`（`Meta.CallbackData("pg:2")` 打包，插件名不能含 `:`，整体不超过平台上限——Telegram 为 64 字节）。实现 `plugin.InteractionHandler` 的插件接收点击回调，`ev.Data` 为剥离插件前缀后的载荷；handler 返回后框架自动应答平台（消除客户端转圈），`ev.AnswerText` 非空时作为提示展示在点击者客户端：

```go
func (p *MyPlugin) OnInteraction(ctx context.Context, b bot.Bot, ev *message.InteractionEvent) error {
    // ev: Platform / MessageType("group"|"private") / GroupId / UserId /
    //     MessageId(按钮所在消息) / CallbackId / Data(载荷) / Raw(平台原始回调)
    page := parsePayload(ev.Data)
    ev.AnswerText = fmt.Sprintf("已翻到第 %d 页", page)
    return nil
}
```

::: tip 优雅降级
不支持的平台上 core 会在出站时自动剥离 keyboard 段（不会导致发送失败），但插件应始终先断言 `bot.Interactive` 再决定附加按钮，并为文本交互保留提示。平台支持矩阵见 [internals](/internals/framework#能力分层)。
:::

## 消息编辑（bot.MsgEditor，可选接口）

支持「先发后改」的平台（Telegram `editMessageText` 等）可断言 `bot.MsgEditor` 编辑已发出消息——配合按钮做就地翻页（点「下一页」原消息刷新内容与按钮，不刷屏）：

```go
if ed, ok := b.(bot.MsgEditor); ok {
    chain := msgchain.Builder().Group().Text("搜索结果（第 2 页）").
        Keyboard(msgchain.Row(msgchain.Button("▶️ 下一页", p.CallbackData("pg:3")))).Build()
    if !ed.EditGroupMsg(msgId, chain) {
        // 编辑失败（消息过旧/媒体消息改文本等）→ 退化为发送新消息
    }
}
```

chain 携带 keyboard 段时同时更换按钮，未携带时保持原按钮。QQ/OneBot v11 无消息编辑 API，断言失败，插件应退化为发送新消息。
