# 第一个插件

本教程带你写一个完整的「掷骰子」插件：群里 @机器人 发送 `/dice` 随机返回 1-6 的点数。几十行代码，覆盖插件开发的核心流程。

## 最终效果

```
你: @AniaBot /dice
AniaBot: 🎲 你掷出了 4 点！
```

## 第一步：创建插件文件

在 `custom/plugins/` 下新建目录（推荐位置，与示例插件同级）：

```
custom/plugins/plugindice/plugin.go
```

## 第二步：定义插件

```go
package plugindice

import (
	"context"
	"math/rand/v2"
	"strconv"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

type DicePlugin struct {
	plugin.Meta
}

func NewPlugin() *DicePlugin {
	return &DicePlugin{
		Meta: plugin.Meta{
			Name:      "掷骰子插件",
			HelpWords: "@我发送 /dice 掷一个骰子",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
			Author:    "you",
			Version:   "1.0.0",
		},
	}
}
```

嵌入 `plugin.Meta` 后，插件接口的所有方法都有了默认实现，我们只需重写关心的部分。

## 第三步：响应群消息

```go
func (p *DicePlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	// cmd.Mention: 消息中是否 @ 了机器人
	// cmd.Name:    / 后的命令名
	if !cmd.Mention || cmd.Name != "dice" {
		return true, nil // 不是我们的命令，放行给后续插件
	}

	point := rand.IntN(6) + 1

	chain := msgchain.Builder().Group()
	chain.Mention(msg.Sender.UserId)
	chain.Text(" 🎲 你掷出了 " + strconv.Itoa(point) + " 点！")
	b.SendGroupMsg(msg.GroupId, chain.Build())

	return false, nil // 已处理，阻断传播
}
```

几个要点：

- **`cmd`** 由框架预解析：`/dice 20` → `cmd.Name = "dice"`, `cmd.Args = ["20"]`
- **`msgchain.Builder()`** 是链式消息构造器，`Group()` 构造群消息，`Friend()` 构造私聊消息
- **返回值** `false` 表示这条消息到此为止，不再传给后面的插件（比如 AI 对话）

## 第四步：注册插件

编辑 `cmd/main.go`：

```go
import (
	_ "github.com/jeanhua/AniaBot/bot/adapter/napcat" // 平台适配器（空白导入触发 init 注册）
	"github.com/jeanhua/AniaBot/bot/core"
	"github.com/jeanhua/AniaBot/custom/plugins/plugindice"
)

func main() {
	bot := core.NewAniaBot()

	bot.AddPlugin(pluginsys.NewPluginSys())
	// ... 其他插件
	bot.AddPlugin(plugindice.NewPlugin()) // ← 注册

	bot.Run()
}
```

启用的平台由配置键 `bot.platform.<name>.enable` 决定（默认仅 QQ，多平台可并存），参考 `cmd/main.go`。

## 第五步：运行测试

```bash
go run ./cmd
```

群里 @机器人 发送 `/dice`，同时私聊发送 `/help` 可以看到你的插件已经出现在列表中。

## 进阶：声明自己的配置

插件只需定义一个带 `cfg` 标签的配置结构体，并实现可选接口 `ConfigSchemaProvider`，框架启动时会自动完成三件事：**注册字段到 Web 面板（可视化编辑）→ 补齐默认值到配置中心 → Start 前把配置填充进结构体**。无需手写 `cfg.Get*` 逐个读取。

```go
type diceConfig struct {
	MaxPoint int `cfg:"plugin.dice.max_point" label:"最大点数" group:"掷骰子" help:"掷 N 面骰" default:"6"`
}

type DicePlugin struct {
	plugin.Meta
	cfg diceConfig
}

// ConfigSchema 返回配置结构体指针（框架在依赖注入前调用，必须每次返回同一指针）
func (p *DicePlugin) ConfigSchema() any {
	return &p.cfg
}

func (p *DicePlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	// 配置已自动填充完成，直接用（也可再做校验兜底）
	if p.cfg.MaxPoint <= 0 {
		p.cfg.MaxPoint = 6
	}
	p.Logger.Info("骰子插件初始化", "maxPoint", p.cfg.MaxPoint)
	return nil
}
```

启动后面板「配置管理」中会自动出现「掷骰子」分组和「最大点数」表单项，改完重启生效。支持的字段标签：

| 标签 | 说明 |
| --- | --- |
| `cfg` | 点分配置键（必填）；嵌套结构体作为键前缀递归；`cfg:"-"` 跳过 |
| `label` / `group` / `help` | 面板显示名 / 分组 / 说明。`group` 支持「父分组.子分组」两级写法（如 `骰子.规则`，`.` 或 ` · ` 分隔），同一父分组合为一张卡片，配置项多时在卡片内按子分组用分节框归类 |
| `type` | 覆盖类型推断：`password`、`text`（多行文本）、`select`（需配合 `options`） |
| `options` | select 可选项，逗号分隔 |
| `sensitive` | `"true"` 时面板不回显（API 密钥等） |
| `default` | 默认值，按字段类型解析；切片逗号分隔；`\n` 可表达多行文本 |

类型推断：`string`→字符串、`bool`→开关、`int`→整数、`float64`→小数、`[]string`/`[]int`→列表。指针字段（如 `*int`）表示可选参数：配置键不存在时保持 `nil`。`Start()` 返回非 nil 错误会导致插件初始化失败并在日志中报错。

::: tip 底层机制
配置统一存于数据库（配置中心），框架启动时构建内存 viper 传给 `Start`。框架级共享键（如 `files.mcp_json`）不属于插件结构体，仍可通过 `cfg.GetString("files.mcp_json")` 读取。需要动态生成字段声明的场景，可实现低层接口 `ConfigRegistrar`（返回 `[]pluginconfig.Field`）。
:::

## 进阶：使用注入的依赖

框架已注入好常用依赖，直接用：

```go
p.Logger.Info("有人掷骰子", "user", msg.Sender.UserId)   // 结构化日志
p.Storage.SetString(ctx, "last", "4", storage.WithTTL(time.Hour)) // 缓存
resp, _ := p.RestyClient.R().Get("https://api.example.com")        // HTTP
if msg.Sender.UserId == p.SystemConfig.AdminId { /* 管理员专属 */ }
```

## 完整代码

项目内置了一个最小示例可直接参考：[`custom/mvp/example.go`](https://github.com/AniaBot-Project/AniaBot/blob/main/custom/mvp/example.go)，以及模板 [`custom/plugins/pluginexample/plugin.go`](https://github.com/AniaBot-Project/AniaBot/blob/main/custom/plugins/pluginexample/plugin.go)。

## 下一步

- [命令解析](/plugin/commands) —— `cmd` 的完整解析规则
- [从零开发完整插件](/plugin/tutorial) —— 综合运用配置、存储、定时任务与面板服务的完整教程
- [消息构造器](/plugin/message-builder) —— 图片、视频、合并转发等全部消息类型
- [数据存储](/plugin/storage) —— 让插件记住东西
