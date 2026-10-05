package marketplace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRepo(t *testing.T) {
	cases := []struct{ in, owner, name string }{
		{"AniaBot-Project/AniaBot-Plugins", "AniaBot-Project", "AniaBot-Plugins"},
		{"https://github.com/AniaBot-Project/AniaBot-Plugins", "AniaBot-Project", "AniaBot-Plugins"},
		{"https://github.com/AniaBot-Project/AniaBot-Plugins/", "AniaBot-Project", "AniaBot-Plugins"},
		{"", "AniaBot-Project", "AniaBot-Plugins"},
		{"not-a-repo", "AniaBot-Project", "AniaBot-Plugins"},
	}
	for _, c := range cases {
		o, n := parseRepo(c.in)
		if o != c.owner || n != c.name {
			t.Fatalf("parseRepo(%q) = %q/%q, want %q/%q", c.in, o, n, c.owner, c.name)
		}
	}
}

func TestManifestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	ms := newManifestStore(dir)
	p := InstalledPlugin{ID: "hello", Name: "Hello", Version: "1.0.0", Commit: "abc", InstalledAt: nowStamp()}
	if err := ms.set(p); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok := ms.find("hello")
	if !ok || got.Version != "1.0.0" {
		t.Fatalf("find 失败: %+v ok=%v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("manifest.json 未写入: %v", err)
	}
	if err := ms.remove("hello"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := ms.find("hello"); ok {
		t.Fatal("remove 后仍能找到")
	}
}

func TestManifestStoreBackupRestore(t *testing.T) {
	dir := t.TempDir()
	ms := newManifestStore(dir)
	if err := ms.set(InstalledPlugin{ID: "a", Name: "A", Version: "1", Commit: "c1"}); err != nil {
		t.Fatal(err)
	}
	// 第二次写入会生成 .bak（第一次写入前的空清单）
	if err := ms.set(InstalledPlugin{ID: "b", Name: "B", Version: "2", Commit: "c2"}); err != nil {
		t.Fatal(err)
	}
	if err := ms.restoreBackup(); err != nil {
		t.Fatalf("restoreBackup: %v", err)
	}
	if _, ok := ms.find("a"); !ok {
		t.Fatal("回滚后应恢复到第一次写入的清单（含 a）")
	}
	if _, ok := ms.find("b"); ok {
		t.Fatal("回滚后不应包含 b")
	}
}

// TestGithubIntegration 真实 GitHub 集成测试（默认跳过，需 ANIABOT_TEST_NETWORK=1）。
func TestGithubIntegration(t *testing.T) {
	if os.Getenv("ANIABOT_TEST_NETWORK") != "1" {
		t.Skip("设置 ANIABOT_TEST_NETWORK=1 以运行真实 GitHub 集成测试")
	}
	ctx := context.Background()
	c := newGitHubClient(defaultRepo, "")
	commit, err := c.latestCommit(ctx, defaultBranch)
	if err != nil {
		t.Fatalf("latestCommit: %v", err)
	}
	idx, err := c.fetchIndex(ctx, commit)
	if err != nil {
		t.Fatalf("fetchIndex: %v", err)
	}
	if len(idx.Plugins) == 0 {
		t.Fatal("index 为空")
	}
	example := idx.Plugins[0]
	if example.ID != "example" {
		t.Fatalf("第一个插件应为 example, got %s", example.ID)
	}
	readme, err := c.fetchReadme(ctx, example.ID, example.ReadmeName(), commit)
	if err != nil || readme == "" {
		t.Fatalf("fetchReadme: %q err=%v", readme, err)
	}
	dst := t.TempDir()
	if err := c.downloadPlugin(ctx, commit, example.ID, dst); err != nil {
		t.Fatalf("downloadPlugin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "plugin.json")); err != nil {
		t.Fatalf("解压后缺少 plugin.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "plugin.go")); err != nil {
		t.Fatalf("解压后缺少 plugin.go: %v", err)
	}
}
