package pluginaichat

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// rowTimeLayout 行级存储时间列的落盘格式（长期记忆、知识库共用）：
// 固定 9 位小数的 UTC 时间，字典序即时间序（time.RFC3339Nano 会裁剪
// 末尾零导致文本序不稳定，不可用）。
const rowTimeLayout = "2006-01-02T15:04:05.000000000Z"

// 长期记忆的行级存储 schema：每条记忆一行，(scope, id) 联合主键。
// tags/emb 以 JSON 存列（检索打分在 Go 侧完成，无需 SQL 下推）。
var memoryTables = []storage.TableDDL{
	{
		Name: "ania_memory",
		SQLite: []string{
			`CREATE TABLE IF NOT EXISTS ania_memory (` +
				`scope TEXT NOT NULL, ` +
				`id TEXT NOT NULL, ` +
				`user_id TEXT NOT NULL DEFAULT '', ` +
				`content TEXT NOT NULL, ` +
				`tags TEXT, ` +
				`emb TEXT, ` +
				`created_at TEXT NOT NULL, ` +
				`PRIMARY KEY (scope, id))`,
		},
		MySQL: []string{
			`CREATE TABLE IF NOT EXISTS ania_memory (` +
				`scope VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`id VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`user_id VARCHAR(255) COLLATE utf8mb4_bin NOT NULL DEFAULT '', ` +
				`content MEDIUMTEXT NOT NULL, ` +
				`tags MEDIUMTEXT, ` +
				`emb MEDIUMTEXT, ` +
				`created_at VARCHAR(40) COLLATE utf8mb4_bin NOT NULL, ` +
				`PRIMARY KEY (scope, id)` +
				`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		},
	},
}

// sqlMemoryStore 基于关系表的记忆行级存储。SQLite 单连接下读取遵循
// "收集→关闭 rows→解析"纪律。错误内部记录日志后以 false/nil 返回。
type sqlMemoryStore struct {
	db     *sql.DB
	logger *slog.Logger
}

func newSQLMemoryStore(db *sql.DB, logger *slog.Logger) *sqlMemoryStore {
	return &sqlMemoryStore{db: db, logger: logger}
}

func (s *sqlMemoryStore) list(scope string) []memoryEntry {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, user_id, content, tags, emb, created_at FROM ania_memory WHERE scope = ? ORDER BY created_at ASC, id ASC`, scope)
	if err != nil {
		s.logger.Error("读取记忆失败", "scope", scope, "error", err)
		return nil
	}
	type rawRow struct {
		id, userID, content string
		tags, emb           sql.NullString
		createdAt           string
	}
	var raws []rawRow
	for rows.Next() {
		var r rawRow
		if err := rows.Scan(&r.id, &r.userID, &r.content, &r.tags, &r.emb, &r.createdAt); err != nil {
			rows.Close()
			s.logger.Error("读取记忆失败", "scope", scope, "error", err)
			return nil
		}
		raws = append(raws, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Error("读取记忆失败", "scope", scope, "error", err)
		return nil
	}
	rows.Close()

	entries := make([]memoryEntry, 0, len(raws))
	for _, r := range raws {
		e := memoryEntry{ID: r.id, UserID: r.userID, Content: r.content}
		if r.tags.Valid {
			if err := json.Unmarshal([]byte(r.tags.String), &e.Tags); err != nil {
				s.logger.Error("反序列化记忆标签失败，忽略标签", "scope", scope, "id", r.id, "error", err)
			}
		}
		if r.emb.Valid {
			if err := json.Unmarshal([]byte(r.emb.String), &e.Emb); err != nil {
				s.logger.Error("反序列化记忆向量失败，忽略向量", "scope", scope, "id", r.id, "error", err)
			}
		}
		if t, err := time.Parse(rowTimeLayout, r.createdAt); err == nil {
			e.CreatedAt = t
		}
		entries = append(entries, e)
	}
	return entries
}

func (s *sqlMemoryStore) insert(scope string, e memoryEntry) bool {
	tags, emb := marshalMemoryJSON(e)
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO ania_memory (scope, id, user_id, content, tags, emb, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		scope, e.ID, e.UserID, e.Content, tags, emb, e.CreatedAt.UTC().Format(rowTimeLayout))
	if err != nil {
		s.logger.Error("写入记忆失败", "scope", scope, "id", e.ID, "error", err)
		return false
	}
	return true
}

func (s *sqlMemoryStore) update(scope string, e memoryEntry) bool {
	tags, emb := marshalMemoryJSON(e)
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE ania_memory SET user_id = ?, content = ?, tags = ?, emb = ? WHERE scope = ? AND id = ?`,
		e.UserID, e.Content, tags, emb, scope, e.ID)
	if err != nil {
		s.logger.Error("更新记忆失败", "scope", scope, "id", e.ID, "error", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (s *sqlMemoryStore) remove(scope, id string) bool {
	res, err := s.db.ExecContext(context.Background(),
		`DELETE FROM ania_memory WHERE scope = ? AND id = ?`, scope, id)
	if err != nil {
		s.logger.Error("删除记忆失败", "scope", scope, "id", id, "error", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (s *sqlMemoryStore) scopes() []string {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT DISTINCT scope FROM ania_memory ORDER BY scope ASC`)
	if err != nil {
		s.logger.Error("列出记忆 scope 失败", "error", err)
		return nil
	}
	var scopes []string
	for rows.Next() {
		var sc string
		if err := rows.Scan(&sc); err != nil {
			rows.Close()
			s.logger.Error("列出记忆 scope 失败", "error", err)
			return nil
		}
		scopes = append(scopes, sc)
	}
	rows.Close()
	return scopes
}

// marshalMemoryJSON 序列化 tags/emb 列；空值写 NULL，与 memoryEntry 的
// omitempty JSON 语义对齐。
func marshalMemoryJSON(e memoryEntry) (tags, emb any) {
	if len(e.Tags) > 0 {
		if data, err := json.Marshal(e.Tags); err == nil {
			tags = string(data)
		}
	}
	if len(e.Emb) > 0 {
		if data, err := json.Marshal(e.Emb); err == nil {
			emb = string(data)
		}
	}
	return tags, emb
}
