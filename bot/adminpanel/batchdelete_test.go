package adminpanel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// ---- 记忆批量删除 ----

// fakeMemorySource 只实现基础 MemorySource（不实现批量接口），用于验证逐条回退路径。
type fakeMemorySource struct {
	deleted map[string]bool
	failIDs map[string]bool
	calls   int
}

func newFakeMemorySource() *fakeMemorySource {
	return &fakeMemorySource{deleted: map[string]bool{}, failIDs: map[string]bool{}}
}

func (f *fakeMemorySource) MemoryScopes() []plugininfo.MemoryScopeInfo { return nil }

func (f *fakeMemorySource) MemoryList(string) ([]plugininfo.MemoryEntryInfo, error) { return nil, nil }

func (f *fakeMemorySource) MemoryCreate(plugininfo.MemoryEntryUpsert) (string, error) {
	return "", nil
}

func (f *fakeMemorySource) MemoryUpdate(plugininfo.MemoryEntryUpsert) error { return nil }

func (f *fakeMemorySource) MemoryDelete(scope, id string) error {
	f.calls++
	if f.failIDs[id] {
		return fmt.Errorf("记忆不存在: %s", id)
	}
	f.deleted[scope+"/"+id] = true
	return nil
}

// fakeMemoryBatchSource 额外实现批量删除接口，一次性记录整批 ID。
type fakeMemoryBatchSource struct {
	*fakeMemorySource
	batchCalls int
	gotScope   string
	gotIDs     []string
}

func (f *fakeMemoryBatchSource) MemoryDeleteMany(scope string, ids []string) (int, error) {
	f.batchCalls++
	f.gotScope, f.gotIDs = scope, ids
	n := 0
	for _, id := range ids {
		if !f.failIDs[id] {
			n++
		}
	}
	return n, nil
}

func TestHandleMemoryBatchDelete(t *testing.T) {
	src := &fakeMemoryBatchSource{fakeMemorySource: newFakeMemorySource()}
	s := &Server{opt: Options{Memories: src}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/memory/batch-delete", http.HandlerFunc(s.handleMemoryBatchDelete))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/memory/batch-delete",
		strings.NewReader(`{"scope":"g:123","ids":["a","b"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if src.batchCalls != 1 || src.calls != 0 {
		t.Fatalf("应走批量接口一次（不逐条删除）: batch=%d single=%d", src.batchCalls, src.calls)
	}
	if src.gotScope != "g:123" || len(src.gotIDs) != 2 || src.gotIDs[0] != "a" || src.gotIDs[1] != "b" {
		t.Fatalf("批量删除参数不符: scope=%q ids=%v", src.gotScope, src.gotIDs)
	}
	var got struct {
		OK      bool `json:"ok"`
		Deleted int  `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.OK || got.Deleted != 2 {
		t.Fatalf("响应不符: %s (err=%v)", rec.Body.String(), err)
	}
}

func TestHandleMemoryBatchDeleteFallback(t *testing.T) {
	src := newFakeMemorySource()
	src.failIDs["gone"] = true
	s := &Server{opt: Options{Memories: src}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/memory/batch-delete", http.HandlerFunc(s.handleMemoryBatchDelete))

	// 只实现基础接口时逐条删除：不存在的 ID 跳过，其余照常删除
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/memory/batch-delete",
		strings.NewReader(`{"scope":"g:123","ids":["a","gone","b"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if src.calls != 3 {
		t.Fatalf("应逐条调用 3 次，实际 %d 次", src.calls)
	}
	if !src.deleted["g:123/a"] || !src.deleted["g:123/b"] {
		t.Fatalf("应删除 a、b 两条，实际 %v", src.deleted)
	}
	var got struct {
		Deleted int `json:"deleted"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Deleted != 2 {
		t.Fatalf("deleted = %d，期望 2（body=%s）", got.Deleted, rec.Body.String())
	}

	// 全部失败（如功能未启用）时返回错误，而不是假装删除成功
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/memory/batch-delete",
		strings.NewReader(`{"scope":"g:123","ids":["gone"]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("全部失败应返回 400，got %d（body=%s）", rec.Code, rec.Body.String())
	}
}

func TestHandleMemoryBatchDeleteValidation(t *testing.T) {
	s := &Server{opt: Options{Memories: newFakeMemorySource()}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/memory/batch-delete", http.HandlerFunc(s.handleMemoryBatchDelete))

	for name, body := range map[string]string{
		"缺少 scope": `{"ids":["a"]}`,
		"缺少 ids":   `{"scope":"g:123"}`,
		"空 ids":    `{"scope":"g:123","ids":[]}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/memory/batch-delete", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，got %d（body=%s）", name, rec.Code, rec.Body.String())
		}
	}

	// 功能未启用（数据源为 nil）时返回 404
	rec := httptest.NewRecorder()
	(&Server{opt: Options{}}).handleMemoryBatchDelete(rec,
		httptest.NewRequest("POST", "/api/memory/batch-delete", strings.NewReader(`{"scope":"g:123","ids":["a"]}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("功能未启用应返回 404，got %d", rec.Code)
	}
}

// ---- 知识库批量删除 ----

// fakeKnowledgeSource 只实现基础 KnowledgeBaseSource，用于验证逐条回退路径。
type fakeKnowledgeSource struct {
	deleted map[string]bool
	failIDs map[string]bool
	calls   int
}

func newFakeKnowledgeSource() *fakeKnowledgeSource {
	return &fakeKnowledgeSource{deleted: map[string]bool{}, failIDs: map[string]bool{}}
}

func (f *fakeKnowledgeSource) KnowledgeScopes() []plugininfo.KnowledgeScopeInfo { return nil }

func (f *fakeKnowledgeSource) KnowledgeList(string) ([]plugininfo.KnowledgeDocInfo, error) {
	return nil, nil
}

func (f *fakeKnowledgeSource) KnowledgeCreate(plugininfo.KnowledgeDocUpsert) (string, error) {
	return "", nil
}

func (f *fakeKnowledgeSource) KnowledgeUpdate(plugininfo.KnowledgeDocUpsert) error { return nil }

func (f *fakeKnowledgeSource) KnowledgeDelete(scope, id string) error {
	f.calls++
	if f.failIDs[id] {
		return fmt.Errorf("文档不存在: %s", id)
	}
	f.deleted[scope+"/"+id] = true
	return nil
}

func (f *fakeKnowledgeSource) KnowledgeImportURL(string, string) (string, error) { return "", nil }

// fakeKnowledgeBatchSource 额外实现批量删除接口，一次性记录整批 ID。
type fakeKnowledgeBatchSource struct {
	*fakeKnowledgeSource
	batchCalls int
	gotScope   string
	gotIDs     []string
}

func (f *fakeKnowledgeBatchSource) KnowledgeDeleteMany(scope string, ids []string) (int, error) {
	f.batchCalls++
	f.gotScope, f.gotIDs = scope, ids
	n := 0
	for _, id := range ids {
		if !f.failIDs[id] {
			n++
		}
	}
	return n, nil
}

func TestHandleKnowledgeBatchDelete(t *testing.T) {
	src := &fakeKnowledgeBatchSource{fakeKnowledgeSource: newFakeKnowledgeSource()}
	s := &Server{opt: Options{Knowledge: src}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/knowledge/batch-delete", http.HandlerFunc(s.handleKnowledgeBatchDelete))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/knowledge/batch-delete",
		strings.NewReader(`{"scope":"global","ids":["a","b","c"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if src.batchCalls != 1 || src.calls != 0 {
		t.Fatalf("应走批量接口一次（不逐条删除）: batch=%d single=%d", src.batchCalls, src.calls)
	}
	if src.gotScope != "global" || len(src.gotIDs) != 3 {
		t.Fatalf("批量删除参数不符: scope=%q ids=%v", src.gotScope, src.gotIDs)
	}
	var got struct {
		Deleted int `json:"deleted"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Deleted != 3 {
		t.Fatalf("deleted = %d，期望 3（body=%s）", got.Deleted, rec.Body.String())
	}
}

func TestHandleKnowledgeBatchDeleteFallback(t *testing.T) {
	src := newFakeKnowledgeSource()
	src.failIDs["gone"] = true
	s := &Server{opt: Options{Knowledge: src}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/knowledge/batch-delete", http.HandlerFunc(s.handleKnowledgeBatchDelete))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/knowledge/batch-delete",
		strings.NewReader(`{"scope":"global","ids":["a","gone"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if src.calls != 2 || !src.deleted["global/a"] {
		t.Fatalf("逐条回退结果不符: calls=%d deleted=%v", src.calls, src.deleted)
	}
	var got struct {
		Deleted int `json:"deleted"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Deleted != 1 {
		t.Fatalf("deleted = %d，期望 1", got.Deleted)
	}

	// 空 ids / 空 scope 应返回 400
	for name, body := range map[string]string{
		"缺少 scope": `{"ids":["a"]}`,
		"空 ids":    `{"scope":"global","ids":[]}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/knowledge/batch-delete", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，got %d", name, rec.Code)
		}
	}

	// 功能未启用（数据源为 nil）时返回 404
	rec = httptest.NewRecorder()
	(&Server{opt: Options{}}).handleKnowledgeBatchDelete(rec,
		httptest.NewRequest("POST", "/api/knowledge/batch-delete", strings.NewReader(`{"scope":"global","ids":["a"]}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("功能未启用应返回 404，got %d", rec.Code)
	}
}
