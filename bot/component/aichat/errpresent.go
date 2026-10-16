package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
)

// 用户可见的错误文案：把错误链提炼成可直接发给用户的一段中文。
//
// SDK 的 *openai.Error / *anthropic.Error 自带的 Error() 文本包含完整请求 URL 与
// 原始响应 JSON（部分网关会把密钥回显在 URL 或响应体里），因此用户可见文案只取
// 结构化字段（状态码、提供方 message/code），不复用原始错误全文；完整原文仍由
// 调用方记入日志供排查。所有走这条路的文本都会先脱敏（掩去疑似密钥/token）。

// maxUserErrRunes 用户可见错误文案的最大长度（rune），超出截断。
const maxUserErrRunes = 300

// UserErrorText 把 LLM 请求链路上的错误转换成用户可读的一段中文：识别 openai /
// anthropic 两种 SDK 的 API 错误，给出状态码含义与上游返回的具体原因；其他错误
// （网络错误等）脱敏截断后展示。取消与超时返回对应提示。
func UserErrorText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "AI 响应已被停止"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时，请稍后再试"
	}

	var oaiErr *openai.Error
	if errors.As(err, &oaiErr) && oaiErr.Response != nil {
		return describeLLMHTTPError(oaiErr.StatusCode, oaiErr.Message, oaiErr.Code)
	}
	var antErr *anthropic.Error
	if errors.As(err, &antErr) && antErr.Response != nil {
		return describeLLMHTTPError(antErr.StatusCode, anthropicErrMessage(antErr), "")
	}

	// 兜底：网络错误、SDK 传输错误等非 API 错误
	return "AI 请求失败：" + SafeErrorText(err)
}

// SafeErrorText 通用错误脱敏文案：去掉内部包装前缀、单行化、掩去疑似密钥/token、
// 超长截断。供创建失败等非请求错误场景直接展示错误原因。
func SafeErrorText(err error) string {
	if err == nil {
		return ""
	}
	return maskSensitive(sanitizeOneLine(stripWrapPrefixes(safeErrText(err))))
}

// contextOverflowPattern 上游「输入超出上下文长度」类错误的文本特征。
var contextOverflowPattern = regexp.MustCompile(`(?i)context length|maximum context|too many tokens|input length exceeds|prompt is too long|context_length_exceeded`)

// contextOverflowHint 超限类错误对应的用户可见文案
const contextOverflowHint = "对话内容超出模型的上下文长度限制"

// knownErrPatterns 常见上游错误的关键词 → 简短中文原因。命中时用户只看中文
// 结论（原始英文长文不展示，完整内容仍在日志里）。
var knownErrPatterns = []struct {
	match *regexp.Regexp
	text  string
}{
	// 图片无效/格式不支持（如 DeepSeek: "unsupported image ... webp, png, jpeg, and gif"）
	{regexp.MustCompile(`(?i)unsupported image|invalid image|not a valid image|image.*is invalid`),
		"消息中的图片不受支持或已损坏（仅支持 webp/png/jpeg/gif）"},
	// 上下文/输入超长
	{contextOverflowPattern, contextOverflowHint},
	// 余额不足（部分提供商用 400/403 文本返回）
	{regexp.MustCompile(`(?i)insufficient (balance|quota)|balance is (empty|insufficient)|has exceeded your (quota|credit)`),
		"账户余额不足"},
	// 内容安全拦截
	{regexp.MustCompile(`(?i)content (filter|policy|moderation)|content_management_policy|inappropriate content`),
		"内容被上游安全策略拦截"},
}

// IsContextOverflowError 判断错误是否为「输入超出上下文长度」类：先看 SDK 错误
// 的结构化 message，再兜底扫描错误文本。ChatBot 失败路径用它标记下一轮强制压缩
// （失败请求不更新 usage，历史可能永远不触发常规压缩阈值）。
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	var oaiErr *openai.Error
	if errors.As(err, &oaiErr) && errorTextOverflow(oaiErr.Message) {
		return true
	}
	var antErr *anthropic.Error
	if errors.As(err, &antErr) && errorTextOverflow(anthropicErrMessage(antErr)) {
		return true
	}
	return errorTextOverflow(safeErrText(err))
}

// errorTextOverflow 文本是否命中上下文超限特征。
func errorTextOverflow(text string) bool {
	return text != "" && contextOverflowPattern.MatchString(text)
}

// matchKnownErrPattern 按上游错误文本匹配已知的常见错误，返回简短中文原因；未命中返回空串。
func matchKnownErrPattern(providerMsg string) string {
	if providerMsg == "" {
		return ""
	}
	for _, p := range knownErrPatterns {
		if p.match.MatchString(providerMsg) {
			return p.text
		}
	}
	return ""
}

// describeLLMHTTPError 用结构化字段组装用户可见文案（不经 err.Error()，
// 避免把请求 URL 与原始响应 JSON 展示给用户）。
func describeLLMHTTPError(status int, msg, code string) string {
	var sb strings.Builder
	sb.WriteString("AI 请求失败（HTTP ")
	sb.WriteString(strconv.Itoa(status))
	if hint := statusHint(status); hint != "" {
		sb.WriteString("，")
		sb.WriteString(hint)
	}
	sb.WriteString("）")
	if known := matchKnownErrPattern(msg); known != "" {
		sb.WriteString("：")
		sb.WriteString(known)
		return sb.String()
	}
	if detail := maskSensitive(sanitizeOneLine(msg)); detail != "" {
		sb.WriteString("：")
		sb.WriteString(detail)
	} else if code != "" {
		sb.WriteString("：")
		sb.WriteString(code)
	}
	return sb.String()
}

// statusHint 返回常见 HTTP 状态码的中文含义；空串表示无特定说明。
func statusHint(code int) string {
	switch code {
	case 400:
		return "请求被拒绝，参数或消息格式可能有误"
	case 401:
		return "API 密钥无效或未授权"
	case 402:
		return "账户余额不足"
	case 403:
		return "没有访问权限（密钥被禁用或有地区限制）"
	case 404:
		return "模型或接口地址不存在"
	case 408:
		return "请求超时"
	case 413:
		return "请求内容过长"
	case 422:
		return "请求参数无法处理"
	case 429:
		return "触发限流，请求频率或用量超限，请稍后再试"
	case 529:
		return "服务过载，请稍后再试"
	}
	if code >= 500 {
		return "服务端错误，请稍后再试"
	}
	return ""
}

// anthropicErrMessage 从 anthropic 错误的原始 JSON 中提取上游错误说明
// （响应体形如 {"type":"error","error":{"type":"...","message":"..."}}）。
func anthropicErrMessage(e *anthropic.Error) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(e.RawJSON()), &body); err == nil {
		return body.Error.Message
	}
	return ""
}

// wrapPrefixPattern aichat 内部层层包装错误的固定前缀，对用户只是噪音。
var wrapPrefixPattern = regexp.MustCompile(`^(?:chat execution failed|LLM generation failed(?: \(fallback\))?|LLM stream failed after partial output):\s*`)

// stripWrapPrefixes 去掉错误文本开头的内部包装前缀（可能多层叠加）。
func stripWrapPrefixes(s string) string {
	for {
		next := wrapPrefixPattern.ReplaceAllString(s, "")
		if next == s {
			return s
		}
		s = next
	}
}

var (
	// sk- 开头的常见密钥形态（OpenAI / DeepSeek / 各类中转），保留前缀后几位便于辨认
	skKeyPattern = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{4})[A-Za-z0-9_-]{6,}`)
	// Authorization 头的 Bearer 形态
	bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	// api_key=xxx / token: xxx / secret=xxx 等键值形态
	keyParamPattern = regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|token|secret|key)\s*[=:]\s*)[^\s,&"']{8,}`)
	// 超长 base64/hex 串（可能是密钥、签名、凭据）
	longTokenPattern = regexp.MustCompile(`[A-Za-z0-9+/=_-]{48,}`)
)

// maskSensitive 掩去文本中疑似密钥 / token 的片段。
func maskSensitive(s string) string {
	s = skKeyPattern.ReplaceAllString(s, "$1****")
	s = bearerPattern.ReplaceAllString(s, "${1}****")
	s = keyParamPattern.ReplaceAllString(s, "${1}****")
	s = longTokenPattern.ReplaceAllString(s, "****")
	return s
}

// sanitizeOneLine 把错误文本折叠成单行并截断，避免超长 JSON 刷屏。
func sanitizeOneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > maxUserErrRunes {
		s = string(runes[:maxUserErrRunes]) + "……"
	}
	return s
}

// safeErrText 防御性取错误文本：手工构造的 SDK 错误可能缺 Request/Response 字段
// 导致其 Error() 空指针 panic，这里兜底避免影响正常回复流程。
func safeErrText(err error) (s string) {
	defer func() {
		if recover() != nil {
			s = "LLM 请求失败"
		}
	}()
	return err.Error()
}
