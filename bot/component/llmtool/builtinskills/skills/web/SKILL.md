---
name: web
description: 联网搜索与网页抓取（内置 CLI，需启用 bash 工具，支持多个搜索平台）：查最新资料、核实外部事实、读取网页正文
---

# 联网搜索与网页抓取

需要最新资讯、外部事实、网页正文，或要查找某个资源链接时使用本技能。
本技能自带 `webctl.py`，通过命令行调用搜索/抓取平台，支持 DeepSeek、Tavily、
Exa、Firecrawl、Jina AI。

## 前置条件

- **必须启用 bash 工具**（面板「AI 对话 · 工具 → 启用 Bash 工具」），否则无法执行命令。
- 需要 Python 3。
- **至少配置一个平台的 API Key 环境变量**（可只配一个，也可配多个），见下方「平台与 Key」。
  可在运行 AniaBot 的系统环境中配置，也可在面板 bash 工具的「环境变量」里每行写
  `KEY=...`。不指定平台时脚本会自动检测这些环境变量并选择可用的平台；一个都没配时
  命令会明确报错并列出可选项。
- `webctl.py` 的绝对路径见 skill_read 结果末尾的「附带文件」列表，**直接用该路径，不要猜**。

## 命令

查看当前哪些平台已配置（排查“为什么不能用”时先跑这个）：

```bash
python3 "<webctl.py 的绝对路径>" providers
```

搜索（默认返回 10 条结果，含标题、链接、摘要；不写平台参数时自动选择）：

```bash
python3 "<webctl.py 的绝对路径>" search "查询词" [--count 8] [--content]
```

抓取单个网页正文（Markdown，完整返回）：

```bash
python3 "<webctl.py 的绝对路径>" fetch "https://example.com/article"
```

需要固定使用某个平台时加 `--<平台名>`（一般不需要）：

```bash
python3 "<webctl.py 的绝对路径>" search --tavily "查询词"
python3 "<webctl.py 的绝对路径>" fetch --firecrawl "https://example.com/article"
```

- Windows 或只有 `python` 命令时，把 `python3` 换成 `python`（可先执行 `python3 --version` 探测）。
- 路径含空格时必须加引号。
- 自动选择顺序：`tavily` → `exa` → `firecrawl` → `jina` → `deepseek`，取第一个已配置 Key 的平台；
  如想改变自动选择，在环境变量中设 `WEB_SEARCH_PROVIDER=<平台名>`。
- 查询词支持 `site:` / `-site:` / `lang:` / `after:` / `before:` / 精确短语等限定符，
  例如 `DeepSeek site:github.com lang:en after:2025`。
- 搜索结果的摘要通常已经够用；需要细节时再加 `--content`，或对具体链接执行 `fetch`（更省流量与费用）。
- `fetch` 返回完整正文；长网页内容较长、输出量较大，不要一次抓很多无关网页。

## 平台与 Key

| 平台 | 参数 | 环境变量 | 能力 |
| --- | --- | --- | --- |
| Tavily | `--tavily` | `TAVILY_API_KEY` | 搜索 / Extract 抓取 |
| Exa | `--exa` | `EXA_API_KEY` | 搜索 / Contents 抓取 |
| Firecrawl | `--firecrawl` | `FIRECRAWL_API_KEY` | 搜索 / Scrape 抓取 |
| Jina AI | `--jina` | `JINA_API_KEY` | 搜索 / Reader 抓取 |
| DeepSeek | `--deepseek` | `DEEPSEEK_API_KEY` | 搜索 / Fetch 抓取 |

自动选择按上表从上到下的顺序进行，不配置平台参数时取第一个已配置 Key 的平台。

## 执行建议

- 先搜索再抓取：从结果中挑 1-3 个可信来源（相关度高、权威站点）交叉确认，不要只看一条。
- 把结果提炼成用户能看懂的回答并附上来源链接，不要原样粘贴大段网页。
- 涉及价格、版本、发布日期等易变信息时，注明信息时间。
- 不要在命令里写 API Key，也不要向用户索要 Key 明文。
- 平台只是搜索/抓取的通道：同一问题用哪个平台都行，某个平台报额度/限流错误时，
  换一个已配置的平台（或直接改用 `WEB_SEARCH_PROVIDER` 指定的平台）重试。

## 错误处理

- `没有检测到任何搜索平台的 API Key`：告诉用户在环境变量（或 bash 工具环境变量）中配置上表任一 Key，
  配置后需重启 AniaBot 生效，重启后再跑 `providers` 确认。
- 明确提示 `未设置 XXX_API_KEY`：说明用 `--<平台>` 指定了平台但没配对应 Key，改配 Key 或去掉平台参数。
- 401 / 402 / 429：分别表示 Key 无效、余额/额度不足、触发限流，如实说明即可，不要反复重试；可换平台。
- 抓取 404：目标页面无法提取内容，换其他来源或让用户提供可访问的链接。
- 找不到 Python：说明宿主机缺少 Python 3，可改用其他方式或请管理员安装。

> 本技能是内置技能：不能删除、不能禁用；如需接入其他搜索平台，可自行新增外置 skill 和 CLI，
> 或在 `webctl.py` 的 `PROVIDERS` 注册表中扩展。
