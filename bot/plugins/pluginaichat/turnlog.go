package pluginaichat

import (
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// recentTurnLimit 每个会话在内存中保留的最近回合数（供 /tasks 展示）。
const recentTurnLimit = 5

// recentTurn 一条已结束/已中止的回合摘要。普通回合不再写入任务表（避免与
// Query 日志重复），但 /tasks 仍要能看到最近回合，因此只在进程内保留最近
// 几条；持久审计与完整明细以 Query 日志为准。
type recentTurn struct {
	Title      string
	Status     string
	StopReason string
	StartedAt  int64 // unix 秒
	FinishedAt int64 // unix 秒
}

// recentTurnLog 按会话保存最近回合摘要（进程内，重启即清空）。
type recentTurnLog struct {
	mu        sync.Mutex
	bySession map[string][]recentTurn
}

func newRecentTurnLog() *recentTurnLog {
	return &recentTurnLog{bySession: make(map[string][]recentTurn)}
}

// add 记录一条回合摘要（新在前，超出上限丢弃最旧的）。
func (l *recentTurnLog) add(key string, turn recentTurn) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bySession == nil {
		l.bySession = make(map[string][]recentTurn)
	}
	turns := append([]recentTurn{turn}, l.bySession[key]...)
	if len(turns) > recentTurnLimit {
		turns = turns[:recentTurnLimit]
	}
	l.bySession[key] = turns
}

// list 返回会话的最近回合（新在前）的副本。
func (l *recentTurnLog) list(key string) []recentTurn {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	turns := l.bySession[key]
	return append([]recentTurn(nil), turns...)
}

// remove 会话淘汰时释放对应摘要。
func (l *recentTurnLog) remove(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.bySession, key)
}

// rememberTurn 把一次回合的终态记入内存摘要（供 /tasks）。
func (p *AIChatPlugin) rememberTurn(id message.QID, isGroup bool, title, status, stopReason string, start time.Time) {
	if p.recentTurns == nil {
		return
	}
	p.recentTurns.add(sessionKey(id, isGroup), recentTurn{
		Title:      title,
		Status:     status,
		StopReason: stopReason,
		StartedAt:  start.Unix(),
		FinishedAt: time.Now().Unix(),
	})
}
