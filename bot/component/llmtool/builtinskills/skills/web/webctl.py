#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""AniaBot 内置 web 技能的联网 CLI：网页搜索与正文抓取。

用法::

    python3 webctl.py search --deepseek "查询词" [--count 10] [--content] [--json]
    python3 webctl.py fetch  --deepseek "https://example.com/article" [--offset 0]

平台通过 ``--<平台名>`` 选择（放在子命令前后均可，当前支持 ``--deepseek``）。
新增平台时，实现 ``xxx_search`` / ``xxx_fetch`` 并注册到 ``PROVIDERS``，
命令行参数会自动生成，无需改动解析逻辑。

环境变量：
    DEEPSEEK_API_KEY   DeepSeek API Key（必填）
    DEEPSEEK_BASE_URL  可选，默认 https://api.deepseek.com

仅依赖 Python 标准库（urllib），Python 3.8+。
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

DEFAULT_TIMEOUT = 30          # 单次请求超时（秒）
DEFAULT_MAX_CHARS = 8000      # fetch 单次返回正文上限（字符）
DEFAULT_SEARCH_COUNT = 10     # 搜索默认结果数


class CLIError(Exception):
    """可直接展示给用户的错误。"""


def _configure_stdout():
    """Windows 控制台默认不是 UTF-8，避免中文输出直接抛异常。"""
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass


def _http_hint(status, detail):
    hints = {
        400: "请求参数错误",
        401: "API Key 无效或未提供",
        402: "账户余额不足",
        403: "请求被安全策略拒绝",
        404: "页面无法抓取（URL 无效、被站点策略阻止或无可提取内容）",
        408: "读取请求体超时",
        413: "请求体过大",
        429: "触发限流，请稍后重试",
        500: "服务端内部错误",
        504: "下游请求超时",
    }
    message = hints.get(status, "HTTP %d" % status)
    if detail:
        message += "：" + detail
    return message


def _post_json(url, payload, api_key, timeout=DEFAULT_TIMEOUT):
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        method="POST",
        headers={
            "Authorization": "Bearer " + api_key,
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": "AniaBot-web-skill/1.0",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        detail = ""
        try:
            parsed = json.loads(exc.read().decode("utf-8", "replace"))
            error = parsed.get("error") or {}
            detail = (error.get("message") or "").strip()
        except Exception:  # noqa: BLE001 - 错误体不是 JSON 时忽略
            detail = ""
        raise CLIError(_http_hint(exc.code, detail))
    except urllib.error.URLError as exc:
        raise CLIError("网络请求失败：%s" % exc.reason)
    except TimeoutError:
        raise CLIError("请求超时（%d 秒），请稍后重试" % timeout)

    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        raise CLIError("服务端返回了无法解析的内容（请稍后重试）")


# ── DeepSeek 平台实现 ──────────────────────────────────────────────
# 文档：https://api-docs.deepseek.com/zh-cn/guides/search
#   POST /v1/tools/search  {query, count, need_content, ori_question}
#   POST /v1/tools/fetch   {url}


def _deepseek_api_key():
    key = os.environ.get("DEEPSEEK_API_KEY", "").strip()
    if not key:
        raise CLIError(
            "未设置 DEEPSEEK_API_KEY 环境变量。请在运行 AniaBot 的环境变量中配置，"
            "或在面板「AI 对话 · 工具 → Bash 环境变量」中追加一行 DEEPSEEK_API_KEY=sk-..."
        )
    return key


def _deepseek_base_url():
    base = os.environ.get("DEEPSEEK_BASE_URL", "").strip()
    return (base or "https://api.deepseek.com").rstrip("/")


def deepseek_search(query, count, ori_question, need_content):
    payload = {"query": query, "count": count, "need_content": need_content}
    if ori_question:
        payload["ori_question"] = ori_question
    data = _post_json(
        _deepseek_base_url() + "/v1/tools/search",
        payload,
        _deepseek_api_key(),
    )
    return data.get("results") or []


def deepseek_fetch(url):
    data = _post_json(
        _deepseek_base_url() + "/v1/tools/fetch",
        {"url": url},
        _deepseek_api_key(),
    )
    return data.get("title") or "", data.get("content") or ""


# 平台注册表：key 为命令行参数名（--deepseek），value 为 (search, fetch)。
PROVIDERS = {
    "deepseek": (deepseek_search, deepseek_fetch),
}


# ── 输出格式化 ────────────────────────────────────────────────────


def format_search(results, max_chars=DEFAULT_MAX_CHARS):
    if not results:
        return "没有找到相关结果。可以换关键词、放宽限定符（如去掉 site:），或稍后重试。"
    lines = []
    used = 0
    shown = 0
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
            meta.append(str(item["date"])[:10])
        score = item.get("rerank_score")
        if isinstance(score, (int, float)):
            meta.append("相关度 %.1f" % score)
        if meta:
            block.append("   " + " · ".join(meta))
        summary = (item.get("summary") or item.get("snippet") or "").strip()
        if summary:
            block.append("   摘要: " + summary)
        content = (item.get("content") or "").strip()
        if content:
            remaining = max_chars - used - 200  # 预留标题/元信息占用的字符
            if remaining <= 0:
                break
            if len(content) > remaining:
                content = content[:remaining] + "…（正文已截断）"
            block.append("   正文: " + content)
        text = "\n".join(block)
        # 单次输出总量受 max_chars 限制，避免超长正文占满模型上下文
        if shown and used + len(text) + 1 > max_chars:
            break
        lines.extend(block)
        used += len(text) + 1
        shown = index
    out = "\n".join(lines).rstrip()
    if shown < len(results):
        out += "\n\n...（共 %d 条结果，已显示前 %d 条；可减小 --count 或缩小查询范围）" % (len(results), shown)
    return out


def format_fetch(title, content, offset, max_chars):
    title = (title or "").strip() or "(无标题)"
    content = content or ""
    total = len(content)
    start = max(0, int(offset or 0))
    if start and start >= total:
        return "offset=%d 超出内容长度（共 %d 字），没有更多内容" % (start, total)
    max_chars = max(200, int(max_chars or DEFAULT_MAX_CHARS))
    chunk = content[start:start + max_chars]
    out = "# " + title + "\n\n" + chunk
    if start + max_chars < total:
        out += "\n\n...(内容未完，共 %d 字；用 --offset %d 继续读取)" % (total, start + max_chars)
    return out


# ── 命令行入口 ────────────────────────────────────────────────────


def build_parser():
    parser = argparse.ArgumentParser(
        prog="webctl.py",
        description="AniaBot 内置联网技能 CLI：网页搜索与正文抓取",
    )
    # 平台开关按 PROVIDERS 自动生成（--deepseek / --xxx）
    for name in PROVIDERS:
        parser.add_argument("--" + name, action="store_true", dest="provider_" + name,
                            help="使用 %s 搜索/抓取平台" % name)

    sub = parser.add_subparsers(dest="command", required=True)

    search = sub.add_parser("search", help="搜索网页")
    search.add_argument("query", help="搜索词，支持 site:/lang:/after: 等限定符")
    search.add_argument("--count", type=int, default=DEFAULT_SEARCH_COUNT,
                        help="结果数量（1-50，默认 %d）" % DEFAULT_SEARCH_COUNT)
    search.add_argument("--content", action="store_true", help="同时返回网页正文（更耗流量与上下文）")
    search.add_argument("--ori-question", default="", help="触发本次搜索的原始问题，帮助理解查询意图")
    search.add_argument("--json", action="store_true", help="输出原始 JSON（供脚本处理）")

    fetch = sub.add_parser("fetch", help="抓取单个网页正文（Markdown）")
    fetch.add_argument("url", help="http/https 网页链接")
    fetch.add_argument("--offset", type=int, default=0, help="从该字符位置继续读取（长文分页）")
    fetch.add_argument("--max-chars", type=int, default=DEFAULT_MAX_CHARS,
                       help="单次返回的最大字符数（默认 %d）" % DEFAULT_MAX_CHARS)
    fetch.add_argument("--json", action="store_true", help="输出原始 JSON（供脚本处理）")

    # 允许 --deepseek 等平台开关写在子命令之后
    for name in PROVIDERS:
        search.add_argument("--" + name, action="store_true", dest="provider_sub_" + name,
                            help=argparse.SUPPRESS)
        fetch.add_argument("--" + name, action="store_true", dest="provider_sub_" + name,
                           help=argparse.SUPPRESS)
    return parser


def _resolve_provider(args):
    for name in PROVIDERS:
        if getattr(args, "provider_" + name, False) or getattr(args, "provider_sub_" + name, False):
            return name
    return ""


def main(argv=None):
    _configure_stdout()
    parser = build_parser()
    args = parser.parse_args(argv)

    provider = _resolve_provider(args)
    if not provider:
        flags = "/".join("--" + name for name in PROVIDERS)
        parser.error("请用 %s 指定搜索/抓取平台（当前支持：%s）" % (flags, "、".join(PROVIDERS)))
    search_fn, fetch_fn = PROVIDERS[provider]

    try:
        if args.command == "search":
            query = args.query.strip()
            if not query:
                raise CLIError("搜索词不能为空")
            if len(query) > 128:
                raise CLIError("搜索词过长（最多 128 个字符）")
            count = max(1, min(50, args.count))
            results = search_fn(query, count, args.ori_question.strip()[:128], args.content)
            if args.json:
                print(json.dumps({"results": results}, ensure_ascii=False, indent=2))
            else:
                print(format_search(results))
        else:
            url = args.url.strip()
            if not url.startswith(("http://", "https://")):
                raise CLIError("URL 必须以 http:// 或 https:// 开头")
            title, content = fetch_fn(url)
            if args.json:
                print(json.dumps({"title": title, "content": content}, ensure_ascii=False))
            else:
                print(format_fetch(title, content, args.offset, args.max_chars))
    except CLIError as exc:
        print("错误: %s" % exc, file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("已取消", file=sys.stderr)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
