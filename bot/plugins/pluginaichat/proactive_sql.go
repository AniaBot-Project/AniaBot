package pluginaichat

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// 主动互动意图的行级存储 schema。
//
// 与长期记忆 / 知识库的取舍一致：意图是「按时间取、按状态更新、按会话统计」
// 的数据，关系表比整段 JSON 更合适——tick 每轮只取到期的一小批，反馈环只
// 更新单行的 outcome，每日上限用 COUNT 统计。时间列沿用 fixed-width UTC
// 文本（字典序即时间序），与 ania_memory / ania_kb_doc 一致。
var intentionTables = []storage.TableDDL{
	{
		Name: "ania_intention",
		SQLite: []string{
			`CREATE TABLE IF NOT EXISTS ania_intention (` +
				`id TEXT NOT NULL, ` +
				`scope TEXT NOT NULL, ` +
				`source TEXT NOT NULL, ` +
				`prompt TEXT NOT NULL, ` +
				`note TEXT NOT NULL DEFAULT '', ` +
				`priority INTEGER NOT NULL DEFAULT 0, ` +
				`due_at TEXT NOT NULL, ` +
				`expires_at TEXT NOT NULL DEFAULT '', ` +
				`status TEXT NOT NULL, ` +
				`created_by TEXT NOT NULL DEFAULT '', ` +
				`created_at TEXT NOT NULL, ` +
				`sent_at TEXT NOT NULL DEFAULT '', ` +
				`outcome TEXT NOT NULL DEFAULT '', ` +
				`outcome_at TEXT NOT NULL DEFAULT '', ` +
				`reason TEXT NOT NULL DEFAULT '', ` +
				`reply TEXT NOT NULL DEFAULT '', ` +
				`attempts INTEGER NOT NULL DEFAULT 0, ` +
				`PRIMARY KEY (id))`,
			`CREATE INDEX IF NOT EXISTS idx_ania_intention_due ON ania_intention(status, due_at)`,
			`CREATE INDEX IF NOT EXISTS idx_ania_intention_scope ON ania_intention(scope, sent_at)`,
		},
		MySQL: []string{
			`CREATE TABLE IF NOT EXISTS ania_intention (` +
				`id VARCHAR(32) COLLATE utf8mb4_bin NOT NULL, ` +
				`scope VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`source VARCHAR(32) COLLATE utf8mb4_bin NOT NULL, ` +
				`prompt MEDIUMTEXT NOT NULL, ` +
				`note TEXT NOT NULL, ` +
				`priority INT NOT NULL DEFAULT 0, ` +
				`due_at VARCHAR(40) COLLATE utf8mb4_bin NOT NULL, ` +
				`expires_at VARCHAR(40) COLLATE utf8mb4_bin NOT NULL, ` +
				`status VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`created_by VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`created_at VARCHAR(40) COLLATE utf8mb4_bin NOT NULL, ` +
				`sent_at VARCHAR(40) COLLATE utf8mb4_bin NOT NULL, ` +
				`outcome VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`outcome_at VARCHAR(40) COLLATE utf8mb4_bin NOT NULL, ` +
				`reason TEXT NOT NULL, ` +
				`reply MEDIUMTEXT NOT NULL, ` +
				`attempts INT NOT NULL DEFAULT 0, ` +
				`PRIMARY KEY (id), ` +
				`KEY idx_ania_intention_due (status, due_at), ` +
				`KEY idx_ania_intention_scope (scope, sent_at)` +
				`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		},
	},
}

// intentionColumns 查询列顺序，与 scanIntention 一一对应。
const intentionColumns = `id, scope, source, prompt, note, priority, due_at, expires_at, ` +
	`status, created_by, created_at, sent_at, outcome, outcome_at, reason, reply, attempts`

// rowScanner 同时适配 *sql.Row 与 *sql.Rows。
type rowScanner interface {
	Scan(dest ...any) error
}

// intentionDBTimeout 单条 SQL 的超时：调度循环与消息热路径（noteMessage 里结算
// replied）都会调到这里，存储卡住时不能无限期阻塞消息处理。
const intentionDBTimeout = 5 * time.Second

// intentionStore 意图行级存储。所有方法失败时只记日志并返回零值，
// 让上层调度按「本轮没有可执行意图」自然降级，绝不阻塞消息主流程。
type intentionStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// dbCtx 返回带超时的数据库上下文。
func (s *intentionStore) dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), intentionDBTimeout)
}

// newIntentionStore 建表并返回存储；建表失败返回 error（由调用方禁用功能）。
func newIntentionStore(db *sql.DB, dialect storage.SQLDialect, logger *slog.Logger) (*intentionStore, error) {
	if err := storage.EnsureTables(context.Background(), db, dialect, intentionTables...); err != nil {
		return nil, err
	}
	return &intentionStore{db: db, logger: logger}, nil
}

func scanIntention(s rowScanner) (ProactiveIntention, error) {
	var it ProactiveIntention
	var dueAt, expiresAt, createdAt, sentAt, outcomeAt string
	err := s.Scan(&it.ID, &it.Scope, &it.Source, &it.Prompt, &it.Note, &it.Priority,
		&dueAt, &expiresAt, &it.Status, &it.CreatedBy, &createdAt, &sentAt,
		&it.Outcome, &outcomeAt, &it.Reason, &it.Reply, &it.Attempts)
	if err != nil {
		return it, err
	}
	it.DueAt = parseRowTime(dueAt)
	it.ExpiresAt = parseRowTime(expiresAt)
	it.CreatedAt = parseRowTime(createdAt)
	it.SentAt = parseRowTime(sentAt)
	it.OutcomeAt = parseRowTime(outcomeAt)
	return it, nil
}

// parseRowTime 解析行内时间列；空串或格式异常返回零值。
func parseRowTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(rowTimeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// formatRowTime 序列化行内时间列；零值写空串（与 SQLite 默认值一致）。
func formatRowTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(rowTimeLayout)
}

// insert 写入一条意图。
func (s *intentionStore) insert(it *ProactiveIntention) bool {
	ctx, cancel := s.dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ania_intention (`+intentionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID, it.Scope, it.Source, it.Prompt, it.Note, it.Priority,
		formatRowTime(it.DueAt), formatRowTime(it.ExpiresAt),
		it.Status, it.CreatedBy, formatRowTime(it.CreatedAt), formatRowTime(it.SentAt),
		it.Outcome, formatRowTime(it.OutcomeAt), it.Reason, it.Reply, it.Attempts)
	if err != nil {
		s.logger.Error("写入主动意图失败", "id", it.ID, "scope", it.Scope, "error", err)
		return false
	}
	return true
}

// update 覆盖一条意图的可变字段（scope / source / created_* 创建后不再变更）。
func (s *intentionStore) update(it *ProactiveIntention) bool {
	ctx, cancel := s.dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`UPDATE ania_intention SET prompt = ?, note = ?, priority = ?, due_at = ?, expires_at = ?, `+
			`status = ?, sent_at = ?, outcome = ?, outcome_at = ?, reason = ?, reply = ?, attempts = ? WHERE id = ?`,
		it.Prompt, it.Note, it.Priority, formatRowTime(it.DueAt), formatRowTime(it.ExpiresAt),
		it.Status, formatRowTime(it.SentAt), it.Outcome, formatRowTime(it.OutcomeAt),
		it.Reason, it.Reply, it.Attempts, it.ID)
	if err != nil {
		s.logger.Error("更新主动意图失败", "id", it.ID, "error", err)
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// MySQL 默认返回「变更行数」而非「匹配行数」（go-sql-driver 未开
		// clientFoundRows），字段值恰好没变时也是 0——不能据此判定记录已不存在
		s.logger.Debug("主动意图更新未产生变更行", "id", it.ID)
	}
	return true
}

// due 取出到期待执行的意图（优先级高的先执行）。
func (s *intentionStore) due(now time.Time, limit int) []ProactiveIntention {
	if limit <= 0 {
		limit = proactiveTickBatch
	}
	ctx, cancel := s.dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+intentionColumns+` FROM ania_intention WHERE status = ? AND due_at <= ? `+
			`ORDER BY priority DESC, due_at ASC LIMIT ?`,
		proactiveStatusPending, formatRowTime(now), limit)
	if err != nil {
		s.logger.Error("读取到期意图失败", "error", err)
		return nil
	}
	return collectIntentions(rows, s.logger, "读取到期意图失败")
}

// pendingByScope 列出某会话待执行的意图（任何来源）。
func (s *intentionStore) pendingByScope(scope string) []ProactiveIntention {
	ctx, cancel := s.dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+intentionColumns+` FROM ania_intention WHERE scope = ? AND status = ? ORDER BY due_at ASC`,
		scope, proactiveStatusPending)
	if err != nil {
		s.logger.Error("读取会话意图失败", "scope", scope, "error", err)
		return nil
	}
	return collectIntentions(rows, s.logger, "读取会话意图失败")
}

// recentByScope 列出某会话最近的意图（新在前），供 /proactive log 使用。
func (s *intentionStore) recentByScope(scope string, limit int) []ProactiveIntention {
	if limit <= 0 {
		limit = 10
	}
	ctx, cancel := s.dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+intentionColumns+` FROM ania_intention WHERE scope = ? ORDER BY created_at DESC LIMIT ?`,
		scope, limit)
	if err != nil {
		s.logger.Error("读取意图历史失败", "scope", scope, "error", err)
		return nil
	}
	return collectIntentions(rows, s.logger, "读取意图历史失败")
}

// recentSpoken 取某会话最近成功发出的主动发言（按发送时间倒序，新在前）。
//
// 供提示词回显「我上次说了什么」：主动互动每次都是全新的一次性会话，不读
// 会话历史，只能靠意图表里已发送的 Reply 保持自我一致。只取 sent 且有内容
// 的记录；走 (scope, sent_at) 索引，且不在调度热路径上。
func (s *intentionStore) recentSpoken(scope string, limit int) []ProactiveIntention {
	if limit <= 0 {
		limit = proactiveRecentUtterances
	}
	ctx, cancel := s.dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+intentionColumns+` FROM ania_intention `+
			`WHERE scope = ? AND status = ? AND reply <> '' `+
			`ORDER BY sent_at DESC LIMIT ?`,
		scope, proactiveStatusSent, limit)
	if err != nil {
		s.logger.Error("读取最近主动发言失败", "scope", scope, "error", err)
		return nil
	}
	return collectIntentions(rows, s.logger, "读取最近主动发言失败")
}

// collectIntentions 收集 rows（先关 rows 再返回，SQLite 单连接下必须如此）。
func collectIntentions(rows *sql.Rows, logger *slog.Logger, errMsg string) []ProactiveIntention {
	var out []ProactiveIntention
	for rows.Next() {
		it, err := scanIntention(rows)
		if err != nil {
			rows.Close()
			logger.Error(errMsg, "error", err)
			return out
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		logger.Error(errMsg, "error", err)
	}
	rows.Close()
	return out
}

// lastSentAt 返回某会话最近一次成功主动发言的时间。
func (s *intentionStore) lastSentAt(scope string) (time.Time, bool) {
	ctx, cancel := s.dbCtx()
	defer cancel()
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT sent_at FROM ania_intention WHERE scope = ? AND status = ? ORDER BY sent_at DESC LIMIT 1`,
		scope, proactiveStatusSent).Scan(&raw)
	if err != nil {
		if err != sql.ErrNoRows {
			s.logger.Error("读取最近主动发言失败", "scope", scope, "error", err)
		}
		return time.Time{}, false
	}
	t := parseRowTime(raw)
	return t, !t.IsZero()
}

// countSentSince 统计某会话（scope 为空表示全局）从 since 起成功发出的条数。
func (s *intentionStore) countSentSince(scope string, since time.Time) int {
	ctx, cancel := s.dbCtx()
	defer cancel()
	var n int
	var err error
	if scope == "" {
		err = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM ania_intention WHERE status = ? AND sent_at >= ?`,
			proactiveStatusSent, formatRowTime(since)).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM ania_intention WHERE scope = ? AND status = ? AND sent_at >= ?`,
			scope, proactiveStatusSent, formatRowTime(since)).Scan(&n)
	}
	if err != nil {
		s.logger.Error("统计主动发言条数失败", "scope", scope, "error", err)
		return 0
	}
	return n
}

// consecutiveIgnored 返回某会话最近 limit 次主动发言中「连续被忽略」的次数
// （从最近一次往前数，遇到 replied 即停止）。
func (s *intentionStore) consecutiveIgnored(scope string, limit int) int {
	if limit <= 0 {
		return 0
	}
	ctx, cancel := s.dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT outcome FROM ania_intention WHERE scope = ? AND status = ? ORDER BY sent_at DESC LIMIT ?`,
		scope, proactiveStatusSent, limit)
	if err != nil {
		s.logger.Error("读取主动发言结果失败", "scope", scope, "error", err)
		return 0
	}
	var outcomes []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			rows.Close()
			return 0
		}
		outcomes = append(outcomes, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Error("读取主动发言结果失败", "scope", scope, "error", err)
		return 0
	}
	rows.Close()

	n := 0
	for _, o := range outcomes {
		if o != proactiveOutcomeIgnored {
			break
		}
		n++
	}
	return n
}

// markReplied 把某会话在观察窗口内发出、尚未结算的发言标记为「有人回应」。
func (s *intentionStore) markReplied(scope string, since time.Time) int {
	ctx, cancel := s.dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`UPDATE ania_intention SET outcome = ?, outcome_at = ? `+
			`WHERE scope = ? AND status = ? AND outcome = '' AND sent_at >= ?`,
		proactiveOutcomeReplied, formatRowTime(time.Now()), scope, proactiveStatusSent, formatRowTime(since))
	if err != nil {
		s.logger.Error("标记主动发言回应失败", "scope", scope, "error", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// markIgnored 把观察窗口已过、仍无人回应的发言标记为「被忽略」，返回条数。
func (s *intentionStore) markIgnored(cutoff time.Time) int {
	ctx, cancel := s.dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`UPDATE ania_intention SET outcome = ?, outcome_at = ? `+
			`WHERE status = ? AND outcome = '' AND sent_at <= ?`,
		proactiveOutcomeIgnored, formatRowTime(time.Now()), proactiveStatusSent, formatRowTime(cutoff))
	if err != nil {
		s.logger.Error("标记主动发言忽略失败", "error", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// expireStale 把超过有效期仍未执行的意图置为 expired，返回条数。
func (s *intentionStore) expireStale(now time.Time) int {
	ctx, cancel := s.dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`UPDATE ania_intention SET status = ?, reason = '超过有效期未发出' `+
			`WHERE status = ? AND expires_at <> '' AND expires_at < ?`,
		proactiveStatusExpired, proactiveStatusPending, formatRowTime(now))
	if err != nil {
		s.logger.Error("作废过期意图失败", "error", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// prune 删除早于 before 的历史意图（待执行的意图不删），返回删除条数。
func (s *intentionStore) prune(before time.Time) int {
	ctx, cancel := s.dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM ania_intention WHERE created_at < ? AND status <> ?`,
		formatRowTime(before), proactiveStatusPending)
	if err != nil {
		s.logger.Error("清理历史意图失败", "error", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// awaitingScopes 返回仍在观察窗口内的会话（sent 且尚未结算）。
func (s *intentionStore) awaitingScopes(since time.Time) []string {
	ctx, cancel := s.dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT scope FROM ania_intention WHERE status = ? AND outcome = '' AND sent_at >= ?`,
		proactiveStatusSent, formatRowTime(since))
	if err != nil {
		s.logger.Error("读取待观察会话失败", "error", err)
		return nil
	}
	var out []string
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			rows.Close()
			return out
		}
		out = append(out, scope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Error("读取待观察会话失败", "error", err)
		return out
	}
	rows.Close()
	return out
}
