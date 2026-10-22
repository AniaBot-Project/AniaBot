package pluginaichat

// 主动互动与上下文的一致性测试：
//   - 主动发出的内容必须能被「下一次对话」看到（写回真实历史）；
//   - 主动互动自己生成前要「只读」回放会话历史：看得到聊到哪儿了，但本次的
//     触发提示词与回复不能写进真实历史。
//
// 写回是必要的：主动互动的生成走一次性会话，发出的内容不会由 Chat 自动落盘，
// 若只发不写回，主对话就不知道「我刚说过什么」——用户接一句「你刚才说什么」，
// AI 的历史里只有用户消息，只能反问对方指什么。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeProactiveHistory 内存版 HistoryStore，用于验证写回路径。
type fakeProactiveHistory struct {
	mu    sync.Mutex
	saved []aichat.Message
}

func (f *fakeProactiveHistory) Load(context.Context) ([]aichat.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]aichat.Message(nil), f.saved...), nil
}

func (f *fakeProactiveHistory) Append(_ context.Context, msgs []aichat.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, msgs...)
	return nil
}

func (f *fakeProactiveHistory) Replace(context.Context, []aichat.Message) error { return nil }

func (f *fakeProactiveHistory) Clear(context.Context) error { return nil }

func (f *fakeProactiveHistory) messages() []aichat.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]aichat.Message(nil), f.saved...)
}

// TestRecordUtteranceWritesHistory 会话未驻留时直接把主动发言写进持久化历史，
// 下次重建会话（或被淘汰后）能随历史回放出来。
func TestRecordUtteranceWritesHistory(t *testing.T) {
	db := newTestSQLDB(t)
	p := &AIChatPlugin{historyDB: db}
	p.Logger = testLogger()
	m := newTestProactiveManager(t)
	m.plugin = p

	id := message.FromUint64(1001)
	m.recordUtterance(id, true, "明天记得交周报")

	store := newSQLHistoryStore(db, sessionKey(id, true), testLogger())
	msgs, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != aichat.RoleAssistant {
		t.Fatalf("应写回一条 assistant 消息: %+v", msgs)
	}
	if got := aichat.ExtractMessageText(msgs[0]); got != "明天记得交周报" {
		t.Fatalf("写回内容不符: %q", got)
	}
}

// TestRecordUtteranceAppendsCachedChat 会话驻留时追加到内存窗口（同时落盘），
// 不必等会话重建就能在下一轮对话里看到。
func TestRecordUtteranceAppendsCachedChat(t *testing.T) {
	store := &fakeProactiveHistory{}
	chat, err := aichat.NewChatBot("http://127.0.0.1:1", "k", "m", "p", 0, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	p := &AIChatPlugin{}
	p.Logger = testLogger()
	id := message.FromUint64(1002)
	p.chats.Store(sessionKey(id, true), newChatEntry(chat, id, true, "p"))

	m := newTestProactiveManager(t)
	m.plugin = p
	m.recordUtterance(id, true, "最近怎么样")

	msgs := store.messages()
	if len(msgs) != 1 || msgs[0].Role != aichat.RoleAssistant {
		t.Fatalf("驻留会话应把主动发言追加到窗口并落盘: %+v", msgs)
	}
	if got := aichat.ExtractMessageText(msgs[0]); got != "最近怎么样" {
		t.Fatalf("写回内容不符: %q", got)
	}
}

// TestRecentUtterancesFiltersAndOrders 回显只取真正发出去的（sent 且有内容），
// 且按发送时间正序排列：不要把排队中/被取消的意图当成「我说过的话」。
func TestRecentUtterancesFiltersAndOrders(t *testing.T) {
	m := newTestProactiveManager(t)
	now := testNoon()
	mustInsert(t, m.store, &ProactiveIntention{
		ID: "s1", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(-2 * time.Hour),
		Status: proactiveStatusSent, CreatedAt: now.Add(-3 * time.Hour),
		SentAt: now.Add(-3 * time.Hour), Reply: "第一句",
	})
	mustInsert(t, m.store, &ProactiveIntention{
		ID: "c1", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
		Status: proactiveStatusCancelled, CreatedAt: now.Add(-2 * time.Hour),
		Reply: "没说出去的话",
	})
	mustInsert(t, m.store, &ProactiveIntention{
		ID: "s2", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: now.Add(-time.Hour), ExpiresAt: now,
		Status: proactiveStatusSent, CreatedAt: now.Add(-time.Hour),
		SentAt: now.Add(-time.Hour), Reply: "第二句",
	})

	got := m.recentUtterances("g:1", 3)
	if strings.Contains(got, "没说出去的话") {
		t.Fatalf("未发出的意图不应回显: %q", got)
	}
	first, second := strings.Index(got, "第一句"), strings.Index(got, "第二句")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("应按发送时间正序回显: %q", got)
	}
}

// TestBuildPromptShowsRecentUtterances 一次性会话不读会话历史，提示词里要回显
// 最近几次主动发言，AI 才知道「我上次说了什么」、避免重复。
func TestBuildPromptShowsRecentUtterances(t *testing.T) {
	m := newTestProactiveManager(t)
	now := testNoon()
	mustInsert(t, m.store, &ProactiveIntention{
		ID: "u1", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
		Status: proactiveStatusSent, CreatedAt: now.Add(-2 * time.Hour),
		SentAt: now.Add(-2 * time.Hour), Reply: "昨天说的那句",
	})

	prompt := m.buildPrompt(&ProactiveIntention{
		ID: "u2", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: now, ExpiresAt: now.Add(time.Hour),
		Status: proactiveStatusPending, CreatedAt: now,
	}, message.FromUint64(1), true)

	if !strings.Contains(prompt, "你最近主动说过的话") || !strings.Contains(prompt, "昨天说的那句") {
		t.Fatalf("提示词应回显最近一次主动发言:\n%s", prompt)
	}
}

// TestReadOnlyHistoryDropsWrites 只读包装：Load 正常读，Append/Replace/Clear
// 一律丢弃（主动互动本次生成的触发词与回复不能进真实历史）。
func TestReadOnlyHistoryDropsWrites(t *testing.T) {
	inner := &fakeProactiveHistory{saved: []aichat.Message{
		aichat.TextMessage(aichat.RoleUser, "旧消息"),
	}}
	store := readOnlyHistory{inner: inner}
	ctx := context.Background()

	msgs, err := store.Load(ctx)
	if err != nil || len(msgs) != 1 || aichat.ExtractMessageText(msgs[0]) != "旧消息" {
		t.Fatalf("Load 应透传内层历史: %+v err=%v", msgs, err)
	}
	if err := store.Append(ctx, []aichat.Message{aichat.TextMessage(aichat.RoleAssistant, "新消息")}); err != nil {
		t.Fatalf("Append 不应报错: %v", err)
	}
	if err := store.Replace(ctx, nil); err != nil {
		t.Fatalf("Replace 不应报错: %v", err)
	}
	if err := store.Clear(ctx); err != nil {
		t.Fatalf("Clear 不应报错: %v", err)
	}
	if got := inner.messages(); len(got) != 1 || aichat.ExtractMessageText(got[0]) != "旧消息" {
		t.Fatalf("只读包装不应改动内层历史: %+v", got)
	}
}

// TestReadOnlyHistoryReplaysIntoChat 只读回放端到端：ChatBot 带着只读历史
// 发起请求时，请求体里能看到之前的对话；本次生成的触发词与回复只进内存窗口，
// 不会追加到内层历史。
func TestReadOnlyHistoryReplaysIntoChat(t *testing.T) {
	inner := &fakeProactiveHistory{saved: []aichat.Message{
		aichat.TextMessage(aichat.RoleUser, "昨天聊到的面试"),
		aichat.TextMessage(aichat.RoleAssistant, "祝你顺利"),
	}}
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, _ := io.ReadAll(r.Body)
		body = string(bs)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"今天面试怎么样"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	chat, err := aichat.NewChatBot(srv.URL, "test-key", "test-model", "系统提示词", 0, nil,
		readOnlyHistory{inner: inner})
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	chat.LoadHistory(context.Background())
	if _, _, err := chat.Chat(context.Background(), "【主动互动】决定要不要说点什么",
		llmtool.CallBackFuncs{}, aichat.ChatOptions{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !strings.Contains(body, "昨天聊到的面试") || !strings.Contains(body, "祝你顺利") {
		t.Fatalf("请求体应包含回放的会话历史: %s", body)
	}
	if got := inner.messages(); len(got) != 2 {
		t.Fatalf("本次生成不应写回真实历史，got %d 条: %+v", len(got), got)
	}
}
