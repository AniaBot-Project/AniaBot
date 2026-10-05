# 常见问题

## 连接与启动

### 启动后机器人收不到消息？

按以下顺序排查：

1. **NapCat 是否已登录**：确认 NapCat 端 QQ 登录成功且在线
2. **适配器类型是否匹配**：`bot.adapter.mode` 为 `ws`（WebSocket）时，NapCat 就必须开启 WebSocket 服务端；为 `http` 时则对应 HTTP 服务端 + 客户端上报
3. **地址是否正确**：默认 `ws://localhost:4455`，Docker 环境不能用 `localhost`，改用内网 IP
4. **access token**：NapCat 端设置了 token 时，在面板「配置管理」中填写 `bot.adapter.token`（重启生效）

### WebSocket 一直重连失败？

- 连接失败会**无限重试**（指数退避，封顶 30 秒），连接状态与最近错误可在 Web 面板查看；修改连接配置后重启生效
- 检查 NapCat 的 WebSocket 服务端是否监听在配置的端口
- Windows 上检查防火墙是否拦截

### 提示「配置读取错误」或插件初始化失败？

配置存于数据库，通过面板修改后**需要重启才生效**——确认改完后重启过 Bot。插件读取不到自己的配置键时会在 `Start()` 返回错误并记录日志；可在面板「配置管理 → 高级模式 (JSON)」中检查键名是否正确（点分路径，大小写不敏感）。

## 多平台

### 怎么接入飞书？

在飞书开放平台创建企业自建应用并开通权限，然后在面板「配置 → 平台适配器」勾选「启用飞书平台」、填写 `bot.feishu.app_id` / `bot.feishu.app_secret` 后重启。默认走 **WebSocket 长连接**，无需公网地址。详见 [配置详解](/guide/configuration#feishu-——-飞书适配器)。

### 怎么接入 Telegram？

向 [@BotFather](https://t.me/BotFather) 创建机器人拿到 Bot Token，然后在面板「配置 → 平台适配器」勾选「启用 Telegram 平台」、填写 `bot.telegram.token` 后重启。默认走 **Bot API 长轮询**，无需公网地址、无需部署协议端；国内部署如无法直连官方 API，可配置 `bot.telegram.proxy`（HTTP/SOCKS5 代理）或 `bot.telegram.api_base`（自建 Bot API 网关/反代）。详见 [配置详解](/guide/configuration#telegram-——-telegram-适配器)。

### 怎么接入微信？

在面板「配置 → 平台适配器」勾选「启用微信平台」（`bot.platform.weixin.enable`）并重启。与其他平台不同，微信没有静态 Token 可申请：重启后在**面板「配置」页的「微信扫码登录」卡片**（或 Bot 控制台）扫码，按提示输入手机上显示的配对数字并确认即可完成授权，凭据自动保存；失效时重新扫码即可。走 **HTTP 长轮询**，无需公网地址、无需部署任何协议端；仅支持一对一私聊（bot ↔ 用户），无群聊/@/撤回等事件。详见 [配置详解](/guide/configuration#weixin-——-微信适配器)。

### 怎么接入 Discord？

在 [Discord Developer Portal](https://discord.com/developers/applications) 创建应用，「Bot」页面获取 Token 并**开启 Message Content Intent**（特权意图，不开则网关拒绝连接），然后在面板「配置 → 平台适配器」勾选「启用 Discord 平台」、填写 `bot.discord.token` 后重启。走 **Gateway WebSocket**，无需公网地址、无需部署协议端；无法直连时可配置 `bot.discord.proxy`（HTTP/SOCKS5 代理，REST 与网关都生效）。详见 [配置详解](/guide/configuration#discord-——-discord-适配器)。

### QQ 和飞书能同时在线吗？

能（加 Telegram、Discord、微信也一样）。平台适配器各自独立开关（`bot.platform.napcat.enable` / `bot.platform.feishu.enable` / `bot.platform.telegram.enable` / `bot.platform.discord.enable` / `bot.platform.weixin.enable`），都开启后即可并存。各平台收到的消息会带上自己的 `Platform` 标识，插件按 `Meta.Platforms` 声明决定处理哪些平台。

### 飞书机器人 @ 了不回复？

1. 确认飞书应用已**发布**（企业自建应用需管理员审核通过，且权限 scope 已开通：`im:message`、`im:message:send_as_bot`、`im:resource` 等）
2. 群里消息默认只推送 **@机器人** 的；要接收群里全部消息需申请 `im:message.group_msg` 敏感权限
3. 面板「控制台日志」页查看飞书适配器日志（含错误码），权限缺失会明确报错

### 为什么某些插件在飞书不生效？

防撤回依赖 QQ 的合并转发与 rkey；戳一戳、群签到、运气王、群荣誉等事件源飞书不存在。这类插件声明 `Meta.Platforms = ["qq"]` 只在 QQ 运行，飞书消息会跳过它们。AI 对话、复读机、拦截器、每日新闻等基于文本/图片的插件在飞书开箱即用。

Telegram 同理：防撤回（依赖 QQ 合并转发/rkey）不生效；成员进出与表情回应仅在机器人是管理员或关闭隐私模式时可见（平台限制）。

Discord 同理：防撤回不生效；成员进出走平台事件（不触发公共进出通知），斜杠命令（Interactions）不在支持范围。

## AI 对话

### @机器人没有反应？

1. 确认 `plugin.ai_chat_bot.api_key` 已填写且有效
2. 查看控制台日志，是否有 API 请求错误（余额不足、模型名错误等）
3. AI 插件 Order = 1000，最后执行 —— 如果某个前置插件返回了 `false`（阻断传播），AI 就收不到消息
4. 同一会话已有请求在进行时，新消息会进入排队队列，待当前响应结束后自动合并回复

### AI 不认识图片？

- 主模型支持多模态：将 `multimodal: true`，AI 会通过 `load_images` 工具按需加载图片
- 主模型不支持多模态：配置 `ocr` 备用视觉模型，图片会先转述为文字
- 两者都没配置时，AI 会明确告知无法查看图片

### 对话历史能保存多久？

历史持久化在 `PersistentStorage`（默认 SQLite），重启不丢。超过 `max_context_tokens` 的 80% 时会自动让 LLM 总结压缩。发送带 `#新对话` 的消息可手动清空。

### AI 创建的定时任务存在哪？

clock 任务持久化在 `PersistentStorage` 的 `clock:` 命名空间下，重启后自动恢复调度。用 `/clock list` 查看，`/clock del <id>` 删除。

## 存储

### 不想装 Redis 可以吗？

可以，默认就是。缓存驱动默认为 `memory`（进程内内存，零依赖）。需要多实例共享缓存时再把 `bot.store.cache.driver` 改为 `redis`。

### 数据文件在哪？

默认 SQLite 持久化数据在 `./data/aniabot.db`（环境变量 `ANIABOT_SQLITE_PATH` 可改）。目录不存在会自动创建。

### 换 MySQL 怎么配？

通过环境变量切换持久化驱动（详见 [配置详解](/guide/configuration#引导配置-环境变量)）：

```bash
export ANIABOT_STORE_DRIVER=mysql
export ANIABOT_MYSQL_DSN="root:password@tcp(host:3306)/aniabot?charset=utf8mb4&parseTime=true&loc=Local"
```

## 插件开发

### 我的插件不触发？

1. 确认在 `cmd/main.go` 中 `bot.AddPlugin()` 注册过
2. 检查是否有高优先级（Order 更小）插件返回了 `false` 阻断传播
3. 命令必须**以 `/` 开头**才会进入 `cmd.Name` 解析；群聊命令还需 @ 机器人（`cmd.Mention == true`）

### 插件 panic 会拖垮整个机器人吗？

不会。框架对每个插件调用都有 `safeExecute` 包裹，panic 会被捕获并触发所有插件的 `OnPanic` 回调（系统插件默认私聊通知管理员）。通过 `bot.Go(name, f)` 启动的协程同样有崩溃恢复。

### 如何调试插件？

保留 `pluginlog` 日志插件，控制台会打印每条收发消息；单元测试参考 `bot/utils` 与各插件的 `_test.go`：

```bash
go test ./...
go test -v -race ./...
```

## 部署

### 怎么交叉编译？

建议使用 Makefile：

```bash
make linux     # GOOS=linux GOARCH=amd64
make windows
```

### 如何更新文档？

文档源码在 `docs/`（VitePress），推送到 `main` 分支后 GitHub Actions 自动构建发布到 `https://aniabot-project.github.io/AniaBot/`。本地预览：

```bash
cd docs
npm install
npm run docs:dev
```



