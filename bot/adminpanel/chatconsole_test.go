package adminpanel

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// fakeChatConsoleSource 记录面板试聊接口的调用，用于校验 handler 行为。
type fakeChatConsoleSource struct {
	sessions []plugininfo.ChatConsoleSession
	messages []plugininfo.ChatConsoleMessage
	busy     bool

	created []plugininfo.ChatConsoleSessionCreate
	sent    []string
	deleted []string
	cleared []string
	stopped []string
	sendErr error
}

func (f *fakeChatConsoleSource) ChatConsoleSessions() []plugininfo.ChatConsoleSession {
	return f.sessions
}

func (f *fakeChatConsoleSource) ChatConsoleCreate(req plugininfo.ChatConsoleSessionCreate) (plugininfo.ChatConsoleSession, error) {
	f.created = append(f.created, req)
	sess := plugininfo.ChatConsoleSession{
		ID:         "s1",
		Title:      req.TargetName,
		TargetKind: req.TargetKind,
		TargetID:   req.TargetID,
		CreatedAt:  time.Now(),
	}
	f.sessions = append(f.sessions, sess)
	return sess, nil
}

func (f *fakeChatConsoleSource) ChatConsoleDelete(id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeChatConsoleSource) ChatConsoleMessages(id string, afterSeq int64) ([]plugininfo.ChatConsoleMessage, bool, error) {
	out := make([]plugininfo.ChatConsoleMessage, 0, len(f.messages))
	for _, m := range f.messages {
		if m.Seq > afterSeq {
			out = append(out, m)
		}
	}
	return out, f.busy, nil
}

func (f *fakeChatConsoleSource) ChatConsoleClear(id string) error {
	f.cleared = append(f.cleared, id)
	return nil
}

func (f *fakeChatConsoleSource) ChatConsoleSend(id, text string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, id+":"+text)
	return nil
}

func (f *fakeChatConsoleSource) ChatConsoleStop(id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}

// fakeContacts 实现 adapter.ContactsExt，返回固定群/好友列表。
type fakeContacts struct {
	groups  []message.GroupInfo
	friends []message.Friend
}

func (f fakeContacts) GetGroupList() (*[]message.GroupInfo, bool) { return &f.groups, true }
func (f fakeContacts) GetFriendList() (*[]message.Friend, bool)   { return &f.friends, true }

func newChatConsoleTestServer(src ChatConsoleSource, contacts []ContactSource) *Server {
	return &Server{opt: Options{ChatConsole: src, Contacts: contacts, Logger: slog.Default()}}
}

func chatConsoleMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chatconsole/targets", s.handleChatConsoleTargets)
	mux.HandleFunc("GET /api/chatconsole/sessions", s.handleChatConsoleSessions)
	mux.HandleFunc("POST /api/chatconsole/sessions", s.handleChatConsoleCreate)
	mux.HandleFunc("DELETE /api/chatconsole/sessions/{id}", s.handleChatConsoleDelete)
	mux.HandleFunc("POST /api/chatconsole/sessions/{id}/clear", s.handleChatConsoleClear)
	mux.HandleFunc("GET /api/chatconsole/sessions/{id}/messages", s.handleChatConsoleMessages)
	mux.HandleFunc("POST /api/chatconsole/sessions/{id}/send", s.handleChatConsoleSend)
	mux.HandleFunc("POST /api/chatconsole/sessions/{id}/stop", s.handleChatConsoleStop)
	return mux
}

func TestHandleChatConsoleTargets(t *testing.T) {
	s := newChatConsoleTestServer(&fakeChatConsoleSource{}, []ContactSource{{
		Name:     "napcat",
		Platform: "qq",
		Contacts: fakeContacts{
			groups: []message.GroupInfo{{GroupID: message.FromString("123456"), GroupName: "测试群"}},
			friends: []message.Friend{
				{UserID: message.FromString("10000"), Nickname: "Neko", Remark: "同事"},
				{UserID: message.FromString("10001"), Nickname: "Momo"},
			},
		},
	}})
	mux := chatConsoleMux(s)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/chatconsole/targets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []plugininfo.ChatConsoleTarget `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Items) != 4 {
		t.Fatalf("期望 4 个目标（通用 + 1 群 + 2 好友），实际 %d: %+v", len(got.Items), got.Items)
	}
	if got.Items[0].Kind != plugininfo.ChatConsoleTargetTest {
		t.Fatalf("首个目标应为通用试聊: %+v", got.Items[0])
	}
	if got.Items[1].Kind != plugininfo.ChatConsoleTargetGroup || got.Items[1].ID != "qq:123456" || got.Items[1].Name != "测试群" {
		t.Fatalf("群目标不符: %+v", got.Items[1])
	}
	// 好友备注优先作为展示名（为空回退昵称）
	if got.Items[2].Name != "Momo" || got.Items[3].Name != "同事" {
		t.Fatalf("好友排序/名称不符: %+v %+v", got.Items[2], got.Items[3])
	}
}

func TestHandleChatConsoleCreate(t *testing.T) {
	src := &fakeChatConsoleSource{}
	s := newChatConsoleTestServer(src, nil)
	mux := chatConsoleMux(s)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions",
		strings.NewReader(`{"target_kind":"group","target_id":"qq:123456","target_name":"测试群"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(src.created) != 1 || src.created[0].TargetID != "qq:123456" {
		t.Fatalf("创建请求未透传: %+v", src.created)
	}

	// 通用试聊：target_id 被清空，目标名固定
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions",
		strings.NewReader(`{"target_kind":"test","target_id":"ignored","target_name":"随便"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if src.created[1].TargetID != "" || src.created[1].TargetName != "通用试聊" {
		t.Fatalf("通用试聊参数不符: %+v", src.created[1])
	}

	cases := []struct {
		name string
		body string
	}{
		{"非法类型", `{"target_kind":"bogus"}`},
		{"缺目标 ID", `{"target_kind":"friend"}`},
		{"标题过长", `{"target_kind":"test","title":"` + strings.Repeat("长", 65) + `"}`},
	}
	for _, c := range cases {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions", strings.NewReader(c.body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: 期望 400，实际 %d（%s）", c.name, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleChatConsoleSessions(t *testing.T) {
	src := &fakeChatConsoleSource{sessions: []plugininfo.ChatConsoleSession{{ID: "s1", Title: "试聊"}}}
	s := newChatConsoleTestServer(src, nil)
	mux := chatConsoleMux(s)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/chatconsole/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []plugininfo.ChatConsoleSession `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "s1" {
		t.Fatalf("会话列表不符: %+v", got.Items)
	}

	// 无会话时返回空数组而非 null
	rec = httptest.NewRecorder()
	chatConsoleMux(newChatConsoleTestServer(&fakeChatConsoleSource{}, nil)).
		ServeHTTP(rec, httptest.NewRequest("GET", "/api/chatconsole/sessions", nil))
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("空列表应序列化为 []: %s", rec.Body.String())
	}
}

func TestHandleChatConsoleMessages(t *testing.T) {
	src := &fakeChatConsoleSource{
		messages: []plugininfo.ChatConsoleMessage{
			{Seq: 1, Role: plugininfo.ChatConsoleRoleUser, Text: "你好", Status: plugininfo.ChatConsoleStatusOK},
			{Seq: 2, Role: plugininfo.ChatConsoleRoleAssistant, Text: "在", Status: plugininfo.ChatConsoleStatusOK},
		},
		busy: true,
	}
	s := newChatConsoleTestServer(src, nil)
	mux := chatConsoleMux(s)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/chatconsole/sessions/s1/messages?after=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []plugininfo.ChatConsoleMessage `json:"items"`
		Busy  bool                            `json:"busy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Seq != 2 || !got.Busy {
		t.Fatalf("增量消息不符: %+v busy=%v", got.Items, got.Busy)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/chatconsole/sessions/s1/messages?after=-1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 after 应 400，实际 %d", rec.Code)
	}
}

func TestHandleChatConsoleSendAndStop(t *testing.T) {
	src := &fakeChatConsoleSource{}
	s := newChatConsoleTestServer(src, nil)
	mux := chatConsoleMux(s)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions/s1/send",
		strings.NewReader(`{"text":" 你好 "}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(src.sent) != 1 || src.sent[0] != "s1:你好" {
		t.Fatalf("发送请求未透传: %+v", src.sent)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions/s1/send", strings.NewReader(`{"text":"  "}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空消息应 400，实际 %d", rec.Code)
	}

	// 生成中再次发送：插件返回错误 → 409
	src.sendErr = errTestBusy
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions/s1/send", strings.NewReader(`{"text":"再来"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("生成中发送应 409，实际 %d", rec.Code)
	}
	src.sendErr = nil

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions/s1/stop", nil))
	if rec.Code != http.StatusOK || len(src.stopped) != 1 {
		t.Fatalf("停止请求未透传: %d %+v", rec.Code, src.stopped)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chatconsole/sessions/s1/clear", nil))
	if rec.Code != http.StatusOK || len(src.cleared) != 1 {
		t.Fatalf("清空请求未透传: %d %+v", rec.Code, src.cleared)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/chatconsole/sessions/s1", nil))
	if rec.Code != http.StatusOK || len(src.deleted) != 1 {
		t.Fatalf("删除请求未透传: %d %+v", rec.Code, src.deleted)
	}
}

func TestHandleChatConsoleUnavailable(t *testing.T) {
	s := &Server{opt: Options{Logger: slog.Default()}}
	mux := chatConsoleMux(s)
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/api/chatconsole/sessions", nil),
		httptest.NewRequest("POST", "/api/chatconsole/sessions", strings.NewReader(`{"target_kind":"test"}`)),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: 未启用应 404，实际 %d", req.Method, req.URL.Path, rec.Code)
		}
	}
}

// errTestBusy 模拟「上一条还在生成」的插件错误。
var errTestBusy = &testBusyError{}

type testBusyError struct{}

func (e *testBusyError) Error() string {
	return "上一条消息还在生成中，请稍候或先停止"
}

// 确保测试用到的接口类型确实被实现
var _ ChatConsoleSource = (*fakeChatConsoleSource)(nil)
var _ adapter.ContactsExt = fakeContacts{}
