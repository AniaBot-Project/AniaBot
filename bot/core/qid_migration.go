package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeanhua/AniaBot/bot/core/configstore"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
)

var sessionScopePattern = regexp.MustCompile(`(^|:)([gf]):([0-9]+)`)

// migrateQQIDPrefix 把旧版无前缀 QQ ID 迁移到 qq: 前缀。
//
// 覆盖范围：
//   - ania_kv 中配置、配置预设、AI 会话/记忆/知识库/团队/定时任务/日志等命名空间
//   - ania_chat_session / ania_chat_message 的会话 ID
//   - ania_memory 的 scope 与 user_id
//   - ania_kb_doc 的 scope
//   - ania_query_log / ania_task_log 的冗余过滤列与完整 payload
//
// 迁移只处理纯数字 QQ ID 和 g:/f: 数字会话 scope，不会改写其他平台前缀。
func migrateQQIDPrefix(ctx context.Context, store storage.PersistentStorage, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	db, dialect, ok := storage.SQLBackend(store)
	if !ok {
		logger.Warn("QQ ID 前缀迁移仅在 SQL 持久化后端上执行")
		return nil
	}

	kvChanged, err := migrateKVRows(ctx, db, logger)
	if err != nil {
		return fmt.Errorf("迁移 ania_kv QQ ID: %w", err)
	}
	chatChanged, err := migrateChatHistory(ctx, db, dialect, logger)
	if err != nil {
		return fmt.Errorf("迁移对话历史 QQ ID: %w", err)
	}
	memoryChanged, err := migrateMemoryRows(ctx, db, dialect, logger)
	if err != nil {
		return fmt.Errorf("迁移长期记忆 QQ ID: %w", err)
	}
	kbChanged, err := migrateKbDocRows(ctx, db, dialect, logger)
	if err != nil {
		return fmt.Errorf("迁移知识库 QQ ID: %w", err)
	}
	queryChanged, err := migrateQueryLogRows(ctx, db, dialect, logger)
	if err != nil {
		return fmt.Errorf("迁移 Query 日志 QQ ID: %w", err)
	}
	taskChanged, err := migrateTaskLogRows(ctx, db, dialect, logger)
	if err != nil {
		return fmt.Errorf("迁移任务日志 QQ ID: %w", err)
	}

	total := kvChanged + chatChanged + memoryChanged + kbChanged + queryChanged + taskChanged
	if total > 0 {
		logger.Info("QQ ID 前缀迁移完成", "rows", total)
	}
	return nil
}

type kvRow struct {
	namespace string
	key       string
	val       string
}

func migrateKVRows(ctx context.Context, db *sql.DB, logger *slog.Logger) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT namespace, key_name, val FROM ania_kv`)
	if err != nil {
		return 0, err
	}
	var items []kvRow
	existing := make(map[string]bool)
	for rows.Next() {
		var r kvRow
		if err := rows.Scan(&r.namespace, &r.key, &r.val); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, r)
		existing[rowID(r.namespace, r.key)] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	changed := 0
	for _, r := range items {
		newKey := rewriteScopeIDs(r.key)
		newVal, valChanged := migrateKVValue(r.namespace, r.key, r.val)
		if newKey == r.key && !valChanged {
			continue
		}
		oldID := rowID(r.namespace, r.key)
		newID := rowID(r.namespace, newKey)
		if newKey != r.key && existing[newID] {
			if valChanged {
				if _, err := db.ExecContext(ctx,
					`UPDATE ania_kv SET val = ? WHERE namespace = ? AND key_name = ?`,
					newVal, r.namespace, newKey); err != nil {
					return changed, err
				}
			}
			if _, err := db.ExecContext(ctx,
				`DELETE FROM ania_kv WHERE namespace = ? AND key_name = ?`,
				r.namespace, r.key); err != nil {
				return changed, err
			}
			delete(existing, oldID)
			changed++
			continue
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE ania_kv SET key_name = ?, val = ? WHERE namespace = ? AND key_name = ?`,
			newKey, newVal, r.namespace, r.key); err != nil {
			return changed, err
		}
		delete(existing, oldID)
		existing[newID] = true
		changed++
	}
	return changed, nil
}

func rowID(namespace, key string) string {
	return namespace + "\x00" + key
}

// migrateKVValue 按命名空间和键名迁移单个 ania_kv 值。
func migrateKVValue(namespace, key, val string) (string, bool) {
	if namespace == configstore.Namespace+":" {
		return migrateConfigRaw(key, val)
	}
	if namespace == configstore.NamespacePresets+":" {
		return migratePresetRaw(val)
	}
	if hasNamespaceSegment(namespace, "memory:") ||
		hasNamespaceSegment(namespace, "kb:") ||
		(hasNamespaceSegment(namespace, "clock:") && strings.HasPrefix(key, "task:")) ||
		(hasNamespaceSegment(namespace, "clocklog:") && strings.HasPrefix(key, "e:")) ||
		(hasNamespaceSegment(namespace, "querylog:") && strings.HasPrefix(key, "e:")) {
		return migrateAIJSONValue(namespace, key, val)
	}
	return val, false
}

func hasNamespaceSegment(namespace, segment string) bool {
	return strings.HasSuffix(namespace, segment) || strings.Contains(namespace, ":"+segment)
}

func migrateConfigRaw(key, raw string) (string, bool) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw, false
	}
	nv, ok := migrateConfigValue(key, v)
	if !ok {
		return raw, false
	}
	data, err := json.Marshal(nv)
	if err != nil {
		return raw, false
	}
	return string(data), true
}

func migrateConfigValue(key string, v any) (any, bool) {
	if key == configstore.KeyPromptJSON {
		raw, ok := v.(string)
		if !ok {
			return nil, false
		}
		next, changed := migratePromptJSON(raw)
		if !changed {
			return nil, false
		}
		return next, true
	}
	if key == "bot.admin_id" {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		next := message.NormalizeQQID(s)
		if next == s {
			return nil, false
		}
		return next, true
	}
	if strings.HasSuffix(key, ".groups") || strings.HasSuffix(key, ".friends") {
		items, ok := asStringSlice(v)
		if !ok {
			return nil, false
		}
		changed := false
		for i, item := range items {
			next := message.NormalizeQQID(item)
			if next != item {
				items[i] = next
				changed = true
			}
		}
		if !changed {
			return nil, false
		}
		return items, true
	}
	if strings.HasSuffix(key, ".group_users") {
		items, ok := asStringSlice(v)
		if !ok {
			return nil, false
		}
		changed := false
		for i, item := range items {
			next := migrateGroupUserRule(item)
			if next != item {
				items[i] = next
				changed = true
			}
		}
		if !changed {
			return nil, false
		}
		return items, true
	}
	return nil, false
}

func asStringSlice(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, len(x))
		for i, item := range x {
			var s string
			switch v := item.(type) {
			case string:
				s = v
			case float64:
				s = strconv.FormatInt(int64(v), 10)
			default:
				return nil, false
			}
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

func migratePromptJSON(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return raw, false
	}
	var cfg struct {
		Groups  map[string]string `json:"groups,omitempty"`
		Friends map[string]string `json:"friends,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return raw, false
	}
	changed := false
	for key, val := range cfg.Groups {
		next := message.NormalizeQQID(key)
		if next != key {
			delete(cfg.Groups, key)
			cfg.Groups[next] = val
			changed = true
		}
	}
	for key, val := range cfg.Friends {
		next := message.NormalizeQQID(key)
		if next != key {
			delete(cfg.Friends, key)
			cfg.Friends[next] = val
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return raw, false
	}
	return string(data), true
}

func migratePresetRaw(raw string) (string, bool) {
	// 用 map 而非结构体解析：保留 name/created_at/updated_at 等全部元数据，
	// 仅改写 config 快照内的 QQ ID。此前用仅含 Config 字段的结构体回写，
	// 会把预设名称和时间戳清成零值，导致面板出现无法删除的无名预设。
	var preset map[string]any
	if err := json.Unmarshal([]byte(raw), &preset); err != nil {
		return raw, false
	}
	cfg, ok := preset["config"].(map[string]any)
	if !ok {
		return raw, false
	}
	changed := false
	for key, val := range cfg {
		next, ok := migrateConfigValue(key, val)
		if ok {
			cfg[key] = next
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	data, err := json.Marshal(preset)
	if err != nil {
		return raw, false
	}
	return string(data), true
}

func migrateAIJSONValue(namespace, key, val string) (string, bool) {
	var out any
	changed := false
	switch {
	case hasNamespaceSegment(namespace, "memory:"):
		var entries []map[string]any
		if err := json.Unmarshal([]byte(val), &entries); err != nil {
			return val, false
		}
		for _, e := range entries {
			if userID, ok := e["user_id"].(string); ok {
				next := message.NormalizeQQID(userID)
				if next != userID {
					e["user_id"] = next
					changed = true
				}
			}
		}
		out = entries
	case hasNamespaceSegment(namespace, "kb:"):
		var docs []map[string]any
		if err := json.Unmarshal([]byte(val), &docs); err != nil {
			return val, false
		}
		for _, d := range docs {
			if scope, ok := d["scope"].(string); ok {
				next := rewriteScopeIDs(scope)
				if next != scope {
					d["scope"] = next
					changed = true
				}
			}
		}
		out = docs
	case hasNamespaceSegment(namespace, "clock:") && strings.HasPrefix(key, "task:"):
		var task map[string]any
		if err := json.Unmarshal([]byte(val), &task); err != nil {
			return val, false
		}
		if targetID, ok := task["target_id"].(string); ok {
			next := message.NormalizeQQID(targetID)
			if next != targetID {
				task["target_id"] = next
				changed = true
			}
		}
		if createdBy, ok := task["created_by"].(string); ok {
			next := message.NormalizeQQID(createdBy)
			if next != createdBy {
				task["created_by"] = next
				changed = true
			}
		}
		out = task
	case hasNamespaceSegment(namespace, "clocklog:") && strings.HasPrefix(key, "e:"):
		var entry map[string]any
		if err := json.Unmarshal([]byte(val), &entry); err != nil {
			return val, false
		}
		if migrateTargetAndSenders(entry) {
			changed = true
		}
		out = entry
	case hasNamespaceSegment(namespace, "querylog:") && strings.HasPrefix(key, "e:"):
		var entry map[string]any
		if err := json.Unmarshal([]byte(val), &entry); err != nil {
			return val, false
		}
		if migrateTargetAndSenders(entry) {
			changed = true
		}
		out = entry
	default:
		return val, false
	}
	if !changed {
		return val, false
	}
	data, err := json.Marshal(out)
	if err != nil {
		return val, false
	}
	return string(data), true
}

func migrateTargetAndSenders(entry map[string]any) bool {
	changed := false
	if targetID, ok := entry["target_id"].(string); ok {
		next := message.NormalizeQQID(targetID)
		if next != targetID {
			entry["target_id"] = next
			changed = true
		}
	}
	if raw, ok := entry["senders"].([]any); ok {
		for i, sender := range raw {
			s, ok := sender.(string)
			if !ok {
				continue
			}
			next := message.NormalizeQQID(s)
			if next != s {
				raw[i] = next
				changed = true
			}
		}
	}
	return changed
}

func rewriteScopeIDs(s string) string {
	locs := sessionScopePattern.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		end := loc[1]
		// 只迁移独立 scope 段：数字后必须是冒号或字符串结尾，避免误改 g:123abc。
		if end < len(s) && s[end] != ':' {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(s[loc[2]:loc[5]]) // 前缀 + g/f
		b.WriteByte(':')
		b.WriteString(message.NormalizeQQID(s[loc[6]:loc[7]]))
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

func migrateGroupUserRule(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return line
	}
	first := strings.Index(line, ":")
	if first < 0 {
		return message.NormalizeQQID(line)
	}
	boundary := first
	for _, prefix := range []string{message.QQIDPrefix, "qo:", "fs:", "tg:", "dc:"} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		next := strings.Index(rest, ":")
		if next < 0 {
			break
		}
		boundary = len(prefix) + next
		break
	}
	group := message.NormalizeQQID(strings.TrimSpace(line[:boundary]))
	user := message.NormalizeQQID(strings.TrimSpace(line[boundary+1:]))
	return group + ":" + user
}

func migrateChatHistory(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, logger *slog.Logger) (int, error) {
	hasSession := tableExists(ctx, db, dialect, "ania_chat_session")
	hasMessage := tableExists(ctx, db, dialect, "ania_chat_message")
	if !hasSession && !hasMessage {
		return 0, nil
	}
	var ids []string
	seen := make(map[string]bool)
	if hasSession {
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT session_id FROM ania_chat_session`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		rows.Close()
	}
	if hasMessage {
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT session_id FROM ania_chat_message`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		rows.Close()
	}

	changed := 0
	for _, old := range ids {
		next := rewriteScopeIDs(old)
		if next == old {
			continue
		}
		if hasSession {
			if rowExists(ctx, db, dialect, "ania_chat_session", "session_id", next) {
				logger.Warn("迁移对话历史时目标会话已存在，保留新会话", "old", old, "new", next)
				if _, err := db.ExecContext(ctx, `DELETE FROM ania_chat_session WHERE session_id = ?`, old); err != nil {
					return changed, err
				}
			} else if _, err := db.ExecContext(ctx,
				`UPDATE ania_chat_session SET session_id = ? WHERE session_id = ?`, next, old); err != nil {
				return changed, err
			}
		}
		if hasMessage {
			if rowExists(ctx, db, dialect, "ania_chat_message", "session_id", next) {
				logger.Warn("迁移对话消息时目标会话已存在，保留新会话", "old", old, "new", next)
				if _, err := db.ExecContext(ctx, `DELETE FROM ania_chat_message WHERE session_id = ?`, old); err != nil {
					return changed, err
				}
			} else if _, err := db.ExecContext(ctx,
				`UPDATE ania_chat_message SET session_id = ? WHERE session_id = ?`, next, old); err != nil {
				return changed, err
			}
		}
		changed++
	}
	return changed, nil
}

func migrateMemoryRows(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, logger *slog.Logger) (int, error) {
	if !tableExists(ctx, db, dialect, "ania_memory") {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT scope, id, user_id FROM ania_memory`)
	if err != nil {
		return 0, err
	}
	type memoryRow struct {
		scope  string
		id     string
		userID string
	}
	var items []memoryRow
	for rows.Next() {
		var r memoryRow
		if err := rows.Scan(&r.scope, &r.id, &r.userID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	changed := 0
	for _, r := range items {
		nextScope := rewriteScopeIDs(r.scope)
		nextUser := message.NormalizeQQID(r.userID)
		if nextScope == r.scope && nextUser == r.userID {
			continue
		}
		if nextScope != r.scope && rowExists2(ctx, db, dialect, "ania_memory", "scope", "id", nextScope, r.id) {
			logger.Warn("迁移长期记忆时目标记录已存在，保留新记录", "old", r.scope, "new", nextScope, "id", r.id)
			if _, err := db.ExecContext(ctx,
				`DELETE FROM ania_memory WHERE scope = ? AND id = ?`, r.scope, r.id); err != nil {
				return changed, err
			}
		} else if _, err := db.ExecContext(ctx,
			`UPDATE ania_memory SET scope = ?, user_id = ? WHERE scope = ? AND id = ?`,
			nextScope, nextUser, r.scope, r.id); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// migrateKbDocRows 迁移知识库文档的 scope。文档从旧版 KV 命名空间搬入
// ania_kb_doc 表前，其 scope 已由 migrateKVRows 重写；此函数兜底处理
// 直接入表（或迁移中断后补跑）的数据，与 ania_memory 的 scope 迁移同构。
func migrateKbDocRows(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, logger *slog.Logger) (int, error) {
	if !tableExists(ctx, db, dialect, "ania_kb_doc") {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT scope, id FROM ania_kb_doc`)
	if err != nil {
		return 0, err
	}
	type kbRow struct {
		scope string
		id    string
	}
	var items []kbRow
	for rows.Next() {
		var r kbRow
		if err := rows.Scan(&r.scope, &r.id); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	changed := 0
	for _, r := range items {
		nextScope := rewriteScopeIDs(r.scope)
		if nextScope == r.scope {
			continue
		}
		if rowExists2(ctx, db, dialect, "ania_kb_doc", "scope", "id", nextScope, r.id) {
			logger.Warn("迁移知识库文档时目标记录已存在，保留新记录", "old", r.scope, "new", nextScope, "id", r.id)
			if _, err := db.ExecContext(ctx,
				`DELETE FROM ania_kb_doc WHERE scope = ? AND id = ?`, r.scope, r.id); err != nil {
				return changed, err
			}
		} else if _, err := db.ExecContext(ctx,
			`UPDATE ania_kb_doc SET scope = ? WHERE scope = ? AND id = ?`,
			nextScope, r.scope, r.id); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func migrateQueryLogRows(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, logger *slog.Logger) (int, error) {
	if !tableExists(ctx, db, dialect, "ania_query_log") {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT seq, target_id, senders, payload FROM ania_query_log`)
	if err != nil {
		return 0, err
	}
	type logRow struct {
		seq     uint64
		target  string
		senders string
		payload string
	}
	var items []logRow
	for rows.Next() {
		var r logRow
		if err := rows.Scan(&r.seq, &r.target, &r.senders, &r.payload); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	changed := 0
	for _, r := range items {
		nextTarget := message.NormalizeQQID(r.target)
		nextSenders := migrateSendersCSV(r.senders)
		nextPayload, payloadChanged := migrateLogPayload(r.payload)
		if nextTarget == r.target && nextSenders == r.senders && !payloadChanged {
			continue
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE ania_query_log SET target_id = ?, senders = ?, payload = ? WHERE seq = ?`,
			nextTarget, nextSenders, nextPayload, r.seq); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func migrateTaskLogRows(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, logger *slog.Logger) (int, error) {
	if !tableExists(ctx, db, dialect, "ania_task_log") {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT seq, target_id, payload FROM ania_task_log`)
	if err != nil {
		return 0, err
	}
	type logRow struct {
		seq     uint64
		target  string
		payload string
	}
	var items []logRow
	for rows.Next() {
		var r logRow
		if err := rows.Scan(&r.seq, &r.target, &r.payload); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	changed := 0
	for _, r := range items {
		nextTarget := message.NormalizeQQID(r.target)
		nextPayload, payloadChanged := migrateLogPayload(r.payload)
		if nextTarget == r.target && !payloadChanged {
			continue
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE ania_task_log SET target_id = ?, payload = ? WHERE seq = ?`,
			nextTarget, nextPayload, r.seq); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func migrateSendersCSV(s string) string {
	if s == "" {
		return s
	}
	trimmed := strings.Trim(s, ",")
	if trimmed == "" {
		return s
	}
	parts := strings.Split(trimmed, ",")
	changed := false
	for i, part := range parts {
		part = strings.TrimSpace(part)
		next := message.NormalizeQQID(part)
		if next != part {
			parts[i] = next
			changed = true
		}
	}
	if !changed {
		return s
	}
	return "," + strings.Join(parts, ",") + ","
}

func migrateLogPayload(payload string) (string, bool) {
	var entry map[string]any
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		return payload, false
	}
	if !migrateTargetAndSenders(entry) {
		return payload, false
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return payload, false
	}
	return string(data), true
}

func tableExists(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, table string) bool {
	var one int
	var err error
	switch dialect {
	case storage.SQLDialectSQLite:
		err = db.QueryRowContext(ctx,
			`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&one)
	case storage.SQLDialectMySQL:
		err = db.QueryRowContext(ctx,
			`SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&one)
	default:
		return false
	}
	return err == nil
}

func rowExists(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, table, column, value string) bool {
	var one int
	err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT 1 FROM %s WHERE %s = ?`, table, column), value).Scan(&one)
	return err == nil
}

func rowExists2(ctx context.Context, db *sql.DB, dialect storage.SQLDialect, table, column1, column2, value1, value2 string) bool {
	var one int
	err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT 1 FROM %s WHERE %s = ? AND %s = ?`, table, column1, column2),
		value1, value2).Scan(&one)
	return err == nil
}
