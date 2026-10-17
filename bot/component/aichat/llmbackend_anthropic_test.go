package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// anthropicUsageJSON 构造 Anthropic usage JSON 片段。
func anthropicUsageJSON(input, output, cacheCreation, cacheRead int) string {
	return fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d,`+
		`"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}`, input, output, cacheCreation, cacheRead)
}

// anthropicStreamEvent 构造一条 Anthropic SSE 帧。
func anthropicStreamEvent(eventType, payload string) string {
	return "event: " + eventType + "\ndata: " + payload + "\n\n"
}

// anthropicTextStream 构造一段纯文本回复的完整 SSE 流（content 为内容块 JSON 数组）。
// anthropic 后端的非流式 generate 内部也走流式通道，假服务器统一以 SSE 应答。
func anthropicTextStream(content string, usage string) string {
	var sb strings.Builder
	sb.WriteString(anthropicStreamEvent("message_start",
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":`+usage+`}}`))
	sb.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	sb.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+content+`}}`))
	sb.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":0}`))
	sb.WriteString(anthropicStreamEvent("message_delta",
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":`+usageOutputTokens(usage)+`}}`))
	sb.WriteString(anthropicStreamEvent("message_stop", `{"type":"message_stop"}`))
	return sb.String()
}

// usageOutputTokens 从 usage JSON 中取出 output_tokens 数字（测试拼装用）。
func usageOutputTokens(usage string) string {
	var u struct {
		OutputTokens int `json:"output_tokens"`
	}
	_ = json.Unmarshal([]byte(usage), &u)
	return fmt.Sprintf("%d", u.OutputTokens)
}

// anthropicSSEReply 返回以 SSE 应答的 handler（body 为完整 SSE 流），并捕获请求体。
func anthropicSSEReply(sseBody string, reqBody *map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reqBody != nil {
			_ = json.NewDecoder(r.Body).Decode(reqBody)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody)
	}
}

// TestAnthropicTokenUsage 用量转换：PromptTokens/TotalTokens 必须包含缓存写入
// 与缓存命中（Anthropic 的 input_tokens 不含这两者，官方口径总输入为三者之和）。
func TestAnthropicTokenUsage(t *testing.T) {
	u := anthropicTokenUsage(anthropic.Usage{
		InputTokens:              5,
		OutputTokens:             3,
		CacheCreationInputTokens: 4,
		CacheReadInputTokens:     2,
	})
	if u.PromptTokens != 11 || u.CompletionTokens != 3 || u.TotalTokens != 14 || u.CachedTokens != 2 {
		t.Fatalf("usage 不符: %+v", u)
	}
}

// TestAnthropicThinkingBlocksReplayedThroughToolLoop 开启扩展思考后，带 tool_use
// 的 assistant 轮在下一轮请求里必须带回 thinking 块。
//
// Anthropic 要求思考块原样回传，缺失时上游直接拒绝整轮请求。曾出现的缺陷是
// GenerateResponse.ThinkingBlocks 由累积器产出却从未落到 Message 上（字段成了
// 死字段），只有在做完整工具循环时才会暴露，故这里走 ExecuteWithTools 全链路。
func TestAnthropicThinkingBlocksReplayedThroughToolLoop(t *testing.T) {
	var bodies []map[string]any

	toolStream := strings.Builder{}
	toolStream.WriteString(anthropicStreamEvent("message_start",
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":`+anthropicUsageJSON(10, 2, 0, 0)+`}}`))
	toolStream.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
	toolStream.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"用户想知道时间"}}`))
	toolStream.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_round1"}}`))
	toolStream.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":0}`))
	toolStream.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_42","name":"now","input":{}}}`))
	toolStream.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`))
	toolStream.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":1}`))
	toolStream.WriteString(anthropicStreamEvent("message_delta",
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`))
	toolStream.WriteString(anthropicStreamEvent("message_stop", `{"type":"message_stop"}`))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			fmt.Fprint(w, toolStream.String())
			return
		}
		fmt.Fprint(w, anthropicTextStream(`"现在是 12:00"`, anthropicUsageJSON(20, 4, 0, 0)))
	}))
	defer srv.Close()

	client, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	orchestrator := NewToolOrchestrator(&stubToolExecutor{
		tools: []llmtool.ToolDef{{Type: "function", Function: llmtool.FunctionDef{
			Name: "now", Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}}},
		execute: func(llmtool.ToolCall) (string, error) { return "12:00", nil },
	}, NewMessageBuilder("系统提示"))

	if _, _, _, err := orchestrator.ExecuteWithTools(context.Background(),
		client, []Message{TextMessage(RoleUser, "现在几点")}, llmtool.CallBackFuncs{}, ChatOptions{}); err != nil {
		t.Fatalf("ExecuteWithTools 失败: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("应有两次 LLM 请求（工具轮 + 收尾轮），实际 %d", len(bodies))
	}
	msgs := bodies[1]["messages"].([]any)
	var asst map[string]any
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "assistant" {
			asst = mm
			break
		}
	}
	if asst == nil {
		t.Fatalf("第二轮请求缺少 assistant 消息: %v", msgs)
	}
	blocks := asst["content"].([]any)
	if len(blocks) < 2 {
		t.Fatalf("assistant 应含 thinking + tool_use 块, got %v", blocks)
	}
	b0 := blocks[0].(map[string]any)
	if b0["type"] != "thinking" || b0["signature"] != "sig_round1" {
		t.Fatalf("思考块未随 tool_use 轮回传（上游会拒绝该请求）: %v", b0)
	}
	if b0["thinking"] != "用户想知道时间" {
		t.Fatalf("思考内容未回传: %v", b0)
	}
	if b1 := blocks[1].(map[string]any); b1["type"] != "tool_use" || b1["id"] != "toolu_42" {
		t.Fatalf("tool_use 块不符: %v", b1)
	}
}

// stubToolExecutor 固定返回同一批工具定义与结果，供编排层测试使用。
type stubToolExecutor struct {
	tools   []llmtool.ToolDef
	execute func(llmtool.ToolCall) (string, error)
}

func (s *stubToolExecutor) Tools() []llmtool.ToolDef { return s.tools }

func (s *stubToolExecutor) Execute(_ context.Context, call llmtool.ToolCall, _ llmtool.CallBackFuncs) (string, error) {
	if s.execute == nil {
		return "", nil
	}
	return s.execute(call)
}

// TestAnthropicGenerate 基本生成：system 走独立参数、文本解析与用量转换（含缓存命中）。
func TestAnthropicGenerate(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(anthropicSSEReply(
		anthropicTextStream(`"hi"`, anthropicUsageJSON(5, 3, 4, 2)), &reqBody))
	defer srv.Close()

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	resp, usage, err := c.Generate(context.Background(), []Message{
		TextMessage(RoleSystem, "你是助手"),
		TextMessage(RoleUser, "hello"),
	}, ChatOptions{})
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if resp.Content != "hi" {
		t.Fatalf("content = %q", resp.Content)
	}
	if usage.PromptTokens != 11 || usage.CompletionTokens != 3 || usage.TotalTokens != 14 || usage.CachedTokens != 2 {
		t.Fatalf("usage 不符: %+v", usage)
	}
	// system 应在独立参数而非 messages 中
	sys, ok := reqBody["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system 参数缺失或形态不对: %v", reqBody["system"])
	}
	msgs := reqBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages 应只有 user 一条, got %d", len(msgs))
	}
	if reqBody["max_tokens"].(float64) != anthropicDefaultMaxTokens {
		t.Fatalf("max_tokens 默认应为 %d, got %v", anthropicDefaultMaxTokens, reqBody["max_tokens"])
	}
}

// TestAnthropicGenerateToolCallWithThinking 带 thinking 与 tool_use 的响应解析：
// 思考文本进 ReasoningContent，带签名的思考块进 ThinkingBlocks，tool_use 转 ToolCall。
func TestAnthropicGenerateToolCallWithThinking(t *testing.T) {
	var sse strings.Builder
	sse.WriteString(anthropicStreamEvent("message_start",
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":`+anthropicUsageJSON(10, 1, 0, 0)+`}}`))
	sse.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先想想"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_abc"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":0}`))
	sse.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"enc_xyz"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":1}`))
	sse.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"time","input":{}}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"utc\"}"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":2}`))
	sse.WriteString(anthropicStreamEvent("message_delta",
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":6}}`))
	sse.WriteString(anthropicStreamEvent("message_stop", `{"type":"message_stop"}`))

	srv := httptest.NewServer(anthropicSSEReply(sse.String(), nil))
	defer srv.Close()

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	resp, _, err := c.Generate(context.Background(), []Message{TextMessage(RoleUser, "hello")}, ChatOptions{})
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if resp.ReasoningContent != "先想想" {
		t.Fatalf("ReasoningContent = %q", resp.ReasoningContent)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_1" ||
		resp.ToolCalls[0].Name != "time" || resp.ToolCalls[0].Arguments != `{"tz":"utc"}` {
		t.Fatalf("tool call 解析不符: %+v", resp.ToolCalls)
	}
	var tbs []thinkingBlock
	if err := json.Unmarshal(resp.ThinkingBlocks, &tbs); err != nil {
		t.Fatalf("ThinkingBlocks 反序列化失败: %v", err)
	}
	if len(tbs) != 2 || tbs[0].Type != "thinking" || tbs[0].Signature != "sig_abc" ||
		tbs[1].Type != "redacted_thinking" || tbs[1].Data != "enc_xyz" {
		t.Fatalf("ThinkingBlocks 内容不符: %+v", tbs)
	}
}

// TestAnthropicThinkingReplayAndToolRoundtrip 多轮回放：assistant 的思考块必须
// 原样回传且位于 tool_use 之前；tool 结果归属 user 角色。
func TestAnthropicThinkingReplayAndToolRoundtrip(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(anthropicSSEReply(
		anthropicTextStream(`"done"`, anthropicUsageJSON(1, 1, 0, 0)), &reqBody))
	defer srv.Close()

	thinkingRaw := json.RawMessage(`[{"type":"thinking","thinking":"先想想","signature":"sig_abc"}]`)
	history := []Message{
		TextMessage(RoleUser, "现在几点"),
		{
			Role:           RoleAssistant,
			ToolCalls:      []llmtool.ToolCall{{ID: "toolu_1", Name: "time", Arguments: `{"tz":"utc"}`}},
			ThinkingBlocks: thinkingRaw,
		},
		{Role: RoleTool, ToolCallID: "toolu_1", Parts: []ContentPart{TextPart("12:00")}},
	}

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	if _, _, err := c.Generate(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}

	msgs := reqBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("应为 user/assistant/user 三条, got %d: %v", len(msgs), msgs)
	}
	asst := msgs[1].(map[string]any)
	blocks := asst["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("assistant 应为 thinking+tool_use 两块, got %d: %v", len(blocks), blocks)
	}
	b0 := blocks[0].(map[string]any)
	if b0["type"] != "thinking" || b0["signature"] != "sig_abc" {
		t.Fatalf("思考块未原样回传: %v", b0)
	}
	b1 := blocks[1].(map[string]any)
	if b1["type"] != "tool_use" || b1["id"] != "toolu_1" {
		t.Fatalf("tool_use 块不符: %v", b1)
	}
	// tool 结果在 user 消息中
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "user" {
		t.Fatalf("tool 结果应归属 user 角色, got %v", toolMsg["role"])
	}
	tr := toolMsg["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool_result 块不符: %v", tr)
	}
	// content 为文本块数组形态
	trContent := tr["content"].([]any)[0].(map[string]any)
	if trContent["text"] != "12:00" {
		t.Fatalf("tool_result 内容不符: %v", tr)
	}
}

// TestAnthropicThinkingParams 开启深度思考：budget_tokens 下发且 max_tokens 抬高，
// temperature/top_p/top_k 不下发（API 不允许共存）。
func TestAnthropicThinkingParams(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(anthropicSSEReply(
		anthropicTextStream(`"ok"`, anthropicUsageJSON(1, 1, 0, 0)), &reqBody))
	defer srv.Close()

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	effort := "high"
	temp, topP, topK := 0.5, 0.9, 20
	maxToken := 100 // 故意小于 budget，验证 max_tokens 被抬高
	_, _, err = c.Generate(context.Background(), []Message{TextMessage(RoleUser, "hi")}, ChatOptions{
		ReasoningEffort: &effort, Temperature: &temp, TopP: &topP, TopK: &topK, MaxToken: &maxToken,
	})
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}

	thinking, ok := reqBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking 参数未下发: %v", reqBody["thinking"])
	}
	budget := thinking["budget_tokens"].(float64)
	if budget != 32768 {
		t.Fatalf("high 档 budget 应为 32768, got %v", budget)
	}
	if reqBody["max_tokens"].(float64) <= budget {
		t.Fatalf("max_tokens 应大于 budget, got %v", reqBody["max_tokens"])
	}
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if _, exists := reqBody[k]; exists {
			t.Fatalf("开启 thinking 时不应下发 %s", k)
		}
	}
}

// TestAnthropicStream 流式：文本/思考/签名/tool_use 参数增量累积与用量提取。
func TestAnthropicStream(t *testing.T) {
	var sse strings.Builder
	sse.WriteString(anthropicStreamEvent("message_start",
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":`+anthropicUsageJSON(7, 1, 0, 3)+`}}`))
	sse.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_s"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":0}`))
	sse.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"你好"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"！"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":1}`))
	sse.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_9","name":"time","input":{}}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"utc\"}"}}`))
	sse.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":2}`))
	sse.WriteString(anthropicStreamEvent("message_delta",
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`))
	sse.WriteString(anthropicStreamEvent("message_stop", `{"type":"message_stop"}`))

	srv := httptest.NewServer(anthropicSSEReply(sse.String(), nil))
	defer srv.Close()

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	var deltas []string
	resp, usage, err := c.GenerateStream(context.Background(),
		[]Message{TextMessage(RoleUser, "hello")},
		ChatOptions{OnStreamDelta: func(d string) { deltas = append(deltas, d) }})
	if err != nil {
		t.Fatalf("GenerateStream 失败: %v", err)
	}
	if resp.Content != "你好！" {
		t.Fatalf("内容累积不符: %q", resp.Content)
	}
	if got := strings.Join(deltas, ""); got != "你好！" {
		t.Fatalf("增量回调序列不符: %q", got)
	}
	if resp.ReasoningContent != "想" {
		t.Fatalf("ReasoningContent = %q", resp.ReasoningContent)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_9" ||
		resp.ToolCalls[0].Arguments != `{"tz":"utc"}` {
		t.Fatalf("tool call 组装不符: %+v", resp.ToolCalls)
	}
	var tbs []thinkingBlock
	if err := json.Unmarshal(resp.ThinkingBlocks, &tbs); err != nil || len(tbs) != 1 ||
		tbs[0].Thinking != "想" || tbs[0].Signature != "sig_s" {
		t.Fatalf("ThinkingBlocks 不符: %v %+v", err, tbs)
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 9 || usage.TotalTokens != 19 || usage.CachedTokens != 3 {
		t.Fatalf("usage 不符: %+v", usage)
	}
}

// TestAnthropicStreamTruncated stop_reason=max_tokens：输出撞上限被截断
// （thinking 计入 max_tokens 时更易触发），resp.Truncated 必须置位。
func TestAnthropicStreamTruncated(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(anthropicStreamEvent("message_start",
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":`+anthropicUsageJSON(7, 1, 0, 0)+`}}`))
	sb.WriteString(anthropicStreamEvent("content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"write_file","input":{}}}`))
	sb.WriteString(anthropicStreamEvent("content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\",\"content\":\"pack"}}`))
	sb.WriteString(anthropicStreamEvent("content_block_stop", `{"type":"content_block_stop","index":0}`))
	sb.WriteString(anthropicStreamEvent("message_delta",
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":8}}`))
	sb.WriteString(anthropicStreamEvent("message_stop", `{"type":"message_stop"}`))

	srv := httptest.NewServer(anthropicSSEReply(sb.String(), nil))
	defer srv.Close()

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	resp, _, err := c.GenerateStream(context.Background(),
		[]Message{TextMessage(RoleUser, "写个长文件")}, ChatOptions{})
	if err != nil {
		t.Fatalf("GenerateStream 失败: %v", err)
	}
	if !resp.Truncated {
		t.Fatal("stop_reason=max_tokens 应置位 Truncated")
	}
	if len(resp.ToolCalls) != 1 || json.Valid([]byte(resp.ToolCalls[0].Arguments)) {
		t.Fatalf("截断的工具参数不应是合法 JSON: %+v", resp.ToolCalls)
	}
}

// TestConvertAnthropicMessagesPromptCache 缓存断点位置与 TTL：
// 启用时 system 最后一个块与最后一条消息的最后一个可缓存块打点；
// 禁用时与旧行为一致（不出现任何 cache_control）。
func TestConvertAnthropicMessagesPromptCache(t *testing.T) {
	messages := []Message{
		TextMessage(RoleSystem, "基础提示"),
		TextMessage(RoleSystem, "扩展提示"),
		TextMessage(RoleUser, "你好"),
		TextMessage(RoleUser, "今天天气"),
	}

	// 禁用：不出现任何断点
	msgs, system, err := convertAnthropicMessages(messages, PromptCacheConfig{})
	if err != nil {
		t.Fatalf("convertAnthropicMessages 失败: %v", err)
	}
	if len(system) != 2 || system[0].CacheControl.Type != "" || system[1].CacheControl.Type != "" {
		t.Fatalf("禁用缓存时 system 不应有断点: %+v", system)
	}
	for _, m := range msgs {
		for _, block := range m.Content {
			if block.OfText != nil && block.OfText.CacheControl.Type != "" {
				t.Fatalf("禁用缓存时消息块不应有断点: %+v", m.Content)
			}
		}
	}

	// 启用（默认 TTL）：断点只落在 system 末块与最后消息末块
	msgs, system, err = convertAnthropicMessages(messages, PromptCacheConfig{Enable: true})
	if err != nil {
		t.Fatalf("convertAnthropicMessages 失败: %v", err)
	}
	if system[0].CacheControl.Type != "" || system[1].CacheControl.Type != "ephemeral" {
		t.Fatalf("system 应只在末块打点: %+v", system)
	}
	if system[1].CacheControl.TTL != "" {
		t.Fatalf("默认 TTL 应为提供方默认（不显式下发）, got %q", system[1].CacheControl.TTL)
	}
	if len(msgs) != 2 {
		t.Fatalf("消息数不符: %d", len(msgs))
	}
	first := msgs[0]
	if first.Content[len(first.Content)-1].OfText.CacheControl.Type != "" {
		t.Fatalf("非最后消息不应打点: %+v", first.Content)
	}
	last := msgs[len(msgs)-1]
	if last.Content[len(last.Content)-1].OfText.CacheControl.Type != "ephemeral" {
		t.Fatalf("最后消息末块应有 ephemeral 断点: %+v", last.Content)
	}

	// TTL 1h
	_, system, err = convertAnthropicMessages(messages, PromptCacheConfig{Enable: true, TTL: "1h"})
	if err != nil {
		t.Fatalf("convertAnthropicMessages 失败: %v", err)
	}
	if system[1].CacheControl.TTL != anthropic.CacheControlEphemeralTTLTTL1h {
		t.Fatalf("TTL 应为 1h, got %q", system[1].CacheControl.TTL)
	}
}

// TestConvertAnthropicMessagesCacheToolResult tool calling 轮次中最后一条是
// tool 结果（user 角色 + tool_result 块），断点应落在最后一个 tool_result 块上，
// 保证下一轮工具调用能命中 system+历史+上一轮工具往返的缓存。
func TestConvertAnthropicMessagesCacheToolResult(t *testing.T) {
	messages := []Message{
		TextMessage(RoleSystem, "你是助手"),
		TextMessage(RoleUser, "查一下时间"),
		{Role: RoleAssistant, ToolCalls: []llmtool.ToolCall{{ID: "toolu_1", Name: "time", Arguments: `{}`}}},
		{Role: RoleTool, ToolCallID: "toolu_1", Parts: []ContentPart{TextPart("2026-08-07")}},
	}
	msgs, _, err := convertAnthropicMessages(messages, PromptCacheConfig{Enable: true})
	if err != nil {
		t.Fatalf("convertAnthropicMessages 失败: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.Content[len(last.Content)-1].OfToolResult == nil ||
		last.Content[len(last.Content)-1].OfToolResult.CacheControl.Type != "ephemeral" {
		t.Fatalf("最后 tool_result 块应有断点: %+v", last.Content)
	}
}

// TestAnthropicGeneratePromptCacheJSON 端到端：启用后请求体中 system 末块与
// 最后一条消息的最后一个内容块应序列化出 cache_control，供 Anthropic 建缓存。
func TestAnthropicGeneratePromptCacheJSON(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(anthropicSSEReply(
		anthropicTextStream(`"hi"`, anthropicUsageJSON(5, 3, 0, 2)), &reqBody))
	defer srv.Close()

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test",
		WithAPIFormat(APIFormatAnthropic),
		WithPromptCache(PromptCacheConfig{Enable: true, TTL: "5m"}))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	if _, _, err := c.Generate(context.Background(), []Message{
		TextMessage(RoleSystem, "你是助手"),
		TextMessage(RoleUser, "hello"),
	}, ChatOptions{}); err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}

	sys := reqBody["system"].([]any)
	lastSys := sys[len(sys)-1].(map[string]any)
	if _, ok := lastSys["cache_control"]; !ok {
		t.Fatalf("system 末块缺少 cache_control: %v", lastSys)
	}
	msgs := reqBody["messages"].([]any)
	lastMsg := msgs[len(msgs)-1].(map[string]any)
	content := lastMsg["content"].([]any)
	lastBlock := content[len(content)-1].(map[string]any)
	if _, ok := lastBlock["cache_control"]; !ok {
		t.Fatalf("最后消息末块缺少 cache_control: %v", lastBlock)
	}
}

// TestAnthropicGenerateNoPromptCacheByDefault 默认（未启用）时请求体保持旧形态，
// 不含任何 cache_control 字段——与上游不支持的代理保持兼容。
func TestAnthropicGenerateNoPromptCacheByDefault(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(anthropicSSEReply(
		anthropicTextStream(`"hi"`, anthropicUsageJSON(5, 3, 0, 2)), &reqBody))
	defer srv.Close()

	c, err := NewLLMClient(srv.URL, "test-key", "claude-test", WithAPIFormat(APIFormatAnthropic))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	if _, _, err := c.Generate(context.Background(), []Message{
		TextMessage(RoleSystem, "你是助手"),
		TextMessage(RoleUser, "hello"),
	}, ChatOptions{}); err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	raw, _ := json.Marshal(reqBody)
	if strings.Contains(string(raw), "cache_control") {
		t.Fatalf("默认请求体不应包含 cache_control: %s", raw)
	}
}
