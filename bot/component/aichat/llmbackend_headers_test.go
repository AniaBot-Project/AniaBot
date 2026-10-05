package aichat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeanhua/AniaBot/bot/version"
)

// TestWithHeadersAllFormats 三种 API 格式后端都附加自定义请求头；
// 与默认头同名（User-Agent）时自定义值覆盖默认值，其余默认头不受影响。
func TestWithHeadersAllFormats(t *testing.T) {
	headers := map[string]string{
		"X-Custom-Token": "secret-token",
		"X-Route":        "proxy-a",
		"User-Agent":     "MyGateway/1.0",
	}

	assertHeaders := func(t *testing.T, got http.Header) {
		t.Helper()
		for k, want := range headers {
			if v := got.Get(k); v != want {
				t.Fatalf("请求头 %s = %q, want %q", k, v, want)
			}
		}
	}

	newServer := func(t *testing.T, handler http.HandlerFunc) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("chat_completions", func(t *testing.T) {
		var got http.Header
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Clone()
			fakeChatHandler(w, r)
		})
		b := newChatCompletionsBackend(srv.URL, "test-key", "test-model", headers)
		if _, _, err := b.generate(context.Background(),
			[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
			t.Fatalf("generate 失败: %v", err)
		}
		assertHeaders(t, got)
	})

	t.Run("responses", func(t *testing.T) {
		var got http.Header
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responsesJSON(
				`[{"type":"message","id":"m1","role":"assistant","status":"completed",`+
					`"content":[{"type":"output_text","text":"hi","annotations":[]}]}]`,
				responsesUsageJSON(5, 3, 2)))
		})
		b := newResponsesBackend(srv.URL, "test-key", "test-model", headers)
		if _, _, err := b.generate(context.Background(),
			[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
			t.Fatalf("generate 失败: %v", err)
		}
		assertHeaders(t, got)
	})

	t.Run("anthropic", func(t *testing.T) {
		var got http.Header
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Clone()
			anthropicSSEReply(anthropicTextStream(`"hi"`, anthropicUsageJSON(5, 3, 0, 0)), nil)(w, r)
		})
		b := newAnthropicBackend(srv.URL, "test-key", "test-model", PromptCacheConfig{}, headers)
		if _, _, err := b.generate(context.Background(),
			[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
			t.Fatalf("generate 失败: %v", err)
		}
		assertHeaders(t, got)
	})
}

// TestWithHeadersDefaultUAKept 未覆盖 User-Agent 时默认 UA 保持不变。
func TestWithHeadersDefaultUAKept(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	b := newChatCompletionsBackend(srv.URL, "test-key", "test-model",
		map[string]string{"X-Extra": "1"})
	if _, _, err := b.generate(context.Background(),
		[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
		t.Fatalf("generate 失败: %v", err)
	}
	if ua != version.UserAgent() {
		t.Fatalf("User-Agent = %q, want %q", ua, version.UserAgent())
	}
}

// TestWithHeadersFallback 主模型失败切换备用模型时，备用客户端同样携带自定义请求头。
func TestWithHeadersFallback(t *testing.T) {
	var fbHeader string
	fbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fbHeader = r.Header.Get("X-Custom-Token")
		fakeChatHandler(w, r)
	}))
	defer fbSrv.Close()

	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"message":"down","type":"server_error"}}`)
	}))
	defer mainSrv.Close()

	c, err := NewLLMClient(mainSrv.URL, "test-key", "main-model",
		WithRetry(1, 0),
		WithHeaders(map[string]string{"X-Custom-Token": "secret-token"}),
		WithFallback(fbSrv.URL, "test-key", "fallback-model", ""),
	)
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	if _, _, err := genReq(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fbHeader != "secret-token" {
		t.Fatalf("备用模型请求头 X-Custom-Token = %q, want %q", fbHeader, "secret-token")
	}
}
