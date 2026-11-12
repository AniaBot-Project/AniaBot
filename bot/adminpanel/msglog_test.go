package adminpanel

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/core/configstore"
)

// roundTripFunc 让测试把任意公网域名重定向到本地 httptest 服务。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestMsgLogsSessionRoute 会话视图把 session / before / after 透传给数据源，
// 并按 limit+1 判断是否还有更早的消息。
func TestMsgLogsSessionRoute(t *testing.T) {
	var gotSession string
	var gotLimit int
	var gotBefore, gotAfter uint64
	s := &Server{opt: Options{
		Logger: slog.Default(),
		MsgLogSessionPage: func(session string, limit int, beforeID, afterID uint64) []msglog.Entry {
			gotSession, gotLimit, gotBefore, gotAfter = session, limit, beforeID, afterID
			return []msglog.Entry{
				{ID: 9, Type: msglog.TypeGroup, GroupId: "qq:1", Text: "c"},
				{ID: 7, Type: msglog.TypeGroup, GroupId: "qq:1", Text: "b"},
				{ID: 5, Type: msglog.TypeGroup, GroupId: "qq:1", Text: "a"},
			}
		},
	}}
	rec := httptest.NewRecorder()
	s.handleMsgLogs(rec, httptest.NewRequest("GET", "/api/msglogs?session=g:qq:1&limit=2&before=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotSession != "g:qq:1" || gotLimit != 3 || gotBefore != 10 || gotAfter != 0 {
		t.Fatalf("参数未透传: session=%q limit=%d before=%d after=%d", gotSession, gotLimit, gotBefore, gotAfter)
	}
	var out struct {
		Items   []msglog.Entry `json:"items"`
		HasMore bool           `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(out.Items) != 2 || !out.HasMore || out.Items[0].ID != 9 {
		t.Fatalf("分页结果异常: %+v", out)
	}

	// after 增量游标同样透传
	rec = httptest.NewRecorder()
	s.handleMsgLogs(rec, httptest.NewRequest("GET", "/api/msglogs?session=g:qq:1&after=5", nil))
	if gotAfter != 5 || gotBefore != 0 {
		t.Fatalf("after 游标未透传: before=%d after=%d", gotBefore, gotAfter)
	}

	// 数据源未提供按会话分页时返回 404
	s2 := &Server{opt: Options{Logger: slog.Default()}}
	rec = httptest.NewRecorder()
	s2.handleMsgLogs(rec, httptest.NewRequest("GET", "/api/msglogs?session=g:qq:1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未启用按会话分页应返回 404, got %d", rec.Code)
	}

	// 非法游标返回 400
	rec = httptest.NewRecorder()
	s.handleMsgLogs(rec, httptest.NewRequest("GET", "/api/msglogs?session=g:qq:1&after=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 after 游标应返回 400, got %d", rec.Code)
	}
}

// TestMsgLogSessionsRoute 会话摘要接口：正常返回与未启用时的空数组。
func TestMsgLogSessionsRoute(t *testing.T) {
	now := time.Now()
	s := &Server{opt: Options{
		Logger: slog.Default(),
		MsgLogSessions: func() []msglog.Session {
			return []msglog.Session{{
				Key: "g:qq:1", Kind: msglog.SessionGroup, GroupId: "qq:1",
				Name: "小明", LastText: "早上好", LastTime: now, LastID: 3, Count: 2,
			}}
		},
	}}
	rec := httptest.NewRecorder()
	s.handleMsgLogSessions(rec, httptest.NewRequest("GET", "/api/msglogs/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []msglog.Session `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.Items[0].Key != "g:qq:1" {
		t.Fatalf("响应异常: %+v, err=%v", out, err)
	}

	// 未实现会话能力时返回空数组而不是 null
	empty := &Server{opt: Options{Logger: slog.Default()}}
	rec = httptest.NewRecorder()
	empty.handleMsgLogSessions(rec, httptest.NewRequest("GET", "/api/msglogs/sessions", nil))
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("未启用时应返回空数组, body = %s", rec.Body.String())
	}
}

// TestMsgLogDeleteRoute 右键删除单条消息日志：ID 透传给数据源，未启用 /
// 缺参 / 非法参数 / 记录不存在各自返回合适的错误码。
func TestMsgLogDeleteRoute(t *testing.T) {
	var gotID uint64
	s := &Server{opt: Options{
		Logger: slog.Default(),
		MsgLogDelete: func(id uint64) bool {
			gotID = id
			return id == 9
		},
	}}
	rec := httptest.NewRecorder()
	s.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs?id=9", nil))
	if rec.Code != http.StatusOK || gotID != 9 {
		t.Fatalf("status = %d, id = %d, body = %s", rec.Code, gotID, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("响应缺少 ok: %s", rec.Body.String())
	}

	// 记录不存在（数据源返回 false）→ 404
	rec = httptest.NewRecorder()
	s.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs?id=8", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("记录不存在应返回 404, got %d", rec.Code)
	}

	// 缺少 id / 非法 id → 400
	for _, query := range []string{"", "?id=abc", "?id=0"} {
		rec = httptest.NewRecorder()
		s.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs"+query, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q 应返回 400, got %d", query, rec.Code)
		}
	}

	// 数据源未提供删除能力 → 404
	empty := &Server{opt: Options{Logger: slog.Default()}}
	rec = httptest.NewRecorder()
	empty.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs?id=9", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未启用删除应返回 404, got %d", rec.Code)
	}
}

// TestMsgLogSessionDeleteRoute 右键会话删除：session 透传给数据源并返回删除
// 条数；会话不存在 / 未启用返回 404，超长会话键返回 400。
func TestMsgLogSessionDeleteRoute(t *testing.T) {
	var gotSession string
	s := &Server{opt: Options{
		Logger: slog.Default(),
		MsgLogDeleteSession: func(session string) int {
			gotSession = session
			if session == "g:qq:1" {
				return 3
			}
			return 0
		},
	}}
	rec := httptest.NewRecorder()
	s.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs?session=g:qq:1", nil))
	if rec.Code != http.StatusOK || gotSession != "g:qq:1" {
		t.Fatalf("status = %d, session = %q, body = %s", rec.Code, gotSession, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"removed":3`) {
		t.Fatalf("响应缺少删除条数: %s", rec.Body.String())
	}

	// 会话没有可删记录 → 404
	rec = httptest.NewRecorder()
	s.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs?session=g:qq:404", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("会话无记录应返回 404, got %d", rec.Code)
	}

	// 会话键过长 → 400
	rec = httptest.NewRecorder()
	long := "g:" + strings.Repeat("9", 200)
	s.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs?session="+url.QueryEscape(long), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超长会话键应返回 400, got %d", rec.Code)
	}

	// 数据源未提供会话删除能力 → 404
	empty := &Server{opt: Options{Logger: slog.Default()}}
	rec = httptest.NewRecorder()
	empty.handleMsgLogDelete(rec, httptest.NewRequest("DELETE", "/api/msglogs?session=g:qq:1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未启用会话删除应返回 404, got %d", rec.Code)
	}
}

// TestWritePagedLogsNil typed nil 切片也应序列化为空数组，避免前端拿到 null。
func TestWritePagedLogsNil(t *testing.T) {
	rec := httptest.NewRecorder()
	writePagedLogs(rec, []msglog.Entry(nil), false)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("typed nil 应输出空数组, body = %s", rec.Body.String())
	}
}

// TestMsgLogMediaProxy 媒体代理转发远端内容，并支持附件下载文件名。
func TestMsgLogMediaProxy(t *testing.T) {
	payload := []byte("hello media")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("代理请求应带 User-Agent")
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()
	up, _ := url.Parse(upstream.URL)

	s := &Server{opt: Options{Logger: slog.Default()}, mediaHTTP: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = up.Scheme
			req.URL.Host = up.Host
			return http.DefaultTransport.RoundTrip(req)
		}),
	}}
	q := url.Values{
		"url":      {"http://media.example.com/files/1"},
		"name":     {"周报 2026.pdf"},
		"download": {"1"},
	}
	rec := httptest.NewRecorder()
	s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body, _ := io.ReadAll(rec.Result().Body); string(body) != string(payload) {
		t.Fatalf("代理内容错误: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "filename*=UTF-8''") {
		t.Fatalf("下载响应头错误: %q", cd)
	}
}

// TestMsgLogMediaReject 媒体代理只允许 http(s) 且拒绝本机 / 私网地址。
func TestMsgLogMediaReject(t *testing.T) {
	s := &Server{opt: Options{Logger: slog.Default()}}
	cases := []struct {
		url  string
		code int
	}{
		{"file:///etc/passwd", http.StatusBadRequest},
		{"ftp://example.com/a.png", http.StatusBadRequest},
		{"javascript:alert(1)", http.StatusBadRequest},
		{"http://127.0.0.1:7700/api/me", http.StatusForbidden},
		{"http://localhost/a.png", http.StatusForbidden},
		{"http://[::1]/a.png", http.StatusForbidden},
		{"http://10.1.2.3/a.png", http.StatusForbidden},
		{"http://192.168.0.2/a.png", http.StatusForbidden},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media?url="+url.QueryEscape(c.url), nil))
		if rec.Code != c.code {
			t.Errorf("%s: status = %d, want %d", c.url, rec.Code, c.code)
		}
	}
}

// TestMsgLogMediaReferer 媒体代理带浏览器 UA 与同源 Referer（部分 CDN 会拒绝无来源请求）。
func TestMsgLogMediaReferer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "http://media.example.com/" {
			t.Errorf("Referer = %q", r.Header.Get("Referer"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("缺少 User-Agent")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	up, _ := url.Parse(upstream.URL)
	s := &Server{opt: Options{Logger: slog.Default()}, mediaHTTP: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = up.Scheme
			req.URL.Host = up.Host
			return http.DefaultTransport.RoundTrip(req)
		}),
	}}
	rec := httptest.NewRecorder()
	s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media?url="+url.QueryEscape("http://media.example.com/a.png"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestMsgLogMediaRefresh 旧地址失效（QQ 的 rkey 过期）时，凭 entry/index 续期
// 后重试一次，用户仍能看到图片。
func TestMsgLogMediaRefresh(t *testing.T) {
	payload := []byte("fresh media")
	fresh := "http://media.example.com/fresh.png"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fresh.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(payload)
			return
		}
		http.Error(w, "url expired", http.StatusForbidden)
	}))
	defer upstream.Close()
	up, _ := url.Parse(upstream.URL)

	var gotEntry uint64
	var gotIndex int
	calls := 0
	s := &Server{
		opt: Options{Logger: slog.Default(), MsgLogMedia: func(entryID uint64, index int) (string, bool) {
			calls++
			gotEntry, gotIndex = entryID, index
			return fresh, true
		}},
		mediaHTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = up.Scheme
			req.URL.Host = up.Host
			return http.DefaultTransport.RoundTrip(req)
		})},
	}
	q := url.Values{
		"url":   {"http://media.example.com/old.png?rkey=expired"},
		"entry": {"7"},
		"index": {"1"},
	}
	rec := httptest.NewRecorder()
	s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body, _ := io.ReadAll(rec.Result().Body); string(body) != string(payload) {
		t.Fatalf("续期后内容错误: %q", body)
	}
	if calls != 1 || gotEntry != 7 || gotIndex != 1 {
		t.Fatalf("续期参数错误: calls=%d entry=%d index=%d", calls, gotEntry, gotIndex)
	}
}

// TestMsgLogMediaResolveWithoutURL 日志里没有直链（记录时就拿不到地址）时，
// 凭 entry/index 直接向平台申请，再代理返回。
func TestMsgLogMediaResolveWithoutURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("media"))
	}))
	defer upstream.Close()
	up, _ := url.Parse(upstream.URL)

	s := &Server{
		opt: Options{Logger: slog.Default(), MsgLogMedia: func(entryID uint64, index int) (string, bool) {
			return "http://media.example.com/only-file-id.zip", true
		}},
		mediaHTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = up.Scheme
			req.URL.Host = up.Host
			return http.DefaultTransport.RoundTrip(req)
		})},
	}
	rec := httptest.NewRecorder()
	s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media?entry=9&index=0&name=a.zip&download=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Fatalf("下载响应头错误: %q", cd)
	}
}

// TestMsgLogMediaRefreshUnavailable 没有续期能力时，旧地址失效按 502 返回；
// entry/index 非法按 400 返回。
func TestMsgLogMediaRefreshUnavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "url expired", http.StatusForbidden)
	}))
	defer upstream.Close()
	up, _ := url.Parse(upstream.URL)

	s := &Server{opt: Options{Logger: slog.Default()}, mediaHTTP: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = up.Scheme
			req.URL.Host = up.Host
			return http.DefaultTransport.RoundTrip(req)
		}),
	}}
	rec := httptest.NewRecorder()
	s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media?url="+url.QueryEscape("http://media.example.com/a.png")+"&entry=1&index=0", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("无续期能力时应返回 502, got %d", rec.Code)
	}

	for _, query := range []string{"entry=abc", "entry=1&index=-1", "entry=0"} {
		rec = httptest.NewRecorder()
		s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media?"+query, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", query, rec.Code)
		}
	}

	// 既没有 url 也没有日志引用：保持旧的 400 提示
	rec = httptest.NewRecorder()
	s.handleMsgLogMedia(rec, httptest.NewRequest("GET", "/api/msglogs/media", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少参数应返回 400, got %d", rec.Code)
	}
}

// TestSanitizeMediaName 下载文件名清理。
func TestSanitizeMediaName(t *testing.T) {
	cases := map[string]string{
		"周报 2026.pdf":            "周报 2026.pdf",
		"../../etc/passwd":       ".._.._etc_passwd",
		"a\"b\nc.pdf":            "a_b_c.pdf",
		strings.Repeat("长", 200): strings.Repeat("长", 120),
	}
	for in, want := range cases {
		if got := sanitizeMediaName(in); got != want {
			t.Errorf("sanitizeMediaName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mediaFallbackName("周报 2026.PDF"); got != "media.pdf" {
		t.Errorf("mediaFallbackName = %q, want media.pdf", got)
	}
	if got := mediaFallbackName("周报.文档"); got != "media" {
		t.Errorf("非 ASCII 后缀应回退为 media, got %q", got)
	}
}

// TestMsgLogMediaLink 过期媒体的续签接口：302 到平台新地址，且重定向不可缓存。
func TestMsgLogMediaLink(t *testing.T) {
	fresh := "https://cdn.example.com/fresh.png?rkey=new"
	s := &Server{opt: Options{
		Logger: slog.Default(),
		MsgLogMedia: func(entryID uint64, index int) (string, bool) {
			if entryID != 7 || index != 1 {
				return "", false
			}
			return fresh, true
		},
	}}
	rec := httptest.NewRecorder()
	s.handleMsgLogMediaLink(rec, httptest.NewRequest("GET", "/api/msglogs/media/link?entry=7&index=1", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != fresh {
		t.Fatalf("Location = %q, want %q", got, fresh)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

// TestMsgLogMediaLinkReject 续签接口的边界：缺少引用、无法续签、内网地址，
// 以及关闭「消息图片直连」时都拒绝。
func TestMsgLogMediaLinkReject(t *testing.T) {
	store := configstore.New(newFakePersistent(), nil)
	s := &Server{opt: Options{
		Logger: slog.Default(),
		Config: store,
		MsgLogMedia: func(uint64, int) (string, bool) {
			return "http://192.168.0.2/a.png", true
		},
	}}

	rec := httptest.NewRecorder()
	s.handleMsgLogMediaLink(rec, httptest.NewRequest("GET", "/api/msglogs/media/link", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少 entry 应返回 400, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.handleMsgLogMediaLink(rec, httptest.NewRequest("GET", "/api/msglogs/media/link?entry=1&index=0", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("内网地址应返回 403, got %d", rec.Code)
	}

	if err := store.Set("bot.admin_panel.media_direct", false); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	rec = httptest.NewRecorder()
	s.handleMsgLogMediaLink(rec, httptest.NewRequest("GET", "/api/msglogs/media/link?entry=1&index=0", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("关闭直连后应返回 403, got %d", rec.Code)
	}

	// 没有续期能力时返回 404，前端会退回媒体代理
	plain := &Server{opt: Options{Logger: slog.Default()}}
	rec = httptest.NewRecorder()
	plain.handleMsgLogMediaLink(rec, httptest.NewRequest("GET", "/api/msglogs/media/link?entry=1&index=0", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无法续签时应返回 404, got %d", rec.Code)
	}
}

// TestHandleMeMediaDirect 面板把「图片直连 CDN」开关下发给前端：未配置时默认
// 开启，配置关闭后前端应全部走媒体代理。
func TestHandleMeMediaDirect(t *testing.T) {
	store := configstore.New(newFakePersistent(), nil)
	s := &Server{opt: Options{Config: store, Logger: slog.Default()}}

	read := func() bool {
		rec := httptest.NewRecorder()
		s.handleMe(rec, httptest.NewRequest("GET", "/api/me", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out struct {
			MediaDirect *bool `json:"media_direct"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if out.MediaDirect == nil {
			t.Fatalf("响应缺少 media_direct: %s", rec.Body.String())
		}
		return *out.MediaDirect
	}

	if !read() {
		t.Fatal("未配置时应默认开启直连")
	}
	if err := store.Set("bot.admin_panel.media_direct", false); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if read() {
		t.Fatal("配置关闭后应下发布尔 false")
	}
}
