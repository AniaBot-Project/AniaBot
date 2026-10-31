package adminpanel

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

type fakeSkillDetailSource struct{}

func (fakeSkillDetailSource) SkillList() ([]plugininfo.SkillInfo, string, []string) {
	return nil, "", nil
}

func (fakeSkillDetailSource) SkillDelete(string) error {
	return nil
}

func (fakeSkillDetailSource) SkillUpload(string, []byte) error {
	return nil
}

func (fakeSkillDetailSource) SkillDetail(name string) (plugininfo.SkillDetail, error) {
	return plugininfo.SkillDetail{
		Name:        name,
		Description: "测试详情",
		Location:    "demo/SKILL.md",
		Content:     "# demo",
		Files: []plugininfo.SkillFileInfo{
			{Name: "reference.md", Kind: "reference", Size: 9, Content: "reference"},
			{Name: "run.sh", Kind: "extra", Size: 7},
		},
	}, nil
}

func TestHandleSkillDetail(t *testing.T) {
	s := &Server{opt: Options{Skills: fakeSkillDetailSource{}}}
	mux := http.NewServeMux()
	mux.Handle("GET /api/skills/{name}", http.HandlerFunc(s.handleSkillDetail))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/skills/demo", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got plugininfo.SkillDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	if got.Name != "demo" || got.Content != "# demo" {
		t.Fatalf("detail = %+v", got)
	}
	if len(got.Files) != 2 || got.Files[0].Name != "reference.md" || got.Files[0].Content != "reference" {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestHandleSkillDetailUnsupported(t *testing.T) {
	s := &Server{opt: Options{Skills: skillSourceWithoutDetail{}}}
	mux := http.NewServeMux()
	mux.Handle("GET /api/skills/{name}", http.HandlerFunc(s.handleSkillDetail))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/skills/demo", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

type skillSourceWithoutDetail struct{}

func (skillSourceWithoutDetail) SkillList() ([]plugininfo.SkillInfo, string, []string) {
	return nil, "", nil
}

func (skillSourceWithoutDetail) SkillDelete(string) error {
	return nil
}

func (skillSourceWithoutDetail) SkillUpload(string, []byte) error {
	return nil
}

// fakeSkillToggleSource 记录启停调用，用于校验面板启停接口。
type fakeSkillToggleSource struct {
	fakeSkillDetailSource
	name    string
	enabled bool
	calls   int
}

func (f *fakeSkillToggleSource) SkillSetEnabled(name string, enabled bool) error {
	f.name, f.enabled = name, enabled
	f.calls++
	return nil
}

func TestHandleSkillToggle(t *testing.T) {
	src := &fakeSkillToggleSource{}
	s := &Server{opt: Options{Skills: src, Logger: slog.Default()}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/skills/{name}/toggle", http.HandlerFunc(s.handleSkillToggle))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/skills/demo/toggle", strings.NewReader(`{"enabled":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if src.calls != 1 || src.name != "demo" || src.enabled {
		t.Fatalf("toggle 调用不符: %+v", src)
	}

	// 缺少 enabled 字段应 400
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/skills/demo/toggle", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少 enabled 字段应 400，got %d", rec.Code)
	}
}

func TestHandleSkillToggleUnsupported(t *testing.T) {
	s := &Server{opt: Options{Skills: skillSourceWithoutDetail{}}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/skills/{name}/toggle", http.HandlerFunc(s.handleSkillToggle))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/skills/demo/toggle", strings.NewReader(`{"enabled":true}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
