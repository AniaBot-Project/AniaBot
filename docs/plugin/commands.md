# 命令解析

框架在分发消息前，会自动把消息文本解析成 `command.Command`，插件直接判断即可，无需自己切字符串。

## Command 结构

```go
type Command struct {
    Name    string   // 命令名（/ 后的第一个单词，无命令时为空）
    Args    []string // 命令参数（按空白切分）
    Mention bool     // 消息中是否 @ 了机器人
}
```

## 解析规则

解析逻辑（`bot/utils/commandparser.go`）：

1. 提取消息中所有 `text` 段拼接为纯文本，同时检测是否有 `@机器人`（`Mention`）
2. 文本**必须以 `/` 开头**才会解析出 `Name`，否则 `Name` 为空
3. 去掉 `/` 后按空白字符切分，第一段为 `Name`，其余为 `Args`

| 用户发送 | Name | Args | Mention |
| --- | --- | --- | --- |
| `@机器人 /news` | `news` | `[]` | `true` |
| `@机器人 /clock add 0 8 * * *` | `clock` | `["add","0","8","*","*","*"]` | `true` |
| `/help` | `help` | `[]` | `false` |
| `@机器人 今天天气如何` | `""` | `nil` | `true` |
| `随便聊聊` | `""` | `nil` | `false` |

## 典型判断模式

```go
func (p *MyPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    // 群聊命令：必须 @ 机器人 + 命令名匹配
    if cmd.Mention && cmd.Name == "dice" {
        // ...
        return false, nil
    }
    return true, nil
}

func (p *MyPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    // 私聊命令：一般不要求 @
    if cmd.Name == "help" {
        // ...
        return false, nil
    }
    return true, nil
}
```

## 处理参数

`Args` 是按空白切分的字符串切片，需要自己校验与转换：

```go
if cmd.Name == "explore" {
    n := 50 // 默认值
    if len(cmd.Args) >= 1 {
        if num, err := strconv.Atoi(cmd.Args[0]); err == nil && num > 0 && num <= 100 {
            n = num
        }
    }
    // ...
}
```

带子命令的解析（参考 `/clock` 的实现）：

```go
if cmd.Name == "clock" {
    sub := ""
    if len(cmd.Args) > 0 {
        sub = cmd.Args[0]
    }
    rest := cmd.Args
    if len(rest) > 0 {
        rest = rest[1:]
    }

    switch sub {
    case "list":
        // ...
    case "add":
        // ...
    default:
        // 回复用法说明
    }
}
```

::: tip 带空格的参数
`/clock add` 用 `|` 分隔参数就是为了绕过空白切分：`/clock add 0 8 * * * | 早安 | 内容`。取出后 `strings.Join(rest, " ")` 再按 `|` 分割即可。这是处理含空格参数的常用技巧。
:::

## 被动监听（非命令）

不依赖 `cmd`，直接读消息原始内容：

```go
// 监听所有消息内容（如 URL 解析插件）
if strings.Contains(msg.RawMessage, "bilibili.com") {
    // ...
}
```

也可以提取消息段做更精细的处理，`utils.ExtraMessageStr(msg)` 返回 `(纯文本, 是否@机器人)`。

## 相关工具函数

`bot/utils` 包提供：

| 函数 | 说明 |
| --- | --- |
| `ParseCommand(msg)` | 框架内部使用的命令解析（一般无需直接调用） |
| `ExtraMessageStr(msg)` | 提取纯文本 + 是否 @ 机器人 |
| `HasMention(msg)` | 消息是否 @ 了机器人 |
