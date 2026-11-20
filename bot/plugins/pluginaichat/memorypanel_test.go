package pluginaichat

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// newTestMemoryPlugin 构造一个挂载了记忆管理器的插件实例（maxEntries=0 不限条数）
func newTestMemoryPlugin() *AIChatPlugin {
	p := &AIChatPlugin{memoryManager: newTestMemoryManager(0)}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return p
}

func TestMemoryPanelCRUD(t *testing.T) {
	p := newTestMemoryPlugin()

	// 新增
	id, err := p.MemoryCreate(plugininfo.MemoryEntryUpsert{
		Scope:   "g:123",
		UserID:  "456",
		Content: "小明喜欢熬夜打榜",
		Tags:    []string{"偏好"},
	})
	if err != nil {
		t.Fatalf("MemoryCreate 失败: %v", err)
	}
	if id == "" {
		t.Fatal("MemoryCreate 未返回 ID")
	}

	// scope 列表
	scopes := p.MemoryScopes()
	if len(scopes) != 1 {
		t.Fatalf("scope 数量不符: %+v", scopes)
	}
	s := scopes[0]
	if s.Scope != "g:123" || s.Kind != "group" || s.Target != "123" || s.Count != 1 {
		t.Fatalf("scope 信息不符: %+v", s)
	}

	// 列表
	entries, err := p.MemoryList("g:123")
	if err != nil {
		t.Fatalf("MemoryList 失败: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != id || entries[0].Content != "小明喜欢熬夜打榜" {
		t.Fatalf("记忆列表不符: %+v", entries)
	}

	// 编辑
	if err := p.MemoryUpdate(plugininfo.MemoryEntryUpsert{
		Scope:   "g:123",
		ID:      id,
		Content: "小明改作息了",
		Tags:    []string{"近况"},
	}); err != nil {
		t.Fatalf("MemoryUpdate 失败: %v", err)
	}
	entries, _ = p.MemoryList("g:123")
	if len(entries) != 1 || entries[0].Content != "小明改作息了" || entries[0].UserID != "" {
		t.Fatalf("更新后内容不符: %+v", entries)
	}

	// 删除（行级存储：scope 内最后一条删除后，scope 从列表消失）
	if err := p.MemoryDelete("g:123", id); err != nil {
		t.Fatalf("MemoryDelete 失败: %v", err)
	}
	if got := p.MemoryScopes(); len(got) != 0 {
		t.Fatalf("删除后 scope 列表应为空: %+v", got)
	}
	// 删除不存在的 ID
	if err := p.MemoryDelete("g:123", id); err == nil {
		t.Fatal("删除不存在的 ID 应报错")
	}
}

func TestMemoryPanelScopeValidation(t *testing.T) {
	p := newTestMemoryPlugin()

	for _, bad := range []string{"", "g:", "f:", "x:123", "abc", "123", "../admin", "memory:g:1", "G:123"} {
		if _, err := p.MemoryList(bad); err == nil {
			t.Fatalf("非法 scope %q 应被拒绝（MemoryList）", bad)
		}
		if _, err := p.MemoryCreate(plugininfo.MemoryEntryUpsert{Scope: bad, Content: "x"}); err == nil {
			t.Fatalf("非法 scope %q 应被拒绝（MemoryCreate）", bad)
		}
		if err := p.MemoryDelete(bad, "deadbeef"); err == nil {
			t.Fatalf("非法 scope %q 应被拒绝（MemoryDelete）", bad)
		}
	}

	// 多平台合法 scope：QQ 数字 ID 与带平台前缀的 ID（如飞书 fs:）均接受
	for _, good := range []string{"g:123", "f:456", "g:fs:oc_xxx", "f:fs:ou_xxx"} {
		if _, err := p.MemoryList(good); err != nil {
			t.Fatalf("合法 scope %q 应被接受（MemoryList）: %v", good, err)
		}
		if _, err := p.MemoryCreate(plugininfo.MemoryEntryUpsert{Scope: good, Content: "x"}); err != nil {
			t.Fatalf("合法 scope %q 应被接受（MemoryCreate）: %v", good, err)
		}
	}

	// 更新时 id 为空应报错
	if err := p.MemoryUpdate(plugininfo.MemoryEntryUpsert{Scope: "g:123", Content: "x"}); err == nil {
		t.Fatal("MemoryUpdate 缺少 id 应报错")
	}
}

func TestMemoryPanelBatchDelete(t *testing.T) {
	p := newTestMemoryPlugin()

	ids := make([]string, 0, 3)
	for _, content := range []string{"记忆一", "记忆二", "记忆三"} {
		id, err := p.MemoryCreate(plugininfo.MemoryEntryUpsert{Scope: "g:123", Content: content})
		if err != nil {
			t.Fatalf("MemoryCreate(%q) 失败: %v", content, err)
		}
		ids = append(ids, id)
	}
	// 另一个 scope 的条目不应被波及
	otherID, err := p.MemoryCreate(plugininfo.MemoryEntryUpsert{Scope: "g:456", Content: "别的群"})
	if err != nil {
		t.Fatalf("MemoryCreate(其他 scope) 失败: %v", err)
	}

	// 删除两条 + 一个不存在的 ID + 重复/空 ID：返回实际删除条数
	deleted, err := p.MemoryDeleteMany("g:123", []string{ids[0], ids[0], "", ids[2], "deadbeef"})
	if err != nil {
		t.Fatalf("MemoryDeleteMany 失败: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("应删除 2 条，实际 %d 条", deleted)
	}
	entries, err := p.MemoryList("g:123")
	if err != nil {
		t.Fatalf("MemoryList 失败: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != ids[1] {
		t.Fatalf("删除后应只剩 1 条，实际 %+v", entries)
	}
	// 其他 scope 不受影响
	if got, _ := p.MemoryList("g:456"); len(got) != 1 || got[0].ID != otherID {
		t.Fatalf("其他 scope 的记忆被误删: %+v", got)
	}

	// 再次删除同一批 ID：返回 0 且不报错（面板重复点击/并发删除）
	if n, err := p.MemoryDeleteMany("g:123", []string{ids[0], ids[2]}); err != nil || n != 0 {
		t.Fatalf("重复删除应返回 0 且不报错，实际 n=%d err=%v", n, err)
	}
	// 空 ID 列表与非法 scope 应拒绝
	if _, err := p.MemoryDeleteMany("g:123", nil); err == nil {
		t.Fatal("空 ID 列表应报错")
	}
	if _, err := p.MemoryDeleteMany("bad-scope", []string{ids[1]}); err == nil {
		t.Fatal("非法 scope 应报错")
	}
}

func TestMemoryPanelDisabled(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	if got := p.MemoryScopes(); got != nil {
		t.Fatalf("功能未启用时 MemoryScopes 应返回 nil，实际 %v", got)
	}
	if _, err := p.MemoryList("g:123"); err == nil {
		t.Fatal("功能未启用时 MemoryList 应报错")
	}
	if _, err := p.MemoryCreate(plugininfo.MemoryEntryUpsert{Scope: "g:123", Content: "x"}); err == nil {
		t.Fatal("功能未启用时 MemoryCreate 应报错")
	}
	if err := p.MemoryUpdate(plugininfo.MemoryEntryUpsert{Scope: "g:123", ID: "x", Content: "y"}); err == nil {
		t.Fatal("功能未启用时 MemoryUpdate 应报错")
	}
	if err := p.MemoryDelete("g:123", "x"); err == nil {
		t.Fatal("功能未启用时 MemoryDelete 应报错")
	}
	if _, err := p.MemoryDeleteMany("g:123", []string{"x"}); err == nil {
		t.Fatal("功能未启用时 MemoryDeleteMany 应报错")
	}
}
