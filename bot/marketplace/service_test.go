// TestServiceListDetail 走完整 Service 路径的真实集成测试（默认跳过）。
package marketplace

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

type mapConfig struct{ m map[string]any }

func (c *mapConfig) Get(k string) (any, bool)  { v, ok := c.m[k]; return v, ok }
func (c *mapConfig) Set(k string, v any) error { c.m[k] = v; return nil }

func TestServiceListDetail(t *testing.T) {
	if os.Getenv("ANIABOT_TEST_NETWORK") != "1" {
		t.Skip("设置 ANIABOT_TEST_NETWORK=1 以运行真实 GitHub 集成测试")
	}
	cfg := &mapConfig{m: map[string]any{}}
	svc := New(cfg, slog.Default(), WithPluginDir(t.TempDir()), WithCacheDir(t.TempDir()))
	ctx := context.Background()

	list, err := svc.List(ctx, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("市场列表为空")
	}
	found := false
	for _, p := range list {
		if p.ID == "example" {
			found = true
		}
	}
	if !found {
		t.Fatalf("市场列表缺少 example: %+v", list)
	}

	d, err := svc.Detail(ctx, "example")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if d.Manifest.Name == "" || d.Readme == "" {
		t.Fatalf("Detail 不完整: name=%q readme_len=%d", d.Manifest.Name, len(d.Readme))
	}

	if err := svc.SaveToken("ghp_fake"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	info := svc.Info()
	if info["token_set"] != true {
		t.Fatalf("Info.token_set 应为 true: %+v", info)
	}
}
