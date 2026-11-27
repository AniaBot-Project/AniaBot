# AniaBot 配置全景

配置全部存在数据库（持久化存储的 `ania_kv` 表），**没有 yaml/json 配置文件**。面板「配置管理」页按分组动态渲染，键名是点分路径（大小写不敏感）。

- 修改后**一律重启生效**，除非下文明确标注「热生效」。
- 值类型：字符串直接写；布尔/数字/数组/对象用 JSON 写法（`true`、`0.6`、`["a","b"]`）。
- 敏感字段（API Key / Token / Secret / 密码）在面板里显示为掩码，AI 工具读到的是 `********`。

## 一、引导环境变量（不走数据库）

这几项必须在数据库打开之前确定，只能用环境变量：

| 变量 | 说明 | 默认 |
| --- | --- | --- |
| `ANIABOT_STORE_DRIVER` | 持久化驱动：`sqlite` / `mysql` | `sqlite` |
| `ANIABOT_SQLITE_PATH` | SQLite 文件路径 | `./data/aniabot.db` |
| `ANIABOT_MYSQL_DSN` | MySQL 标准 go-sql-driver DSN（驱动为 mysql 时必填） | 无 |

## 二、环境变量覆盖（临时、不写库）

数据库里的**任意键**都能用环境变量临时覆盖，优先级高于库里的值，适合容器/调试。命名规则：`ANIA_` + 键名大写、`.` 与 `-` 换成 `_`：

| 配置键 | 环境变量 |
| --- | --- |
| `bot.admin_panel.listen` | `ANIA_BOT_ADMIN_PANEL_LISTEN` |
| `bot.admin_panel.enable` | `ANIA_BOT_ADMIN_PANEL_ENABLE` |
| `plugin.ai_chat_bot.api_key` | `ANIA_PLUGIN_AI_CHAT_BOT_API_KEY` |

非字符串按 JSON 解析（如 `ANIA_BOT_MSG_EVENT_TIMEOUT_SEC=600`）。生效时启动日志会打印「环境变量覆盖配置 key=…」。典型用途：面板被误关时用 `ANIA_BOT_ADMIN_PANEL_ENABLE=true` 临时拉起。

## 三、bot.* —— 框架

### Bot 基础

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `bot.admin_ids` | 空 | 管理员 ID 列表，每行一个，可多个。QQ 写 `qq:QQ号`；其他平台写带前缀 ID（`fs:ou_xxx`、`tg:123456`、`dc:123456`、`wx:…`、`qo:…`、`lil:…`）。管理员可 `/reboot`、`/exit`、看全部定时任务、接收启动/异常通知、审批 AI 改配置 |
| `bot.msg_event_timeout_sec` | `300` | 单条框架消息事件的最长执行时长，超时强制中止；**AI 的 IM 回合已改为后台运行器，不再受它限制**（仅面板试聊室等 AI 内层路径仍使用）。0 或负数回退 300 |

### Web 面板

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `bot.admin_panel.enable` | `true` | 是否启用面板 |
| `bot.admin_panel.listen` | `127.0.0.1:7700` | 监听地址；`0.0.0.0:7700` 可局域网访问（有密码保护） |
| `bot.admin_panel.media_direct` | `true` | 消息日志的图片优先由浏览器直连平台 CDN，失败回退面板代理；改后刷新面板即生效 |

### 缓存存储（与持久化存储不同）

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `bot.store.cache.driver` | `memory` | `memory`（进程内，重启清空）或 `redis`（多实例共享） |
| `bot.store.cache.redis.address` | `localhost:6379` | Redis 地址 |
| `bot.store.cache.redis.password` | 空 | Redis 密码（敏感） |
| `bot.store.cache.redis.db` | `0` | Redis DB 序号 |

### 自动更新

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `bot.update.source_dir` | 空 | AniaBot 仓库的克隆路径；**留空则禁用自动更新** |
| `bot.update.git_url` | 空 | 非空时更新前覆盖源码目录的 origin |
| `bot.update.branch` | `main` | 跟踪分支 |

### API 余额查询（面板概览页）

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `bot.balance.enable` | `false` | 是否在概览页显示 API 余额（改后即时生效） |
| `bot.balance.cache_sec` | `300` | 结果缓存秒数 |
| `bot.balance.url` | 内置 DeepSeek 风格地址 | 支持 `${base_url}` `${api_key}` `${model}` 占位符 |
| `bot.balance.method` | `GET` | `GET` / `POST` |
| `bot.balance.headers` | 内置 | JSON 对象，值支持上述占位符 |
| `bot.balance.body` | 空 | POST 请求体，支持占位符 |
| `bot.balance.format` | 内置 | 显示模板，`{gjson.path}` 取响应字段 |

## 四、平台适配器

每个平台一个总开关 `bot.platform.<name>.enable`，勾选并填好连接信息后**重启**才连。默认只有 NapCat 打开。

| 平台 | 开关键 | 平台 ID 前缀 | 接收方式 |
| --- | --- | --- | --- |
| QQ（NapCat） | `bot.platform.napcat.enable`（默认 true） | 无（纯数字） | WebSocket / HTTP，OneBot v11 |
| QQ（Luckylilia/LLBot） | `bot.platform.luckylilia.enable`（false） | `lil:` | WebSocket / HTTP，OneBot v11 |
| QQ 官方 | `bot.platform.qqofficial.enable`（false） | `qo:` | 官方 WebSocket 网关，无需公网 |
| 飞书 | `bot.platform.feishu.enable`（false） | `fs:` | 长连接 ws（推荐，无需公网）/ webhook |
| Telegram | `bot.platform.telegram.enable`（false） | `tg:` | 长轮询，无需公网 |
| Discord | `bot.platform.discord.enable`（false） | `dc:` | Gateway WebSocket，无需公网 |
| 微信 | `bot.platform.weixin.enable`（false） | `wx:` | iLink bot 长轮询，扫码登录 |

### QQ（NapCat）—— `bot.adapter.*`

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `bot.adapter.mode` | `ws` | `ws`（推荐）或 `http` |
| `bot.adapter.token` | 空 | NapCat 侧设了 token 时填（敏感） |
| `bot.adapter.ws.address` | `ws://localhost:4455` | NapCat WebSocket Server 地址 |
| `bot.adapter.ws.worker_count` | `0` | 处理线程数，0 自动 |
| `bot.adapter.ws.worker_queue_size` | `1024` | 消息队列长度，超出丢弃 |
| `bot.adapter.http.listen_port` | `6679` | HTTP 模式下 Bot 接收事件上报的本地端口 |
| `bot.adapter.http.target_url` | `http://localhost:6680` | HTTP 模式下 NapCat 的 HTTP Server 地址 |
| `bot.adapter.request_timeout_sec` | `10` | 等 NapCat 回包时长 |
| `bot.adapter.upload_timeout_sec` | `120` | 发文件/视频/语音等先上传内容的等待时长，大文件调大 |

### QQ（Luckylilia）—— `bot.luckylilia.*`

键名与 NapCat 同构，只是换命名空间：`mode`（默认 ws）、`token`、`ws.address`（`ws://localhost:3001`）、`ws.worker_count`、`ws.worker_queue_size`、`http.listen_port`（6689）、`http.target_url`（`http://localhost:6690`）、`request_timeout_sec`（10）、`upload_timeout_sec`（120）。可与 NapCat 同时启用（两个 QQ 账号并存）。

### QQ 官方 —— `bot.qqofficial.*`

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `bot.qqofficial.app_id` | 空 | QQ 开放平台「开发 → 开发设置」的 AppID |
| `bot.qqofficial.app_secret` | 空 | 机器人 AppSecret（敏感，换取 access_token） |
| `bot.qqofficial.sandbox` | `false` | 沙箱环境（未上架只能连沙箱） |
| `bot.qqofficial.api_base` | `https://api.sgroup.qq.com` | OpenAPI 地址 |
| `bot.qqofficial.auto_quote` | `false` | 回复自动引用触发消息（官方群不能 @ 成员） |
| `bot.qqofficial.markdown` | `false` | 用 Markdown 消息发送，失败降级纯文本 |

### 飞书 —— `bot.feishu.*`

`app_id`、`app_secret`（敏感）、`mode`（`ws` 默认 / `webhook`）、`webhook.listen`（`127.0.0.1:7777`）、`webhook.path`（`/webhook/event`）、`webhook.verification_token`、`webhook.encrypt_key`（敏感）。ws 模式不用填 webhook 三项。

### Telegram —— `bot.telegram.*`

`token`（敏感，@BotFather 获取）、`api_base`（`https://api.telegram.org`，国内可填反代）、`proxy`（`http://host:port` 或 `socks5://host:port`）、`polling.timeout`（`30` 长轮询秒数）、`parse_mode`（`off` / `html` 默认 / `markdown` / `markdownv2`；html 最稳，AI 的 Markdown 会转成 Telegram HTML）。

### Discord —— `bot.discord.*`

`token`（敏感，需在 Developer Portal 打开 **Message Content Intent**，否则网关拒绝连接）、`proxy`、`member_events`（`false`，需同步开 Server Members Intent）。

### 微信 —— `bot.weixin.*`

`token`（敏感，扫码后自动保存在状态目录，一般无需填）、`api_base`、`cdn_base`、`state_dir`（`./data/weixin`）、`bot_type`（`3`）。微信不需要填连接配置，启用后首次启动在**控制台**扫码登录（面板也有二维码登录入口）。

## 五、plugin.ai_chat_bot.* —— AI 对话

### 模型

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `base_url` | `https://api.deepseek.com` | API 地址；anthropic 格式填 `https://api.anthropic.com` |
| `api_key` | 空 | 敏感。缺 base_url / model / api_key 任一，AI 功能直接降载不可用 |
| `model` | `deepseek-chat` | 模型名 |
| `api_format` | `chat_completions` | `chat_completions`（OpenAI 兼容）/ `responses`（OpenAI Responses）/ `anthropic`（Messages） |
| `multimodal` | `false` | 主模型是否支持图片输入 |
| `rate_limit` | `4` | 同时执行的 AI 对话回合上限，超出直接拒绝并清空该会话排队；后台子代理使用独立并发池，不占该名额 |
| `max_context_tokens` | `128000` | 上下文 Token 上限，超过 80% 触发历史压缩 |
| `max_iterations` | `0` | 单次回复中工具调用循环的轮数上限；`0` 表示不限制（由重复调用/无进展看门狗兜底）。旧库已有值（如 100）继续生效，可改为 0 关闭 |
| `headers` | 空 | 附加到全部 LLM 请求的自定义头，每行 `Name: Value`；值支持会话占位符 `${session}`（每会话唯一，新建会话后换新）/ `${session_id}` / `${session_type}` |
| `prompt` | 内置默认提示词 | 系统提示词（人设/行为规则）。清空会回退默认 |
| `thinking.enable` | `false` | 启用深度思考 |
| `thinking.mode` | `auto` | `none` / `low` / `medium` / `high` / `auto` |
| `prompt_cache.enable` | `true` | anthropic 格式下打 cache_control 断点（其他格式由上游自动前缀缓存） |
| `prompt_cache.ttl` | `5m` | `5m` / `1h`，仅 anthropic |
| `retry.max_attempts` | `3` | 应用层重试次数（429/5xx/网络错误，指数退避） |
| `retry.base_delay_sec` | `2` | 退避基准秒数 |
| `fallback.base_url` / `api_key` / `model` / `api_format` | 空 | 备用模型；`model` 留空表示不启用，主模型重试耗尽后切一次 |
| `compressor.base_url` / `api_key` / `model` / `api_format` | 空 | 上下文压缩专用模型，留空跟随主模型 |

### 工具开关（高危项默认全关）

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `skills_dir` | `./skills` | 外置技能目录 |
| `skills` | 空 | 技能白名单，空=全部加载；只作用于外置技能，内置技能始终加载 |
| `skills_disabled` | 空 | 由面板「技能管理」维护的禁用列表 |
| `bash.enable` | `false` | 宿主机执行 shell（高危） |
| `bash.shell` | 空 | 留空用系统默认（Linux/macOS `sh`，Windows `cmd`） |
| `bash.env` | 空 | `KEY=VALUE`，每行一个 |
| `bash.whitelist` | 空 | 命令白名单正则，非空时只放行匹配项 |
| `bash.blacklist` | `config(\.dev)?\.(yaml\|yml\|json)`、`^mkfs`、`^shutdown`、`^reboot` | 命令黑名单正则 |
| `bash.working_dir` | 空 | 起始目录 |
| `bash.persist_cwd` | `false` | 命令内 `cd` 是否延续到后续调用（该目录所有会话共享） |
| `bash.timeout_sec` | `300` | 单条命令超时，0 用默认 |
| `file.enable` | `false` | `file` 读宿主机文件并发送 |
| `local_image.enable` | `false` | `local_image` 查看宿主机本地图片 |
| `file_tools.enable` | `false` | `read_file` / `write_file` / `edit_file` / `glob` / `grep` 文件工具组 |
| `file_tools.root` | 空 | 非空时把文件工具限制在该目录内 |
| `computer_use.enable` | `false` | 截图 + 鼠标键盘控制（仅 Windows 宿主机） |
| `computer_use.max_width` | `1280` | 截图最大宽度，0 不缩放 |
| `config_tool.enable` | `false` | `config_get` / `config_set` / `config_file_get` / `config_file_set` |
| `skill_tool.enable` | `false` | `skill_list` / `skill_install` / `skill_remove` |
| `mcp_tool.enable` | `false` | `mcp_list` / `mcp_add` / `mcp_remove` / `mcp_reconnect` |
| `todo.enable` | `true` | `todo_write` 任务清单 |
| `mcp.lazy_load` | `true` | MCP 工具懒加载（先 discover 再 load）；关掉则启动全量注册，工具列表恒定更利于上游缓存 |

### 工具结果长度治理（tool_result）

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `tool_result.enable` | `true` | 超长工具结果落盘，AI 用 `read_tool_output` 读回；关闭后超长结果头尾截断 |
| `tool_result.max_inline_runes` | `16000` | 内联字符上限（rune），超过则落盘/截断 |
| `tool_result.preview_runes` | `4000` | 落盘后回填的预览长度（头 3/4 + 尾 1/4） |
| `tool_result.dir` | 空 | 落盘目录，空使用 `./data/tool-outputs` |
| `tool_result.max_file_mb` | `16` | 单个落盘文件体积上限 |
| `tool_result.retention_hours` | `24` | 落盘文件保留时长，过期自动清理 |
| `tool_result.max_total_mb` | `512` | 落盘目录总容量上限，超出优先删除最旧文件 |

### 回合与请求保护（turn / llm）

框架的 `bot.msg_event_timeout_sec` 不再约束 AI 的 IM 回合（消息入口入队后立即返回，后台运行器执行）。回合自身三层保护：

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `turn.soft_timeout_sec` | `600` | 软截止：到时向模型注入一次「收尾或转后台」提醒，**不取消**；0 关闭 |
| `turn.hard_timeout_sec` | `14400` | 硬截止（默认 4 小时）：最终兜底，超时取消回合并提示原因；0 表示不限制 |
| `turn.progress_reminder_after` | `6` | 工具轮数达到该值时提醒模型考虑委派子代理；0 关闭 |
| `turn.repeat_tool_warn` | `3,5,8` | 相同工具+参数连续重复达到这些次数时各提醒一次；留空关闭 |
| `turn.no_progress_limit` | `12` | 连续相同工具调用达到该次数时终止回合并回报原因；0 只提醒 |
| `llm.request_timeout_sec` | `600` | 单次 LLM 请求（含流式）总超时，按可重试错误处理；0 不限制 |
| `llm.stream_idle_timeout_sec` | `120` | 流式响应空闲超时（无新内容），中断并重试；0 不限制 |

### 子代理（subagent）

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `subagent.enable` | `true` | 启用子代理；关闭后不注册 `subagent_run/list/send/cancel`、`task_handoff` 与 `workflow_run` |
| `subagent.default_timeout_sec` | `0` | 子代理单次执行默认超时；`0`（默认）表示不限制，不再跟随 `bot.msg_event_timeout_sec` |
| `subagent.max_timeout_sec` | `0` | 模型单次 `timeout_sec` 的业务上限；0 不限 |
| `subagent.max_concurrent_per_session` | `4` | 每会话同时运行的后台子代理上限，超出直接拒绝 |
| `subagent.max_concurrent_global` | `8` | 全局后台子代理并发上限，与对话回合的 `rate_limit` 分开计算 |
| `subagent.result_inline_runes` | `8000` | 异步子代理结果内联上限；超长结果落盘为预览 + handle，用 `read_tool_output` 读回；0 不限 |

子代理是异步的：`subagent_run` 立即返回任务 ID，完成后结果自动回到会话。可传 `name`（1~32 位、小写字母开头，只含小写字母/数字/连字符，会话内运行中唯一）；`subagent_send(id 或名字, message)` 给运行中的子代理追加指令（下一个模型请求前以 `[追加指令]` 注入），`subagent_cancel` 与 `/stop <ID 或名字>` 支持 ID 或名字。完成通知首行为 `【子代理执行完成】状态=completed`（失败/超时为 `状态=failed` / `状态=timeout`），附任务 ID 与名称。深度固定为 1（子代理不能再委派），没有对应配置项。

### 长任务目标（goal）

跨回合自动续跑：AI 用 `goal_create` 登记目标后，每个回合正常结束且会话空闲时自动开启下一轮推进，直到完成、阻塞或达到轮数上限。目标按会话唯一，存 SQL 表 `ania_agent_goal`（需要 SQL 持久化；建表失败目标功能自动关闭）。

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `goal.enable` | `true` | 启用目标续跑；关闭后不注册 `goal_create/update/status`，也不会自动续跑 |
| `goal.max_rounds` | `64` | 单个目标默认最多自动续跑的轮数（AI 建目标可覆盖，硬上限 500）；达到后自动 `blocked`（`round-limit`）并通知 |

- 工具：`goal_create(objective, max_rounds?)` / `goal_update(action=complete|blocked|pause|resume|clear, reason?, evidence?)` / `goal_status`。
- 用户命令：`/goal`、`/goal pause`、`/goal resume`、`/goal clear`（群聊需 @）。`/stop` 会暂停目标。
- **进程重启后目标仍 active 但不会自动续跑**（续跑武装状态不落库），需用户 `/goal resume`；`blocked` 只要 AI 说明原因即可，没有「连续多轮」门槛。
- 计划模式阻断 `goal_create`/`goal_update`，`goal_status` 放行。
- 每轮续跑仍是普通回合（不单独登记任务），面板「后台任务」页下方可看进行中的目标，`/goal` 看轮次与状态。

### 工作流（workflow）与团队任务板（team_task）

- **工作流** `workflow_run(goal?, steps=[{id,task,depends_on}], concurrency?)`：1~8 个步骤、ID 唯一、依赖无环；并发 1~4（默认 2）；后台执行（每步一个子代理，按依赖调度，失败后继 `skipped`），全部结束后汇总结果回灌会话；任务类型 `workflow`，`/stop` 可取消；仅 `subagent.enable=true` 时注册；计划模式阻断。无独立配置项。
- **团队任务板** `team_task_create/list/claim/complete`：会话级持久任务清单，`blocked_by` 依赖未完成不能认领，`write_scopes` 仅建议不做锁；每会话最多 20 个未完成任务；复用任务表（类型 `team_task`），需要 SQL 持久化；计划模式阻断 create/claim/complete、放行 list。无独立配置项。
- 两者都可以在面板「后台任务」页或 `/tasks` 里观察（面板可导出/清除日志）。

### 任务记录（task）

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `task.retention_days` | `7` | 已结束任务记录保留天数，每 6 小时清理一次；0 不清理 |

任务记录存于 SQL 表 `ania_agent_task`（需要 SQL 持久化，建表失败自动降级为不持久化），记子代理/交接/工作流/团队任务/团队运行/团队成员（`subagent`/`handoff`/`workflow`/`team_task`/`team_run`/`team_member`）；普通回合不再写任务表（完整过程看 Query 日志），旧库可能残留 `turn` 行。进程重启时遗留任务标记 `interrupted`，用户用 `/resume <任务ID>` 恢复；`/tasks` 查看本会话运行中子代理、最近后台任务与最近回合，`/stop` 取消当前回合并清空排队，`/stop <任务ID>` 取消指定后台子代理。面板「后台任务」页可按筛选条件导出 JSON 或清除已结束记录（运行中/排队中的任务保留），任务指令、结果、错误与工具步骤完整保存、不截断。`#新对话` 会先停止本会话运行中的子代理/工作流与团队运行、暂停长任务目标（`/goal resume` 可恢复），再清空上下文。


### 备用识图（主模型不支持多模态时把图片转文字）

`ocr.enable`（`false`）、`ocr.base_url`（`https://api.siliconflow.cn/v1`）、`ocr.api_key`（敏感）、`ocr.model`（`Qwen/Qwen3-VL-8B-Instruct`）、`ocr.max_token`（`600`）、`ocr.temperature` / `top_p` / `top_k`（留空不传）、`ocr.prompt`（识图提示词）。

### 定时任务 / 主动互动

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `clock.enable` | `true` | AI 定时任务总开关 |
| `clock.default_timeout_sec` | `120` | 单次任务超时；`0` 表示不限制 |
| `proactive.enable` | `false` | 主动互动总开关（还要按会话启用） |
| `proactive.activity` | `50` | 活跃度滑条 0–100，换算冷场阈值/最小间隔/每日上限/冷却；50 = 历史默认 |
| `proactive.scopes` | 空 | 启用主动互动的会话，每行 `g:<群ID>` 或 `f:<用户ID>`（ID 带平台前缀，如 `g:qq:123456`）；`/proactive on` 会直接改写这里 |
| `proactive.quiet_hours` | `23:00-08:00` | 免打扰时段（可跨午夜），留空不限 |
| `proactive.timeout_sec` | `120` | 单次生成超时 |
| `proactive.max_context_messages` | `30` | 每个会话保留的现场消息条数 |

### 记忆 / 知识库

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `memory.enable` | `true` | 长期记忆总开关（需要 SQL 持久化） |
| `memory.max_entries` | `200` | 单会话记忆条数上限 |
| `memory.auto_inject` | `false` | 每轮按用户消息检索相关记忆并注入（尾部注入，不动 system） |
| `memory.inject_max` | `3` | 每轮注入条数上限，0 不限 |
| `kb.enable` | `true` | 知识库总开关（需要 SQL 持久化） |
| `kb.max_docs` | `500` | 单 scope（全局/群/私聊）文档上限 |
| `kb.auto_inject` | `true` | 每次对话前检索相关文档注入上下文 |
| `kb.embedding.enable` | `false` | 向量检索（语义+关键词混合打分），启动时回填向量 |
| `kb.embedding.base_url` | 空 | 留空用主模型 Base URL；主模型无 embedding 接口时填 Jina 等 |
| `kb.embedding.api_key` | 空 | 敏感，留空用主模型 Key |
| `kb.embedding.model` | `jina-embeddings-v3` | 向量模型名 |

### 回复 / 配额 / 会话 / 钩子 / 审批

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `stream.enable` | `true` | 平台支持时逐字流式回复（飞书卡片/Telegram/Discord 编辑消息），失败退化一次性发送 |
| `quota.enable` | `false` | 每日 Token 配额 |
| `quota.daily_tokens` | `0` | 每会话每日上限，0 不限 |
| `quota.global_daily_tokens` | `0` | 全局每日上限，0 不限 |
| `session.max_idle_minutes` | `120` | 闲置会话内存回收（历史在库里，下次自动恢复）；0 关闭 |
| `session.max_sessions` | `128` | 内存驻留会话上限，超出淘汰最久未活跃；0 不限 |
| `session.no_mention_clear` | `30` | 群聊连续 30 条未 @ 自动清空该群历史，下次 @ 开新对话；0 关闭 |
| `hooks.enable` | `false` | AI 钩子（按 `files.hooks_json` 在会话事件上跑 shell） |
| `hooks.timeout_sec` | `10` | 单个钩子默认超时（上限 60） |
| `approval.enable` | `false` | 工具审批：危险工具执行前要请求者/管理员回「允许」；同时是 bash 未列名命令的审批通道 |
| `approval.tools` | `file` | 需审批的工具名，逗号分隔 |
| `approval.timeout_sec` | `120` | 审批超时（10–240），超时视为拒绝 |

## 六、其他内置插件

### 请求拦截 `plugin.interceptor.*`

`enable`（`false`）、`mode`（`blacklist` 默认 / `whitelist`）、`platforms`（默认六个平台全选，未勾选的平台直接拦截不触发 AI）、`groups`（群 ID 名单）、`friends`（用户 ID 名单；黑名单下也作用于群内发送者，白名单下只作用于私聊）、`group_users`（`群ID:用户ID`，仅在该群屏蔽该成员，优先级最高）。

### 复读机 `plugin.repeat.*`

`enable`（`true`，也可 `/close repeat`、`/enable repeat` 开关并同步写回）、`trigger_count`（`3`，同一条消息连续出现多少条后跟读）。

### 每日新闻 `plugin.dailynews.*`

`enable`（`true`）、`api`（`https://60s.viki.moe/v2/60s?encoding=image-proxy`）、`cron`（`0 18 * * *`）、`groups`（播报群 ID 列表，每行一个；默认值是示例，必须改成真实群号）。

## 七、扩展配置（四份 JSON）

面板「扩展配置」页管理，对应配置键 `files.mcp_json` / `files.prompt_json` / `files.hooks_json` / `files.commands_json`。AI 侧对应 `config_file_get` / `config_file_set`。

**`mcp`（`files.mcp_json`）—— MCP 服务器列表，改完重启生效**

```json
{"servers": [
  {"name": "名字", "transport": "stdio|streamable|sse",
   "command": "启动命令（stdio）", "args": ["参数"], "env": {"K": "V"},
   "endpoint": "HTTP 地址（streamable/sse）", "headers": {"K": "V"},
   "timeout": 30, "description": "说明"}
]}
```

**`prompt`（`files.prompt_json`）—— 按群/私聊覆盖系统提示词，保存后数秒生效**

```json
{"groups": {"qq:123456": "群专属提示词"}, "friends": {"fs:ou_xxx": "私聊专属提示词"}}
```

ID 必须带平台前缀（`qq:` / `fs:` / `tg:` / `dc:` / `wx:` / `qo:` / `lil:`）。

**`hooks`（`files.hooks_json`）—— AI 钩子，保存后数秒生效**

```json
{"hooks": {"PreToolUse": [
  {"matcher": "工具名正则（可为空）", "command": "shell 命令", "timeout_sec": 10}
]}}
```

事件名：`SessionStart` / `UserPromptSubmit` / `PreToolUse` / `PostToolUse` / `Stop` / `SubagentStop` / `PreCompact`。命令从 stdin 收 JSON 载荷；退出码 `0` 通过（stdout 注入上下文）、`2` 阻断（stderr 作为原因）、其他仅记日志。

**`commands`（`files.commands_json`）—— 自定义斜杠命令，保存后数秒生效**

```json
{"commands": {"命令名": "提示词模板，$args 为用户参数占位符"}}
```

命令名字母开头、最长 32 字符，不得与内置命令撞名；命中后消息被改写成展开后的纯文本再走正常对话。面板/会话里也可以 `/cmd add <名> <模板>` 维护。

## 八、数据与备份

| 内容 | 位置 |
| --- | --- |
| 配置、对话历史、记忆、知识库、定时任务、各类日志 | 持久化存储（默认 `./data/aniabot.db`，MySQL 时在数据库里） |
| 微信登录凭据与长轮询游标 | `./data/weixin/` |
| 市场安装的第三方插件源码/二进制 | `./data/plugins/`、`./data/marketplace/` |
| 外置技能 | `./skills`（可改 `skills_dir`） |
| 内置技能（只读来源，物化副本） | `<skills_dir>/.builtin/`，每次加载由二进制重写，改这里无效 |

数据表（SQL 后端）：`ania_kv`（配置与 KV）、`ania_chat_session` / `ania_chat_message`（对话历史）、`ania_memory`、`ania_kb_doc`、`ania_intention`（主动互动意图）、`ania_query_log`（含普通对话的 `stop_reason` 与历史 `task_id`）、`ania_task_log`、`ania_op_log`、`ania_agent_task`（后台任务注册表：子代理/交接/工作流/团队任务/团队运行/团队成员，历史 `turn` 行仅兼容旧库）、`ania_agent_goal`（长任务目标）。

备份：停掉 Bot 后复制整个 `data/` 目录最稳妥；只想备份配置可用面板「配置管理」页的导出功能，或用「配置预设」把当前配置存成快照后一键回切。恢复时注意 Bot 必须停止，否则 SQLite 可能拿到不一致的副本。
