package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// ---- 循环保护 / 无限轮数 / 单请求内上下文保护 ----

// mockToolLoopServer 返回一个 chat_completions mock server：按第 n 次请求
// （1 起）调用 respFor(n) 生成响应体；返回请求计数供断言。
func mockToolLoopServer(respFor func(call int) string) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, respFor(n))
	}))
	return srv, &calls
}

// toolCallLLMResponse 构造带一个工具调用的 chat completion 响应。
func toolCallLLMResponse(callID, name, arguments string, promptTokens int) string {
	argsJSON, _ := json.Marshal(arguments)
	return fmt.Sprintf(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
		`"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[`+
		`{"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}`+
		`]},"finish_reason":"tool_calls"}],`+
		`"usage":{"prompt_tokens":%d,"completion_tokens":3,"total_tokens":%d}}`,
		callID, name, string(argsJSON), promptTokens, promptTokens+3)
}

// finalLLMResponse 构造无工具调用的最终文本回复。
func finalLLMResponse(content string, promptTokens int) string {
	return fmt.Sprintf(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":%d,"completion_tokens":2,"total_tokens":%d}}`,
		content, promptTokens, promptTokens+2)
}

// toolLoopOrchestrator 构造带一个工具的执行器与编排器（默认不限轮数）。
func toolLoopOrchestrator(exec *fakeToolExecutor) *ToolOrchestrator {
	exec.tools = []llmtool.ToolDef{{Function: llmtool.FunctionDef{Name: "tool_a"}}}
	o := newTestOrchestrator(exec)
	o.SetMaxIterations(0) // 不限轮数
	return o
}

// countMessagesContaining 统计消息文本中包含 substr 的消息条数。
func countMessagesContaining(messages []Message, substr string) int {
	n := 0
	for _, msg := range messages {
		if strings.Contains(ExtractMessageText(msg), substr) {
			n++
		}
	}
	return n
}

// TestUnlimitedIterationsNoHardCap maxIterations=0 时循环不因轮数退出：
// mock 连续返回 22 轮工具调用后再给最终回答，仍能正常结束且不出现
// "exceeded maximum iterations"。
func TestUnlimitedIterationsNoHardCap(t *testing.T) {
	const toolRounds = 22
	srv, calls := mockToolLoopServer(func(n int) string {
		if n <= toolRounds {
			return toolCallLLMResponse(fmt.Sprintf("call_%d", n), "tool_a", fmt.Sprintf(`{"step":%d}`, n), 10)
		}
		return finalLLMResponse("全部完成", 10)
	})
	defer srv.Close()

	exec := &fakeToolExecutor{}
	o := toolLoopOrchestrator(exec)
	o.SetMaxIterations(0)

	content, _, usage, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
		[]Message{TextMessage(RoleUser, "做个长任务")}, llmtool.CallBackFuncs{}, ChatOptions{})
	if err != nil {
		t.Fatalf("不限轮数模式下不应因轮数退出: %v", err)
	}
	if content != "全部完成" {
		t.Fatalf("最终内容不符: %q", content)
	}
	if got := calls.Load(); got != toolRounds+1 {
		t.Fatalf("LLM 调用次数 = %d, want %d", got, toolRounds+1)
	}
	if usage.Iterations != toolRounds+1 {
		t.Fatalf("Iterations = %d, want %d", usage.Iterations, toolRounds+1)
	}
}

// TestRepeatToolReminders 相同工具+参数连续重复达到各阈值时注入提醒，
// 每个阈值只提醒一次；提醒文本与次数符合约定。
func TestRepeatToolReminders(t *testing.T) {
	srv, calls := mockToolLoopServer(func(n int) string {
		if n <= 5 {
			return toolCallLLMResponse(fmt.Sprintf("call_%d", n), "tool_a", "{}", 10)
		}
		return finalLLMResponse("收尾", 10)
	})
	defer srv.Close()

	exec := &fakeToolExecutor{}
	o := toolLoopOrchestrator(exec)
	o.SetMaxIterations(5) // 第 5 轮后强制收尾，共 6 次 LLM 调用

	_, messages, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
		[]Message{TextMessage(RoleUser, "循环")}, llmtool.CallBackFuncs{},
		ChatOptions{Watchdog: WatchdogOptions{RepeatWarnThresholds: []int{2, 4}}})
	if err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("LLM 调用次数 = %d, want 6", got)
	}
	if n := countMessagesContaining(messages, "你连续 2 次执行相同的工具调用"); n != 1 {
		t.Fatalf("阈值 2 应提醒一次, got %d", n)
	}
	if n := countMessagesContaining(messages, "你连续 4 次执行相同的工具调用"); n != 1 {
		t.Fatalf("阈值 4 应提醒一次, got %d", n)
	}
	if n := countMessagesContaining(messages, "[系统提醒] 你连续"); n != 2 {
		t.Fatalf("重复提醒总数 = %d, want 2", n)
	}
	// 提醒应以 user 角色追加
	for _, msg := range messages {
		if strings.Contains(ExtractMessageText(msg), "[系统提醒] 你连续") && msg.Role != RoleUser {
			t.Fatalf("提醒消息角色 = %s, want user", msg.Role)
		}
	}
}

// TestRepeatReminderNotTriggeredForDifferentCalls 交替的不同工具调用签名
// 不算「连续重复」，不注入提醒。
func TestRepeatReminderNotTriggeredForDifferentCalls(t *testing.T) {
	srv, _ := mockToolLoopServer(func(n int) string {
		if n <= 4 {
			name := "tool_a"
			if n%2 == 0 {
				name = "tool_b"
			}
			return toolCallLLMResponse(fmt.Sprintf("call_%d", n), name, "{}", 10)
		}
		return finalLLMResponse("收尾", 10)
	})
	defer srv.Close()

	exec := &fakeToolExecutor{}
	o := toolLoopOrchestrator(exec)
	o.SetMaxIterations(4)

	_, messages, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
		[]Message{TextMessage(RoleUser, "交替")}, llmtool.CallBackFuncs{},
		ChatOptions{Watchdog: WatchdogOptions{RepeatWarnThresholds: []int{2}}})
	if err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}
	if n := countMessagesContaining(messages, "[系统提醒] 你连续"); n != 0 {
		t.Fatalf("签名交替变化不应触发重复提醒, got %d", n)
	}
}

// TestNoProgressAbort 相同工具调用达到 NoProgressLimit 时终止循环并返回
// *WatchdogAbortError（插件层用 errors.As 识别）；终止发生在重复执行之前。
func TestNoProgressAbort(t *testing.T) {
	srv, calls := mockToolLoopServer(func(n int) string {
		return toolCallLLMResponse(fmt.Sprintf("call_%d", n), "tool_a", "{}", 10)
	})
	defer srv.Close()

	var executed atomic.Int32
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		executed.Add(1)
		return "result", nil
	}}
	o := toolLoopOrchestrator(exec)
	o.SetMaxIterations(10)

	_, _, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
		[]Message{TextMessage(RoleUser, "死循环")}, llmtool.CallBackFuncs{},
		ChatOptions{Watchdog: WatchdogOptions{NoProgressLimit: 3}})
	if err == nil {
		t.Fatal("期望无进展终止错误, got nil")
	}
	var abortErr *WatchdogAbortError
	if !errors.As(err, &abortErr) {
		t.Fatalf("错误类型应为 *WatchdogAbortError, got %T: %v", err, err)
	}
	if !strings.Contains(abortErr.Reason, "无进展") || !strings.Contains(abortErr.Reason, "3 次") {
		t.Fatalf("终止原因文案不符: %q", abortErr.Reason)
	}
	// 第 3 次重复时在工具执行前终止：LLM 调 3 次、工具执行 2 次
	if got := calls.Load(); got != 3 {
		t.Fatalf("LLM 调用次数 = %d, want 3", got)
	}
	if got := executed.Load(); got != 2 {
		t.Fatalf("工具执行次数 = %d, want 2", got)
	}
}

// TestProgressReminderOnce 工具轮数达到 ProgressReminderAfter 时注入一次
// 委派提醒（整轮只一次）。
func TestProgressReminderOnce(t *testing.T) {
	srv, calls := mockToolLoopServer(func(n int) string {
		if n <= 4 {
			return toolCallLLMResponse(fmt.Sprintf("call_%d", n), "tool_a", fmt.Sprintf(`{"step":%d}`, n), 10)
		}
		return finalLLMResponse("完成", 10)
	})
	defer srv.Close()

	exec := &fakeToolExecutor{}
	o := toolLoopOrchestrator(exec)
	o.SetMaxIterations(4)

	_, messages, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
		[]Message{TextMessage(RoleUser, "多轮")}, llmtool.CallBackFuncs{},
		ChatOptions{Watchdog: WatchdogOptions{ProgressReminderAfter: 2}})
	if err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("LLM 调用次数 = %d, want 5", got)
	}
	if n := countMessagesContaining(messages, "考虑交给 subagent_run 后台执行"); n != 1 {
		t.Fatalf("委派提醒应只注入一次, got %d", n)
	}
}

// TestSoftDeadlineReminderOnce 软截止时间已过时注入一次收尾/转后台提醒；
// 零值 SoftDeadline 时不注入。
func TestSoftDeadlineReminderOnce(t *testing.T) {
	cases := []struct {
		name      string
		deadline  time.Time
		wantCount int
	}{
		{"已过软截止", time.Now().Add(-time.Minute), 1},
		{"未设置软截止", time.Time{}, 0},
		{"未来软截止", time.Now().Add(time.Hour), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := mockToolLoopServer(func(n int) string {
				if n <= 3 {
					return toolCallLLMResponse(fmt.Sprintf("call_%d", n), "tool_a", fmt.Sprintf(`{"step":%d}`, n), 10)
				}
				return finalLLMResponse("完成", 10)
			})
			defer srv.Close()

			exec := &fakeToolExecutor{}
			o := toolLoopOrchestrator(exec)
			o.SetMaxIterations(3)

			_, messages, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
				[]Message{TextMessage(RoleUser, "长任务")}, llmtool.CallBackFuncs{},
				ChatOptions{Watchdog: WatchdogOptions{SoftDeadline: tc.deadline}})
			if err != nil {
				t.Fatalf("ExecuteWithTools 失败: %v", err)
			}
			if got := calls.Load(); got != 4 {
				t.Fatalf("LLM 调用次数 = %d, want 4", got)
			}
			if n := countMessagesContaining(messages, "调用 task_handoff"); n != tc.wantCount {
				t.Fatalf("软截止提醒次数 = %d, want %d", n, tc.wantCount)
			}
		})
	}
}

// TestContextPruneOldToolResults 单请求内上下文保护：prompt 用量超过窗口
// 85% 时，较旧的大工具结果被裁成头尾摘要（ToolCallID 保留），最近 6 条
// 消息免疫；未设置/未超阈值时不裁剪。
func TestContextPruneOldToolResults(t *testing.T) {
	bigOld := strings.Repeat("头", 1500) + "中间" + strings.Repeat("尾", 1500)
	bigRecent := strings.Repeat("近", 3000)

	buildMessages := func() []Message {
		return []Message{
			TextMessage(RoleSystem, "system"),
			TextMessage(RoleUser, "历史1"),
			{Role: RoleAssistant, ToolCalls: []llmtool.ToolCall{{ID: "call_old", Name: "tool_a", Arguments: "{}"}}},
			{Role: RoleTool, ToolCallID: "call_old", Parts: []ContentPart{TextPart(bigOld)}},
			TextMessage(RoleAssistant, "中间回复"),
			TextMessage(RoleUser, "继续"),
			{Role: RoleAssistant, ToolCalls: []llmtool.ToolCall{{ID: "call_recent", Name: "tool_a", Arguments: "{}"}}},
			{Role: RoleTool, ToolCallID: "call_recent", Parts: []ContentPart{TextPart(bigRecent)}},
			TextMessage(RoleAssistant, "再回复"),
			TextMessage(RoleUser, "再继续"),
			TextMessage(RoleAssistant, "ok"),
			TextMessage(RoleUser, "现在开始"),
			TextMessage(RoleUser, "最后"),
		}
	}

	cases := []struct {
		name             string
		maxContextTokens int
		firstPromptToken int
		wantPrune        bool
	}{
		{"超过窗口85%触发裁剪", 1000, 900, true},
		{"未超阈值不裁剪", 1000, 500, false},
		{"保护关闭不裁剪", 0, 900, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := mockToolLoopServer(func(n int) string {
				if n == 1 {
					return toolCallLLMResponse("call_new", "tool_a", "{}", tc.firstPromptToken)
				}
				return finalLLMResponse("完成", 10)
			})
			defer srv.Close()

			exec := &fakeToolExecutor{}
			o := toolLoopOrchestrator(exec)
			o.SetMaxContextTokens(tc.maxContextTokens)

			_, messages, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
				buildMessages(), llmtool.CallBackFuncs{}, ChatOptions{})
			if err != nil {
				t.Fatalf("ExecuteWithTools 失败: %v", err)
			}

			var oldMsg, recentMsg *Message
			for i := range messages {
				switch messages[i].ToolCallID {
				case "call_old":
					oldMsg = &messages[i]
				case "call_recent":
					recentMsg = &messages[i]
				}
			}
			if oldMsg == nil || recentMsg == nil {
				t.Fatalf("未找到预期的工具结果消息: old=%v recent=%v", oldMsg, recentMsg)
			}

			oldText := ExtractMessageText(*oldMsg)
			pruned := strings.HasPrefix(oldText, contextPruneMarker)
			if pruned != tc.wantPrune {
				t.Fatalf("旧大工具结果裁剪 = %v, want %v; text=%q", pruned, tc.wantPrune, truncateForTest(oldText, 200))
			}
			if tc.wantPrune {
				if !strings.Contains(oldText, fmt.Sprintf("原始 %d 字符", utf8.RuneCountInString(bigOld))) {
					t.Fatalf("裁剪文案应包含原始字符数: %q", truncateForTest(oldText, 200))
				}
				if !strings.Contains(oldText, "开头：") || !strings.Contains(oldText, "结尾：") {
					t.Fatalf("裁剪文案应包含头尾摘要: %q", truncateForTest(oldText, 200))
				}
				if oldMsg.Role != RoleTool {
					t.Fatalf("裁剪后消息角色应保持 tool, got %s", oldMsg.Role)
				}
				if !utf8.ValidString(oldText) || utf8.RuneCountInString(oldText) > contextPruneKeptRunes+200 {
					t.Fatalf("裁剪后体积应约 2000 rune, got %d", utf8.RuneCountInString(oldText))
				}
			}
			// 最近 6 条内的工具结果不裁剪
			if got := ExtractMessageText(*recentMsg); got != bigRecent {
				t.Fatalf("最近消息中的工具结果不应被裁剪, len=%d", utf8.RuneCountInString(got))
			}
		})
	}
}

// truncateForTest 截断文本便于失败信息输出。
func truncateForTest(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
}
