# 平台能力与优雅降级

AniaBot 把不同平台的差异收敛在适配器边界：插件拿到的事件与消息段是统一模型，公共能力（发消息、查资料、读历史）所有平台可用；平台专属能力通过**可选接口 + 类型断言**探测。插件写一次，能跑的平台尽量跑；跑不了的平台优雅降级，而不是崩溃。

## 平台与 ID

| 平台标识 | 适配器 | 框架 ID 前缀 | 说明 |
| --- | --- | --- | --- |
| `qq` | `napcat`、`luckylilia` | `qq:`（Luckylilia 为 `lil:`） | OneBot v11；两套适配器共享 `bot.QQ` 能力 |
| `qqofficial` | `qqofficial` | `qo:` | QQ 开放平台，ID 为 openid |
| `feishu` | `feishu` | `fs:` | 飞书 / Lark |
| `telegram` | `telegram` | `tg:` | Bot API 长轮询 |
| `discord` | `discord` | `dc:` | Gateway WebSocket |
| `weixin` | `weixin` | `wx:` | iLink bot 长轮询 |

事件里的 `msg.Platform` / `notice.Platform` / `event.Platform` 是平台标识；`Meta.Platforms` 按它过滤。ID 已经是带前缀的统一形式，插件不要自己拼前缀：

```go
gid := message.FromString("123456")       // QQ 群号 → qq:123456
uid := message.FromUint64(123456)         // 同上，明确是数字 ID
same := gid == message.FromString("123456") // 直接 == 比较

// 只有调外部 QQ API 需要裸号时才去前缀
raw := gid.TrimQQPrefix()
```

`message.FromString` 对纯数字自动补 `qq:`，对 `fs:` / `tg:` / `dc:` 等非数字 ID 原样保留；`QID.Uint64()` 只对 QQ ID 有意义。**不要**用 `message.QID(123)` 做整数转换（会变成 Unicode 码点）。

::: tip 同时接入 NapCat 与 Luckylilia
两者平台标识都是 `qq`，但 ID 前缀不同：NapCat 为 `qq:`，Luckylilia 为 `lil:`。纯数字群号经 `message.FromString` 会补成 `qq:` 并路由到 NapCat；要用 Luckylilia 时配置里直接写 `lil:<群号>`，或使用事件里拿到的 `GroupId`。插件内部不要假设 QQ 群号一定等于 `qq:` 前缀。
:::

## 声明支持平台

```go
p.Platforms = []string{"qq"}                 // 只支持 QQ
p.Platforms = []string{"qq", "feishu"}       // QQ + 飞书
// 不设置 = 空 = 全平台
```

框架在分发事件前按事件来源平台过滤，`Meta.Platforms` 不包含该平台的插件收不到消息/通知/发送事件/平台事件。依赖 QQ 专属能力的插件（如防撤回，需要合并转发 + rkey）应声明 `[]string{"qq"}`；只用到公共能力、或对可选能力做了降级的插件可以留空。

::: tip 平台过滤是“收不到”，不是“收到后返回 true”
被 `Platforms` 过滤的插件不会执行，也不参与 `Order` 链。不要指望在事件里判断平台来替代声明。
:::

## 能力探测的两类接口

### 1. 平台包装接口：断言成功即支持

这类接口只在对应平台的事件外观上实现，断言失败就表示平台不支持：

| 接口 | 支持平台 | 用途 |
| --- | --- | --- |
| `bot.QQ` | `qq`（NapCat / Luckylilia） | 合并转发、戳一戳、群签到、表情回应、rkey、AI 语音、好友/群列表等 |
| `bot.Interactive` | 目前为 Telegram | 内联按钮（inline keyboard） |
| `bot.MsgEditor` | 目前为 Telegram | 编辑已发出的消息（先发后改） |

```go
if qb, ok := b.(bot.QQ); ok {
	qb.SendPokeMsg(msg.Sender.UserId, &msg.GroupId)
} else {
	// 非 QQ：降级为普通文本回复
}
```

### 2. core 统一路由接口：断言可能成功，必须检查返回值

为了支持后台路径（定时任务、面板推送没有事件来源），core 自身实现了部分可选接口，再按目标 ID 前缀路由到适配器。此时事件外观即使是飞书/微信，断言也可能成功，但**平台不支持时调用返回 `false`**：

| 接口 | 实际支持 | 不支持时的表现 |
| --- | --- | --- |
| `bot.GroupForwardSender` | QQ（NapCat / Luckylilia） | 调用返回 `("", false)` |
| `bot.FriendForwardSender` | QQ（NapCat / Luckylilia） | 调用返回 `("", false)` |
| `bot.StreamSender` | 飞书 / Telegram / Discord | QQ 显式返回 false；其他平台返回 `(nil, false)` |
| `bot.MediaRefresher` | QQ（NapCat / Luckylilia） | 返回 `("", false)` |
| `bot.GroupMembers` | QQ（NapCat / Luckylilia） | 返回 `(nil, false)` |

```go
// 推荐写法：断言 + 调用结果双判断
if fs, ok := b.(bot.GroupForwardSender); ok {
	if _, success := fs.SendGroupForwardMsg(msg.GroupId, chain.Build()); success {
		return false, nil
	}
}
// 平台不支持或调用失败：退化为逐条发送普通消息
p.sendAsPlainMessages(b, msg.GroupId, parts)
```

这也是合并转发、流式回复、媒体续期在非 QQ 平台上的正确写法：**不要只断言成功就认为一定可用**。

## 常用能力与降级示例

### 合并转发

```go
fb := msgchain.Builder().GroupForward()
for _, m := range messages {
	node := msgchain.Builder().Group()
	node.Text(m.Content)
	fb.Message(m.UserId, m.Nickname, node.Build())
}

if fs, ok := b.(bot.GroupForwardSender); ok {
	if _, success := fs.SendGroupForwardMsg(groupId, fb.Build()); success {
		return
	}
}
// 降级：逐条发送或合并为一段文本
for _, m := range messages {
	c := msgchain.Builder().Group()
	c.Text(m.Content)
	b.SendGroupMsg(groupId, c.Build())
}
```

私聊合并转发对应 `bot.FriendForwardSender`；QQ 也可以直接断言 `bot.QQ` 调 `SendFriendForwardMsg`，但 `FriendForwardSender` 对后台路径更通用。

### 内联按钮与点击回调

```go
if iv, ok := b.(bot.Interactive); ok && iv.SupportsKeyboard() {
	c := msgchain.Builder().Group().
		Text("第 1 页").
		Keyboard(msgchain.Row(
			msgchain.Button("下一页", p.CallbackData("pg:2")),
			msgchain.ButtonURL("打开网页", "https://example.com"),
		))
	b.SendGroupMsg(msg.GroupId, c.Build())
} else {
	// 降级：提示用户发送文本命令
}
```

平台不支持时 core 会在出站自动剥离 `keyboard` 段，不会导致发送失败；但用户看不到按钮，所以仍应保留文本交互提示。点击回调由实现了 `plugin.InteractionHandler` 的插件接收：

```go
func (p *MyPlugin) OnInteraction(ctx context.Context, b bot.Bot, ev *message.InteractionEvent) error {
	ev.AnswerText = "已处理" // 可选：给点击者一条提示
	// ev.Data 是去掉「插件名:」前缀后的载荷
	return nil
}
```

### 消息编辑 / 流式回复

```go
if ed, ok := b.(bot.MsgEditor); ok {
	edited := msgchain.Builder().Group().Text("第 2 页").Build()
	if ed.EditGroupMsg(msgId, edited) {
		return
	}
}
// 编辑失败或平台不支持：发送新消息
```

`bot.StreamSender` 用于「先发一条，再逐段替换」的打字机效果：

```go
if ss, ok := b.(bot.StreamSender); ok {
	if h, ok := ss.SendGroupStream(groupId, initialChain); ok {
		defer h.End()
		_ = h.Patch("生成中的文本")
	}
}
// 不支持流式：等最终文本一次性发送
```

平台不支持时不要调用 `Patch`；`StreamHandle` 为 nil 时框架不会兜底。

### 媒体地址续期

QQ 的图片/文件/语音地址带 rkey 等时效参数，通常几分钟后失效（rkey 本身有效期约 2 小时，但直链不一定能撑到那时）。用 `bot.MediaRefresher` 重新申请：

```go
if mr, ok := b.(bot.MediaRefresher); ok {
	if url, ok := mr.RefreshMediaURL(message.SegmentImage, fileId, groupId, ""); ok {
		// 用新地址下载/展示
	}
}
// 失败：显示「图片已过期」占位，不要重试到死循环
```

`kind` 取 `message.SegmentImage` / `SegmentRecord` / `SegmentVideo` / `SegmentFile`；`groupId` / `userId` 用于定位会话（群文件、私聊文件续期需要），没有会话信息时传空串。

### 群成员 / 通讯录

```go
if gm, ok := b.(bot.GroupMembers); ok {
	if list, ok := gm.GetGroupMemberList(groupId, false); ok {
		// 用昵称替换 [at:id] 占位
	}
}
```

群成员枚举目前仅 QQ 支持；其他平台断言可能成功但返回 `false`，所以必须判断第二个返回值。`bot.QQ` 也有 `GetGroupMemberList` / `GetFriendList` / `GetGroupList`，但它们只适用于 QQ 事件。

### 平台自有事件

无法映射为公共事件/通知的平台事件走 `OnPlatformEvent`：

```go
// import "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
func (p *MyPlugin) OnPlatformEvent(ctx context.Context, b bot.Bot, ev message.PlatformEvent) error {
	if ev.Platform != "feishu" {
		return nil
	}
	// 具体类型由适配器包定义。飞书卡片回调是
	// *callback.CardActionTriggerEvent（larksuite/oapi-sdk-go/v3/event/dispatcher/callback）
	data, ok := ev.Data.(*callback.CardActionTriggerEvent)
	if !ok {
		return nil
	}
	// 处理卡片回调
	return nil
}
```

事件按 `Meta.Platforms` 过滤后广播；`Data` 的具体类型随适配器而异，务必 comma-ok 断言，不要直接强转。

## 平台差异速查

| 能力 | QQ（NapCat / Luckylilia） | 飞书 | Telegram | Discord | 微信 | QQ 官方 |
| --- | --- | --- | --- | --- | --- | --- |
| 群/私聊消息、查询、历史 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 合并转发 | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| 流式回复 | ❌ | ✅ | ✅ | ✅ | ❌ | ❌ |
| 内联按钮 | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| 编辑已发消息 | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |
| 媒体地址续期 | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| 群成员枚举 | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| `bot.QQ` 专属能力 | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |

::: warning 矩阵会随适配器演进
上表是当前版本的实现情况，不是框架承诺。写插件时以类型断言和调用返回值判断，不要把平台名写成硬编码分支；这样适配器补上能力后，插件自动获得支持。
:::

## 设计建议

1. **先只用公共能力**：发消息、查消息、读历史、存储、配置，这些跨平台，先保证能跑。
2. **QQ 专属能力集中到一个分支**：`if qb, ok := b.(bot.QQ); ok { ... }`，分支外提供通用降级。
3. **需要后台推送的插件**用 core 的统一路由接口（`GroupForwardSender` / `StreamSender` / `MediaRefresher`），不要从事件回调里把 `b` 存下来后台用。
4. **声明 `Platforms` 要克制**：真的离不开 QQ 才写 `[]string{"qq"}`；只是「有更好、没有也能用」的能力不要声明，否则会丢掉其他平台的事件。
5. **测试降级路径**：用一个不支持该能力的平台跑一遍，或写单测用假的 `bot.Bot` 断言失败分支。

## 相关

- [API · Bot 接口](/api/bot) —— 全部公共与可选能力签名
- [消息构造器](/plugin/message-builder) —— 合并转发、按钮键盘的构造方式
- [插件生命周期与依赖注入](/plugin/lifecycle) —— 事件分发与超时
- [API · 事件接口](/api/events) —— 平台事件与交互回调
