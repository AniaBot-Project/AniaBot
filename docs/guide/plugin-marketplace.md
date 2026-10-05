# 插件市场

AniaBot 提供插件市场：从独立的官方插件仓库（[jeanhua/AniaBot-Plugins](https://github.com/jeanhua/AniaBot-Plugins)）浏览插件介绍，并在面板上**在线安装 / 升级 / 卸载**第三方插件。安装会自动下载插件源码、生成注册代码、重新编译并重启 Bot。

> ⚠️ **安全提示**：安装插件等于在 Bot 所在机器上编译并执行插件代码（与 Bot 同进程）。请只安装你信任的插件。市场仓库的插件经过维护者人工审查，但无法保证第三方依赖与未来版本绝对安全。插件市场默认关闭。

## 工作原理

Go 不支持跨平台热加载插件，因此 AniaBot 采用「**源码级安装 + 重新编译 + 重启**」模型：

```
插件仓库 (GitHub API / Token) → 下载插件源码
→ 写入 data/plugins（持久卷）与源码树 custom/plugins/<id>
→ tools/plugingen 生成注册代码（cmd/marketplace_plugins.go）
→ go mod tidy → go build → 替换二进制 → 重启
```

第三方插件统一安装到 `custom/plugins` 目录，与框架内置插件（`bot/plugins`）完全隔离；插件编译期与当前框架 API 绑定，**编译不过就装不上**，不会出现"装上了但跑不起来"的版本错配。

## 前置条件

1. **以编译后的二进制部署**（推荐容器部署，见 [容器部署](./docker)），`go run` 开发模式下不可用
2. 已配置源码目录：`bot.marketplace.source_dir`（留空时回退用自动更新的 `bot.update.source_dir`），且至少完成过一次自动更新（源码树需包含前端产物与 `tools/plugingen`）
3. 开启插件市场：配置管理 → 插件市场 → `bot.marketplace.enable=true`，重启后生效
4. 机器上装有 `git` 与 `go`（容器镜像已内置）

## 使用

### 登录 GitHub（在线登录，OAuth 设备流）

GitHub API 未登录限流 60 次/小时，登录后可提升到 5000 次/小时。插件市场只支持在线登录（不再提供手动 Token 输入）。

> 开箱即用：默认已内置 AniaBot 官方 OAuth App 的 Client ID（`bot.marketplace.oauth_client_id` 默认值），面板直接点「使用 GitHub 登录」即可；多人共用官方 App 时可能触发 GitHub 设备流限流（每小时 50 次），如遇限流或想独立配额，再按下面步骤创建自己的 App 并覆盖配置。

1. 在 GitHub 创建一个 OAuth App：<https://github.com/settings/developers> → OAuth Apps → New OAuth App（Authorization callback URL 可随便填，如 `http://localhost`，设备流不使用回调地址）
2. 在应用设置中启用 **Device flow**（OAuth App → 应用设置 → Enable Device Flow）
3. 把应用的 **Client ID** 填入面板「配置管理 → 插件市场」的 `bot.marketplace.oauth_client_id`，重启后生效
4. 在插件市场页点击「使用 GitHub 登录」→ 弹出授权码与链接 → 浏览器打开 <https://github.com/login/device> 输入授权码 → 自动完成登录

### 浏览与安装

1. 面板 →「插件市场」，查看插件列表（名称 / 简介 / 作者 / 版本 / 标签）
2. 点击插件查看详情（README 介绍）
3. 点击「安装」，确认安全提示后进入流水线：下载源码 → 校验元信息 → 编译 → 替换二进制 → 重启，页面实时显示进度日志
4. 重启完成后插件即生效，出现在「状态总览 → 已加载插件」与 `/help` 中；若插件声明了 `ConfigSchema()`，其配置项自动出现在「配置管理」

### 升级 / 卸载 / 回滚

- **升级**：插件卡片显示「可更新」时点击「升级」（安装到指定 commit，可复现）
- **卸载**：已安装插件点击「卸载」，重新编译并重启后移除；实现了 `plugin.UnloadEvent` 接口的插件会在重启前收到 `OnUnload` 回调（`reason=uninstall`），可借此清理自身数据
- **回滚**：任何安装/卸载操作前都会备份旧二进制（`<exe>.old`），出问题可点击「回滚上次安装」恢复

### 自动更新与容器重建

- 自动更新流水线在拉取最新代码后会**自动重放**已安装的插件并重新生成注册代码，再编译，插件不会因更新丢失
- 插件持久副本存放在 `bot.marketplace.plugin_dir`（默认 `./data/plugins`，位于持久卷），容器重建后依然保留

## 配置项

| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `bot.marketplace.enable` | `false` | 是否启用插件市场 |
| `bot.marketplace.repo` | `jeanhua/AniaBot-Plugins` | 插件仓库 owner/repo |
| `bot.marketplace.branch` | `main` | 插件仓库分支 |
| `bot.marketplace.source_dir` | 空 | 编译用源码目录，留空回退 `bot.update.source_dir` |
| `bot.marketplace.plugin_dir` | `./data/plugins` | 已安装插件持久副本目录 |
| `bot.marketplace.cache_dir` | `./data/marketplace` | 市场索引/下载缓存目录 |
| `bot.marketplace.oauth_client_id` | `Ov23li6fHYmQOGOmliT4` | GitHub OAuth App Client ID（在线登录用，默认官方 App，可覆盖） |

## 提交自己的插件

插件市场是独立仓库 [jeanhua/AniaBot-Plugins](https://github.com/jeanhua/AniaBot-Plugins)，通过 Pull Request 提交：

1. Fork 仓库，在 `plugins/<id>/` 下创建插件（`plugin.json` + `README.md` + Go 源码），规范见仓库内 `docs/plugin-spec.md`
2. 本地校验 `bash scripts/validate.sh`，或直接依赖 CI
3. 提交 PR，CI 会校验元信息并编译；维护者人工审查（重点：网络请求、文件读写、进程执行、凭据访问、第三方依赖）后合并

插件开发方式与内置插件完全一致，见 [插件开发指南](../plugin/overview)。

## 相关链接

- [插件仓库 jeanhua/AniaBot-Plugins](https://github.com/jeanhua/AniaBot-Plugins) —— 浏览插件源码、提交 PR 发布自己的插件
- [插件规范（plugin.json）](https://github.com/jeanhua/AniaBot-Plugins/blob/main/docs/plugin-spec.md)
- [贡献指南（CONTRIBUTING）](https://github.com/jeanhua/AniaBot-Plugins/blob/main/CONTRIBUTING.md)
- [插件系统概览](/plugin/overview) —— 插件开发入门

