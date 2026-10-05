# 快速开始

本指南带你在 5 分钟内从零跑起一个 AniaBot 机器人。

## 前置准备

| 依赖 | 版本要求 | 说明 |
| --- | --- | --- |
| Go | **1.25+** | 见 `go.mod`，交叉编译友好 |
| NapCat | 任意近期版本 | **QQ 平台**协议端，提供 OneBot v11 接口（不玩 QQ 可跳过） |
| QQ 官方机器人 | 任意 | **QQ 官方平台**：QQ 开放平台创建机器人获取 AppID/AppSecret，WebSocket 收事件无需公网地址（不玩可跳过） |
| 飞书自建应用 | 任意 | **飞书平台**：开放平台创建应用 + 开通权限（不玩飞书可跳过） |
| Telegram Bot Token | 任意 | **Telegram 平台**：向 [@BotFather](https://t.me/BotFather) 创建机器人获取 Token，长轮询无需公网地址（不玩 Telegram 可跳过） |
| Discord Bot Token | 任意 | **Discord 平台**：[Developer Portal](https://discord.com/developers/applications) 创建应用获取 Token 并开启 Message Content Intent，Gateway WebSocket 无需公网地址（不玩 Discord 可跳过） |
| Redis | 可选 | 缓存后端之一；默认使用内存缓存，无需安装 |
| LLM API Key | 可选 | 启用 AI 对话时需要（DeepSeek / OpenAI 等） |

::: tip 想用 Redis？
默认缓存驱动为 `memory`（零依赖，重启清空）。在面板「配置管理」页将 `bot.store.cache.driver` 改为 `redis` 并填写地址即可，多实例部署时建议切换。
:::

## 第一步：部署 NapCat

AniaBot 不直接实现 QQ 协议，而是通过 [NapCat](https://napneko.github.io/) 连接 QQ。请先按照 NapCat 官方文档完成部署并登录你的机器人 QQ 号，然后开放网络接口（二选一）：

- **WebSocket 服务端**：端口如 `4455`（推荐，事件推送更及时）
- **HTTP**：开放 HTTP 服务端（如 `6680`）并配置 HTTP 客户端上报到 AniaBot 的监听地址

## 第二步：获取源码

```bash
git clone https://github.com/AniaBot-Project/AniaBot.git
cd AniaBot
go mod tidy
```

## 第三步：启动与配置

```bash
go run ./cmd
```

AniaBot 的配置存储在数据库中，**首次启动**会自动写入默认配置，并在控制台打印 Web 控制面板的**随机初始密码**（仅显示一次）：

```
============================================================
  Web 控制面板初始密码（仅显示一次，登录后可修改）:
    Xx9aBcDeFg
============================================================
```

使用该密码登录 `http://127.0.0.1:7700`，首次登录会自动进入**设置向导**：先在「平台接入」步骤勾选要启用的平台（QQ(NapCat) 默认勾选，QQ 官方 / 飞书 / Telegram / Discord / 微信可选；其余平台填写对应连接配置与管理员 ID，微信无需填写、重启后在「配置管理」页扫码登录），再填 AI 模型配置（Base URL / API Key / 模型），保存后一键重启即可生效。

默认启用 QQ 平台。要同时接入 QQ 官方 / 飞书 / Telegram，在向导中勾选对应平台并填写 AppID/AppSecret 或 Bot Token 即可（或稍后在「配置 → 平台适配器」启用，详见 [QQ 官方适配器](/guide/configuration#qqofficial-——-qq-官方适配器)、[飞书适配器](/guide/configuration#feishu-——-飞书适配器) 与 [Telegram 适配器](/guide/configuration#telegram-——-telegram-适配器)）。多平台可同时在线；QQ 官方与 Telegram 都无需部署额外协议端，Telegram 国内部署可在配置中填写代理或自建 API 网关地址。

完整配置项说明见 [配置详解](/guide/configuration)，面板使用见 [Web 控制面板](/guide/web-panel)。

## 第四步：开始使用

看到插件注册日志后，机器人就已上线：

- 私聊机器人发送 `/help` —— 查看已加载插件
- 群里 **@机器人 发送 `/help`** —— 群聊版帮助
- **@机器人 随便说点什么** —— 开始 AI 对话 🎉

启动成功后，系统插件会自动私聊管理员发送「AniaBot启动成功」。

## 构建发布

```bash
make linux     # 交叉编译 Linux amd64 → build/AniaBot
make windows   # 编译 Windows → build/AniaBot.exe
make web       # 重新构建 Web 面板前端（修改 web/ 后需要）
make clean     # 清理 build/
```

所有存储后端均为纯 Go 实现（无 CGO），交叉编译开箱即用。Web 面板前端产物（ot/adminpanel/dist）不随仓库提交，通过 go:embed 嵌入二进制——**全新克隆后需先执行一次 make web（或 cd web && npm run build）再编译**，详见 [Web 控制面板](/guide/web-panel#面板开发)。

## 目录结构速览

```
AniaBot/
├── cmd/main.go            # 入口：空白导入平台适配器包（init 注册）+ 注册插件 + 启动
├── web/                   # Web 控制面板前端（Vite + Vue3 + Tailwind）
├── common/
│   ├── adapter/           # 平台适配器抽象：Adapter（公共契约）+ QQExt（可选能力）+ Definition/Register 注册表
│   ├── bot/               # bot.Bot（公共能力）+ bot.QQ（QQ 专属可选接口）
│   ├── plugin/            # 插件接口：Meta / Plugin / PlatformEventHandler
│   └── model/             # message（通用消息段）/ msgchain（消息构造器）/ command
├── bot/
│   ├── core/              # 核心：插件生命周期、事件分发、多适配器容器 + ID 前缀路由、DI、配置中心
│   ├── adminpanel/        # Web 控制面板后端（配置/状态 API + 内嵌前端）
│   ├── adapter/napcat/    # NapCat WebSocket / HTTP 适配器（QQ 平台）
│   ├── adapter/qqofficial/ # QQ 官方适配器（QQ 开放平台 API v2，WebSocket 网关）
│   ├── adapter/feishu/    # 飞书适配器（官方 SDK，长连接 / Webhook）
│   ├── adapter/telegram/  # Telegram 适配器（Bot API，长轮询）
│   ├── adapter/discord/   # Discord 适配器（discordgo，Gateway WebSocket）
│   ├── adapter/weixin/    # 微信适配器（iLink bot，HTTP 长轮询 + CDN 媒体）
│   ├── component/         # AI 引擎：aichat / llmtool / functool
│   ├── plugins/           # 六个内置插件（系统/日志/复读/请求拦截/AI/每日新闻）
│   └── utils/             # 命令解析、消息提取等工具
└── custom/                # 自定义插件示例与模板
```

新增平台（如 Telegram）= 在 `bot/adapter/` 新建一个实现公共 `Adapter` 接口的包，`init()` 里 `adapter.Register(...)` 注册，然后在 `cmd/main.go` 加一行空白导入即可，框架核心零改动。

配置存于数据库（默认 `./data/aniabot.db`，可用环境变量调整，见 [配置详解](/guide/configuration#引导配置-环境变量)）。

## 下一步

- [容器部署](/guide/docker) —— 生产环境推荐：Docker + Fork 工作流，面板一键更新自己的版本
- [配置详解](/guide/configuration) —— 每一个配置项的含义
- [内置插件](/guide/builtin-plugins) —— 各插件的命令与用法
- [第一个插件](/plugin/first-plugin) —— 开始写你自己的功能





