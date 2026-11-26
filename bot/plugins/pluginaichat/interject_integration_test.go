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
	"sync/atomic"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// interjectFakeExec 测试用工具执行器：工具执行期间往会话待处理队列里塞一条
// 用户消息，模拟「AI 处理到一半时用户插话」。
type interjectFakeExec struct {
	p       *AIChatPlugin
	id      message.QID
	isGroup bool
}

func (e *interjectFakeExec) Tools() []llmtool.ToolDef {
	return []llmtool.ToolDef{{Function: llmtool.FunctionDef{Name: "tool_a"}}}
}

func (e *interjectFakeExec) Execute(_ context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
	if call.Name == "tool_a" {
		e.p.enqueuePending(e.id, e.isGroup, testGroupMsg(2001, "顺便补充：用中文回答"))
	}
	return "工具结果", nil
}

// TestProcessChatBatchInjectsQueuedMessageMidTurn 端到端验证插话接线：
// 工具执行期间到达的用户消息在下一次 LLM 请求前作为【用户插话】注入，队列被
// 排空（不会重复处理），最终回复照常发送。
func TestProcessChatBatchInjectsQueuedMessageMidTurn(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
				`"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[`+
				`{"id":"call_1","type":"function","function":{"name":"tool_a","arguments":"{}"}}`+
				`]},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"全部完成"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`)
	}))
	defer srv.Close()

	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	id := message.FromUint64(2001)
	exec := &interjectFakeExec{p: p, id: id, isGroup: true}
	chat, err := aichat.NewChatBot(srv.URL, "k", "m", "系统提示", 128000, exec, nil)
	if err != nil {
		t.Fatalf("NewChatBot 失败: %v", err)
	}

	rb := &recordBot{}
	if ok := p.processChatBatch(context.Background(), rb, id, true, chat,
		[]message.Message{testGroupMsg(2001, "做个任务")}); !ok {
		t.Fatal("processChatBatch 应成功")
	}

	mu.Lock()
	got := append([]string(nil), bodies...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("LLM 请求次数 = %d, want 2", len(got))
	}
	if strings.Contains(got[0], "【用户插话】") {
		t.Fatal("插话发生在工具执行期间，第一次请求不应包含")
	}
	if !strings.Contains(got[1], "【用户插话】") || !strings.Contains(got[1], "顺便补充：用中文回答") {
		t.Fatalf("第二次请求应包含插话内容, body=%s", got[1])
	}
	if n := p.pendingCount(sessionKey(id, true)); n != 0 {
		t.Fatalf("插话注入后队列应被排空, got %d", n)
	}
	if !containsSent(rb, "全部完成") {
		t.Fatalf("最终回复仍应发送, got %v", rb.sent())
	}
}
