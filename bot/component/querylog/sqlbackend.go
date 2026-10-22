package querylog

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// Query 日志的行级存储 schema：每条日志一行，seq 自增主键（计数由 Logger
// 在互斥内分配，启动时 SELECT MAX(seq) 初始化）。payload 为 Entry 完整 JSON；
// 其余列是过滤条件的冗余下推列——SQL 仅用于收窄候选，Filter.match 仍为最终判定。
var queryLogTables = []storage.TableDDL{
	{
		Name: "ania_query_log",
		SQLite: []string{
			`CREATE TABLE IF NOT EXISTS ania_query_log (` +
				`seq INTEGER NOT NULL PRIMARY KEY, ` +
				`kind TEXT NOT NULL DEFAULT '', ` +
				`chat_type TEXT NOT NULL, ` +
				`target_id TEXT NOT NULL, ` +
				`senders TEXT NOT NULL, ` +
				`status TEXT NOT NULL, ` +
				`query_text TEXT NOT NULL, ` +
				`created_at INTEGER NOT NULL, ` +
				`payload TEXT NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS idx_ania_query_log_time ON ania_query_log(created_at)`,
		},
		MySQL: []string{
			`CREATE TABLE IF NOT EXISTS ania_query_log (` +
				`seq BIGINT NOT NULL, ` +
				`kind VARCHAR(16) COLLATE utf8mb4_bin NOT NULL DEFAULT '', ` +
				`chat_type VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`target_id VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`senders MEDIUMTEXT NOT NULL, ` +
				`status VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`query_text MEDIUMTEXT NOT NULL, ` +
				`created_at BIGINT NOT NULL, ` +
				`payload MEDIUMTEXT NOT NULL, ` +
				`PRIMARY KEY (seq), ` +
				`KEY idx_ania_query_log_time (created_at)` +
				`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		},
	},
}

// sqlBackend SQL 版日志存储：过滤条件下推 WHERE、容量淘汰走范围删除，
// 避免 KV 后端每次读取全量列键 + 逐键加载的开销。SQLite 单连接下读取
// 遵循"收集→关闭 rows→解析"纪律。
type sqlBackend struct {
	db      *sql.DB
	logger  *slog.Logger
	hasKind bool // kind 列是否可用（旧库补列失败时降级，不影响读写其它字段）
}

func newSQLBackend(db *sql.DB, logger *slog.Logger, hasKind bool) *sqlBackend {
	return &sqlBackend{db: db, logger: logger, hasKind: hasKind}
}

// ensureKindColumn 为升级前的旧库补充 kind 列：CREATE TABLE IF NOT EXISTS 不会
// 修改已存在的表结构，缺少该列时按需执行一次 ALTER。返回该列是否可用——
// 补列失败时后端跳过该列的下推与写入（payload 里仍保留 kind，Go 侧过滤照常
// 生效，只是退化为全表扫描）。
func ensureKindColumn(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, logger *slog.Logger) bool {
	var probe string
	// SELECT 在列不存在时报错，与表内是否有数据无关
	err := db.QueryRowContext(ctx, `SELECT kind FROM ania_query_log LIMIT 1`).Scan(&probe)
	if err == nil || err == sql.ErrNoRows {
		return true
	}
	stmt := `ALTER TABLE ania_query_log ADD COLUMN kind TEXT NOT NULL DEFAULT ''`
	if dialect == storage.SQLDialectMySQL {
		stmt = `ALTER TABLE ania_query_log ADD COLUMN kind VARCHAR(16) COLLATE utf8mb4_bin NOT NULL DEFAULT ''`
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		logger.Error("querylog 补充 kind 列失败，该列不参与下推与写入", "error", err)
		return false
	}
	return true
}

func (b *sqlBackend) maxSeq() uint64 {
	var n uint64
	if err := b.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(MAX(seq), 0) FROM ania_query_log`).Scan(&n); err != nil {
		b.logger.Error("querylog 读取最大序号失败", "error", err)
		return 0
	}
	return n
}

// logColumns 从 Entry 导出冗余下推列与完整 payload。senders 存包裹式 CSV
// （",a,b,"），精确匹配某发言者用 LIKE '%,id,%'，不会误中子串。
func logColumns(e Entry) (kind, chatType, targetID, senders, status, queryText string, createdAt int64, payload string, err error) {
	data, err := json.Marshal(e)
	if err != nil {
		return "", "", "", "", "", "", 0, "", err
	}
	senders = ","
	for _, s := range e.Senders {
		senders += s + ","
	}
	return e.Kind, e.ChatType, e.TargetID, senders, string(e.Status), e.Query, e.Time.Unix(), string(data), nil
}

func (b *sqlBackend) insert(seq uint64, e Entry) {
	kind, chatType, targetID, senders, status, queryText, createdAt, payload, err := logColumns(e)
	if err != nil {
		b.logger.Error("querylog 序列化失败", "id", e.ID, "error", err)
		return
	}
	var execErr error
	if b.hasKind {
		_, execErr = b.db.ExecContext(context.Background(),
			`INSERT INTO ania_query_log (seq, kind, chat_type, target_id, senders, status, query_text, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			seq, kind, chatType, targetID, senders, status, queryText, createdAt, payload)
	} else {
		_, execErr = b.db.ExecContext(context.Background(),
			`INSERT INTO ania_query_log (seq, chat_type, target_id, senders, status, query_text, created_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			seq, chatType, targetID, senders, status, queryText, createdAt, payload)
	}
	if execErr != nil {
		b.logger.Error("querylog 落盘失败", "id", e.ID, "error", execErr)
	}
}

func (b *sqlBackend) load(seq uint64) (Entry, bool) {
	var payload string
	err := b.db.QueryRowContext(context.Background(),
		`SELECT payload FROM ania_query_log WHERE seq = ?`, seq).Scan(&payload)
	if err != nil {
		return Entry{}, false
	}
	var e Entry
	if err := json.Unmarshal([]byte(payload), &e); err != nil {
		b.logger.Error("querylog 反序列化失败", "seq", seq, "error", err)
		return Entry{}, false
	}
	return e, true
}

func (b *sqlBackend) overwrite(seq uint64, e Entry) {
	kind, chatType, targetID, senders, status, queryText, createdAt, payload, err := logColumns(e)
	if err != nil {
		b.logger.Error("querylog 序列化失败", "id", e.ID, "error", err)
		return
	}
	var execErr error
	if b.hasKind {
		_, execErr = b.db.ExecContext(context.Background(),
			`UPDATE ania_query_log SET kind = ?, chat_type = ?, target_id = ?, senders = ?, status = ?, query_text = ?, created_at = ?, payload = ? WHERE seq = ?`,
			kind, chatType, targetID, senders, status, queryText, createdAt, payload, seq)
	} else {
		_, execErr = b.db.ExecContext(context.Background(),
			`UPDATE ania_query_log SET chat_type = ?, target_id = ?, senders = ?, status = ?, query_text = ?, created_at = ?, payload = ? WHERE seq = ?`,
			chatType, targetID, senders, status, queryText, createdAt, payload, seq)
	}
	if execErr != nil {
		b.logger.Error("querylog 落盘失败", "id", e.ID, "error", execErr)
	}
}

func (b *sqlBackend) evict(maxSeq uint64, maxEntries int) {
	if maxEntries <= 0 || maxSeq <= uint64(maxEntries) {
		return
	}
	// 序号单调递增不复用：淘汰线以下的一定是最旧的记录
	if _, err := b.db.ExecContext(context.Background(),
		`DELETE FROM ania_query_log WHERE seq <= ?`, maxSeq-uint64(maxEntries)); err != nil {
		b.logger.Error("querylog 淘汰失败", "error", err)
	}
}

func (b *sqlBackend) markRunningInterrupted(now time.Time) int {
	rows, err := b.db.QueryContext(context.Background(),
		`SELECT seq, payload FROM ania_query_log WHERE status = ?`, string(StatusRunning))
	if err != nil {
		b.logger.Error("querylog 查询运行中记录失败", "error", err)
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
			b.logger.Error("querylog 查询运行中记录失败", "error", err)
			return 0
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		b.logger.Error("querylog 查询运行中记录失败", "error", err)
		return 0
	}
	rows.Close()

	// status 列只是收窄候选，payload 内的状态才是终判：
	// 仅把确实为 running 的记录标记为中断，避免覆盖异常数据
	count := 0
	for _, r := range found {
		var e Entry
		if err := json.Unmarshal([]byte(r.payload), &e); err != nil {
			b.logger.Error("querylog 反序列化失败，跳过中断标记", "seq", r.seq, "error", err)
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
	q := `SELECT payload FROM ania_query_log ORDER BY seq DESC`
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
	if f.Kind != "" && b.hasKind {
		// 普通记录不写 kind（留空），历史记录同样是空串；主动互动等写具体值
		if f.Kind == KindChat {
			// 普通记录不写 kind（留空），历史记录同样是空串；NULL 也归为普通对话
			where = append(where, `(kind = '' OR kind IS NULL)`)
		} else {
			where = append(where, `kind = ?`)
			args = append(args, f.Kind)
		}
	}
	if f.ChatType != "" {
		where = append(where, `chat_type = ?`)
		args = append(args, f.ChatType)
	}
	if f.TargetID != "" {
		where = append(where, `target_id = ?`)
		args = append(args, f.TargetID)
	}
	if f.Sender != "" {
		where = append(where, `senders LIKE ? ESCAPE '\'`)
		args = append(args, "%,"+escapeLike(f.Sender)+",%")
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
		where = append(where, `LOWER(query_text) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+escapeLike(f.Keyword)+"%")
	}
	if beforeSeq > 0 {
		where = append(where, `seq < ?`)
		args = append(args, beforeSeq)
	}
	q := `SELECT payload FROM ania_query_log`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY seq DESC`
	// 表总量受容量淘汰约束（默认 200 条），不加 LIMIT 全扫有界；
	// 凑满 limit 条匹配即停（SQL 下推只是收窄，match 才是终判）
	return b.loadPayloads(q, args, f.match, limit)
}

// loadPayloads 执行查询并逐行解码，仅保留 match 通过的记录，凑满 limit 条
// （limit<=0 不限）即停止读取。
func (b *sqlBackend) loadPayloads(q string, args []any, match func(Entry) bool, limit int) []Entry {
	rows, err := b.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		b.logger.Error("querylog 查询失败", "error", err)
		return nil
	}
	var payloads []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			b.logger.Error("querylog 查询失败", "error", err)
			return nil
		}
		payloads = append(payloads, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		b.logger.Error("querylog 查询失败", "error", err)
		return nil
	}
	rows.Close()

	out := make([]Entry, 0, len(payloads))
	for _, p := range payloads {
		var e Entry
		if err := json.Unmarshal([]byte(p), &e); err != nil {
			b.logger.Error("querylog 反序列化失败，跳过该条", "error", err)
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
