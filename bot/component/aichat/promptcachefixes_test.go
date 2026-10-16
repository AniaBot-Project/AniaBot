package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// requestUserText 从请求体提取最后一条 user 消息的文本（断言时间前缀一致性用）。
func requestUserText(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	t.Fatalf("请求体中没有 user 消息: %s", body)
	return ""
}

// TestPersistedUserMessageMatchesRequest 持久化的用户消息必须与请求中真正
// 发送的那一份逐字节一致（含时间前缀）：落盘时重新取时间戳会让下一轮请求的
// prompt 前缀缓存恰好在上一轮用户消息处整段分叉。
//
// 用 1.1s 的响应延迟跨过 RFC3339 秒边界，确保「落盘时重新生成时间戳」的
// 旧实现必然失败（若复用请求时间戳则两者相等）。
func TestPersistedUserMessageMatchesRequest(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(bs))
		// 跨秒边界：请求构建时间与响应（落盘）时间必然落在不同的秒
		time.Sleep(1100 * time.Millisecond)
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	store := &fakeHistoryStore{}
	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}

	if _, _, err := bot.Chat(context.Background(), "时间戳一致性", llmtool.CallBackFuncs{}, ChatOptions{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("expected 1 request, got %d", len(bodies))
	}
	sent := requestUserText(t, bodies[0])
	persisted := ""
	for _, m := range store.saved {
		if m.Role == RoleUser {
			persisted = ExtractMessageText(m)
		}
	}
	if persisted == "" {
		t.Fatal("落盘历史缺少用户消息")
	}
	if persisted != sent {
		t.Fatalf("落盘用户消息与请求不一致（前缀缓存会在该处分叉）:\n请求: %q\n落盘: %q", sent, persisted)
	}
}

// TestPersistedUserTextPrefixReusesRequestTime PersistUserText（剥离注入）路径
// 同样复用请求时间前缀，而不是落盘时重新取时间。
func TestPersistedUserTextPrefixReusesRequestTime(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(bs))
		time.Sleep(1100 * time.Millisecond)
		fakeChatHandler(w, r)
	}))
	defer srv.Close()

	store := &fakeHistoryStore{}
	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}

	if _, _, err := bot.Chat(context.Background(), "【长期记忆】注入内容\n\n原始问题", llmtool.CallBackFuncs{}, ChatOptions{
		PersistUserText: "原始问题",
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	sent := requestUserText(t, bodies[0])
	// 请求里的前缀 = 时间戳；落盘副本应携带同一前缀 + 注入前原文
	prefix := sent[:strings.Index(sent, "【长期记忆】")]
	persisted := ""
	for _, m := range store.saved {
		if m.Role == RoleUser {
			persisted = ExtractMessageText(m)
		}
	}
	if persisted != prefix+"原始问题" {
		t.Fatalf("落盘副本的时间前缀应复用请求时的取值:\n请求前缀: %q\n落盘全文: %q", prefix, persisted)
	}
}

// TestIsContextOverflowError 超限类错误识别：SDK 结构化 message 与普通错误文本
// 都能命中；无关错误不误报。
func TestIsContextOverflowError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New(`400 Bad Request: {"error":{"message":"This model's maximum context length is 65536 tokens","type":"invalid_request_error"}}`), true},
		{errors.New("context_length_exceeded"), true},
		{errors.New("prompt is too long: 210000 tokens > 200000 maximum"), true},
		{errors.New("connection reset by peer"), false},
		{errors.New("insufficient balance"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsContextOverflowError(c.err); got != c.want {
			t.Fatalf("IsContextOverflowError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestMaybeCompressHonorsForcedFlag 超限失败标记使下一轮强制执行一次压缩
// （即使用量未越过常规阈值），且标记只消费一次。
func TestMaybeCompressHonorsForcedFlag(t *testing.T) {
	calls := 0
	compressor := func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error) {
		calls++
		return []Message{TextMessage(RoleUser, "摘要")}, TokenUsage{}, nil
	}
	w := newMessageWindow(100000, nil, compressor, &fakeHistoryStore{})
	// llmClient 为 nil 时 MaybeCompress 直接返回：塞一个非 nil 占位
	w.llmClient = &LLMClient{}

	w.append(TextMessage(RoleUser, "一条很久以前的消息"))

	// 无标记：不压缩（lastPromptTokens 未超阈值）
	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("未触发条件时不应压缩，实际调用 %d 次", calls)
	}

	// 标记后：即使无 usage 也强制压缩一次
	w.markOverflowFailure()
	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("标记后应强制压缩一次，实际 %d 次", calls)
	}

	// 标记已被消费：再次调用不再压缩
	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("强制标记只应消费一次，实际累计 %d 次", calls)
	}
}

// TestChatMarksOverflowFailureOnOverflowError 请求以超限类错误失败时 Chat 标记
// 下一轮强制压缩（失败不更新 usage，历史可能永远够不到常规压缩阈值）。
func TestChatMarksOverflowFailureOnOverflowError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	store := &fakeHistoryStore{}
	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 8192, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	if _, _, err := bot.Chat(context.Background(), "撑爆上下文", llmtool.CallBackFuncs{}, ChatOptions{}); err == nil {
		t.Fatal("超限请求应返回错误")
	}
	if !bot.window.forceCompress {
		t.Fatal("超限失败后应标记下一轮强制压缩")
	}
	// 失败轮的历史不落盘（消息未产生），但标记必须已置位
	if len(store.saved) != 0 {
		t.Fatalf("失败轮不应落盘历史，实际 %d 条", len(store.saved))
	}
}

// TestChatDoesNotMarkOnUnrelatedError 普通错误（网络/额度）不应触发强制压缩。
func TestChatDoesNotMarkOnUnrelatedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":{"message":"insufficient balance","type":"insufficient_quota"}}`)
	}))
	defer srv.Close()

	bot, err := NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 8192, nil, &fakeHistoryStore{})
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	if _, _, err := bot.Chat(context.Background(), "余额不足", llmtool.CallBackFuncs{}, ChatOptions{}); err == nil {
		t.Fatal("请求应返回错误")
	}
	if bot.window.forceCompress {
		t.Fatal("非超限错误不应标记强制压缩")
	}
}
