# 排障手册

排障的顺序永远是：**先看日志 → 定位是「没连上 / 没配置 / 被拦住 / 超时 / 外部接口报错」中的哪一类 → 再去改对应的配置键**。不要凭猜测改一堆设置。

## 一、日志在哪儿看

| 位置 | 面板路径 | 内容 | 保留 |
| --- | --- | --- | --- |
| 控制台日志 | 控制台日志 `/#/console` | 进程日志（slog + 标准 log）：启动流程、适配器连接、配置加载、插件加载、报错栈 | 约 2000 条，**纯内存，重启即清空** |
| 对话记录 | AI 运行记录 → 对话记录 `/#/querylogs` | 每次 AI 请求的触发、工具调用、token 消耗、最终输出；含主动互动与面板试聊（失败/跳过也有记录）；普通对话带停止原因（异常在列表标出）与历史关联任务 ID（可跳后台任务页签） | SQL 持久化 |
| 后台任务 | AI 运行记录 → 后台任务 `/#/agent-tasks` | AI 后台任务注册表（子代理/交接/工作流/追问/团队任务/团队运行/团队成员；普通回合不写入也不展示）与进行中的长任务目标；可按状态/类型/关键词筛选，点击行可看任务指令、成员产出与工具调用步骤（内容完整保存，不截断），并沿父/子任务展开追问链；支持按筛选条件导出 JSON、清除已结束记录（运行中/排队中保留） | SQL 持久化 |
| 操作日志 | 消息与操作日志 → 操作日志 `/#/oplogs` | 人/AI 的操作审计：改配置、重启、装插件、审批 | SQL 持久化 |
| 任务日志 | AI 定时任务 `/#/tasklogs` | 定时任务的每次执行结果与推送状态 | SQL 持久化 |
| 消息日志 | 消息与操作日志 → 消息日志 `/#/msglogs` | 全部进出消息（含 Bot 自己发的），按会话分组 | 约 5000 条，存在缓存存储（`memory` 驱动重启清空，`redis` 驱动保留） |

如果 Bot 连启动都失败，只能看**进程的标准输出/stderr**（`docker logs`、systemd journal 或终端）。

## 二、启动与平台连接

**现象：启动后平台一直不在线（状态总览显示未连接/重连中）**

1. 先看控制台日志有没有 `首次启动：暂不连接平台适配器…`。有则说明**首次设置向导还没完成**（`meta.setup_pending=true`），去面板走完向导并重启。
2. 平台开关是否打开：`bot.platform.<名字>.enable`（改完必须重启）。
3. 连接参数是否匹配：
   - NapCat：`bot.adapter.mode` 与 `ws.address` / `http.target_url` 是否和 NapCat 侧配置一致；NapCat 设了 token 时要填 `bot.adapter.token`。
   - Luckylilia：`bot.luckylilia.*`（NapCat 的键不能复用）。
   - 飞书：`app_id` / `app_secret`；webhook 模式还要公网可达 + `verification_token` / `encrypt_key`。
   - Telegram / Discord：`token`；Discord 必须在 Developer Portal 开启 **Message Content Intent**，否则网关拒绝连接。
   - 微信：不需要填连接配置，首次要**扫码登录**（控制台二维码，或面板二维码登录入口）；凭据存在 `bot.weixin.state_dir`（默认 `./data/weixin`），容器重建要挂持久卷。
4. 适配器状态里的 `adapter_detail` 一般会带失败原因（地址拒绝、鉴权失败、DNS 等）。

**现象：进程启动即退出**

看 stderr。常见原因：端口被占用（面板 `bot.admin_panel.listen` 或 `bot.adapter.http.listen_port`）、SQLite 文件不可写、MySQL DSN 错误、`ANIABOT_STORE_DRIVER` 拼错。

## 三、AI 不回复

按顺序排除：

1. **配置缺失**：控制台日志里搜 `AI 对话未配置`。缺少 `plugin.ai_chat_bot.base_url` / `model` / `api_key` 任一项时 AI 插件会降载（消息直接跳过，不报错刷屏）。补齐后重启。
2. **没触发**：群聊必须 **@机器人**；私聊直接说即可。还可能被请求拦截插件挡住：`plugin.interceptor.enable` / `mode` / `groups` / `friends` / `group_users` / `platforms`（未勾选的平台直接拦截）。
3. **在忙或排队**：提示「当前请求太多啦，请稍后再试~」是 `rate_limit` 并发上限（默认 4）且该会话排队消息被清空；「排队消息太多啦」是排队队列满。等前一条结束或调大 `rate_limit`（后台子代理有独立并发池，不占这个名额）。
4. **运行很久 / 被停止**：AI 回合不再受 `bot.msg_event_timeout_sec` 限制。软截止（`turn.soft_timeout_sec`，默认 600 秒）只会提醒模型收尾/转后台，不会中断；硬截止（`turn.hard_timeout_sec`，默认 4 小时）才会取消并提示「已超过最长运行时间」；「任务因重复执行相同工具调用被终止」是看门狗（`turn.repeat_tool_warn` / `turn.no_progress_limit`）。单次请求挂起看 `llm.request_timeout_sec` / `llm.stream_idle_timeout_sec`（超时按可重试错误处理，会重试/切备用模型）。
5. **任务想继续 / 想停后台任务**：`/tasks` 看本会话运行中子代理、最近后台任务与最近回合；`/resume <任务ID>` 恢复进程重启前被中断（interrupted）的子代理/交接任务（仅本会话）；`/stop` 取消当前回合并清空排队（同时暂停自动续跑目标），`/stop <任务ID 或名字>` 只取消指定后台子代理。
6. **长任务目标不自动续跑**：先 `/goal` 看状态与「已续跑 N/M 轮」。常见原因——① 进程重启过：续跑武装状态不落库，目标仍是 active 但不会自动继续，用户在当前会话发 `/goal resume` 即可；② 用户之前 `/stop` 过：目标被暂停，`/goal resume` 恢复；③ 达到 `plugin.ai_chat_bot.goal.max_rounds`（默认 64）：状态变 `blocked`、原因 `round-limit`，可 `/goal clear` 后让 AI 重新建目标（或建目标时给更大 `max_rounds`，硬上限 500）；④ 会话一直有排队消息或运行中的子代理：等它们收敛后才会续跑；⑤ `goal.enable=false` 或 SQL 存储不可用（目标工具根本没注册）。
7. **工作流 / 团队任务板失败**：`workflow_run` 在启动前就会校验 DAG（步骤 1~8、ID 唯一、依赖存在且无环），参数错误会直接返回原因；执行中单步失败时依赖它的步骤标记 `skipped`，其余分支继续，最后回灌汇总。`workflow_run` 需要 `subagent.enable=true`，`team_task_*` 需要 SQL 持久化（任务表建不起来时返回「任务板未启用」）；团队任务在 `blocked_by` 依赖未完成时不能认领，先 `team_task_list` 看依赖状态。
8. **停止原因怎么读**：「AI 运行记录 → 对话记录」列表（异常结束会标出）与详情里的 `stop_reason`——`completed` 正常完成、`aborted` 中止、`max_tokens` 达到输出上限、`error` 出错、`watchdog` 循环保护终止、`timeout` 超时、`user_stop` 用户 `/stop`；后台任务表里的状态另有 `cancelled`（取消）/`interrupted`（进程重启中断，可 `/resume`）。
9. **停在审批**：AI 调用了需审批的工具（`approval.tools` 里的工具、或 `config_set` / `config_file_set` 恒需管理员审批），会私聊通知管理员回「允许」/「拒绝」，超时（默认 120 秒，`approval.timeout_sec`）自动拒绝。管理员没收到 → 检查 `bot.admin_ids` 是否配了、ID 是否带正确平台前缀。
10. **配额用尽**：开了 `quota.enable` 且当日超标时请求被拒，面板「Token 用量 → 配额限制」可看/重置。
11. **上游报错**：「AI 运行记录 → 对话记录」会有错误原文。401 = Key 失效，402/余额不足 = 充值，429 = 限流（可调 `retry.*`、配 `fallback.model` 备用模型），400 多半是 `api_format` 与 `base_url` 不匹配（如拿 Anthropic 地址配成 `chat_completions`）。

## 四、回复质量与上下文

- **忘了前文 / 被截断**：`max_context_tokens` 太小（默认 128000）会让 `messageWindow` 频繁压缩历史；压缩质量差可以配 `compressor.model`（更便宜/更聪明的模型）。
- **群聊突然开新对话**：`session.no_mention_clear` 默认 30 —— 连续 30 条未 @ 就清空该群历史。想保留上下文就调大或设 0，也可以让用户发 `#新对话` 主动清空。
- **人设不对**：`plugin.ai_chat_bot.prompt`（全局）；某个群/人单独覆盖用 `files.prompt_json`（数秒生效）。注意改人设后要重启才会重新注入（除 prompt_json 覆盖）。
- **回复拆成多条 / 合并转发**：AI 用 `|||` 分段；群聊拆出超过 3 段会在支持合并转发的平台（QQ）合成一条合并转发，其他平台逐条发。
- **流式回复异常**：`stream.enable`（默认开）只在飞书/Telegram/Discord 等支持消息编辑的平台生效，失败会自动退化为一次性发送；不需要可以关掉。

## 五、工具与权限

| 现象 | 原因与处理 |
| --- | --- |
| AI 说没有某能力（命令、读写文件、截图） | 对应开关没开：`bash.enable` / `file_tools.enable` / `file.enable` / `local_image.enable` / `computer_use.enable`（默认全关，改完重启） |
| 工具执行被拦 | 计划模式（`/plan on` 后改文件、跑命令、改配置、建任务、写记忆都被禁）；或工具审批在等管理员 |
| bash 命令被拒绝 | `bash.blacklist` 命中，或 `bash.whitelist` 非空但没匹配上；未列名命令在 `approval.enable=true` 时要走审批 |
| 文件工具报路径越界 | 配了 `file_tools.root`，路径必须在该目录内 |
| 截图/鼠标键盘不可用 | `computer_use.enable` 只支持 **Windows 宿主机**，其他平台编译为不支持 |
| MCP 工具调不出来 | `files.mcp_json` 格式错（保存时只校验 JSON 语法，语义错要重启看日志）；`mcp.lazy_load=true` 时先 `mcp_discover_<服务器>` 再 `mcp_load_<服务器>`；服务器命令本身要能在宿主机跑通 |
| 技能不生效 | 白名单 `plugin.ai_chat_bot.skills` 漏了它 / 被禁用（`skills_disabled`）/ 外置技能改文件后没 `skill_reload` |
| AI 想改配置但被拒 | `config_tool.enable` 没开，或管理员审批超时/拒绝 |
| 后台任务 / 子代理相关的任务查不到 | 任务记录需要 SQL 持久化：确认存储后端为 sqlite/mysql 且启动日志没有「创建后台任务表失败」；普通回合不写任务表（到「对话记录」查），也不在后台任务页签展示；`/tasks` 仍可看到内存中运行中的子代理与最近回合 |
| 工作流 / 任务板用不了 | `workflow_run` 需要 `subagent.enable=true`；团队任务板需要 SQL 持久化（建表失败会提示「任务板未启用」）；工作流参数不合法（步骤数、ID 重复、依赖缺失/成环）会在启动前直接报错 |
| 长任务目标不续跑 | 进程重启后需 `/goal resume`；`/stop` 会暂停目标；轮数用尽会 `blocked(round-limit)`；`goal.enable=false` 或 SQL 不可用时目标工具不注册 |
| 进程重启后长任务没了 | 启动时遗留任务会被标记 `interrupted`（不自动续跑）；用户在该会话发 `/resume <任务ID>` 让主 AI 带历史继续（仅子代理/交接任务）；长任务目标发 `/goal resume` 重新武装 |

## 六、配置改了没反应

99% 是**没重启**。只有这些是热生效：`files.prompt_json` / `files.hooks_json` / `files.commands_json`（数秒）、面板技能启停、`/proactive on|off`、`bot.balance.*`。

其他易错点：
- 面板保存了但没点「保存」就切页 → 面板有「未保存修改」提示。
- 在环境变量里覆盖了同一个键（`ANIA_*`），优先级高于数据库 → 看启动日志「环境变量覆盖配置」。
- 值类型不对：布尔要 `true`/`false`，数组要 JSON（`["a","b"]`），`int` 别加引号。
- 改的是 `bot.platform.xxx.enable` 但连接参数在另一个分组（`bot.xxx.*`），只开了开关没填参数。

## 七、平台差异导致的“看起来像 bug”

- **QQ 官方**：不能 @ 群成员；无历史消息/群资料 API（历史靠内存缓存）；被动回复有频控（群 5 分钟 5 次、单聊 60 分钟 4 次）；不支持流式。
- **飞书**：卡片交互、流式都支持；webhook 模式要公网 HTTPS。
- **Telegram**：`parse_mode=html` 最稳；`markdown`/`markdownv2` 遇到 AI 输出易解析失败后降级纯文本。
- **Discord**：不开 Message Content Intent 连不上；语音频道不会出现在通讯录。
- **微信**：扫码登录、token 可能过期（日志提示后会重新出二维码）；媒体走 CDN 加解密。
- **Luckylilia**：与 NapCat ID 前缀不同（`lil:`），配置项也各一套，排障别串。

## 八、数据与持久化

- 日志出现 `创建对话历史表失败，历史将不持久化` → SQL 探测或建表失败（数据库权限/只读/连接问题）。此时对话历史只在内存，重启丢。
- 记忆/知识库/主动互动需要 SQL 后端；建表失败时这些功能会**自动禁用**（不阻塞启动），控制台会有对应日志。
- 没有 SQL 后端（或探测失败）时，`/clock` 任务、长期记忆、知识库、主动互动都不可用 —— 换回默认 SQLite 即可恢复。
- 备份：停 Bot 后复制整个 `data/`；只备份配置用面板「配置管理 → 导出 JSON」或配置预设。
- **不要**手改 `data/aniabot.db`（`write_file` / `edit_file` 对 `aniabot.db` 有显式保护）。

## 九、面板

- 打不开：进程是否在跑（先看进程/容器状态）→ 监听地址（`127.0.0.1` 只能本机访问）→ 防火墙/端口占用 → 是否在启动阶段就崩了。
- 密码忘了：停 Bot 后 `./AniaBot -set-password 新密码`。
- 面板被关：`ANIA_BOT_ADMIN_PANEL_ENABLE=true` 临时启动。
- 面板能开但接口 401：登录态过期（24 小时滑动），重新登录。

## 十、插件、技能与更新

- **插件市场安装失败**：多为编译失败（插件与当前版本不兼容、网络拉不到依赖）。面板会显示构建日志，可用「回滚」回到之前版本。
- **装完插件没出现**：安装流程需要重启，等面板进度完成；确认插件与当前平台/`Meta.Platforms` 不冲突。
- **自动更新失败**：`bot.update.source_dir` 为空（功能禁用）、目录不是 git 仓库、构建失败。`go run` 开发模式下自动更新不可用。
- **技能上传失败**：SKILL.md 缺 frontmatter 或 `name` 与已有技能冲突（含内置技能）；zip 结构不对要保证根目录或单层目录下有 SKILL.md。

## 十一、反馈问题该收集什么

1. Bot 版本与部署方式（二进制 / Docker / 源码）、操作系统。
2. 控制台日志里从启动到出错那一段（面板「控制台日志」可复制）。
3. 相关配置的键和值（**密钥请打码**）。
4. 复现步骤，以及「对话记录」里对应那条请求的编号/时间。
