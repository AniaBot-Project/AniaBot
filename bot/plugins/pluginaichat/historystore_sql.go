package pluginaichat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/storage"
)

// 对话历史的行级存储 schema：会话表 + 消息表，一对多关系（无外键，
// 由应用层维护一致性）。每条消息一行，追加只插入新行，避免 KV 整段
// JSON 反复全量重写的写放大。
//
// ania_chat_session.msg_count 充当消息序号分配器：Append 在事务内读取
// 并推进计数，消息行按 (session_id, seq) 联合主键落盘；Replace（压缩/
// 截断后）在事务内清空该会话消息并重排 seq、重置计数；Clear 直接删除
// 两表对应行，下次 Append 自动重建会话行、seq 从 0 开始。
var chatHistoryTables = []storage.TableDDL{
	{
		Name: "ania_chat_session",
		SQLite: []string{
			`CREATE TABLE IF NOT EXISTS ania_chat_session (` +
				`session_id TEXT NOT NULL, ` +
				`msg_count INTEGER NOT NULL DEFAULT 0, ` +
				`created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, ` +
				`updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, ` +
				`PRIMARY KEY (session_id))`,
		},
		MySQL: []string{
			// session_id 显式 utf8mb4_bin（字节序、大小写敏感），与 ania_kv 对齐
			`CREATE TABLE IF NOT EXISTS ania_chat_session (` +
				`session_id VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`msg_count INT NOT NULL DEFAULT 0, ` +
				`created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, ` +
				`updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, ` +
				`PRIMARY KEY (session_id)` +
				`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		},
	},
	{
		Name: "ania_chat_message",
		SQLite: []string{
			`CREATE TABLE IF NOT EXISTS ania_chat_message (` +
				`session_id TEXT NOT NULL, ` +
				`seq INTEGER NOT NULL, ` +
				`role TEXT NOT NULL, ` +
				`content TEXT NOT NULL, ` +
				`created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, ` +
				`PRIMARY KEY (session_id, seq))`,
		},
		MySQL: []string{
			`CREATE TABLE IF NOT EXISTS ania_chat_message (` +
				`session_id VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
				`seq INT NOT NULL, ` +
				`role VARCHAR(32) COLLATE utf8mb4_bin NOT NULL, ` +
				`content MEDIUMTEXT NOT NULL, ` +
				`created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, ` +
				`PRIMARY KEY (session_id, seq)` +
				`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		},
	},
}

// sqlHistoryStore 基于关系表的对话历史行级存储。
//
// 每个群聊/好友会话一个实例（session 已含 g:/f: 前缀）。同一会话由插件层
// 会话锁串行访问，不存在并发写；SQLite 单连接（MaxOpenConns(1)）下所有
// 读取遵循"收集→关闭 rows→解析"纪律，写入均在单事务内完成且事务期间只
// 使用 tx 句柄。错误内部记录日志后吞掉，避免拖垮主对话流程。
type sqlHistoryStore struct {
	db      *sql.DB
	session string
	logger  *slog.Logger
}

func newSQLHistoryStore(db *sql.DB, session string, logger *slog.Logger) *sqlHistoryStore {
	return &sqlHistoryStore{db: db, session: session, logger: logger}
}

// messageRow 一条待插入的消息行（content 为单条 Message 的 JSON）。
type messageRow struct {
	role    string
	content string
}

// marshalRows 预序列化消息。先序列化再开事务：SQLite 单连接下事务持有唯一
// 连接，序列化错误不应导致事务悬挂；序列化失败的消息跳过。
func (s *sqlHistoryStore) marshalRows(messages []aichat.Message) []messageRow {
	rows := make([]messageRow, 0, len(messages))
	for _, m := range messages {
		data, err := json.Marshal(m)
		if err != nil {
			s.logger.Error("序列化历史消息失败，跳过该条", "session", s.session, "error", err)
			continue
		}
		rows = append(rows, messageRow{role: string(m.Role), content: string(data)})
	}
	return rows
}

func (s *sqlHistoryStore) Load(ctx context.Context) ([]aichat.Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content FROM ania_chat_message WHERE session_id = ? ORDER BY seq ASC`, s.session)
	if err != nil {
		s.logger.Error("加载对话历史失败", "session", s.session, "error", err)
		return nil, nil
	}
	var contents []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			s.logger.Error("读取对话历史失败", "session", s.session, "error", err)
			return nil, nil
		}
		contents = append(contents, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Error("读取对话历史失败", "session", s.session, "error", err)
		return nil, nil
	}
	rows.Close()
	if len(contents) == 0 {
		return nil, nil
	}
	msgs := make([]aichat.Message, 0, len(contents))
	for _, c := range contents {
		var m aichat.Message
		if err := json.Unmarshal([]byte(c), &m); err != nil {
			// 单条损坏不阻断整体回放，跳过即可
			s.logger.Error("反序列化历史消息失败，跳过该条", "session", s.session, "error", err)
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func (s *sqlHistoryStore) Append(ctx context.Context, messages []aichat.Message) error {
	rows := s.marshalRows(messages)
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.Error("追加对话历史失败：开启事务", "session", s.session, "error", err)
		return err
	}
	defer tx.Rollback()
	base, err := s.allocSeqTx(ctx, tx, len(rows))
	if err != nil {
		s.logger.Error("追加对话历史失败：分配序号", "session", s.session, "error", err)
		return err
	}
	if err := s.bulkInsertTx(ctx, tx, base, rows); err != nil {
		s.logger.Error("追加对话历史失败：写入消息", "session", s.session, "error", err)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("追加对话历史失败：提交事务", "session", s.session, "error", err)
		return err
	}
	return nil
}

func (s *sqlHistoryStore) Replace(ctx context.Context, messages []aichat.Message) error {
	rows := s.marshalRows(messages)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.Error("覆盖对话历史失败：开启事务", "session", s.session, "error", err)
		return err
	}
	defer tx.Rollback()
	// 同事务内先删后插：seq 从 0 重排不会撞主键；崩溃只会整体回滚，
	// 不会出现计数与行数分叉
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ania_chat_message WHERE session_id = ?`, s.session); err != nil {
		s.logger.Error("覆盖对话历史失败：清理旧消息", "session", s.session, "error", err)
		return err
	}
	if err := s.setCountTx(ctx, tx, len(rows)); err != nil {
		s.logger.Error("覆盖对话历史失败：重置计数", "session", s.session, "error", err)
		return err
	}
	if err := s.bulkInsertTx(ctx, tx, 0, rows); err != nil {
		s.logger.Error("覆盖对话历史失败：写入消息", "session", s.session, "error", err)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("覆盖对话历史失败：提交事务", "session", s.session, "error", err)
		return err
	}
	return nil
}

func (s *sqlHistoryStore) Clear(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.Error("清除对话历史失败：开启事务", "session", s.session, "error", err)
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ania_chat_message WHERE session_id = ?`, s.session); err != nil {
		s.logger.Error("清除对话历史失败：删除消息", "session", s.session, "error", err)
		return err
	}
	// 会话行一并删除：会话表只保留仍有历史的会话，下次 Append 自动重建
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ania_chat_session WHERE session_id = ?`, s.session); err != nil {
		s.logger.Error("清除对话历史失败：删除会话", "session", s.session, "error", err)
		return err
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("清除对话历史失败：提交事务", "session", s.session, "error", err)
		return err
	}
	return nil
}

// allocSeqTx 在事务内为追加分配起始序号，并把会话计数推进 count。
// 会话行不存在时创建（created_at/updated_at 取默认值）。
func (s *sqlHistoryStore) allocSeqTx(ctx context.Context, tx *sql.Tx, count int) (int, error) {
	var cur int
	err := tx.QueryRowContext(ctx,
		`SELECT msg_count FROM ania_chat_session WHERE session_id = ?`, s.session).Scan(&cur)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ania_chat_session (session_id, msg_count) VALUES (?, ?)`,
			s.session, count); err != nil {
			return 0, err
		}
		return 0, nil
	case err != nil:
		return 0, err
	default:
		if _, err := tx.ExecContext(ctx,
			`UPDATE ania_chat_session SET msg_count = ?, updated_at = CURRENT_TIMESTAMP WHERE session_id = ?`,
			cur+count, s.session); err != nil {
			return 0, err
		}
		return cur, nil
	}
}

// setCountTx 在事务内把会话计数重置为 count（Replace 重排后调用）；会话行
// 不存在时创建。空历史（count=0）时保留会话行、计数归零。
func (s *sqlHistoryStore) setCountTx(ctx context.Context, tx *sql.Tx, count int) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE ania_chat_session SET msg_count = ?, updated_at = CURRENT_TIMESTAMP WHERE session_id = ?`,
		count, s.session)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ania_chat_session (session_id, msg_count) VALUES (?, ?)`,
			s.session, count); err != nil {
			return err
		}
	}
	return nil
}

// bulkInsertTx 在事务内分块批量插入消息行，seq 从 base 递增。
// 分块（每 100 条一个多行 INSERT）限制单条语句的占位符数量与包体积。
func (s *sqlHistoryStore) bulkInsertTx(ctx context.Context, tx *sql.Tx, base int, rows []messageRow) error {
	const chunk = 100
	for i := 0; i < len(rows); i += chunk {
		end := min(i+chunk, len(rows))
		part := rows[i:end]
		var sb strings.Builder
		sb.WriteString(`INSERT INTO ania_chat_message (session_id, seq, role, content) VALUES `)
		args := make([]any, 0, len(part)*4)
		for j, r := range part {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?)`)
			args = append(args, s.session, base+i+j, r.role, r.content)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}
