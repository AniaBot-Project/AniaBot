package adminpanel

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeTaskPanelSource 测试用 TaskSource + GoalSource 实现，记录收到的 limit。
type fakeTaskPanelSource struct {
	tasks    []AgentTaskView
	goals    []AgentGoalView
	gotLimit int
	calls    int
}

func (f *fakeTaskPanelSource) AgentTasks(limit int) []AgentTaskView {
	f.calls++
	f.gotLimit = limit
	return f.tasks
}

func (f *fakeTaskPanelSource) AgentGoals() []AgentGoalView { return f.goals }

// TestTasksAPINoSource 未启用数据源时两个接口都返回空数组（而非 null / 404）。
func TestTasksAPINoSource(t *testing.T) {
	s := &Server{opt: Options{}}

	rec := httptest.NewRecorder()
	s.handleAgentTasks(rec, httptest.NewRequest("GET", "/api/agent-tasks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("无数据源时 agent-tasks status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("无数据源时 agent-tasks body = %q, want []", got)
	}

	rec = httptest.NewRecorder()
	s.handleAgentGoals(rec, httptest.NewRequest("GET", "/api/agent-goals", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("无数据源时 agent-goals status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("无数据源时 agent-goals body = %q, want []", got)
	}
}

// TestTasksAPIEncoding 有数据源时任务与目标按 JSON 切片编码，字段名与类型正确。
func TestTasksAPIEncoding(t *testing.T) {
	src := &fakeTaskPanelSource{
		tasks: []AgentTaskView{{
			ID: "t-abc-1234", SessionKey: "g:qq:1", Kind: "subagent", Status: "running",
			Title: "调研", Result: "部分结果", Error: "", StopReason: "", Meta: `{"step":1}`,
			Tokens: 321, CreatedAt: 100, StartedAt: 101, UpdatedAt: 102, FinishedAt: 0,
		}},
		goals: []AgentGoalView{{
			SessionKey: "f:qq:2", Objective: "完成迁移", Phase: "active",
			RoundsStarted: 2, MaxRounds: 64, BlockedCode: "", BlockedMessage: "", UpdatedAt: 200,
		}},
	}
	s := &Server{opt: Options{AgentTasks: src.AgentTasks, AgentGoals: src.AgentGoals}}

	rec := httptest.NewRecorder()
	s.handleAgentTasks(rec, httptest.NewRequest("GET", "/api/agent-tasks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var tasks []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks len = %d, want 1", len(tasks))
	}
	if tasks[0]["id"] != "t-abc-1234" || tasks[0]["status"] != "running" || tasks[0]["session_key"] != "g:qq:1" {
		t.Fatalf("task 字段不符: %+v", tasks[0])
	}
	if tasks[0]["tokens"] != float64(321) || tasks[0]["updated_at"] != float64(102) {
		t.Fatalf("task 数值字段不符: %+v", tasks[0])
	}
	if _, ok := tasks[0]["stop_reason"]; !ok {
		t.Fatalf("task 缺少 stop_reason 字段: %+v", tasks[0])
	}

	rec = httptest.NewRecorder()
	s.handleAgentGoals(rec, httptest.NewRequest("GET", "/api/agent-goals", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("goals status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var goals []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &goals); err != nil {
		t.Fatalf("goals invalid JSON: %v\n%s", err, rec.Body.String())
	}
	if len(goals) != 1 || goals[0]["objective"] != "完成迁移" || goals[0]["rounds_started"] != float64(2) {
		t.Fatalf("goal 字段不符: %+v", goals)
	}
}

// TestTasksAPILimit 默认 100；合法值透传；超过 500 钳制到 500；非法值 400。
func TestTasksAPILimit(t *testing.T) {
	cases := []struct {
		query    string
		wantCode int
		wantCall bool
		wantLim  int
	}{
		{"", http.StatusOK, true, 100},
		{"?limit=7", http.StatusOK, true, 7},
		{"?limit=500", http.StatusOK, true, 500},
		{"?limit=1000", http.StatusOK, true, 500},
		{"?limit=abc", http.StatusBadRequest, false, 0},
		{"?limit=0", http.StatusBadRequest, false, 0},
		{"?limit=-5", http.StatusBadRequest, false, 0},
	}
	for _, c := range cases {
		src := &fakeTaskPanelSource{tasks: []AgentTaskView{}}
		s := &Server{opt: Options{AgentTasks: src.AgentTasks}}
		rec := httptest.NewRecorder()
		s.handleAgentTasks(rec, httptest.NewRequest("GET", "/api/agent-tasks"+c.query, nil))
		if rec.Code != c.wantCode {
			t.Fatalf("query %q: status = %d, want %d (body=%s)", c.query, rec.Code, c.wantCode, rec.Body.String())
		}
		if c.wantCall {
			if src.calls != 1 || src.gotLimit != c.wantLim {
				t.Fatalf("query %q: calls=%d gotLimit=%d, want calls=1 limit=%d",
					c.query, src.calls, src.gotLimit, c.wantLim)
			}
		} else if src.calls != 0 {
			t.Fatalf("query %q: 非法 limit 不应调用数据源, calls=%d", c.query, src.calls)
		}
	}
}

// TestTasksAPIRoutes 注册后后台任务相关接口受登录保护（未登录 401）。
func TestTasksAPIRoutes(t *testing.T) {
	s := &Server{opt: Options{}, mux: http.NewServeMux()}
	s.routes()
	cases := []struct{ method, path string }{
		{"GET", "/api/agent-tasks"},
		{"GET", "/api/agent-tasks/export"},
		{"DELETE", "/api/agent-tasks"},
		{"GET", "/api/agent-goals"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录 status = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

// TestTasksAPIExport 导出接口把筛选条件透传给数据源，返回带下载文件名的 JSON 附件；
// 无数据源时 404（与其它日志导出接口一致）。
func TestTasksAPIExport(t *testing.T) {
	var got AgentTaskFilter
	calls := 0
	s := &Server{opt: Options{
		Logger: slog.Default(),
		AgentTasksExport: func(f AgentTaskFilter) []AgentTaskView {
			calls++
			got = f
			return []AgentTaskView{{ID: "t-abc", SessionKey: "g:qq:1", Kind: "subagent", Status: "completed"}}
		},
	}}
	rec := httptest.NewRecorder()
	s.handleAgentTasksExport(rec, httptest.NewRequest("GET",
		"/api/agent-tasks/export?status=completed&kind=subagent&keyword=%E8%B0%83%E7%A0%94", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("导出数据源调用次数 = %d, want 1", calls)
	}
	if got.Status != "completed" || got.Kind != "subagent" || got.Keyword != "调研" {
		t.Fatalf("筛选条件未透传: %+v", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "agent-tasks-") {
		t.Fatalf("Content-Disposition = %q, want agent-tasks 文件名", cd)
	}
	var payload struct {
		Total int              `json:"total"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	if payload.Total != 1 || len(payload.Items) != 1 || payload.Items[0]["id"] != "t-abc" {
		t.Fatalf("导出内容不符: %+v", payload)
	}

	// 无数据源：404
	rec = httptest.NewRecorder()
	(&Server{opt: Options{Logger: slog.Default()}}).handleAgentTasksExport(rec,
		httptest.NewRequest("GET", "/api/agent-tasks/export", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无数据源导出 status = %d, want 404", rec.Code)
	}
}

// TestTasksAPIClear 清除接口把筛选条件透传给数据源并返回删除条数；无数据源时 404。
func TestTasksAPIClear(t *testing.T) {
	var got AgentTaskFilter
	s := &Server{opt: Options{
		Logger:          slog.Default(),
		AgentTasksClear: func(f AgentTaskFilter) int { got = f; return 3 },
	}}
	rec := httptest.NewRecorder()
	s.handleAgentTasksClear(rec, httptest.NewRequest("DELETE",
		"/api/agent-tasks?status=failed&kind=team_member&keyword=%E5%8D%9A%E5%AE%A2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got.Status != "failed" || got.Kind != "team_member" || got.Keyword != "博客" {
		t.Fatalf("筛选条件未透传: %+v", got)
	}
	var res struct {
		OK      bool `json:"ok"`
		Removed int  `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	if !res.OK || res.Removed != 3 {
		t.Fatalf("清除响应不符: %+v", res)
	}

	rec = httptest.NewRecorder()
	(&Server{opt: Options{Logger: slog.Default()}}).handleAgentTasksClear(rec,
		httptest.NewRequest("DELETE", "/api/agent-tasks", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无数据源清除 status = %d, want 404", rec.Code)
	}
}
