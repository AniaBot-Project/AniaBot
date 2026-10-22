package adminpanel

import (
	"net/http"
	"sort"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
)

// token 用量聚合：总量 / 今日 / 每日序列共用同一结构。
type tokenStatAcc struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CacheHitRate     float64 `json:"cache_hit_rate"` // cached / prompt，prompt 为 0 时为 0
}

func (a *tokenStatAcc) add(prompt, completion, total, cached int) {
	a.Requests++
	a.PromptTokens += prompt
	a.CompletionTokens += completion
	a.TotalTokens += total
	a.CachedTokens += cached
}

func (a *tokenStatAcc) finish() {
	if a.PromptTokens > 0 {
		// 上游语义中 prompt_tokens 包含命中缓存的部分（hit + miss），
		// 因此命中率 = cached / prompt
		a.CacheHitRate = float64(a.CachedTokens) / float64(a.PromptTokens)
	}
}

// tokenDayStat 单日用量（daily 序列元素）。
type tokenDayStat struct {
	tokenStatAcc
	Date string `json:"date"` // 2006-01-02（本地时区）
}

// tokenStatsDailyDays daily 序列覆盖的天数（含今天）
const tokenStatsDailyDays = 14

// handleTokenStats 返回 token 消耗监控指标：历史总量、今日用量与最近 14 天
// 每日用量（含缓存命中率）。数据源为 Query 日志与定时任务执行日志的留存记录
// （各受容量上限约束，非全量历史），缓存字段依赖上游 API 返回，不支持时为 0。
func (s *Server) handleTokenStats(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	todayStr := now.Format("2006-01-02")

	// 预建最近 N 天的桶（含无数据的日期，前端直接渲染连续序列）
	daily := make([]tokenDayStat, 0, tokenStatsDailyDays)
	buckets := make(map[string]*tokenStatAcc, tokenStatsDailyDays)
	for i := tokenStatsDailyDays - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		ds := d.Format("2006-01-02")
		daily = append(daily, tokenDayStat{Date: ds})
		buckets[ds] = &daily[len(daily)-1].tokenStatAcc
	}

	var summary, today tokenStatAcc
	addEntry := func(t time.Time, prompt, completion, total, cached int) {
		summary.add(prompt, completion, total, cached)
		ds := t.Local().Format("2006-01-02")
		if ds == todayStr {
			today.add(prompt, completion, total, cached)
		}
		if b, ok := buckets[ds]; ok {
			b.add(prompt, completion, total, cached)
		}
	}

	if s.opt.QueryLogs != nil {
		for _, e := range s.opt.QueryLogs(querylog.Filter{Limit: 500}) {
			addEntry(e.Time, e.PromptTokens, e.CompletionTokens, e.TotalTokens, e.CachedTokens)
		}
	}
	if s.opt.TaskLogs != nil {
		for _, e := range s.opt.TaskLogs(tasklog.Filter{Limit: 500}) {
			addEntry(e.TriggerTime, e.PromptTokens, e.CompletionTokens, e.TotalTokens, e.CachedTokens)
		}
	}

	summary.finish()
	today.finish()
	for i := range daily {
		daily[i].finish()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"summary": summary,
		"today":   today,
		"daily":   daily,
	})
}

// tokenDetailDailyDays 详细统计 daily 序列覆盖的默认天数（range=all 时，含今天）
const tokenDetailDailyDays = 30

// tokenRange 时间维度筛选窗口：start/end 界定统计范围（含端点，零值不限），
// days 为 daily 序列覆盖的天数（含窗口最后一天）。
type tokenRange struct {
	key   string
	start time.Time
	end   time.Time
	days  int
}

// tokenCustomMaxDays 自定义日期范围的最大跨度（天）
const tokenCustomMaxDays = 62

// resolveTokenRange 把 range 查询参数解析为统计窗口，非法值返回 false。
// 支持：today（今日）、yesterday（昨日）、7d（近 7 天）、30d（近 30 天）、
// month（本月）、all / 空（全部留存，daily 序列仍取最近 30 天）。均按本地时区。
// custom（自定义）需配合 start / end（2006-01-02，含端点两天，跨度 1~62 天）。
func resolveTokenRange(key, startStr, endStr string, now time.Time) (tokenRange, bool) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch key {
	case "", "all":
		return tokenRange{key: "all", days: tokenDetailDailyDays}, true
	case "today":
		return tokenRange{key: key, start: today, days: 1}, true
	case "yesterday":
		// end 取今日 0 点前一纳秒（Filter 的 End 为含端点语义）
		return tokenRange{key: key, start: today.AddDate(0, 0, -1), end: today.Add(-time.Nanosecond), days: 1}, true
	case "7d":
		return tokenRange{key: key, start: today.AddDate(0, 0, -6), days: 7}, true
	case "30d":
		return tokenRange{key: key, start: today.AddDate(0, 0, -29), days: 30}, true
	case "month":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return tokenRange{key: key, start: first, days: now.Day()}, true
	case "custom":
		sd, err1 := time.ParseInLocation("2006-01-02", startStr, now.Location())
		ed, err2 := time.ParseInLocation("2006-01-02", endStr, now.Location())
		if err1 != nil || err2 != nil {
			return tokenRange{}, false
		}
		days := int(ed.Sub(sd)/(24*time.Hour)) + 1
		if days < 1 || days > tokenCustomMaxDays {
			return tokenRange{}, false
		}
		// end 取结束日后一天的 0 点前一纳秒（含结束日全天）
		return tokenRange{key: key, start: sd, end: ed.AddDate(0, 0, 1).Add(-time.Nanosecond), days: days}, true
	default:
		return tokenRange{}, false
	}
}

// tokenTopTargets 详细统计中按目标（群/好友）排行的条目数上限
const tokenTopTargets = 10

// tokenDayDetail 单日用量（按来源拆分：对话 / 主动互动 / 定时任务）。
type tokenDayDetail struct {
	Date      string       `json:"date"`
	Query     tokenStatAcc `json:"query"`
	Proactive tokenStatAcc `json:"proactive"`
	Task      tokenStatAcc `json:"task"`
	Total     tokenStatAcc `json:"total"`
}

// tokenTargetStat 按会话目标（目标会话 ID / 用户 ID）聚合的用量。
type tokenTargetStat struct {
	tokenStatAcc
	ChatType string `json:"chat_type"` // group / friend
	TargetID string `json:"target_id"`
}

// tokenStatSummary tokenStatAcc 的扁平展示结构（供详细统计的拆分维度使用）。
type tokenStatSummary struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
	// 单次请求平均消耗（无请求时为 0）
	AvgTotalTokens float64 `json:"avg_total_tokens"`
}

func summarize(a *tokenStatAcc) tokenStatSummary {
	s := tokenStatSummary{
		Requests:         a.Requests,
		PromptTokens:     a.PromptTokens,
		CompletionTokens: a.CompletionTokens,
		TotalTokens:      a.TotalTokens,
		CachedTokens:     a.CachedTokens,
		CacheHitRate:     a.CacheHitRate,
	}
	if a.Requests > 0 {
		s.AvgTotalTokens = float64(a.TotalTokens) / float64(a.Requests)
	}
	return s
}

// handleTokenStatsDetail 返回更细粒度的 token 统计维度：按来源（对话 / 主动互动 /
// 定时任务）、会话类型（群聊 / 私聊）、执行状态、消耗目标排行（Top 10）、24 小时分布与
// 分来源的每日序列。数据源与 handleTokenStats 相同（留存日志，非全量历史）。
// 支持查询参数 range 限定统计窗口：today / yesterday / 7d / 30d / month / all
// （默认 all，即全部留存记录）/ custom（自定义，需配合 start / end=2006-01-02，
// 跨度 1~62 天）；daily 序列覆盖窗口内天数（all 时仍为最近 30 天）。
func (s *Server) handleTokenStatsDetail(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	q := r.URL.Query()
	rng, ok := resolveTokenRange(q.Get("range"), q.Get("start"), q.Get("end"), now)
	if !ok {
		writeError(w, http.StatusBadRequest, "range 仅支持 today / yesterday / 7d / 30d / month / all / custom（custom 需配合 start / end=2006-01-02，跨度 1~62 天）")
		return
	}
	todayStr := now.Format("2006-01-02")

	// 预建窗口内每天的桶（分来源，含无数据的日期）；窗口无 end 时以今天收尾
	endDay := now
	if !rng.end.IsZero() {
		endDay = rng.end
	}
	daily := make([]tokenDayDetail, 0, rng.days)
	buckets := make(map[string]*tokenDayDetail, rng.days)
	for i := rng.days - 1; i >= 0; i-- {
		ds := endDay.AddDate(0, 0, -i).Format("2006-01-02")
		daily = append(daily, tokenDayDetail{Date: ds})
		buckets[ds] = &daily[len(daily)-1]
	}

	var summary, today, byQuery, byProactive, byTask, byGroup, byFriend tokenStatAcc
	// hourly 与 daily 同构（按来源拆分，Date 字段不用），供单天窗口时前端直接渲染当日小时序列
	hourly := make([]tokenDayDetail, 24)
	statusCount := map[string]int{}
	targets := make(map[string]*tokenTargetStat)
	var iterations int

	addEntry := func(t time.Time, source, chatType, targetID, status string, iters, prompt, completion, total, cached int) {
		summary.add(prompt, completion, total, cached)
		lt := t.Local()
		ds := lt.Format("2006-01-02")
		if ds == todayStr {
			today.add(prompt, completion, total, cached)
		}
		if b, ok := buckets[ds]; ok {
			b.Total.add(prompt, completion, total, cached)
			switch source {
			case "task":
				b.Task.add(prompt, completion, total, cached)
			case "proactive":
				b.Proactive.add(prompt, completion, total, cached)
			default:
				b.Query.add(prompt, completion, total, cached)
			}
		}
		switch source {
		case "task":
			byTask.add(prompt, completion, total, cached)
		case "proactive":
			byProactive.add(prompt, completion, total, cached)
		default:
			byQuery.add(prompt, completion, total, cached)
		}
		switch chatType {
		case "group":
			byGroup.add(prompt, completion, total, cached)
		case "friend":
			byFriend.add(prompt, completion, total, cached)
		}
		hh := &hourly[lt.Hour()]
		hh.Total.add(prompt, completion, total, cached)
		switch source {
		case "task":
			hh.Task.add(prompt, completion, total, cached)
		case "proactive":
			hh.Proactive.add(prompt, completion, total, cached)
		default:
			hh.Query.add(prompt, completion, total, cached)
		}
		if status != "" && status != "running" {
			// running 中的执行尚未产生最终用量，不计入状态分布
			statusCount[status]++
		}
		iterations += iters
		if targetID != "" {
			key := chatType + ":" + targetID
			ts, ok := targets[key]
			if !ok {
				ts = &tokenTargetStat{ChatType: chatType, TargetID: targetID}
				targets[key] = ts
			}
			ts.add(prompt, completion, total, cached)
		}
	}

	if s.opt.QueryLogs != nil {
		for _, e := range s.opt.QueryLogs(querylog.Filter{Start: rng.start, End: rng.end, Limit: 500}) {
			// 主动互动是 AI 自行发起的执行，与用户触发的对话分开计量
			source := "query"
			if e.KindValue() == querylog.KindProactive {
				source = "proactive"
			}
			addEntry(e.Time, source, e.ChatType, e.TargetID, string(e.Status),
				e.Iterations, e.PromptTokens, e.CompletionTokens, e.TotalTokens, e.CachedTokens)
		}
	}
	if s.opt.TaskLogs != nil {
		for _, e := range s.opt.TaskLogs(tasklog.Filter{Start: rng.start, End: rng.end, Limit: 500}) {
			addEntry(e.TriggerTime, "task", e.TargetType, e.TargetID, string(e.Status),
				e.Iterations, e.PromptTokens, e.CompletionTokens, e.TotalTokens, e.CachedTokens)
		}
	}

	summary.finish()
	today.finish()
	byQuery.finish()
	byProactive.finish()
	byTask.finish()
	byGroup.finish()
	byFriend.finish()
	for i := range hourly {
		hourly[i].Query.finish()
		hourly[i].Proactive.finish()
		hourly[i].Task.finish()
		hourly[i].Total.finish()
	}
	for i := range daily {
		daily[i].Query.finish()
		daily[i].Proactive.finish()
		daily[i].Task.finish()
		daily[i].Total.finish()
	}

	// 目标排行：按总消耗降序取 Top N
	type targetView struct {
		tokenStatSummary
		ChatType string `json:"chat_type"`
		TargetID string `json:"target_id"`
	}
	ranked := make([]targetView, 0, len(targets))
	for _, ts := range targets {
		ts.finish()
		ranked = append(ranked, targetView{summarize(&ts.tokenStatAcc), ts.ChatType, ts.TargetID})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].TotalTokens > ranked[j].TotalTokens })
	if len(ranked) > tokenTopTargets {
		ranked = ranked[:tokenTopTargets]
	}

	avgIterations := 0.0
	if summary.Requests > 0 {
		avgIterations = float64(iterations) / float64(summary.Requests)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"range":          rng.key,
		"summary":        summarize(&summary),
		"today":          summarize(&today),
		"by_source":      map[string]tokenStatSummary{"query": summarize(&byQuery), "proactive": summarize(&byProactive), "task": summarize(&byTask)},
		"by_chat_type":   map[string]tokenStatSummary{"group": summarize(&byGroup), "friend": summarize(&byFriend)},
		"by_status":      statusCount,
		"top_targets":    ranked,
		"hourly":         hourly,
		"daily":          daily,
		"iterations":     iterations,
		"avg_iterations": avgIterations,
	})
}
