# 演示模式（demo mock）

本目录实现 Web 面板的"在线体验"演示模式：`npm run build:demo`（`vite build --mode demo`）产出一份不需要后端的纯静态面板，部署在 GitHub Pages（`/demo/`）供文档访客体验。正式构建（`npm run build`）中 `import.meta.env.MODE === 'demo'` 为静态 false，整个 mock 目录经 tree-shake 剔除，不会进入 `bot/adminpanel/dist`。

## 数据来源

`seed/*.json` 全部来自**真实运行的实例**：用临时 SQLite 目录启动 AniaBot，通过面板 API 完成初始化、写入演示配置、创建定时任务/记忆/团队/知识库后，把各端点的真实 JSON 响应原样抓取而来：

- `config_schema.json`（171 个字段）、`config.json`、`plugins.json`、`team_roles.json`、`oplogs.json`、`consolelogs.json`（真实启动日志）等 —— 直接来自真实 API；
- `marketplace_plugins.json` 与 `marketplace_detail_*.json` —— 通过插件市场的真实管道（AniaBot-Project/AniaBot-Plugins 的 index.json + README）抓取的 16 个真实插件；
- `status.json`（适配器状态改为 connected）、`host.json`（换成中立的 Linux 容器画像）、`balance.json`（启用并给演示值）—— 结构保持真实，仅替换了演示值；已清除真实主机名/用户名等隐私信息；
- 消息/Query/任务日志、Token 统计、配额、通讯录、技能 —— 真实实例为空，由 `synthetic.js` 按真实 DTO 的字段形状（参考 `bot/adminpanel` 各结构体）合成合理数值。

## 行为说明

- 登录自动通过（`/api/me` 直接返回 ok），任意密码可登录；
- 增删改（定时任务/记忆/团队/知识库/配置/预设/市场安装卸载等）会修改内存状态、即时生效，刷新页面恢复初始演示数据；
- 主机 CPU 曲线、uptime、控制台日志等按请求动态生成/追加，让面板"活着"；
- 市场安装会模拟"拉取 → 编译 → 重启"几秒钟的进度日志；
- `handlers.js` 会把抓取时刻的时间戳整体平移到当前时间，演示数据永远像"刚刚发生"。

## 重新抓取种子数据

后端接口字段变动后，可重跑抓取流程刷新 seed（需要 Go + Node 环境）：

```bash
cd web && npm ci && npm run build          # 先产出面板 dist（go:embed 需要）
go build -o /tmp/aniabot-demo/aniabot.exe ./cmd
cd /tmp/aniabot-demo && ANIABOT_STORE_DRIVER=sqlite ANIABOT_SQLITE_PATH=./data/aniabot.db ./aniabot.exe
# 另开终端：登录（初始密码在控制台）、写入演示数据并把各端点响应存为 seed/*.json
```

抓取时的演示脚本（建配置、定时任务、记忆、团队、知识库等）可参考本目录各 seed 文件的内容反推，或参考 `handlers.js` 里对应路由的请求体。
