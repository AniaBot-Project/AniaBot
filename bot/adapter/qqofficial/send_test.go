package qqofficial

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// TestParseOpenID ID 前缀解析：仅接受 qo: 前缀。
func TestParseOpenID(t *testing.T) {
	if raw, ok := parseOpenID("qo:ABC123"); !ok || raw != "ABC123" {
		t.Errorf("parseOpenID(qo:ABC123) = %q/%v", raw, ok)
	}
	if _, ok := parseOpenID("123456"); ok {
		t.Error("QQ 裸数字 ID 不应被接受")
	}
	if _, ok := parseOpenID("tg:123"); ok {
		t.Error("其他平台前缀 ID 不应被接受")
	}
	if _, ok := parseOpenID("qo:"); ok {
		t.Error("空 openid 不应被接受")
	}
}

// TestSplitText 按 rune 分包，保留 rune 完整性。
func TestSplitText(t *testing.T) {
	if parts := splitText("", 10); parts != nil {
		t.Fatalf("空文本 = %v", parts)
	}
	if parts := splitText("abc", 10); len(parts) != 1 || parts[0] != "abc" {
		t.Fatalf("短文本 = %v", parts)
	}
	long := strings.Repeat("汉", 100)
	parts := splitText(long, 30)
	if len(parts) != 4 {
		t.Fatalf("分包数 = %d, want 4", len(parts))
	}
	for i, p := range parts {
		if i < 3 && len([]rune(p)) != 30 {
			t.Errorf("parts[%d] rune 数 = %d", i, len([]rune(p)))
		}
	}
}

// TestReplyToken 被动回复凭证：序号递增、次数耗尽降级、过期降级。
func TestReplyToken(t *testing.T) {
	a := NewAdapter(nil)
	// 无凭证
	if _, _, ok := a.nextReplySeq("G", true); ok {
		t.Fatal("无凭证应 ok=false")
	}
	a.storeReplyToken("G", "M1")
	for want := 1; want <= 5; want++ {
		_, seq, ok := a.nextReplySeq("G", true)
		if !ok || seq != want {
			t.Fatalf("第 %d 次回复 seq=%d ok=%v", want, seq, ok)
		}
	}
	// 群聊 5 次耗尽
	if _, _, ok := a.nextReplySeq("G", true); ok {
		t.Fatal("群聊 5 次后应耗尽")
	}
	// 过期
	a.storeReplyToken("G2", "M2")
	v, _ := a.replyTokens.Load("G2")
	tok := v.(*replyToken)
	tok.mu.Lock()
	tok.at = time.Now().Add(-10 * time.Minute)
	tok.mu.Unlock()
	if _, _, ok := a.nextReplySeq("G2", true); ok {
		t.Fatal("过期凭证应 ok=false")
	}
	// 单聊 4 次上限、60 分钟有效
	a.storeReplyToken("U", "M3")
	for i := range 4 {
		if _, _, ok := a.nextReplySeq("U", false); !ok {
			t.Fatalf("单聊第 %d 次回复应有效", i+1)
		}
	}
	if _, _, ok := a.nextReplySeq("U", false); ok {
		t.Fatal("单聊 4 次后应耗尽")
	}
}

// mockQQServer 构造一个 OpenAPI 测试服务器，返回可编程响应。
// handler 按「方法 路径」注册，返回 (httpStatus, body)。
func mockQQServer(t *testing.T, handler func(method, path string, body []byte) (int, []byte)) (*qqOfficialAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		status, resp := handler(r.Method, r.URL.Path, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(resp)
	}))
	tm := newTokenManager("appid", "secret", resty.New())
	tm.token = "test-token"
	tm.expiresAt = time.Now().Add(time.Hour)
	a := NewAdapter(nil)
	a.client = newQQClient(srv.URL, tm)
	a.tokens = tm
	return a, srv
}

// TestSendGroupMsg 群文本消息：携带被动回复 msg_id+msg_seq；默认不自动引用。
func TestSendGroupMsg(t *testing.T) {
	var gotBody []byte
	var gotPath string
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		gotPath = path
		gotBody = body
		return 200, []byte(`{"id":"ROBOT1.0_sent","timestamp":"2026-07-21T10:00:00+08:00"}`)
	})
	defer srv.Close()

	a.storeReplyToken("GROUP1", "ROBOT1.0_evt")
	builder := msgchain.Builder().Group()
	builder.Text("你好")
	msgID, ok := a.SendGroupMsg("qo:GROUP1", builder.Build())
	if !ok {
		t.Fatal("发送应成功")
	}
	if msgID != "qo:ROBOT1.0_sent" {
		t.Errorf("返回消息 ID = %q", msgID)
	}
	if gotPath != "/v2/groups/GROUP1/messages" {
		t.Errorf("请求路径 = %q", gotPath)
	}
	var req sendMessageRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if req.MsgType != 0 || req.Content != "你好" {
		t.Errorf("文本消息 = %+v", req)
	}
	if req.MsgID != "ROBOT1.0_evt" || req.MsgSeq != 1 {
		t.Errorf("被动回复凭证 = %q/%d", req.MsgID, req.MsgSeq)
	}
	// 自动引用默认关闭：直接发送，不带引用气泡
	if req.MessageReference != nil {
		t.Errorf("默认不应携带引用: %+v", req.MessageReference)
	}
}

// TestSendGroupMsgAutoQuote 开启自动引用后，首条消息携带触发消息的 message_reference。
func TestSendGroupMsgAutoQuote(t *testing.T) {
	var gotBody []byte
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		gotBody = body
		return 200, []byte(`{"id":"ROBOT1.0_sent"}`)
	})
	defer srv.Close()

	a.cfg.autoQuote = true
	a.storeReplyToken("GROUP1", "ROBOT1.0_evt")
	builder := msgchain.Builder().Group()
	builder.Text("你好")
	if _, ok := a.SendGroupMsg("qo:GROUP1", builder.Build()); !ok {
		t.Fatal("发送应成功")
	}
	var req sendMessageRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if req.MessageReference == nil || req.MessageReference.MessageID != "ROBOT1.0_evt" {
		t.Errorf("自动引用丢失: %+v", req)
	}
	if req.MsgID != "ROBOT1.0_evt" || req.MsgSeq != 1 {
		t.Errorf("被动回复凭证 = %q/%d", req.MsgID, req.MsgSeq)
	}
}

// TestSendTextReferenceFirstPartOnly 长文本分包时仅首包携带引用，msg_seq 依次递增。
func TestSendTextReferenceFirstPartOnly(t *testing.T) {
	var calls []sendMessageRequest
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		var req sendMessageRequest
		_ = json.Unmarshal(body, &req)
		calls = append(calls, req)
		return 200, []byte(`{"id":"ROBOT1.0_part"}`)
	})
	defer srv.Close()

	a.storeReplyToken("G", "ROBOT1.0_evt")
	long := strings.Repeat("字", textRunesLimit+5)
	if _, ok := a.sendText(t.Context(), "G", true, long, "ROBOT1.0_ref"); !ok {
		t.Fatal("发送应成功")
	}
	if len(calls) != 2 {
		t.Fatalf("分包数 = %d, want 2", len(calls))
	}
	if calls[0].MessageReference == nil || calls[0].MessageReference.MessageID != "ROBOT1.0_ref" {
		t.Errorf("首包应携带引用: %+v", calls[0])
	}
	if calls[1].MessageReference != nil {
		t.Errorf("后续包不应携带引用: %+v", calls[1])
	}
	if calls[0].MsgSeq != 1 || calls[1].MsgSeq != 2 {
		t.Errorf("msg_seq = %d/%d, want 1/2", calls[0].MsgSeq, calls[1].MsgSeq)
	}
}

// TestSendChainNoImplicitReference 无被动回复凭证（如定时任务主动发送）时不携带引用。
func TestSendChainNoImplicitReference(t *testing.T) {
	var gotBody []byte
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		gotBody = body
		return 200, []byte(`{"id":"ROBOT1.0_noref"}`)
	})
	defer srv.Close()

	segs := []message.OB11Segment{
		{Type: message.SegmentText, Data: message.TextMessage{Text: "定时推送"}.Marshal()},
	}
	if _, ok := a.sendChain(t.Context(), "GROUP1", true, segs); !ok {
		t.Fatal("发送应成功")
	}
	var req sendMessageRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req.MessageReference != nil {
		t.Errorf("无凭证不应携带引用: %+v", req.MessageReference)
	}
	if req.MsgID != "" {
		t.Errorf("无凭证不应携带 msg_id: %q", req.MsgID)
	}
}

// TestPostMessageMsgIDExpired 被动回复凭证失效（msg_id 过期）降级主动消息重试。
func TestPostMessageMsgIDExpired(t *testing.T) {
	var calls []sendMessageRequest
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		var req sendMessageRequest
		_ = json.Unmarshal(body, &req)
		calls = append(calls, req)
		if req.MsgID != "" {
			return 400, []byte(`{"err_code":40034005,"message":"回复消息msg_id已过期","trace_id":"t"}`)
		}
		return 200, []byte(`{"id":"ROBOT1.0_active"}`)
	})
	defer srv.Close()

	a.storeReplyToken("U", "ROBOT1.0_old")
	id, ok := a.sendText(t.Context(), "U", false, "hi", "")
	if !ok || id != "ROBOT1.0_active" {
		t.Fatalf("降级主动消息失败: id=%q ok=%v", id, ok)
	}
	if len(calls) != 2 {
		t.Fatalf("调用次数 = %d, want 2", len(calls))
	}
	if calls[0].MsgID != "ROBOT1.0_old" {
		t.Errorf("首次应携带 msg_id: %+v", calls[0])
	}
	if calls[1].MsgID != "" {
		t.Errorf("重试应去掉 msg_id: %+v", calls[1])
	}
}

// TestPostMessageMarkdownRejected Markdown 被拒降级纯文本。
func TestPostMessageMarkdownRejected(t *testing.T) {
	var calls []sendMessageRequest
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		var req sendMessageRequest
		_ = json.Unmarshal(body, &req)
		calls = append(calls, req)
		if req.MsgType == 2 {
			return 400, []byte(`{"err_code":50056,"message":"不允许发送 markdown content"}`)
		}
		return 200, []byte(`{"id":"ROBOT1.0_plain"}`)
	})
	defer srv.Close()

	a.cfg.markdown = true
	id, ok := a.sendText(t.Context(), "U", false, "# 标题", "")
	if !ok || id != "ROBOT1.0_plain" {
		t.Fatalf("降级纯文本失败: id=%q ok=%v", id, ok)
	}
	if len(calls) != 2 || calls[1].MsgType != 0 || calls[1].Content != "# 标题" {
		t.Fatalf("降级请求错误: %+v", calls)
	}
}

// TestSendChainMixed 文本与媒体穿插：文本单独成条、媒体走上传+msg_type7。
func TestSendChainMixed(t *testing.T) {
	var paths []string
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		paths = append(paths, path)
		if strings.HasSuffix(path, "/files") {
			return 200, []byte(`{"file_uuid":"u","file_info":"FI","ttl":300}`)
		}
		return 200, []byte(`{"id":"ROBOT1.0_m` + strings.Repeat("x", len(paths)) + `"}`)
	})
	defer srv.Close()

	segs := []message.OB11Segment{
		{Type: message.SegmentText, Data: message.TextMessage{Text: "看图"}.Marshal()},
		{Type: message.SegmentImage, Data: message.ImageMessage{File: "https://e.com/a.png", Url: "https://e.com/a.png"}.Marshal()},
		{Type: message.SegmentText, Data: message.TextMessage{Text: "怎么样"}.Marshal()},
	}
	msgID, ok := a.sendChain(t.Context(), "GROUP1", true, segs)
	if !ok || msgID == "" {
		t.Fatalf("混合发送失败: %q/%v", msgID, ok)
	}
	// 文本 → files 上传 → 媒体消息 → 文本
	want := []string{
		"/v2/groups/GROUP1/messages",
		"/v2/groups/GROUP1/files",
		"/v2/groups/GROUP1/messages",
		"/v2/groups/GROUP1/messages",
	}
	if len(paths) != len(want) {
		t.Fatalf("调用序列 = %v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("调用序列 = %v, want %v", paths, want)
		}
	}
}

// TestSendChainMentionDegraded at 段静默退化、reply 段翻译为 message_reference。
func TestSendChainMentionDegraded(t *testing.T) {
	var bodies []sendMessageRequest
	a, srv := mockQQServer(t, func(method, path string, body []byte) (int, []byte) {
		var req sendMessageRequest
		_ = json.Unmarshal(body, &req)
		bodies = append(bodies, req)
		return 200, []byte(`{"id":"ROBOT1.0_z"}`)
	})
	defer srv.Close()

	segs := []message.OB11Segment{
		{Type: message.SegmentReply, Data: message.ReplyMessage{Id: "qo:ROBOT1.0_ref"}.Marshal()},
		{Type: message.SegmentMention, Data: message.MentionMessage{QQ: "qo:MEMBER1"}.Marshal()},
		{Type: message.SegmentText, Data: message.TextMessage{Text: "回复你"}.Marshal()},
	}
	msgID, ok := a.sendChain(t.Context(), "GROUP1", true, segs)
	if !ok {
		t.Fatal("发送应成功")
	}
	if msgID != "qo:ROBOT1.0_z" {
		t.Errorf("消息 ID = %q", msgID)
	}
	if len(bodies) != 1 {
		t.Fatalf("请求数 = %d", len(bodies))
	}
	if bodies[0].MessageReference == nil || bodies[0].MessageReference.MessageID != "ROBOT1.0_ref" {
		t.Errorf("message_reference 丢失: %+v", bodies[0])
	}
	if strings.Contains(bodies[0].Content, "MEMBER1") {
		t.Errorf("at 段不应出现在文本中: %q", bodies[0].Content)
	}
}
