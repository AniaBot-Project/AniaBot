package aichat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeanhua/AniaBot/bot/version"
)

// TestUserAgentHeader 三种 API 格式后端的 LLM 请求都必须携带 AniaBot UA
// （版本号由 CI 注入，未注入时为 dev），且覆盖各 SDK 自带的默认 User-Agent。
func TestUserAgentHeader(t *testing.T) {
	wantUA := version.UserAgent()

	t.Run("chat_completions", func(t *testing.T) {
		var ua string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua = r.Header.Get("User-Agent")
			fakeChatHandler(w, r)
		}))
		defer srv.Close()

		b := newChatCompletionsBackend(srv.URL, "test-key", "test-model", nil)
		if _, _, err := b.generate(context.Background(),
			[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
			t.Fatalf("generate 失败: %v", err)
		}
		if ua != wantUA {
			t.Fatalf("User-Agent = %q, want %q", ua, wantUA)
		}
	})

	t.Run("responses", func(t *testing.T) {
		var ua string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua = r.Header.Get("User-Agent")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responsesJSON(
				`[{"type":"message","id":"m1","role":"assistant","status":"completed",`+
					`"content":[{"type":"output_text","text":"hi","annotations":[]}]}]`,
				responsesUsageJSON(5, 3, 2)))
		}))
		defer srv.Close()

		b := newResponsesBackend(srv.URL, "test-key", "test-model", nil)
		if _, _, err := b.generate(context.Background(),
			[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
			t.Fatalf("generate 失败: %v", err)
		}
		if ua != wantUA {
			t.Fatalf("User-Agent = %q, want %q", ua, wantUA)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		var ua string
		// anthropic 的 generate 内部走流式通道，假服务器统一以 SSE 应答
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua = r.Header.Get("User-Agent")
			anthropicSSEReply(anthropicTextStream(`"hi"`, anthropicUsageJSON(5, 3, 0, 0)), nil)(w, r)
		}))
		defer srv.Close()

		b := newAnthropicBackend(srv.URL, "test-key", "test-model", PromptCacheConfig{}, nil)
		if _, _, err := b.generate(context.Background(),
			[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
			t.Fatalf("generate 失败: %v", err)
		}
		if ua != wantUA {
			t.Fatalf("User-Agent = %q, want %q", ua, wantUA)
		}
	})
}

// TestVersionDefaults 未注入时版本号为 dev，UA 与 ldflags 片段保持一致格式。
func TestVersionDefaults(t *testing.T) {
	if version.Version == "" {
		t.Fatal("Version 不应为空")
	}
	if got := version.UserAgent(); got != "AniaBot/"+version.Version {
		t.Fatalf("UserAgent = %q", got)
	}
	if got := version.Ldflags("-s -w"); got != "-s -w -X github.com/jeanhua/AniaBot/bot/version.Version="+version.Version {
		t.Fatalf("Ldflags = %q", got)
	}
	if got := version.Ldflags(""); got != "-X github.com/jeanhua/AniaBot/bot/version.Version="+version.Version {
		t.Fatalf(`Ldflags("") = %q`, got)
	}
}
