package adminpanel

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
)

func TestTokenStatsDetail(t *testing.T) {
	now := time.Now()
	s := &Server{opt: Options{
		QueryLogs: func(f querylog.Filter) []querylog.Entry {
			return []querylog.Entry{
				{Time: now, ChatType: "group", TargetID: "100", Status: querylog.StatusSuccess, Iterations: 3, PromptTokens: 1000, CompletionTokens: 200, TotalTokens: 1200, CachedTokens: 400},
				{Time: now.Add(-26 * time.Hour), ChatType: "friend", TargetID: "200", Status: querylog.StatusError, Iterations: 1, PromptTokens: 500, CompletionTokens: 100, TotalTokens: 600},
			}
		},
		TaskLogs: func(f tasklog.Filter) []tasklog.Entry {
			return []tasklog.Entry{
				{TriggerTime: now, TargetType: "group", TargetID: "100", Status: tasklog.StatusTimeout, Iterations: 2, PromptTokens: 800, CompletionTokens: 300, TotalTokens: 1100},
			}
		},
	}}
	rec := httptest.NewRecorder()
	s.handleTokenStatsDetail(rec, httptest.NewRequest("GET", "/api/tokenstats/detail", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	for _, k := range []string{"summary", "today", "by_source", "by_chat_type", "by_status", "top_targets", "hourly", "daily", "avg_iterations"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing key %q", k)
		}
	}
	summary := got["summary"].(map[string]any)
	if summary["requests"].(float64) != 3 || summary["total_tokens"].(float64) != 2900 {
		t.Fatalf("summary = %v", summary)
	}
	daily := got["daily"].([]any)
	if len(daily) != 30 {
		t.Fatalf("daily len = %d", len(daily))
	}
	hourly := got["hourly"].([]any)
	if len(hourly) != 24 {
		t.Fatalf("hourly len = %d", len(hourly))
	}
	top := got["top_targets"].([]any)
	if len(top) != 2 {
		t.Fatalf("top_targets len = %d", len(top))
	}
	first := top[0].(map[string]any)
	if first["target_id"] != "100" || first["total_tokens"].(float64) != 2300 {
		t.Fatalf("top[0] = %v", first)
	}
}

// range 窗口应同时作用于日志查询条件（Start/End）与 daily 序列长度。
func TestTokenStatsDetailRange(t *testing.T) {
	now := time.Now()
	yesterdayNoon := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(-12 * time.Hour)
	newServer := func() *Server {
		return &Server{opt: Options{
			QueryLogs: func(f querylog.Filter) []querylog.Entry {
				entries := []querylog.Entry{
					{Time: now, ChatType: "group", TargetID: "100", Status: querylog.StatusSuccess, TotalTokens: 1200},
					{Time: yesterdayNoon, ChatType: "friend", TargetID: "200", Status: querylog.StatusError, TotalTokens: 600},
					{Time: now.AddDate(0, 0, -10), ChatType: "group", TargetID: "300", Status: querylog.StatusSuccess, TotalTokens: 100},
				}
				var out []querylog.Entry
				for _, e := range entries {
					if !f.Start.IsZero() && e.Time.Before(f.Start) {
						continue
					}
					if !f.End.IsZero() && e.Time.After(f.End) {
						continue
					}
					out = append(out, e)
				}
				return out
			},
		}}
	}

	get := func(t *testing.T, url string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		newServer().handleTokenStatsDetail(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: status = %d, body = %s", url, rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: invalid JSON: %v", url, err)
		}
		return got
	}

	// 今日：仅包含今天的记录，daily 只有 1 天
	got := get(t, "/api/tokenstats/detail?range=today")
	if got["range"] != "today" {
		t.Fatalf("range = %v", got["range"])
	}
	if s := got["summary"].(map[string]any); s["requests"].(float64) != 1 || s["total_tokens"].(float64) != 1200 {
		t.Fatalf("today summary = %v", s)
	}
	if d := got["daily"].([]any); len(d) != 1 {
		t.Fatalf("today daily len = %d", len(d))
	}

	// 近 7 天：10 天前的记录被排除，daily 为 7 天
	got = get(t, "/api/tokenstats/detail?range=7d")
	if s := got["summary"].(map[string]any); s["requests"].(float64) != 2 || s["total_tokens"].(float64) != 1800 {
		t.Fatalf("7d summary = %v", s)
	}
	if d := got["daily"].([]any); len(d) != 7 {
		t.Fatalf("7d daily len = %d", len(d))
	}

	// 昨日：仅昨日记录（今日与 10 天前均被排除），daily 为 1 天
	got = get(t, "/api/tokenstats/detail?range=yesterday")
	if s := got["summary"].(map[string]any); s["requests"].(float64) != 1 || s["total_tokens"].(float64) != 600 {
		t.Fatalf("yesterday summary = %v", s)
	}
	if d := got["daily"].([]any); len(d) != 1 {
		t.Fatalf("yesterday daily len = %d", len(d))
	}

	// 自定义：昨天~今天 → 2 天，含今日与昨日记录
	start := yesterdayNoon.Format("2006-01-02")
	end := now.Format("2006-01-02")
	got = get(t, "/api/tokenstats/detail?range=custom&start="+start+"&end="+end)
	if s := got["summary"].(map[string]any); s["requests"].(float64) != 2 || s["total_tokens"].(float64) != 1800 {
		t.Fatalf("custom summary = %v", s)
	}
	if d := got["daily"].([]any); len(d) != 2 {
		t.Fatalf("custom daily len = %d", len(d))
	}

	// 自定义缺参数 / 跨度超限：400
	for _, url := range []string{
		"/api/tokenstats/detail?range=custom",
		"/api/tokenstats/detail?range=custom&start=" + start,
		"/api/tokenstats/detail?range=custom&start=2020-01-01&end=2020-06-01",
		"/api/tokenstats/detail?range=custom&start=" + end + "&end=" + start,
	} {
		rec := httptest.NewRecorder()
		newServer().handleTokenStatsDetail(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 400 {
			t.Fatalf("%s: status = %d", url, rec.Code)
		}
	}

	// 非法 range：400
	rec := httptest.NewRecorder()
	newServer().handleTokenStatsDetail(rec, httptest.NewRequest("GET", "/api/tokenstats/detail?range=bogus", nil))
	if rec.Code != 400 {
		t.Fatalf("bogus range: status = %d", rec.Code)
	}
}

// 主动互动（Kind=proactive）在 Token 统计中单独计量，不再混入「对话」。
func TestTokenStatsDetailProactiveSource(t *testing.T) {
	now := time.Now()
	s := &Server{opt: Options{
		QueryLogs: func(f querylog.Filter) []querylog.Entry {
			return []querylog.Entry{
				{Time: now, ChatType: "group", TargetID: "100", Status: querylog.StatusSuccess, TotalTokens: 1000},
				{Time: now, Kind: querylog.KindProactive, ChatType: "group", TargetID: "100", Status: querylog.StatusSkipped, TotalTokens: 400},
			}
		},
	}}
	rec := httptest.NewRecorder()
	s.handleTokenStatsDetail(rec, httptest.NewRequest("GET", "/api/tokenstats/detail", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got struct {
		BySource map[string]struct {
			Requests    int `json:"requests"`
			TotalTokens int `json:"total_tokens"`
		} `json:"by_source"`
		ByStatus map[string]int `json:"by_status"`
		Daily    []struct {
			Query struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"query"`
			Proactive struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"proactive"`
		} `json:"daily"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.BySource["proactive"].TotalTokens != 400 || got.BySource["proactive"].Requests != 1 {
		t.Fatalf("by_source.proactive = %+v", got.BySource["proactive"])
	}
	if got.BySource["query"].TotalTokens != 1000 || got.BySource["query"].Requests != 1 {
		t.Fatalf("by_source.query = %+v", got.BySource["query"])
	}
	if got.ByStatus["skipped"] != 1 {
		t.Fatalf("by_status.skipped = %d", got.ByStatus["skipped"])
	}
	last := got.Daily[len(got.Daily)-1]
	if last.Proactive.TotalTokens != 400 || last.Query.TotalTokens != 1000 {
		t.Fatalf("daily 末尾 = %+v", last)
	}
}
