package aichat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// TestCollectSteerMessages 运行中追加输入的收集规则：Steer 为 nil 时不追加；
// 空白项被丢弃；非空文本原样作为 user 消息（编排层不再自动加任何前缀）。
func TestCollectSteerMessages(t *testing.T) {
	if got := collectSteerMessages(ChatOptions{}); got != nil {
		t.Fatalf("Steer 为 nil 时应返回 nil, got %v", got)
	}

	calls := 0
	opts := ChatOptions{Steer: func() []string {
		calls++
		if calls > 1 {
			return nil
		}
		return []string{"  【用户插话】补充说明  ", "", "   "}
	}}
	msgs := collectSteerMessages(opts)
	if len(msgs) != 1 {
		t.Fatalf("应只保留 1 条非空追加输入, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != RoleUser {
		t.Fatalf("追加输入应为 user 消息, got %q", msgs[0].Role)
	}
	if got := ExtractMessageText(msgs[0]); got != "【用户插话】补充说明" {
		t.Fatalf("追加输入文本 = %q（不应被加上额外前缀）", got)
	}
	if second := collectSteerMessages(opts); second != nil {
		t.Fatalf("无新输入时应返回 nil, got %v", second)
	}
}

// TestSteerInjectedBeforeNextLLMRequest 端到端验证插话时序：运行中追加的输入在
// 下一次 LLM 请求前作为 user 消息进入上下文（第一次请求不包含），且文本原样下发
// （编排层不添加「[追加指令]」前缀，前缀由调用方自带）。
func TestSteerInjectedBeforeNextLLMRequest(t *testing.T) {
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
			fmt.Fprint(w, toolCallLLMResponse("call_1", "tool_a", "{}", 10))
			return
		}
		fmt.Fprint(w, finalLLMResponse("完成", 20))
	}))
	defer srv.Close()

	exec := &fakeToolExecutor{}
	o := toolLoopOrchestrator(exec)

	var steerCalls atomic.Int32
	content, messages, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
		[]Message{TextMessage(RoleUser, "开始")},
		llmtool.CallBackFuncs{},
		ChatOptions{Steer: func() []string {
			// 第一次请求前还没有插话；工具执行后、第二次请求前注入一条
			if steerCalls.Add(1) == 2 {
				return []string{"【用户插话】补充一点：用中文回答"}
			}
			return nil
		}})
	if err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}
	if content != "完成" {
		t.Fatalf("最终内容 = %q", content)
	}
	if got := countMessagesContaining(messages, "【用户插话】"); got != 1 {
		t.Fatalf("返回消息中插话应恰好 1 条, got %d", got)
	}
	if got := countMessagesContaining(messages, "[追加指令]"); got != 0 {
		t.Fatalf("编排层不应添加 [追加指令] 前缀, got %d 条", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("LLM 请求次数 = %d, want 2", len(bodies))
	}
	if strings.Contains(bodies[0], "【用户插话】") {
		t.Fatal("第一次请求前没有插话，不应出现在请求体中")
	}
	if !strings.Contains(bodies[1], "【用户插话】补充一点：用中文回答") {
		t.Fatalf("第二次请求应包含插话内容, body=%s", bodies[1])
	}
}
