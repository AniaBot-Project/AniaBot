# AI 引擎自主性升级方案（设计提案）

> **状态**：**P0+P1+P2 的已实现项已完成**（分支 `feat/agent-autonomy`，P2 落地见提交 `88b560a`）。P2 中仍未实现：**Session fork / 任务级 session 历史**（当前 `/resume` 用主会话续接，不是 fork）、**交付物 `present` / 文件附件主动推送**。已实现的 P2 能力有几处刻意简化，文档按实际口径记录：goal 的 `blocked` 语义简化（没有 DSH「同一阻塞条件连续 ≥3 轮」的门槛，AI 说明原因即可）；子代理 steering 是「下一次 LLM 请求前注入」，不是 DSH 的完整 inbox/消息队列；主会话的运行中插话同样采用「下一次 LLM 请求前注入」（`【用户插话】`/`【后台消息】` 前缀），并已落地 `send_message` 让 AI 在回合执行中主动发消息（每个回合最多 8 条）；子代理没有独立会话历史落库。实现细节以代码与 `AGENTS.md` 为准；本文保留设计背景与取舍，第 10 节待决策问题已按最终决策就地标注。Goal 续跑配置节已在 P2 重新加入（`plugin.ai_chat_bot.goal.*`，默认开启、`max_rounds=64`）。
> **范围**：`bot/component/aichat`、`bot/plugins/pluginaichat`、`bot/core` 的事件预算、子代理/任务编排、提示词与面板。
> **参考实现**：DeepSeek Harness（`E:\repo\deepseek-harness`）与 super-agent 原型（`E:\repo\dsh-superagent-prototype`），下称 DSH / 原型。

## 0. 结论速览

现引擎把**三件不同的事**压成了一个 300 秒的框架事件预算：**框架事件预算、单次回复预算、后台任务生命周期**。三条链路互相牵连，导致：

1. 单会话响应被 5 分钟硬截断，超时后取消整个工具循环，用户只收到一句"本次处理已达时间上限，请重新发送消息"；
2. 子代理即使异步执行，默认超时仍从同一个 `bot.msg_event_timeout_sec` 派生（默认 300 秒，单次最多 1800 秒）——**后台任务同样是 5 分钟必死**；
3. 工具循环有 100 轮硬上限，到顶后强制无工具收尾；
4. 对话与会话锁串行，长任务期间新消息排队，预算耗尽时排队消息被丢弃；
5. 进程重启后正在跑的子代理/回合全部丢失，没有任务记录，也无从恢复。

**方案主张：不是删掉超时和上限，而是解耦三类预算，并把"硬中断"改造成"分级降级 + 后台交接"：**

| 层 | 现状 | 目标 |
| --- | --- | --- |
| 框架事件预算 | 300s，贯穿 AI 全链路 | 只约束消息入口；AI 工作不再挂在事件上（P1 后入口 <50ms 返回） |
| 单次回复预算 | 300s 硬取消 | 软截止（提示收尾/交接，不取消）+ 无进展看门狗 + 可选硬截止（默认关） |
| 任务生命周期 | 从事件预算派生，5min | 独立配置，默认不限制；有状态、可取消、可恢复、可观测 |
| 工具轮数 | 100 轮硬上限 | 默认不限制；重复调用提醒 + 无进展检测 + 用户 /stop |
| 长任务形态 | 同步回合内做完 | 多步任务默认委派子代理后台执行；超限自动"交接"到后台任务 |
| 子代理 | 匿名、一次性、无恢复 | 命名、结构化状态、可追加指令、持久化、结果通知可追踪 |

一句话：**把"等待"变成消息（inbox/notice），把"超时"变成协商（收尾提示/后台交接），把"后台"变成一等公民（任务注册表）。**

---

## 1. 背景与目标

### 1.1 诉求

- 不再限制每个会话的响应时间，超时处理要优雅（不能"没做完直接停止"）。
- 去掉工具调用次数限制。
- 复杂任务不能提前停止；即使停止，也要保留已完成的工作并能继续。
- 强化 subagent 积极性：任何多步任务尽量交给 subagent/后台执行，工作时**不阻塞正常对话**。
- 参考 DSH / super-agent 原型，整体提升 agent 能力。

### 1.2 验收标准（可检验）

| # | 验收项 | 判定方式 |
| --- | --- | --- |
| A1 | 复杂任务不再因固定时长被中断 | 构造 ≥15 分钟的多轮工具任务（如多源调研+写文件），能正常跑完并交付；日志无 "事件总预算耗尽" |
| A2 | 无工具轮数硬上限 | 单轮 ≥150 次工具调用可继续；仅有重复调用/无进展提醒 |
| A3 | 任务进行中用户插话不被阻塞 | 长任务运行时用户发新消息，≤2s 内收到 ack（或收到"已排队/已转后台"提示），消息不被丢弃，任务结束后被处理 |
| A4 | 中断可恢复 | 进程在任务中途重启：任务标记 `interrupted`；用户 `/resume <id>`（或配置自动续跑）能继续，已完成步骤不重做 |
| A5 | 多步任务默认后台化 | 提示词+运行时统计：≥3 步的任务中，主回合直接执行的占比明显下降（上线后 Query 日志统计委派率） |
| A6 | 无失控成本 | 长任务有 token 统计与配额；重复/无进展循环能被看门狗终止并给出原因 |

### 1.3 非目标

- 不照搬 DSH 的 Cordis 插件体系、事件溯源全量回放、Workflow JS 沙箱、桌面端 UI 能力。
- 不取消全部安全阀：用户 `/stop`、进程关闭、配额、审批、工具级超时（bash 等）保留。
- 不改平台适配层与插件生命周期语义（框架事件超时对其他插件保持不变）。

---

## 2. 现状诊断

### 2.1 一条 300 秒预算贯穿全链路

```
core.onGroupEvent
  └─ context.WithTimeout(ania.ctx, msgEventTimeout())        bot/core/core.go:767, :804
       └─ pluginaichat.OnGroupMsg
            └─ bridgeRunContext → deadlineExemptContext       bot/plugins/pluginaichat/lock.go:232-283
                 ├─ eventDeadline = 父 ctx deadline（即 300s） lock.go:233-236
                 └─ drainChatBatches(runCtx, eventDeadline)   plugin.go:371-408
                      ├─ remaining = time.Until(eventDeadline) plugin.go:384-386
                      ├─ batchCtx = WithTimeout(min(msgEventTimeout, remaining))   plugin.go:394
                      ├─ chat.Chat(batchCtx, ...)             → LLM 调用 / 工具循环 / 子代理
                      └─ 预算耗尽：丢弃排队消息 + "本次处理已达时间上限，请重新发送消息"
                                                              plugin.go:387-392, :410-419
```

关键点：

- `bot.msg_event_timeout_sec` 默认 300 秒、上限 24 小时（`bot/core/core.go:93, :98, :104-116`；面板字段 `bot/core/configfields.go:14`），从框架一路透传到 AI 请求。
- 插件虽然已经用 `deadlineExemptContext` **忽略**框架 deadline 的取消（只把 deadline 当作"总预算"结算，`lock.go:320-360`），但**预算仍然是同一个数**，到期由插件自己取消并丢弃排队消息。
- 子代理超时也来自同一常量：`subagentTimeout()` 返回 `p.msgEventTimeout`（默认 5 分钟，`subagent.go:20-41`），单次调用 `timeout_sec` 上限 1800 秒，且父 ctx 有 deadline 时再压缩 30 秒收尾预留（`subagent.go:57-80`）。异步子代理虽然用 `context.WithCancel(context.Background())` 绕开了父 deadline（`subagent_async.go:146-187`），但 `runSubagent` 内部仍按上述默认值套 300 秒超时——**"后台"并没有真正摆脱事件预算**。
- **0/负值现在不是"关闭"而是"回退默认"**：`core.msgEventTimeout()` 对 ≤0 回退 300s（`core.go:109-111`）；`mainMaxIterations()` 对 ≤0 回退 20（`llm.go:224-228`），且 `ToolOrchestrator` 在 `maxIterations<=0` 时直接返回 `exceeded maximum iterations`（`toolexecutor.go:157,247`）；`eventBudget()` 返回 0 会被 `drainChatBatches` 当作"已过期"立刻收口（`lock.go:407-414` + `plugin.go:387-392`）。**改限制时必须显式定义 off 语义**，这是最容易踩的坑。

### 2.2 其余硬限制（与本诉求相关）

| 机制 | 位置 | 现状 |
| --- | --- | --- |
| 工具轮数 | `aichat/toolexecutor.go:41-53, :157`；配置 `pluginaichat/config.go:309` 默认 100；`llm.go:224-228` 兜底 20 | 到达上限后追加"工具轮数上限"提示并做一次无工具收尾（`:222-244`），任务被强制截断 |
| 全局并发槽 | `plugin.go:907-914` 默认 `rate_limit=2` | 同时只处理 2 个 AI 请求，第 3 个直接拒绝"当前请求太多啦"；后台子代理消耗也算在内（子代理 goroutine 不占槽，但主回合占） |
| 会话串行 | `plugin.go:284-306` 会话锁；`pendingqueue.go:9-10` 队列上限 20 | 长任务期间新消息排队；预算耗尽时被丢弃 |
| 每会话驻留 | `config.go:295-299` 默认 128 会话 / 120 分钟 | 进行中会话不淘汰（已有保护），但重启后全丢 |
| 工具级超时 | `functool/bash.go:23, :222` 默认 300s；MCP 连接超时 `llmtool/mcp.go:65`；钩子 `agenthook/runner.go:46-55` ≤60s；审批 `approval.go:125-141` 120s | 保留合理，但它们叠加在 300s 总预算上互相挤压 |
| LLM 请求 | `llmbackend_*.go:26-47` 均未设置请求/HTTP 超时；重试 `llmclient.go:186-226`（`retry.max_attempts=3, base_delay_sec=2`），失败切备用一次 | 完全依赖 ctx 兜底；一旦去掉回合超时，上游挂死会让请求永久挂起、会话锁与并发槽永不释放——**必须补请求级/流空闲看门狗** |
| 异步子代理结果治理 | `subagent.go:193`（"结果不截断"）；`subagent_async.go:207-219` | 结果作为普通 user 消息入上下文，不经过 `tool_result` 落盘/截断治理，超长结果直接撑爆窗口 |
| 单请求内上下文 | `chatbot.go:113-120, :150`；`memorywindow.go:229-263` | 只在每轮 `Chat` 前压缩一次；一个回合内多步工具循环可把 messages 撑过窗口，整批请求失败 |
| `/stop` 覆盖范围 | `plugin.go:243-265, :451-471`；`llm.go:86-105` | 只有"存在活跃主请求"时才能停；主回合结束、后台子代理仍在跑时，`/stop` 回答"当前没有正在进行的 AI 请求"，**取消不到后台任务** |
| 定时任务预算 | `clock.go:580-586, :682-689`；`config.go:151-154`；`clocksubagent.go:19-25, :340-382` | 单任务默认 120s（最长 1800s）；定时子代理回喂 ≤5 轮、等待预留 30s，预算耗尽取消剩余子代理 |
| 上下文窗口 | `messageWindow` 80% 阈值压缩；压缩失败 `truncateOldestHalf` 有损落盘（`memorywindow.go:229-263`） | 长任务更频繁触发；压缩失败路径会永久丢旧历史，影响后续步骤的可恢复性 |

### 2.3 "复杂任务提前停止"的根因排序

1. **回合总预算到期（最主要）**：`drainChatBatches` 取消当前批、丢弃排队消息，用户看到"时间上限"提示（`plugin.go:382-408, :410-419`）。
2. **子代理 5 分钟超时**：异步子代理默认超时 = `msgEventTimeout`，单次上限 1800s；复杂调研/编译类任务普遍超过 5 分钟（`subagent.go:22-28, :62-80`）。
3. **工具轮数上限**：100 轮对多文件重构、长链路调研偏紧；到顶后强制无工具收尾（`toolexecutor.go:222-244`）。
4. **异步子代理结果丢失路径**：pending 队列满 20（`subagent_async.go:222-226`）、`/stop`/批错误/预算耗尽时的 `drainPending`（`plugin.go:391,417`）、上下文超限整批不落盘（`chatbot.go:155-161`）。
5. **单请求内不压缩**：长工具循环撑爆上下文 → 整个请求失败（`chatbot.go:114,150`；`memorywindow.go:229`）。
6. **全局并发槽拒绝**：`rate_limit=2` 时长任务长期占槽，第 3 个会话直接被拒（`plugin.go:909-914`）。
7. **LLM 无请求级超时 + 重试/fallback 消耗同一预算**（`llmclient.go`），事件预算末尾常被重试与退避吃掉。
8. **输出 max_tokens 截断**：Anthropic 后端默认输出上限 8192（`llmbackend_anthropic.go:71-72`），长文件写入/大参数工具调用易被截断；被截断的工具调用会被跳过并要求重试（`toolexecutor.go:187-203`）。
9. **后台接管路径的额外截断**：清扫器单会话 30s 预算（`subagent_async.go:327-341`）会打断排队触发的长任务。
10. **进程重启/关闭**：无任务表，`b.Go` 协程随进程消失，子代理结果与进度全部丢失；`b.Go` 不随 ctx 取消（`core/tracer.go`），异步子代理因此也不受关停取消。
11. **审批/hook/bash 等待都计入同一预算**（`approval.go:125-141`、`agenthook/runner.go:54`、`functool/bash.go:222`）。
12. **拼写级问题**：`ToolOrchestrator` 在 `maxIterations<=0` 时直接返回 `exceeded maximum iterations`（`toolexecutor.go:247`），若有人把配置改成 0 会立刻报错——本次一并修正。

### 2.4 现有"异步子代理"为什么仍不够"后台"

- 超时从回合预算派生（见 2.1），后台 ≠ 长寿命。
- 结果通过合成消息入 `pendingQueue` 并触发新一轮（`subagent_async.go:194-236`），方向正确；但任务本身不落库、不显示在面板、不可跨重启恢复；**结果不截断、不落盘**（`subagent.go:193`），超长结论会撑爆上下文。
- 主回合仍在**同步等**自己的工具循环跑完；只有委派出去的部分是异步的。
- 子代理无名字、无结构化状态、运行中无法追加指令（只能取消）；没有每会话并发上限。
- **不可取消**：`/stop` 只在有活跃主请求时才会 `cancelAsyncSubagents`（`plugin.go:243-265, :451-471` + `subagent_async.go:96-102`）；主回合结束后后台任务无法用 `/stop` 停止，也没有统一清理入口（`AIChatPlugin.OnUnload` 未清理后台子代理）。
- 委派全靠提示词一行（`subagenttool.go:30-36`、`config.go:47`），没有运行时提醒/启发式。

### 2.5 与 DSH 的差距（只列与本次目标相关的）

| 能力 | DSH | AniaBot 现状 |
| --- | --- | --- |
| 回合超时 | 无 turn/step 硬预算，预算下沉到单工具、单流 idle 看门狗（`packages/core/agent-loop/README.md:238`；`guard/timeout-policy`） | 300s 回合硬预算 |
| 续跑 | goal（durable objective + 外置 round driver，默认 256 轮） | 无 |
| 后台 | jobs（output ring/状态/唤醒/防自激）+ schedule | 无（bash 同步） |
| 子代理 | 立即返回名字、完成通知入 inbox、命名树、send_message、并发 8/深度 1、无内建超时 | 异步但 5min、匿名、无通信、无持久化 |
| 通知模型 | 完成 = 一条 user 消息；父空闲开新 turn、忙则下一步注入 | 合成消息入队 + 触发，基本一致 |
| 上下文 | compaction（0.8 阈值/16% 保留/溢出重试/工具结果裁剪） | 80% 摘要压缩，降级路径有限 |
| 取消 | stop reason 语义化（completed/aborted/max-tokens/error/refusal）+ 保留已输出 | 统一"请求超时/已停止" |

---

## 3. 设计原则

1. **预算分层**：框架事件、单回合、单工具、单 LLM 请求、任务生命周期各自独立，互不派生。
2. **硬中断最小化**：唯一默认的强制停止来源是用户 `/stop` 与进程关闭；其他限制优先"提醒 → 协商 → 降级 → 交接"。
3. **等待即消息**：不轮询、不空转；异步结果作为一条消息回到会话（idle 开新 turn，busy 则下一步注入）。
4. **状态持久化**：任务有 id、状态、进度、结果、停止原因；中断可恢复，通知幂等。
5. **委派优先**：提示词 + 编排启发式 + 运行时边界（并发/深度/预算）三层配合，不能只靠模型自觉。
6. **安全阀仍然存在**：看门狗、配额、并发上限、审批、工具超时必须有，只是不再表现为"5 分钟一刀切"。
7. **不重写平台层**：所有改造收敛在 aichat / pluginaichat / core 的事件预算接入点。

---

## 4. 目标架构

### 4.1 总览

```
                 ┌──────────────────────────── 平台消息 ────────────────────────────┐
                 │ core.onGroupEvent/onFriendEvent（框架事件预算只覆盖这里）           │
                 └───────────────┬───────────────────────────────────────────────────┘
                                 │ 只做：去重、命令、mention 门、入队
                                 ▼
                    ┌───────────────────────┐
                    │ Session Inbox (持久化) │  next-turn：用户消息/完成通知/续跑
                    │                       │  next-step：steer（运行中插入）
                    └───────────┬───────────┘
                                │ claim（同一会话串行）
                                ▼
         ┌──────────────────────────────────────────────┐
         │ Session Runner（每会话一个 driver goroutine）│
         │  runTurn: 组装 → LLM → 工具批次 → 决定续/停   │
         │  预算：soft / hard / watchdog / token         │
         └───────┬───────────────┬──────────────┬───────┘
                 │               │              │
        ┌────────▼──────┐ ┌──────▼──────┐ ┌────▼─────────┐
        │ Task Registry │ │ Subagent 池 │ │ Job 运行时    │
        │ (SQL 持久化)  │ │ (任务 =     │ │ (bash 后台)   │
        │               │ │  独立会话)  │ │              │
        └────────┬──────┘ └──────┬──────┘ └────┬─────────┘
                 └───────────────┴─────────────┘
                        完成 → 合成消息写回 Inbox → 唤醒 Runner
```

### 4.2 轮次生命周期：三级预算替代硬超时

一个回合（turn）从 claim 到结束，只受以下约束：

| 预算 | 默认 | 行为 |
| --- | --- | --- |
| **软截止** `turn.soft_timeout_sec` | 600s | 到期**不取消**：在下一步注入一条系统提示——"已运行较久，请评估：能收尾就收尾；否则调用 `task_handoff` 把剩余工作交后台并给出进度摘要"。只注入一次 |
| **硬截止** `turn.hard_timeout_sec` | 0（关闭） | 到期取消当前 turn，但保留会话锁内的历史；写一条可续跑的任务记录；向用户发明确原因 |
| **无进展看门狗** | 重复工具调用阈值 3/5/8 次；连续 12 步无新结果 | 先提醒（不同参数/换方案/收尾），持续无进展则终止并回报"已停：连续重复动作" |
| **工具级超时** | 保留（bash 300s、MCP、审批） | 单工具失败以错误结果回填模型，回合继续 |
| **LLM 请求超时** | `llm.request_timeout_sec=600`；流空闲 `stream_idle_timeout_sec=120` | 只作用于单次请求，超时重试/切换备用模型；不伤回合 |
| **token 配额** | 沿用现有 quota；新增任务级 soft cap（默认 0=off） | 到达软上限提醒模型收尾；硬配额沿用现有拒绝逻辑 |

> 设计依据：DSH 明确"No built-in turn budget"，把预算下沉到单工具/单流（`docs/subsystems/tools.md`、`guard/timeout-policy`）；同时 IM 多用户场景必须保留兜底，所以保留**可关闭**的硬截止与看门狗。
>
> **前置条件（必须与放开同批完成）**：
> 1. 补 LLM 请求级超时 + 流空闲看门狗——当前仓库三个后端都没有设置请求/HTTP 超时（`llmbackend_*.go:26-47`），事件预算是唯一兜底；
> 2. 明确 `0 = 关闭` 的语义并处理 `eventBudget()==0` 被当作"已过期"的现有实现（`lock.go:407-414`）；
> 3. 修 `/stop` 对后台子代理的取消路径（`plugin.go:243-265, :451-471`），否则长任务将无法用指令停止。

### 4.3 优雅收口：从"超时取消"到"后台交接"

软截止/工具轮数阈值/模型主动判断三者任一触发时，走 **handoff（交接）** 而不是丢弃：

```
runTurn 到达软截止
  → 注入收尾提示（不取消）
  → 模型选择：
      A. 直接收尾：给出当前结论 + 剩余事项（turn 正常结束）
      B. 调用 task_handoff（或运行时自动）：
           1. 用「目标 + 已完成摘要 + 待办清单 + 最近 N 轮对话摘要」构造后台任务
           2. 任务在独立会话键（如 g:<id>#task-<id>）下由 Subagent/Job 继续执行
           3. 当前 turn 立即返回："任务已转入后台（#id），完成后我会把结果发到这里"
           4. 后台完成 → 合成消息入 Inbox → 新 turn 汇报/继续
```

要点：

- 交接不复制完整历史（只带摘要 + 待办），避免上下文与 token 翻倍；原始历史仍留在主会话。
- 主会话在后台任务运行期间**完全空闲**，可正常对话；用户随时发消息优先处理（人类优先）。
- 交接任务与普通子代理共用任务注册表和通知通道，`/stop`、`/tasks` 一视同仁。
- 这是"超时处理优雅"的核心：**不再有"没做完被停止"，只有"换一个地方继续做"**。

### 4.4 任务注册表（Task Registry）

所有"离开当前回合继续存在的工作"统一建模为 Task：

| 字段 | 说明 |
| --- | --- |
| `id` | `t-<unixnano>-<rand>`，对外可见（`/tasks`、通知里引用） |
| `session_key` | 发起会话 `g:<id>` / `f:<id>` |
| `parent_id` | 父子任务（交接/子代理/团队成员） |
| `kind` | `turn` \| `subagent` \| `handoff` \| `bash_job` \| `clock` \| `proactive` \| `goal_round` |
| `status` | `queued` \| `running` \| `waiting` \| `completed` \| `failed` \| `timeout` \| `cancelled` \| `interrupted` |
| `title/prompt` | 展示与重跑所需 |
| `result/error` | 最终文本；错误脱敏 |
| `stop_reason` | `completed` / `aborted` / `max_tokens` / `error` / `watchdog` / `timeout` |
| `tokens` | 子树用量归集（父任务汇总） |
| `meta` | JSON：阶段、工具计数、恢复提示 |
| 时间戳 | `created_at/started_at/updated_at/finished_at` |

状态机：

```
queued → running → completed
                 ├→ failed
                 ├→ cancelled   （/stop 或用户取消）
                 ├→ timeout     （硬截止/任务级兜底）
                 └→ interrupted （进程重启/关闭）
interrupted → running（/resume 或可配置自动续跑）
```

重启恢复策略（默认保守，与 DSH 的 disarm 一致）：

- 启动时把所有 `running` 任务标记 `interrupted`，记录"中止于第 N 步"。
- 默认**不自动续跑**（避免重启后自动烧钱）；`/tasks` 展示可恢复任务，用户 `/resume <id>` 续跑。
- 可选配置 `task.auto_resume=true`（默认 false）：重启后自动带 recovery note 续跑，且只对 `kind=handoff/subagent` 生效。

### 4.5 会话运行器：消息入口零阻塞（P1）

现状 `OnGroupMsg` 在事件 goroutine 内一直跑到回复完成，因此才需要"事件预算"。改为：

```
OnGroupMsg:
  去重/自过滤 → 命令（/stop /status /tasks /plan …）就地处理 → mention 门
  → 入队 Inbox（持久化） → 确保 Runner 运行 → 立即 return false
Runner（每会话一个，按 session_key 串行）:
  for {
    batch := inbox.claim()
    if empty → 退出（会话回到 idle）
    lock := tryLock()            // 同一会话串行，跨会话并发
    runTurn(batch)               // 无框架 deadline；只有 soft/hard/watchdog
    release
  }
```

收益：

- 框架 `bot.msg_event_timeout_sec` 对 AI 彻底失效，不需要改框架语义；其他插件不受影响。
- 用户消息始终先入队再处理，长任务期间不会丢消息。
- `/stop` = 取消当前 turn + **保留** Inbox（不丢用户后续消息）。
- `/status` 展示：当前 turn 阶段/耗时/token/排队数/后台任务数。

实现要点：

- 复用现有 `pendingQueue` → 升级为持久化 Inbox（至少 `next-turn` 队列落 SQL；`next-step` 可先内存）。
- 复用 `activeContexts`/`sessionLock`/`kickPending`/`sweepPendingSessions` 的既有机制，避免重写调度。
- 迁移期保留旧路径开关 `runner.enable`（默认 false → 灰度开启），便于回滚。

### 4.6 Subagent 2.0：让"委派"成为默认路径

**提示词层**（最强，模型每次请求可见）：

- 工具 description 明确"异步、结果自动回到会话、可并行启动多个"。
- 系统提示词新增"任务编排"段（见 6.4）：≥3 步/预计 >1 分钟/多轮检索 → 直接委派；委派后不等待、不轮询，继续做别的事或结束本轮；独立子任务放在**同一条回复**里并行启动；主对话只负责理解、拆解、委派、汇总。
- 子代理自己的提示词：完成时输出自包含结果（结论 + 证据 + 未完成项），中间过程不外发。

**编排启发式**（DSH 没有，需要补）：

- 主回合工具轮数达到 `progress_reminder_after`（默认 6）→ 注入一次提醒："任务已多步，考虑用 subagent_run 拆出独立部分"。
- 模型连续两轮只做"读"类操作且无副作用 → 提示可委派。
- 软截止时自动走交接（4.3）。
- 统计委派率写入 Query 日志，面板可看。

**运行时边界**：

| 项 | 默认 | 说明 |
| --- | --- | --- |
| 单会话并发子代理 | 4 | 超出返回可读错误（不静默排队） |
| 全局并发子代理 | 8 | 与对话轮次**分池**，互不挤占 |
| 委派深度 | 1（子代理不可再委派） | 可配 2；到限由提示词告知"自己做" |
| 单任务超时 | 0（不限制） | 仅 `task.wall_clock_sec` 全局兜底（默认 0）与看门狗 |
| 单任务 token 软预算 | 0（off） | 到达时提醒收尾，不强制 |
| 结果回灌上限 | 8000 runes | 超出落盘 + handle，沿用现有 `tool_result` 机制（异步结果当前不经过该治理，见 2.2） |

**结果治理补齐**（放开时长后必须做，否则更容易撑爆上下文）：

- 异步子代理结果接入 `tool_result` 同款治理：超 `subagent.result_inline_runes` 落盘为会话私有文件，合成消息只放摘要 + handle，模型用 `read_tool_output` 读取。
- 高价值长结果（报告、代码）建议落 `./data/task-outputs/`，并在通知里给出文件路径，IM 侧可直接作为附件推送（P2 的 `present` 语义）。

**能力升级**：

- 命名：`subagent_run(name=...)`，同名冲突拒绝；`/tasks` 按名字展示。
- 结构化返回：`status`（completed/blocked/failed/timeout/cancelled）+ `summary` + `result` + `artifacts`；不再只有一段文本。
- 运行中通信：`subagent_send(id, message)` 给运行中的子代理追加指令/纠正（下一步注入）；`subagent_cancel(id)`。
- 持久化：任务记录 + （可选）子代理会话历史落 SQL，支持重启后继续或重跑。
- 可观测：`subagent_list` 返回状态、阶段、耗时、token。

### 4.7 Goal 续跑：跨多轮的长任务

适用：用户给出一个长目标（"把这个仓库的 X 迁移完"），一轮做不完但可以持续推进。

- 工具：`goal_create(objective, max_rounds?)` / `goal_update(action=complete|blocked|pause|resume)` / `goal_status`。
- 持久化：`ania_agent_goal`（或复用任务表 + `kind=goal`），记录 objective/phase/rounds_started/max_rounds/blocked_reason。
- 驱动：会话 idle 且无运行中子任务 → 注入 `<goal_round>` 提示开新 turn；`max_rounds` 默认 256，到达自动 `blocked(round-limit)`。
- 停止语义：`complete` 须有证据；`blocked` 要求同一阻塞条件连续 ≥3 轮（防"偷懒式 blocked"）；重启/卸载后 disarm，需用户显式 resume。
- 与子代理配合：每轮开始前若子树仍有任务在跑，先等其收敛（对应 DSH `waitForTreeWork`）。
- 成本闸门：quota + 每目标 token 预算 + 用户可随时 pause。

> Goal 是"复杂任务不提前停止"的终极形态：把"多轮继续"从用户/模型行为变成**运行时驱动**。建议 P2 落地，避免与 P0/P1 的规则互相干扰。

### 4.8 后台 Job（bash 等长命令）

现状 `bash` 同步阻塞至多 300s（`functool/bash.go:22`），会吃满回合预算。引入：

- `bash run_in_background=true`（或自动：预计长命令/超时自动转后台）→ 返回 job id。
- 工具：`job_output`（可 wait 但硬上限 10min，且可被用户插话唤醒）、`job_list`、`job_kill`。
- 完成通知：与子代理一致，合成消息入 Inbox；提示词要求"不要轮询、结束回合等通知"。
- 生命周期：默认内存（P1），P2 视需要落库（DSH 也是进程内，重启即丢；不会假装还在跑）。

### 4.9 提示词与工具契约（摘要，全文见 6.4）

- 主提示词新增"任务编排"段与"时间预算"段。
- `subagent_run` description 重写（异步语义、并行、不阻塞）。
- 子代理提示词补充"结果自包含"。
- 中断语义：stop reason 映射为用户可读文案（完成/用户停止/超时/模型错误/达到输出上限/看门狗）。

### 4.10 可观测性与面板

- `/status`：当前阶段、耗时、token、排队数、后台任务数、当前任务 id。
- `/tasks`：本会话任务列表（id/kind/status/耗时/摘要）；`/tasks all` 管理员看全局。
- `/stop`：取消当前 turn，保留 Inbox；`/stop <id>` 取消指定任务。
- `/resume <id>`：恢复 `interrupted` 任务。
- Query 日志：新增 `stop_reason`、`task_id` 字段（实际落地为这两个；设计中的 `delegations` 委派率字段未实现）；Token 统计按任务维度可选汇总。
- 面板：Query 日志页增加停止原因筛选；新增"任务"页（P1，可先复用 Query 页）。

### 4.11 其他 agent 能力提升（对齐 DSH，按性价比排序）

| 能力 | 现状 | 建议 | 优先级 |
| --- | --- | --- | --- |
| 停止原因语义化 | 统一"请求超时/已停止" | `completed/aborted/max_tokens/error/watchdog/timeout` 全链路透传，UI 与提示词按原因给不同收尾 | P0 |
| token/成本可视化 | Query 日志已有 token | 增加"每回合/每任务/每子代理"维度；任务表带 tokens 归集 | P0/P1 |
| 工具结果治理 | 已有 spill（16000 rune 落盘） | 子代理结果接入；任务输出目录；IM 附件推送 | P0/P1 |
| Compaction | 80% 阈值、失败有损截断 | 阈值/保留比例参数化、溢出重试、工具结果先裁剪、摘要落事件 | P2 |
| Session fork / resume | 无 | 任务级 session 可 fork（带父上下文摘要）、崩溃后可恢复 | P2 |
| Plan 模式 | 硬屏蔽副作用工具 | 保留硬门禁；补充"计划产出后自动拆任务/子代理"的衔接 | P2 |
| Skills / MCP / Hooks / 审批 | 已有 | 不动；子代理与后台任务同样继承（当前已具备） | — |
| Agent preset / 多角色 | 无 | 不照搬 DSH preset 体系；IM 场景用"群/会话级模型与工具配置"轻量替代 | 暂不做 |
| 事件溯源 session log | 行级历史 + 内存窗口 | 不照搬；任务表 + 关键节点落库已足够 | 不做 |

### 4.12 安全、成本与失控防护

| 风险 | 防护 |
| --- | --- |
| 无限工具循环 | 重复调用提醒（3/5/8）+ 无进展终止 + `max_iterations` 可配兜底（默认 0=off） |
| 无限续跑烧钱 | goal max_rounds、任务 token 软预算、quota、`max_consecutive_wakes` 防自激、默认不自动 resume |
| 会话长期占用 | 每会话串行 + 任务级 wall clock 兜底（可配）+ 人类优先队列 |
| 重启丢任务 | 任务表 + interrupted 语义 + 启动恢复扫描 |
| 通知风暴 | 完成通知合并（同一 turn 内多条一次性注入）+ 链路深度/频率限制 |
| 权限扩大 | 子代理权限在启动时固定，审批仍在父会话；计划模式仍阻断副作用工具 |
| 平台限流 | 长任务进度消息节流；结果推送失败重试并入 outbox（P2） |

---

## 5. 关键改造点（文件级）

### 5.1 P0：解耦与放宽（低风险，先止血）

> 原则：**先补兜底，再放开限制**；先修现存缺陷，再改默认值。以下按依赖顺序排列。

| # | 文件 | 改动 |
| --- | --- | --- |
| 1 | `bot/component/aichat/llmclient.go` + 三个 `llmbackend_*.go` | 新增 per-request 超时（`option.WithRequestTimeout`，默认 600s）与**流空闲看门狗**（无增量超过 N 秒取消本次请求，默认 120s，可重试）；这是放开回合预算的前置兜底 |
| 2 | `bot/plugins/pluginaichat/config.go` | 新增 `turn`/`llm`/`subagent`/`task`/`goal` 配置节；`max_iterations` 默认改 0（0=不限）；help 文案同步；`defaultPrompt` 增加"任务编排"段 |
| 3 | `bot/plugins/pluginaichat/llm.go:224-229` | `mainMaxIterations()`：`<0` 保留兜底、`0` 表示不限、`>0` 为上限（删除"≤0 回退 20"，否则无法通过配置关闭）；调用点（子代理 `subagent.go:145`、`clock.go:809`、`proactive_exec.go:292`、`chatconsole.go:604`）自动跟随 |
| 4 | `bot/component/aichat/toolexecutor.go` | `maxIterations<=0` 时循环不设上限、不做末轮强制总结（`:157, :222-247`）；新增看门狗回调钩子（见 #5） |
| 5 | 新文件 `bot/component/aichat/watchdog.go` | 重复工具调用检测（`name+args` 哈希，阈值 3/5/8）→ 注入"换个方案"提醒；连续 N 步无新工具结果/无状态变化 → 终止并返回 `stop_reason=watchdog`；与 `ChatOptions` 的注入口配合 |
| 6 | `bot/plugins/pluginaichat/subagent.go` | 超时不再从 `msgEventTimeout` 派生：读 `subagent.default_timeout_sec`（默认 0=不限）；删除 `subagentMaxTimeoutSec=1800` 硬上限（改配置项）；仅在父 ctx **真有** deadline 时才做收尾预留与预算拒绝 |
| 7 | `bot/plugins/pluginaichat/subagent_async.go` + `plugin.go` + `llm.go` | **修 `/stop` 缺口**：无活跃主请求时也取消该会话全部后台子代理（`stopRequest` 之外增加"任务取消"路径，或 `/stop all`）；异步子代理注册到统一取消表，`OnUnload` 时全部取消；启动预检不再依赖父剩余预算 |
| 8 | `bot/plugins/pluginaichat/plugin.go` + `lock.go` | 事件总预算改读 `turn.soft_timeout_sec`/`turn.hard_timeout_sec`：hard=0 时不派生取消 ctx；**显式处理 `eventBudget==0` 语义**（当前 0 被当作"已过期"，`lock.go:407-414`）；排空循环改为"一批一轮、队列空即结束"，避免持续入队无限延长单事件（旧 bug）；`reportEventBudgetExhausted` 文案改为"已运行较久，正在收尾/可转后台" |
| 9 | `bot/plugins/pluginaichat/subagent_async.go` | 异步子代理结果接入长度治理：超 `subagent.result_inline_runes` 落盘（`./data/task-outputs/`）+ 摘要回灌；同时写入 Query 日志（P1 换任务表）；pending 队列容量/优先级复核（`pendingqueue.go:9-10`） |
| 10 | `bot/component/aichat/chatbot.go` + `memorywindow.go` | 单请求内上下文保护：每个 LLM step 前检查 prompt 规模，超过阈值先做一次压缩/裁剪再继续，避免长工具循环撑爆窗口（`:113-120, :150`） |
| 11 | `bot/plugins/pluginaichat/clock.go` + `clocksubagent.go` | 定时任务超时默认改为可配（`clock.default_timeout_sec` 0=不限，上限 1800s 可配）；定时子代理回喂轮数/等待预留改配置（`clocksubagent.go:19-25, :340-382`） |
| 12 | `bot/plugins/pluginaichat/subagenttool.go` + `config.go` | `subagent_run` description 重写（异步、可并行、不阻塞）；子代理提示词补充"结果自包含" |
| 13 | `bot/plugins/pluginaichat/statuscmd.go` | `/status` 增加 stop reason/后台任务数；新增 `/tasks`（P0 读内存任务）、`/stop all` |
| 14 | `bot/core/configfields.go:14` + `core.go:109-111` | 可选：为 `bot.msg_event_timeout_sec` 增加 0=不限制语义（只影响其他插件；AI 已由 #8 解耦）。建议保持默认不动，仅更新 help 说明"该值不再约束 AI 回合" |
| 15 | `bot/plugins/pluginaichat/config.go:307` + `plugin.go:909-914` | 并发公平性过渡：长回合会长期占用 `rate_limit`（默认 2）槽位；P0 至少提高默认值/文档说明，P1 拆"对话池 / 后台池" |
| 16 | 测试与文档 | 改写 `clocksubagent_test.go:240-343`（"总预算必须有上界"不变量）与 `locktest_fixes_test.go` 中依赖 300s/100 轮的断言；新增"无限轮数/无超时/看门狗"用例；修正 `docs/guide/configuration.md:324, :475`、`docs/internals/agent-tools.md:196-213`、`AGENTS.md:326,400-401,447`、内置 skill `reference/config.md`、`troubleshooting.md` 中与实现不符的描述 |

### 5.2 P1：结构化（解决阻塞、恢复、后台）

| # | 文件 | 改动 |
| --- | --- | --- |
| 1 | 新文件 `bot/plugins/pluginaichat/taskstore.go` | `ania_agent_task` 表 + CRUD + 状态机 + 启动时 interrupted 扫描（sqlite/mysql DDL，参照现有 `sqlstore.go`） |
| 2 | 新文件 `bot/plugins/pluginaichat/runner.go` | Session Runner：Inbox claim → 锁 → runTurn；消息入口只入队；`/stop` 保留 Inbox；长任务 ack |
| 3 | `pendingqueue.go` | 队列升级为 Inbox（`next-turn` 落库；保留内存快路径） |
| 4 | 新文件 `bot/plugins/pluginaichat/handoff.go` | 软截止/轮数阈值触发交接：摘要构造、后台任务创建、结果回灌 |
| 5 | `subagent_async.go` / `subagent.go` | 子代理接入任务表（id/状态/结果）；命名、结构化返回、steering（send） |
| 6 | 新文件 `bot/plugins/pluginaichat/jobtool.go` + `bot/component/functool/bash.go` | 后台 bash job：start/output/list/kill、完成通知 |
| 7 | `querylog.go` + 面板数据源 | `stop_reason`/`task_id`/`delegations` 字段；任务页/筛选 |
| 8 | `plugin.go` Start | 恢复扫描、分池并发（对话 2~4 / 后台 8）、灰度开关 |

### 5.3 P2：自主性（能力对齐 DSH）

| # | 内容 | 参考 | 完成情况（`feat/agent-autonomy`） |
| --- | --- | --- | --- |
| 1 | Goal 续跑（工具 + 表 + round driver） | DSH `packages/goal/*`、`goal-round-driver` | ✅ 已实现：`goal_create/update/status`、`ania_agent_goal`、回合结束续跑驱动、`/goal` 命令；blocked 语义简化（无 3 轮门槛）；重启后 disarm |
| 2 | 子代理树：`lead/agent-N` 命名、`list_agents`、`send_message`、fork（带父上下文种子） | DSH `tool-subagent-control`、`subagent-fork-in-process` | ⚠️ 部分实现：`subagent_run(name=)` 命名、`subagent_send` 运行中注入、结构化状态通知、`/stop <名字>`；**未做**树形 `list_agents`、fork（带父上下文种子）与跨重启续跑 |
| 3 | Team 任务板：CAS revision、blockedBy DAG、advisory write scopes、claim/complete | DSH `packages/team/*` | ⚠️ 部分实现：`team_task_create/list/claim/complete`、blockedBy 依赖、advisory write scopes、复用 `ania_agent_task(meta JSON)`；**未做** CAS revision/乐观并发 |
| 4 | Workflow：Go 原生 DAG/Step API（`RunSteps` + 并发/失败语义），不做 JS 沙箱 | DSH `packages/workflow`（降级） | ✅ 已实现：`workflow_run` 1~8 步 DAG、并发 1~4、失败后继 skipped、结果回灌；不提供独立 Go API 包 |
| 5 | Compaction 升级：阈值参数化、工具结果裁剪、溢出重试、摘要事件化 | DSH `compaction-basic` | ❌ 未实现（仍是 80% 阈值压缩 + 单请求内工具结果裁剪） |
| 6 | Session fork / resume / 任务级 session 历史 | DSH `docs/subsystems/session.md` | ❌ 未实现（`/resume` 用主会话历史续接，不 fork 独立会话） |
| 7 | 交付物：`present` 式的文件主动推送（IM 附件） | DSH `deliverables/tool-present` | ❌ 未实现（现有 `file` 工具只能由 AI 显式发送） |

---

## 6. 配置、数据与契约草案

### 6.1 新增/调整配置项

```yaml
plugin:
  ai_chat_bot:
    # 工具轮数上限（保留旧键位置，语义改为 0=不限）
    max_iterations: 0

    # 回合预算（原 bot.msg_event_timeout_sec 不再约束 AI 回合）
    turn:
      soft_timeout_sec: 600      # 软截止：注入收尾/交接提示，不取消；0=关闭
      hard_timeout_sec: 0        # 硬截止：0=永不（仅 /stop 与进程关闭可停）
      progress_reminder_after: 6 # 连续工具轮数达到 N 提示考虑委派；0=关闭
      repeat_tool_warn: "3,5,8"  # 相同工具+参数重复次数提醒阈值
      no_progress_limit: 12      # 连续 N 步无新工具结果/状态变化后终止

    # LLM 请求级保护（替代回合超时）
    llm:
      request_timeout_sec: 600
      stream_idle_timeout_sec: 120

    # 子代理
    subagent:
      enable: true
      default_timeout_sec: 0     # 0=不限制
      max_concurrent_per_session: 4
      max_concurrent_global: 8
      max_depth: 1
      token_budget: 0            # 单任务 token 软预算，0=不限
      result_inline_runes: 8000

    # 任务/后台
    task:
      max_background: 8          # 全局后台任务并发（与对话并发分池）
      wall_clock_sec: 0          # 任务总时长兜底，0=不限
      retention_days: 7
      auto_resume: false         # 重启后自动续跑（默认关闭）

    goal:
      enable: true
      max_rounds: 256
```

兼容处理：

- `bot.msg_event_timeout_sec` 继续约束**框架插件事件**（其他插件），不再作为 AI 回合预算；`pluginaichat` 仅在其缺失时回退（迁移期）。
- `plugin.ai_chat_bot.max_iterations` 保留键名，语义改为"0=不限"；旧值 100 继续生效（用户可改为 0）。
- 旧行为可通过 `runner.enable=false`（P1）与 `turn.hard_timeout_sec=300` 复现，便于回滚。

### 6.2 新增表 DDL

```sql
-- SQLite / MySQL 双版本，参照 bot/plugins/pluginaichat/sqlstore.go 的 EnsureTables 用法
CREATE TABLE ania_agent_task (
  id           TEXT PRIMARY KEY,
  session_key  TEXT NOT NULL,
  parent_id    TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL,          -- turn|subagent|handoff|bash_job|clock|proactive|goal_round
  status       TEXT NOT NULL,          -- queued|running|waiting|completed|failed|timeout|cancelled|interrupted
  title        TEXT NOT NULL,
  prompt       TEXT NOT NULL DEFAULT '',
  result       TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  stop_reason  TEXT NOT NULL DEFAULT '',
  tokens       INTEGER NOT NULL DEFAULT 0,
  meta         TEXT NOT NULL DEFAULT '',  -- JSON：阶段/工具计数/恢复提示
  created_at   INTEGER NOT NULL,
  started_at   INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL,
  finished_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_agent_task_session ON ania_agent_task(session_key, status);
CREATE INDEX idx_agent_task_status  ON ania_agent_task(status, updated_at);
```

（P2 已按方案落地：目标状态建独立表 `ania_agent_goal`（会话唯一目标，`session_key` 主键），`ania_agent_goal` 与任务表共用启动建表/降级路径。后续普通回合不再写入任务表，`goal_round` 类型已移除，目标续跑按普通回合记录在 Query 日志；任务表现在只登记真正的后台工作。）

### 6.3 新增/调整工具（模型可见）

| 工具 | 参数 | 说明 |
| --- | --- | --- |
| `subagent_run` | `task`(必填)、`name?`、`context?`(fresh/recent)、`timeout_sec?`、`output_schema?` | 异步必返；启动即返回 id/name |
| `subagent_list` | — | 状态/阶段/耗时/token |
| `subagent_send` | `id`、`message` | 运行中追加指令（下一步注入） |
| `subagent_cancel` | `id` | 取消并保留已完成结果 |
| `task_handoff` | `summary`、`remaining`、`acceptance?` | 把剩余工作交后台（软截止时模型调用） |
| `task_list` / `task_get` | `id?` | 任务查询（面板/用户命令同源） |
| `goal_create` / `goal_update` / `goal_status` | 见 4.7 | P2 |
| `job_start` / `job_output` / `job_list` / `job_kill` | 命令、wait、offset 等 | 后台 bash（P1） |

### 6.4 提示词片段（追加到默认系统提示词）

```markdown
## 任务编排（重要）
1. 判断任务规模：预计需要 3 步以上工具调用、超过 1 分钟、或需要多轮搜索/调研/读写多文件时，
   不要在本轮亲自做完——直接调用 subagent_run 委派给子代理，由它在后台执行。
2. 委派后不要等待、不要轮询：继续处理用户的其他问题，或者结束本轮。
   子代理完成后结果会自动回到本会话并触发你继续处理。
3. 多个互相独立的子任务，必须放在同一条回复里并行启动（一次调用多个 subagent_run），
   不要串行地一个一个等。
4. 主对话只负责：理解需求、澄清歧义、拆解任务、委派、汇总结果、对外汇报。
   执行细节（搜索、读文件、写代码、长命令）尽量交给子代理/后台任务。
5. 需要跨多轮持续推进的目标，用 goal_create 建立目标由系统自动续跑；
   目标没有完成证据时不要声称完成。
6. 时间预算：系统不会因为"跑得久"强行终止你；到达软截止时会提示你收尾。
   优先选择"把剩余工作交给后台"（task_handoff）而不是草草结束；
   交接时写清：目标、已完成部分、剩余步骤、验收标准。
7. 用户随时可能插话：新消息会作为下一步输入或新一轮处理；不要假设用户必须等你。
```

子代理提示词补充：

```markdown
## 返回格式
- 最终回复会作为结果返回给委派你的主 AI，请输出自包含结果：结论 + 关键证据 + 未完成项/风险。
- 不要输出中间过程（除非主 AI 明确要求）；中间消息不会发送给任何人。
- 若需要主 AI 决策，在结果开头标注【需要决策】并给出选项。
```

---

## 7. 分阶段落地计划

| 阶段 | 内容 | 预估 | 验收 |
| --- | --- | --- | --- |
| **P0** | 配置语义解耦（无限轮数/无限回合/子代理独立超时）、LLM 请求与流空闲看门狗、无进展看门狗、提示词与工具描述、`/status` 与 `/tasks` 只读、测试与文档 | 2~3 天 | A1/A2/A6 部分通过；`go test -race ./...` 全绿 |
| **P1** | 任务注册表、Session Runner（入口零阻塞）、Inbox 持久化、后台交接、子代理任务化（命名/结构化/steering）、后台 bash job、面板与 Query 日志、灰度开关 | 5~8 天 | A1~A5 全部通过；重启恢复演示；回滚开关验证 |
| **P2** | Goal 续跑、子代理树/fork/send_message、Team 任务板、Workflow DAG、Compaction 升级、session forge/resume | 2~3 周 | 长目标任务跨 10+ 轮完成；多子代理并行调研可交付。**已落地**：Goal 续跑、子代理命名/steering/结构化通知（树与 fork 未做）、Team 任务板（无 CAS revision）、Workflow DAG；**未落地**：Compaction 升级、Session fork/任务级历史、交付物推送 |

建议先只做 P0 并观察一周（Query 日志里的 stop reason、平均回合时长、委派率、token 消耗），再决定 P1 的范围。

**P0 过渡期注意**：

- 消息入口仍同步阻塞（P1 才做 runner），长回合会占用一个框架事件 worker。建议 P0 期间 `turn.hard_timeout_sec` 先设 1800~3600 作为过渡，P1 完成、入口零阻塞后再改为 0。
- 每项改动独立 PR/提交：先补 LLM 超时与看门狗（#1/#5），再放开配置（#2~#4），最后改子代理与预算（#6~#9）；任一步可单独回滚。
- 测试：现有 `clocksubagent_test.go:240-343` 中"总预算必须有上界"不变量在放开后不再成立，需替换为"看门狗/取消能在有限时间收口"的新不变量；`locktest_fixes_test.go` 中 300s 预算用例同样需要重写。

**验证与观测（每阶段必做）**：

1. 单测：`go test -race ./bot/plugins/pluginaichat ./bot/component/aichat ./bot/core`。
2. 构造性测试：mock LLM server 挂起不返回 → 验证 per-request 超时能释放会话锁与 `rateCh` 槽位；mock 流式响应中途静默 → 验证 idle 看门狗生效。
3. 长任务回归：跑一个 ≥15 分钟、≥150 次工具调用的任务，确认无"时间上限"提示、无轮数截断、上下文不溢出。
4. 观测：Query 日志新增 `stop_reason` / `task_id` / `delegations` 后，观察一周的停止原因分布与单回合时长分位数。

---

## 8. 风险与缓解

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 去掉硬超时后请求挂起 | 会话被永久占用、进程无法优雅退出 | LLM 请求超时 + 流空闲看门狗（P0 #1 先行）；工具自身超时；看门狗兜底 |
| 去掉事件预算后 LLM 层无任何时限 | 上游挂死 → 会话锁/并发槽永久占用（当前事件预算是唯一兜底） | #1 必须先于 #8 合并；构造性测试验证 |
| 模型陷入无限循环 | 烧 token、卡死会话；无循环检测时重复执行有副作用工具 | 重复调用提醒、无进展终止、token 配额、用户 /stop；计划模式门禁仍在 |
| 异步子代理不可取消/不可观测 | 后台任务失控、进程退出直接丢失 | 修 `/stop` 缺口、统一取消表、`OnUnload` 清理（P0 #7）；任务表持久化（P1） |
| 取消并发/深度限制后资源暴涨 | OOM/费用 | 对话池与后台池分开、每树/全局上限、quota、任务 token 软预算 |
| 消息/结果丢失面扩大 | 长任务期间 pending 队列更易满 | 提高容量 + 结果落盘 + 合成结果优先/持久化（P0 #9/P1）；区分可恢复错误 |
| 上下文爆炸 | 单请求内不压缩 + 子代理结果不截断 → overflow、结果丢失 | 单请求内压缩检查（P0 #10）+ 结果治理（P0 #9）；保留现有 spill 与窗口压缩 |
| 消息入口异步化引入乱序/丢失 | 用户消息丢失 | Inbox 落库 + 幂等通知 + 迁移期灰度开关 + 保留旧路径 |
| 重启恢复把旧任务重新跑一遍（重复副作用） | 重复发送/重复写 | 默认不自动 resume；恢复时带 recovery note 要求先核对；任务幂等键 |
| 定时任务堆积 | 长任务并发多，挤占 LLM/工具资源 | 全局后台并发上限；clock 任务互斥保护保留 |
| 提示词改动导致模型"过度委派" | 简单问题也开子代理，成本上升 | 提示词给出明确阈值 + 委派率统计 + 可调 `progress_reminder_after` |
| 长任务结果通知刷屏 | 群聊体验差 | 完成通知合并/节流；progress 不外发；失败才提示 |
| 已有测试大量依赖旧预算语义 | 回归风险 | 先补语义测试再改代码；保留旧行为开关；分阶段合并 |
| 启动建表可能静默失效 | `Start` 1 分钟预算 + 串行 MCP 30s 连接可能导致 `EnsureTables(startCtx)` 使用过期 ctx → 历史持久化静默关闭（静态推断，需实测） | P0 顺手排查：建表用独立 `context.Background()` + 超时，不继承启动预算 |

---

## 9. 兼容性与迁移

- **配置**：新增键全部有默认值；旧键 `max_iterations`、`bot.msg_event_timeout_sec` 保留语义兼容；不迁移数据。
- **行为开关**：`runner.enable`（P1 灰度）、`turn.hard_timeout_sec`（复现旧行为）、`task.auto_resume`（默认关）。
- **面板**：新增配置分组（AI 对话 · 任务/子代理），旧页面不改布局。
- **文档**：同步更新 `AGENTS.md`（`:326, :400-401, :447` 等子代理/预算描述）、`docs/internals/agent-llm.md`/`agent-context.md`/`agent-tools.md`、`docs/guide/configuration.md`、内置 `aniabot` skill 的 `reference/config.md`/`troubleshooting.md`、`CHANGELOG.md`。现有文档与实现不符处见附录 C.5。
- **数据**：`ania_agent_task` 为新增表，启动时 `EnsureTables` 失败则降级为内存任务（不阻断启动，遵循现有降级约定）。

---

## 10. 待决策问题（评审用）

> 以下问题已随 P0+P1 实施定案（决策就地标注）；P2 相关决策仅作为后续实现口径归档。

1. **P0 是否立即实施**，还是等 P1 设计一起评审？→ **已决策：P0+P1 一起实施**（同一分支，先补 LLM 请求超时/看门狗再放开限制）。
2. **硬截止默认值**：建议默认 0（不限制），由看门狗与 `/stop` 兜底；保守可设 4 小时。→ **已决策：默认 4 小时**（`turn.hard_timeout_sec=14400`，可配 0 关闭）。
3. **子代理默认超时**：建议 0（不限制）；保守可设 30 分钟。→ **最终实现默认 0（不限制）**，需要上限时可用 `subagent.default_timeout_sec` 设正数（如 1800 = 30 分钟）；`subagent.max_timeout_sec=0` 表示模型传入值不设业务上限。
4. **是否引入 Goal 自动续跑**（P2）：收益大但成本不可控，建议默认开启但 `max_rounds` 保守（如 64），并提供 pause。→ **已决策（P2 口径）：默认开启，`max_rounds=64`**；**已在 P2 落地**（`plugin.ai_chat_bot.goal.enable` / `goal.max_rounds`，硬上限 500；进程重启后需 `/goal resume` 重新武装）。
5. **并发分池**：对话 `rate_limit` 是否从 2 调到 4？后台任务独立池默认 8。→ **已决策：`rate_limit=4`（仅对话回合），后台子代理独立池 `subagent.max_concurrent_global=8`。**
6. **重启自动续跑**：建议默认关闭（需用户 `/resume`），避免升级重启后自动烧钱。→ **已决策：默认不自动续跑**；启动时把遗留任务标记 `interrupted`，用户 `/resume <任务ID>` 手动恢复（仅本会话、仅子代理/交接任务）。

---

## 附录 A：DSH / 原型的机制对照（可移植性）

| DSH / 原型机制 | 证据 | 本方案落点 |
| --- | --- | --- |
| 无 turn 硬预算，预算下沉到工具/流 | `packages/core/agent-loop/README.md:238`；`guard/timeout-policy/src/index.ts:55-80` | 4.2 三级预算 |
| 完成通知 = user 合成消息；idle 开新 turn / busy 下一步注入 | `packages/subagent/subagent/src/manager.ts:1136-1158`；jobs `tool-jobs/src/index.ts:353-393` | 4.3/4.5 Inbox + 通知 |
| 子代理立即返回、命名树、send_message、深度/并发上限 | `packages/subagent/tool-subagent/src/index.ts:210-217,328-336`；`docs/subsystems/subagent.md` | 4.6 Subagent 2.0 |
| 委派提示词（同一条消息并行启动、不轮询、结束回合等通知） | `tool-subagent/src/index.ts:379`；`tool-jobs/src/index.ts:320` | 4.9 / 6.4 |
| goal durable + round driver（默认 256 轮、重启 disarm） | `packages/goal/*`；`docs/subsystems/goal.md` | 4.7 |
| compaction（0.8/16%/溢出重试/工具结果裁剪） | `packages/compaction/compaction-basic/src/config.ts` | P2-5 |
| 原型：任务会话与对话会话分离、一任务一目录 | `dsh-superagent-prototype/src/executor/dsh.ts:23-26,424-470` | 4.3 交接（IM 下用独立会话键代替目录） |
| 原型：主循环禁止等待（sleep/长 job wait 守卫） | `dsh-superagent-prototype/src/app/sessions.ts:63-86` | 4.5 入口零阻塞 + 提示词 |
| 原型：六态 report（progress/waiting/ready/blocked/failed/dropped） | `src/core/model.ts:211-232`；`prompts/work.md:36-46` | 4.6/4.7 结构化状态 |
| 原型：持久化副作用 + outbox 幂等投递 | `src/app/effects.ts:42-160`；`src/app/outbox.ts:22-51` | P1 任务表 + 通知幂等 |
| 原型：人类优先并发槽 | `src/executor/dsh.ts:166-199` | 4.11 分池 + 人类优先 |

## 附录 B：本仓库关键代码索引

| 主题 | 文件 |
| --- | --- |
| 框架事件超时 | `bot/core/core.go:86-116, :745-817`；`bot/core/configfields.go:14` |
| 回合预算/排空循环 | `bot/plugins/pluginaichat/plugin.go:371-419`；`lock.go:232-415` |
| 工具循环 | `bot/component/aichat/toolexecutor.go:41-53, :126-248` |
| 会话锁与并发槽 | `bot/plugins/pluginaichat/lock.go:16-214`；`plugin.go:907-914` |
| 排队队列 | `bot/plugins/pluginaichat/pendingqueue.go` |
| 子代理（同步/异步/工具） | `bot/plugins/pluginaichat/subagent.go`、`subagent_async.go`、`subagenttool.go` |
| 定时任务子代理 | `bot/plugins/pluginaichat/clocksubagent.go` |
| 会话创建/工具注册 | `bot/plugins/pluginaichat/llm.go:163-222, :322-414` |
| 系统提示词与默认值 | `bot/plugins/pluginaichat/config.go:10-68, :264-353` |
| LLM 客户端/重试 | `bot/component/aichat/llmclient.go`；`llmbackend_*.go` |
| 工具级超时 | `bot/component/functool/bash.go:22-23, :222`；`bot/component/llmtool/mcp.go:65` |
| 任务/日志 | `bot/plugins/pluginaichat/querylog.go`；`bot/component/querylog/`；`bot/component/tasklog/` |

## 附录 C：审计发现的现存缺陷（建议随 P0 顺手修）

> 均基于当前 `main`（`965a7b9`）静态审计；最后两条为静态推断，需实测确认。

1. **`/stop` 取消不到后台子代理**：`stopRequest` 只在 `activeContexts` 有活跃主请求时返回 `stopped=true`（`llm.go:86-105`），而 `/stop` 只有在 `stopped` 为真时才 `cancelAsyncSubagents`（`plugin.go:255-259, :461-465`）。主回合结束后仍在跑的子代理无法被用户停止，也没有 `/stop all`。
2. **`b.Go` 后台任务不随关停取消**：`core/tracer.go` 的 `b.Go` 只做 panic recover，不接 ctx；异步子代理用 `context.WithCancel(context.Background())`（`subagent_async.go:171`），`AIChatPlugin` 又没有 `OnUnload` 清理入口 → 进程退出只能硬杀。
3. **异步子代理结果不截断/不落盘**：`subagent.go:193` 明确"结果不截断"，`subagent_async.go:207-219` 直接拼进 user 消息，绕过 `tool_result` 治理；长报告会撑爆上下文。
4. **单请求内不压缩**：`chatbot.go:113-120` 只在 `Chat` 开头压缩；工具循环内 messages 持续增长可溢出，`chatbot.go:150-161` 整批失败且不落盘。
5. **文档与实现不符**：
   - `docs/guide/configuration.md:324`：`max_iterations` 默认写 20，实际配置默认 100（`config.go:309`），只有 `≤0` 兜底才是 20（`llm.go:224-229`）。
   - `docs/guide/configuration.md:475`、`docs/internals/agent-tools.md:196-213`、`AGENTS.md:447`：异步子代理"按父剩余预算收缩"，实际异步路径只在启动前预检剩余 >30s，执行超时不收缩、不随父取消（`subagent_async.go:162-177`）；"结果完整回填且超限落盘"对异步路径不成立（见缺陷 3）。
   - `plugin.go:974` 注释称 OCR 客户端"不配备用模型切换"，但实际 `llmClientOptions()` 在配置了 fallback 时会拼上 `WithFallback`。
6. **`max_iterations` 无法通过配置关闭**：`mainMaxIterations()` 对 `≤0` 回退 20（`llm.go:224-228`）；而 `ToolOrchestrator` 在 `maxIterations<=0` 时直接返回 `exceeded maximum iterations`（`toolexecutor.go:247`）——两个实现方向相反，改配置容易踩坑。
7. **`eventBudget()==0` 语义隐患**：`drainChatBatches` 把 `remaining<=0` 当作预算耗尽（`plugin.go:387-392`），而 `eventBudget` 在父 deadline 到期时正返回 0（`lock.go:407-413`）——实现"0=不限"时必须用显式布尔值区分，不能复用 0。
8. **`Start` 1 分钟预算可能导致建表失败**（静态推断）：`pluginaichat` 串行连接 MCP（每个 30s 外层超时）可能吃掉启动预算，`EnsureTables(startCtx)` 若用过期 ctx 会静默使 `historyDB=nil`，历史持久化随之关闭。建议建表使用独立 `context.Background()`。
9. **后台清扫器 30s/会话预算**（`subagent_async.go:327-341`）会打断由排队消息触发的长任务；P1 入口零阻塞后应改为"每批一轮、队列空即结束"。
10. **压缩失败的有损降级**：`memorywindow.go:248-255, :287-296` 在压缩请求失败时 `truncateOldestHalf` 并 `persistReplace`，旧历史被永久删除；长任务该路径更易触发，建议保留摘要或标记"被截断"。
