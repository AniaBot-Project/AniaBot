# 配置详解

AniaBot 的全部配置存储在**数据库**中（持久化存储的 `ania_kv` 表），通过内置的 **Web 控制面板**查看与修改，不使用任何 yaml 配置文件。

- 首次启动时自动写入默认配置，并进入[首次设置向导](/guide/web-panel#首次设置向导)。
- 配置在面板中保存后**重启 Bot 生效**。
- 面板操作方式见 [Web 控制面板](/guide/web-panel)。

::: tip 键名约定
配置键为点分路径（大小写不敏感），如 `plugin.ai_chat_bot.model`。在面板的「配置编辑」页按键名查找并修改即可；本文各节列出所有键名、默认值与说明。
:::

## 引导配置（环境变量）

唯一不经过数据库的配置是**持久化存储本身的位置**（配置中心的载体，必须先于配置加载），通过环境变量设置：

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `ANIABOT_STORE_DRIVER` | 持久化驱动：`sqlite` / `mysql` | `sqlite` |
| `ANIABOT_SQLITE_PATH` | SQLite 数据库文件路径 | `./data/aniabot.db` |
| `ANIABOT_MYSQL_DSN` | MySQL 标准 go-sql-driver DSN | 无（驱动为 mysql 时必填） |

其余所有配置（管理员、适配器、缓存、面板、插件）都在数据库中，通过面板编辑。

## 环境变量覆盖（ANIA 前缀）

数据库中的**任意配置键**都可以用环境变量临时覆盖（优先级高于数据库中的值，不写回数据库），适合容器部署或临时调试。命名规则：`ANIA_` + 配置键全大写、点与横线转为下划线：

| 配置键 | 环境变量 |
| --- | --- |
| `bot.admin_panel.listen` | `ANIA_BOT_ADMIN_PANEL_LISTEN` |
| `bot.adapter.ws.address` | `ANIA_BOT_ADAPTER_WS_ADDRESS` |
| `plugin.ai_chat_bot.api_key` | `ANIA_PLUGIN_AI_CHAT_BOT_API_KEY` |

非字符串类型（int / bool / 数组等）按 JSON 解析，如 `ANIA_BOT_ADMIN_ID=qq:123456789`。覆盖生效时启动日志会打印 `环境变量覆盖配置 key=...`。

::: tip 典型用途：恢复被关闭的面板
如果在面板中误将 `bot.admin_panel.enable` 关闭导致面板无法访问，可用 `ANIA_BOT_ADMIN_PANEL_ENABLE=true` 启动临时拉起面板，改回后再正常启动。详见 [Web 控制面板](/guide/web-panel#启用与访问)。
:::

## bot —— 框架配置

面板位置：**配置编辑 → Bot 配置**

### admin_id —— 管理员

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.admin_id` | `qq:123456789` | 管理员 ID。QQ 为 `qq:QQ号`（如 `qq:123456789`），其他平台为带前缀的 ID（如飞书 `fs:ou_xxx`）。拥有最高权限：远程 `/exit` 退出、强制执行定时推送、查看全部定时任务、接收 panic 告警与启动通知等 |
| `bot.msg_event_timeout_sec` | `300` | 单条消息事件（如一次 AI 回复）的最大执行时长（秒），超时强制中止。AI 执行复杂任务（多轮工具调用/子代理）被超时中断时调大 |

### admin_panel —— Web 控制面板

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.admin_panel.enable` | `true` | 是否启用面板 |
| `bot.admin_panel.listen` | `127.0.0.1:7700` | 监听地址；改为 `0.0.0.0:7700` 可局域网访问（面板有密码保护） |

首次启动会在控制台打印**随机初始密码**（仅显示一次），登录后可在面板右上角修改。详见 [Web 控制面板](/guide/web-panel)。

### platform —— 平台适配器开关

多平台并存，各自独立开关（`bot.platform.<name>.enable`）。默认仅启用 QQ，QQ 官方 / 飞书 / Telegram / Discord / 微信默认关闭：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.platform.napcat.enable` | `true` | 是否启用 QQ（NapCat）平台 |
| `bot.platform.luckylilia.enable` | `false` | 是否启用 QQ（Luckylilia/LLBot）平台（需同时配置下方 `bot.luckylilia.*`；与 NapCat 可并存，ID 前缀为 `lil:`） |
| `bot.platform.qqofficial.enable` | `false` | 是否启用 QQ 官方机器人平台（需同时配置下方 `bot.qqofficial.*`） |
| `bot.platform.feishu.enable` | `false` | 是否启用飞书平台（需同时配置下方 `bot.feishu.*`） |
| `bot.platform.telegram.enable` | `false` | 是否启用 Telegram 平台（需同时配置下方 `bot.telegram.*`） |
| `bot.platform.discord.enable` | `false` | 是否启用 Discord 平台（需同时配置下方 `bot.discord.*`） |
| `bot.platform.weixin.enable` | `false` | 是否启用微信平台（需同时配置下方 `bot.weixin.*`；首次连接在控制台扫码登录） |

勾选后**重启生效**。未来新增平台同样在此出现对应开关。

### qqofficial —— QQ 官方适配器

在 [QQ 开放平台](https://q.qq.com/) 注册并创建机器人，在管理端「开发 → 开发设置」拿到 **AppID / AppSecret**，并在「功能配置」的事件订阅中选择 **WebSocket 方式**、勾选群聊（@机器人）与单聊场景，然后在面板勾选启用 QQ 官方并填写。事件经官方 WebSocket 网关推送，**无需公网地址、无需部署协议端**（旧版 Token 鉴权已废弃，AniaBot 使用 Access Token 鉴权并自动刷新）。群聊订阅默认仅在 @机器人 时推送（`GROUP_AT_MESSAGE_CREATE`）；若在后台开启「接收所有消息」，群内每条消息都会推送（`GROUP_MESSAGE_CREATE`），AniaBot 两种模式都支持：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.qqofficial.app_id` | 空 | 机器人 AppID |
| `bot.qqofficial.app_secret` | 空 | 机器人 AppSecret（敏感字段），用于换取 access_token |
| `bot.qqofficial.sandbox` | `false` | 沙箱环境开关；机器人未上架前只能连接沙箱环境联调 |
| `bot.qqofficial.api_base` | `https://api.sgroup.qq.com` | OpenAPI 地址（沙箱开关优先于此配置） |
| `bot.qqofficial.markdown` | `false` | AI 文本回复以 Markdown 消息（msg_type=2）发送，富文本渲染；发送失败自动降级纯文本 |

::: tip QQ 官方能做什么 / 不能做什么
QQ 官方适配器覆盖**群聊 @机器人** 与 **单聊（C2C）** 两大场景：支持文本 / 图片 / 语音 / 视频 / 文件 / 引用回复，好友添加映射公共通知，机器人被拉群/移出/删除等走平台事件；入站消息自动注入 @机器人 的 at 段，群聊 @ 触发 AI 对话开箱即用。**平台限制**：
- 发消息以**被动回复**为主（携带事件的 msg_id，群聊 5 分钟内最多 5 次、单聊 60 分钟内最多 4 次），超限自动降级主动消息（受官方频控与每日配额限制）
- **不能 @ 群成员**（无对应 API），回复语义以「引用消息」表达（无显式 reply 段时自动引用触发消息，客户端显示为引用气泡）
- 无消息历史 / 单条消息查询 / 群资料 API：历史仅覆盖适配器运行期间的内存缓存（AI 会话历史不受影响，由持久化存储承载）
- 无消息编辑 API：不支持流式回复（自动退化一次性发送）
- 媒体（图片/视频/语音/文件）先经 `/files` 上传换取 file_info 再发送，URL 直传与本地字节分片上传都支持
- openid 为 per-AppID 身份：同一用户在群聊（member_openid）与单聊（user_openid）下 ID 不同，且与 NapCat 的 `qq:` QQ ID 完全无关
- 开启「接收所有消息」（全量模式）后，群内非 @ 消息也会像 NapCat 一样流经插件链（词云、计数、消息清理等插件可正常工作），AI 仍然只在被 @ 时响应；机器人自己发送的消息会被自动过滤，防止自我循环；消息正文中残留的 `<@openid>` 提及标记会自动剥离，不会污染 AI 输入
- 频道（guild）场景不在支持范围；合并转发、戳一戳、群签到、rkey 等 NapCat 专属能力 QQ 官方**没有**——依赖它们的插件（如防撤回）在本平台不生效
:::

### feishu —— 飞书适配器

在飞书开放平台创建企业自建应用，在「凭证与基础信息」拿到 App ID / Secret，并在「权限管理」开通 `im:message`、`im:message:send_as_bot`、`im:resource` 等权限，然后在面板勾选启用飞书并填写：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.feishu.app_id` | 空 | 应用 App ID |
| `bot.feishu.app_secret` | 空 | 应用 App Secret（敏感字段） |
| `bot.feishu.mode` | `ws` | 事件订阅方式：`ws`（WebSocket 长连接，推荐，**无需公网地址**）/ `webhook`（需公网 HTTPS 回调地址） |
| `bot.feishu.webhook.listen` | `127.0.0.1:7777` | webhook 模式本地监听地址 |
| `bot.feishu.webhook.path` | `/webhook/event` | webhook 模式回调路径，飞书后台填 `https://<公网地址><此路径>` |
| `bot.feishu.webhook.verification_token` | 空 | webhook 模式在飞书「事件订阅」页配置（ws 模式无需） |
| `bot.feishu.webhook.encrypt_key` | 空 | webhook 模式可选的事件加密密钥（ws 模式无需） |

::: tip 飞书能做什么 / 不能做什么
飞书适配器支持文本 / @提及 / 富文本 / 图片 / 文件 / 回复，撤回 / 表情回应 / 成员进出会映射到对应公共通知。合并转发、戳一戳、群签到、rkey 等 QQ 专属能力飞书**没有**——依赖它们的插件（如防撤回）在飞书不生效。
:::

### telegram —— Telegram 适配器

在 Telegram 中向 [@BotFather](https://t.me/BotFather) 发送 `/newbot` 创建机器人并获取 **Bot Token**，然后在面板勾选启用 Telegram 并填写。Telegram 采用 **Bot API 长轮询**（`getUpdates`）接收事件，**无需公网地址、无需部署协议端**：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.telegram.token` | 空 | Bot Token（敏感字段），形如 `123456:ABC-DEF...` |
| `bot.telegram.api_base` | `https://api.telegram.org` | Bot API 地址；国内部署可填自建 Bot API 网关/反代地址 |
| `bot.telegram.proxy` | 空 | HTTP/SOCKS5 代理（`http://host:port` 或 `socks5://host:port`），留空直连 |
| `bot.telegram.polling.timeout` | `30` | `getUpdates` 长轮询等待秒数（建议 10-50） |

::: tip Telegram 能做什么 / 不能做什么
Telegram 适配器支持文本 / @提及 / 图片 / 文件 / 语音 / 视频 / 回复，成员进出、表情回应、机器人被拉群/移出会映射到对应公共通知与平台事件；群聊中 @机器人 触发 AI 对话。**平台限制**：@ 只能以 `@username` 形式（无按 ID @ 的 API）；仅当机器人是管理员或关闭隐私模式时才能收到其他成员的加入/离开消息与表情回应；Bot API 无消息历史端点，历史消息仅覆盖适配器运行期间的缓存（AI 会话历史不受影响，由持久化存储承载）；消息撤回、合并转发等 QQ 专属能力 Telegram **没有**。
:::

### discord —— Discord 适配器

在 [Discord Developer Portal](https://discord.com/developers/applications) 创建应用，在「Bot」页面获取 **Bot Token**，并**务必开启「Message Content Intent」**（特权意图，否则网关拒绝连接）；邀请机器人进服务器时使用 `bot` scope 并勾选 Send Messages / Read Message History / Add Reactions / Attach Files 等权限。然后在面板勾选启用 Discord 并填写。事件经 Gateway WebSocket 推送，**无需公网地址、无需部署协议端**：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.discord.token` | 空 | Bot Token（敏感字段），重置后旧 Token 立即失效 |
| `bot.discord.proxy` | 空 | HTTP/SOCKS5 代理（`http://host:port` 或 `socks5://host:port`），留空直连；REST 与 WebSocket 网关都走代理 |
| `bot.discord.member_events` | `false` | 接收服务器成员进出事件；需在 Developer Portal 同步开启 Server Members Intent，成员进出以平台事件投递 |

::: tip Discord 能做什么 / 不能做什么
Discord 适配器支持文本 / @提及 / @everyone / 图片 / 文件 / 语音 / 视频 / 引用回复（原生渲染 Markdown），消息删除映射撤回公共通知、表情回应映射群消息表情回应通知；机器人进/出服务器与成员进出走平台事件（`discord.bot_added` / `discord.bot_removed` / `discord.guild_member_add` / `discord.guild_member_remove`）；群聊中 @机器人 触发 AI 对话；历史消息经官方 API 拉取（单次最多 100 条，内存缓存兜底）。**平台限制**：
- Message Content 为特权意图，必须在 Developer Portal 开启，否则网关拒绝连接（close 4014）
- 附件超过约 25 MiB 无法上传（超限附件跳过不发送）；外部 URL 附件由 Bot 下载后重传（Discord 不抓取外链）
- 消息删除事件不携带删除者与原消息作者：作者从运行期缓存反查；删除者经审计日志尽力解析（**需为机器人勾选 View Audit Log 权限**——管理删除会落审计条目，本人自删不落，据「无匹配条目」推断自删；无权限或作者未入缓存时操作者留空）
- 成员进出事件携带服务器 ID 而非频道 ID，因此映射为平台事件而非公共进出通知
- 斜杠命令（Interactions）、合并转发、戳一戳等不在支持范围；QQ 专属能力（防撤回依赖的合并转发等）在本平台不生效
:::

### weixin —— 微信适配器

接入微信 **iLink bot** 通道（HTTP 长轮询收消息，**无需公网地址、无需部署任何协议端**）。与其他平台不同，微信没有静态 Token 可申请：**启用平台并重启后，在 Web 面板「配置」页顶部的「微信扫码登录」卡片点击扫码**（或直接看 Bot 控制台的二维码），用手机微信扫码、按提示输入手机上显示的配对数字并确认即可完成授权，登录凭据自动保存在状态目录（`bot.weixin.state_dir`，建议放在 data 卷，容器重建后不丢登录）。凭据失效（长时间未使用等）时适配器会重新进入扫码流程，在面板或控制台重新扫一次即可。如需**更换 bot 账号**，在面板卡片（或控制台）重新扫码授权即可，新凭据保存后自动热生效（最长约一个轮询周期），无需重启。

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.weixin.token` | 空 | 登录凭据由控制台扫码后自动保存，一般无需填写；手动置入则跳过扫码（优先于状态文件） |
| `bot.weixin.api_base` | `https://ilinkai.weixin.qq.com` | iLink Bot API 地址；登录成功后自动记录服务端下发的专属地址 |
| `bot.weixin.cdn_base` | `https://novac2c.cdn.weixin.qq.com/c2c` | 微信媒体 CDN 地址（图片/文件上传下载） |
| `bot.weixin.state_dir` | `./data/weixin` | 登录凭据与长轮询游标的保存目录 |
| `bot.weixin.bot_type` | `3` | 扫码登录的 ilink bot 类型 |

::: tip 微信能做什么 / 不能做什么
微信适配器支持**一对一私聊**（bot ↔ 用户）：文本、图片 / 文件 / 视频（经微信 CDN 端侧 AES 加密中转）、语音（平台自带转写时 AI 直接可读）、引用消息（以「引用」摘要呈现上下文）；私聊直接触发 AI 对话。**平台限制**：
- 无群聊会话概念：插件看到的所有会话都是私聊（`message_type=private`）
- 无好友/群列表 API，面板通讯录不展示本平台；无消息历史 API，历史消息仅覆盖适配器运行期间的缓存（AI 会话历史不受影响，由持久化存储承载）
- 无 @、戳一戳、表情回应、撤回等事件；QQ 专属能力在本平台不生效
- 每个微信号只能作为「用户」扫码授权一个 bot 会话；「设置向导」中仅勾选启用（微信无需填写连接配置），重启后到配置页扫码
:::

### adapter —— QQ(NapCat) 协议适配器

WebSocket 与 HTTP **二选一**，由配置键 `bot.adapter.mode`（`ws` / `http`）决定。在[首次设置向导](/guide/web-panel#首次设置向导)中选择，也可在面板「配置编辑」中修改，**重启后生效**：

::: code-group

```text [WebSocket（推荐）]
bot.adapter.mode               = ws                    # 连接模式（默认）
bot.adapter.token              # 若 NapCat 端设置了 access token 则设置该键
bot.adapter.ws.address         = ws://localhost:4455   # NapCat WebSocket 服务端地址
bot.adapter.ws.worker_count    = 0                     # 事件处理线程数，0 = 按 CPU 自动调整
bot.adapter.ws.worker_queue_size = 1024                # 消息队列长度，超出则丢弃
```

```text [HTTP]
bot.adapter.mode               = http                  # 连接模式
bot.adapter.token              # 若 NapCat 端设置了 access token 则设置该键
bot.adapter.http.listen_port   = 6679                  # 本地监听端口，接收 NapCat 事件上报
bot.adapter.http.target_url    = http://localhost:6680 # NapCat HTTP 服务端地址
```

:::

::: warning Docker 部署注意
HTTP 模式下 NapCat 向 `localhost` 上报会失败，请将 NapCat 的 HTTP Client 地址改为 AniaBot 所在机器的内网 IP。
:::

### luckylilia —— QQ(Luckylilia/LLBot) 协议适配器

接入 [Luckylilia（LLBot）](https://api.luckylillia.com/llms.txt) —— 一个与 NapCat 同类的 **OneBot v11** QQ 协议端。能力与 NapCat 适配器基本一致（文本/图片/合并转发/戳一戳/群签到/表情回应等），全部面向 QQ 插件生态的插件可直接生效。WebSocket 与 HTTP **二选一**，由配置键 `bot.luckylilia.mode`（`ws` / `http`）决定，在面板勾选启用 QQ（Luckylilia）并填写，**重启后生效**：

::: code-group

```text [WebSocket（推荐）]
bot.platform.luckylilia.enable    = true                  # 启用平台
bot.luckylilia.mode               = ws                    # 连接模式（默认）
bot.luckylilia.token              # 若 LLBot 端设置了 Token 则填写（Authorization: Bearer 携带）
bot.luckylilia.ws.address         = ws://localhost:3001   # LLBot WebSocket 正向服务地址
bot.luckylilia.ws.worker_count    = 0                     # 事件处理线程数，0 = 按 CPU 自动调整
bot.luckylilia.ws.worker_queue_size = 1024                # 消息队列长度，超出则丢弃
```

```text [HTTP]
bot.platform.luckylilia.enable    = true                  # 启用平台
bot.luckylilia.mode               = http                  # 连接模式
bot.luckylilia.token              # 若 LLBot 端设置了 Token 则填写
bot.luckylilia.http.listen_port   = 6689                  # 本地监听端口，接收 LLBot 事件上报
bot.luckylilia.http.target_url    = http://localhost:6690 # LLBot HTTP 服务端地址
```

:::

::: tip 与 NapCat 并存
本平台的群号/好友号/消息 ID 统一带 `lil:` 前缀（NapCat 为 `qq:`），因此**两者可同时启用**（两个 QQ 账号各连一个协议端），消息互不串线；管理员等按 ID 判断的配置需填写带 `lil:` 前缀的 ID（如 `lil:123456`）。`nc_get_rkey`（防撤回依赖）为 NapCat 专属接口，LLBot 不支持时相关能力自动退化。
:::

### store.cache —— 缓存存储

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.store.cache.driver` | `memory` | 缓存层（易失，支持 TTL 与列表语义）：`memory`（进程内，重启清空）/ `redis`（多实例共享） |
| `bot.store.cache.redis.address` | `localhost:6379` | Redis 地址（driver 为 redis 时生效） |
| `bot.store.cache.redis.password` | 空 | Redis 密码 |
| `bot.store.cache.redis.db` | `0` | Redis 数据库编号 |

持久化层的位置由上方[环境变量](#引导配置-环境变量)决定，不在面板中配置。

## plugin.ai_chat_bot —— AI 对话插件

面板位置：**配置编辑 → 插件配置**

### 基础配置

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.api_format` | `chat_completions` | LLM API 格式：`chat_completions`（OpenAI 兼容，DeepSeek 等）/ `responses`（OpenAI Responses API）/ `anthropic`（Anthropic Messages API，Claude） |
| `plugin.ai_chat_bot.base_url` | `https://api.deepseek.com` | API 地址；`anthropic` 格式填 `https://api.anthropic.com` |
| `plugin.ai_chat_bot.api_key` | 空（必填） | API 密钥 |
| `plugin.ai_chat_bot.model` | `deepseek-chat` | 主模型名称 |
| `plugin.ai_chat_bot.multimodal` | `false` | 主模型是否支持图片输入 |
| `plugin.ai_chat_bot.rate_limit` | `2` | 同时处理的 AI 请求并发上限，超出后直接拒绝 |
| `plugin.ai_chat_bot.headers` | `[]` | 自定义请求头，附加到全部 LLM 请求（主对话 / 子代理 / 定时任务 / 压缩器 / 备用识图 / 备用模型，以及启用向量检索时的 embedding 请求）；每行一个，格式 `Name: Value` 或 `Name=Value`，同名覆盖默认头（含 `User-Agent`），适合自建网关鉴权、路由标记等场景；非法行跳过并记日志 |

::: tip 关于自定义请求头
配置示例（面板「自定义请求头」文本框每行一条）：

```
Authorization: Bearer your-gateway-token
X-Proxy-Route: team-a
```

请求头会原样下发，请只填写可信内容；值里有冒号时用 `=` 分隔（如 `X-Token=a:b`）更直观，反之亦然。修改后需重启 Bot 生效。
:::

::: tip 关于 API 格式
三种格式的对话能力（工具调用、流式回复、token 统计、备用模型切换）行为一致。差异说明：

- `anthropic`：深度思考（`thinking.mode`）映射为 `budget_tokens`，思考块会随历史持久化并在多轮中原样回传；`top_k` 原生支持，但开启思考时 temperature/top_p/top_k 按 API 要求不下发
- `responses`：`top_k` 不支持会被忽略
- 上下文压缩器（`plugin.ai_chat_bot.compressor.api_format`）、备用模型（`plugin.ai_chat_bot.fallback.api_format`）可独立选择格式，留空跟随主模型；子代理与 Agent 团队成员固定跟随主模型格式
:::

### 会话管理

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.session.max_idle_minutes` | `120` | 闲置会话回收时间（分钟），超过未活动的会话被从内存淘汰；`0` 表示不按闲置淘汰 |
| `plugin.ai_chat_bot.session.max_sessions` | `128` | 最大驻留内存的会话数，超出时淘汰最久未活动的会话；`0` 表示不限制 |

淘汰只释放内存对象，对话历史已持久化，下次发言自动重建并回放。注意：会话内通过 `mcp_load` 动态加载的工具会随淘汰失效（等同重启），再次对话时需重新加载。

### 模型参数

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.max_context_tokens` | `128000` | 上下文 token 预算，超过 80% 自动压缩历史 |
| `plugin.ai_chat_bot.max_iterations` | `20` | 主对话单次回复的最大工具调用轮数，超出后强制结束 |
| `plugin.ai_chat_bot.temperature` | `1.2` | 采样温度 |
| `plugin.ai_chat_bot.top_p` | `0.9` | 核采样 |
| `plugin.ai_chat_bot.top_k` | `100` | Top-K 采样 |
| `plugin.ai_chat_bot.max_token` | 未设置 | 单次回复最大输出 token；留空不传该参数，使用模型 API 最大输出上限（anthropic 格式必填，留空时按 8192 下发）。设得过低会把长内容（如 write_file 的参数）拦腰截断 |
| `plugin.ai_chat_bot.thinking.enable` | `false` | 深度思考开关 |
| `plugin.ai_chat_bot.thinking.mode` | `auto` | `none` / `low` / `medium` / `high` / `auto` |
| `plugin.ai_chat_bot.prompt` | 内置场景化 system prompt，按工具场景选择并说明异常处理方式（完整默认值见 `bot/plugins/pluginaichat/config.go` 的 `defaultPrompt`） | 系统提示词（system prompt） |

::: tip 按群/按人定制人格（保存后立即生效，无需重启）
在面板的「文件编辑 → Prompt 覆盖」页（配置键 `files.prompt_json`，原 `aniabot.prompt.json`），可为特定群聊或好友覆盖 system prompt：

```json
{
  "groups": { "qq:123456": "你是这个群的管理助手..." },
  "friends": { "qq:7891011": "你是我的私人秘书..." }
}
```
:::

### Skill 系统

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.skills_dir` | `./skills` | Skill 目录 |
| `plugin.ai_chat_bot.skills` | `[]` | 指定加载的 skill 名称，空 = 加载全部 |
| `plugin.ai_chat_bot.skill_tool.enable` | `false` | 启用 AI Skill 管理工具（`skill_list` / `skill_install` / `skill_remove`），允许 AI 用 `webSearch` / `webExplore` 上网搜索技能资源后自行下载安装（zip 链接 / GitHub 仓库 / SKILL.md 直链），或直接撰写 SKILL.md 内容创建技能，安装后热重载立即生效 |

常驻的 `skill_reload` 工具（无需开关）用于 AI 直接编辑本地 skill 文件（如经 `bash`）后刷新缓存——面板/管理工具的安装删除会自动热重载，绕过管理器直接改文件则需调用它刷新。

### 联网搜索

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.search.token` | 空 | Jina AI token，用于 `web_search` / `web_explore` 工具 |

### OCR 备用识图

主模型不支持多模态时，可配置一个备用视觉模型把图片转述为文字：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.ocr.enable` | `false` | 是否启用 OCR 备用识图 |
| `plugin.ai_chat_bot.ocr.base_url` | `https://api.siliconflow.cn/v1` | 视觉模型 API 地址 |
| `plugin.ai_chat_bot.ocr.api_key` | 空 | API 密钥 |
| `plugin.ai_chat_bot.ocr.model` | `Qwen/Qwen3-VL-8B-Instruct` | 视觉模型名称 |
| `plugin.ai_chat_bot.ocr.temperature` | `0.6` | 采样温度 |
| `plugin.ai_chat_bot.ocr.top_p` | `0.95` | 核采样 |
| `plugin.ai_chat_bot.ocr.top_k` | `20` | Top-K 采样 |
| `plugin.ai_chat_bot.ocr.max_token` | `600` | 单次描述最大 token |
| `plugin.ai_chat_bot.ocr.prompt` | `你负责将看到的图片用markdown格式描述出来，不要有无关的其他对话` | 图片描述提示词 |

### 高危工具（默认关闭）

::: danger 安全提醒
以下工具允许 AI 直接操作宿主机，存在风险，**默认全部关闭**，请确认环境隔离后再开启。
:::

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.bash.enable` | `false` | 允许 AI 在宿主机执行 shell 命令 |
| `plugin.ai_chat_bot.bash.shell` | 空 | 命令解释器，留空使用系统默认（Linux/macOS 为 `sh`，Windows 为 `cmd`），可填 `/bin/bash`、`/bin/ash` 等 |
| `plugin.ai_chat_bot.bash.env` | `[]` | 环境变量，如 `["HOME=/root"]` |
| `plugin.ai_chat_bot.bash.whitelist` | `[]` | 命中这些正则的命令直接放行；黑白名单都不命中（含均未配置）时经工具审批确认后执行 |
| `plugin.ai_chat_bot.bash.blacklist` | `["config(\\.dev)?\\.(yaml|yml|json)", "^mkfs", "^shutdown", "^reboot"]` | 匹配这些正则的命令被禁止（优先于白名单） |
| `plugin.ai_chat_bot.local_image.enable` | `false` | 允许 AI 读取宿主机本地图片 |
| `plugin.ai_chat_bot.computer_use.enable` | `false` | 电脑操作工具：AI 可截图查看宿主机屏幕并控制鼠标键盘（`screenshot` / `mouse_click` / `mousemove` / `mouse_scroll` / `keyboard_type` / `keyboard_press` / `active_window` / `list_windows`）；仅支持 Windows 宿主机。可把 `mouse_click` 等加入 `approval.tools` 实现每次操作人工确认，所有操作写入操作审计日志 |
| `plugin.ai_chat_bot.computer_use.max_width` | `1280` | 截图最大宽度（像素），超出时等比缩小以节省 token，`0` 表示不缩放 |

### 任务清单（todo）

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.todo.enable` | `true` | 启用后 AI 可用 `todo_write` 维护当前会话的任务清单（内存态），复杂多步任务逐项推进；有未完成项时后续对话自动注入提醒 |

### 工具审批（approval）

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.approval.enable` | `false` | 启用后下列工具执行前需人工确认（请求发送者或管理员回复「允许/拒绝」）；同时作为 bash 未列名命令的审批通道（关闭时 bash 未列名命令默认放行，只认黑名单）。配置修改类工具（`config_set`/`config_file_set`）恒需管理员审批（提示私聊发给管理员），与此开关无关 |
| `plugin.ai_chat_bot.approval.tools` | `file` | 需审批的工具名（逗号分隔）；bash 有命令级黑白名单 + 审批三段式，无需列入；配置修改类工具恒需管理员审批，无需列入。启用电脑操作工具后建议把 `mouse_click`、`keyboard_type` 等列入，实现每次操作人工确认 |
| `plugin.ai_chat_bot.approval.timeout_sec` | `120` | 审批超时（秒），超时无回复自动拒绝；范围 10~240 |

### AI 钩子（hooks）

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.hooks.enable` | `false` | 启用后按 `files.hooks_json`（面板「扩展配置」页编辑）在会话事件上执行 shell 命令；钩子在宿主机执行，请仅配置可信命令 |
| `plugin.ai_chat_bot.hooks.timeout_sec` | `10` | 单个钩子默认超时（秒），可在 JSON 中按条覆盖，上限 60 |

钩子事件：`SessionStart` / `UserPromptSubmit` / `PreToolUse` / `PostToolUse` / `Stop` / `SubagentStop` / `PreCompact`；其中 `UserPromptSubmit` 与 `PreToolUse` 可阻断（退出码 2）。`PreToolUse` 挂在高频工具上会按轮放大延迟，请谨慎配置。语义详见 [AI 引擎（三）](/internals/agent-tools#钩子系统-hooks)。

### AI 定时任务（clock）

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.clock.enable` | `true` | 启用后 AI 可自主创建定时任务 |
| `plugin.ai_chat_bot.clock.default_timeout_sec` | `120` | 单次触发默认超时（秒） |
| `plugin.ai_chat_bot.clock.max_log_entries` | `500` | 执行日志保留条数（滚动覆盖） |

与框架的 `StartCron` 静态任务不同，clock 任务由 AI / 用户**动态创建**、持久化保存、重启不丢。执行日志可在面板「状态总览」页查看。详见 [AI 对话插件](/guide/builtin-plugins#ai-对话插件)。

### AI 长期记忆（memory）

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.memory.enable` | `true` | 启用后 AI 可通过 `memory_save` / `memory_search` / `memory_forget` 工具管理长期记忆 |
| `plugin.ai_chat_bot.memory.max_entries` | `200` | 单个会话（群/好友）的记忆条数上限 |

记忆按群聊 / 好友隔离、持久化保存、重启不丢。详见 [AI 对话插件](/guide/builtin-plugins#ai-对话插件)。

### AI 子代理（subagent）

子代理默认启用、无独立配置项：AI 通过 `subagent_run` 工具把复杂/耗时的子任务委派给一次性子代理执行，子代理以全新一次性上下文运行、拥有与主 AI 一致的工具能力，但**不能再委派子代理**。

执行参数全部跟随主模型：模型与 API 格式、生成参数（Temperature / 参数 / 深度思考等）取「AI 对话 · 模型」配置；工具调用轮数取主模型的「最大工具调用轮数」；超时取框架级「消息处理超时」（`bot.msg_event_timeout_sec`，默认 5 分钟，单次调用可覆盖、上限 1800 秒，实际还会按主请求剩余预算自动收缩并预留收尾时间）。子代理结果不截断，完整回填给主 AI。详见 [AI 对话插件](/guide/builtin-plugins#ai-对话插件)。


### AI 知识库（knowledge）

知识库让 AI 把完整资料（文章、URL 正文等）存入会话库或全局库，并在对话中按需检索引用：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.kb.enable` | `true` | 启用后 AI 可通过 `kb_add` / `kb_search` / `kb_forget` 等工具管理知识库 |
| `plugin.ai_chat_bot.kb.max_docs` | `500` | 单个作用域（会话库 / 全局库）的文档条数上限 |
| `plugin.ai_chat_bot.kb.auto_inject` | `true` | 每次对话前自动按关键词检索相关文档并注入上下文（不走向量，避免每条消息产生 embedding 成本） |
| `plugin.ai_chat_bot.kb.embedding.enable` | `false` | 启用向量检索：入库时计算语义向量，检索时与关键词混合打分；provider 不支持时自动退回纯关键词 |
| `plugin.ai_chat_bot.kb.embedding.base_url` | 空 | Embedding API 地址，留空使用主模型 Base URL（主模型无 embedding 接口时可填 `https://api.jina.ai/v1` 等） |
| `plugin.ai_chat_bot.kb.embedding.api_key` | 空 | Embedding API 密钥，留空使用主模型 API Key（用 Jina 时可填 Jina AI Token） |
| `plugin.ai_chat_bot.kb.embedding.model` | `jina-embeddings-v3` | Embedding 模型，如 `text-embedding-3-small`、`BAAI/bge-large-zh-v1.5` |

长文档按 600 字符一块、60 字符重叠切片入库，检索命中块而非整篇，避免无关内容占用上下文。详见 [AI 对话插件](/guide/builtin-plugins#ai-对话插件)。

### Agent 团队（team）

多代理编排，默认启用、无独立配置项：主 AI 可组建团队，把子任务派发给多个带角色描述的成员代理**并行执行**，全部完成后汇总结果；团队定义可在会话内保存复用，也可在 Web 面板「Agent 团队」页管理。

每个成员复用子代理执行路径（独立一次性上下文），模型与执行参数同样跟随主模型（详见上一节），**不能递归组建团队**；单次最多并行 5 个成员，成员结果完整汇总、不截断。详见 [AI 对话插件](/guide/builtin-plugins#ai-对话插件)。

### 每日 Token 配额（quota）

按「每会话每日」与「全局每日」两个维度限制 AI 消耗（含主对话、子代理、定时任务）：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.quota.enable` | `false` | 启用每日配额限制 |
| `plugin.ai_chat_bot.quota.daily_tokens` | `0` | 每会话每日 token 上限；`0` 不限制，超出后该会话当日 AI 请求被拒绝 |
| `plugin.ai_chat_bot.quota.global_daily_tokens` | `0` | 全局每日 token 上限；`0` 不限制，所有会话合计超限后全部拒绝 |

计数按天持久化（键带日期天然过期），重启不丢；未设置上限时仍会记录用量，供面板「配额管理」页展示。

### Query 日志（query_log）

在面板记录每次 AI 回复的完整执行过程：

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.query_log.enable` | `true` | 启用 Query 日志 |
| `plugin.ai_chat_bot.query_log.max_entries` | `200` | 日志保留条数（滚动覆盖） |
| `plugin.ai_chat_bot.query_log.max_tool_records` | `200` | 单条日志保留的工具调用明细条数，超出部分仅保留总数；`0` 表示不限制 |
| `plugin.ai_chat_bot.query_log.max_result_runes` | `1000` | 工具执行结果写入日志时的截断上限（字符数）；`0` 表示不限制。仅影响日志记录，不影响 AI 实际收到的完整输出；用户输入、工具参数、最终回复始终完整记录 |

每条日志包含：触发会话、发送者、用户输入、LLM 轮数、工具调用明细（名称/参数/结果/耗时）、token 用量与最终回复，状态区分 `running` / `success` / `stopped` / `timeout` / `error`。面板「Query 日志」页可筛选查看。

### 流式回复（stream）

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.stream.enable` | `true` | 平台支持「先发后改」时（飞书卡片 / Telegram / Discord 消息实时更新）逐字展示回复；不支持或出错时自动退化为一次性回复 |

### 重试、备用模型与压缩器

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.retry.max_attempts` | `3` | 应用层最大尝试次数；`0` 或 `1` 不重试。429 / 5xx / 网络错误时指数退避重试（SDK 已内置 429/5xx 重试，此为补充层） |
| `plugin.ai_chat_bot.retry.base_delay_sec` | `2` | 退避基准（秒），每次重试等待 `基准×2^n` 秒并带随机抖动，上限 30 秒 |
| `plugin.ai_chat_bot.fallback.base_url` | 空 | 备用模型 Base URL，留空使用主模型配置 |
| `plugin.ai_chat_bot.fallback.api_key` | 空 | 备用模型 API Key，留空使用主模型配置 |
| `plugin.ai_chat_bot.fallback.model` | 空 | 备用模型；主模型重试耗尽或遇到不可重试错误时自动切换重试一次（流式已输出首字节后不切换，避免重复输出） |
| `plugin.ai_chat_bot.fallback.api_format` | 空 | 备用模型 API 格式，留空跟随主模型 |
| `plugin.ai_chat_bot.compressor.base_url` | 空 | 上下文压缩器 Base URL，留空使用主模型 |
| `plugin.ai_chat_bot.compressor.api_key` | 空 | 上下文压缩器 API Key |
| `plugin.ai_chat_bot.compressor.model` | 空 | 压缩器模型，建议填更便宜的模型降低历史压缩成本 |
| `plugin.ai_chat_bot.compressor.api_format` | 空 | 压缩器 API 格式，留空跟随主模型 |

## plugin.interceptor —— 请求拦截插件

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.interceptor.enable` | `false` | 是否启用请求拦截，关闭时放行全部消息 |
| `plugin.interceptor.mode` | `blacklist` | 名单模式：`blacklist` 名单内屏蔽 / `whitelist` 仅名单内放行 |
| `plugin.interceptor.platforms` | 全选 | 可用平台（多选）：仅勾选平台进入名单判定，未勾选的平台直接拦截；单独屏蔽某平台时取消勾选它 |
| `plugin.interceptor.groups` | `[]` | 群 ID 名单，每行一个（QQ 为 `qq:群号`，其他平台为带前缀的群 ID，如 `fs:oc_xxx`） |
| `plugin.interceptor.friends` | `[]` | 用户 ID 名单，每行一个（QQ 为 `qq:QQ号`，其他平台带前缀），对私聊及群聊消息发送者均生效 |

被拦截的会话消息不再传播到后续插件（AI 对话插件收不到，不产生 AI 请求）。

::: warning `whitelist` 模式下的放行逻辑
群聊消息必须同时满足「群号在 `groups` 名单」且「发送者 QQ 在 `friends` 名单」才会放行；只填 `groups` 不会放行该群内其他成员的消息。`whitelist` 模式下名单留空会拦截所有会话。
:::

详见 [请求拦截插件](/guide/builtin-plugins#请求拦截插件)。

## plugin.dailyNews —— 每日新闻插件

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.dailyNews.api` | `https://60s.viki.moe/v2/60s?encoding=image-proxy` | 新闻图 API |
| `plugin.dailyNews.cron` | `0 18 * * *` | cron 表达式，默认每天 18:00 触发 |
| `plugin.dailyNews.groups` | `[qq:123456, qq:7891011]` | 接收推送的群 ID 列表（QQ 为 `qq:群号`，其他平台带前缀） |

## files.mcp_json —— MCP 服务定义

配置键 `files.mcp_json`（面板「文件编辑 → MCP 服务器」页，原 `aniabot.mcp.json`）定义 AI 可用的 MCP Server，内容为 JSON，支持 stdio / SSE / Streamable HTTP 三种传输：

```json
{
  "servers": [
    {
      "name": "my-server",
      "transport": "stdio",
      "command": "python",
      "args": ["-m", "mcp_server"],
      "env": { "API_KEY": "xxx" },
      "timeout": 30
    },
    {
      "name": "remote-server",
      "transport": "sse",
      "endpoint": "http://localhost:3000",
      "headers": { "Authorization": "Bearer token123" },
      "timeout": 30
    }
  ]
}
```

MCP 工具默认采用两阶段懒加载：AI 先通过发现工具查看有哪些 MCP 能力，按需加载到当前会话，避免工具描述撑爆上下文。

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.ai_chat_bot.mcp.lazy_load` | `true` | MCP 工具懒加载：开启时按需发现/加载（`mcp_discover` / `mcp_load`），节省上下文；但会话内动态加载会改变 tools 列表，可能降低上游 prompt 缓存命中率。关闭后启动时全量注册所有 MCP 工具（工具列表恒定、缓存友好，但工具较多时上下文开销大） |
| `plugin.ai_chat_bot.mcp_tool.enable` | `false` | 启用 AI MCP 管理工具（`mcp_list` / `mcp_add` / `mcp_remove` / `mcp_reconnect`），允许 AI 自行添加/删除/重连/查看 MCP 服务器；添加/删除写入 `files.mcp_json` 持久化并即时热注册/注销生效 |

