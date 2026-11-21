package weixin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// testFriendChain / testGroupChain 最小消息链实现。
func testFriendChain(text string) msgchain.FriendChain {
	return msgchain.Builder().Friend().Text(text).Build()
}

func testGroupChain(text string) msgchain.GroupChain {
	return msgchain.Builder().Group().Text(text).Build()
}

func TestFrameIDRoundTrip(t *testing.T) {
	uid := frameUserID("abc@im.wechat")
	if uid != "wx:abc@im.wechat" {
		t.Fatalf("frameUserID = %q", uid)
	}
	mid := frameMsgID("abc@im.wechat", "12345")
	if mid != "wx:abc@im.wechat:12345" {
		t.Fatalf("frameMsgID = %q", mid)
	}
	user, id, ok := parseFrameMsgID(mid.String())
	if !ok || user != "abc@im.wechat" || id != "12345" {
		t.Fatalf("parseFrameMsgID = %q %q %v", user, id, ok)
	}
	// 非本平台前缀
	if _, _, ok := parseFrameMsgID("tg:123:456"); ok {
		t.Fatal("expected ok=false for foreign prefix")
	}
	// 缺少消息段
	if _, _, ok := parseFrameMsgID("wx:abc@im.wechat"); ok {
		t.Fatal("expected ok=false when mid missing")
	}
}

func TestSendToUserRejectsForeignID(t *testing.T) {
	a := NewAdapter(nil)
	if _, ok := a.SendFriendMsg("tg:123", testFriendChain("hi")); ok {
		t.Fatal("expected foreign ID to be rejected")
	}
	if _, ok := a.SendGroupMsg(message.QID("qq:123"), testGroupChain("hi")); ok {
		t.Fatal("expected foreign group ID to be rejected")
	}
}

func TestSplitText(t *testing.T) {
	if got := splitText(""); got != nil {
		t.Fatalf("splitText(\"\") = %v", got)
	}
	if got := splitText("hello"); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("splitText short = %v", got)
	}
	// 恰好等于上限
	s := string(make([]rune, 4000))
	if got := splitText(s); len(got) != 1 {
		t.Fatalf("splitText(limit) parts = %d", len(got))
	}
	// 超限分包且 rune 完整
	long := strings.Repeat("a", 4000) + "尾"
	got := splitText(long)
	if len(got) != 2 {
		t.Fatalf("splitText(long) parts = %d", len(got))
	}
	if got[1] != "尾" {
		t.Fatalf("splitText rune boundary broken: %q", got[1])
	}
}

func TestQueryEscape(t *testing.T) {
	cases := map[string]string{
		"abc123":    "abc123",
		"a b":       "a%20b",
		"a+b":       "a%2Bb",
		"中文":        "%E4%B8%AD%E6%96%87",
		"a/b?c=d&e": "a%2Fb%3Fc%3Dd%26e",
		"a~b-c_d.e": "a~b-c_d.e",
	}
	for in, want := range cases {
		if got := queryEscape(in); got != want {
			t.Fatalf("queryEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnpackBusinessError(t *testing.T) {
	// 服务端返回 HTTP 200 但 ret=-14（凭证失效）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":-14,"errmsg":"session timeout"}`))
	}))
	defer srv.Close()

	c := newClient("tok", srv.URL, DefaultCDNBase)
	err := c.sendMessage(t.Context(), &WeixinMessage{ToUserID: "u@im.wechat"})
	if err == nil {
		t.Fatal("expected business error")
	}
	if !StaleToken(err) {
		t.Fatalf("expected stale token error, got %v", err)
	}
}

func TestUnpackHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gateway boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newClient("tok", srv.URL, DefaultCDNBase)
	err := c.sendMessage(t.Context(), &WeixinMessage{ToUserID: "u@im.wechat"})
	if err == nil || StaleToken(err) {
		t.Fatalf("expected non-stale http error, got %v", err)
	}
}

func TestStateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := newStateStore(dir)
	if st, err := s.load(); err != nil || st != nil {
		t.Fatalf("load on empty: st=%v err=%v", st, err)
	}
	want := &accountState{Token: "tok-1", BaseURL: "https://x", BotID: "b@im.bot", UserID: "u@im.wechat", CDNBase: "https://cdn"}
	if err := s.save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != want.Token || got.BotID != want.BotID || got.UserID != want.UserID {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := s.clear(); err != nil {
		t.Fatal(err)
	}
	if st, err := s.load(); err != nil || st != nil {
		t.Fatalf("load after clear: st=%v err=%v", st, err)
	}
	// 游标
	if buf := s.loadUpdatesBuf(); buf != "" {
		t.Fatalf("empty updates buf = %q", buf)
	}
	s.saveUpdatesBuf("cursor-bytes")
	if buf := s.loadUpdatesBuf(); buf != "cursor-bytes" {
		t.Fatalf("updates buf = %q", buf)
	}
}

func TestContextTokenStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ctx.json")
	s := newContextTokenStore(path)
	s.set("u1@im.wechat", "tok-a")
	s.set("u2@im.wechat", "tok-b")
	s.set("", "ignored")      // 空用户忽略
	s.set("u1@im.wechat", "") // 空 token 忽略
	if got := s.get("u1@im.wechat"); got != "tok-a" {
		t.Fatalf("u1 token = %q", got)
	}
	s.flush()

	// 重开恢复
	s2 := newContextTokenStore(path)
	if got := s2.get("u2@im.wechat"); got != "tok-b" {
		t.Fatalf("u2 token after reload = %q", got)
	}
}

func TestTranslateTextMessage(t *testing.T) {
	a := NewAdapter(nil)
	a.setSelf("bot@im.bot")
	msg := a.translateMessage(&WeixinMessage{
		FromUserID:   "u1@im.wechat",
		MessageID:    42,
		MessageType:  MsgTypeUser,
		CreateTimeMs: 1700000000000,
		ItemList: []*MessageItem{
			{Type: ItemText, TextItem: &TextItem{Text: "你好"}},
		},
	})
	if msg == nil {
		t.Fatal("translateMessage returned nil")
	}
	if msg.MessageType != "private" || msg.SubType != "friend" {
		t.Fatalf("message type = %s/%s", msg.MessageType, msg.SubType)
	}
	if msg.UserId != "wx:u1@im.wechat" || msg.SelfId != "wx:bot@im.bot" {
		t.Fatalf("ids = %q self=%q", msg.UserId, msg.SelfId)
	}
	// 私聊消息不能带 GroupId：框架用它区分群聊/私聊，填用户 ID 会让同一会话
	// 在消息日志里被拆成群聊 + 私聊，指令也按群聊回复。
	if msg.GroupId != "" {
		t.Fatalf("私聊消息不应填 GroupId, got %q", msg.GroupId)
	}
	if msg.MessageId != "wx:u1@im.wechat:42" {
		t.Fatalf("message id = %q", msg.MessageId)
	}
	if msg.RawMessage != "你好" {
		t.Fatalf("raw = %q", msg.RawMessage)
	}
	if msg.Platform != Platform {
		t.Fatalf("platform = %q", msg.Platform)
	}
	if msg.Time != 1700000000 {
		t.Fatalf("time = %d", msg.Time)
	}
}

func TestHandleMessageDropsSelfMessages(t *testing.T) {
	a := NewAdapter(nil)
	a.setSelf("bot@im.bot")
	called := false
	a.SetTrigger(adapter.TriggerWrapper{OnFriendMsg: func(m message.Message) { called = true }})
	a.handleMessage(&WeixinMessage{FromUserID: "u@im.wechat", MessageType: MsgTypeBot})
	if called {
		t.Fatal("bot self message should not trigger plugin chain")
	}
	// 用户消息触发
	a.handleMessage(&WeixinMessage{
		FromUserID:   "u@im.wechat",
		MessageID:    3,
		MessageType:  MsgTypeUser,
		ContextToken: "ctx-1",
		ItemList:     []*MessageItem{{Type: ItemText, TextItem: &TextItem{Text: "hi"}}},
	})
	if !called {
		t.Fatal("user message should trigger plugin chain")
	}
	// context_token 已缓存
	if got := a.ctxTokens.get("u@im.wechat"); got == "" {
		t.Fatal("context token not cached from inbound message")
	}
}

func TestTranslateVoiceWithTranscript(t *testing.T) {
	a := NewAdapter(nil)
	a.setSelf("bot@im.bot")
	msg := a.translateMessage(&WeixinMessage{
		FromUserID: "u@im.wechat",
		MessageID:  7,
		ItemList: []*MessageItem{
			{Type: ItemVoice, VoiceItem: &VoiceItem{Text: "语音内容"}},
		},
	})
	if msg == nil || msg.RawMessage != "语音内容" {
		t.Fatalf("voice transcript message = %+v", msg)
	}
	// 无转写 → [语音] 占位
	msg = a.translateMessage(&WeixinMessage{
		FromUserID: "u@im.wechat",
		MessageID:  8,
		ItemList:   []*MessageItem{{Type: ItemVoice, VoiceItem: &VoiceItem{}}},
	})
	if msg == nil || msg.RawMessage != "[语音]" {
		t.Fatalf("voice placeholder = %+v", msg)
	}
}

func TestTranslateRefMessage(t *testing.T) {
	a := NewAdapter(nil)
	a.setSelf("bot@im.bot")
	msg := a.translateMessage(&WeixinMessage{
		FromUserID: "u@im.wechat",
		MessageID:  9,
		ItemList: []*MessageItem{{
			Type:     ItemText,
			TextItem: &TextItem{Text: "回复你"},
			RefMsg: &RefMessage{
				MessageItem: &MessageItem{MsgID: "55"},
				Title:       "原始消息",
			},
		}},
	})
	if msg == nil {
		t.Fatal("nil message")
	}
	if msg.Message[0].Type != message.SegmentReply {
		t.Fatalf("first segment = %s, want reply", msg.Message[0].Type)
	}
	if id, _ := msg.Message[0].Data["id"].(string); id != "wx:u@im.wechat:55" {
		t.Fatalf("reply id = %q", id)
	}
	if msg.RawMessage != "【引用】原始消息回复你" {
		t.Fatalf("raw = %q", msg.RawMessage)
	}
}

func TestMsgCachePushFindHistory(t *testing.T) {
	a := NewAdapter(nil)
	m1 := message.Message{MessageId: "wx:u@im.wechat:1"}
	m2 := message.Message{MessageId: "wx:u@im.wechat:2"}
	a.msgCache.Push("u@im.wechat", m1)
	time.Sleep(time.Millisecond)
	a.msgCache.Push("u@im.wechat", m2)

	if found, ok := a.GetMsgDetail("wx:u@im.wechat:1"); !ok || found.MessageId != "wx:u@im.wechat:1" {
		t.Fatalf("GetMsgDetail = %+v ok=%v", found, ok)
	}
	hist, ok := a.GetFriendMsgHistory("wx:u@im.wechat", 10, 0)
	if !ok || len(*hist) != 2 || (*hist)[0].MessageId != "wx:u@im.wechat:2" {
		t.Fatalf("history = %+v ok=%v", hist, ok)
	}
	// 群历史不支持
	if _, ok := a.GetGroupMsgHistory("wx:u@im.wechat", 10, 0); ok {
		t.Fatal("group history should be unsupported")
	}
}

func TestUploadKindAndSource(t *testing.T) {
	if mt, name := uploadKindOf(message.OB11Segment{Type: message.SegmentImage}); mt != UploadMediaImage || name != "image.png" {
		t.Fatalf("image kind = %d %q", mt, name)
	}
	if mt, _ := uploadKindOf(message.OB11Segment{Type: message.SegmentRecord}); mt != UploadMediaVoice {
		t.Fatalf("record kind = %d", mt)
	}
	if got := segmentFileSource(map[string]any{"url": "http://x", "file": "base64://y"}); got != "http://x" {
		t.Fatalf("source = %q", got)
	}
	if got := segmentFileSource(map[string]any{"file": "base64://y"}); got != "base64://y" {
		t.Fatalf("source = %q", got)
	}
	if got := baseNameOf("https://cdn.example.com/a/b/photo.png?x=1"); got != "photo.png" {
		t.Fatalf("baseNameOf = %q", got)
	}
}

// TestMediaSegmentForUpload file 段指向图片文件时转为 image 段（图片上传通道），
// 普通文件保持 file 段（文件附件通道）。
func TestMediaSegmentForUpload(t *testing.T) {
	s := mediaSegmentForUpload(message.OB11Segment{
		Type: message.SegmentFile,
		Data: message.FileMessage{File: "https://e.com/a.png", Name: "a.png"}.Marshal(),
	})
	if s.Type != message.SegmentImage {
		t.Fatalf("图片文件应转 image 段, got %q", s.Type)
	}
	if kind, _ := uploadKindOf(s); kind != UploadMediaImage {
		t.Fatalf("图片文件应按图片上传, kind = %d", kind)
	}

	s = mediaSegmentForUpload(message.OB11Segment{
		Type: message.SegmentFile,
		Data: message.FileMessage{File: "https://e.com/a.pdf", Name: "a.pdf"}.Marshal(),
	})
	if s.Type != message.SegmentFile {
		t.Fatalf("非图片文件应保持 file 段, got %q", s.Type)
	}
	if kind, _ := uploadKindOf(s); kind != UploadMediaFile {
		t.Fatalf("非图片文件应按文件上传, kind = %d", kind)
	}

	// image 段原样返回
	img := message.OB11Segment{Type: message.SegmentImage, Data: message.ImageMessage{File: "https://e.com/a.png"}.Marshal()}
	if got := mediaSegmentForUpload(img); got.Type != message.SegmentImage {
		t.Fatalf("image 段应原样返回, got %q", got.Type)
	}
}

func TestNewClientID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newClientID()
		if seen[id] {
			t.Fatalf("duplicate client id %q", id)
		}
		seen[id] = true
	}
}

func TestAccountStateJSONShape(t *testing.T) {
	b, _ := json.Marshal(accountState{Token: "t", BaseURL: "u", BotID: "b", UserID: "u2", CDNBase: "c"})
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"token", "base_url", "bot_id", "user_id", "cdn_base"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing json key %q in %s", k, b)
		}
	}
}

// TestSwitchCredentialsHotReload 面板扫码换账号热切换：token 变化时整体替换
// 客户端并跟随新 base 地址，跨账号重置轮询游标；token 未变化时不切换。
func TestSwitchCredentialsHotReload(t *testing.T) {
	a := newTestAdapter(t, "https://stub.example")
	a.mu.Lock()
	a.client = newClient("old-token", "https://stub.example", DefaultCDNBase)
	a.clientBotID = "old@im.bot"
	a.mu.Unlock()

	apiBase := "https://stub.example"
	cdnBase := DefaultCDNBase
	buf := "cursor-old"

	// 同账号重复保存（token 未变化）：不切换、游标保留
	if err := a.saveLoginCredentials(&accountState{Token: "old-token", BotID: "old@im.bot", BaseURL: apiBase, CDNBase: cdnBase}); err != nil {
		t.Fatalf("save same-account credentials: %v", err)
	}
	a.switchCredentialsIfChanged(&apiBase, &cdnBase, &buf)
	if a.currentToken() != "old-token" || buf != "cursor-old" {
		t.Fatalf("same token should not switch: token=%q buf=%q", a.currentToken(), buf)
	}

	// 换账号：切换客户端、跟随新 base、重置游标、self 跟随新 bot ID
	if err := a.saveLoginCredentials(&accountState{Token: "new-token", BotID: "new@im.bot", BaseURL: "https://new.example", CDNBase: cdnBase}); err != nil {
		t.Fatalf("save new-account credentials: %v", err)
	}
	a.switchCredentialsIfChanged(&apiBase, &cdnBase, &buf)
	if a.currentToken() != "new-token" {
		t.Fatalf("token should hot-switch, got %q", a.currentToken())
	}
	if apiBase != "https://new.example" {
		t.Fatalf("apiBase should follow new account, got %q", apiBase)
	}
	if buf != "" {
		t.Fatalf("cursor should reset across accounts, got %q", buf)
	}
	if a.SelfID() != message.QID("wx:new@im.bot") {
		t.Fatalf("self should follow new bot id, got %q", a.SelfID())
	}

	// 恢复游标后再次触发（token 已一致）：游标保留
	buf = "cursor-new"
	a.switchCredentialsIfChanged(&apiBase, &cdnBase, &buf)
	if buf != "cursor-new" || a.currentToken() != "new-token" {
		t.Fatalf("idempotent re-check should keep state: token=%q buf=%q", a.currentToken(), buf)
	}
}

// TestMsgCachePushStripsInlinePayload 入站图片下载解密出的 data URI（MB 级）不入缓存，
// 只保留轻量键，且不修改传入的消息。
func TestMsgCachePushStripsInlinePayload(t *testing.T) {
	a := NewAdapter(nil)
	dataURI := "data:image/png;base64,AAAA"
	m := message.Message{
		MessageId: "wx:u@im.wechat:1",
		Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: message.ImageMessage{File: "weixin_image", Url: dataURI}.Marshal()},
		},
	}
	a.msgCache.Push("u@im.wechat", m)

	if _, ok := m.Message[0].Data["url"]; !ok {
		t.Fatal("传入的消息被修改了")
	}
	cached, ok := a.GetMsgDetail("wx:u@im.wechat:1")
	if !ok {
		t.Fatal("GetMsgDetail 应命中")
	}
	if _, ok := cached.Message[0].Data["url"]; ok {
		t.Fatal("缓存的 data URI 未被剔除")
	}
	if cached.Message[0].Data["file"] != "weixin_image" {
		t.Fatalf("file 键应保留 = %v", cached.Message[0].Data["file"])
	}
}
