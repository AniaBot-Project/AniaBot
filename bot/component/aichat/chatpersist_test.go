package aichat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// TestChatPersistsUserMessage 本轮用户消息必须进入会话历史（窗口与落盘）：
// 此前落盘起点按 builtLen 取后缀会跳过用户消息，历史里只剩 assistant 回复，
// 模型跨轮看不到用户说过什么。同时校验第二轮请求体带上了第一轮的用户消息。
func TestChatPersistsUserMessage(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(bs))
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	store := &fakeHistoryStore{}
	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}

	if _, _, err := bot.Chat(context.Background(), "第一轮问题", llmtool.CallBackFuncs{}, ChatOptions{}); err != nil {
		t.Fatalf("第一轮 Chat: %v", err)
	}
	// 窗口与落盘都应包含用户消息 + 助手回复，且顺序为 user 在前
	assertUserBeforeAssistant(t, "窗口", bot.window.history())
	assertUserBeforeAssistant(t, "落盘", store.saved)

	if _, _, err := bot.Chat(context.Background(), "第二轮问题", llmtool.CallBackFuncs{}, ChatOptions{}); err != nil {
		t.Fatalf("第二轮 Chat: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(bodies))
	}
	// 第二轮请求体应同时带上两轮用户消息（含首轮）
	if !strings.Contains(bodies[1], "第一轮问题") || !strings.Contains(bodies[1], "第二轮问题") {
		t.Fatalf("第二轮请求体缺少历史用户消息: %s", bodies[1])
	}
}

// assertUserBeforeAssistant 断言末尾两条为 [user, assistant]。
func assertUserBeforeAssistant(t *testing.T, label string, msgs []Message) {
	t.Helper()
	if len(msgs) < 2 {
		t.Fatalf("%s 历史仅 %d 条: %+v", label, len(msgs), msgs)
	}
	got := msgs[len(msgs)-2:]
	if got[0].Role != RoleUser || got[1].Role != RoleAssistant {
		t.Fatalf("%s 末尾应为 [user, assistant], got %+v", label, got)
	}
}

// TestChatPersistUserTextStripsInjection 注入文本只作用于当轮请求：请求体含注入，
// 但历史（窗口与落盘）保存的是注入前的原始用户消息。
func TestChatPersistUserTextStripsInjection(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(bs))
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	store := &fakeHistoryStore{}
	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}

	injected := "【长期记忆】[abc12345] 小明喜欢喝咖啡\n\n原始用户问题"
	if _, _, err := bot.Chat(context.Background(), injected, llmtool.CallBackFuncs{}, ChatOptions{
		PersistUserText: "原始用户问题",
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// 当轮请求体带注入
	if !strings.Contains(bodies[0], "【长期记忆】") {
		t.Fatalf("当轮请求体应包含注入内容: %s", bodies[0])
	}
	// 历史中无注入、有原始消息
	assertNoInjection(t, "窗口", bot.window.history())
	assertNoInjection(t, "落盘", store.saved)
}

// assertNoInjection 断言历史里不含注入标记，且用户消息为注入前原文。
func assertNoInjection(t *testing.T, label string, msgs []Message) {
	t.Helper()
	for _, m := range msgs {
		if text := ExtractMessageText(m); strings.Contains(text, "【长期记忆】") {
			t.Fatalf("%s 历史不应包含注入内容: %q", label, text)
		}
	}
	if len(msgs) == 0 {
		t.Fatalf("%s 历史为空", label)
	}
	last := msgs[len(msgs)-1]
	if last.Role != RoleAssistant {
		t.Fatalf("%s 末条应为 assistant: %+v", label, last)
	}
	user := msgs[len(msgs)-2]
	if user.Role != RoleUser || !strings.Contains(ExtractMessageText(user), "原始用户问题") {
		t.Fatalf("%s 用户消息应为注入前原文: %+v", label, user)
	}
}

// TestChatPersistUserTextAlsoStripsHookContext UserPromptSubmit 钩子注入的上下文
// 同样只作用于当轮：历史保存注入前原文（钩子每轮都会重新触发，无需回放）。
func TestChatPersistUserTextAlsoStripsHookContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	store := &fakeHistoryStore{}
	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	bot.SetHookRunner(&fakeHookRunner{run: func(ctx context.Context, ev agenthook.Event, p agenthook.Payload) agenthook.Result {
		if ev == agenthook.EventUserPromptSubmit {
			return agenthook.Result{Context: "【钩子上下文】"}
		}
		return agenthook.Result{}
	}}, "g:1", agenthook.AgentKindMain)

	if _, _, err := bot.Chat(context.Background(), "原始用户问题", llmtool.CallBackFuncs{}, ChatOptions{
		PersistUserText: "原始用户问题",
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for _, m := range store.saved {
		if text := ExtractMessageText(m); strings.Contains(text, "【钩子上下文】") {
			t.Fatalf("历史不应包含钩子注入: %q", text)
		}
	}
}

// TestChatPersistUserTextEmptyKeepsInput 未提供 PersistUserText 时保持旧语义：
// 历史保存请求实际使用的用户输入（含调用方自行拼入的内容）。
func TestChatPersistUserTextEmptyKeepsInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	store := &fakeHistoryStore{}
	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	if _, _, err := bot.Chat(context.Background(), "带前缀的输入", llmtool.CallBackFuncs{}, ChatOptions{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(store.saved) == 0 {
		t.Fatal("历史为空")
	}
	user := store.saved[len(store.saved)-2]
	if user.Role != RoleUser || !strings.Contains(ExtractMessageText(user), "带前缀的输入") {
		t.Fatalf("未指定 PersistUserText 时应保存实际输入: %+v", user)
	}
}

// TestChatPersistRoundTripSecondRequestUnchanged PersistUserText 的剥离不影响
// 本轮之后的请求：第二轮仍能在历史里看到第一轮的原始用户消息。
func TestChatPersistRoundTripSecondRequestUnchanged(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(bs))
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil, &fakeHistoryStore{})
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	if _, _, err := bot.Chat(context.Background(), "【注入】第一轮", llmtool.CallBackFuncs{}, ChatOptions{PersistUserText: "第一轮"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bot.Chat(context.Background(), "第二轮", llmtool.CallBackFuncs{}, ChatOptions{}); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &req); err != nil {
		t.Fatalf("解析第二轮请求体失败: %v", err)
	}
	var userTexts []string
	for _, m := range req.Messages {
		if m.Role == "user" {
			userTexts = append(userTexts, m.Content)
		}
	}
	if len(userTexts) != 2 || !strings.Contains(userTexts[0], "第一轮") || strings.Contains(userTexts[0], "【注入】") {
		t.Fatalf("第二轮应看到首轮原文（无注入）与次轮消息: %v", userTexts)
	}
}
