package adminpanel

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// fakeContextSource 记录面板上下文管理接口的调用，用于校验 handler 行为。
type fakeContextSource struct {
	sessions []plugininfo.ContextSession
	details  map[string]plugininfo.ContextDetail
	busy     map[string]bool

	cleared    []string
	deleted    []string
	terminated map[string]bool
	exported   [][]string
}

func (f *fakeContextSource) ContextSessions() []plugininfo.ContextSession { return f.sessions }

func (f *fakeContextSource) ContextDetail(key string) (plugininfo.ContextDetail, error) {
	d, ok := f.details[key]
	if !ok {
		return plugininfo.ContextDetail{}, errors.New("会话不存在或已无上下文：" + key)
	}
	return d, nil
}

func (f *fakeContextSource) ContextClear(key string, terminate bool) (plugininfo.ContextClearResult, error) {
	if f.busy[key] && !terminate {
		return plugininfo.ContextClearResult{Key: key}, plugininfo.ErrContextBusy
	}
	f.cleared = append(f.cleared, key)
	if f.terminated == nil {
		f.terminated = map[string]bool{}
	}
	f.terminated[key] = terminate && f.busy[key]
	return plugininfo.ContextClearResult{Key: key, OK: true, Terminated: f.terminated[key]}, nil
}

func (f *fakeContextSource) ContextDelete(key string, terminate bool) (plugininfo.ContextClearResult, error) {
	if f.busy[key] && !terminate {
		return plugininfo.ContextClearResult{Key: key}, plugininfo.ErrContextBusy
	}
	f.deleted = append(f.deleted, key)
	return plugininfo.ContextClearResult{Key: key, OK: true, Deleted: true, Terminated: terminate && f.busy[key]}, nil
}

func (f *fakeContextSource) ContextClearAll(terminate bool) plugininfo.ContextClearAllResult {
	res := plugininfo.ContextClearAllResult{OK: true}
	for _, s := range f.sessions {
		one, err := f.ContextClear(s.Key, terminate)
		if err != nil || !one.OK {
			res.OK = false
			res.Failed = append(res.Failed, plugininfo.ContextClearResult{Key: s.Key, Error: err.Error()})
			continue
		}
		res.Cleared++
		if one.Terminated {
			res.Terminated++
		}
	}
	return res
}

func (f *fakeContextSource) ContextExport(keys []string) []plugininfo.ContextDetail {
	f.exported = append(f.exported, keys)
	out := make([]plugininfo.ContextDetail, 0, len(f.details))
	for _, d := range f.details {
		out = append(out, d)
	}
	return out
}

func newContextTestServer(src ContextSource) *Server {
	return &Server{opt: Options{Contexts: src, Logger: slog.Default()}}
}

func contextMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/contexts", s.handleContextList)
	mux.HandleFunc("GET /api/contexts/detail", s.handleContextDetail)
	mux.HandleFunc("GET /api/contexts/export", s.handleContextsExport)
	mux.HandleFunc("POST /api/contexts/clear", s.handleContextsClear)
	mux.HandleFunc("DELETE /api/contexts", s.handleContextDelete)
	return mux
}

func TestHandleContextList(t *testing.T) {
	// 无数据源：返回空数组而不是错误，前端按空列表展示
	mux := contextMux(newContextTestServer(nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contexts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var empty struct {
		Items []plugininfo.ContextSession `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || len(empty.Items) != 0 {
		t.Fatalf("无数据源应返回空数组: %v %+v", err, empty)
	}

	src := &fakeContextSource{sessions: []plugininfo.ContextSession{
		{Key: "g:qq:1", Kind: "group", Target: "qq:1", MessageCount: 3},
		{Key: "f:qq:2", Kind: "friend", Target: "qq:2", Resident: true},
	}}
	mux = contextMux(newContextTestServer(src))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contexts", nil))
	var got struct {
		Items []plugininfo.ContextSession `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0].Key != "g:qq:1" || !got.Items[1].Resident {
		t.Fatalf("会话列表不符: %+v", got.Items)
	}
}

func TestHandleContextDetail(t *testing.T) {
	src := &fakeContextSource{details: map[string]plugininfo.ContextDetail{
		"g:qq:1": {ContextSession: plugininfo.ContextSession{Key: "g:qq:1"}, Source: plugininfo.ContextSourceLive},
	}}
	mux := contextMux(newContextTestServer(src))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contexts/detail?key=g:qq:1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var detail plugininfo.ContextDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if detail.Key != "g:qq:1" || detail.Source != plugininfo.ContextSourceLive {
		t.Fatalf("详情不符: %+v", detail)
	}

	// 缺少 key / 非法 key → 400
	for _, path := range []string{"/api/contexts/detail", "/api/contexts/detail?key=bogus"} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", path, rec.Code)
		}
	}

	// 不存在的会话 → 404
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contexts/detail?key=g:qq:404", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话 status = %d, want 404", rec.Code)
	}
}

func TestHandleContextsExport(t *testing.T) {
	src := &fakeContextSource{details: map[string]plugininfo.ContextDetail{
		"g:qq:1": {ContextSession: plugininfo.ContextSession{Key: "g:qq:1"}},
		"f:qq:2": {ContextSession: plugininfo.ContextSession{Key: "f:qq:2"}},
	}}
	mux := contextMux(newContextTestServer(src))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contexts/export?keys=g:qq:1,f:qq:2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "aniabot-contexts-") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	var payload struct {
		Total int                        `json:"total"`
		Items []plugininfo.ContextDetail `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload.Total != 2 || len(payload.Items) != 2 {
		t.Fatalf("导出内容不符: %+v", payload)
	}
	if len(src.exported) != 1 || len(src.exported[0]) != 2 {
		t.Fatalf("会话键未透传: %+v", src.exported)
	}

	// 不带 keys：导出全部（传空列表给数据源）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contexts/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("全部导出 status = %d", rec.Code)
	}
	if len(src.exported) != 2 || len(src.exported[1]) != 0 {
		t.Fatalf("全部导出应传空键列表: %+v", src.exported)
	}

	// 非法会话键 → 400
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contexts/export?keys=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法会话键 status = %d, want 400", rec.Code)
	}
}

func TestHandleContextsClear(t *testing.T) {
	src := &fakeContextSource{busy: map[string]bool{"g:qq:2": true}}
	mux := contextMux(newContextTestServer(src))

	// 未指定会话 → 400
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/contexts/clear", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空请求 status = %d, want 400", rec.Code)
	}

	// 指定多个会话：执行中的会话计入失败，其余成功
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/contexts/clear",
		strings.NewReader(`{"keys":["g:qq:1","g:qq:2"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res plugininfo.ContextClearAllResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if res.OK || res.Cleared != 1 || len(res.Failed) != 1 || res.Failed[0].Key != "g:qq:2" {
		t.Fatalf("清空结果不符: %+v", res)
	}

	// 确认终止后重发：全部成功并记录终止
	src.cleared = nil
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/contexts/clear",
		strings.NewReader(`{"keys":["g:qq:1","g:qq:2"],"terminate":true}`)))
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !res.OK || res.Cleared != 2 || res.Terminated != 1 {
		t.Fatalf("终止清空结果不符: %+v", res)
	}

	// all=true：清空全部
	src.sessions = []plugininfo.ContextSession{{Key: "g:qq:1"}}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/contexts/clear",
		strings.NewReader(`{"all":true,"terminate":true}`)))
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !res.OK || res.Cleared != 1 {
		t.Fatalf("全部清空结果不符: %+v", res)
	}
}

func TestHandleContextDelete(t *testing.T) {
	src := &fakeContextSource{busy: map[string]bool{"g:qq:2": true}}
	mux := contextMux(newContextTestServer(src))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/contexts?key=g:qq:1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res plugininfo.ContextClearResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !res.OK || !res.Deleted || len(src.deleted) != 1 {
		t.Fatalf("删除结果不符: %+v", res)
	}

	// 执行中且未确认终止 → 409
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/contexts?key=g:qq:2", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("忙时删除 status = %d, want 409", rec.Code)
	}

	// 确认终止 → 成功
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/contexts?key=g:qq:2&terminate=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("终止删除 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 缺少 key → 400
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/contexts", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少 key status = %d, want 400", rec.Code)
	}
}
