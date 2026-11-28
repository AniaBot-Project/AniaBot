# AI 引擎（三）工具、MCP 与高级编排

本章介绍 Agent 的**能力扩展与高级编排**：工具如何定义与执行、MCP 如何两阶段懒加载、Skill 系统、AI 定时任务、会话运行器与任务注册表、子代理与后台交接、跨回合目标续跑、工作流 DAG、团队任务板、看门狗、Agent 团队、知识库与配额、钩子与工具门禁。

## 工具系统

### 工具抽象

一个工具是嵌入 `llmtool.BaseTool[ParamsType]` 的结构体，参数结构体用 `json` 标签命名、`desc` 标签描述：

```go
type EchoParams struct {
    Text string `json:"text" desc:"要回显的文本"`
}

type EchoTool struct {
    llmtool.BaseTool[EchoParams]
}

func NewEchoTool() *EchoTool {
    return &EchoTool{BaseTool: llmtool.MakeBaseTool("echo", "回显文本", EchoParams{})}
}

func (t *EchoTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
    return params.(*EchoParams).Text, nil
}
```

### 反射自动生成 Schema

`parser.go` 用 Go 反射从参数结构体自动生成 OpenAI 兼容 function schema——**无需手写 JSON Schema**：

- 字段类型映射：int/float→`number`，bool→`boolean`，slice→`array`，struct→`object`，其余→`string`
- 无 `omitempty` 的字段自动进 `required`；嵌套 struct / slice 递归解析
- MCP 工具则直接把 `inputSchema` 转为 OpenAI 格式（补充 `type/properties` 默认值与描述、枚举描述、required 排序）

### 确定性输出

工具列表按**名称排序**输出：Go map 遍历顺序随机，若直接序列化会导致每次请求 tools 字段排列不同，把上游 prompt 前缀缓存（DeepSeek context caching）全部打失。工具未找到的错误文本、skill 列表等一切回填给 LLM 的文本也都保证排序确定性。

### 执行器分层：共享 + 会话

```go
type ToolExecuter struct {        // 共享层：内置工具 + MCP manager
    tools       map[string]Tool
    mcpManagers []*MCPToolManager
}
type SessionToolExecutor struct { // 会话层：每个会话独立
    shared       *ToolExecuter
    sessionTools map[string]Tool  // 动态加载的 MCP 工具 + 会话绑定工具
}
```

- 每个会话创建独立的 `SessionToolExecutor`，`mcp_load` 等动态工具互不影响
- 同名工具会话层优先（定义与实际执行一致，避免重复 function 定义被提供方 400 拒绝）
- `SessionToolExecutor` 用 RWMutex 保护会话工具表：同一轮多个工具并行执行，`mcp_load` 并发写与其他工具读不会触发 Go map 并发 fatal

### 内置工具注册层级

| 层级 | 内容 |
| --- | --- |
| `CreateDefaultTools()` | 常开：`time`、`get_msg_history`、`get_private_file_url`、`load_images`；配置门控：`bash`（黑白名单正则）、`send_file`、`local_image`、`computer_use` 工具组（`screenshot`/`mouse_click`/`mousemove`/`mouse_scroll`/`keyboard_type`/`keyboard_press`/`active_window`/`list_windows`，仅 Windows 宿主机注册）。**联网搜索/浏览不再内置**，改由内置 `web` 技能 + CLI 提供（见下节） |
| `CreateToolsWithMCP()` | 追加 MCP 工具（`mcpLazyLoad` 决定发现/加载模式或全量注册） |
| `CreateToolsWithSkill()` | 追加 `skill_read` / `skill_reload` 工具与 SkillManager（同时加载内置技能） |

另由 aichat 插件在会话层注册：`config_get`/`config_set`（配置中心读写，敏感字段掩码、仅注册键可写、重启生效——重启由管理员发送 `/reboot` 命令执行，AI 只负责引导）、`config_file_get`/`config_file_set`（扩展配置读写：`files.mcp_json`/`files.prompt_json`/`files.hooks_json`/`files.commands_json`，只校验 JSON 语法，hooks/commands/prompt 保存后数秒热生效、mcp 重启生效）、`mcp_list`/`mcp_add`/`mcp_remove`/`mcp_reconnect`（MCP 服务器自管理，写 `files.mcp_json` 持久化 + 运行时热注册/注销）、会话绑定的 clock/memory/knowledge/team/subagent 工具（`subagent_run/list/followup/send/cancel`），以及主会话的 `send_message`（执行中主动发消息：发送走 `SendText` 语义，`|||` 拆分多条、群聊 @ 发起人、合并转发等与普通回复一致；不做条数限制，靠工具描述约束发送时机——只同步关键节点、不逐步汇报、不能代替最终回复）、`task_handoff`（后台交接）、`goal_create/update/status`（SQL + `goal.enable`）、`workflow_run`（`subagent.enable`）、`team_task_create/list/claim/complete`（任务存储可用）。其中配置修改类工具（`config_set`/`config_file_set`）执行前恒需管理员审批（与审批开关无关，请求者本人不能批准）。

### 回调桥（CallBackFuncs）

工具不直接接触平台，而是通过回调桥发送消息：

```go
type CallBackFuncs struct {
    SendText          func(text string) (string, error)
    SendImage         func(bs64content string) (string, error)
    SendFile          func(name, bs64content string) (string, error)
    GetPrivateFileURL func(fileId string) (string, error)
    LoadImages        func() (string, error)
    TakeLoadedImages  func() []string
    LoadLocalImage    func(path string) (string, error)
    // 工具结果长度治理与读回（超长结果落盘，AI 用 read_tool_output 读取）
    LimitToolResult func(toolName, result string, isError bool) string
    ReadToolOutput  func(handle string, offset, limit int, fromEnd bool, pattern string) (string, error)
}
```

主会话的回调把文本/图片/文件经 `bot.Bot` 发到当前群/私聊；框架不再自动用 `SendText` 发送工具轮中间文本（非流式只发最终回复，流式由 `streamReply` 覆盖式展示），该回调保留给工具显式发送消息——主会话的 `send_message` 工具就是它在 AI 侧的薄封装。定时任务与子代理的回调则**丢弃 `SendText` 内容**（只记日志），只允许工具显式发送图片/文件，最终回复由编排层统一推送。

### 工具结果长度治理（落盘 + read_tool_output）

所有工具回填给 AI 的结果都在 `ToolOrchestrator.executeToolCalls` 统一治理（只有这一个出口，内置/会话/平台/MCP 工具一视同仁），避免单个工具返回 MB 级内容把一轮工具循环直接撑爆，或永久留在窗口历史里逐轮重发：

- 成功结果 rune 数 ≤ `plugin.ai_chat_bot.tool_result.max_inline_runes`（默认 16000）→ 原样回填
- 超过上限 → 完整内容写入会话临时文件（`<dir>/<sha256(会话键)[:8]>/<handle>.txt`，默认 `./data/tool-outputs`），回填「预览（默认 4000 字符，头 3/4 + 尾 1/4）+ handle + read_tool_output 读取方式」；Query 日志与 PostToolUse 钩子拿到的是治理后的文本
- 错误结果 **不落盘**，只做头尾截断（模型必须当轮看到失败原因）；落盘失败/功能关闭同样退化为头尾截断
- `read_tool_output` 是共享层工具（落盘启用时注册），支持 `offset`/`limit`/`from_end`/`pattern`（字面子串，非正则），自身输出有硬上限（8000 rune / 2000 行 / 单行 2000 rune），且**豁免**再次落盘；handle 只能读取所属会话目录，过期（默认 24h）提示重新执行原工具
- 落盘文件由后台 janitor 按 `retention_hours` / `max_total_mb` 清理；`#新对话` 与未@自动清空历史时顺手删除对应会话目录
- 原 `bash.max_output`、MCP 8000 rune、`skill_read` 16000 rune 的**工具内截断已移除**：内容在工具内部被截掉后统一层再也拿不回原文，统一交给本机制处理

## MCP 两阶段懒加载

MCP（Model Context Protocol）接入通过 `modelcontextprotocol/go-sdk`，支持三种传输：**stdio**（子进程）、**Streamable HTTP**、**SSE**。

```mermaid
sequenceDiagram
    participant AI as LLM
    participant E as ToolExecuter
    participant M as MCPToolManager
    participant S as MCP Server

    Note over E,M: 启动时（Initialize）
    E->>M: 连接 + ListTools
    M-->>E: toolDefinitions 全部元数据
    E->>E: 注册 mcp_discover_<server>（共享层）<br/>注册 mcp_load_<server>（每个会话）

    Note over AI,M: 会话中（两阶段）
    AI->>E: 调用 mcp_discover_<server>
    E-->>AI: 工具名 + 简短描述列表
    AI->>E: 调用 mcp_load_<server>(tool_name)
    E->>M: LoadTool(tool_name)
    M-->>E: MCPTool（缓存）
    E->>E: RegisterSession 到会话执行器
    AI->>E: 直接调用该 MCP 工具
```

**为什么两阶段**：MCP 服务器可能暴露几十上百个工具，每个工具的描述都会进入 LLM 请求的 tools 字段——全量注册会让上下文爆炸。发现工具只读无副作用，加载按需进入会话；加载的工具在同一会话后续轮次中直接可用，与内置工具同权。

`plugin.ai_chat_bot.mcp.lazy_load`（默认 `true`）控制是否使用该模式：关闭后启动时全量注册所有 MCP 工具（`RegisterMCP`），工具列表恒定、上游 prompt 缓存命中率更高，但工具较多时上下文开销大。

其他细节：

- MCP 工具结果与其他工具一样经过统一长度治理：超过 `tool_result.max_inline_runes` 时完整内容落盘、回填预览 + handle，由 AI 用 `read_tool_output` 读回（工具内不再单独截断）
- 连接 HTTP 客户端不在整体 `http.Client.Timeout` 上设超时（会中断 SSE 长连接持续读取），改为 Transport 层 `DialContext` + `ResponseHeaderTimeout` 保护握手阶段
- 可选 `ToolFilterFunc`（前缀/名单/关键词/组合过滤器）在连接时过滤工具
- **运行时管理**：`ToolExecuter` 以 RWMutex 保护共享工具表与 manager 列表，支持启动后 `AddMCP` / `RemoveMCP` / `ReconnectMCP`（AI 的 `mcp_add` 等工具依赖此能力；重连后已加载到会话的旧工具句柄失效，需重新 `mcp_load`）

## Skill 系统

Skill 把领域知识封装成 `SKILL.md`（支持 frontmatter 的 `name` / `description`），AI **按需阅读**而非全量塞入：

- 目录结构：`skills/<name>/SKILL.md`（可带 reference.md、script.sh 等附属文件）或单文件 `skills/SKILL.md`
- **内置技能与外置技能**：内置技能随二进制发布（`bot/component/llmtool/builtinskills`，`//go:embed`），加载时物化到 `<skillsDir>/.builtin`（隐藏目录，脚本有可执行权限供 `bash` 调用）；内置技能**不可删除、不可禁用、不受 `skills` 白名单限制**。外置技能从 `skillsDir` 加载，可在面板「技能管理」页启用/禁用——禁用后描述不进入 `available_skills`，`skill_read` 也拒绝读取；禁用列表持久化在 `plugin.ai_chat_bot.skills_disabled`
- **内置 `web` 技能**：自带 CLI `webctl.py`，支持 DeepSeek / Tavily / Exa / Firecrawl / Jina AI 五个搜索抓取平台（`--deepseek` / `--tavily` / `--exa` / `--firecrawl` / `--jina`），AI 先 `skill_read` 拿到脚本绝对路径，再用 `bash` 执行 `search` / `fetch` / `providers` 子命令；需要 `bash` 工具与至少一个平台 Key 环境变量（`DEEPSEEK_API_KEY` / `TAVILY_API_KEY` / `EXA_API_KEY` / `FIRECRAWL_API_KEY` / `JINA_API_KEY`）。不带平台参数时脚本自动检测已配置的 Key，按 Tavily → Exa → Firecrawl → Jina AI → DeepSeek 的优先级选择（`WEB_SEARCH_PROVIDER` 可覆盖），`providers` 子命令列出各平台配置情况。新增搜索平台只需在 CLI 的 `PROVIDERS` 注册表增加实现，平台参数、环境变量自动检测与 `providers` 列表都会自动生成，无需改 Go 代码
- **内置 `aniabot` 技能**：让 AI 了解自己跑在什么框架上——组成与数据落点、配置键与生效时机、Web 面板各页、会话指令、插件/技能/MCP 机制与排障路径，用于回答「怎么配置 / 为什么没生效 / 要重启吗」并指导用户操作。主体 `SKILL.md` 只放高频结论，三份参考文档（`reference/config.md`、`reference/panel.md`、`reference/troubleshooting.md`）按需 `skill_read`；它不引入新机制，只是把框架事实写进上下文，**改配置键、面板页面或指令时要同步更新**（有测试校验内置技能可加载与参考文档可读）
- `SkillManager` 启动时加载，`Reload` 原子替换注册表（面板上传/删除/启停后热更新）并保留内置技能与禁用状态；常驻的 `skill_reload` 工具走 `Refresh`（按最近一次的目录与白名单原地重载），供 AI 经 `bash` 等直接编辑本地 skill 文件后刷新缓存
- system prompt 注入 `available_skills` 注册表（名称 + 一句话描述），模型判断需要时调用 `skill_read` 读取完整内容
- 输出确定性：skill 列表按名称排序（作为工具结果文本回填给 LLM）
- **AI 自管理技能**：开启 `plugin.ai_chat_bot.skill_tool.enable`（默认关闭）后，会话注册 `skill_list` / `skill_install` / `skill_remove` 三个工具——找资源不做专用搜索工具（GitHub API 搜索有频控），AI 可直接用内置 `web` 技能联网搜索技能仓库或 zip 直链，再从 zip 直链 / GitHub 仓库（自动转 codeload zip）/ SKILL.md 直链下载安装，或直接撰写 SKILL.md 全文创建技能；安装/卸载复用面板同款磁盘逻辑（zip-slip / 体积 / 校验防护，内置技能不可删除、名称冲突会被拒绝），热重载立即生效，操作记入操作日志
- **AI 自管理 MCP 服务器**：开启 `plugin.ai_chat_bot.mcp_tool.enable`（默认关闭）后，会话注册 `mcp_list` / `mcp_add` / `mcp_remove` / `mcp_reconnect` 四个工具——添加/删除经 DI 注入的 `ConfigEditor` 写入 `files.mcp_json` 持久化（名称校验满足 LLM 工具名规范，环境变量/请求头以 `KEY=VALUE` 列表传参），同时调用 `ToolExecuter.AddMCP` / `RemoveMCP` 运行时热注册/注销立即生效（持久化失败会回滚运行时注册；删除时运行时未注册的服务器容忍注销错误，保证配置层面删除总可用）；`mcp_reconnect` 对启动时连接失败从未注册的服务器会从配置读取定义重新注册

## AI 定时任务（clock）

与框架 `StartCron` 静态任务不同，clock 任务由 AI / 用户**动态创建**、持久化保存、重启不丢：

- `clockManager` 拥有**独立 `*cron.Cron`**（不与框架共享），任务存 `clock:` 命名空间（`task:<id>` + `index` + `seq`）
- 支持 5 字段 cron、`@every` 等、单次任务（`run_once` 执行后自动销毁）、按目标（群/好友）隔离

### 触发执行

```mermaid
flowchart TB
    A[cron 触发 / RunNow] --> B{上一次还在跑?}
    B -->|是| C[跳过本次触发]
    B -->|否| D[b.Go 启动执行]
    D --> E[记录任务日志 running]
    D --> F[配额检查]
    F -->|超限| G[跳过并告知用户]
    F -->|通过| H[构建一次性 ChatBot<br/>nil historyStore]
    H --> I[触发 prompt:<br/>【定时任务】标题 内容]
    I --> J[完整工具循环]
    J --> K{委派了异步子代理?}
    K -->|是| L[drainClockSubagents 等待全部<br/>结果回喂 AI 合成最终回复]
    K -->|否| M{调用了 skip_output?}
    L --> M
    M -->|是| N[最终回复只进日志，不推送]
    M -->|否| O[只推送最终回复]
    N --> P[任务日志 success / timeout / error]
    O --> P
```

- 每次触发**全新一次性上下文**（nil historyStore，执行完丢弃），带完整工具能力
- 超时 `clock.default_timeout_sec`（默认 120），超时后发消息告知用户
- 执行日志走 `bot/component/tasklog`（`ania_task_log` 行级），记录工具调用明细、token 用量、最终回复；进程重启后遗留的 running 记录标记为 `interrupted`
- 中间轮文本丢弃，只有最终回复推送给目标；工具显式发送的图片/文件正常发出
- `skip_output` 工具（`clockskip.go`，仅在任务会话注册）：AI 判断本次无需汇报时调用，收尾输出（含子代理结果汇总后的合成轮）只记入执行日志、不推送给目标，供巡检/监控类任务保持安静；跳过标记与原因随任务日志落盘（面板与 `/clock log` 可见）
- 播报提示（`noteDelivered` / `deliveryHint`）：成功推送（最终回复、工具发出的图片/文件）后按目标会话记一个投递序号；用户在该会话的下一轮发言里，提示以尾部注入的方式拼在用户消息前（请求成功后才 `commitDeliveryHint` 记账，失败轮次会重新注入；`PersistUserText` 保证它不进入历史回放），指引 AI 先用该会话可用的 `*_get_msg_history` 工具确认播报内容——任务会话与主对话的历史互不相通，不提示的话用户对着播报追问时 AI 只能反问
- 用户经 `/clock` 管理，AI 经 `clock_create/list/update/delete/log` 工具管理

## 会话运行器与任务注册表

AI 消息入口不再在框架事件 goroutine 里跑完整个回合（`runner.go`）：`OnGroupMsg`/`OnFriendMsg` 只做去重、自过滤、命令与 mention 门，然后把消息入队并立即返回；后台 goroutine 按会话串行接管队列。

- **入口零阻塞**：`submitTurn` 入队后 `b.Go("runner:<session>")` 唤醒运行器；同一会话同一时刻只有一个回合（会话锁），跨会话并发受 `rate_limit`（默认 4）槽位限制。长回合不再占用框架事件 worker，也不受 `bot.msg_event_timeout_sec` 约束。
- **回合上下文**：`context.WithoutCancel(parent)` 保留 Value（追踪、图片注册表）但丢弃框架 deadline；关联插件生命周期 `p.runCtx`（`OnUnload` 取消）；`turn.hard_timeout_sec`（默认 14400s，0=不限）叠加硬截止。
- **排空循环**：每批处理完继续 drain 队列；批次执行期间到达的消息由工具循环在每次 LLM 请求前经 `ChatOptions.Steer` 注入当前对话（见下节），没来得及注入的剩余消息由运行器作为下一批处理。出错/取消时该批用户消息之外的行为由错误处理决定（`/stop` 是显式停止，会清空该会话排队队列）。一个回合**正常结束**后运行器调用 `maybeDriveGoal` 尝试推进当前会话的长任务目标（无排队消息、无运行中子代理、目标 active 且已武装时才开下一轮）。
- **任务注册表**（`taskstore.go` + `tasktrack.go` + `teamlog.go`）：后台工作（异步子代理、交接、工作流、追问、团队任务板、团队运行/成员）各写一行 `ania_agent_task`（`id` 形如 `t-<36进制纳秒>-<4位hex>`，含 `session_key/parent_id/kind/status/title/prompt/result/error/stop_reason/tokens/meta/时间戳`），kind 取值 `subagent|handoff|workflow|followup|team_task|team_run|team_member`（`turn` 仅为旧库遗留类型，新回合不再写入），状态机 `queued → running → completed|failed|timeout|cancelled|interrupted`，停止原因 `completed|aborted|max_tokens|error|watchdog|timeout|user_stop`。普通回合的完整过程由 Query 日志记录，避免两套表重复；`/tasks` 的「最近回合」来自进程内 `recentTurnLog`（每会话 5 条，重启即清空）。任务表写入走 3 秒独立 ctx，DB 卡住不影响回合；建表失败降级为不持久化（不阻断启动）。同一条启动路径还建 `ania_agent_goal`（长任务目标表，`session_key` 主键），建表失败时目标功能同样自动降级。
- **重启恢复**：启动时 `MarkInterrupted` 把残留的 `queued/running/waiting` 标记为 `interrupted`；默认不自动续跑，用户发 `/resume <任务ID>` 把本会话的 `interrupted` 子代理/交接任务重新入队（任务目标作为系统来源消息），由主 AI 带着会话历史继续。
- **清理**：按 `task.retention_days`（默认 7 天）每 6 小时删除已结束且过期的任务记录。
- **用户命令**：`/tasks`（本会话运行中后台任务 + 最近后台任务 + 最近回合，含状态/耗时/类型/停止原因/token）、`/stop`（取消当前回合并清空本会话排队、暂停自动续跑目标）、`/stop <任务ID 或名字>`（只取消指定后台任务：子代理、团队成员 ID 只取消该成员，团队运行 ID 取消整队）、`/resume <任务ID>`、`/goal [status|pause|resume|clear]`。
- **面板**：`taskpanel.go` 实现 `adminpanel.TaskSource` / `TaskAdminSource` / `GoalSource`：展示最近后台任务（`GET /api/agent-tasks`，默认 100 条、上限 500；返回 `parent_id` 与 `prompt`）与 active 目标（`GET /api/agent-goals`），对应面板「AI 运行记录 → 后台任务」页签；`AgentTasksExport` / `AgentTasksClear` 支撑 `GET /api/agent-tasks/export`（按状态/类型/关键词导出全部命中记录）与 `DELETE /api/agent-tasks`（只清除已结束行，运行中/排队中保留），对应页面右上角的导出/清除按钮。`AgentTasks` 的 SQL 用 `kind <> 'turn'` 排除升级前的历史回合行（普通回合的完整过程在 Query 日志），并支持 `?task=` 深链定位。团队运行（`team_run` 父记录）、团队成员（`team_member` 子记录）、追问（`followup` 子记录）与异步子代理的详情里可直接看到任务指令、成员产出摘要与工具调用步骤（`meta.tool_calls`；参数/结果完整保存、不截断，与 Query 日志同口径，明细也不限条数）；详情弹窗把 `parent_id` 与子行解析成可点击的追问/任务链；工具步骤在执行中即按约 3 秒节流增量落盘（`taskStore.UpdateProgress` 只改 `meta`/`updated_at`，`finished_at = 0` 保证迟到的进度写不会覆盖终态），面板轮询即可看到进度，收尾时再写全量。任务行点击弹出详情窗（与 Query 日志一致，避免就地展开占高度），Query 日志详情里的 `task_id` 可跳回本页。

### 运行中插话（next-step 注入）与 send_message

回合执行期间到达的新消息不再等整轮结束才处理：`processChatBatch` 给本次请求挂上 `ChatOptions.Steer`（`interject.go` 的 `buildInterjectionSteer`），工具循环在**每次发起 LLM 请求前**排空该会话的待处理队列，把消息作为 user 消息追加到当前对话尾部，模型下一步即可看到并回应；正在生成的回复与正在执行的工具批都不受打断。

- **两种前缀**：用户消息加 `【用户插话】你正在处理当前任务…请在本轮内纳入考虑并直接回应…` 前缀（群聊仍带 `[nickname:… id:…]` 发送者前缀，文案也引导「需要立刻回复时先用 `send_message` 回一条再继续」）；合成消息（`Sender.UserId=0`，如异步子代理完成通知、工作流汇总、`/resume` 恢复提示）加 `【后台消息】…` 前缀，不冒充用户发言。
- **逐条注入、同批下发**：一次排空到的多条消息（无论来自同一人还是不同人）按到达顺序**逐条**作为独立 user 消息注入，各自保留【用户插话】/【后台消息】前缀与发送者信息，不会先拼成一条长文本（便于模型区分谁说了什么、也便于历史追溯）；但它们在同一次「下一次 LLM 请求前」一起进入上下文，不会为每条消息单独触发一次模型请求。
- **剩余消息是下一批**：注入只发生在两次模型请求之间；最后一批 LLM 请求开始后才到达的消息留在队列里，由同一会话运行器在本批结束后作为**下一批**处理（正常路径不丢失、不重复消费）。`/stop`、取消与错误路径维持旧行为：清空待处理队列。`#新对话` 属于批次级语义（整批开始前清空历史）不能在回合中途执行，遇到时连同其后的消息一起放回队列、留到下一批处理。消息**到达时**（`submitTurn`）即先停止该会话运行中的异步子代理/工作流与团队运行，并清空已完成条目的追问窗口（`stopSessionBackground`），批次开始时再兜底停一次，避免旧后台任务的结果回灌进新对话；进行中的长任务目标也会被暂停（`/goal resume` 可恢复），否则回合结束后会立刻自动续跑旧目标。历史清空、动态 MCP 工具卸载、落盘结果删除与会话 token 换新仍在批次开始时执行。
- **图片可加载**：插话消息中的图片登记到本轮请求的图片注册表，`load_images` 可按哈希加载。
- **Query 日志可审计**：注入的插话在注入时追加到本次 Query 日志的 `Query` 字段（原始输入保留，每段以 `【处理中插话】` 开头、带发送者前缀；经 `Logger.Update` 增量落盘，与工具明细的增量写互不覆盖），面板「AI 运行记录 → 对话记录」、导出 JSON 与关键词筛选都能看到。
- **前缀归属**：`ChatOptions.Steer` 的契约是「回调返回的文本原样作为 user 消息追加」，编排层不再追加 `[追加指令]` 前缀——子代理 steering 的 `[追加指令]` 由 `subagent.go` 的 `steerDrain` 自己加，主会话插话加 `【用户插话】`/`【后台消息】`。
- **`send_message`**：主会话工具（`sendtool.go`，`getChat` 注册），参数 `text`，经既有 `SendText` 回调发送，因此 `|||` 拆分多条、群聊 @ 发起人、合并转发阈值等语义与普通回复一致；不做条数限制（长任务关键节点该同步就同步），发送时机由工具描述约束：只发有信息量的进展、不逐步汇报、不能代替最终回复；发送后 AI 必须继续执行并照常给出最终回复。不注册给子代理、定时任务与面板试聊室，计划模式不拦截（不是副作用操作）。

## 子代理（subagent）

`subagent_run` 让主 AI 委派复杂/耗时子任务给**一次性子代理**（`subagent.enable` 默认开启；关闭后不注册子代理与交接工具）：

- 子代理是全新 `ChatBot`（nil historyStore，执行完丢弃），独立 `SessionToolExecutor`，拥有与主 AI 一致的会话级工具（clock/memory/knowledge），但**不再注册 subagent 自身与团队工具**（防递归，深度天然为 1）
- 中间轮文本丢弃；发送图片/文件、读历史仍作用于当前会话
- 图片状态隔离：子代理有自己的图片队列，`LoadImages` 是 stub（推迟给主 AI），避免两个工具循环互踩队列
- 执行参数全部跟随主模型：模型与 API 格式直取主模型四元组（`subagentLLMConfig`），生成参数经 `buildSubagentChatOptions` 复用主模型配置，工具轮数取 `mainMaxIterations()`（主模型的最大工具调用轮数）
- **异步语义**：工具启动受跟踪的 goroutine（`b.Go`）后立即返回任务 ID；完成时格式化结果、作为合成消息入该会话待处理队列并唤醒运行器——主回合若仍在运行，会在其下一次 LLM 请求前以 `【后台消息】` 注入当前对话；否则由主 AI 在后续回合处理。主回合不等待、不轮询。回灌消息首行是机器可读状态：`【子代理执行完成】状态=completed`，失败/超时为 `【子代理执行结束】状态=failed` / `状态=timeout`，随后附 `任务ID`、（有名字时的）`名称` 与（追问产生的）`父任务ID`；被 `/stop` 取消的任务不回灌
- **结果治理与完成态保留**：异步路径的即时回灌按 `subagent.result_inline_runes`（默认 8000）处理——超长结果由 `toolResultLimiter.LimitAt` 落盘为会话私有文件，合成消息只带摘要 + handle，主 AI 用 `read_tool_output` 读取。内存中的完成条目保留**完整结果**（不受内联上限影响），以便追问时重建上下文；保留时长 `subagent.finished_ttl_min`（默认 30 分钟，0 关闭），过期由常驻 janitor 清理，`#新对话` / 面板清空上下文会立即丢弃整个窗口。
- **追问（followup）**：`subagent_followup(id, message, timeout_sec?)` 对已完成条目启动一个新的异步子代理，任务文本 = 原任务 + 上一轮结果（按内联上限治理，超长落盘）+ 追加追问，并明确要求「先利用已有信息回答、信息不足再取数、不要整段重做」。新任务 `kind=followup`、任务行 `parent_id` 指向原任务，完成后通知带 `父任务ID`，面板可展开追问链；团队成员复用原角色提示词。`subagent_send` 命中已完成条目时自动走同一追问语义。`subagent.max_followups_per_task`（默认 3，0 不限）按追问链根任务计数，避免对同一任务无限续问。`subagent_list(include_finished=true)` 展示完成条目的状态、结果摘要与剩余可追问时间。
- **并发边界**：每会话 `subagent.max_concurrent_per_session`（4）与全局 `subagent.max_concurrent_global`（8）两个信号量，全局池与对话回合的 `rate_limit` **分开计算**；超出直接返回可读错误，不静默排队。`/stop` 与 `subagent_cancel`/`/stop <ID>` 按 ID **或名字**取消；`OnUnload` 取消全部后台子代理
- **命名与运行中通信**：`subagent_run` 可传 `name`（1~32 位、小写字母开头，只含小写字母/数字/连字符，会话内运行中唯一），`subagent_list` 按 ID + 名字展示；`subagent_send(id|name, message)` 把指令写入该子代理的 steering 通道（8 格缓冲），子代理在下一次 LLM 请求前以 `[追加指令]` 注入、不打断当前工具批；`subagent_cancel(id|name)` 取消单个。完成后的条目不再接受 steering/取消，只接受 followup。结构化返回目前是「状态 + 任务 ID + 名称 + 结果正文」，没有独立的 JSON `artifacts` 字段，也没有子代理会话历史落库/跨重启续跑（规划中的 P2 剩余项）。

### 超时预算

```go
func resolveSubagentTimeout(defaultTimeout time.Duration, timeoutSec int, parentCtx) (timeout, error) {
    // 1. 默认超时 = subagent.default_timeout_sec（默认 0=不限制；可配正数秒数）
    // 2. timeoutSec>0 时覆盖默认值，先限幅 int 再乘 time.Second（防溢出）；业务上限 subagent.max_timeout_sec（0=不限）
    // 3. 仅当父上下文真有 deadline（父回合硬截止）时，预留 30s 收尾并压缩到剩余预算；不足时缩到剩余量一半、保底 10s
}
```

超时与框架消息事件预算**彻底解耦**：`bot.msg_event_timeout_sec` 只约束框架事件入口，不再作为子代理默认超时。子代理自身超时以错误结果返回，让主 AI 用剩余时间收尾；`/stop` 通过异步子代理组的 cancel 取消。

### 定时任务专属：异步子代理

clock 任务里注册的是**异步**子代理变体（`clocksubagent.go`）：子代理在后台并发执行（`subagent_run`/`subagent_list`/`subagent_cancel`），任务收尾时 `drainClockSubagents` 等待全部完成（预留 30s 合成预算），把结果回喂给同一个一次性 ChatBot 做多轮合成（上限 5 轮），只推送最终合成的回复。子代理超时随剩余预算收缩。

## 后台交接（task_handoff）与看门狗

- **`task_handoff`**（主会话、`subagent.enable=true` 时注册）：模型把「已完成摘要 `summary` + 剩余工作 `remaining` + 验收标准 `acceptance`」交给后台子代理继续，当前回合立即结束。工具内部把三段拼成自包含的后台任务提示词，复用异步子代理通道（kind=`handoff`），完成后结果照常回到会话。主会话的软截止提醒会明确引导模型调用它，把「超时」变成「换一个地方继续做」；没有该工具的会话由插件层换成通用收尾措辞。
- **委派提醒**：同一回合工具轮数达到 `turn.progress_reminder_after`（6）时注入一次「考虑用 `subagent_run` 拆出独立部分」提醒。只有注册了 `subagent_run` 的会话（主会话、定时任务）使用点名工具的文案，子代理自身、主动互动与面板试聊改用不带工具名的通用提醒，避免诱导模型调用本次没有注册的工具。
- **重复/无进展看门狗**（`bot/component/aichat/toolwatchdog.go`）：同一轮工具调用的签名 = 排序后的 `name+args`；签名连续不变时按 `turn.repeat_tool_warn`（`3,5,8`）各注入一次换方案提醒，达到 `turn.no_progress_limit`（12）时返回 `aichat.WatchdogAbortError` 终止工具循环（`0` 只提醒）；提醒以 user 角色追加并随本轮返回进入历史，后续回合模型仍能看到。
- **单请求内上下文保护**：每步 LLM 返回的 prompt token 超过窗口 85% 时，把较旧的大工具结果裁剪为「标记 + 原始长度 + 头尾片段」（保留最近 6 条消息与 tool_call 配对），窗口级压缩仍由回合开始时的 `messageWindow` 负责。

## 长任务目标（goal）

`goaltool.go` / `goalstore.go` 把「多轮继续」从用户/模型行为变成**运行时驱动**：AI 用 `goal_create(objective, max_rounds?)` 登记一个跨回合目标，之后由运行器在每个回合正常结束后按需开启下一轮。

- **持久化**：每个会话同一时刻最多一个目标，存 SQL 表 `ania_agent_goal`（`session_key` 主键，`objective/phase/rounds_started/max_rounds/blocked_code/blocked_message`）；工具只在 `goal.enable=true` 且 SQL 可用时注册，建表失败则目标功能降级关闭。
- **状态机**：`active` / `paused` / `blocked` / `complete`。`goal_update` 支持 `complete`（可附 `evidence`）、`blocked`（必须给 `reason`）、`pause`、`resume`、`clear`；`blocked` 语义相对 DSH 简化——不要求「同一阻塞条件连续 N 轮」，给原因即停。轮数用尽后 `resume` 会被拒绝。
- **续跑驱动**：进程内 `goalArmed` 标记（`goal_create` 或 `/goal resume` 置位）；回合正常结束 → `maybeDriveGoal` 检查「已武装、无排队消息、无运行中子代理、phase=active」→ `rounds_started+1`、写库、把 `<goal_round>第 N/M 轮…</goal_round>` 合成消息入队并启动运行器。达到 `max_rounds` 时自动转 `blocked(round-limit)` 并通知会话。
- **重启语义**：`goalArmed` **不持久化**，进程重启后目标仍 active 但不再自动续跑，需用户 `/goal resume`（与 DSH disarm 一致，避免升级重启后自动烧 token）。
- **用户命令**：`/goal [status|pause|resume|clear]`；`/stop` 会 `pauseGoalOnStop` 把 active 目标暂停。计划模式阻断 `goal_create`/`goal_update`，`goal_status` 放行。
- **并发**：目标状态的「读-改-写」由 `AIChatPlugin.goalMu` 串行化。三类调用来自不同协程——`/goal` 命令在框架事件协程、`goal_create`/`goal_update` 工具在回合协程、续跑驱动在后台协程；若读与写之间被插入一次 `clear`（`DELETE`），基于旧快照的 `Upsert` 会走「UPDATE 0 行 → INSERT」兜底把目标复活，`pause`/`resume` 也会被旧快照覆盖。锁内只做 DB 读写，发送与入队都在锁外。回归用例 `TestGoalClearSerializedWithDrive` 用阻塞存储复现该交错。

## 工作流（workflow）

`workflow.go` 把「多步、有先后依赖」的委派打包成一次调用：`workflow_run(goal?, steps=[{id, task, depends_on}], concurrency?)`。

- **校验（纯函数）**：1~8 步；步骤 ID 1~32 位字母/数字/下划线/连字符且唯一；`task` 非空；依赖存在、不能自依赖、DAG 无环（拓扑排序）。`concurrency` 只做归一化（≤0→2，>4→4）。
- **执行**：通过校验后登记任务表（`kind=workflow`）并注册进会话的异步子代理组（`/stop`、`OnUnload` 可取消），后台 goroutine 按拓扑序调度：每个就绪步骤取一个全局子代理信号量名额、复用一个后台子代理；失败步骤的传递后继标记 `skipped`，无依赖关系的分支继续。整体状态 `completed`（全部完成）或 `failed`（有失败/跳过），取消时按 `cancelled`/`timeout` 落库且不向会话注入失败消息。
- **回灌**：全部步骤收敛后 `formatWorkflowSummary` 生成 `【工作流执行完成】状态=…` 的合成消息（含每步状态与结果摘要、汇总 token），入 pending 队列并唤醒主 AI；有失败/跳过时任务表记错误，但汇总消息照常回灌，由主 AI 决定重试或拆小。
- **门控**：仅 `subagent.enable=true` 时在主会话注册；计划模式阻断。

## 团队任务板（team_task）

`teamboard.go` 复用任务表做一份**会话级持久任务板**（`kind=team_task`），把多步骤工作拆成可认领、可依赖的条目：

- **工具**：`team_task_create(title, description?, blocked_by?, write_scopes?)` / `team_task_list(include_completed?)` / `team_task_claim(id, owner)` / `team_task_complete(id, result?, owner?)`。
- **Meta JSON**：`owner` / `blocked_by` / `write_scopes` / `result` 存在任务行 `meta` 列；`write_scopes` 仅作为建议（advisory）记录与展示，不做强制文件锁。
- **依赖语义**：`blocked_by` 必须是同会话已存在的团队任务；依赖未全部 `completed` 时 `claim` 返回可读原因；`complete` 返回因此解除阻塞、当前可认领的后续任务 ID。`owner` 校验防止他人代完成。
- **边界**：每会话最多 20 个未完成任务，列表单次最多 50 行；任务板需要 SQL 持久化，未启用时各工具返回可读提示。计划模式阻断 create/claim/complete，`team_task_list` 放行。

## Agent 团队（team）

在子代理之上的一层编排：AI 可保存/调用「团队」——一组带角色描述的子代理成员（默认启用，无配置开关）：

- 团队按 scope（`g:` / `f:` / 全局 `global`）隔离，`team:` 命名空间 KV 存取；团队名严格校验（中文/字母/数字/下划线/连字符）
- `team_run` **异步**启动多个成员并行执行（上限 `teamMaxMembers`=5，防并发风暴），每个成员复用 `runSubagentWithOptions` 并只传角色提示词——模型与执行参数（超时/轮数）全部与普通子代理一致（即跟随主模型）。工具立即返回团队任务 ID 与成员 ID（`<runID>-m1`、`<runID>-m2`…）；运行中可用 `subagent_send` 按成员 ID 定向注入、按团队 ID 广播全队，`subagent_cancel`/`/stop <ID>` 取消单个成员或整队
- 全部成员结束后汇总报告作为 `【Agent 团队执行完成】状态=completed|failed` 合成消息回灌主 AI（含任务 ID、成员 ID 与各成员状态），主回合不等待、不轮询
- **完成态与追问**：团队与每个成员都登记进会话后台工作表，完成后在 `subagent.finished_ttl_min` 内保留；成员 ID 可 `subagent_followup`（沿用成员角色提示词），团队 ID 可基于整队报告继续追问。`#新对话`、面板清空上下文与 `OnUnload` 会取消运行中的团队并清空追问窗口
- 团队定义可保存复用（`team_create/list/delete`），面板提供管理页
- **运行记录**：每次 `team_run` 写一条 `kind=team_run` 父记录（总体任务、汇总报告、成员合计 token/耗时、成员摘要与成员 ID），每个成员写一条 `kind=team_member` 子记录（任务行主键即成员 ID `<runID>-mN`，`parent_id` 指向父记录，含任务指令、成员产出、状态、耗时与工具调用步骤；执行中按约 3 秒增量刷新），追问产生的任务行 `kind=followup` 且 `parent_id` 指向来源任务；面板「AI 运行记录 → 后台任务」页签点击行可查看，并可在详情里沿父/子 ID 展开追问链；taskStore 不可用时整体降级为不记录
- 团队成员的一次性会话不注册团队工具（防递归组建团队）

## 知识库（knowledge）

`kb_add` / `kb_search` 等工具让 AI 把完整资料存入知识库并按需检索：

- 作用域：会话库（`g:` / `f:`）+ 全局库（`global`，所有会话可检索，仅面板可管理）
- **长文档切片**：入库时按 600 字符一块、60 字符重叠切片，检索命中块而非整篇，避免无关内容占用上下文
- 向量检索可选（与长期记忆共享 `embedder`）：每块一个 embedding（float32），检索按余弦相似度加分；embedding 服务不可用自动退化为纯关键词
- 去重（标题+内容规范化）、上限（`kb.max_docs`）、截断（8000 符文）
- **行级存储**：每篇文档一行（`ania_kb_doc`，`(scope,id)` 联合主键，tags/embedding 为 JSON 列），增删改只写单行；旧版「每作用域一个 JSON 数组」的 KV 数据在启动时一次性迁移入表，探测或建表失败时该功能禁用

## 配额与用量统计

- **quotaManager**：按「每会话每日」+「全局每日」两个维度限制 token 消耗，键 `daily:<日期>:<会话key>` 天然按天过期；Check-Add 为宽松语义（非硬实时），面板可查看用量
- **usageAcc**：goroutine 安全的派生用量累加器，归集主循环之外的消耗（异步子代理、team 成员、备用识图），收尾并入统计与配额
- **Query 日志**：一次「触发 → 最终响应」的完整记录（`ania_query_log` 行级），含用户输入、发送者、工具调用明细（工具结果记录统一长度治理后的文本，超长结果以预览 + handle 落盘）、token、状态（running/success/skipped/stopped/timeout/error），日志不淘汰，面板「AI 运行记录 → 对话记录」页签按条件筛选并支持导出/清除；主动互动的执行同样落在这里（`kind=proactive`，以触发说明替代用户输入，保持安静时状态为 skipped 并在 note 记录原因），Token 统计按来源单列。发送者列表忽略合成消息（`qq:0`，如子代理完成通知），整批都是合成消息时回退为会话目标 ID。普通对话回合回填 `stop_reason`（completed/aborted/max_tokens/error/watchdog/timeout/user_stop）；`task_id` 为历史关联字段（普通回合已不再写任务表，新记录为空），面板详情可跳转「AI 运行记录 → 后台任务」页签，并支持 `GET /api/querylogs?task_id=…` 过滤。两列在老库上通过幂等迁移（SQLite PRAGMA / MySQL information_schema 探测后 ALTER）补出

## 钩子系统（hooks）

`bot/component/agenthook` 提供会话生命周期钩子，两种形态共存：

- **Shell 钩子**（管理员配置）：面板「配置管理 → 扩展配置」编辑 `files.hooks_json`，在事件触发时执行配置的命令。载荷经 **stdin 以 JSON** 传入（`hook_event_name/session_id/agent_kind/tool_name/tool_input/tool_result/prompt`），退出码语义对齐 Claude Code：**0=通过**（stdout 作为上下文注入）、**2=阻断**（stderr 作为原因）、**其他=非阻断错误**（仅记日志）；超时（默认 10s，按条可覆盖，上限 60s）按非阻断错误处理
- **Go 钩子**（插件开发者）：插件实现 `agenthook.Handler` 接口（`OnAgentHook(ctx, event, payload) Result`），core 在启动后收集所有实现者，经 `HandlerRegistry` 扇出注入 pluginaichat；Go 钩子先于 shell 钩子执行，panic 自动隔离

事件一览：

| 事件 | 时机 | 可阻断 |
| --- | --- | --- |
| `SessionStart` | 会话（重）创建 | 否；产出的上下文在下一轮对话注入一次 |
| `UserPromptSubmit` | 用户消息进入对话前 | 是（回复原因给用户）；可注入上下文 |
| `PreToolUse` | 每个工具执行前（可按 `matcher` 正则匹配工具名） | 是（工具结果回填原因，循环继续） |
| `PostToolUse` | 每个工具执行后 | 否（仅通知） |
| `Stop` / `SubagentStop` | 主回复完成 / 子代理完成 | 否 |
| `PreCompact` | 上下文压缩即将发生 | 否 |

管理器以 5s TTL 重读配置中心，raw 变化才重新编译正则（面板编辑秒级热生效）；任一钩子阻断即短路后续钩子。引擎层（`aichat`）只依赖窄接口 `HookRunner`，自身不碰 shell——依赖方向保持 `aichat → agenthook → functool`，无反向依赖。

## 工具门禁管线

每个工具调用在 goroutine 内、真正执行前经过请求级门禁（`ChatOptions.PreToolGate`），顺序固定：

1. **计划模式**（内存判断，最便宜）：`/plan on` 期间副作用工具（bash/file/config_set/config_file_set/鼠标键盘输入类 mouse_click/mousemove/mouse_scroll/keyboard_type/keyboard_press/记忆写/知识库写/clock 增删改/skill/mcp 管理/子代理/团队/goal_create 与 goal_update/workflow_run/team_task_create·claim·complete）直接阻断，`todo_write` 刻意放行（清单是规划工作流的一部分）；`goal_status`、`team_task_list` 与 screenshot/active_window/list_windows 一样属只读观察，不阻断（计划模式下 AI 可以看屏幕、看任务板做分析）
2. **PreToolUse 钩子**（shell 有界 10s）
3. **管理员审批**：配置修改类工具（`config_set`/`config_file_set`）恒需管理员回复「允许」才执行，请求者本人不能批准，与审批开关无关。审批提示优先私聊发给管理员（`requestAdminOnly`：待批请求同时登记在发起会话键与管理员私聊索引，两处回复均可批），管理员私聊发送失败时回退到发起会话；无权者的审批回复会被消费并提示
4. **人工审批**（等真人，最贵放最后——已被否决的工具不再打扰用户）：`approval.tools` 列出的工具由请求者或管理员批准

阻断文本作为该工具的结果消息回填（语义等同工具报错），循环继续，面板 Query 日志可见被拦调用。门禁在 goroutine 内调用而非 spawn 前统一调用：审批等待不阻塞同轮并行工具的启动。

## 任务清单（todo_write）

内存态、按会话隔离的任务清单工具（全量替换语义，对齐主流 agent）：校验 status 合法性、单一项 in_progress、条数/字数上限；空数组即清空。仅注册到主会话（子代理/定时任务的一次性会话不共享父清单）。有未完成项时在后续对话**尾部注入** `<todo_reminder>` 提醒，内容哈希去重——清单没变不重复注入，避免每轮污染上下文。

## 工具审批与 bash 三段式

`approvalManager`（pluginaichat）：配置工具执行前向会话发送确认消息，请求发送者或管理员回复「允许/同意/allow/yes」或「拒绝/deny/no」决定放行，超时（默认 120s，钳制 10~240s）自动拒绝；结论写入操作日志（`tool_approval`）。回复拦截位于消息入口**第一行**（审批等待期间会话锁被占、回复通常不带 @）；每会话互斥锁把并行工具触发的多个审批串行化逐个提示；`/stop` 经同一 context 取消等待。子代理/定时任务路径 requester 为 0，仅管理员可批。

bash 工具为命令级三段式：黑名单命中→拒绝；白名单命中→放行；都不命中（含均未配置）→ 经 `CallBackFuncs.RequestApproval` 走上述审批，审批未启用（`RequestApproval` 为 nil）时默认放行。`RequestApproval` 在并行回调包装层（`lockedCallbacks`）**透传不加锁**——审批阻塞 ~120s，进互斥锁会卡死同轮其他工具的 SendText。

## 电脑操作（computer use）工具组

`bot/component/computeruse`（OS 层）+ `functool/computeruse.go`（工具层），按 `plugin.ai_chat_bot.computer_use.enable` 注册，**仅 Windows 宿主机**：底层用纯 syscall 实现（GDI `CreateDIBSection`/`BitBlt` 截图、`SendInput` 注入鼠标键盘、`EnumWindows` 枚举窗口），无 CGO，其他平台编译为 `ErrUnsupported` 桩（首次截屏/取坐标时声明 DPI 感知，高分屏截图便不模糊、坐标不错位）。

- **坐标空间**：AI 的点击/滚动/区域截图坐标基于「最近一次 screenshot 的图像」；截图工具按「原点 + 缩放」更新共享 `View`（区域截图原点偏移、`max_width` 降采样都折算在内），输入工具据此换算回虚拟桌面像素（多显示器负原点支持）
- **截图回传**：截图写临时文件后复用 `LoadLocalImage` 管线（多模态推队列 / OCR 备用识别），读完即删，不动 `CallBackFuncs` 接口
- **安全**：输入类工具（mouse_click/mousemove/mouse_scroll/keyboard_type/keyboard_press）纳入计划模式阻断名单；每次操作写 oplog 审计（`screenshot`/`mouse_click`/…）；可加入 `approval.tools` 逐次人工确认

## 自定义斜杠命令

`commandManager` 把 `/名 参数` 映射为提示词模板（`files.commands_json`，5s TTL 热生效）：命中后消息被改写为展开后的单文本段（`$args` 替换为参数，无占位符则追加），走正常对话流程（排队/批处理/知识库/记忆注入不变）。名称受正则约束且不得撞内置命令；`/cmd add/del` 仅管理员。

## 下一步

- [AI 引擎（一）LLM 客户端与对话循环](/internals/agent-llm) —— 循环底层
- [AI 引擎（二）上下文、历史与记忆](/internals/agent-context) —— 状态管理
- [技术原理总览](/internals/)
