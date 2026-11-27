# AI 引擎（二）上下文、历史与记忆

本章介绍 Agent 的**状态管理**：token 预算上下文窗口如何工作、超限时如何压缩、对话历史如何跨重启持久化、内存中的会话缓存如何回收，以及 AI 长期记忆的实现。

## messageWindow：token 预算上下文窗口

`messageWindow` 不是固定轮数的滑动窗口，而是**按 token 预算管理**的窗口：

- 每次请求结束后记录 `lastPromptTokens`（最后一次 LLM 调用的 prompt 数，即当前上下文真实大小）
- 一旦超过 `max_context_tokens × 80%`，下一轮请求前触发 `MaybeCompress`
- `max_context_tokens` 默认 128000，可配置

```mermaid
flowchart TB
    A[请求结束<br/>RecordUsage 记录 lastPromptTokens] --> B{超过 80% 阈值?}
    B -->|否| C[继续对话]
    B -->|是| D[MaybeCompress]
    D --> E{压缩器可用?}
    E -->|是| F["LLM 摘要旧对话<br/>返回 [user] 之前的对话摘要"]
    E -->|否| G[失败降级: 丢弃最旧一半历史]
    F --> H[替换窗口 + 全量落盘 Replace]
    G --> H
```

### 压缩器

- 压缩调用独立 LLM（可配置更便宜的压缩专用模型 `compressor.*`，未配置复用主模型）
- 摘要以 **user 角色**消息保存（`"以下是之前的对话摘要：..."`），而非 system：system 位置已被完整 basePrompt 占用，重复 system 会稀释指令且浪费 token
- 压缩 prompt 明确要求「保留关键信息、用户意图、讨论结论和重要上下文，省略工具调用细节和中间推理过程」
- 压缩的 token 消耗并入当次请求统计（`compressUsage`），但不计入迭代轮数
- 压缩判定通过后、真正执行前触发 `PreCompact` 钩子（仅通知，见 [AI 引擎（三）](/internals/agent-tools#钩子系统-hooks)）

### 失败降级：truncateOldestHalf

压缩失败（网络抖动恰好在上下文最满时最易发生）**不能阻断对话**，直接丢弃最旧一半历史：

- 切点需对齐 `tool_call` 边界：保留区第一条若是 tool 结果消息，其对应的 assistant tool_calls 已在被丢弃的一半中——孤立 tool 消息会被 OpenAI 兼容 API 拒绝（sticky 400）。因此切点后移越过所有孤立 tool 消息；极端情况下清空历史（等效新对话，请求合法）
- 截断后 `lastPromptTokens` 置 0，若仍超阈值下一轮会再走压缩/截断，逐步收敛

## 单请求内上下文保护（工具循环兜底）

窗口压缩只在**每轮 `Chat` 开始时**做一次；一个回合内的多步工具循环仍可能把 messages 逐步撑过窗口，导致整批请求以「超上下文」失败。`ToolOrchestrator` 因此增加回合内兜底：每步 LLM 返回后检查 `usage.PromptTokens`，超过 `max_context_tokens × 85%` 时，把较旧的大工具结果裁成「标记 + 原始长度 + 头尾片段」（`contextPruneKeepRecent=6` 条最近消息与 tool_call 配对完全不动，被裁结果保留头尾各 1000 字符）。裁剪只改写当轮请求上下文，不改窗口与持久化历史——历史仍由回合开始时的 `MaybeCompress` 负责。

这样长工具循环既不会因上下文溢出整批失败，也不会把几轮巨型工具输出永久写进历史。

**运行中插话**：回合执行期间到达的新消息同样注入这份在途上下文——工具循环在**每次 LLM 请求前**通过 `ChatOptions.Steer` 排空会话队列，用户消息加 `【用户插话】` 前缀、子代理完成通知等合成消息（`Sender.UserId=0`）加 `【后台消息】` 前缀，作为 user 消息追加到对话尾部，模型下一步就能看到；最后一批请求开始后才到达的消息留在队列里，由运行器在本批结束后作为下一批处理。插话中的图片会登记到本轮请求的图片注册表，`load_images` 可按哈希加载（机制见 [AI 引擎（三）](/internals/agent-tools)）。

## 历史持久化

### HistoryStore 抽象

```go
type HistoryStore interface {
    Load(ctx context.Context) ([]Message, error)
    Append(ctx context.Context, messages []Message) error // 增量追加
    Replace(ctx context.Context, messages []Message) error // 全量覆盖（压缩/截断后）
    Clear(ctx context.Context) error
}
```

两个写入语义的设计动机：常规对话用 **Append 增量**（行级存储只 INSERT 新行，避免整条历史反复全量重写的写放大），压缩/截断后历史重排才用 **Replace 全量覆盖**。

### 本轮落盘与注入剥离

每轮对话的落盘范围是 **用户消息 + AI 回复 + 工具消息链**：`Chat` 组装请求后按「本轮到用户消息」的起点取 `updatedMessages` 后缀写入窗口。当调用方向用户消息拼入了只应作用于当轮的注入（记忆/知识库片段、待办提醒、计划模式附言等）时，通过 `ChatOptions.PersistUserText` 传入注入前的原始文本：注入只进入本轮请求，历史中保存的是原文。同类地，`UserPromptSubmit` 钩子注入的上下文在提供 `PersistUserText` 时也不写入历史（钩子每轮重新触发，无需回放）。动机：旧注入随历史回放既白占 token，又可能引用已失效的记忆 ID 或过期资料。

### SQL 行级存储（推荐）

SQL 后端下（探测 `storage.SQLBackend`）建两张表：

- `ania_chat_session`：每会话一行，`msg_count` 兼作序号分配器
- `ania_chat_message`：每消息一行，`(session_id, seq)` 联合主键，无外键（应用层维护一致性）

Append 在事务内读取并推进 `msg_count` 分配 seq，只 INSERT 新行；Replace 清空该会话消息并重排 seq；Clear 删除两表对应行。SQLite 单连接（`MaxOpenConns(1)`）下读取遵循「收集→关闭 rows→解析」纪律，写入在单事务内完成。

### 无 SQL 时

SQL 探测或建表失败时历史不落盘（仅内存窗口，重启即丢），不再回退 KV。

### 图片降级落盘

落盘副本中的图片片段一律降级为文本标记 `[图片 <hash>]`：

- 远程 http(s) URL（QQ 临时链接）重启后失效，无保留价值
- data URI（base64 内联）体积可达 MB 级，原样落盘会撑大单行体积
- 只影响落盘数据；内存中当前会话仍保留图片供本轮使用

### 后台落盘

`persistAppend` 使用**独立的后台 context**，避免请求被 `/stop` 取消时丢失刚写入的历史；落盘失败仅记录日志，不影响内存对话。

## 会话缓存与回收

`pluginaichat` 用 `sync.Map` 持有每个会话的 `chatEntry`（ChatBot + lastActive 时间戳）。会话只增不减会导致内存线性增长，因此有 janitor 回收：

- **闲置淘汰**：1 分钟 tick，闲置超过 `session.max_idle_minutes`（默认 120，0 禁用）的会话被淘汰
- **LRU 上限**：驻留超过 `session.max_sessions`（默认 128，0 禁用）时淘汰最久未活跃的
- 只有指向 AI 的交互才刷新活跃时间——繁忙群里没人 @ 的死会话不会被误认为活跃

### 淘汰的安全性

```mermaid
flowchart LR
    A[候选淘汰] --> B{tryLock 成功?<br/>会话锁}
    B -->|否| C[响应中, 跳过]
    B -->|是| D{活跃时间未变?<br/>无排队消息?}
    D -->|否| E[跳过]
    D -->|是| F[CompareAndDelete 删除]
```

- 响应中的会话持有会话锁，`tryLock` 必然失败 → 跳过
- 拿锁后复检活跃时间与排队队列，防止「刚被新消息触达」的会话被误删
- 只删内存对象：历史已持久化，下次发言 `getChat` 自动重建并回放
- 已知副作用：会话内 `mcp_load` 动态加载的工具随淘汰失效（等同重启）

### 群聊自动清理

群里连续 30 条消息无人 @ 机器人时，自动清空该群对话缓存（历史、动态 MCP 工具），防止群消息被无意中计入上下文。清空与进行中的对话共用会话锁，避免并发读写。

### 面板查看与重置（上下文管理）

面板「上下文管理」页（`/#/contexts`）把每个会话真正持有的上下文原样展示出来：system prompt 三段（静态提示词 / available_skills / 场景描述）、消息历史（含工具调用与工具结果）与工具定义，并给出各段字符数与 token 粗估（约 2 字符折 1 token）。

- **快照来源**：`ChatBot.ContextSnapshot()` 用 `TryLock` 读消息窗口——回合执行中窗口被本轮占用，读不到时退回持久化历史（`source=persisted`），面板请求不会被长回合阻塞。为此 `ChatBot` 的窗口操作（`Chat` / `LoadHistory` / `AppendHistory` / `ClearHistory`）统一持有一把互斥锁；插件层本来就按会话串行调用它们，加锁只为旁路只读安全。
- **每轮注入不可见**：记忆 / 知识库 / 待办提醒 / 计划模式附言只拼到当轮用户消息上、不写入历史，因此不会出现在快照里（重置时会一并清掉待办清单）。
- **清空 / 删除**：先按需终止在跑请求（面板弹确认框后传 `terminate=true`），再轮询会话锁（上限 15s）拿到锁后执行——清空内存窗口与持久化历史、卸载动态 MCP 工具、删除落盘工具结果、清空待办与排队消息、换新会话 token，并把会话对象移出内存（下次发言按空历史重建）。「删除会话」额外清除长任务目标与计划模式；「清空」只暂停目标、保留计划模式。
- **导出**：单个 / 多个会话的完整快照导出为 JSON（结构与详情一致），便于离线对比上下文结构做调优。

## 长期记忆（memoryManager）

与上下文窗口不同，长期记忆**跨会话、跨重启**保留，由 AI 通过 `memory_save` / `memory_search` / `memory_update` / `memory_forget` 工具自行管理：

- **作用域隔离**：scope = `g:群ID` / `f:用户ID`，工具在注册时绑定 scope，从机制上保证记忆不会跨会话泄露
- **去重**：内容规范化（空白折叠）后相同则不重复写入；写入时另做近似重复检测（CJK 分词后词元集合的 Dice 系数，阈值 0.55），命中则在返回值里提示 AI 用 `memory_update` 合并、`memory_forget` 删除多余条目
- **更新**：`memory_update` 按 ID 覆盖内容；未传 `user_id` / `tags` 时保留原值，显式传空则清空；内容变更后重算语义向量
- **上限**：`memory.max_entries`（默认 200），写满返回 `ErrMemoryFull`，提示 AI 先清理或合并
- **截断**：单条内容上限 2000 符文
- **检索**：无 query 时返回全部（新在前）；有 query 时关键词打分（标签命中权重大于内容命中），零分剔除后排序
- **语义向量**：可选 embedding（与知识库共享 `embedder`），入库时计算向量，检索时语义加分；embedding 服务不可用时静默降级为纯关键词
- **注入语**：`memory.auto_inject` 注入块除「可参考」外还提示发现不符或过时时用 `memory_update` 更正，让 AI 在使用中顺手维护
- **存储**：每条记忆一行（`ania_memory`，`(scope,id)` PK，tags/embedding JSON 列，`created_at` 定宽 UTC 文本使文本序 = 时间序）；SQL 探测或建表失败时记忆功能整体禁用
- 面板实现 `adminpanel.MemorySource`，可在「记忆管理」页直接增删改查，scope 校验 `^[gf]:\d+$` 防越界

## 备用识图（OCR）

主模型不支持多模态时，`load_images` / `local_image` / `screenshot`（电脑操作工具）把图片交给**备用识别模型**（`ocr.*` 配置，默认 SiliconFlow 的 Qwen3-VL）生成文字描述，返回给 LLM；主模型支持多模态（`multimodal=true`）时则把图片推入队列、下一轮以图片上下文注入。

## 下一步

- [AI 引擎（三）工具、MCP 与高级编排](/internals/agent-tools) —— 工具如何定义、MCP/Skill/定时任务/子代理
- [AI 引擎（一）LLM 客户端与对话循环](/internals/agent-llm) —— 请求循环本身
- [技术原理总览](/internals/)

