package pluginaichat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// 知识库的行级存储 schema：每篇文档一行，(scope, id) 联合主键。
// tags/emb 以 JSON 存列（切片与检索打分在 Go 侧完成，无需 SQL 下推）。
// 旧版为每个 scope 一个 JSON 数组整段读写，单值体积随文档数增长（开启向量
// 检索后每篇文档约 200KB，MySQL 单值 16MB 上限会被撑爆）；行级化后增删改
// 只写单行，读取量也只与被读文档成正比。
var kbTables = []storage.TableDDL{
	{
		Name: "ania_kb_doc",
		SQLite: []string{
			`CREATE TABLE IF NOT EXISTS ania_kb_doc (` +
				`scope TEXT NOT NULL, ` +
				`id TEXT NOT NULL, ` +
				`title TEXT NOT NULL, ` +
				`content TEXT NOT NULL, ` +
				`tags TEXT, ` +
				`emb TEXT, ` +
				`source TEXT NOT NULL, ` +
				`created_at TEXT NOT NULL, ` +
				`PRIMARY KEY (scope, id))`,
		},
		MySQL: []string{
			// title/source 非键，标题与 URL 均可能超长，用 MEDIUMTEXT 存储，
			// 避免 VARCHAR(255) 在严格模式下截断报错
			`CREATE TABLE IF NOT EXISTS ania_kb_doc (` +
				`scope VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`id VARCHAR(16) COLLATE utf8mb4_bin NOT NULL, ` +
				`title MEDIUMTEXT NOT NULL, ` +
				`content MEDIUMTEXT NOT NULL, ` +
				`tags MEDIUMTEXT, ` +
				`emb MEDIUMTEXT, ` +
				`source MEDIUMTEXT NOT NULL, ` +
				`created_at VARCHAR(40) COLLATE utf8mb4_bin NOT NULL, ` +
				`PRIMARY KEY (scope, id)` +
				`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		},
	},
}

// sqlKbStore 基于关系表的知识库文档行级存储。SQLite 单连接下读取遵循
// "收集→关闭 rows→解析"纪律。错误内部记录日志后以 false/nil 返回。
type sqlKbStore struct {
	db     *sql.DB
	logger *slog.Logger
}

func newSQLKbStore(db *sql.DB, logger *slog.Logger) *sqlKbStore {
	return &sqlKbStore{db: db, logger: logger}
}

// kbRow 一行文档的原始列值（tags/emb 待解析）。
type kbRow struct {
	id, title, content string
	tags, emb, source  sql.NullString
	createdAt          string
}

const kbSelectCols = `id, title, content, tags, emb, source, created_at`

func (s *sqlKbStore) list(scope string) []kbDoc {
	return s.listWith(scope, true)
}

// listMeta 读取指定 scope 的全部文档但不含向量（Emb 恒为空），供面板列表用。
func (s *sqlKbStore) listMeta(scope string) []kbDoc {
	return s.listWith(scope, false)
}

// listWith 读取指定 scope 的全部文档；withEmb 为 false 时不加载向量列
// （向量单篇可达数百 KB，面板展示与去重比较都不需要它）。
func (s *sqlKbStore) listWith(scope string, withEmb bool) []kbDoc {
	cols := `id, title, content, tags, source, created_at`
	if withEmb {
		cols = kbSelectCols
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+cols+` FROM ania_kb_doc WHERE scope = ? ORDER BY created_at ASC, id ASC`, scope)
	if err != nil {
		s.logger.Error("读取知识库文档失败", "scope", scope, "error", err)
		return nil
	}
	var raws []kbRow
	for rows.Next() {
		var r kbRow
		var scanErr error
		if withEmb {
			scanErr = rows.Scan(&r.id, &r.title, &r.content, &r.tags, &r.emb, &r.source, &r.createdAt)
		} else {
			scanErr = rows.Scan(&r.id, &r.title, &r.content, &r.tags, &r.source, &r.createdAt)
		}
		if scanErr != nil {
			rows.Close()
			s.logger.Error("读取知识库文档失败", "scope", scope, "error", scanErr)
			return nil
		}
		raws = append(raws, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Error("读取知识库文档失败", "scope", scope, "error", err)
		return nil
	}
	rows.Close()

	docs := make([]kbDoc, 0, len(raws))
	for _, r := range raws {
		docs = append(docs, s.parseRow(scope, r))
	}
	return docs
}

// listDigest 轻量列表：只取 ID/标题/内容（去重比较所需的最小列），
// 不加载 tags/emb/source/created_at——向量列单篇可达数百 KB，
// 为去重整段加载会白白放大读取量。
func (s *sqlKbStore) listDigest(scope string) []kbDoc {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, title, content FROM ania_kb_doc WHERE scope = ? ORDER BY created_at ASC, id ASC`, scope)
	if err != nil {
		s.logger.Error("读取知识库文档失败", "scope", scope, "error", err)
		return nil
	}
	var docs []kbDoc
	for rows.Next() {
		var d kbDoc
		if err := rows.Scan(&d.ID, &d.Title, &d.Content); err != nil {
			rows.Close()
			s.logger.Error("读取知识库文档失败", "scope", scope, "error", err)
			return nil
		}
		d.Scope = scope
		docs = append(docs, d)
	}
	rows.Close()
	return docs
}

func (s *sqlKbStore) get(scope, id string) (kbDoc, bool) {
	var r kbRow
	err := s.db.QueryRowContext(context.Background(),
		`SELECT `+kbSelectCols+` FROM ania_kb_doc WHERE scope = ? AND id = ?`,
		scope, id).Scan(&r.id, &r.title, &r.content, &r.tags, &r.emb, &r.source, &r.createdAt)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.Error("读取知识库文档失败", "scope", scope, "id", id, "error", err)
		}
		return kbDoc{}, false
	}
	return s.parseRow(scope, r), true
}

func (s *sqlKbStore) count(scope string) int {
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM ania_kb_doc WHERE scope = ?`, scope).Scan(&n); err != nil {
		s.logger.Error("统计知识库文档条数失败", "scope", scope, "error", err)
		return 0
	}
	return n
}

func (s *sqlKbStore) insert(scope string, d kbDoc) bool {
	tags, emb := marshalKbJSON(d)
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO ania_kb_doc (scope, id, title, content, tags, emb, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		scope, d.ID, d.Title, d.Content, tags, emb, d.Source, d.CreatedAt.UTC().Format(rowTimeLayout))
	if err != nil {
		s.logger.Error("写入知识库文档失败", "scope", scope, "id", d.ID, "error", err)
		return false
	}
	return true
}

func (s *sqlKbStore) update(scope string, d kbDoc) bool {
	tags, emb := marshalKbJSON(d)
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE ania_kb_doc SET title = ?, content = ?, tags = ?, emb = ?, source = ? WHERE scope = ? AND id = ?`,
		d.Title, d.Content, tags, emb, d.Source, scope, d.ID)
	if err != nil {
		s.logger.Error("更新知识库文档失败", "scope", scope, "id", d.ID, "error", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (s *sqlKbStore) remove(scope, id string) bool {
	res, err := s.db.ExecContext(context.Background(),
		`DELETE FROM ania_kb_doc WHERE scope = ? AND id = ?`, scope, id)
	if err != nil {
		s.logger.Error("删除知识库文档失败", "scope", scope, "id", id, "error", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (s *sqlKbStore) scopes() []string {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT DISTINCT scope FROM ania_kb_doc ORDER BY scope ASC`)
	if err != nil {
		s.logger.Error("列出知识库作用域失败", "error", err)
		return nil
	}
	var scopes []string
	for rows.Next() {
		var sc string
		if err := rows.Scan(&sc); err != nil {
			rows.Close()
			s.logger.Error("列出知识库作用域失败", "error", err)
			return nil
		}
		scopes = append(scopes, sc)
	}
	rows.Close()
	return scopes
}

// parseRow 把一行原始列值解析为 kbDoc；JSON/时间解析失败记日志并忽略该字段。
func (s *sqlKbStore) parseRow(scope string, r kbRow) kbDoc {
	d := kbDoc{ID: r.id, Scope: scope, Title: r.title, Content: r.content}
	if r.tags.Valid {
		if err := json.Unmarshal([]byte(r.tags.String), &d.Tags); err != nil {
			s.logger.Error("反序列化知识库标签失败，忽略标签", "scope", scope, "id", r.id, "error", err)
		}
	}
	if r.emb.Valid {
		if err := json.Unmarshal([]byte(r.emb.String), &d.Emb); err != nil {
			s.logger.Error("反序列化知识库向量失败，忽略向量", "scope", scope, "id", r.id, "error", err)
		}
	}
	if r.source.Valid {
		d.Source = r.source.String
	}
	if t, err := time.Parse(rowTimeLayout, r.createdAt); err == nil {
		d.CreatedAt = t
	}
	return d
}

// marshalKbJSON 序列化 tags/emb 列；空值写 NULL，与 kbDoc 的
// omitempty JSON 语义对齐。
func marshalKbJSON(d kbDoc) (tags, emb any) {
	if len(d.Tags) > 0 {
		if data, err := json.Marshal(d.Tags); err == nil {
			tags = string(data)
		}
	}
	if len(d.Emb) > 0 {
		if data, err := json.Marshal(d.Emb); err == nil {
			emb = string(data)
		}
	}
	return tags, emb
}

// migrateLegacyKV 把旧版「每 scope 一个 JSON 数组」的 kb: 键一次性搬入行级表。
// 旧版本删除最后一篇文档时会留下空数组键（面板「0 篇」残留的来源），迁移
// 顺带清理这类空键。逐键搬运完成后删除旧键，因此迁移天然幂等：中断后重新
// 启动只处理尚未搬完的键，已入表的 (scope,id) 由存在性检查跳过。
// 读取失败的键保留待下次重试，不静默丢数据。
func (km *knowledgeManager) migrateLegacyKV() {
	if km.legacy == nil {
		return
	}
	keys, err := km.legacy.Keys(context.Background(), "")
	if err != nil {
		km.logger.Error("扫描旧版知识库数据失败，跳过迁移", "error", err)
		return
	}
	migrated, cleaned := 0, 0
	for _, scope := range keys {
		var docs []kbDoc
		if ok := km.legacy.Get(context.Background(), scope, &docs); !ok {
			km.logger.Warn("读取旧版知识库数据失败，保留待下次重试", "scope", scope)
			continue
		}
		// 历史数据缺 ID 时补一个，并先回写旧键：否则中途失败重跑会
		// 为同一篇文档重新分配 ID，导致重复入库
		assigned := false
		for i := range docs {
			if docs[i].ID == "" {
				docs[i].ID = newKbID()
				assigned = true
			}
		}
		if assigned {
			if ok := km.legacy.Set(context.Background(), scope, docs); !ok {
				// 补发的 ID 未落旧键，重跑会再分配新 ID 导致重复入库，跳过该作用域
				km.logger.Warn("回写旧版知识库补发 ID 失败，保留待下次重试", "scope", scope)
				continue
			}
		}
		complete := true
		for _, d := range docs {
			if _, exists := km.store.get(scope, d.ID); exists {
				continue // 上次搬运已入表
			}
			d.Scope = scope
			if !km.store.insert(scope, d) {
				complete = false
				break
			}
			migrated++
		}
		if !complete {
			km.logger.Warn("旧版知识库数据迁移未完成，保留待下次重试", "scope", scope)
			continue
		}
		if len(docs) == 0 {
			cleaned++
		}
		km.legacy.Del(context.Background(), scope)
	}
	if migrated > 0 || cleaned > 0 {
		km.logger.Info("旧版知识库数据已迁移到行级存储", "docs", migrated, "empty_scopes_cleaned", cleaned)
	}
}
