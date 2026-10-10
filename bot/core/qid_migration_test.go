package core

import (
	"context"
	"testing"

	"github.com/jeanhua/AniaBot/common/storage"
)

func TestMigrateQQIDPrefixKV(t *testing.T) {
	ctx := context.Background()
	store := makeSqliteStore(t)
	db, dialect, ok := storage.SQLBackend(store)
	if !ok || dialect != storage.SQLDialectSQLite {
		t.Fatalf("SQL backend probe failed: %v %v", dialect, ok)
	}

	rows := []struct {
		ns, key, val string
	}{
		{"__config:", "bot.admin_id", `"123"`},
		{"__config:", "plugin.dailynews.groups", `["123","456"]`},
		{"__config:", "plugin.custom.groups", `[123,456]`},
		{"__config:", "plugin.interceptor.group_users", `["123:456"]`},
		{"__config:", "files.prompt_json", `"{\"groups\":{\"123\":\"prompt\"}}"`},
		{"plugin:history:", "g:123", `[]`},
		{"plugin:memory:", "f:456", `[{"user_id":"789"}]`},
		{"plugin:kb:", "g:123", `[{"scope":"g:123"}]`},
		{"plugin:team:", "g:123:Team", `{}`},
		{"plugin:clock:", "task:a", `{"target_id":"123","created_by":"456"}`},
		{"plugin:quota:", "daily:2026-08-11:g:123", `50`},
		{"plugin:history:", "g:fs:ou_x", `[]`},
		{"__config_presets:", "日常", `{"name":"日常","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","config":{"bot.admin_id":"123","plugin.dailynews.groups":["456"]}}`},
		{"__config_presets:", "无QQID", `{"name":"无QQID","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","config":{"plugin.ai_chat_bot.model":"gpt-4o"}}`},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO ania_kv (namespace, key_name, val) VALUES (?, ?, ?)`,
			r.ns, r.key, r.val); err != nil {
			t.Fatalf("insert %s/%s: %v", r.ns, r.key, err)
		}
	}

	if err := migrateQQIDPrefix(ctx, store, testDiscardLogger()); err != nil {
		t.Fatal(err)
	}

	check := func(ns, key, want string) {
		t.Helper()
		var got string
		err := db.QueryRowContext(ctx,
			`SELECT val FROM ania_kv WHERE namespace = ? AND key_name = ?`, ns, key).Scan(&got)
		if err != nil {
			t.Fatalf("read %s/%s: %v", ns, key, err)
		}
		if got != want {
			t.Fatalf("%s/%s = %q, want %q", ns, key, got, want)
		}
	}

	check("__config:", "bot.admin_id", `"qq:123"`)
	check("__config:", "plugin.dailynews.groups", `["qq:123","qq:456"]`)
	check("__config:", "plugin.custom.groups", `["qq:123","qq:456"]`)
	check("__config:", "plugin.interceptor.group_users", `["qq:123:qq:456"]`)
	check("__config:", "files.prompt_json", `"{\"groups\":{\"qq:123\":\"prompt\"}}"`)
	check("plugin:history:", "g:qq:123", `[]`)
	check("plugin:memory:", "f:qq:456", `[{"user_id":"qq:789"}]`)
	check("plugin:kb:", "g:qq:123", `[{"scope":"g:qq:123"}]`)
	check("plugin:team:", "g:qq:123:Team", `{}`)
	check("plugin:clock:", "task:a", `{"created_by":"qq:456","target_id":"qq:123"}`)
	check("plugin:quota:", "daily:2026-08-11:g:qq:123", `50`)
	check("plugin:history:", "g:fs:ou_x", `[]`)
	// 预设迁移必须保留 name/created_at/updated_at 元数据（map 回写键序确定）
	check("__config_presets:", "日常", `{"config":{"bot.admin_id":"qq:123","plugin.dailynews.groups":["qq:456"]},"created_at":"2026-08-01T10:00:00Z","name":"日常","updated_at":"2026-08-02T10:00:00Z"}`)
	// 无需迁移的预设保持原样
	check("__config_presets:", "无QQID", `{"name":"无QQID","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-02T10:00:00Z","config":{"plugin.ai_chat_bot.model":"gpt-4o"}}`)

	var oldCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ania_kv WHERE key_name = ?`, "g:123").Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatalf("旧会话 key 应已迁移，仍剩 %d 条", oldCount)
	}
}

func TestMigrateQQIDPrefixTables(t *testing.T) {
	ctx := context.Background()
	store := makeSqliteStore(t)
	db, dialect, ok := storage.SQLBackend(store)
	if !ok || dialect != storage.SQLDialectSQLite {
		t.Fatalf("SQL backend probe failed: %v %v", dialect, ok)
	}

	for _, ddl := range []string{
		`CREATE TABLE ania_chat_session (session_id TEXT NOT NULL PRIMARY KEY, msg_count INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE ania_chat_message (session_id TEXT NOT NULL, seq INTEGER NOT NULL, content TEXT NOT NULL, PRIMARY KEY (session_id, seq))`,
		`CREATE TABLE ania_memory (scope TEXT NOT NULL, id TEXT NOT NULL, user_id TEXT NOT NULL DEFAULT '', PRIMARY KEY (scope, id))`,
		`CREATE TABLE ania_kb_doc (scope TEXT NOT NULL, id TEXT NOT NULL, title TEXT NOT NULL, content TEXT NOT NULL, PRIMARY KEY (scope, id))`,
		`CREATE TABLE ania_query_log (seq INTEGER NOT NULL PRIMARY KEY, target_id TEXT NOT NULL, senders TEXT NOT NULL, payload TEXT NOT NULL)`,
		`CREATE TABLE ania_task_log (seq INTEGER NOT NULL PRIMARY KEY, target_id TEXT NOT NULL, payload TEXT NOT NULL)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	inserts := []string{
		`INSERT INTO ania_chat_session (session_id, msg_count) VALUES ('g:1', 1)`,
		`INSERT INTO ania_chat_message (session_id, seq, content) VALUES ('g:1', 0, '{}')`,
		`INSERT INTO ania_memory (scope, id, user_id) VALUES ('g:2', 'm1', '3')`,
		`INSERT INTO ania_kb_doc (scope, id, title, content) VALUES ('g:2', 'k1', '旧文档', '正文')`,
		`INSERT INTO ania_query_log (seq, target_id, senders, payload) VALUES (1, '4', ',5,', '{"target_id":"4","senders":["5"]}')`,
		`INSERT INTO ania_task_log (seq, target_id, payload) VALUES (1, '6', '{"target_id":"6"}')`,
	}
	for _, stmt := range inserts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	if err := migrateQQIDPrefix(ctx, store, testDiscardLogger()); err != nil {
		t.Fatal(err)
	}

	var got string
	if err := db.QueryRowContext(ctx,
		`SELECT session_id FROM ania_chat_session`).Scan(&got); err != nil || got != "g:qq:1" {
		t.Fatalf("chat session = %q, %v", got, err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT session_id FROM ania_chat_message`).Scan(&got); err != nil || got != "g:qq:1" {
		t.Fatalf("chat message session = %q, %v", got, err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT scope || ':' || user_id FROM ania_memory`).Scan(&got); err != nil || got != "g:qq:2:qq:3" {
		t.Fatalf("memory = %q, %v", got, err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT scope FROM ania_kb_doc`).Scan(&got); err != nil || got != "g:qq:2" {
		t.Fatalf("kb doc scope = %q, %v", got, err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT target_id || ':' || senders || ':' || payload FROM ania_query_log`).Scan(&got); err != nil ||
		got != "qq:4:,qq:5,:{\"senders\":[\"qq:5\"],\"target_id\":\"qq:4\"}" {
		t.Fatalf("query log = %q, %v", got, err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT target_id || ':' || payload FROM ania_task_log`).Scan(&got); err != nil ||
		got != "qq:6:{\"target_id\":\"qq:6\"}" {
		t.Fatalf("task log = %q, %v", got, err)
	}
}
