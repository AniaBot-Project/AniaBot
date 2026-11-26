package pluginaichat

import (
	"context"
	"testing"
	"time"
)

// TestAgentTasksExcludesLegacyTurnRows 面板后台任务数据源不再返回历史 turn 行：
// 普通回合的完整过程在 Query 日志里，这类旧记录不应出现在面板（也不占 limit）。
func TestAgentTasksExcludesLegacyTurnRows(t *testing.T) {
	store, db := newTaskTestStore(t)
	p := &AIChatPlugin{taskStore: store, historyDB: db}
	ctx := context.Background()
	base := time.Now().Unix()

	turn := taskFixture("g:1", base)
	turn.Kind = taskKindTurn
	turn.Title = "历史回合"
	if err := store.Create(ctx, turn); err != nil {
		t.Fatalf("Create turn: %v", err)
	}
	sub := taskFixture("g:1", base+1)
	sub.Kind = taskKindSubagent
	sub.Title = "后台子代理"
	if err := store.Create(ctx, sub); err != nil {
		t.Fatalf("Create subagent: %v", err)
	}

	got := p.AgentTasks(10)
	if len(got) != 1 {
		t.Fatalf("面板任务数 = %d, want 1（turn 行应被排除）: %+v", len(got), got)
	}
	if got[0].Kind != taskKindSubagent || got[0].Title != "后台子代理" {
		t.Fatalf("返回了错误的任务: %+v", got[0])
	}

	// limit 作用在过滤后的结果上：只有 1 条真实后台任务
	if got := p.AgentTasks(1); len(got) != 1 || got[0].Kind != taskKindSubagent {
		t.Fatalf("limit=1 结果异常: %+v", got)
	}
}
