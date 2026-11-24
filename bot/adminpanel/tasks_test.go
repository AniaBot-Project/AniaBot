package adminpanel

import (
	"encoding/json"
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

// TestTasksAPIRoutes 注册后 /api/agent-tasks 与 /api/agent-goals 受登录保护（未登录 401）。
func TestTasksAPIRoutes(t *testing.T) {
	s := &Server{opt: Options{}, mux: http.NewServeMux()}
	s.routes()
	for _, path := range []string{"/api/agent-tasks", "/api/agent-goals"} {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未登录 status = %d, want 401", path, rec.Code)
		}
	}
}
