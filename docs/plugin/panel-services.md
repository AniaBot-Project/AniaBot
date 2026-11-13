# 插件注册面板服务

AniaBot 的 Web 控制面板与插件有两种协作机制，**不要混淆**：

| 机制 | 作用 | 接口 |
| --- | --- | --- |
| 配置表单 | 在「配置管理」页渲染你的插件配置项 | `plugin.ConfigSchemaProvider` / `plugin.ConfigRegistrar`（见 [第一个插件 · 声明自己的配置](/plugin/first-plugin#进阶-声明自己的配置)） |
| 面板功能页 | 让你的插件成为「消息日志 / 任务日志 / 记忆管理 / 知识库 …」等页面的**数据源** | `adminpanel.XxxSource` 可选接口（本章） |

面板的功能页面**不是写死的**：core 启动面板前会遍历插件，用类型断言探测 `adminpanel.XxxSource` 可选接口（与 `bot.QQ` 同一套「可选接口 + 类型断言」惯例）。插件实现了哪个接口，对应页面就自动接上它的数据；没有任何插件实现时，页面返回空数据，面板照常运行。

## 发现机制

`bot/core/core.go` 的 `startAdminPanel()` 负责接线：

```go
for _, p := range ania.plugins {
    if src, ok := p.(adminpanel.TaskLogSource); ok {
        taskLogFn = src.TaskLogQuery
    }
    if src, ok := p.(adminpanel.ClockTaskSource); ok {
        clockSrc = src
    }
    // ... MsgLogSource / SkillSource / MemorySource / KnowledgeBaseSource /
    //     TeamSource / QuotaSource / QueryLogSource 同理
    if taskLogFn != nil && clockSrc != nil && /* ... */ {
        break // 全部接口都集齐后停止遍历
    }
}
```

关键语义：

- **单提供者**：每个接口只有一个插件接线，后遍历到的插件会**覆盖**前面的（同样 `break` 发生在集齐全部接口时）
- **内置插件已占用全部接口**：默认 `cmd/main.go` 里，AI 对话插件实现了 8 个接口、日志插件实现了 `MsgLogSource`，在第 6 个插件（AI 对话）处就集齐并 `break` —— **之后注册的自定义插件不会成为面板数据源**
- **要让自定义插件接管某个页面**：从 `cmd/main.go` 移除占用该接口的内置插件（典型做法是移除 `pluginaichat.NewAIChatPlugin()`），或完全按自己的插件组合构建（见下文示例）

::: warning 单提供者的实际影响
面板服务是「最后集齐者胜」的单例接线，不是多插件聚合。如果你自研了定时任务/日志类插件，并希望面板「任务日志」「消息日志」页展示**你的**数据，需要在 `main.go` 中移除对应的内置提供者（`pluginaichat` / `pluginlog`），否则面板展示的是内置插件的数据。
:::

## 接口总览

| 面板页面 | 接口 | 内置提供者 |
| --- | --- | --- |
| 消息日志 | `adminpanel.MsgLogSource` + `adminpanel.MsgLogSessionSource` + 删除类的 `adminpanel.MsgLogDeleteSource` / `adminpanel.MsgLogSessionDeleteSource`（后三项可选） | 日志打印插件（`pluginlog`） |
| 任务日志 | `adminpanel.TaskLogSource` | AI 对话插件（clock） |
| 定时任务 | `adminpanel.ClockTaskSource` | AI 对话插件（clock） |
| 技能管理 | `adminpanel.SkillSource` + `adminpanel.SkillToggleSource`（可选） | AI 对话插件 |
| 记忆管理 | `adminpanel.MemorySource` | AI 对话插件 |
| 知识库 | `adminpanel.KnowledgeBaseSource` | AI 对话插件 |
| Agent 团队 | `adminpanel.TeamSource` | AI 对话插件 |
| 配额管理 | `adminpanel.QuotaSource` | AI 对话插件 |
| Query 日志 | `adminpanel.QueryLogSource` | AI 对话插件 |
| 试聊室 | `adminpanel.ChatConsoleSource` | AI 对话插件 |

## 接口签名

### MsgLogSource —— 消息日志页

```go
type MsgLogSource interface {
    // 分页返回消息日志（新在前）；beforeID>0 时仅返回 ID 小于它的更旧日志（滚动分页游标）
    MsgLogPage(limit int, beforeID uint64) []msglog.Entry
}

// 可选：聊天式消息面板的会话分类与按会话分页。未实现时面板退化为「全部消息」单视图。
type MsgLogSessionSource interface {
    // 会话摘要（按最近活动倒序），面板左侧按群聊 / 私聊 / 系统通知分组展示
    MsgLogSessions() []msglog.Session
    // 指定会话的一页日志（新在前）：beforeID>0 取更旧分页；afterID>0 取轮询增量
    MsgLogSessionPage(session string, limit int, beforeID, afterID uint64) []msglog.Entry
}

// 可选：为过期媒体地址续期。QQ 等平台的图片 / 文件地址带有时效性鉴权参数，
// 过期后由面板媒体代理在转发失败时调用本接口重新申请一次。
type MsgLogMediaSource interface {
    MsgLogMediaURL(entryID uint64, index int) (string, bool)
}

// 可选：删除单条日志（面板「消息日志」页右键「删除消息」）。未实现时该入口不可用。
type MsgLogDeleteSource interface {
    // 返回是否实际删除（日志不存在 / 已被容量淘汰 / 已删除时为 false）
    MsgLogDelete(id uint64) bool
}

// 可选：删除整个会话的日志（面板右键会话「删除会话记录」）。未实现时该入口不可用。
type MsgLogSessionDeleteSource interface {
    // 删除会话 session（g:/f:/n:system）的全部日志，返回删除条数（无记录时为 0）
    MsgLogDeleteSession(session string) int
}
```

`msglog.Entry` 字段：`ID uint64`、`Time`、`Type`、`GroupId`、`UserId`、`Nickname`、`Title`、`Text`、`Media`、`MessageId`。`Media` 描述消息内的图片 / 文件（`Kind`、`URL`、`Name`、`Size`、`FileId`），`URL` 只应填浏览器可访问的 http(s) 地址——`data:` / `base64:` 内联负载体积可达 MB 级，写进日志会让缓存存储迅速膨胀，面板会把它们当作不可展示的占位。`Entry.Deleted` 是内部删除标记，接口返回的数据源应自行过滤掉它（直接用 `msglog.Recorder` 的读取方法即可，已统一过滤）。

会话键由 `Entry.SessionKey()` 统一生成：群消息 / 群通知为 `g:<群ID>`，好友消息 / 好友通知为 `f:<用户ID>`，其余通知为 `n:system`。推荐直接用 `bot/component/msglog` 的 `Recorder`：它按上述规则维护会话索引（首次查询时从存储重建，之后随 `Add` 增量更新），并自动对预览文本做截断。图片默认由浏览器直连 `Media.URL`（不占服务器带宽）；日志里的地址过期时，面板用 `/api/msglogs/media/link?entry=…&index=…` 向平台续签并 302 到新地址，直连失败或下载文件时才走媒体代理 `/api/msglogs/media?url=…&entry=…&index=…`（管理员登录态访问、拒绝本机 / 私网地址，地址过期时也会续签），插件只需填好 `Media.URL`。

`Entry.MessageId` 与 `Media.FileId` 决定媒体地址能否续期：QQ 过期后按 `FileId` 调 `get_image` / `get_file` / `get_group_file_url` 重新签发，失败再按 `MessageId` 重新拉取原消息；两者都缺省时面板只能显示加载失败，因此尽量一并记录（`Recorder` 侧无需额外处理，插件填好条目字段即可）。

### TaskLogSource —— 任务日志页

```go
type TaskLogSource interface {
    TaskLogQuery(f tasklog.Filter) []tasklog.Entry
}
```

`tasklog.Filter` 支持按 `TargetType`（group/friend）、`TargetID`、`TaskID`、`Status`、时间范围、标题关键词过滤与 `Before` 游标分页；`tasklog.Entry` 包含任务标题、触发内容、状态（running/success/timeout/error/interrupted）、耗时、回复与 token 用量等。推荐直接用 `bot/component/tasklog` 的 `Logger`（内部已实现过滤与分页）。

### ClockTaskSource —— 定时任务页

```go
type ClockTaskSource interface {
    ClockTasks() []plugininfo.ClockTaskInfo
    CreateClockTask(t plugininfo.ClockTaskCreate) (string, error)
    UpdateClockTask(id string, f plugininfo.ClockTaskUpdate) error
    DeleteClockTask(id string) error
}
```

面板对插件管理的动态定时任务做增删改查与启停；`plugininfo.ClockTaskInfo/Create/Update` 见 `common/plugininfo/clock.go`。

### SkillSource —— 技能管理页

```go
type SkillSource interface {
    SkillList() (skills []plugininfo.SkillInfo, dir string, whitelist []string)
    SkillDelete(name string) error
    SkillUpload(filename string, data []byte) error // 从 zip 内容安装并热重载
}

// 可选：启用/禁用外置 skill（内置 skill 由实现方拒绝禁用）
type SkillToggleSource interface {
    SkillSetEnabled(name string, enabled bool) error
}
```

`plugininfo.SkillInfo` 的 `Builtin` / `Disabled` 字段用于面板展示内置标识与禁用状态。

### MemorySource —— 记忆管理页

```go
type MemorySource interface {
    MemoryScopes() []plugininfo.MemoryScopeInfo
    MemoryList(scope string) ([]plugininfo.MemoryEntryInfo, error)
    MemoryCreate(up plugininfo.MemoryEntryUpsert) (string, error)
    MemoryUpdate(up plugininfo.MemoryEntryUpsert) error
    MemoryDelete(scope, id string) error
}
```

### KnowledgeBaseSource —— 知识库页

```go
type KnowledgeBaseSource interface {
    KnowledgeScopes() []plugininfo.KnowledgeScopeInfo
    KnowledgeList(scope string) ([]plugininfo.KnowledgeDocInfo, error)
    KnowledgeCreate(up plugininfo.KnowledgeDocUpsert) (string, error)
    KnowledgeUpdate(up plugininfo.KnowledgeDocUpsert) error
    KnowledgeDelete(scope, id string) error
    KnowledgeImportURL(scope, url string) (string, error)
}
```

### TeamSource —— Agent 团队页

```go
type TeamSource interface {
    TeamRoles() []plugininfo.TeamRoleInfo
    TeamScopes() []plugininfo.TeamScopeInfo
    TeamList(scope string) ([]plugininfo.TeamInfo, error)
    TeamCreate(up plugininfo.TeamUpsert) error
    TeamUpdate(up plugininfo.TeamUpsert) error
    TeamDelete(scope, name string) error
}
```

### QuotaSource —— 配额管理页

```go
type QuotaSource interface {
    QuotaSummary() (plugininfo.QuotaSummaryInfo, error)
    QuotaReset(scope string) error // scope="all" 清空当日全部，否则仅清除指定会话
}
```

### QueryLogSource —— Query 日志页

```go
type QueryLogSource interface {
    QueryLogRecent(f querylog.Filter) []querylog.Entry
}
```

### ChatConsoleSource —— 试聊室页

把聊天搬到面板上：管理员在面板里直接发消息，插件用自己的对话管线生成回复并回显（不向任何平台发送消息）。发送是异步的——`ChatConsoleSend` 立即返回，面板按 `Seq` 轮询 `ChatConsoleMessages` 拿流式文本、工具调用明细与最终状态。试聊的一轮对话同样写入 Query 日志（`chat_type=panel`）并计入 Token 统计，但不计入每日配额；AI 用 `|||` 分隔的回复按多条 `ChatConsoleMessage` 返回，与线上拆成多条发送的效果一致。

```go
type ChatConsoleSource interface {
    // 全部试聊会话（最近活动在前）
    ChatConsoleSessions() []plugininfo.ChatConsoleSession
    // 新建会话（目标为通用试聊或某个群聊/私聊 scope），返回创建结果
    ChatConsoleCreate(req plugininfo.ChatConsoleSessionCreate) (plugininfo.ChatConsoleSession, error)
    // 删除会话（连同转录与 AI 上下文）
    ChatConsoleDelete(id string) error
    // Seq 大于 afterSeq 的消息；最后一条总会重新返回，供面板刷新流式文本。
    // busy 表示该会话是否仍在生成
    ChatConsoleMessages(id string, afterSeq int64) ([]plugininfo.ChatConsoleMessage, bool, error)
    // 清空转录与 AI 上下文（保留会话）
    ChatConsoleClear(id string) error
    // 异步发送一条用户消息；上一条仍在生成时应返回错误
    ChatConsoleSend(id, text string) error
    // 中止该会话正在生成的回复（空闲时为空操作）
    ChatConsoleStop(id string) error
}
```

`plugininfo.ChatConsoleSession`（`ID` / `Title` / `TargetKind` / `TargetID` / `TargetName` / `CreatedAt` / `UpdatedAt` / `Count` / `Busy`）与 `plugininfo.ChatConsoleMessage`（`Seq` / `Role`（user/assistant/system）/ `Text` / `Status`（running/ok/error/stopped/timeout/interrupted）/ `Error` / `Time` / `DurationMs` / `Attachments` / `ToolCalls` / `ToolCallsTotal` / `Usage`）是面板展示用的视图模型；`TargetKind` 取值 `test`（不绑定会话 scope）/ `group` / `friend`。附件用 data URI 直接回传给浏览器（图片内联、文件可下载），体积过大的内容建议只回一句话说明。

`ChatConsoleCreate` 的 `target_id` 由面板从通讯录选择，与线上会话键（`g:<群 ID>` / `f:<用户 ID>`）中的 ID 一致，插件据此复用该会话的系统提示词覆盖、记忆、知识库等能力即可。

## 完整示例：自定义「消息日志」提供者

下面实现一个记录插件自身操作的面板数据源（比如一个「群投票插件」把每次投票结果记入消息日志页）。完整代码骨架：

```go
package pluginpoll

import (
	"context"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

type PollPlugin struct {
	plugin.Meta
	recorder *msglog.Recorder // 消息日志记录器
}

func NewPlugin() *PollPlugin {
	return &PollPlugin{
		Meta: plugin.Meta{
			Name:      "群投票插件",
			HelpWords: "@我发送 /poll 选项A 选项B 发起投票",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup,
		},
	}
}

func (p *PollPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	// 记录器挂在缓存存储上；写入上限 0 = 使用默认上限（5000 条）
	p.recorder = msglog.New(p.Storage, 0)
	return nil
}

// ---- 面板服务：实现 adminpanel.MsgLogSource ----

// MsgLogPage 分页返回本插件记录的消息日志（新在前）。
func (p *PollPlugin) MsgLogPage(limit int, beforeID uint64) []msglog.Entry {
	if p.recorder == nil {
		return nil
	}
	return p.recorder.Page(limit, beforeID)
}

func (p *PollPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !cmd.Mention || cmd.Name != "poll" {
		return true, nil
	}
	// 记录一条日志：面板「消息日志」页可看到
	if p.recorder != nil {
		p.recorder.Add(msglog.Entry{
			Time:     time.Now(),
			Type:     msglog.TypeGroup,
			GroupId:  msg.GroupId.String(),
			UserId:   msg.Sender.UserId.String(),
			Nickname: msg.Sender.Nickname,
			Text:     "发起投票: " + msg.RawMessage,
		})
	}
	chain := msgchain.Builder().Group()
	chain.Mention(msg.Sender.UserId)
	chain.Text(" 投票已发起，请选择选项~")
	b.SendGroupMsg(msg.GroupId, chain.Build())
	return false, nil
}
```

真实实现可直接参考内置插件：

- 消息日志：`bot/plugins/pluginlog/plugin.go`
- 任务日志 / 定时任务：`bot/plugins/pluginaichat/clock.go`
- 记忆 / 知识库 / 技能 / 团队 / 配额：`bot/plugins/pluginaichat/` 下的 `memorypanel.go` / `knowledgepanel.go` / `skillpanel.go` / `teampanel.go` / `quota.go`

## 如何让自定义实现生效

默认 `cmd/main.go` 注册了全部内置插件，其中 `pluginaichat` 会在第 6 个插件处集齐全部接口并终止发现。要让上面的插件成为「消息日志」页提供者，需要**移除内置日志插件**（它与 `MsgLogSource` 冲突）：

```go
func main() {
	bot := core.NewAniaBot()

	bot.AddPlugin(pluginsys.NewPluginSys())
	// bot.AddPlugin(pluginlog.NewPlugin())      // ← 移除：把「消息日志」页让给自定义插件
	// bot.AddPlugin(pluginaichat.NewAIChatPlugin()) // ← 若你的插件接管的是 AI 相关页面，同样移除
	bot.AddPlugin(pluginpoll.NewPlugin())

	bot.Run()
}
```

## 注意事项

- **方法缺一不可**：`adminpanel.XxxSource` 是完整接口，实现时必须实现全部方法（不需要的能力返回空值/错误即可）
- **nil 安全**：面板会在数据源为 nil 时返回空数据并正常渲染，插件内部初始化失败时返回 nil 即可，不要 panic
- **即时生效**：`MemorySource` / `SkillSource` / `KnowledgeBaseSource` / `TeamSource` / `QuotaSource` 的改动由插件自己落盘，**无需重启**；`TaskLogSource` / `MsgLogSource` / `QueryLogSource` 是查询接口，同样实时反映插件当前数据；`MsgLogDeleteSource` / `MsgLogSessionDeleteSource` 由面板提交后立即生效，插件负责自行持久化删除结果（用 `msglog.Recorder` 的 `Delete` / `DeleteSession` 即可）
- **数据量控制**：分页类接口遵循「新在前 + `before` 游标」约定，返回条数不要超过 `limit`；记录型组件（`msglog` / `tasklog` / `querylog`）内部已实现容量淘汰，直接复用即可
- **与 `bot.QQ` 同款惯例**：这是「可选接口 + 类型断言」能力的又一应用，探测不到就优雅降级（页面空数据），框架与面板代码零改动

## 下一步

- [从零开发一个完整插件](/plugin/tutorial) —— 把配置、存储、定时任务与面板服务串起来
- [插件系统概览](/plugin/overview) —— 生命周期与事件模型
- [配置注册](/plugin/first-plugin#进阶-声明自己的配置) —— 面板配置表单机制



