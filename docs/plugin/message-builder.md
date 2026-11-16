# 消息构造器

`msgchain` 包提供链式构造器，用于拼装 OneBot v11 消息段。四种入口对应四种消息形态：

```go
msgchain.Builder().Group()         // 群聊消息
msgchain.Builder().Friend()        // 私聊消息
msgchain.Builder().GroupForward()  // 群聊合并转发
msgchain.Builder().FriendForward() // 私聊合并转发
```

## 快速上手

```go
chain := msgchain.Builder().Group()
chain.Mention(msg.Sender.UserId)     // @某人（仅群聊）
chain.Text(" 你好！")
chain.Face(14)                       // QQ 自带表情
chain.ImageUrl("https://example.com/pic.png")

msgId, ok := b.SendGroupMsg(msg.GroupId, chain.Build())
```

方法可以链式连写：

```go
chain := msgchain.Builder().Friend().
    Text("任务完成 ✅").
    ImageUrl("https://example.com/report.png")
b.SendFriendMsg(userId, chain.Build())
```

## 普通消息方法

群聊（`GroupChainBuilder`）与私聊（`FriendChainBuilder`）方法一致，唯一区别是群聊多了 `Mention`：

| 方法 | 说明 |
| --- | --- |
| `Text(text)` | 文本 |
| `Face(faceId)` | QQ 小黄脸表情（id 为数字） |
| `Mention(userId)` | @某人（**仅群聊**） |
| `Reply(msgId)` | 引用回复某条消息 |
| `ImageUrl(url)` / `ImageLocal(path)` / `ImageBase64(b64)` | 图片：网络 / 本地文件 / Base64 |
| `VideoUrl(url)` / `VideoLocal(path)` / `VideoBase64(b64)` | 视频，三种来源同上 |
| `RecordUrl(url)` / `RecordLocal(path)` / `RecordBase64(b64)` | 语音，三种来源同上 |
| `FileUrl(name, url)` / `FileLocal(name, path)` / `FileBase64(name, b64)` | 文件，需指定显示文件名 |
| `Raw(segments...)` | 直接追加原始 `OB11Segment`，用于转发收到的消息段 |
| `Keyboard(rows...)` | 内联按钮键盘（行 × 按钮），用 `Button` / `ButtonURL` / `Row` 构造；平台不支持时出站自动剥离，插件应先断言 `bot.Interactive` 探测（见 [Bot 接口](/api/bot#内联按钮交互-bot-interactive-可选接口)） |

最后调用 `Build()` 得到链对象，传给 `b.SendGroupMsg` / `b.SendFriendMsg`。

### Raw 的典型用途：原样转发

复读机插件就是这么实现的 —— 把收到的消息段原封不动发回去：

```go
chain := msgchain.Builder().Group()
chain.Raw(msg.Message...) // msg.Message 是 []message.OB11Segment
b.SendGroupMsg(msg.GroupId, chain.Build())
```

### 内联按钮：翻页等轻交互

支持按钮的平台（如 Telegram）可在消息上附加可点击按钮，点击回调送回插件的 `OnInteraction`，适合翻页、菜单：

```go
chain := msgchain.Builder().Group().Text("搜索结果（第 1 页）").
    Keyboard(msgchain.Row(
        msgchain.Button("◀️ 上一页", p.CallbackData("pg:0")), // 回调按钮：点击产生回调事件
        msgchain.Button("▶️ 下一页", p.CallbackData("pg:2")),
        msgchain.ButtonURL("网页版", "https://example.com"),  // 链接按钮：点击打开网页
    )).Build()
```

回调数据用 `Meta.CallbackData(载荷)` 打包成「插件名:载荷」，框架按前缀路由回本插件；接收方式与能力探测见 [Bot 接口 · 内联按钮交互](/api/bot#内联按钮交互-bot-interactive-可选接口)。

## 合并转发消息

把多条消息打包成一条「聊天记录」转发，防撤回插件用它回顾消息：

```go
fb := msgchain.Builder().GroupForward()

for _, m := range messages {
    node := msgchain.Builder().Group()
    node.Text(m.Content)
    // 每个 node 显示为 m.UserId 这个人（昵称为 m.Nickname）发的消息
    fb.Message(m.UserId, m.Nickname, node.Build())
}

// 合并转发是可选能力：断言成功也要检查返回值，非 QQ 平台/后台路径可能返回 false
if fs, ok := b.(bot.GroupForwardSender); ok {
    if _, success := fs.SendGroupForwardMsg(groupId, fb.Build()); success {
        return false, nil
    }
}
// 降级：逐条发送或合并为一段文本
```

私聊合并转发用 `Builder().FriendForward()` + `bot.FriendForwardSender`（QQ 也可用 `bot.QQ.SendFriendForwardMsg`）。平台不支持时请退化为普通消息，详见 [平台能力与优雅降级](/plugin/platforms)。

::: tip 伪造聊天记录？
`Message(userId, nickname, chain)` 的 userId 和 nickname 完全由你指定 —— 利用这一点可以实现「伪造聊天记录」等趣味玩法。
:::

## 发送接口

构造好链之后，通过 `bot.Bot` 发送：

```go
b.SendGroupMsg(groupId, groupChain)  // → (msgId, ok) 公共能力，所有平台可用
b.SendFriendMsg(userId, friendChain) // → (msgId, ok) 公共能力，所有平台可用

// 以下为可选能力，需先类型断言；调用后仍要检查返回值：
if qb, ok := b.(bot.QQ); ok {
    qb.SendGroupForwardMsg(groupId, forwardChain)    // → (msgId, ok)
    qb.SendFriendForwardMsg(userId, forwardChain)    // → (msgId, ok)
    qb.SendGroupAIVoiceMsg(groupId, character, text) // AI 语音（需 NapCat 支持）
    qb.SendPokeMsg(userId, &groupId)                 // 戳一戳（groupId 可传 nil 表示私聊）
}

// 不依赖 QQ 事件的统一路由接口（后台推送也可用）：
if fs, ok := b.(bot.GroupForwardSender); ok { fs.SendGroupForwardMsg(...) }
if fs, ok := b.(bot.FriendForwardSender); ok { fs.SendFriendForwardMsg(...) }
```

::: tip 多平台
公共能力 `SendGroupMsg` / `SendFriendMsg` 在所有平台可用（飞书等平台内部自动翻译）；
QQ 专属方法在 `bot.QQ` 可选接口中；合并转发、流式回复、媒体续期等由 core 统一路由的接口在非支持平台可能断言成功但返回 `false`，务必检查返回值。
:::

所有发送方法返回 `(msgId, success)`，失败时记得处理：

```go
if _, ok := b.SendGroupMsg(msg.GroupId, chain.Build()); !ok {
    p.Logger.Error("消息发送失败", "group", msg.GroupId)
}
```

完整接口见 [API · Bot 接口](/api/bot)。
