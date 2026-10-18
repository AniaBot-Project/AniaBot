package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// fakeToolExecutor 测试用工具执行器：fn 为 nil 时按工具名返回固定文本。
type fakeToolExecutor struct {
	fn    func(ctx context.Context, call llmtool.ToolCall, callbacks llmtool.CallBackFuncs) (string, error)
	tools []llmtool.ToolDef
}

func (f *fakeToolExecutor) Execute(ctx context.Context, call llmtool.ToolCall, callbacks llmtool.CallBackFuncs) (string, error) {
	if f.fn != nil {
		return f.fn(ctx, call, callbacks)
	}
	return "result:" + call.Name, nil
}

func (f *fakeToolExecutor) Tools() []llmtool.ToolDef { return f.tools }

func newTestOrchestrator(exec ToolExecutor) *ToolOrchestrator {
	return NewToolOrchestrator(exec, NewMessageBuilder("test prompt"))
}

// TestExecuteToolCallsParallelPreservesOrder 并行执行下结果消息必须与工具调用顺序一致：
// 即使慢工具先启动、快工具后启动，回填到结果切片的下标仍按工具数组顺序。
func TestExecuteToolCallsParallelPreservesOrder(t *testing.T) {
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		if call.Name == "slow" {
			time.Sleep(100 * time.Millisecond) // 慢工具先完成不了，验证结果仍保序
		}
		return "result:" + call.Name, nil
	}}
	o := newTestOrchestrator(exec)

	calls := []llmtool.ToolCall{
		{ID: "call_slow", Name: "slow", Arguments: "{}"},
		{ID: "call_fast", Name: "fast", Arguments: "{}"},
		{ID: "call_third", Name: "third", Arguments: "{}"},
	}
	results, err := o.executeToolCalls(context.Background(), calls, llmtool.CallBackFuncs{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, want := range []string{"call_slow", "call_fast", "call_third"} {
		if results[i].ToolCallID != want {
			t.Fatalf("result[%d] ToolCallID = %q, want %q", i, results[i].ToolCallID, want)
		}
		if got := ExtractMessageText(results[i]); got != "result:"+calls[i].Name {
			t.Fatalf("result[%d] text = %q, want %q", i, got, "result:"+calls[i].Name)
		}
	}
}

// TestExecuteToolCallsErrorContinues 单个工具失败不中断其余工具，错误转为结果文本。
func TestExecuteToolCallsErrorContinues(t *testing.T) {
	var ran []string
	var mu sync.Mutex
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		mu.Lock()
		ran = append(ran, call.Name)
		mu.Unlock()
		if call.Name == "bad" {
			return "", errors.New("boom")
		}
		return "result:" + call.Name, nil
	}}
	o := newTestOrchestrator(exec)

	calls := []llmtool.ToolCall{
		{ID: "c1", Name: "bad", Arguments: "{}"},
		{ID: "c2", Name: "good", Arguments: "{}"},
	}
	results, err := o.executeToolCalls(context.Background(), calls, llmtool.CallBackFuncs{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if got := ExtractMessageText(results[0]); !strings.Contains(got, "Error executing tool: boom") {
		t.Fatalf("bad tool result = %q, want error text", got)
	}
	if got := ExtractMessageText(results[1]); got != "result:good" {
		t.Fatalf("good tool result = %q", got)
	}
	if len(ran) != 2 {
		t.Fatalf("expected both tools ran, got %v", ran)
	}
}

// TestExecuteToolCallsPanicIsolated 工具 panic 转为错误文本，不传染其他工具与进程。
func TestExecuteToolCallsPanicIsolated(t *testing.T) {
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		if call.Name == "panic" {
			panic("tool blew up")
		}
		return "result:" + call.Name, nil
	}}
	o := newTestOrchestrator(exec)

	calls := []llmtool.ToolCall{
		{ID: "c1", Name: "panic", Arguments: "{}"},
		{ID: "c2", Name: "ok", Arguments: "{}"},
	}
	results, err := o.executeToolCalls(context.Background(), calls, llmtool.CallBackFuncs{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ExtractMessageText(results[0]); !strings.Contains(got, "Error executing tool") || !strings.Contains(got, "tool blew up") {
		t.Fatalf("panic tool result = %q, want error text", got)
	}
	if got := ExtractMessageText(results[1]); got != "result:ok" {
		t.Fatalf("ok tool result = %q", got)
	}
}

// TestExecuteToolCallsContextCancel 上下文取消时返回 ctx.Err()（与串行版本语义一致）。
func TestExecuteToolCallsContextCancel(t *testing.T) {
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		<-ctx.Done() // 等待取消
		return "", ctx.Err()
	}}
	o := newTestOrchestrator(exec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消

	_, err := o.executeToolCalls(ctx, []llmtool.ToolCall{{ID: "c1", Name: "x", Arguments: "{}"}}, llmtool.CallBackFuncs{}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestExecuteToolCallsObserverAndCallbacksRace 并行执行下观察者回调全部触发、
// 回调经互斥串行化无数据竞争（配合 -race 运行）。
func TestExecuteToolCallsObserverAndCallbacksRace(t *testing.T) {
	const n = 8
	var cbTexts []string
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, cbs llmtool.CallBackFuncs) (string, error) {
		if cbs.SendText != nil {
			if _, err := cbs.SendText("mid:" + call.Name); err != nil {
				return "", err
			}
		}
		return "result:" + call.Name, nil
	}}
	o := newTestOrchestrator(exec)

	var observed int32
	o.SetToolObserver(func(info ToolCallInfo) {
		atomic.AddInt32(&observed, 1)
		if info.Name == "" {
			t.Errorf("observer got empty tool name")
		}
	})

	calls := make([]llmtool.ToolCall, 0, n)
	for i := range n {
		calls = append(calls, llmtool.ToolCall{ID: fmt.Sprintf("c%d", i), Name: fmt.Sprintf("t%d", i), Arguments: "{}"})
	}
	cbs := llmtool.CallBackFuncs{SendText: func(s string) (string, error) {
		cbTexts = append(cbTexts, s) // 生产代码已串行化，此处无锁依赖 -race 检出
		return s, nil
	}}

	results, err := o.executeToolCalls(context.Background(), calls, cbs, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int(observed) != n {
		t.Fatalf("observer called %d times, want %d", observed, n)
	}
	if len(cbTexts) != n {
		t.Fatalf("callback called %d times, want %d", len(cbTexts), n)
	}
	for i, r := range results {
		if got := ExtractMessageText(r); got != "result:t"+fmt.Sprint(i) {
			t.Fatalf("result[%d] = %q", i, got)
		}
	}
}

// TestExecuteToolCallsGateBlocks 门禁阻断的工具不执行、结果文本回填到正确下标、
// 循环继续且观察者照常记录（面板可见被拦调用）。
func TestExecuteToolCallsGateBlocks(t *testing.T) {
	var ran []string
	var mu sync.Mutex
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		mu.Lock()
		ran = append(ran, call.Name)
		mu.Unlock()
		return "result:" + call.Name, nil
	}}
	o := newTestOrchestrator(exec)

	var observed []string
	o.SetToolObserver(func(info ToolCallInfo) {
		observed = append(observed, info.Name)
	})

	gate := func(ctx context.Context, call llmtool.ToolCall) (bool, string) {
		if call.Name == "danger" {
			return true, "【计划模式】工具已被阻止"
		}
		return false, ""
	}
	calls := []llmtool.ToolCall{
		{ID: "c1", Name: "danger", Arguments: "{}"},
		{ID: "c2", Name: "good", Arguments: "{}"},
	}
	results, err := o.executeToolCalls(context.Background(), calls, llmtool.CallBackFuncs{}, gate, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ExtractMessageText(results[0]); got != "【计划模式】工具已被阻止" {
		t.Fatalf("blocked result = %q", got)
	}
	if got := ExtractMessageText(results[1]); got != "result:good" {
		t.Fatalf("good result = %q", got)
	}
	if len(ran) != 1 || ran[0] != "good" {
		t.Fatalf("被阻断的工具不应执行, ran=%v", ran)
	}
	if len(observed) != 2 {
		t.Fatalf("阻断与执行都应触发观察者, observed=%v", observed)
	}
}

// TestExecuteToolCallsPostToolUse 执行完成的工具触发 PostToolUse 钩子（载荷含工具名与
// 截断结果）；被门禁阻断的调用未真正执行，不触发。
func TestExecuteToolCallsPostToolUse(t *testing.T) {
	exec := &fakeToolExecutor{}
	o := newTestOrchestrator(exec)

	type record struct {
		tool, result string
	}
	var records []record
	var mu sync.Mutex
	o.SetHookRunner(&fakeHookRunner{run: func(ctx context.Context, ev agenthook.Event, p agenthook.Payload) agenthook.Result {
		mu.Lock()
		defer mu.Unlock()
		if ev == agenthook.EventPostToolUse {
			records = append(records, record{tool: p.ToolName, result: p.ToolResult})
		}
		return agenthook.Result{}
	}}, agenthook.Payload{SessionKey: "g:1", AgentKind: agenthook.AgentKindMain})

	gate := func(ctx context.Context, call llmtool.ToolCall) (bool, string) {
		return call.Name == "blocked", "被阻断"
	}
	calls := []llmtool.ToolCall{
		{ID: "c1", Name: "blocked", Arguments: "{}"},
		{ID: "c2", Name: "good", Arguments: "{}"},
	}
	if _, err := o.executeToolCalls(context.Background(), calls, llmtool.CallBackFuncs{}, gate, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 || records[0].tool != "good" {
		t.Fatalf("只有真正执行的工具才触发 PostToolUse, records=%+v", records)
	}
	if records[0].result != "result:good" {
		t.Fatalf("PostToolUse 载荷结果不符: %q", records[0].result)
	}
}

// TestStreamToolRoundBoundary 流式模式下：工具边界触发 OnStreamRoundEnd、增量回调收到内容、
// 工具轮文本不由框架自动发送（流式由调用方经增量覆盖展示，SendText 一律不碰）。
func TestStreamToolRoundBoundary(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			// 第一轮：先输出一段文本，再发起工具调用
			fmt.Fprint(w, sseChunk(streamEvent(`[{"index":0,"delta":{"content":"先看下"}}]`)))
			fmt.Fprint(w, sseChunk(streamEvent(`[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"tool_a","arguments":"{}"}}]},"finish_reason":"tool_calls"}]`)))
		} else {
			fmt.Fprint(w, sseChunk(streamEvent(`[{"index":0,"delta":{"content":"最终回答"},"finish_reason":"stop"}]`)))
		}
		fmt.Fprint(w, sseChunk(usageEvent()))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		return "tool result", nil
	}}
	exec.tools = []llmtool.ToolDef{{Function: llmtool.FunctionDef{Name: "tool_a"}}}
	o := newTestOrchestrator(exec)
	o.SetMaxIterations(5)

	var deltas []string
	roundEnds := atomic.Int32{}
	sendTextCalls := atomic.Int32{}
	c := newTestClient(srv.URL)

	content, _, _, err := o.ExecuteWithTools(context.Background(), c,
		[]Message{TextMessage(RoleUser, "hi")},
		llmtool.CallBackFuncs{SendText: func(s string) (string, error) { sendTextCalls.Add(1); return "", nil }},
		ChatOptions{
			OnStreamDelta:    func(d string) { deltas = append(deltas, d) },
			OnStreamRoundEnd: func() { roundEnds.Add(1) },
		})
	if err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}
	if content != "最终回答" {
		t.Fatalf("最终内容不符: %q", content)
	}
	if got := strings.Join(deltas, ""); got != "先看下最终回答" {
		t.Fatalf("流式增量不符: %q", got)
	}
	if roundEnds.Load() != 1 {
		t.Fatalf("工具边界应触发一次 OnStreamRoundEnd, got %d", roundEnds.Load())
	}
	if sendTextCalls.Load() != 0 {
		t.Fatalf("流式模式下不应调用 SendText, got %d", sendTextCalls.Load())
	}
}

// TestNonStreamToolRoundTextNotSent 非流式模式下工具轮的 assistant 文本不自动发送：
// 中间轮常是「边想边说」的草稿，发出去会与最终回复重复刷屏；只有最后一轮（无工具
// 调用）的内容作为最终回复返回，由调用方发送。
func TestNonStreamToolRoundTextNotSent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			// 第一轮：带草稿文本 + 工具调用
			fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"先看看这张图，回个短的","tool_calls":[`+
				`{"id":"call_1","type":"function","function":{"name":"tool_a","arguments":"{}"}}`+
				`]},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"最终回答"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`)
	}))
	defer srv.Close()

	exec := &fakeToolExecutor{}
	exec.tools = []llmtool.ToolDef{{Function: llmtool.FunctionDef{Name: "tool_a"}}}
	o := newTestOrchestrator(exec)
	o.SetMaxIterations(5)

	var sent []string
	content, _, _, err := o.ExecuteWithTools(context.Background(), newTestClient(srv.URL),
		[]Message{TextMessage(RoleUser, "看看图")},
		llmtool.CallBackFuncs{SendText: func(s string) (string, error) {
			sent = append(sent, s)
			return "发送成功", nil
		}},
		ChatOptions{})
	if err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}
	if content != "最终回答" {
		t.Fatalf("最终内容不符: %q", content)
	}
	if len(sent) != 0 {
		t.Fatalf("非流式模式下工具轮文本不应自动发送, got %q", sent)
	}
}

// TestExecuteToolCallsTruncatedSkipped 截断调用被跳过：工具不执行，
// 以明确的截断提示回填（模型当轮即可学会分段重试）。
func TestExecuteToolCallsTruncatedSkipped(t *testing.T) {
	var ran []string
	var mu sync.Mutex
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		mu.Lock()
		ran = append(ran, call.Name)
		mu.Unlock()
		return "result:" + call.Name, nil
	}}
	o := newTestOrchestrator(exec)

	results, err := o.executeToolCalls(context.Background(),
		[]llmtool.ToolCall{
			{ID: "c1", Name: "write_file", Arguments: `{"path":"a.go","content":"pack`},
			{ID: "c2", Name: "time", Arguments: "{}"},
		}, llmtool.CallBackFuncs{}, nil, map[int]bool{0: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("期望 2 条结果消息, got %d", len(results))
	}
	got := ExtractMessageText(results[0])
	if !strings.Contains(got, "截断") || !strings.Contains(got, "append=true") {
		t.Fatalf("截断提示应包含分段重试指引: %q", got)
	}
	if got := ExtractMessageText(results[1]); got != "result:time" {
		t.Fatalf("未截断的调用应正常执行: %q", got)
	}
	if len(ran) != 1 || ran[0] != "time" {
		t.Fatalf("截断调用不应执行, got %v", ran)
	}
}

// TestExecuteWithToolsTruncatedToolCall 端到端：finish_reason=length 且工具参数
// 被截断时——合法调用正常执行、截断调用不执行并回填截断提示、回放的 assistant
// 消息参数被清洗为合法空对象（anthropic 格式以 RawMessage 回传，半截 JSON 会
// 破坏下一轮请求体）。
func TestExecuteWithToolsTruncatedToolCall(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			// 第一轮：完整调用 time + 被 max_tokens 截断的 write_file
			fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
				`"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[`+
				`{"id":"call_1","type":"function","function":{"name":"time","arguments":"{}"}},`+
				`{"id":"call_2","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a.go\",\"content\":\"pack"}}`+
				`]},"finish_reason":"length"}],`+
				`"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"已分段写入完成"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`)
	}))
	defer srv.Close()

	var ran []string
	var mu sync.Mutex
	exec := &fakeToolExecutor{fn: func(ctx context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
		mu.Lock()
		ran = append(ran, call.Name)
		mu.Unlock()
		return "result:" + call.Name, nil
	}}
	exec.tools = []llmtool.ToolDef{
		{Function: llmtool.FunctionDef{Name: "time"}},
		{Function: llmtool.FunctionDef{Name: "write_file"}},
	}
	o := newTestOrchestrator(exec)
	o.SetMaxIterations(5)
	c := newTestClient(srv.URL)

	content, messages, _, err := o.ExecuteWithTools(context.Background(), c,
		[]Message{TextMessage(RoleUser, "写个长文件")}, llmtool.CallBackFuncs{}, ChatOptions{})
	if err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}
	if content != "已分段写入完成" {
		t.Fatalf("最终内容不符: %q", content)
	}
	if len(ran) != 1 || ran[0] != "time" {
		t.Fatalf("只有完整调用应被执行, got %v", ran)
	}

	// 回放消息检查：assistant 的截断参数被清洗为 "{}"，工具结果带截断提示
	var assistantMsg, truncResult *Message
	for i := range messages {
		switch {
		case messages[i].Role == RoleAssistant && len(messages[i].ToolCalls) == 2:
			assistantMsg = &messages[i]
		case messages[i].Role == RoleTool && strings.Contains(ExtractMessageText(messages[i]), "截断"):
			truncResult = &messages[i]
		}
	}
	if assistantMsg == nil {
		t.Fatal("缺少带 2 个工具调用的 assistant 消息")
	}
	if args := assistantMsg.ToolCalls[1].Arguments; !json.Valid([]byte(args)) || args != "{}" {
		t.Fatalf("截断参数应被清洗为空对象, got %q", args)
	}
	if truncResult == nil || truncResult.ToolCallID != "call_2" {
		t.Fatalf("write_file 的截断结果消息不符: %+v", truncResult)
	}
	if !strings.Contains(ExtractMessageText(*truncResult), "append=true") {
		t.Fatalf("截断提示应包含分段指引: %q", ExtractMessageText(*truncResult))
	}
}

// TestExecuteToolCallsPostToolUseContextFeedback PostToolUse 钩子返回的 Context
// 应作为附加反馈拼到工具结果后回填给模型；Context 为空时结果保持原样。
func TestExecuteToolCallsPostToolUseContextFeedback(t *testing.T) {
	exec := &fakeToolExecutor{}
	o := newTestOrchestrator(exec)
	o.SetHookRunner(&fakeHookRunner{run: func(ctx context.Context, ev agenthook.Event, p agenthook.Payload) agenthook.Result {
		if ev != agenthook.EventPostToolUse {
			return agenthook.Result{}
		}
		if p.ToolName == "good" {
			return agenthook.Result{Context: "lint: unused variable x in line 3"}
		}
		return agenthook.Result{}
	}}, agenthook.Payload{SessionKey: "g:1", AgentKind: agenthook.AgentKindMain})

	results, err := o.executeToolCalls(context.Background(),
		[]llmtool.ToolCall{
			{ID: "c1", Name: "good", Arguments: "{}"},
			{ID: "c2", Name: "plain", Arguments: "{}"},
		}, llmtool.CallBackFuncs{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("期望 2 条结果消息, got %d", len(results))
	}
	got := results[0].Parts[0].Text
	if !strings.Contains(got, "result:good") || !strings.Contains(got, "[钩子附加反馈] lint: unused variable x") {
		t.Fatalf("钩子反馈应拼到工具结果后: %q", got)
	}
	plain := results[1].Parts[0].Text
	if strings.Contains(plain, "钩子附加反馈") {
		t.Fatalf("空 Context 不应拼接反馈: %q", plain)
	}
}
