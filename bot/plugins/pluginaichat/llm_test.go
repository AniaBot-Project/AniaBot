package pluginaichat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
)

// TestLLMClientOptionsCarryCustomHeaders 插件解析后的自定义请求头经
// llmClientOptions 附加到实际 LLM 请求（面板配置 → 请求头 端到端生效）；
// 未配置时不附加（空 map 无副作用）。
func TestLLMClientOptionsCarryCustomHeaders(t *testing.T) {
	var gotToken, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Custom-Token")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p := &AIChatPlugin{customHeaders: parseHeaderLinesOrFail(t, []string{"X-Custom-Token: secret-token", "User-Agent: MyGateway/1.0"})}
	client, err := aichat.NewLLMClient(srv.URL, "test-key", "test-model", p.llmClientOptions()...)
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	if _, _, err := client.Generate(context.Background(),
		[]aichat.Message{aichat.TextMessage(aichat.RoleUser, "hello")}, aichat.ChatOptions{}); err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if gotToken != "secret-token" {
		t.Fatalf("X-Custom-Token = %q, want %q", gotToken, "secret-token")
	}
	if gotUA != "MyGateway/1.0" {
		t.Fatalf("User-Agent = %q, want %q", gotUA, "MyGateway/1.0")
	}
}

func parseHeaderLinesOrFail(t *testing.T, lines []string) map[string]string {
	t.Helper()
	headers, invalid := parseHeaderLines(lines)
	if len(invalid) > 0 {
		t.Fatalf("意外非法行: %v", invalid)
	}
	return headers
}
