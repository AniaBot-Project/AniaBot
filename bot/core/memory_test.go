package core

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

func testMemoryStorage() *AniaMemoryStorage {
	return NewAniaMemoryStorage(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestMemoryStorageReclaimsExpiredEntries 过期条目必须被真正回收，而不只是读取时
// 跳过：core 的幂等去重对每条入站消息写一个 TTL=10min 的键，只做惰性删除会让
// 这些键永久驻留 map，内存随消息量单调上涨直到进程重启。
//
// 直接改写 expireAt 制造"已过期"状态（而非依赖真实等待）：短 TTL + 全量测试并发
// 时，写在循环中途就会过期并触发回收，断言会随负载抖动。
func TestMemoryStorageReclaimsExpiredEntries(t *testing.T) {
	store := testMemoryStorage()
	ctx := context.Background()

	const n = memReclaimMinEntries + 100
	for i := 0; i < n; i++ {
		if !store.SetString(ctx, keyN(i), "1", storage.WithTTL(time.Hour)) {
			t.Fatalf("写入第 %d 个键失败", i)
		}
	}
	// 全部标记为已过期（保持 TTL 语义，只是把到期时刻挪到过去）
	store.mu.Lock()
	past := time.Now().Add(-time.Minute)
	for _, v := range store.data {
		v.expireAt = past
	}
	store.mu.Unlock()

	if _, ok := store.GetString(ctx, keyN(0)); ok {
		t.Fatal("过期键不应可读")
	}
	if got := len(store.data); got != n {
		t.Fatalf("前置条件：回收前应仍有 %d 条, got %d", n, got)
	}

	// 再写一个键触发写路径回收：占比过半即整表清扫
	store.SetString(ctx, "trigger", "1")
	store.mu.RLock()
	remaining := len(store.data)
	store.mu.RUnlock()
	if remaining > n/2 {
		t.Fatalf("过期键未在写路径回收，仍驻留 %d 条（写入 %d 条）", remaining, n)
	}
}

// TestMemoryStorageKeepsUnexpiredEntries 回收不得误删未过期条目。
func TestMemoryStorageKeepsUnexpiredEntries(t *testing.T) {
	store := testMemoryStorage()
	ctx := context.Background()
	for i := 0; i < memReclaimMinEntries+50; i++ {
		store.SetString(ctx, keyN(i), "v", storage.WithTTL(time.Hour))
	}
	store.SetString(ctx, "trigger", "1")
	if got, ok := store.GetString(ctx, keyN(7)); !ok || got != "v" {
		t.Fatalf("未过期条目被误删: ok=%v val=%q", ok, got)
	}
	store.mu.RLock()
	remaining := len(store.data)
	store.mu.RUnlock()
	if remaining < memReclaimMinEntries {
		t.Fatalf("未过期条目被大量删除，仅剩 %d 条", remaining)
	}
}

// TestMemoryStorageCheckExistStillWorksWithExpired 惰性判定与回收共存：
// 已过期的键在 CheckExist 语义下等同不存在（可被重新占用）。
func TestMemoryStorageCheckExistStillWorksWithExpired(t *testing.T) {
	store := testMemoryStorage()
	ctx := context.Background()
	if !store.SetString(ctx, "k", "v1", storage.WithTTL(time.Millisecond), storage.WithCheckExist()) {
		t.Fatal("首次占用应成功")
	}
	time.Sleep(10 * time.Millisecond)
	if !store.SetString(ctx, "k", "v2", storage.WithCheckExist()) {
		t.Fatal("过期后应可重新占用")
	}
	if got, _ := store.GetString(ctx, "k"); got != "v2" {
		t.Fatalf("重新占用后应读到新值, got %q", got)
	}
}

func keyN(i int) string {
	return "k" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)) + string(rune('a'+(i/17576)%26))
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		s, pattern string
		want       bool
	}{
		{"anything", "*", true},
		{"exact", "exact", true},
		{"exact", "other", false},
		{"user:1:active", "user:*:active", true},
		// 前后缀在原串中重叠时不能误判：user:active 缺少中间段
		{"user:active", "user:*:active", false},
		{"axbxc", "a*b*c", true},
		// 中间字面段 b 不存在
		{"a1c", "a*b*c", false},
		// '*' 可匹配空串（与 Redis glob 一致）
		{"abc", "a*b*c", true},
		{"prefix:key", "prefix:*", true},
		{"other:key", "prefix:*", false},
		{"hello-world", "*world", true},
		{"hello-world", "*mars", false},
		{"xxabcyy", "*abc*", true},
		{"xxdefyy", "*abc*", false},
	}
	for _, c := range cases {
		got, err := matchPattern(c.s, c.pattern)
		if err != nil {
			t.Fatalf("matchPattern(%q, %q) error: %v", c.s, c.pattern, err)
		}
		if got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.s, c.pattern, got, c.want)
		}
	}
}
