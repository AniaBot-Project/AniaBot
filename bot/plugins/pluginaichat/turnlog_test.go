package pluginaichat

import (
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestRecentTurnLogCapAndOrder 每个会话只保留最近 recentTurnLimit 条，新在前。
func TestRecentTurnLogCapAndOrder(t *testing.T) {
	log := newRecentTurnLog()
	for i := 1; i <= recentTurnLimit+2; i++ {
		log.add("g:1", recentTurn{Title: string(rune('A' + i - 1)), Status: taskStatusCompleted})
	}
	got := log.list("g:1")
	if len(got) != recentTurnLimit {
		t.Fatalf("保留条数 = %d, want %d", len(got), recentTurnLimit)
	}
	if got[0].Title != "G" || got[len(got)-1].Title != "C" {
		t.Fatalf("顺序/裁剪不对: %+v", got)
	}
	// 返回副本：修改结果不影响内部状态
	got[0].Title = "changed"
	if again := log.list("g:1"); again[0].Title != "G" {
		t.Fatalf("list 应返回副本，got %q", again[0].Title)
	}
}

// TestRecentTurnLogSessionIsolationAndRemove 会话之间隔离，会话淘汰后可清理。
func TestRecentTurnLogSessionIsolationAndRemove(t *testing.T) {
	log := newRecentTurnLog()
	log.add("g:1", recentTurn{Title: "群回合"})
	log.add("f:2", recentTurn{Title: "私聊回合"})

	if got := log.list("g:1"); len(got) != 1 || got[0].Title != "群回合" {
		t.Fatalf("g:1 = %+v", got)
	}
	log.remove("g:1")
	if got := log.list("g:1"); len(got) != 0 {
		t.Fatalf("remove 后 g:1 = %+v", got)
	}
	if got := log.list("f:2"); len(got) != 1 {
		t.Fatalf("remove 不应影响其它会话: %+v", got)
	}
}

// TestRememberTurnStoresTerminalState rememberTurn 记录终态、耗时与标题。
func TestRememberTurnStoresTerminalState(t *testing.T) {
	p := &AIChatPlugin{recentTurns: newRecentTurnLog()}
	start := time.Now().Add(-3 * time.Second)
	p.rememberTurn(message.FromUint64(7), false, "清理构建缓存", taskStatusFailed, taskStopError, start)

	got := p.recentTurns.list("f:qq:7")
	if len(got) != 1 {
		t.Fatalf("最近回合 = %+v", got)
	}
	turn := got[0]
	if turn.Title != "清理构建缓存" || turn.Status != taskStatusFailed || turn.StopReason != taskStopError {
		t.Fatalf("字段不对: %+v", turn)
	}
	if turn.StartedAt != start.Unix() || turn.FinishedAt < turn.StartedAt {
		t.Fatalf("时间戳不对: %+v", turn)
	}
}
