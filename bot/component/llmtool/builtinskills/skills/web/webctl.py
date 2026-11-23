#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""AniaBot 内置 web 技能的联网 CLI：网页搜索与正文抓取。

用法::

    python3 webctl.py search "查询词" [--count 10] [--content] [--json] [--<平台>]
    python3 webctl.py fetch  "https://example.com/article" [--json] [--<平台>]
    python3 webctl.py providers [--json]

平台通过 ``--<平台名>`` 选择（放在子命令前后均可）：deepseek / tavily / exa /
firecrawl / jina。不写平台参数时自动检测已配置的 API Key 环境变量，按
``PROVIDERS`` 的顺序选用第一个可用的平台；环境变量 ``WEB_SEARCH_PROVIDER``
可指定自动选择时优先使用的平台。

本 CLI 只负责取数与格式化输出，不做长度截断/分页——长输出交由框架层的
工具输出与上下文管理处理。

环境变量：
    DEEPSEEK_API_KEY     DeepSeek Search / Fetch
    TAVILY_API_KEY       Tavily Search / Extract
    EXA_API_KEY          Exa Search / Contents
    FIRECRAWL_API_KEY    Firecrawl Search / Scrape
    JINA_API_KEY         Jina AI Search / Reader
    WEB_SEARCH_PROVIDER  可选，自动选择时优先使用的平台名（须已配置对应 Key）
    DEEPSEEK_BASE_URL    可选，默认 https://api.deepseek.com

仅依赖 Python 标准库（urllib），Python 3.8+。新增平台时实现
``xxx_search`` / ``xxx_fetch`` 并注册到 ``PROVIDERS``，命令行参数、环境变量
自动检测与 ``providers`` 列表都会自动生成，无需改动解析逻辑。
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from collections import namedtuple

DEFAULT_TIMEOUT = 30          # 搜索类请求超时（秒）
FETCH_TIMEOUT = 60            # 抓取类请求超时（秒），正文提取通常更慢
DEFAULT_SEARCH_COUNT = 10     # 搜索默认结果数
USER_AGENT = "AniaBot-web-skill/2.0"

# 提示信息里复用的配置说明
_CONFIG_HINT = "请在运行 AniaBot 的环境变量中配置，或在面板「AI 对话 · 工具 → 环境变量」中追加一行。"


class CLIError(Exception):
    """可直接展示给用户的错误。"""


def _configure_stdout():
    """Windows 控制台默认不是 UTF-8，避免中文输出直接抛异常。"""
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass


# ── HTTP 请求与错误处理 ────────────────────────────────────────────


def _http_hint(status, detail):
    hints = {
        400: "请求参数错误",
        401: "API Key 无效或未提供",
        402: "账户余额/额度不足",
        403: "请求被安全策略拒绝",
        404: "页面无法抓取（URL 无效、被站点策略阻止或无可提取内容）",
        408: "读取请求体超时",
        413: "请求体过大",
        422: "请求参数或 URL 无法处理",
        429: "触发限流或额度用尽，请稍后重试",
        500: "服务端内部错误",
        502: "服务端网关错误",
        504: "下游请求超时",
    }
    message = hints.get(status, "HTTP %d" % status)
    if detail:
        message += "：" + detail
    return message


def _stringify_detail(value):
    """把各平台五花八门的错误字段统一成一句可展示文本。"""
    if isinstance(value, str):
        return value.strip()[:300]
    if isinstance(value, dict):
        for key in ("message", "detail", "error", "msg"):
            detail = _stringify_detail(value.get(key))
            if detail:
                return detail
    if isinstance(value, list) and value:
        return _stringify_detail(value[0])
    return ""


def _error_detail(raw_text):
    """从错误响应体（JSON 或纯文本）中提取可展示的错误说明。"""
    text = (raw_text or "").strip()
    if not text:
        return ""
    try:
        parsed = json.loads(text)
    except (ValueError, TypeError):
        return text[:300]
    if isinstance(parsed, dict):
        for key in ("error", "message", "detail", "msg", "error_message", "errorMessage"):
            detail = _stringify_detail(parsed.get(key))
            if detail:
                return detail
        data = parsed.get("data")
        if isinstance(data, (dict, list)):
            detail = _stringify_detail(data)
            if detail:
                return detail
    return ""


def _request(url, payload=None, headers=None, timeout=DEFAULT_TIMEOUT):
    """发起请求并解析 JSON：``payload`` 为 None 时 GET，否则 POST JSON。"""
    body = None
    if payload is not None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    request_headers = {
        "Accept": "application/json",
        "User-Agent": USER_AGENT,
    }
    if body is not None:
        request_headers["Content-Type"] = "application/json"
    if headers:
        request_headers.update(headers)
    request = urllib.request.Request(
        url,
        data=body,
        method="POST" if body is not None else "GET",
        headers=request_headers,
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        try:
            detail = _error_detail(exc.read().decode("utf-8", "replace"))
        except Exception:  # noqa: BLE001 - 错误体读取失败时保留状态码提示
            detail = ""
        raise CLIError(_http_hint(exc.code, detail))
    except urllib.error.URLError as exc:
        raise CLIError("网络请求失败：%s" % exc.reason)
    except TimeoutError:
        raise CLIError("请求超时（%d 秒），请稍后重试" % timeout)

    if not raw.strip():
        raise CLIError("服务端返回了空响应（请稍后重试）")
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        raise CLIError("服务端返回了无法解析的内容（请稍后重试）")


def _env_value(name):
    return os.environ.get(name, "").strip()


def _require_key(env_name, label):
    value = _env_value(env_name)
    if not value:
        raise CLIError("未设置 %s 环境变量（%s 需要）。%s" % (env_name, label, _CONFIG_HINT))
    return value


def _bearer(token):
    return {"Authorization": "Bearer " + token}


def _result(title, url, summary="", content="", site_name="", date="", score=None):
    """统一各平台的搜索结果结构，便于格式化与 ``--json`` 输出。"""
    return {
        "title": (title or "").strip(),
        "url": (url or "").strip(),
        "summary": (summary or "").strip(),
        "content": (content or "").strip(),
        "site_name": (site_name or "").strip(),
        "date": (date or "").strip(),
        "score": score if isinstance(score, (int, float)) else None,
    }


# ── DeepSeek 平台实现 ──────────────────────────────────────────────
# 文档：https://api-docs.deepseek.com/zh-cn/guides/search
#   POST /v1/tools/search  {query, count, need_content, ori_question}
#   POST /v1/tools/fetch   {url}


def _deepseek_base_url():
    base = _env_value("DEEPSEEK_BASE_URL")
    return (base or "https://api.deepseek.com").rstrip("/")


def deepseek_search(query, count, need_content, ori_question=""):
    payload = {"query": query, "count": count, "need_content": need_content}
    if ori_question:
        payload["ori_question"] = ori_question
    data = _request(
        _deepseek_base_url() + "/v1/tools/search",
        payload,
        _bearer(_require_key("DEEPSEEK_API_KEY", "DeepSeek")),
    )
    results = []
    for item in data.get("results") or []:
        results.append(_result(
            item.get("title"), item.get("url"),
            summary=item.get("summary") or item.get("snippet"),
            content=item.get("content") if need_content else "",
            site_name=item.get("site_name"),
            date=item.get("date"),
            score=item.get("rerank_score"),
        ))
    return results


def deepseek_fetch(url):
    data = _request(
        _deepseek_base_url() + "/v1/tools/fetch",
        {"url": url},
        _bearer(_require_key("DEEPSEEK_API_KEY", "DeepSeek")),
    )
    return data.get("title") or "", data.get("content") or ""


# ── Tavily 平台实现 ────────────────────────────────────────────────
# 文档：https://docs.tavily.com/documentation/api-reference/endpoint/search
#   POST https://api.tavily.com/search   {query, max_results, include_raw_content}
#   POST https://api.tavily.com/extract  {urls: [url]}


def tavily_search(query, count, need_content, ori_question=""):
    payload = {"query": query, "max_results": max(1, min(20, count))}
    if need_content:
        payload["include_raw_content"] = True
    data = _request(
        "https://api.tavily.com/search",
        payload,
        _bearer(_require_key("TAVILY_API_KEY", "Tavily")),
    )
    results = []
    for item in data.get("results") or []:
        results.append(_result(
            item.get("title"), item.get("url"),
            summary=item.get("content"),
            content=item.get("raw_content") if need_content else "",
            date=item.get("published_date"),
            score=item.get("score"),
        ))
    return results


def tavily_fetch(url):
    data = _request(
        "https://api.tavily.com/extract",
        {"urls": [url]},
        _bearer(_require_key("TAVILY_API_KEY", "Tavily")),
        timeout=FETCH_TIMEOUT,
    )
    results = data.get("results") or []
    if not results:
        failed = data.get("failed_results") or []
        detail = _stringify_detail(failed[0].get("error")) if failed and isinstance(failed[0], dict) else ""
        raise CLIError("Tavily 无法抓取该页面" + ("：" + detail if detail else ""))
    item = results[0]
    return item.get("title") or "", item.get("raw_content") or ""


# ── Exa 平台实现 ──────────────────────────────────────────────────
# 文档：https://docs.exa.ai/reference/search
#   POST https://api.exa.ai/search    {query, numResults, contents: {text}}
#   POST https://api.exa.ai/contents  {urls: [url], text: {maxCharacters}}


def _exa_headers():
    return {"x-api-key": _require_key("EXA_API_KEY", "Exa")}


def exa_search(query, count, need_content, ori_question=""):
    payload = {
        "query": query,
        "numResults": max(1, min(100, count)),
        "contents": {"text": {"maxCharacters": 8000 if need_content else 1000}},
    }
    data = _request("https://api.exa.ai/search", payload, _exa_headers())
    results = []
    for item in data.get("results") or []:
        text = (item.get("text") or "").strip()
        results.append(_result(
            item.get("title"), item.get("url"),
            summary="" if need_content else text,
            content=text if need_content else "",
            date=item.get("publishedDate") or item.get("published_date"),
            score=item.get("score"),
        ))
    return results


def exa_fetch(url):
    payload = {"urls": [url], "text": {"maxCharacters": 50000}}
    data = _request(
        "https://api.exa.ai/contents",
        payload,
        _exa_headers(),
        timeout=FETCH_TIMEOUT,
    )
    results = data.get("results") or []
    if not results:
        statuses = data.get("statuses") or []
        detail = ""
        if statuses and isinstance(statuses[0], dict):
            error = statuses[0].get("error")
            detail = _stringify_detail(error)
            if not detail and isinstance(error, dict):
                # Exa 的失败体：{httpStatusCode: 404, tag: "CRAWL_NOT_FOUND"}
                parts = []
                if error.get("tag"):
                    parts.append(str(error["tag"]))
                if error.get("httpStatusCode"):
                    parts.append("HTTP %s" % error["httpStatusCode"])
                detail = " ".join(parts)
        raise CLIError("Exa 无法抓取该页面" + ("：" + str(detail) if detail else ""))
    item = results[0]
    return item.get("title") or "", item.get("text") or ""


# ── Firecrawl 平台实现 ─────────────────────────────────────────────
# 文档：https://docs.firecrawl.dev/features/search
# v1 API 已于 2026-12-10 关停（返回 HTTP 410），这里只使用 v2。
#   POST https://api.firecrawl.dev/v2/search  {query, limit, scrapeOptions}
#   POST https://api.firecrawl.dev/v2/scrape   {url, formats: ["markdown"]}


def _firecrawl_headers():
    return _bearer(_require_key("FIRECRAWL_API_KEY", "Firecrawl"))


def _unwrap_firecrawl(data, action):
    if isinstance(data, dict) and data.get("success") is False:
        detail = _stringify_detail(data.get("error")) or "未知错误"
        raise CLIError("Firecrawl %s失败：%s" % (action, detail))
    return data


def firecrawl_search(query, count, need_content, ori_question=""):
    payload = {"query": query, "limit": max(1, min(100, count))}
    if need_content:
        payload["scrapeOptions"] = {"formats": ["markdown"]}
    data = _unwrap_firecrawl(
        _request("https://api.firecrawl.dev/v2/search", payload, _firecrawl_headers(), timeout=FETCH_TIMEOUT),
        "搜索",
    )
    container = data.get("data") if isinstance(data, dict) else None
    items = []
    if isinstance(container, dict):
        # v2：搜索结果在 data.web；data.news 仅在指定 sources 时出现
        for key in ("web", "news"):
            values = container.get(key)
            if isinstance(values, list):
                items.extend(values)
    results = []
    for item in items:
        if not isinstance(item, dict):
            continue
        results.append(_result(
            item.get("title"), item.get("url"),
            summary="" if need_content else (item.get("description") or item.get("snippet")),
            content=item.get("markdown") if need_content else "",
            date=item.get("publishedDate") or item.get("date"),
        ))
    return results


def firecrawl_fetch(url):
    payload = {"url": url, "formats": ["markdown"]}
    data = _unwrap_firecrawl(
        _request("https://api.firecrawl.dev/v2/scrape", payload, _firecrawl_headers(), timeout=FETCH_TIMEOUT),
        "抓取",
    )
    page = data.get("data") or {}
    metadata = page.get("metadata") or {}
    status_code = metadata.get("statusCode")
    if isinstance(status_code, int) and status_code >= 400:
        # Firecrawl 对目标站点的 4xx/5xx 仍可能返回成功状态 + 错误页正文，按抓取失败处理
        raise CLIError("Firecrawl 抓取失败：目标页面返回 HTTP %d" % status_code)
    return metadata.get("title") or "", page.get("markdown") or ""


# ── Jina AI 平台实现 ───────────────────────────────────────────────
# 文档：https://jina.ai/reader/
#   GET https://s.jina.ai/?q=<query>&count=<n>   搜索（Accept: application/json）
#   GET https://r.jina.ai/<url>                  抓取正文（Accept: application/json）


def _jina_headers(with_content):
    headers = _bearer(_require_key("JINA_API_KEY", "Jina AI"))
    if not with_content:
        # 搜索时只要标题/链接/摘要，不需要每个结果的整页正文（更省 token）
        headers["X-Respond-With"] = "no-content"
    return headers


def _raise_jina_error(data):
    if not isinstance(data, dict):
        return
    code = data.get("code")
    if isinstance(code, int) and code >= 400:
        detail = _stringify_detail(data.get("message")) or _stringify_detail(data.get("error"))
        raise CLIError("Jina AI 请求失败" + ("：" + detail if detail else "（code=%s）" % code))


def jina_search(query, count, need_content, ori_question=""):
    params = urllib.parse.urlencode({"q": query, "count": max(1, min(20, count))})
    data = _request(
        "https://s.jina.ai/?" + params,
        headers=_jina_headers(need_content),
        timeout=FETCH_TIMEOUT,
    )
    _raise_jina_error(data)
    items = data.get("data") or []
    if isinstance(items, dict):
        items = [items]
    results = []
    for item in items:
        if not isinstance(item, dict):
            continue
        results.append(_result(
            item.get("title"), item.get("url"),
            summary=item.get("description"),
            content=item.get("content") if need_content else "",
            date=item.get("timestamp") or item.get("date"),
        ))
    return results


def jina_fetch(url):
    data = _request("https://r.jina.ai/" + url, headers=_jina_headers(True), timeout=FETCH_TIMEOUT)
    _raise_jina_error(data)
    page = data.get("data") or {}
    if isinstance(page, list):
        page = page[0] if page else {}
    return page.get("title") or "", page.get("content") or ""


# 平台注册表：key 为命令行参数名（--tavily），value 为 (label, env, search, fetch)。
# 字典顺序即自动选择的优先级：未指定平台时取第一个已配置对应环境变量的平台。
# DeepSeek 是原有平台，放在最后作为兼容回退。
Provider = namedtuple("Provider", "label env search fetch")

PROVIDERS = {
    "tavily": Provider("Tavily", "TAVILY_API_KEY", tavily_search, tavily_fetch),
    "exa": Provider("Exa", "EXA_API_KEY", exa_search, exa_fetch),
    "firecrawl": Provider("Firecrawl", "FIRECRAWL_API_KEY", firecrawl_search, firecrawl_fetch),
    "jina": Provider("Jina AI", "JINA_API_KEY", jina_search, jina_fetch),
    "deepseek": Provider("DeepSeek", "DEEPSEEK_API_KEY", deepseek_search, deepseek_fetch),
}


# ── 平台选择（显式参数 / 环境变量自动检测）─────────────────────────


def configured_providers():
    """按优先级返回已配置 API Key 的平台名列表。"""
    return [name for name, provider in PROVIDERS.items() if _env_value(provider.env)]


def preferred_provider_name():
    return _env_value("WEB_SEARCH_PROVIDER").lower()


def _resolve_provider(args):
    explicit = [
        name for name in PROVIDERS
        if getattr(args, "provider_" + name, False) or getattr(args, "provider_sub_" + name, False)
    ]
    if len(explicit) > 1:
        raise CLIError("一次只能指定一个平台，收到：%s" % "、".join("--" + name for name in explicit))
    if explicit:
        name = explicit[0]
        if not _env_value(PROVIDERS[name].env):
            raise CLIError("已指定 --%s，但未设置 %s 环境变量。%s" % (
                name, PROVIDERS[name].env, _CONFIG_HINT))
        return name

    preferred = preferred_provider_name()
    if preferred:
        if preferred not in PROVIDERS:
            raise CLIError("WEB_SEARCH_PROVIDER=%s 不是支持的平台（可选：%s）" % (
                preferred, "、".join(PROVIDERS)))
        if not _env_value(PROVIDERS[preferred].env):
            raise CLIError("WEB_SEARCH_PROVIDER 指定了 %s，但未设置其环境变量 %s。%s" % (
                preferred, PROVIDERS[preferred].env, _CONFIG_HINT))
        return preferred

    configured = configured_providers()
    if configured:
        return configured[0]

    lines = ["没有检测到任何搜索平台的 API Key，请至少配置一个环境变量" +
             "（运行 AniaBot 的系统环境，或面板「AI 对话 · 工具 → 环境变量」）："]
    for name, provider in PROVIDERS.items():
        lines.append("  %s=...  （%s，--%s）" % (provider.env, provider.label, name))
    raise CLIError("\n".join(lines))


# ── 输出格式化 ────────────────────────────────────────────────────


def _short_date(value):
    """日期展示：ISO 时间戳截到 10 位，其他格式（如 ``Oct 17, 2026``）原样保留。"""
    text = str(value or "").strip()
    if len(text) > 10 and text[4:5] == "-" and text[7:8] == "-":
        return text[:10]
    return text


def format_search(results):
    if not results:
        return "没有找到相关结果。可以换关键词、放宽限定符（如去掉 site:），或稍后重试。"
    blocks = []
    for index, item in enumerate(results, 1):
        block = []
        title = (item.get("title") or "(无标题)").strip()
        url = (item.get("url") or "").strip()
        block.append("%d. %s" % (index, title))
        if url:
            block.append("   URL: %s" % url)
        meta = []
        if item.get("site_name"):
            meta.append(str(item["site_name"]))
        if item.get("date"):
            meta.append(_short_date(item["date"]))
        score = item.get("score")
        if isinstance(score, (int, float)):
            meta.append("相关度 %.2f" % score)
        if meta:
            block.append("   " + " · ".join(meta))
        summary = (item.get("summary") or "").strip()
        if summary:
            block.append("   摘要: " + summary)
        content = (item.get("content") or "").strip()
        if content:
            block.append("   正文: " + content)
        blocks.append("\n".join(block))
    return "\n\n".join(blocks).rstrip()


def format_fetch(title, content):
    title = (title or "").strip() or "(无标题)"
    return "# " + title + "\n\n" + (content or "")


# ── 命令行入口 ────────────────────────────────────────────────────


def _add_provider_flags(parser, hidden=False):
    for name, provider in PROVIDERS.items():
        parser.add_argument(
            "--" + name,
            action="store_true",
            # 顶层与子命令各注册一次，两个 dest 不同，最后合并判断
            dest=("provider_sub_" if hidden else "provider_") + name,
            help=argparse.SUPPRESS if hidden else "使用 %s 平台（环境变量 %s）" % (provider.label, provider.env),
        )


def build_parser():
    parser = argparse.ArgumentParser(
        prog="webctl.py",
        description="AniaBot 内置联网技能 CLI：网页搜索与正文抓取（支持多个平台，"
                    "不指定平台时自动检测已配置的 API Key）",
    )
    # 平台开关按 PROVIDERS 自动生成（--tavily / --exa / ...）
    _add_provider_flags(parser)

    sub = parser.add_subparsers(dest="command", required=True)

    search = sub.add_parser("search", help="搜索网页")
    search.add_argument("query", help="搜索词，支持 site:/lang:/after: 等限定符")
    search.add_argument("--count", type=int, default=DEFAULT_SEARCH_COUNT,
                        help="结果数量（1-50，默认 %d；部分平台上限更小）" % DEFAULT_SEARCH_COUNT)
    search.add_argument("--content", action="store_true",
                        help="同时返回网页正文（更耗流量、费用与上下文）")
    search.add_argument("--ori-question", default="", help="触发本次搜索的原始问题，帮助理解查询意图（仅 DeepSeek）")
    search.add_argument("--json", action="store_true", help="输出原始 JSON（供脚本处理）")

    fetch = sub.add_parser("fetch", help="抓取单个网页正文（Markdown）")
    fetch.add_argument("url", help="http/https 网页链接")
    fetch.add_argument("--json", action="store_true", help="输出原始 JSON（供脚本处理）")

    providers = sub.add_parser("providers", help="查看各平台的 Key 配置情况与自动选择结果")
    providers.add_argument("--json", action="store_true", help="输出原始 JSON（供脚本处理）")

    # 允许 --tavily 等平台开关写在子命令之后
    _add_provider_flags(search, hidden=True)
    _add_provider_flags(fetch, hidden=True)
    return parser


def _cmd_providers(args):
    configured = configured_providers()
    preferred = preferred_provider_name()
    auto = ""
    issue = ""
    if preferred:
        if preferred not in PROVIDERS:
            issue = "WEB_SEARCH_PROVIDER=%s 不是支持的平台" % preferred
        elif preferred not in configured:
            issue = "WEB_SEARCH_PROVIDER=%s 未配置环境变量 %s" % (
                preferred, PROVIDERS[preferred].env)
        else:
            auto = preferred
    if not auto and configured:
        auto = configured[0]

    if args.json:
        print(json.dumps({
            "providers": [
                {
                    "name": name,
                    "label": provider.label,
                    "env": provider.env,
                    "configured": name in configured,
                }
                for name, provider in PROVIDERS.items()
            ],
            "configured": configured,
            "preferred": preferred,
            "auto": auto,
            "issue": issue,
        }, ensure_ascii=False, indent=2))
        return 0

    lines = ["搜索平台（自动选择按以下顺序取第一个已配置 Key 的平台）："]
    for index, (name, provider) in enumerate(PROVIDERS.items(), 1):
        status = "已配置" if name in configured else "未配置"
        mark = "→" if name == auto else " "
        lines.append("%s %d. %-10s %s  环境变量 %s" % (mark, index, name, status, provider.env))
    lines.append("")
    if issue:
        lines.append("注意：%s" % issue)
    lines.append("当前自动选择：%s" % (auto or "无（请先配置任一环境变量）"))
    lines.append("可用 --<平台名> 显式指定平台；设置 WEB_SEARCH_PROVIDER 可调整自动选择的优先平台。")
    print("\n".join(lines))
    return 0


def main(argv=None):
    _configure_stdout()
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.command == "providers":
        return _cmd_providers(args)

    try:
        provider = _resolve_provider(args)
        entry = PROVIDERS[provider]
        if args.command == "search":
            query = args.query.strip()
            if not query:
                raise CLIError("搜索词不能为空")
            if len(query) > 128:
                raise CLIError("搜索词过长（最多 128 个字符）")
            count = max(1, min(50, args.count))
            results = entry.search(query, count, args.content, args.ori_question.strip()[:128])
            if args.json:
                print(json.dumps({"provider": provider, "results": results}, ensure_ascii=False, indent=2))
            else:
                print(format_search(results))
        else:
            url = args.url.strip()
            if not url.startswith(("http://", "https://")):
                raise CLIError("URL 必须以 http:// 或 https:// 开头")
            title, content = entry.fetch(url)
            if args.json:
                print(json.dumps({"provider": provider, "title": title, "url": url, "content": content},
                                 ensure_ascii=False))
            else:
                print(format_fetch(title, content))
    except CLIError as exc:
        print("错误: %s" % exc, file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("已取消", file=sys.stderr)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
