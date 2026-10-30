package tasklog

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// 任务日志的行级存储 schema：每条日志一行，seq 自增主键（计数由 Logger
// 在互斥内分配，启动时 SELECT MAX(seq) 初始化）。payload 为 Entry 完整 JSON；
// 其余列是过滤条件的冗余下推列——SQL 仅用于收窄候选，Filter.match 仍为最终判定。
var taskLogTables = []storage.TableDDL{
	{
		Name: "ania_task_log",
		SQLite: []string{
			`CREATE TABLE IF NOT EXISTS ania_task_log (` +
				`seq INTEGER NOT NULL PRIMARY KEY, ` +
				`task_id TEXT NOT NULL, ` +
				`title TEXT NOT NULL, ` +
				`target_type TEXT NOT NULL, ` +
				`target_id TEXT NOT NULL, ` +
				`status TEXT NOT NULL, ` +
				`created_at INTEGER NOT NULL, ` +
				`payload TEXT NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS idx_ania_task_log_time ON ania_task_log(created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_ania_task_log_task ON ania_task_log(task_id)`,
		},
		MySQL: []string{
			`CREATE TABLE IF NOT EXISTS ania_task_log (` +
				`seq BIGINT NOT NULL, ` +
				`task_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL, ` +
				`title MEDIUMTEXT NOT NULL, ` +
				`target_type VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`target_id VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`status VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`created_at BIGINT NOT NULL, ` +
				`payload MEDIUMTEXT NOT NULL, ` +
				`PRIMARY KEY (seq), ` +
				`KEY idx_ania_task_log_time (created_at), ` +
				`KEY idx_ania_task_log_task (task_id)` +
				`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		},
	},
}

// sqlBackend SQL 版日志存储：过滤条件下推 WHERE、清除按条件删除，
// 避免每次读取全量列键 + 逐键加载的开销。SQLite 单连接下读取
// 遵循"收集→关闭 rows→解析"纪律。
type sqlBackend struct {
	db     *sql.DB
	logger *slog.Logger
}

func newSQLBackend(db *sql.DB, logger *slog.Logger) *sqlBackend {
	return &sqlBackend{db: db, logger: logger}
}

func (b *sqlBackend) maxSeq() uint64 {
	var n uint64
	if err := b.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(MAX(seq), 0) FROM ania_task_log`).Scan(&n); err != nil {
		b.logger.Error("tasklog 读取最大序号失败", "error", err)
		return 0
	}
	return n
}

// logColumns 从 Entry 导出冗余下推列与完整 payload。
func logColumns(e Entry) (taskID, title, targetType, targetID, status string, createdAt int64, payload string, err error) {
	data, err := json.Marshal(e)
	if err != nil {
		return "", "", "", "", "", 0, "", err
	}
	return e.TaskID, e.TaskTitle, e.TargetType, e.TargetID, string(e.Status), e.TriggerTime.Unix(), string(data), nil
}

func (b *sqlBackend) insert(seq uint64, e Entry) {
	taskID, title, targetType, targetID, status, createdAt, payload, err := logColumns(e)
	if err != nil {
		b.logger.Error("tasklog 序列化失败", "id", e.ID, "error", err)
		return
	}
	if _, err := b.db.ExecContext(context.Background(),
		`INSERT INTO ania_task_log (seq, task_id, title, target_type, target_id, status, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, taskID, title, targetType, targetID, status, createdAt, payload); err != nil {
		b.logger.Error("tasklog 落盘失败", "id", e.ID, "error", err)
	}
}

func (b *sqlBackend) load(seq uint64) (Entry, bool) {
	var payload string
	err := b.db.QueryRowContext(context.Background(),
		`SELECT payload FROM ania_task_log WHERE seq = ?`, seq).Scan(&payload)
	if err != nil {
		return Entry{}, false
	}
	var e Entry
	if err := json.Unmarshal([]byte(payload), &e); err != nil {
		b.logger.Error("tasklog 反序列化失败", "seq", seq, "error", err)
		return Entry{}, false
	}
	return e, true
}

func (b *sqlBackend) overwrite(seq uint64, e Entry) {
	taskID, title, targetType, targetID, status, createdAt, payload, err := logColumns(e)
	if err != nil {
		b.logger.Error("tasklog 序列化失败", "id", e.ID, "error", err)
		return
	}
	if _, err := b.db.ExecContext(context.Background(),
		`UPDATE ania_task_log SET task_id = ?, title = ?, target_type = ?, target_id = ?, status = ?, created_at = ?, payload = ? WHERE seq = ?`,
		taskID, title, targetType, targetID, status, createdAt, payload, seq); err != nil {
		b.logger.Error("tasklog 落盘失败", "id", e.ID, "error", err)
	}
}

// clear 清除满足条件的日志。无过滤条件时直接整表删除；有条件时先用
// query 求出精确命中集，再按序号分批删除，保证 SQL 与 Go 侧匹配语义一致。
func (b *sqlBackend) clear(f Filter) int {
	if f == (Filter{}) {
		res, err := b.db.ExecContext(context.Background(), `DELETE FROM ania_task_log`)
		if err != nil {
			b.logger.Error("tasklog 清除失败", "error", err)
			return 0
		}
		n, _ := res.RowsAffected()
		return int(n)
	}
	matched := b.query(f, 0, 0)
	if len(matched) == 0 {
		return 0
	}
	const chunk = 500
	removed := 0
	for start := 0; start < len(matched); start += chunk {
		end := min(start+chunk, len(matched))
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, end-start)
		for _, e := range matched[start:end] {
			seq, err := strconv.ParseUint(e.ID, 36, 64)
			if err != nil || seq == 0 {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, seq)
		}
		if len(args) == 0 {
			continue
		}
		res, err := b.db.ExecContext(context.Background(),
			`DELETE FROM ania_task_log WHERE seq IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			b.logger.Error("tasklog 清除失败", "error", err)
			continue
		}
		n, _ := res.RowsAffected()
		removed += int(n)
	}
	return removed
}

func (b *sqlBackend) markRunningInterrupted(now time.Time) int {
	rows, err := b.db.QueryContext(context.Background(),
		`SELECT seq, payload FROM ania_task_log WHERE status = ?`, string(StatusRunning))
	if err != nil {
		b.logger.Error("tasklog 查询运行中记录失败", "error", err)
		return 0
	}
	type seqPayload struct {
		seq     uint64
		payload string
	}
	var found []seqPayload
	for rows.Next() {
		var r seqPayload
		if err := rows.Scan(&r.seq, &r.payload); err != nil {
			rows.Close()
			b.logger.Error("tasklog 查询运行中记录失败", "error", err)
			return 0
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		b.logger.Error("tasklog 查询运行中记录失败", "error", err)
		return 0
	}
	rows.Close()

	// status 列只是收窄候选，payload 内的状态才是终判：
	// 仅把确实为 running 的记录标记为中断，避免覆盖异常数据
	count := 0
	for _, r := range found {
		var e Entry
		if err := json.Unmarshal([]byte(r.payload), &e); err != nil {
			b.logger.Error("tasklog 反序列化失败，跳过中断标记", "seq", r.seq, "error", err)
			continue
		}
		if e.Status != StatusRunning {
			continue
		}
		interruptEntry(&e, now)
		b.overwrite(r.seq, e)
		count++
	}
	return count
}

func (b *sqlBackend) recent(limit int) []Entry {
	q := `SELECT payload FROM ania_task_log ORDER BY seq DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return b.loadPayloads(q, args, func(Entry) bool { return true }, limit)
}

func (b *sqlBackend) query(f Filter, beforeSeq uint64, limit int) []Entry {
	var where []string
	var args []any
	if f.TargetType != "" {
		where = append(where, `target_type = ?`)
		args = append(args, f.TargetType)
	}
	if f.TargetID != "" {
		where = append(where, `target_id = ?`)
		args = append(args, f.TargetID)
	}
	if f.TaskID != "" {
		where = append(where, `task_id = ?`)
		args = append(args, f.TaskID)
	}
	if f.Status != "" {
		where = append(where, `status = ?`)
		args = append(args, string(f.Status))
	}
	if !f.Start.IsZero() {
		where = append(where, `created_at >= ?`)
		args = append(args, f.Start.Unix())
	}
	if !f.End.IsZero() {
		where = append(where, `created_at <= ?`)
		args = append(args, f.End.Unix())
	}
	if f.Keyword != "" {
		// LOWER() 对齐 MySQL utf8mb4_bin 的大小写敏感与现有不区分大小写语义
		// （均为 ASCII 级折叠；非 ASCII 大小写对的极端差异由 Go 侧 match 终判兜住）
		where = append(where, `LOWER(title) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+escapeLike(f.Keyword)+"%")
	}
	if beforeSeq > 0 {
		where = append(where, `seq < ?`)
		args = append(args, beforeSeq)
	}
	q := `SELECT payload FROM ania_task_log`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY seq DESC`
	// 日志不做容量淘汰，表可能很大；凑满 limit 条匹配即停
	//（limit<=0 时读到底，仅导出等场景使用；SQL 下推只是收窄，match 才是终判）
	return b.loadPayloads(q, args, f.match, limit)
}

// loadPayloads 执行查询并逐行解码，仅保留 match 通过的记录，凑满 limit 条
// （limit<=0 不限）即停止读取。
func (b *sqlBackend) loadPayloads(q string, args []any, match func(Entry) bool, limit int) []Entry {
	rows, err := b.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		b.logger.Error("tasklog 查询失败", "error", err)
		return nil
	}
	var payloads []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			b.logger.Error("tasklog 查询失败", "error", err)
			return nil
		}
		payloads = append(payloads, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		b.logger.Error("tasklog 查询失败", "error", err)
		return nil
	}
	rows.Close()

	out := make([]Entry, 0, len(payloads))
	for _, p := range payloads {
		var e Entry
		if err := json.Unmarshal([]byte(p), &e); err != nil {
			b.logger.Error("tasklog 反序列化失败，跳过该条", "error", err)
			continue
		}
		if !match(e) {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// escapeLike 转义 LIKE 模式中的特殊字符（配合 ESCAPE '\'）。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
