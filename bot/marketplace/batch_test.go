package marketplace

import (
	"strings"
	"testing"
	"time"
)

// newBatchService 构造一个已开启市场、插件目录在临时目录的服务，
// 并预置若干已安装插件（pluginID -> name/version 任意）。
func newBatchService(t *testing.T, installed ...string) *Service {
	t.Helper()
	cfg := &mapConfig{m: map[string]any{
		"bot.marketplace.enable":     true,
		"bot.marketplace.plugin_dir": t.TempDir(),
		"bot.marketplace.cache_dir":  t.TempDir(),
	}}
	svc := New(cfg, nil)
	for _, id := range installed {
		if err := svc.manifest().set(InstalledPlugin{ID: id, Name: id, Version: "1.0.0"}); err != nil {
			t.Fatalf("预置已安装插件失败: %v", err)
		}
	}
	return svc
}

func TestNormalizeIDs(t *testing.T) {
	got, err := normalizeIDs([]string{" aa ", "bb", "aa", "", "  "})
	if err != nil {
		t.Fatalf("normalizeIDs: %v", err)
	}
	if len(got) != 2 || got[0] != "aa" || got[1] != "bb" {
		t.Fatalf("应去空白去重并保持顺序, got %v", got)
	}
	if _, err := normalizeIDs([]string{"../etc"}); err == nil {
		t.Fatal("非法 ID 应被拒绝")
	}
	if _, err := normalizeIDs([]string{"A"}); err == nil {
		t.Fatal("大写 ID 应被拒绝")
	}
}

func TestBuildOps(t *testing.T) {
	svc := newBatchService(t, "installed-a", "installed-b")

	t.Run("空请求", func(t *testing.T) {
		if _, err := svc.buildOps(nil, nil); err == nil {
			t.Fatal("空列表应报错")
		}
	})

	t.Run("混合批量", func(t *testing.T) {
		ops, err := svc.buildOps([]string{"new-a", "new-b"}, []string{"installed-a"})
		if err != nil {
			t.Fatalf("buildOps: %v", err)
		}
		if len(ops) != 3 {
			t.Fatalf("应有 3 个操作, got %d", len(ops))
		}
		if ops[0].kind != opInstall || ops[0].id != "new-a" || ops[1].kind != opInstall || ops[1].id != "new-b" {
			t.Fatalf("安装操作顺序错误: %+v", ops)
		}
		if ops[2].kind != opUninstall || ops[2].id != "installed-a" {
			t.Fatalf("卸载操作应在最后: %+v", ops)
		}
	})

	t.Run("安装卸载冲突", func(t *testing.T) {
		if _, err := svc.buildOps([]string{"installed-a"}, []string{"installed-a"}); err == nil || !strings.Contains(err.Error(), "同时出现") {
			t.Fatalf("应因冲突报错, got %v", err)
		}
	})

	t.Run("卸载未安装插件", func(t *testing.T) {
		if _, err := svc.buildOps(nil, []string{"not-installed"}); err == nil || !strings.Contains(err.Error(), "未安装") {
			t.Fatalf("应因未安装报错, got %v", err)
		}
	})

	t.Run("超过数量上限", func(t *testing.T) {
		ids := make([]string, 0, maxBatchSize+1)
		for i := 0; i <= maxBatchSize; i++ {
			ids = append(ids, "plugin-"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26)))
		}
		if _, err := svc.buildOps(ids, nil); err == nil || !strings.Contains(err.Error(), "最多操作") {
			t.Fatalf("超过上限应报错, got %v", err)
		}
	})

	t.Run("未开启市场", func(t *testing.T) {
		off := New(&mapConfig{m: map[string]any{}}, nil)
		if err := off.Batch([]string{"x"}, nil); err == nil {
			t.Fatal("市场未开启时批量接口应报错")
		}
	})
}

func TestBatchTimeout(t *testing.T) {
	if got := batchTimeout(1); got != 15*time.Minute {
		t.Fatalf("单操作超时应为 15m, got %v", got)
	}
	if got := batchTimeout(3); got != 21*time.Minute {
		t.Fatalf("3 个操作超时应为 21m, got %v", got)
	}
	if got := batchTimeout(50); got != 30*time.Minute {
		t.Fatalf("超时应封顶 30m, got %v", got)
	}
}
