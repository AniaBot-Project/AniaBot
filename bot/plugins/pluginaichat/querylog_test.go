package pluginaichat

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestQueryLogLiveToolCalls 回归：工具调用完成后应立即增量落盘，
// 面板在 Query 执行中（running）即可看到工具明细与进度，无需等 finishQuery。
func TestQueryLogLiveToolCalls(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	p.cfg.QueryLog.Enable = true
	p.initQueryLogger()
	if p.queryLogger == nil {
		t.Fatal("queryLogger 应已初始化")
	}

	chat, err := aichat.NewChatBot("http://127.0.0.1", "key", "model", "prompt", 1000, nil, nil)
	if err != nil {
		t.Fatalf("创建 ChatBot 失败: %v", err)
	}

	r := p.beginQuery(chat, message.FromString("10001"), true, nil, "查一下天气")
	if r == nil {
		t.Fatal("beginQuery 应返回 recorder")
	}

	// 模拟两次工具调用完成（orchestrator 观察者回调）
	p.onToolCall(r, aichat.ToolCallInfo{Name: "web_search", Arguments: `{"q":"天气"}`, Result: "晴", DurationMs: 120})
	p.onToolCall(r, aichat.ToolCallInfo{Name: "bash", Arguments: `{"cmd":"date"}`, DurationMs: 30, Err: errors.New("exit 1")})

	// 未 finishQuery（仍 running）时读取：工具明细/总数/错误应已落盘可见
	entries := p.queryLogger.Query(querylog.Filter{Limit: 1})
	if len(entries) != 1 {
		t.Fatalf("应有 1 条日志, got %d", len(entries))
	}
	e := entries[0]
	if e.Status != querylog.StatusRunning {
		t.Fatalf("状态应为 running, got %s", e.Status)
	}
	if len(e.ToolCalls) != 2 || e.ToolCallsTotal != 2 {
		t.Fatalf("执行中工具调用应已落盘: len=%d total=%d", len(e.ToolCalls), e.ToolCallsTotal)
	}
	if e.ToolCalls[1].Error == "" {
		t.Fatal("第二次调用的错误应已落盘")
	}

	// finishQuery 后终态覆盖：状态/轮数/token/回复回填，明细保留
	p.finishQuery(r, chat, aichat.TokenUsage{Iterations: 3, TotalTokens: 100}, "今天晴", nil)
	e = p.queryLogger.Query(querylog.Filter{Limit: 1})[0]
	if e.Status != querylog.StatusSuccess {
		t.Fatalf("状态应为 success, got %s", e.Status)
	}
	if len(e.ToolCalls) != 2 {
		t.Fatalf("完成后明细应保留: len=%d", len(e.ToolCalls))
	}
	if e.Iterations != 3 || e.TotalTokens != 100 || e.Reply == "" {
		t.Fatalf("终态字段未回填: iterations=%d tokens=%d reply=%q", e.Iterations, e.TotalTokens, e.Reply)
	}
}

// TestProactiveQueryLog 主动互动执行写入 Query 日志：Kind 标记、触发说明、
// 保持安静状态与原因、token 与工具调用均可回填，且能与普通对话区分筛选。
func TestProactiveQueryLog(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	p.cfg.QueryLog.Enable = true
	p.initQueryLogger()

	chat, err := aichat.NewChatBot("http://127.0.0.1", "key", "model", "prompt", 1000, nil, nil)
	if err != nil {
		t.Fatalf("创建 ChatBot 失败: %v", err)
	}

	r := p.beginProactiveQuery(chat, message.FromString("12345"), true,
		"【主动互动 · 冷场触发】意图 abcd1234\n会话已安静 25 分钟")
	if r == nil {
		t.Fatal("beginProactiveQuery 应返回 recorder")
	}
	p.onToolCall(r, aichat.ToolCallInfo{Name: "memory_search", Arguments: `{"query":"面试"}`, Result: "无相关记忆", DurationMs: 8})
	p.finishProactiveQuery(r, aichat.TokenUsage{Iterations: 2, PromptTokens: 900, CompletionTokens: 40, TotalTokens: 940, CachedTokens: 300},
		querylog.StatusSkipped, "", "AI 判断此刻无需发言：没有具体由头", nil)

	e := p.queryLogger.Query(querylog.Filter{Kind: querylog.KindProactive, Limit: 1})
	if len(e) != 1 {
		t.Fatalf("主动互动日志应可按 kind 筛选, got %d", len(e))
	}
	log := e[0]
	if log.Kind != querylog.KindProactive {
		t.Fatalf("Kind 未标记: %q", log.Kind)
	}
	if log.Status != querylog.StatusSkipped {
		t.Fatalf("保持安静状态应为 skipped, got %s", log.Status)
	}
	if log.Note == "" || log.Reply != "" {
		t.Fatalf("skip 日志应记录原因而非回复: note=%q reply=%q", log.Note, log.Reply)
	}
	if log.TotalTokens != 940 || log.Iterations != 2 {
		t.Fatalf("token/轮数未回填: tokens=%d iterations=%d", log.TotalTokens, log.Iterations)
	}
	if len(log.ToolCalls) != 1 || log.ToolCalls[0].Name != "memory_search" {
		t.Fatalf("工具调用未记录: %+v", log.ToolCalls)
	}
	if !strings.Contains(log.Query, "主动互动") {
		t.Fatalf("Query 应记录触发说明: %q", log.Query)
	}
	// 普通对话筛选不应命中主动互动记录
	if got := p.queryLogger.Query(querylog.Filter{Kind: querylog.KindChat}); len(got) != 0 {
		t.Fatalf("主动互动记录不应命中普通对话筛选, got %d", len(got))
	}
}
