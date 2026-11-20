package pluginaichat

import (
	"context"
	"database/sql"
	"strings"
)

// sqlDeleteByIDsChunk 单条 SQL 携带的 ID 数量上限：SQLite 的变量个数上限在旧版本
// 只有 999，面板「全选」删除大作用域时必须分批，否则会因变量过多整批失败。
const sqlDeleteByIDsChunk = 500

// sqlDeleteByIDs 按 (scope, id) 批量删除行级存储中的记录，返回实际删除条数。
// 长期记忆（ania_memory）与知识库（ania_kb_doc）共用：ids 去重并跳过空值，
// 按 sqlDeleteByIDsChunk 分批执行；中途失败时返回已删除的条数并带回错误。
// table 只能是包内的表名常量，不能来自外部输入。
func sqlDeleteByIDs(db *sql.DB, table, scope string, ids []string) (int, error) {
	uniq := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}

	deleted := 0
	for start := 0; start < len(uniq); start += sqlDeleteByIDsChunk {
		end := min(start+sqlDeleteByIDsChunk, len(uniq))
		batch := uniq[start:end]

		holders := make([]string, len(batch))
		args := make([]any, 0, len(batch)+1)
		args = append(args, scope)
		for i, id := range batch {
			holders[i] = "?"
			args = append(args, id)
		}

		res, err := db.ExecContext(context.Background(),
			`DELETE FROM `+table+` WHERE scope = ? AND id IN (`+strings.Join(holders, ", ")+`)`, args...)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
	}
	return deleted, nil
}
