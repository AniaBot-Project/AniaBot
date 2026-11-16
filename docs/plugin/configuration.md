# 插件配置

AniaBot 的配置统一保存在数据库（配置中心），插件不需要自己解析配置文件。推荐实现可选接口 `plugin.ConfigSchemaProvider`：声明一个带 `cfg` 标签的结构体，框架会在启动时完成三件事：

1. **注册字段**：把字段元信息写给 Web 控制面板，自动渲染「配置管理」表单；
2. **补齐默认值**：配置中心缺少的键写入 `default`（已有值绝不覆盖）；
3. **填充结构体**：在插件 `Start` 之前把配置值反射进结构体，业务代码直接读字段。

```go
type diceConfig struct {
	MaxPoint int `cfg:"plugin.dice.max_point" label:"最大点数" group:"掷骰子" default:"6"`
}

type DicePlugin struct {
	plugin.Meta
	cfg diceConfig
}

// ConfigSchema 返回配置结构体指针：框架在依赖注入之前调用，
// 必须每次返回同一个指针，且不能在这里使用 p.Logger / p.Storage。
func (p *DicePlugin) ConfigSchema() any { return &p.cfg }
```

启动后，面板「配置管理」会自动出现「掷骰子」分组；修改配置后需要**重启 Bot** 才会重新生成 viper 快照并填入结构体。

::: tip 配置键约定
统一使用 `plugin.<插件 id>.*` 小写点分键（如 `plugin.dice.max_point`），避免与其他插件串键。同一插件内不要复用同一个键；框架注册表中同键**后注册者覆盖先注册者**。
:::

## 字段标签

| 标签 | 适用字段 | 说明 |
| --- | --- | --- |
| `cfg` | 全部叶子字段 | **必填**，点分配置键。嵌套结构体字段的 `cfg` 作为键前缀（不带前缀时透明沿用父前缀）；`cfg:"-"` 跳过该字段 |
| `label` | 全部 | 面板显示名，缺省用 Go 字段名 |
| `group` | 全部 | 面板分组。推荐一个插件只用一个顶层分组（用插件名）；字段多时可用「父分组.子分组」两级写法，如 `group:"签到打卡.规则"` |
| `help` | 全部 | 字段说明，展示在表单下方 |
| `default` | 全部 | 默认值（字符串形式，按字段类型解析；切片用逗号分隔；字符串可用 `\n` 表达多行） |
| `type` | 全部 | 覆盖类型推断：`password` / `text` / `select` / `multiselect` / `slider` 等，见下文 |
| `options` | `select` / `multiselect` | 可选项，逗号分隔；这两个类型**必须**声明 |
| `min` / `max` / `step` | `slider` | 滑条范围与步长，缺省 `0` / `100` / `1` |
| `ticks` | `slider` | 刻度标签，逗号分隔，从 min 到 max 均匀分布 |
| `sensitive` | 全部 | `"true"` 时按敏感字段处理：面板不回显、API 掩码 |

### 类型推断

| Go 字段类型 | 注册类型 | 面板控件 |
| --- | --- | --- |
| `string` | `string` | 单行输入框 |
| `bool` | `bool` | 开关 |
| `int` / `int64` 等 | `int` | 数字输入框 |
| `float32` / `float64` | `float` | 小数输入框 |
| `[]string` | `strings` | 列表编辑器 |
| `[]int` | `ints` | 数字列表编辑器 |

`type` 还可以显式指定：

- `password`：密码输入框（通常配合 `sensitive:"true"`）；
- `text`：多行文本；
- `select`：下拉单选，字段必须是 `string`，配合 `options`；
- `multiselect`：下拉多选，字段必须是 `[]string`，配合 `options`；
- `slider`：滑条，字段必须是 `int` / `float` 系，配合 `min` / `max` / `step` / `ticks`。

::: warning 不支持的类型
配置结构体只支持上表中的标量、切片及它们的指针。`map`、结构体切片、`time.Duration` 等类型需要在启动时自行从字符串转换，或者拆成基础类型字段。类型不合法时框架会记录错误日志，并跳过该插件的这份配置结构体。
:::

### 可选字段（指针）

指针标量（`*string`、`*int`、`*float64`、`*bool`）表示**可选参数**：

```go
type modelConfig struct {
	Temperature *float64 `cfg:"plugin.chat.temperature" label:"温度" type:"float"`
	MaxTokens   *int     `cfg:"plugin.chat.max_tokens" label:"最大 Token" type:"int"`
}
```

- 没有 `default` 的指针字段注册为 `optional`：配置键未设置时保持 `nil`，面板留空/清空会删除对应配置键；
- 带 `default` 的指针字段在启动时会分配默认值，适合「有默认值但允许显式设为 0」的参数；
- 业务代码使用前仍需判空，例如 `if p.cfg.Temperature != nil { ... }`。

### 默认值与生效时机

`default` 只在配置中心**缺少该键**时写入，因此升级插件新增字段会自动补默认值，不会覆盖用户已保存的值。修改面板配置写入数据库后，需要重启 Bot 才会重新加载到 `Start` 的结构体；运行期不会热更新（除非插件自己实现了动态读取）。

```go
func (p *DicePlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	// 配置已自动填充。对非法值做防御性兜底，别让插件因配置错误 panic。
	if p.cfg.MaxPoint <= 0 {
		p.cfg.MaxPoint = 6
	}
	p.Logger.Info("骰子插件初始化", "max_point", p.cfg.MaxPoint)
	return nil
}
```

## 嵌套结构体

嵌套结构体的 `cfg` 标签作为键前缀，适合把一组相关配置归类：

```go
type basicConfig struct {
	Enable bool   `cfg:"enable" label:"启用" group:"我的插件" default:"true"`
	Prefix string `cfg:"prefix" label:"命令前缀" group:"我的插件" default:"/"`
}

type networkConfig struct {
	Timeout int    `cfg:"timeout" label:"超时(秒)" group:"我的插件.网络" default:"30"`
	Proxy   string `cfg:"proxy" label:"代理" group:"我的插件.网络"`
}

type myConfig struct {
	Basic   basicConfig   `cfg:"basic"`   // → plugin.my.basic.enable
	Network networkConfig `cfg:"network"` // → plugin.my.network.timeout
	Debug   bool          `cfg:"debug" label:"调试日志" group:"我的插件" default:"false"`
}
```

嵌套结构体本身也可以不写 `cfg`，此时内部字段直接沿用父级前缀。**叶子字段**必须声明 `cfg`，否则注册报错。

## 分组规范

面板「配置管理」侧边栏按 `group` 聚合，一个顶层分组对应一个插件目录项。建议：

- 一个插件只使用**一个**顶层分组，名称用插件名（如「地震预警」「签到打卡」）；
- 字段很多时使用两级分组，如 `group:"地震预警.推送"`、`group:"地震预警.过滤"`，面板会把它们收在同一张卡片内分节展示；
- 不要按「网络 / 展示 / 限流」拆成多个顶层分组，否则侧边栏会出现一串碎条目，与其他插件风格不一致。

## 配置敏感信息

API Key、Token、密码等字段加 `type:"password" sensitive:"true"`，面板不会回显明文：

```go
Token string `cfg:"plugin.my.token" label:"API Token" type:"password" sensitive:"true" group:"我的插件"`
```

敏感字段仍会以明文存储于配置中心数据库，请同时保护好 `data/` 目录与数据库访问权限。不要在自己插件的日志里打印密钥。

## 低层接口：动态字段

如果字段是运行时才能确定的（例如根据远端模型列表生成选项），实现 `plugin.ConfigRegistrar`：

```go
func (p *MyPlugin) ConfigFields() []pluginconfig.Field {
	return []pluginconfig.Field{
		{
			Key:     "plugin.my.mode",
			Label:   "模式",
			Type:    "select",
			Options: []string{"fast", "quality"},
			Group:   "我的插件",
			Default: "fast",
		},
	}
}
```

`ConfigFields()` 同样在依赖注入之前调用，不能使用注入字段。`ConfigRegistrar` 与 `ConfigSchemaProvider` 可以共存，共用同一份注册表；同键以后注册者为准。普通插件优先用结构体标签，可读性和类型安全更好。

## 读写框架配置：ConfigEditor

绝大多数插件只需要读自己的配置。若要读取或修改**框架/其他插件**的配置键（如让插件复用 AI 对话的模型参数），使用注入的 `p.ConfigEditor`：

```go
if p.ConfigEditor != nil { // 持久化存储不可用时为 nil，必须判空
	baseURL, ok := p.ConfigEditor.Get("plugin.ai_chat_bot.base_url")
	if ok {
		p.Logger.Info("复用 AI 配置", "base_url", baseURL)
	}
	// 写入数据库；是否生效取决于读取方，通常需要重启
	_ = p.ConfigEditor.Set("plugin.my.enabled", true)
}
```

`ConfigEditor` 的 `Get` / `Set` / `Delete` / `All` 都直接操作配置中心，值为 JSON 解码后的原生类型。它适合管理类插件（如 AI 配置工具）；普通插件读取非自身配置，也可以在 `Start` 里直接用传入的 `cfg *viper.Viper`：

```go
// 框架级共享键，不属于插件配置结构体
mcpJSON := cfg.GetString("files.mcp_json")
aiModel := cfg.GetString("plugin.ai_chat_bot.model")
```

::: warning 不要写回 viper
`Start` 收到的 viper 是启动时的内存快照，修改它不会落库，也不会通知其他插件。需要持久化修改时用 `ConfigEditor`。
:::

## 完整示例

```go
package pluginwidget

import (
	"context"
	"strings"

	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

type widgetConfig struct {
	Enable  bool     `cfg:"plugin.widget.enable" label:"启用" group:"小组件" default:"true"`
	Mode    string   `cfg:"plugin.widget.mode" label:"模式" type:"select" options:"simple,advanced" group:"小组件" default:"simple"`
	Groups  []string `cfg:"plugin.widget.groups" label:"作用群" group:"小组件" help:"群 ID，逗号分隔；QQ 可填群号，其他平台带前缀"`
	Limit   int      `cfg:"plugin.widget.limit" label:"每日上限" group:"小组件.限制" default:"20" min:"0" max:"100" type:"slider"`
	Token   string   `cfg:"plugin.widget.token" label:"Token" type:"password" sensitive:"true" group:"小组件.密钥"`
	Verbose *bool    `cfg:"plugin.widget.verbose" label:"详细日志" group:"小组件.调试" help:"留空使用默认行为"`
}

type WidgetPlugin struct {
	plugin.Meta
	cfg widgetConfig
}

func NewPlugin() *WidgetPlugin {
	p := &WidgetPlugin{}
	p.Name = "小组件"
	p.HelpWords = "@我发送 /widget 使用小组件"
	p.ShowFor = plugininfo.ShowForGroup
	p.Order = plugin.LevelNormal
	return p
}

func (p *WidgetPlugin) ConfigSchema() any { return &p.cfg }

func (p *WidgetPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.cfg.Limit <= 0 {
		p.cfg.Limit = 20
	}
	// 规范化配置中的群 ID：QQ 群号补 qq: 前缀，其他平台 ID 原样保留
	for i, g := range p.cfg.Groups {
		p.cfg.Groups[i] = strings.TrimSpace(g)
	}
	p.Logger.Info("小组件初始化", "mode", p.cfg.Mode, "groups", len(p.cfg.Groups))
	return nil
}
```

## 常见坑

- `ConfigSchema()` 在依赖注入之前调用：不要用 `p.Logger` / `p.Storage`，每次必须返回同一指针；
- 配置键必须小写，且不要复用其他插件的键前缀；同键注册会覆盖；
- 导出的叶子字段漏写 `cfg` 标签会注册失败，整个配置结构体被跳过（日志可见）；
- 指针字段记得判空；`[]string` 默认值用逗号分隔，不是换行；
- 修改配置后重启生效，别在 `Start` 里假设配置会热更新；
- `ConfigEditor` 可能为 `nil`，使用前必须判空；
- 需要密码/密钥的字段用 `sensitive:"true"`，并且不要在日志中输出。

## 相关

- [第一个插件](/plugin/first-plugin) —— 从零写一个带配置的插件
- [插件生命周期与依赖注入](/plugin/lifecycle) —— `ConfigSchema` 在生命周期中的位置
- [从零开发完整插件](/plugin/tutorial) —— 配置 + 存储 + 定时任务 + 面板服务的完整流程
