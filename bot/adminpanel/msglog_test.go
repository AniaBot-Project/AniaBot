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
