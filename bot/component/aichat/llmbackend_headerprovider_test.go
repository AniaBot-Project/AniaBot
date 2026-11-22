package aichat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestWithHeaderProviderAllFormats 动态请求头（WithHeaderProvider）三种格式
// 都会下发，且同一客户端的每次请求重新取值：provider 返回值变化后，下一次
// 请求即使用新值（无需重建客户端）。
func TestWithHeaderProviderAllFormats(t *testing.T) {
	for _, format := range []string{APIFormatChatCompletions, APIFormatResponses, APIFormatAnthropic} {
		t.Run(format, func(t *testing.T) {
			var mu sync.Mutex
			var got []string
			var dup [][]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				got = append(got, r.Header.Get("X-Session"))
				dup = append(dup, r.Header.Values("X-Session"))
				mu.Unlock()
				switch format {
				case APIFormatResponses:
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, responsesJSON(
						`[{"type":"message","id":"m1","role":"assistant","status":"completed",`+
							`"content":[{"type":"output_text","text":"hi","annotations":[]}]}]`,
						responsesUsageJSON(5, 3, 2)))
				case APIFormatAnthropic:
					anthropicSSEReply(anthropicTextStream(`"hi"`, anthropicUsageJSON(5, 3, 0, 0)), nil)(w, r)
				default:
					fakeChatHandler(w, r)
				}
			}))
			defer srv.Close()

			current := "v1"
			client, err := NewLLMClient(srv.URL, "test-key", "test-model",
				WithAPIFormat(format),
				WithHeaders(map[string]string{"X-Session": "static"}),
				WithHeaderProvider(func() map[string]string {
					return map[string]string{"X-Session": current}
				}),
			)
			if err != nil {
				t.Fatalf("NewLLMClient 失败: %v", err)
			}
			if _, _, err := client.Generate(context.Background(),
				[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
				t.Fatalf("首次 generate 失败: %v", err)
			}

			current = "v2"
			if _, _, err := client.Generate(context.Background(),
				[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{}); err != nil {
				t.Fatalf("第二次 generate 失败: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(got) != 2 {
				t.Fatalf("请求次数 = %d, want 2", len(got))
			}
			if got[0] != "v1" || got[1] != "v2" {
				t.Fatalf("动态请求头 = %v, want [v1 v2]（应覆盖静态值且每次请求重新取值）", got)
			}
			for i, values := range dup {
				if len(values) != 1 {
					t.Fatalf("第 %d 次请求 X-Session 出现 %d 个值 %v，动态头应覆盖静态头而非追加",
						i+1, len(values), values)
				}
			}
		})
	}
}

// TestWithHeaderProviderFallback 切换备用模型时，备用客户端同样携带动态请求头。
func TestWithHeaderProviderFallback(t *testing.T) {
	var mu sync.Mutex
	var fbHeader string
	fbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fbHeader = r.Header.Get("X-Session")
		mu.Unlock()
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
		WithHeaderProvider(func() map[string]string {
			return map[string]string{"X-Session": "dynamic-token"}
		}),
		WithFallback(fbSrv.URL, "test-key", "fallback-model", ""),
	)
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	if _, _, err := genReq(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if fbHeader != "dynamic-token" {
		t.Fatalf("备用模型动态请求头 = %q, want %q", fbHeader, "dynamic-token")
	}
}
