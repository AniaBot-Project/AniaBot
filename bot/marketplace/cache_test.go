package marketplace

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestReadmeCacheRoundtripAndExpiry(t *testing.T) {
	svc := New(&mapConfig{m: map[string]any{}}, slog.Default(), WithCacheDir(t.TempDir()))
	if _, ok := svc.loadCachedReadme("example"); ok {
		t.Fatal("空缓存不应命中")
	}
	svc.saveCachedReadme("example", "# 示例插件")
	got, ok := svc.loadCachedReadme("example")
	if !ok || got != "# 示例插件" {
		t.Fatalf("缓存写入后应命中: ok=%v got=%q", ok, got)
	}
	// 模拟过期：把文件 mtime 拨到 TTL 之前
	path := svc.readmeCachePath("example")
	old := time.Now().Add(-readmeCacheTTL - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if _, ok := svc.loadCachedReadme("example"); ok {
		t.Fatal("过期缓存不应命中")
	}
}

func TestClearReadmeCache(t *testing.T) {
	svc := New(&mapConfig{m: map[string]any{}}, slog.Default(), WithCacheDir(t.TempDir()))
	svc.saveCachedReadme("a", "A")
	svc.saveCachedReadme("b", "B")
	svc.clearReadmeCache()
	for _, id := range []string{"a", "b"} {
		if _, ok := svc.loadCachedReadme(id); ok {
			t.Fatalf("clear 后 %s 缓存仍命中", id)
		}
	}
}

func TestIndexSyncedAt(t *testing.T) {
	svc := New(&mapConfig{m: map[string]any{}}, slog.Default(), WithCacheDir(t.TempDir()))
	if got := svc.indexSyncedAt(); got != 0 {
		t.Fatalf("无缓存文件时应为 0, got %d", got)
	}
	if err := os.MkdirAll(svc.cacheDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc.indexCachePath(), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := svc.indexSyncedAt(); got == 0 {
		t.Fatal("有缓存文件时应返回 mtime")
	}
}
