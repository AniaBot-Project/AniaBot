---
name: web
description: 联网搜索与网页抓取（内置 CLI，需启用 bash 工具）：查最新资料、核实外部事实、读取网页正文
---

# 联网搜索与网页抓取

需要最新资讯、外部事实、网页正文，或要查找某个资源链接时使用本技能。
本技能自带 `webctl.py`，通过命令行调用搜索/抓取平台（当前支持 DeepSeek）。

## 前置条件

- **必须启用 bash 工具**（面板「AI 对话 · 工具 → 启用 Bash 工具」），否则无法执行命令。
- 需要 Python 3。
- 需要 `DEEPSEEK_API_KEY` 环境变量：可在运行 AniaBot 的系统环境中配置，或在面板
  bash 工具的「环境变量」里加一行 `DEEPSEEK_API_KEY=sk-...`。没有 Key 时命令会明确报错。
- `webctl.py` 的绝对路径见 skill_read 结果末尾的「附带文件」列表，**直接用该路径，不要猜**。

## 命令

搜索（默认返回 10 条结果，含标题、链接、摘要）：

```bash
python3 "<webctl.py 的绝对路径>" search --deepseek "查询词" [--count 8] [--content]
```

抓取单个网页正文（Markdown，长文按 `--offset` 分页）：

```bash
python3 "<webctl.py 的绝对路径>" fetch --deepseek "https://example.com/article" [--offset 8000]
```

- Windows 或只有 `python` 命令时，把 `python3` 换成 `python`（可先执行 `python3 --version` 探测）。
- 路径含空格时必须加引号。
- 查询词支持 `site:` / `-site:` / `lang:` / `after:` / `before:` / 精确短语等限定符，
  例如 `DeepSeek site:github.com lang:en after:2025`。
- 搜索结果的摘要通常已经够用；需要细节时再加 `--content`，或对具体链接执行 `fetch`。
- 单次 `fetch` 最多返回 8000 字，末尾会给出 `--offset` 续读位置；不要一次抓很多无关网页。

## 执行建议

- 先搜索再抓取：从结果中挑 1-3 个可信来源（相关度高、权威站点）交叉确认，不要只看一条。
- 把结果提炼成用户能看懂的回答并附上来源链接，不要原样粘贴大段网页。
- 涉及价格、版本、发布日期等易变信息时，注明信息时间。
- 不要在命令里写 API Key，也不要向用户索要 Key 明文。

## 错误处理

- `未设置 DEEPSEEK_API_KEY`：告诉用户在环境变量（或 bash 工具环境变量）中配置。
- 401 / 402 / 429：分别表示 Key 无效、余额不足、触发限流，如实说明即可，不要反复重试。
- 抓取 404：目标页面无法提取内容，换其他来源或让用户提供可访问的链接。
- 找不到 Python：说明宿主机缺少 Python 3，可改用其他方式或请管理员安装。

> 本技能是内置技能：不能删除、不能禁用；如需更换搜索平台，可自行新增外置 skill 和 CLI。
