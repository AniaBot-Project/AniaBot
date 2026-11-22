package pluginaichat

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// TestRenderHeadersSessionPlaceholders 会话占位符替换：会话 token / 目标 ID /
// 会话类型按传入取值替换；值中的其他内容（前后缀、静态头）保持不变。
func TestRenderHeadersSessionPlaceholders(t *testing.T) {
	headers := map[string]string{
		"X-Session":      "AniaBot-${session}",
		"X-Session-Id":   "${session_id}",
		"X-Session-Type": "${session_type}",
		"X-Static":       "no-placeholder",
	}
	vars := headerVars{session: "a1b2c3d4e5f60718", sessionID: "qq:123456", sessionType: "group"}

	got := renderHeaders(headers, vars)
	want := map[string]string{
		"X-Session":      "AniaBot-a1b2c3d4e5f60718",
		"X-Session-Id":   "qq:123456",
		"X-Session-Type": "group",
		"X-Static":       "no-placeholder",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("headers = %v, want %v", got, want)
	}
	// 入参不应被修改（模板要被多个会话复用）
	if headers["X-Session"] != "AniaBot-${session}" {
		t.Fatalf("模板被就地修改: %q", headers["X-Session"])
	}
}

// TestRenderHeadersNoPlaceholderKeepsMap 无占位符时原样返回（不复制、不改值）。
func TestRenderHeadersNoPlaceholderKeepsMap(t *testing.T) {
	headers := map[string]string{"Authorization": "Bearer token"}
	got := renderHeaders(headers, headerVarsGlobal)
	if got["Authorization"] != "Bearer token" || len(got) != 1 {
		t.Fatalf("headers = %v, want 原样返回", got)
	}
	if got := renderHeaders(nil, headerVarsGlobal); got != nil {
		t.Fatalf("空配置应返回 nil, got %v", got)
	}
}

// TestGlobalHeaderPlaceholders 无会话的全局请求（备用识图 / 向量化）占位符取 global。
func TestGlobalHeaderPlaceholders(t *testing.T) {
	headers := parseHeaderLinesOrFail(t, []string{
		"X-Session: ${session}",
		"X-Session-Id: ${session_id}",
		"X-Session-Type: ${session_type}",
	})
	got := renderHeaders(headers, headerVarsGlobal)
	for name, want := range map[string]string{
		"X-Session":      "global",
		"X-Session-Id":   "global",
		"X-Session-Type": "global",
	} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
}

// TestUnknownHeaderPlaceholders 未知占位符（拼写错误）被发现并去重排序，已知的不报。
func TestUnknownHeaderPlaceholders(t *testing.T) {
	headers := map[string]string{
		"A": "${sessoin}",
		"B": "${session} ${foo} ${foo}",
		"C": "${session_id}-${session_type}",
		"D": "no placeholder",
	}
	got := unknownHeaderPlaceholders(headers)
	want := []string{"${foo}", "${sessoin}"}
	if len(got) != len(want) {
		t.Fatalf("unknown = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unknown = %v, want %v", got, want)
		}
	}
	if got := unknownHeaderPlaceholders(nil); len(got) != 0 {
		t.Fatalf("空配置应无未知占位符, got %v", got)
	}
}

// TestHeadersNeedSession 判断配置里是否真的用到会话占位符：只有静态头、或只有
// 未知占位符时不需要会话 token。
func TestHeadersNeedSession(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"空配置", nil, false},
		{"仅静态头", map[string]string{"Authorization": "Bearer token"}, false},
		{"未知占位符", map[string]string{"X-Foo": "${foo}"}, false},
		{"session", map[string]string{"X-Session": "AniaBot-${session}"}, true},
		{"session_id", map[string]string{"X-Session": "${session_id}"}, true},
		{"session_type", map[string]string{"X-Session": "${session_type}"}, true},
	}
	for _, c := range cases {
		if got := headersNeedSession(c.headers); got != c.want {
			t.Errorf("%s: headersNeedSession = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestNoSessionTokenWithoutPlaceholders 配置里没有会话占位符时，创建会话客户端
// 不应生成会话 token（避免为每个会话白白维护一份不会被使用的状态）。
func TestNoSessionTokenWithoutPlaceholders(t *testing.T) {
	p := &AIChatPlugin{}
	p.customHeaders = parseHeaderLinesOrFail(t, []string{"X-Static: static-value"})
	scope := sessionHeaderScope(message.FromUint64(123), true)
	if opts := p.llmClientOptionsFor(scope); len(opts) == 0 {
		t.Fatal("应返回客户端可选参数")
	}
	count := 0
	p.sessionTokens.Range(func(any, any) bool {
		count++
		return true
	})
	if count != 0 {
		t.Fatalf("无占位符时不应生成会话 token, got %d 个", count)
	}
}

// TestSessionTokenStableAndRenewed 会话 token：同一会话键稳定（客户端重建也沿用），
// 不同会话键互不相同，新建会话后换新。
func TestSessionTokenStableAndRenewed(t *testing.T) {
	p := &AIChatPlugin{}
	key := sessionKey(message.FromUint64(123456), true)
	first := p.sessionToken(key)
	if len(first) != 16 {
		t.Fatalf("token = %q, want 16 位十六进制", first)
	}
	if again := p.sessionToken(key); again != first {
		t.Fatalf("同一会话 token 应稳定: %q != %q", again, first)
	}
	other := p.sessionToken(sessionKey(message.FromUint64(654321), true))
	if other == first {
		t.Fatalf("不同会话 token 不应相同: %q", other)
	}

	p.renewSessionToken(key)
	renewed := p.sessionToken(key)
	if renewed == first {
		t.Fatalf("新建会话后 token 应换新, still %q", renewed)
	}
	if p.sessionToken(sessionKey(message.FromUint64(654321), true)) != other {
		t.Fatal("换新只应影响当前会话")
	}
}

// TestHeaderProviderFollowsRenewedToken 动态请求头提供者每次取值：token 换新后
// 同一个 provider（即同一个已创建的客户端）立即使用新值。
func TestHeaderProviderFollowsRenewedToken(t *testing.T) {
	p := &AIChatPlugin{}
	p.customHeaders = parseHeaderLinesOrFail(t, []string{"X-Session: ${session}"})
	scope := sessionHeaderScope(message.FromUint64(123456), true)
	provider := p.headerProviderFor(scope)

	before := provider()["X-Session"]
	if before != p.sessionToken(scope.key) {
		t.Fatalf("provider = %q, want %q", before, p.sessionToken(scope.key))
	}
	p.renewSessionToken(scope.key)
	after := provider()["X-Session"]
	if after == before {
		t.Fatalf("换新后 provider 仍返回旧值 %q", after)
	}
}

// TestSessionHeaderPlaceholdersEndToEnd 面板配置的请求头经真实 HTTP 请求下发：
// 每个会话各自唯一的 ${session}（客户端多次请求稳定、换新后变化），
// ${session_id} / ${session_type} 与实际会话一致，静态头不受影响。
func TestSessionHeaderPlaceholdersEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var captured []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = append(captured, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p := &AIChatPlugin{}
	p.customHeaders = parseHeaderLinesOrFail(t, []string{
		"X-Session: AniaBot-${session}",
		"X-Session-Id: ${session_id}",
		"X-Session-Type: ${session_type}",
		"X-Static: static-value",
	})
	groupScope := sessionHeaderScope(message.FromUint64(123456), true)
	friendScope := sessionHeaderScope(message.FromUint64(789), false)

	newClient := func(scope headerScope) *aichat.LLMClient {
		t.Helper()
		client, err := aichat.NewLLMClient(srv.URL, "test-key", "test-model",
			p.llmClientOptionsFor(scope)...)
		if err != nil {
			t.Fatalf("NewLLMClient 失败: %v", err)
		}
		return client
	}
	generate := func(client *aichat.LLMClient) {
		t.Helper()
		if _, _, err := client.Generate(context.Background(),
			[]aichat.Message{aichat.TextMessage(aichat.RoleUser, "hello")}, aichat.ChatOptions{}); err != nil {
			t.Fatalf("Generate 失败: %v", err)
		}
	}

	groupClient := newClient(groupScope)
	friendClient := newClient(friendScope)
	generate(groupClient)
	generate(groupClient)
	generate(friendClient)

	mu.Lock()
	group1, group2, friend := captured[0].Clone(), captured[1].Clone(), captured[2].Clone()
	mu.Unlock()

	groupToken := strings.TrimPrefix(group1.Get("X-Session"), "AniaBot-")
	if len(groupToken) != 16 || groupToken == group1.Get("X-Session") {
		t.Fatalf("X-Session = %q, want AniaBot-<16 位 token>", group1.Get("X-Session"))
	}
	if group2.Get("X-Session") != group1.Get("X-Session") {
		t.Fatalf("同一会话的连续请求 token 应稳定: %q != %q",
			group2.Get("X-Session"), group1.Get("X-Session"))
	}
	if friend.Get("X-Session") == group1.Get("X-Session") {
		t.Fatalf("不同会话 token 不应相同: %q", friend.Get("X-Session"))
	}
	if got := group1.Get("X-Session-Id"); got != "qq:123456" {
		t.Fatalf("群聊 X-Session-Id = %q, want qq:123456", got)
	}
	if got := group1.Get("X-Session-Type"); got != "group" {
		t.Fatalf("群聊 X-Session-Type = %q, want group", got)
	}
	if got := friend.Get("X-Session-Id"); got != "qq:789" {
		t.Fatalf("私聊 X-Session-Id = %q, want qq:789", got)
	}
	if got := friend.Get("X-Session-Type"); got != "friend" {
		t.Fatalf("私聊 X-Session-Type = %q, want friend", got)
	}
	if got := group1.Get("X-Static"); got != "static-value" {
		t.Fatalf("静态头 = %q, want static-value", got)
	}

	// 新建会话：同一客户端（未重建）下一次请求即换新 token，其余字段不变
	p.renewSessionToken(groupScope.key)
	generate(groupClient)
	mu.Lock()
	renewed := captured[3].Clone()
	mu.Unlock()
	if renewed.Get("X-Session") == group1.Get("X-Session") {
		t.Fatalf("新建会话后 X-Session 应变化, still %q", renewed.Get("X-Session"))
	}
	if renewed.Get("X-Session-Id") != "qq:123456" || renewed.Get("X-Session-Type") != "group" {
		t.Fatalf("新建会话不应改变目标字段: id=%q type=%q",
			renewed.Get("X-Session-Id"), renewed.Get("X-Session-Type"))
	}
}

// TestNewConversationRenewsSessionHeader 端到端：#新对话 清空上下文后，
// 同一个 ChatBot（LLM 客户端未重建）的本次请求立即携带新的 ${session}。
func TestNewConversationRenewsSessionHeader(t *testing.T) {
	var mu sync.Mutex
	var sessions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessions = append(sessions, r.Header.Get("X-Session"))
		mu.Unlock()
		fakePlainChatReply(w, "收到")
	}))
	defer srv.Close()

	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.customHeaders = parseHeaderLinesOrFail(t, []string{"X-Session: ${session}"})

	id := message.FromUint64(2001)
	scope := sessionHeaderScope(id, true)
	before := p.sessionToken(scope.key)

	chat, err := aichat.NewChatBot(srv.URL, "k", "m", "系统提示", 128000, nil, nil,
		aichat.WithClientOptions(p.llmClientOptionsFor(scope)...))
	if err != nil {
		t.Fatalf("NewChatBot 失败: %v", err)
	}
	rb := &recordBot{}
	if ok := p.processChatBatch(context.Background(), rb, id, true, chat,
		[]message.Message{testGroupMsg(2001, "你好")}); !ok {
		t.Fatal("首个 processChatBatch 应成功")
	}
	if ok := p.processChatBatch(context.Background(), rb, id, true, chat,
		[]message.Message{testGroupMsg(2001, "#新对话")}); !ok {
		t.Fatal("#新对话 的 processChatBatch 应成功")
	}

	mu.Lock()
	got := append([]string(nil), sessions...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("应发起 2 次模型请求, got %d", len(got))
	}
	if got[0] != before {
		t.Fatalf("首个请求 X-Session = %q, want %q", got[0], before)
	}
	if got[1] == got[0] {
		t.Fatalf("#新对话 后 X-Session 应变化, still %q", got[1])
	}
	if got[1] != p.sessionToken(scope.key) {
		t.Fatalf("#新对话 后 X-Session = %q, want %q", got[1], p.sessionToken(scope.key))
	}
}
