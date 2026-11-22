package pluginaichat

import (
	"crypto/rand"
	"encoding/hex"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"golang.org/x/net/http/httpguts"
)

// 自定义请求头值支持的会话占位符：按会话（群聊/私聊/全局请求）替换为实际值，
// 让同一份配置能为每个会话生成各自的请求头（如网关侧按会话区分缓存/计费）。
//
//	${session}       会话实例 token：每个会话唯一，新建会话后换新
//	${session_id}    会话目标 ID（群号 / 对方 ID，含平台前缀）
//	${session_type}  group / friend / panel（面板试聊）/ global（无会话请求）
const (
	headerPlaceholderSession     = "${session}"
	headerPlaceholderSessionID   = "${session_id}"
	headerPlaceholderSessionType = "${session_type}"
)

// headerGlobalValue 无会话请求（备用识图 / 向量化）的占位符取值。
const headerGlobalValue = "global"

// parseHeaderLines 解析自定义请求头配置（plugin.ai_chat_bot.headers），
// 支持 "Name: Value" 与 "Name=Value" 两种写法：按行内最先出现的分隔符切分，
// 值中可自由包含另一种字符（如 "Authorization: Bearer a=b"）。空行忽略；
// 缺分隔符、名字为空或名字/值含非法字符（net/http 会在发请求时拒绝，坏配置
// 会拖垮全部 LLM 请求）的行跳过，并以 1 基行号返回给调用方记录告警（只记
// 行号不记内容，避免把误填的凭据写进日志）。名字统一规范化
// （X-Custom-Token），大小写不同视为同一条，重复以最后一行为准。
func parseHeaderLines(lines []string) (headers map[string]string, invalidLines []int) {
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		idx := strings.IndexAny(line, ":=")
		if idx <= 0 {
			invalidLines = append(invalidLines, i+1)
			continue
		}
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			invalidLines = append(invalidLines, i+1)
			continue
		}
		if headers == nil {
			headers = make(map[string]string, len(lines))
		}
		// 规范化名字：与 net/http 的 Set 行为一致，也保证大小写不同的重复
		// 名字合并为一条（map 遍历顺序随机，不合并会让最终生效值不确定）
		headers[textproto.CanonicalMIMEHeaderKey(name)] = value
	}
	return headers, invalidLines
}

// headerNames 返回请求头名字的升序列表（日志用，避免把凭据值写进日志）。
func headerNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// headerVars 自定义请求头占位符的取值。会话级请求由 headerScope 生成，
// 无会话的全局请求（备用识图 / 向量化）统一取 headerGlobalValue。
type headerVars struct {
	session     string // 会话实例 token：每个会话唯一，新建会话（#新对话）后换新
	sessionID   string // 会话目标 ID（群号 / 对方 ID，含平台前缀）
	sessionType string // group / friend / panel / global
}

// headerVarsGlobal 无会话请求（备用识图 / 向量化）的占位符取值。
var headerVarsGlobal = headerVars{
	session:     headerGlobalValue,
	sessionID:   headerGlobalValue,
	sessionType: headerGlobalValue,
}

// headerScope 一个会话的自定义请求头取值来源：key 为会话 token 的存取键
// （线上会话 sessionKey、面板试聊 panelchat:ID），其余为占位符的静态字段。
type headerScope struct {
	key         string
	sessionID   string
	sessionType string // group / friend / panel
}

// sessionHeaderScope 线上会话（群聊/私聊）的取值来源：目标 ID（含平台前缀，
// 如 qq:123456）+ 会话类型。
func sessionHeaderScope(id message.QID, isGroup bool) headerScope {
	sessionType := "friend"
	if isGroup {
		sessionType = "group"
	}
	return headerScope{key: sessionKey(id, isGroup), sessionID: id.String(), sessionType: sessionType}
}

// headerVarsFor 按会话当前 token 组装占位符取值（每次调用都重新取 token）。
func (p *AIChatPlugin) headerVarsFor(scope headerScope) headerVars {
	return headerVars{
		session:     p.sessionToken(scope.key),
		sessionID:   scope.sessionID,
		sessionType: scope.sessionType,
	}
}

// headersNeedSession 自定义请求头中是否有会话占位符：没有时不需要生成/
// 维护会话 token，也不必每请求重算请求头（静态头原样下发）。
func headersNeedSession(headers map[string]string) bool {
	for _, value := range headers {
		if strings.Contains(value, headerPlaceholderSession) ||
			strings.Contains(value, headerPlaceholderSessionID) ||
			strings.Contains(value, headerPlaceholderSessionType) {
			return true
		}
	}
	return false
}

// headerProviderFor 动态请求头提供者：每次 LLM 请求都按会话当前 token 重新
// 渲染自定义请求头，新建会话后无需重建客户端即可换新 ${session}。
func (p *AIChatPlugin) headerProviderFor(scope headerScope) func() map[string]string {
	return func() map[string]string {
		return renderHeaders(p.customHeaders, p.headerVarsFor(scope))
	}
}

// newSessionToken 生成会话实例 token（16 位十六进制）。每个会话唯一，新建会话
// （#新对话 / 自动清空历史 / 面板新对话）后重新生成，用作 ${session} 的取值。
func newSessionToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// sessionToken 返回会话键对应的实例 token，首次访问时生成；进程生命周期内
// 保持不变（会话对象被回收重建、历史仍延续时也不变）。
func (p *AIChatPlugin) sessionToken(key string) string {
	if v, ok := p.sessionTokens.Load(key); ok {
		return v.(string)
	}
	v, _ := p.sessionTokens.LoadOrStore(key, newSessionToken())
	return v.(string)
}

// renewSessionToken 新建会话后换新 token：之后的请求（含子代理/定时任务等
// 共享同一会话键的请求）自动使用新值，无需重建 LLM 客户端。
func (p *AIChatPlugin) renewSessionToken(key string) {
	p.sessionTokens.Store(key, newSessionToken())
}

// headerReplacer 按会话取值构造占位符替换器。
func headerReplacer(v headerVars) *strings.Replacer {
	return strings.NewReplacer(
		headerPlaceholderSession, v.session,
		headerPlaceholderSessionID, v.sessionID,
		headerPlaceholderSessionType, v.sessionType,
	)
}

// renderHeaders 把自定义请求头值中的会话占位符替换为该会话的实际值。
// 没有占位符时原样返回入参（不复制：调用方只读，不会改动配置）。
func renderHeaders(headers map[string]string, v headerVars) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	hasPlaceholder := false
	for _, value := range headers {
		if strings.Contains(value, "${") {
			hasPlaceholder = true
			break
		}
	}
	if !hasPlaceholder {
		return headers
	}
	r := headerReplacer(v)
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		out[name] = r.Replace(value)
	}
	return out
}

// unknownHeaderPlaceholders 返回请求头值中不受支持的 ${…} 占位符（去重升序），
// 供启动日志告警：未知占位符会原样下发，通常是拼写错误。
func unknownHeaderPlaceholders(headers map[string]string) []string {
	known := map[string]bool{
		headerPlaceholderSession:     true,
		headerPlaceholderSessionID:   true,
		headerPlaceholderSessionType: true,
	}
	seen := make(map[string]bool)
	var unknown []string
	for _, value := range headers {
		for {
			start := strings.Index(value, "${")
			if start < 0 {
				break
			}
			end := strings.Index(value[start:], "}")
			if end < 0 {
				break
			}
			token := value[start : start+end+1]
			if !known[token] && !seen[token] {
				seen[token] = true
				unknown = append(unknown, token)
			}
			value = value[start+end+1:]
		}
	}
	slices.Sort(unknown)
	return unknown
}
