package pluginaichat

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// 长任务目标（goal）：一个会话同一时刻最多一个目标，用于跨回合自动续跑。
//
// 表 ania_agent_goal 只存业务状态；「本轮是否已被续跑驱动」是进程内状态
// （goalArmed），不持久化——进程重启后目标保持 active 但**不再自动续跑**，
// 需要用户发送 /goal resume 或 AI 被人类要求继续时重新武装（与 DSH 的
// disarm 语义一致，避免升级重启后自动烧 token）。
const (
	goalPhaseActive   = "active"
	goalPhasePaused   = "paused"
	goalPhaseBlocked  = "blocked"
	goalPhaseComplete = "complete"

	// 目标续跑达到最大轮数时写入的阻塞码
	goalBlockRoundLimit  = "round-limit"
	goalDefaultMaxRounds = 64
	goalMaxRoundsLimit   = 500
)

// agentGoal 一条会话目标记录。
type agentGoal struct {
	SessionKey     string
	Objective      string
	Phase          string
	RoundsStarted  int
	MaxRounds      int
	BlockedCode    string
	BlockedMessage string
	CreatedAt      int64
	UpdatedAt      int64
}

// goalTables ania_agent_goal 的双方言 DDL。
func goalTables() []storage.TableDDL {
	return []storage.TableDDL{
		{
			Name: "ania_agent_goal",
			SQLite: []string{
				`CREATE TABLE IF NOT EXISTS ania_agent_goal (` +
					`session_key TEXT NOT NULL, ` +
					`objective TEXT NOT NULL, ` +
					`phase TEXT NOT NULL, ` +
					`rounds_started INTEGER NOT NULL DEFAULT 0, ` +
					`max_rounds INTEGER NOT NULL DEFAULT 64, ` +
					`blocked_code TEXT NOT NULL DEFAULT '', ` +
					`blocked_message TEXT NOT NULL DEFAULT '', ` +
					`created_at INTEGER NOT NULL, ` +
					`updated_at INTEGER NOT NULL, ` +
					`PRIMARY KEY (session_key))`,
			},
			MySQL: []string{
				`CREATE TABLE IF NOT EXISTS ania_agent_goal (` +
					`session_key VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
					`objective MEDIUMTEXT NOT NULL, ` +
					`phase VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
					`rounds_started INT NOT NULL DEFAULT 0, ` +
					`max_rounds INT NOT NULL DEFAULT 64, ` +
					`blocked_code VARCHAR(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '', ` +
					`blocked_message MEDIUMTEXT NOT NULL, ` +
					`created_at BIGINT NOT NULL, ` +
					`updated_at BIGINT NOT NULL, ` +
					`PRIMARY KEY (session_key)` +
					`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
			},
		},
	}
}

// goalStore 目标表存储层。
type goalStore struct {
	db     *sql.DB
	logger *slog.Logger
}

func newGoalStore(db *sql.DB, logger *slog.Logger) *goalStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &goalStore{db: db, logger: logger}
}

// EnsureTables 建表（幂等）；方言探测失败时返回错误由调用方降级。
func (s *goalStore) EnsureTables(ctx context.Context) error {
	dialect, err := taskDialect(s.db)
	if err != nil {
		return err
	}
	return storage.EnsureTables(ctx, s.db, dialect, goalTables()...)
}

// Get 读取会话目标；不存在返回 (nil,false,nil)。
func (s *goalStore) Get(ctx context.Context, sessionKey string) (*agentGoal, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT session_key, objective, phase, rounds_started, max_rounds, blocked_code, blocked_message, created_at, updated_at `+
			`FROM ania_agent_goal WHERE session_key = ?`, sessionKey)
	g := &agentGoal{}
	if err := row.Scan(&g.SessionKey, &g.Objective, &g.Phase, &g.RoundsStarted, &g.MaxRounds,
		&g.BlockedCode, &g.BlockedMessage, &g.CreatedAt, &g.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	return g, true, nil
}

// Upsert 写入或更新会话目标（created_at 只在首次写入时生效）。
// 用「先 UPDATE 再 INSERT」而不是方言专属 upsert：目标写操作只发生在会话回合
// 串行路径上，不存在同一会话的并发写。
func (s *goalStore) Upsert(ctx context.Context, g *agentGoal) error {
	now := time.Now().Unix()
	if g.CreatedAt == 0 {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	res, err := s.db.ExecContext(ctx,
		`UPDATE ania_agent_goal SET objective = ?, phase = ?, rounds_started = ?, max_rounds = ?, `+
			`blocked_code = ?, blocked_message = ?, updated_at = ? WHERE session_key = ?`,
		g.Objective, g.Phase, g.RoundsStarted, g.MaxRounds, g.BlockedCode, g.BlockedMessage, g.UpdatedAt, g.SessionKey)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO ania_agent_goal (session_key, objective, phase, rounds_started, max_rounds, blocked_code, blocked_message, created_at, updated_at) `+
			`VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.SessionKey, g.Objective, g.Phase, g.RoundsStarted, g.MaxRounds, g.BlockedCode, g.BlockedMessage, g.CreatedAt, g.UpdatedAt)
	return err
}

// Clear 删除会话目标。
func (s *goalStore) Clear(ctx context.Context, sessionKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ania_agent_goal WHERE session_key = ?`, sessionKey)
	return err
}

// ListActive 列出所有 active 目标（面板/诊断用）。
func (s *goalStore) ListActive(ctx context.Context) ([]*agentGoal, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT session_key, objective, phase, rounds_started, max_rounds, blocked_code, blocked_message, created_at, updated_at `+
			`FROM ania_agent_goal WHERE phase = ? ORDER BY updated_at DESC`, goalPhaseActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*agentGoal
	for rows.Next() {
		g := &agentGoal{}
		if err := rows.Scan(&g.SessionKey, &g.Objective, &g.Phase, &g.RoundsStarted, &g.MaxRounds,
			&g.BlockedCode, &g.BlockedMessage, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
