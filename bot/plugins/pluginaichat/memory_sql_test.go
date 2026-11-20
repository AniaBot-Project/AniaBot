package pluginaichat

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"

	_ "modernc.org/sqlite"
)

// newTestMemoryDB 打开内存 SQLite 并建好长期记忆表。
func newTestMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := storage.EnsureTables(context.Background(), db, storage.SQLDialectSQLite, memoryTables...); err != nil {
		db.Close()
		t.Fatalf("ensure tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestMemoryManagerSQL(t *testing.T, maxEntries int) *memoryManager {
	t.Helper()
	db := newTestMemoryDB(t)
	logger := testLogger()
	return &memoryManager{
		store:      newSQLMemoryStore(db, logger),
		logger:     logger,
		maxEntries: maxEntries,
	}
}

func TestSQLMemoryStoreFieldRoundTrip(t *testing.T) {
	db := newTestMemoryDB(t)
	s := newSQLMemoryStore(db, testLogger())

	entry := memoryEntry{
		ID:        "abc12345",
		UserID:    "456",
		Content:   "小明喜欢熬夜打榜",
		Tags:      []string{"偏好", "作息"},
		Emb:       []float32{0.1, -0.2, 3.5},
		CreatedAt: time.Date(2026, 8, 5, 12, 30, 0, 123456789, time.UTC),
	}
	if !s.insert("g:123", entry) {
		t.Fatal("insert 失败")
	}

	entries := s.list("g:123")
	if len(entries) != 1 {
		t.Fatalf("list = %d 条, want 1", len(entries))
	}
	got := entries[0]
	if got.ID != entry.ID || got.UserID != entry.UserID || got.Content != entry.Content {
		t.Fatalf("字段往返不符: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "偏好" || got.Tags[1] != "作息" {
		t.Fatalf("tags 往返不符: %+v", got.Tags)
	}
	if len(got.Emb) != 3 || got.Emb[0] != 0.1 || got.Emb[1] != -0.2 || got.Emb[2] != 3.5 {
		t.Fatalf("emb 往返不符: %+v", got.Emb)
	}
	if !got.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("created_at 往返不符: %v -> %v", entry.CreatedAt, got.CreatedAt)
	}

	// 空 tags/emb 写 NULL、读回 nil
	if !s.insert("g:123", memoryEntry{ID: "def67890", Content: "无标签", CreatedAt: time.Now().UTC()}) {
		t.Fatal("insert 失败")
	}
	for _, e := range s.list("g:123") {
		if e.ID == "def67890" && (e.Tags != nil || e.Emb != nil) {
			t.Fatalf("空 tags/emb 应读回 nil: %+v", e)
		}
	}
}

func TestSQLMemoryStoreRemoveManyChunked(t *testing.T) {
	s := newSQLMemoryStore(newTestMemoryDB(t), testLogger())

	// 超过单条 SQL 的 ID 上限（sqlDeleteByIDsChunk），验证分批删除与精确计数
	total := sqlDeleteByIDsChunk + 200
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("id%05d", i)
		ids = append(ids, id)
		if !s.insert("g:1", memoryEntry{ID: id, Content: "记忆", CreatedAt: time.Now().UTC()}) {
			t.Fatalf("insert %s 失败", id)
		}
	}
	// 其他 scope 的同名 ID 不能被误删（scope 必须参与条件）
	if !s.insert("g:2", memoryEntry{ID: ids[0], Content: "保留", CreatedAt: time.Now().UTC()}) {
		t.Fatal("insert 失败")
	}

	// 重复 ID 只算一次
	batch := append(append([]string{}, ids...), ids[0])
	if got := s.removeMany("g:1", batch); got != total {
		t.Fatalf("removeMany = %d, want %d", got, total)
	}
	if got := len(s.list("g:1")); got != 0 {
		t.Fatalf("g:1 应被清空，实际剩 %d 条", got)
	}
	if got := len(s.list("g:2")); got != 1 {
		t.Fatalf("其他 scope 被误删，实际剩 %d 条", got)
	}
}

func TestSQLMemoryManagerScenarios(t *testing.T) {
	m := newTestMemoryManagerSQL(t, 2)

	// add + scope 隔离
	e1, err := m.add("g:123", "456", "小明喜欢熬夜打榜", []string{"偏好"})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.list("g:999"); len(got) != 0 {
		t.Fatalf("scope 隔离失效: %+v", got)
	}

	// 规范化去重
	e2, err := m.add("g:123", "", " 小明喜欢熬夜打榜 \n", nil)
	if err != nil || e1.ID != e2.ID {
		t.Fatalf("去重失效: %v %s vs %s", err, e1.ID, e2.ID)
	}

	// 上限（Sleep 保证两条记录 created_at 严格递增：Windows 上 time.Now 精度可能为微秒，
	// 相同时间戳时 SQL 按 id ASC 排随机 ID 会让顺序不稳定）
	time.Sleep(time.Millisecond)
	if _, err := m.add("g:123", "", "第二条", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.add("g:123", "", "第三条", nil); err == nil {
		t.Fatal("达到上限应报错")
	}

	// update：字段更新且创建时间不变
	if err := m.update("g:123", e1.ID, "789", "新内容", []string{"新标签"}); err != nil {
		t.Fatal(err)
	}
	got := m.list("g:123")
	if len(got) != 2 || got[0].ID != e1.ID {
		t.Fatalf("update 后列表不符（按创建时间升序，首条应为 e1）: %+v", got)
	}
	if got[0].Content != "新内容" || got[0].UserID != "789" || len(got[0].Tags) != 1 || got[0].Tags[0] != "新标签" {
		t.Fatalf("update 字段不符: %+v", got[0])
	}
	if !got[0].CreatedAt.Equal(e1.CreatedAt) {
		t.Fatalf("update 不应改变创建时间: %v -> %v", e1.CreatedAt, got[0].CreatedAt)
	}

	// 跨 scope 更新/删除不生效
	if err := m.update("g:999", e1.ID, "", "跨 scope", nil); err == nil {
		t.Fatal("不应跨 scope 更新")
	}
	if m.remove("g:999", e1.ID) {
		t.Fatal("不应跨 scope 删除")
	}

	// remove 后可继续写入
	if !m.remove("g:123", e1.ID) {
		t.Fatal("remove 失败")
	}
	if _, err := m.add("g:123", "", "第三条", nil); err != nil {
		t.Fatalf("删除后 add 仍失败: %v", err)
	}

	// scopes 排序
	if _, err := m.add("f:456", "", "私聊记忆", nil); err != nil {
		t.Fatal(err)
	}
	scopes := m.scopes()
	if len(scopes) != 2 || scopes[0] != "f:456" || scopes[1] != "g:123" {
		t.Fatalf("scopes 不符（应排序）: %v", scopes)
	}
}

// TestMemoryStoreConformance 同一操作序列分别作用于 KV 与 SQL 后端，
// 结果应一致（内容序列与 scopes）。
func TestMemoryStoreConformance(t *testing.T) {
	kv := newTestMemoryManager(0)
	sqlm := newTestMemoryManagerSQL(t, 0)

	if _, err := kv.add("g:1", "u1", "记忆A", []string{"标签A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlm.add("g:1", "u1", "记忆A", []string{"标签A"}); err != nil {
		t.Fatal(err)
	}
	// Sleep 保证同 scope 内 created_at 严格递增（Windows 上 time.Now 精度可能为微秒，
	// 相同时间戳时 SQL 按 id ASC 排随机 ID、KV 按插入序，两者序列会不一致）
	time.Sleep(time.Millisecond)
	if _, err := kv.add("g:1", "", "记忆B", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlm.add("g:1", "", "记忆B", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.add("f:2", "", "记忆C", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlm.add("f:2", "", "记忆C", nil); err != nil {
		t.Fatal(err)
	}

	compare := func(scope string) {
		t.Helper()
		kvList, sqlList := kv.list(scope), sqlm.list(scope)
		if len(kvList) != len(sqlList) {
			t.Fatalf("%s: 条数不符 kv=%d sql=%d", scope, len(kvList), len(sqlList))
		}
		for i := range kvList {
			if kvList[i].Content != sqlList[i].Content || kvList[i].UserID != sqlList[i].UserID {
				t.Fatalf("%s 第 %d 条不符:\nkv  %+v\nsql %+v", scope, i, kvList[i], sqlList[i])
			}
			if len(kvList[i].Tags) != len(sqlList[i].Tags) {
				t.Fatalf("%s 第 %d 条 tags 不符: kv=%v sql=%v", scope, i, kvList[i].Tags, sqlList[i].Tags)
			}
		}
	}
	compare("g:1")
	compare("f:2")

	// update + remove 后仍一致
	kvID, sqlID := kv.list("g:1")[0].ID, sqlm.list("g:1")[0].ID
	if err := kv.update("g:1", kvID, "u2", "记忆A改", []string{"标签A2"}); err != nil {
		t.Fatal(err)
	}
	if err := sqlm.update("g:1", sqlID, "u2", "记忆A改", []string{"标签A2"}); err != nil {
		t.Fatal(err)
	}
	kv.remove("g:1", kv.list("g:1")[1].ID)
	sqlm.remove("g:1", sqlm.list("g:1")[1].ID)
	compare("g:1")

	kvScopes, sqlScopes := kv.scopes(), sqlm.scopes()
	if len(kvScopes) != len(sqlScopes) {
		t.Fatalf("scopes 不符: kv=%v sql=%v", kvScopes, sqlScopes)
	}
	for i := range kvScopes {
		if kvScopes[i] != sqlScopes[i] {
			t.Fatalf("scopes 不符: kv=%v sql=%v", kvScopes, sqlScopes)
		}
	}
}
