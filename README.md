<div align="center">
  <img src="./README/logo.webp" width="200" alt="AniaBot Logo"/>
  <h1>AniaBot</h1>
  <p>一个插件驱动型多平台机器人框架</p>
  <a href="https://aniabot-project.github.io/AniaBot/demo/#/">在线Demo</a> |
  <a href="https://aniabot-project.github.io/AniaBot/">📖 文档</a> |
  <a href="https://github.com/AniaBot-Project/AniaBot">GitHub</a>
</div>


## 项目介绍

**AniaBot** 是一个基于 Go 语言开发的高性能、插件驱动型**多平台**机器人框架，采用模块化设计，让开发者能够快速构建功能强大的机器人应用。

- **高性能**：基于 Go 语言，充分利用并发特性
- **插件驱动**：功能模块化，易于扩展和维护
- **多平台**：QQ（NapCat/LLBot WebSocket/HTTP，OneBot v11）+ 飞书（官方 SDK 长连接/Webhook）+ Telegram（Bot API 长轮询，无需公网地址）+ Discord（discordgo，Gateway WebSocket，无需公网地址）+ 微信（iLink bot，HTTP 长轮询，扫码登录，无需公网地址）等；新增平台只需实现一个适配器并注册，框架核心零改动
- **开箱即用**：内置 AI 对话（含 MCP/Skills/Tool Use）、复读机、每日新闻等插件
- **插件市场**：独立插件仓库（AniaBot-Plugins）支持 PR 提交插件，面板「插件市场」可浏览插件介绍、在线安装/升级/卸载并自动编译重启（容器内开箱即用）

![framework](./README/framework.webp)

![pannel](./README/pannel.webp)

## 快速开始

```bash
git clone https://github.com/AniaBot-Project/AniaBot.git
cd AniaBot
go mod tidy
cd web && npm ci && npm run build
```

直接运行，首次启动会自动写入默认配置，并在控制台打印 Web 控制面板的随机初始密码：

```bash
go run ./cmd
```

交叉编译

```bash
make linux
make windows
```

登录 `http://127.0.0.1:7700`，按设置向导填写平台连接（NapCat/LLBot 地址 / 飞书 App ID、Secret / Telegram Bot Token / Discord Bot Token，微信无需连接配置、启用后扫码登录即可）、管理员 ID 与 AI 模型配置即可。默认启用 QQ 平台；在「配置 → 平台适配器」中勾选要启用的平台并填写对应连接信息后重启，即可多平台同时在线（如 QQ + 飞书 + Telegram + Discord + 微信）。

二进制部署时，可在面板「自动更新」页一键从 git 拉取最新代码、重新编译并自动重启（需配置源码目录，详见[文档](https://aniabot-project.github.io/AniaBot/guide/web-panel#自动更新)）。

## 二进制部署

可以直接到 [Releases](https://github.com/AniaBot-Project/AniaBot/releases) 下载对应平台的压缩包（linux amd64/arm64、macOS amd64/arm64、Windows amd64），解压即可运行。

首次启动会在当前目录生成 `data/` 存放数据库与配置，并在控制台打印 Web 控制面板的随机初始密码，登录 `http://127.0.0.1:7700` 完成配置。后续升级时重新下载替换二进制即可，数据不受影响。

## Docker 部署

镜像内置 Go / Node.js / git 工具链，面板的「自动更新」在容器内可直接使用。

使用 Docker Compose：

```bash
git clone https://github.com/AniaBot-Project/AniaBot.git && cd ./AniaBot
docker compose up -d
```

或直接使用镜像：

```bash
cd ~/Bot/
docker run -d --name aniabot \
  --net=host \
  -v ./data:/app/aniabot/data \
  -v ./skills:/app/aniabot/skills \
  jeanhua/aniabot:latest
```

详细配置和插件开发教程请查阅 **[文档站点](https://aniabot-project.github.io/AniaBot/)**。

## 插件市场

AniaBot 内置插件市场：面板「插件市场」页可浏览、在线安装 / 升级 / 卸载第三方插件（自动下载源码 → 编译 → 重启，容器内开箱即用）。

- **插件仓库**：[github.com/AniaBot-Project/AniaBot-Plugins](https://github.com/AniaBot-Project/AniaBot-Plugins) —— 官方插件市场，通过 PR 提交自己的插件（插件代码 + plugin.json 元信息 + README 介绍）
- **使用指南**：[插件市场使用文档](https://aniabot-project.github.io/AniaBot/guide/plugin-marketplace)
- **开发自己的插件**：[插件开发文档](https://aniabot-project.github.io/AniaBot/plugin/overview)，写好后到插件仓库提交 PR 即可发布

## 许可证

本项目采用 MIT 许可证，详见 [LICENSE](./LICENSE) 文件。
