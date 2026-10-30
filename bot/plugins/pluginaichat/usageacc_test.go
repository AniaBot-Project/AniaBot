package pluginaichat

import (
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/common/model/message"
)

func TestUsageAccAddTake(t *testing.T) {
	acc := &usageAcc{}
	acc.add(aichat.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CachedTokens: 3, Iterations: 2, LastPromptTokens: 999})
	acc.add(aichat.TokenUsage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5, Iterations: 1})

	u := acc.take()
	if u.PromptTokens != 14 || u.CompletionTokens != 6 || u.TotalTokens != 20 || u.CachedTokens != 3 || u.Iterations != 3 {
		t.Fatalf("累计值不符: %+v", u)
	}
	// LastPromptTokens 表示「当前上下文真实大小」，派生调用不累加
	if u.LastPromptTokens != 0 {
		t.Fatalf("LastPromptTokens 不应累加, got %d", u.LastPromptTokens)
	}
	// take 后清零
	if u2 := acc.take(); u2.TotalTokens != 0 || u2.Iterations != 0 {
		t.Fatalf("take 后应清零, got %+v", u2)
	}
}

func TestMergeTokenUsage(t *testing.T) {
	dst := aichat.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CachedTokens: 2, Iterations: 1, LastPromptTokens: 100}
	out := mergeTokenUsage(dst, aichat.TokenUsage{PromptTokens: 20, CompletionTokens: 8, TotalTokens: 28, CachedTokens: 4, Iterations: 3, LastPromptTokens: 999})
	if out.PromptTokens != 30 || out.CompletionTokens != 13 || out.TotalTokens != 43 || out.CachedTokens != 6 || out.Iterations != 4 {
		t.Fatalf("合并结果不符: %+v", out)
	}
	// LastPromptTokens 保持 dst 原值，不被派生调用污染
	if out.LastPromptTokens != 100 {
		t.Fatalf("LastPromptTokens 不应被合并, got %d", out.LastPromptTokens)
	}
}

func TestExtraUsageGatedByQueryLogger(t *testing.T) {
	// Query 日志不可用（记录器未初始化）：不暂存（无人消费，避免累计器无限增长）
	p := &AIChatPlugin{}
	p.addExtraUsage("g:1", aichat.TokenUsage{PromptTokens: 10, TotalTokens: 10})
	if u := p.takeExtraUsage("g:1"); u.TotalTokens != 0 {
		t.Fatalf("日志不可用时应不暂存, got %+v", u)
	}
}

func TestExtraUsageRoundTrip(t *testing.T) {
	p := &AIChatPlugin{queryLogger: querylog.New(newSQLPFake(), testLogger())}
	p.addExtraUsage("g:1", aichat.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
	p.addExtraUsage("g:1", aichat.TokenUsage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4})
	// 零值不入账
	p.addExtraUsage("g:1", aichat.TokenUsage{})

	u := p.takeExtraUsage("g:1")
	if u.PromptTokens != 13 || u.CompletionTokens != 3 || u.TotalTokens != 16 {
		t.Fatalf("暂存累计不符: %+v", u)
	}
	// 取走后清零
	if u2 := p.takeExtraUsage("g:1"); u2.TotalTokens != 0 {
		t.Fatalf("取走后应为零值, got %+v", u2)
	}
	// 会话隔离
	p.addExtraUsage("g:2", aichat.TokenUsage{TotalTokens: 7})
	if u3 := p.takeExtraUsage("g:1"); u3.TotalTokens != 0 {
		t.Fatalf("其他会话的用量不应串入, got %+v", u3)
	}
}

// TestFinishQueryMergesExtraUsage 派生用量（子代理/团队成员/图片识别）并入
// Query 日志条目：统计口径为「主请求 + 全部派生调用」的完整成本。
func TestFinishQueryMergesExtraUsage(t *testing.T) {
	p := &AIChatPlugin{queryLogger: querylog.New(newSQLPFake(), testLogger())}
	chat, err := aichat.NewChatBot("http://127.0.0.1:1", "k", "m", "prompt", 0, nil, nil)
	if err != nil {
		t.Fatalf("创建 ChatBot 失败: %v", err)
	}

	id := message.FromUint64(12345)
	batch := []message.Message{{Sender: message.MessageSender{UserId: message.FromUint64(1), Nickname: "用户"}}}
	recorder := p.beginQuery(chat, id, true, batch, "你好")
	if recorder == nil {
		t.Fatal("beginQuery 应返回 recorder")
	}

	// 模拟主请求期间派生的消耗（team_run 成员同步完成）
	p.addExtraUsage(sessionKey(id, true), aichat.TokenUsage{
		PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140, CachedTokens: 10, Iterations: 3,
	})

	main := aichat.TokenUsage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70, CachedTokens: 5, Iterations: 2}
	p.finishQuery(recorder, chat, main, "回复", nil)

	entries := p.queryLogger.Query(querylog.Filter{Limit: 1})
	if len(entries) != 1 {
		t.Fatalf("应有 1 条日志, got %d", len(entries))
	}
	e := entries[0]
	if e.PromptTokens != 150 || e.CompletionTokens != 60 || e.TotalTokens != 210 || e.CachedTokens != 15 || e.Iterations != 5 {
		t.Fatalf("日志条目应为主请求 + 派生用量之和: %+v", e)
	}
	// 暂存已被取走，不会重复计入下一条
	if u := p.takeExtraUsage(sessionKey(id, true)); u.TotalTokens != 0 {
		t.Fatalf("finishQuery 后暂存应已清空, got %+v", u)
	}
}

// TestFinishQueryNilRecorderNoPanic 日志记录器不可用时 finishQuery 直接跳过。
func TestFinishQueryNilRecorderNoPanic(t *testing.T) {
	p := &AIChatPlugin{}
	chat, err := aichat.NewChatBot("http://127.0.0.1:1", "k", "m", "prompt", 0, nil, nil)
	if err != nil {
		t.Fatalf("创建 ChatBot 失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.finishQuery(nil, chat, aichat.TokenUsage{}, "", nil)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finishQuery(nil) 阻塞")
	}
}
