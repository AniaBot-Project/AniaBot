package marketplace

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// stubUnloader 记录 UnloadPlugin 调用。
type stubUnloader struct {
	calls  []string
	called bool
	err    error
}

func (s *stubUnloader) UnloadPlugin(ctx context.Context, id string) (bool, error) {
	s.calls = append(s.calls, id)
	return s.called, s.err
}

// TestWithUnloaderOption New 的 WithUnloader 选项注入卸载能力。
func TestWithUnloaderOption(t *testing.T) {
	u := &stubUnloader{}
	svc := New(&mapConfig{m: map[string]any{}}, slog.Default(), WithUnloader(u))
	if svc.unloader != u {
		t.Fatal("WithUnloader 未生效")
	}
}

// TestFireUnloadHook 卸载钩子调用：插件实现钩子时记录「已执行」，
// 未注入卸载能力时静默跳过。
func TestFireUnloadHook(t *testing.T) {
	t.Run("插件实现钩子", func(t *testing.T) {
		u := &stubUnloader{called: true}
		svc := New(&mapConfig{m: map[string]any{}}, slog.Default(), WithUnloader(u))

		svc.fireUnloadHook(context.Background(), "example")

		if len(u.calls) != 1 || u.calls[0] != "example" {
			t.Fatalf("应以插件 ID 调用卸载钩子: %+v", u.calls)
		}
		logs := strings.Join(svc.state.snapshot()["logs"].([]string), "\n")
		if !strings.Contains(logs, "数据清理") {
			t.Fatalf("任务日志应记录卸载钩子执行: %q", logs)
		}
	})

	t.Run("钩子执行失败不阻断", func(t *testing.T) {
		u := &stubUnloader{called: true, err: errors.New("清理失败")}
		svc := New(&mapConfig{m: map[string]any{}}, slog.Default(), WithUnloader(u))

		svc.fireUnloadHook(context.Background(), "example") // 不应 panic

		if len(u.calls) != 1 {
			t.Fatalf("应调用一次卸载钩子: %+v", u.calls)
		}
		logs := strings.Join(svc.state.snapshot()["logs"].([]string), "\n")
		if !strings.Contains(logs, "继续卸载") {
			t.Fatalf("任务日志应记录失败并继续: %q", logs)
		}
	})

	t.Run("未注入卸载能力", func(t *testing.T) {
		svc := New(&mapConfig{m: map[string]any{}}, slog.Default())
		svc.fireUnloadHook(context.Background(), "example") // 不应 panic
		if logs := svc.state.snapshot()["logs"].([]string); len(logs) != 0 {
			t.Fatalf("未注入时不应产生日志: %v", logs)
		}
	})
}
