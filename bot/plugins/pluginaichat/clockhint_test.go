package pluginaichat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// TestClockDeliveryHintLifecycle 验证投递提示的状态机：投递后产生提示，
// 请求成功（commit）后不再重复，提示注入期间到达的新投递仍会提示。
func TestClockDeliveryHintLifecycle(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, time.Minute)

	id := message.FromUint64(1001)
	if _, _, ok := m.deliveryHint(id, true, "qq_get_msg_history"); ok {
		t.Fatal("未投递时不应有提示")
	}

	m.noteDelivered(&ClockTask{ID: "1", Title: "早报", TargetType: clockTargetGroup, TargetID: "1001"})
	hint, seq, ok := m.deliveryHint(id, true, "qq_get_msg_history")
	if !ok {
		t.Fatal("投递后应产生提示")
	}
	if !strings.Contains(hint, "早报") || !strings.Contains(hint, "qq_get_msg_history") {
		t.Fatalf("提示应包含任务标题与历史消息工具名, got %q", hint)
	}
	if seq == 0 {
		t.Fatal("应返回投递序号")
	}

	// 请求失败（未 commit）时提示仍然有效，下一次发言会重新注入
	if _, _, ok := m.deliveryHint(id, true, ""); !ok {
		t.Fatal("未 commit 前提示应保持有效")
	}

	m.commitDeliveryHint(id, true, seq)
	if _, _, ok := m.deliveryHint(id, true, ""); ok {
		t.Fatal("已提示过的投递不应重复提示")
	}

	// 提示注入期间又有新投递：commit 旧序号不能把新投递一并吞掉
	m.noteDelivered(&ClockTask{ID: "2", Title: "午报", TargetType: clockTargetGroup, TargetID: "1001"})
	m.commitDeliveryHint(id, true, seq)
	hint, _, ok = m.deliveryHint(id, true, "")
	if !ok || !strings.Contains(hint, "午报") {
		t.Fatalf("新投递应再次提示, got ok=%v hint=%q", ok, hint)
	}

	// 同号群聊与私聊互不串扰
	if _, _, ok := m.deliveryHint(id, false, ""); ok {
		t.Fatal("同号私聊不应看到群聊的投递提示")
	}

	// 无标题任务回退占位名
	m.noteDelivered(&ClockTask{ID: "3", TargetType: clockTargetFriend, TargetID: "1001"})
	hint, _, ok = m.deliveryHint(id, false, "")
	if !ok || !strings.Contains(hint, "未命名任务") {
		t.Fatalf("空标题应回退占位名, got ok=%v hint=%q", ok, hint)
	}
}

// TestSendFinalOutputRecordsDelivery 验证只有真正发出的消息才登记投递提示：
// skip_output、空文本与发送失败都不应留下提示。
func TestSendFinalOutputRecordsDelivery(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, time.Minute)
	m.bot = &recordBot{}

	id := message.FromUint64(2001)
	task := &ClockTask{ID: "1", Title: "巡检", TargetType: clockTargetFriend, TargetID: "2001"}

	if m.sendFinalOutput(task, "无需汇报", true, "无异常") {
		t.Fatal("跳过输出时不应发送")
	}
	if _, _, ok := m.deliveryHint(id, false, ""); ok {
		t.Fatal("跳过输出不应登记投递")
	}
	if m.sendFinalOutput(task, "  ", false, "") {
		t.Fatal("空文本不应发送")
	}
	if _, _, ok := m.deliveryHint(id, false, ""); ok {
		t.Fatal("空文本不应登记投递")
	}
	if !m.sendFinalOutput(task, "有异常", false, "") {
		t.Fatal("正常回复应发送")
	}
	if _, _, ok := m.deliveryHint(id, false, ""); !ok {
		t.Fatal("发送成功后应登记投递")
	}
}

// TestDeliveryHintInjectedIntoMainChatOnce 端到端验证：定时任务播报后用户发言，
// 首个请求的用户消息前携带播报提示；提示只注入一次，且不随对话历史回放
// （PersistUserText 落盘注入前的原文）。
func TestDeliveryHintInjectedIntoMainChatOnce(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		fakePlainChatReply(w, "收到")
	}))
	defer srv.Close()

	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.PersistentStorage = newSQLPFake()
	p.clockManager = newClockManager(p, time.Minute)

	id := message.FromUint64(1001)
	key := sessionKey(id, true)
	p.rememberHistoryTool(key, "qq_get_msg_history")

	chat, err := aichat.NewChatBot(srv.URL, "k", "m", "系统提示", 128000, nil, nil)
	if err != nil {
		t.Fatalf("NewChatBot 失败: %v", err)
	}
	rb := &recordBot{}

	// 定时任务先播报（不写主对话历史）
	p.clockManager.noteDelivered(&ClockTask{ID: "1", Title: "早报", TargetType: clockTargetGroup, TargetID: "1001"})

	if ok := p.processChatBatch(context.Background(), rb, id, true, chat, []message.Message{testGroupMsg(1001, "今天天气怎么样")}); !ok {
		t.Fatal("首个 processChatBatch 应成功")
	}
	if ok := p.processChatBatch(context.Background(), rb, id, true, chat, []message.Message{testGroupMsg(1001, "好的")}); !ok {
		t.Fatal("第二个 processChatBatch 应成功")
	}

	if len(bodies) != 2 {
		t.Fatalf("应发起 2 次模型请求, got %d", len(bodies))
	}
	first := lastUserContent(bodies[0])
	if !strings.Contains(first, "【定时任务播报】") || !strings.Contains(first, "早报") || !strings.Contains(first, "qq_get_msg_history") {
		t.Fatalf("首个请求应携带播报提示, got %q", first)
	}
	if !strings.Contains(first, "今天天气怎么样") {
		t.Fatalf("播报提示不应挤掉用户消息, got %q", first)
	}
	// 第二个请求的历史里不应残留提示（落盘/回放用注入前原文）
	for _, body := range bodies[1:] {
		if all := allUserContents(body); strings.Contains(all, "【定时任务播报】") {
			t.Fatalf("播报提示不应进入对话历史回放, got %q", all)
		}
	}
}

// lastUserContent 返回请求体中最后一条 user 消息的文本（本轮输入）。
func lastUserContent(body map[string]any) string {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return ""
	}
	last := ""
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok || mm["role"] != "user" {
			continue
		}
		if s, ok := mm["content"].(string); ok {
			last = s
		}
	}
	return last
}

// allUserContents 拼接请求体中全部 user 消息的文本（含历史回放）。
func allUserContents(body map[string]any) string {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok || mm["role"] != "user" {
			continue
		}
		if s, ok := mm["content"].(string); ok {
			sb.WriteString(s)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
