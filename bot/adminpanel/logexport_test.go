package adminpanel

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
)

// TestQueryLogsExport 导出接口按筛选条件取全部记录，带下载文件名。
func TestQueryLogsExport(t *testing.T) {
	now := time.Now()
	var got querylog.Filter
	s := &Server{opt: Options{
		Logger: slog.Default(),
		QueryLogs: func(f querylog.Filter) []querylog.Entry {
			got = f
			return []querylog.Entry{{
				ID: "1", Time: now, ChatType: "group", TargetID: "100", Query: "你好",
				Status: querylog.StatusSuccess,
				ToolCalls: []querylog.ToolCallRecord{
					{Name: "bash", Result: strings.Repeat("x", 3000)},
				},
			}}
		},
	}}
	rec := httptest.NewRecorder()
	s.handleQueryLogsExport(rec, httptest.NewRequest("GET", "/api/querylogs/export?chat_type=group&kind=proactive", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got.ChatType != "group" || got.Kind != querylog.KindProactive {
		t.Fatalf("筛选条件未传给数据源: %+v", got)
	}
	if got.Limit != 0 {
		t.Fatalf("导出应不分页, limit = %d", got.Limit)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "querylogs-") {
		t.Fatalf("缺少下载文件名: %q", cd)
	}
	var out struct {
		Total int              `json:"total"`
		Items []querylog.Entry `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析导出 JSON 失败: %v", err)
	}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].Query != "你好" {
		t.Fatalf("导出内容异常: %+v", out)
	}
	// 工具结果完整导出（不截断）
	if n := len(out.Items[0].ToolCalls[0].Result); n != 3000 {
		t.Fatalf("工具结果应完整导出, got %d 字符", n)
	}
}

// TestQueryLogsClear 清除接口把筛选条件透传给插件并返回删除条数。
func TestQueryLogsClear(t *testing.T) {
	var got querylog.Filter
	s := &Server{opt: Options{
		Logger: slog.Default(),
		QueryLogsClear: func(f querylog.Filter) int {
			got = f
			return 3
		},
	}}
	rec := httptest.NewRecorder()
	s.handleQueryLogsClear(rec, httptest.NewRequest("DELETE", "/api/querylogs?keyword=天气&chat_type=group", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got.Keyword != "天气" || got.ChatType != "group" {
		t.Fatalf("筛选条件未透传: %+v", got)
	}
	var out struct {
		OK      bool `json:"ok"`
		Removed int  `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.OK || out.Removed != 3 {
		t.Fatalf("响应异常: %+v, err=%v", out, err)
	}

	rec = httptest.NewRecorder()
	s.handleQueryLogsClear(rec, httptest.NewRequest("DELETE", "/api/querylogs?kind=bad", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法筛选应返回 400, got %d", rec.Code)
	}
}

// TestTaskLogsExportAndClear 定时任务执行日志的导出与清除。
func TestTaskLogsExportAndClear(t *testing.T) {
	var exportFilter tasklog.Filter
	s := &Server{opt: Options{
		Logger: slog.Default(),
		TaskLogs: func(f tasklog.Filter) []tasklog.Entry {
			exportFilter = f
			return []tasklog.Entry{{
				ID: "1", TaskID: "t1", TaskTitle: "巡检", TargetType: "group", TargetID: "100",
				TriggerTime: time.Now(), Status: tasklog.StatusSuccess,
			}}
		},
		TaskLogsClear: func(f tasklog.Filter) int {
			if f.Status != "" || f.TargetType != "group" {
				t.Fatalf("清除筛选条件异常: %+v", f)
			}
			return 1
		},
	}}
	rec := httptest.NewRecorder()
	s.handleTaskLogsExport(rec, httptest.NewRequest("GET", "/api/tasklogs/export?target_type=group", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if exportFilter.TargetType != "group" || exportFilter.Limit != 0 {
		t.Fatalf("导出筛选条件异常: %+v", exportFilter)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "tasklogs-") {
		t.Fatalf("缺少下载文件名: %q", cd)
	}

	rec = httptest.NewRecorder()
	s.handleTaskLogsClear(rec, httptest.NewRequest("DELETE", "/api/tasklogs?target_type=group", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK      bool `json:"ok"`
		Removed int  `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.OK || out.Removed != 1 {
		t.Fatalf("响应异常: %+v, err=%v", out, err)
	}
}
