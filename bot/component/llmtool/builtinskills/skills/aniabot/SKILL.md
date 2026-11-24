---
name: aniabot
description: AniaBot 自身说明书（架构、配置项与生效时机、Web 面板、聊天指令、插件/技能/MCP 机制、常见故障排查）。用户问某项功能怎么配置、在哪里设置、为什么没生效或没反应、怎么装插件、要不要重启时先读本技能
---

# AniaBot 自身说明书

AniaBot 是一个用 Go 写的插件驱动型多平台机器人框架，**你自己就跑在它上面**。本技能说明你的组成、配置存在哪、怎么改、改完什么时候生效、出问题先看哪里——用来指导用户完成配置与操作。

## 一、铁律（先读再动手）

1. **没有配置文件。** 全部配置存放在数据库（持久化存储的 `ania_kv` 表）里，正常的修改入口是 Web 控制面板的「配置管理」页，或你的 `config_set` / `config_file_set` 工具。改完**重启 Bot 才生效**（少数热生效项见第四节）。
2. **重启只能由管理员触发**：管理员在会话里发 `/reboot`，或在面板点「重启」。你**不要**用 bash 重启进程、杀进程、或另起一份——会抢同一个数据库与面板端口。
3. **敏感字段对你是掩码**（`********`）。API Key、Token、密码这类值你看不到，也不该向用户索要；需要核对时请用户自己在面板里看。
4. **你不知道说话的人是不是管理员**：消息里只有 `[nickname:… id:…]`。凡涉及管理员权限（重启、审批、改配置、看全部定时任务），先说明「需要管理员」，不要假设对方有权限。
5. **不要改数据库文件、不要删 `data/`**：`data/aniabot.db`（默认 SQLite）里同时装着配置、对话历史、长期记忆、知识库、定时任务和各类日志。
6. **有副作用的操作先确认**：改配置、删记忆/知识库、卸载插件、重启，先讲清影响并得到用户明确同意。

## 二、你的组成

- **平台适配器**（可多平台同时在线）：QQ（NapCat / Luckylilia，OneBot v11）、QQ 官方、飞书、Telegram、Discord、微信（扫码登录）。判断当前平台看注入给你的工具名和 ID 前缀：
  | 线索 | 平台 |
  | --- | --- |
  | `qq_*` 工具、数字群号、`lil:` 前缀 | QQ（NapCat / Luckylilia） |
  | `qo:` 前缀 | QQ 官方 |
  | `fs:` 前缀 | 飞书 |
  | `tg_*` 工具 / `tg:` 前缀 | Telegram |
  | `dc:` 前缀 | Discord |
  | `wx:` 前缀 | 微信 |
- **插件**：内置「系统 / 日志 / 请求拦截 / 复读机 / 每日新闻 / AI 对话」六个；第三方插件从面板「插件市场」安装（自动下载源码 → 编译 → 重启）。
- **AI 对话能力**：三种 LLM API 格式（`chat_completions` / `responses` / `anthropic`）、工具调用、MCP、技能（skill）、长期记忆、知识库、定时任务、主动互动、子代理（异步委派 + 后台交接）、Agent 团队、工具审批、计划模式、上下文压缩、任务编排与配额限制。
- **数据落点**：SQLite `./data/aniabot.db`（可用环境变量换 MySQL）；微信登录凭据在 `./data/weixin/`；市场插件在 `./data/plugins/`；技能目录默认 `./skills`（配置项 `plugin.ai_chat_bot.skills_dir`）。

## 三、要查/改配置，按这个顺序走

1. **`config_get`**（本次会话注册了才可用）：不带参数列出全部已注册配置项（`键 = 当前值 # 中文标签`），带 `key` 查单项。这是最权威的键清单，**不要凭记忆报键名**。
2. **`config_set`**：只能改注册表里的键；写入数据库，**恒需管理员审批**（系统私聊通知管理员回「允许」/「拒绝」），**重启后生效**。改完必须提醒：让管理员发 `/reboot`。
3. **`config_file_get` / `config_file_set`**：改四份扩展配置 JSON——`mcp`（MCP 服务器）、`prompt`（群/私聊专属提示词）、`hooks`（AI 钩子）、`commands`（自定义斜杠命令）。只校验 JSON 语法，语义写错会让对应功能加载失败，所以先 `config_file_get` 看现状再整份覆盖。
4. **Web 控制面板**：`http://<bot.admin_panel.listen>`，默认 `http://127.0.0.1:7700`（面板页清单见 `reference/panel.md`）。面板保存后同样要重启。
5. **没有配置工具时**：说明这些工具需要在面板「AI 对话 · 工具 → 启用配置管理工具」打开并重启；在打开之前**只给面板路径、键名和填写要点**，绝不要假装读过配置或编造当前值。

### 「用户想改 X」对照表

| 用户需求 | 该去哪儿 |
| --- | --- |
| 管理员是谁、谁有权限 | 配置管理 → Bot 基础 → `bot.admin_ids`（每行一个，QQ 写 `qq:QQ号`） |
| AI 模型 / API Key / API 格式 / 多模态 | 配置管理 → AI 对话 · 模型（`plugin.ai_chat_bot.base_url` / `api_key` / `model` / `api_format`） |
| AI 人设、说话风格 | 配置管理 → AI 对话 · 模型 → 系统提示词 `plugin.ai_chat_bot.prompt` |
| 只给某个群/某个人换人设 | 扩展配置 → `prompt`（`files.prompt_json`，键是 `qq:群号` / `fs:oc_xxx` 这类带前缀 ID） |
| 让 AI 能执行命令 / 读写文件 / 截图 | AI 对话 · 工具（`bash.enable`、`file_tools.enable`、`computer_use.enable` 等，默认全关） |
| 让 AI 能改配置 / 管理技能 / 管理 MCP | AI 对话 · 工具（`config_tool.enable`、`skill_tool.enable`、`mcp_tool.enable`，默认全关） |
| 技能的白名单、目录、启用禁用 | AI 对话 · 工具（`skills_dir` / `skills`），启停用面板「技能管理」页 |
| 接一个 MCP 服务器 | 扩展配置 → `mcp`，或（已开启时）你的 `mcp_add` 工具；MCP 改动**重启后生效** |
| 定时提醒 / 周期任务 | 会话里 `/clock`，或你已注册的 `clock_*` 工具；面板「AI 定时任务」页可看执行记录 |
| 让 AI 主动开口 | 配置管理 → AI 对话 · 主动互动（总开关 + 活跃度滑条），再按会话 `/proactive on` |
| 记忆 / 知识库内容 | 面板「记忆管理」「知识库」页；会话里用 memory_* / kb_* 工具 |
| Token 用量、每日配额 | AI 对话 · 配额（`quota.enable` / `daily_tokens` / `global_daily_tokens`）；面板「配额管理」「Token 统计」 |
| 谁能用 AI（黑白名单） | 请求拦截插件（`plugin.interceptor.*`，按群/按用户/按群内成员） |
| 复读机开关 | 复读机插件（`plugin.repeat.*`），或群内 `/close repeat`、`/enable repeat` |
| 每日新闻推送 | 每日新闻插件（`plugin.dailynews.*`：cron、群列表），或 `/news` |
| 接 QQ / 飞书 / Telegram / Discord / 微信 | 配置管理 → 平台适配器（先勾 `bot.platform.<名字>.enable`）→ 对应适配器分组填连接信息；**重启生效** |
| 面板端口 / 局域网访问 / 关掉面板 | Web 面板 → `bot.admin_panel.listen`（`0.0.0.0:7700` 可局域网访问） |
| AI 回复跑太久、复杂任务被打断 | AI 回合已不再受 `bot.msg_event_timeout_sec` 限制：软截止提醒 / 硬截止 / 无进展看门狗在 `plugin.ai_chat_bot.turn.*`（硬截止默认 4 小时），单次请求超时在 `plugin.ai_chat_bot.llm.*`；后台任务见 `/tasks` |
| 定时任务推送失败、发送超时 | 对应适配器的请求/上传超时（如 NapCat `bot.adapter.request_timeout_sec`） |
| 装第三方插件 | 面板「插件市场」（安装/升级/卸载都会自动编译重启） |
| 升级 AniaBot 本体 | 面板「自动更新」（需先在配置里填 `bot.update.source_dir` 源码目录） |
| 忘掉面板密码 | 停掉 Bot 后执行 `./AniaBot -set-password 新密码`，再正常启动 |
| 看日志 / 排查故障 | 面板「控制台日志」「操作日志」「Query 日志」「消息日志」+ `reference/troubleshooting.md` |

## 四、生效时机（最容易答错的地方）

| 改动 | 何时生效 |
| --- | --- |
| 绝大多数配置键（模型、平台连接、工具开关、超时、面板地址……） | **重启后**（管理员 `/reboot` 或面板重启） |
| `config_set` 写入的任何键 | **重启后** |
| 扩展配置 `prompt` / `hooks` / `commands` | 保存后**数秒内自动生效**（无需重启） |
| 扩展配置 `mcp` | **重启后** |
| 面板「技能管理」里的启用/禁用 | 立即生效（写入 `plugin.ai_chat_bot.skills_disabled`） |
| `/proactive on` / `/proactive off` | 立即生效，并写回 `plugin.ai_chat_bot.proactive.scopes`；面板手改该键后可用 `/proactive reload` 免重启重读 |
| `plugin.ai_chat_bot.skills`（白名单） | **重启后**（面板上传/删除技能会自动热重载，但白名单本身在启动时读） |
| `bot.balance.*`（面板概览的 API 余额） | 修改后即时生效 |
| `/plan on`、`/plan off`、`#新对话`、`/stop`、`/tasks`、`/resume`、`/status` | 会话内立即生效 |

## 五、用户能用的指令（群聊里一条都要先 @ 机器人）

| 指令 | 说明 | 权限 |
| --- | --- | --- |
| `/help` | 列出已加载插件与各自的用法 | 所有人 |
| `/stop` | 立刻停止当前会话正在进行的 AI 回复，并清空该会话排队消息；`/stop 任务ID` 只取消指定的后台子代理 | 所有人 |
| `/tasks` | 查看当前会话运行中的子代理与最近的后台任务（状态、耗时、类型、停止原因、token） | 所有人 |
| `/resume 任务ID` | 恢复进程重启前被中断（interrupted）的子代理/交接任务：把任务目标重新交给主 AI 带会话历史继续；仅限本会话 | 所有人 |
| `/status` | 查看当前会话的 AI 状态：响应中 / 等待子代理 / 排队中 / 空闲，含已持续时长、最近阶段与排队消息数 | 所有人 |
| `/plan on` 或 `/plan off`、`/plan` | 计划模式开关：只分析规划，禁止改文件、跑命令、改配置等副作用操作 | 所有人 |
| `/clock` | 定时任务：`list` / `add [--once] <cron> \| <标题> \| <内容>` / `del <id>` / `on` 或 `off <id>` / `info <id>` / `timeout <id> <秒>` / `log [n]` / `help`；`list all`、`run <id>` 仅管理员 | 所有人（部分仅管理员） |
| `/proactive` | 主动互动：`on` / `off` / `reload` / `now [想说的话]` / `list` / `log [n]` / `help` | 查看所有人，修改仅管理员 |
| `/cmd` | 自定义斜杠命令：`list` / `add <名> <模板>` / `del <名>` | 管理仅管理员 |
| `/news` | 立即取一次每日新闻；`/news force` 强制执行播报任务 | 所有人（force 仅管理员） |
| `/close repeat`、`/enable repeat` | 关/开复读机（会写回配置） | 仅管理员 |
| `/reboot` | 重启 Bot 进程（配置生效的标准方式） | 仅管理员 |
| `/exit` | 退出 Bot 进程（私聊可用，之后需人工重新启动） | 仅管理员 |
| `#新对话` | 消息里带这个标签会清空本会话历史，开一段全新对话 | 所有人 |

管理员身份来自 `bot.admin_ids`；其他平台 ID 要带前缀（`fs:` / `tg:` / `dc:` / `wx:` / `qo:` / `lil:`）。

## 六、排障速查

详细排查步骤见 `reference/troubleshooting.md`，这里是第一反应：

| 现象 | 先查什么 |
| --- | --- |
| AI 完全不回复 | 面板「控制台日志」搜 `AI 对话未配置`（缺 base_url / model / api_key → 在 AI 对话 · 模型 里补齐后重启）；群聊是否 @ 了机器人 |
| 启动后平台一直没连上 | 是否卡在首次设置向导（`meta.setup_pending` 未完成时**不连接任何平台**）；平台 `enable` 与连接参数（ws 地址 / token）是否正确 |
| 改了配置没反应 | 99% 是没重启；只有第四节列的热生效项例外 |
| 成员/群被拦了、AI 不回某人 | 查请求拦截插件名单（`plugin.interceptor.*`，含按平台的「可用平台」勾选） |
| 回复很慢或中途停下 | 长任务已改为后台执行且默认不限轮数：面板「Query 日志」看卡在哪一步；`turn.*` 看软/硬截止与看门狗，`llm.*` 看单次请求/流空闲超时；用 `/tasks` 看后台任务与 `/resume` 恢复被中断任务 |
| 429 / 上下文超限 / 余额不足 | 面板「Query 日志」「Token 统计」看消耗；上下文压缩模型（`compressor.*`）是否可用；`max_context_tokens` 是否偏小 |
| 图片看不懂 | `plugin.ai_chat_bot.multimodal`（主模型是否支持图片）+ 备用识图 `ocr.*` |
| MCP 工具没出现 | `files.mcp_json` 格式/命令是否正确（MCP 改动要重启）；懒加载模式下先 `mcp_discover_*` 再 `mcp_load_*` |
| 技能没起作用 | 白名单 `plugin.ai_chat_bot.skills` 是否漏了它、是否被禁用；外置技能改完文件要 `skill_reload` |
| 面板打不开 | 进程是否在跑、`bot.admin_panel.listen` 端口/防火墙；误关面板可用环境变量 `ANIA_BOT_ADMIN_PANEL_ENABLE=true` 临时拉起 |
| 面板密码忘了 | `./AniaBot -set-password 新密码`（先停 Bot） |

## 七、参考文档

用 `skill_read` 的 `file_name` 参数按需读取：

- `reference/config.md` —— 全部配置键分组清单（默认值、生效时机）、四份扩展配置 JSON 的格式、环境变量覆盖、存储与备份。
- `reference/panel.md` —— Web 面板每个页面能做什么、登录与密码、首次设置向导、重启/更新/插件市场/试聊室等操作。
- `reference/troubleshooting.md` —— 按现象展开的排查路径与对应的日志/配置位置。

还有两条更深的路径（按当前会话实际注册的工具判断能不能用）：

- 宿主机上有 AniaBot 源码（`bot.update.source_dir` 指向的目录）且 `read_file` / `grep` / `bash` 可用时，可以直接读源码确认真实行为，不要靠猜。
- 需要查文档时，官方文档站是 `https://aniabot-project.github.io/AniaBot/`（配置详解在 `/guide/configuration`，面板在 `/guide/web-panel`，AI 工具在 `/internals/agent-tools`），配合内置 `web` 技能抓取。

## 八、回答这类问题的风格

- 一次给出**可执行的下一步**：哪个页面、哪个配置键、填什么、要不要重启，别只丢一堆键名。
- 用户只是问「这是什么/能不能做」时，先回答再问要不要改，不要上来就改配置。
- 不确定当前配置时，明确说「我看不到当前配置」，而不是猜一个值。
- 你是**内置技能**：内容随程序发布，改 `<skills_dir>/.builtin/aniabot/` 下的文件在下次重载/重启时会被还原。想定制请让用户新建一个外置技能（`skills/<名字>/SKILL.md`），或在面板「技能管理」里上传。
