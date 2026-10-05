package core

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeanhua/AniaBot/bot/core/testdata/custom/plugins/fake"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// recordPlugin 记录 OnUnload 调用（reason 与次数），可注入错误与 panic。
type recordPlugin struct {
	plugin.Meta
	order *[]string
	calls []plugin.UnloadReason
	err   error
	panic bool
}

func (p *recordPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	p.calls = append(p.calls, reason)
	if p.order != nil {
		*p.order = append(*p.order, p.GetMeta().Name)
	}
	if p.panic {
		panic("卸载钩子 panic")
	}
	return p.err
}

// TestUnloadPlugins 卸载清扫：只调用实现 UnloadEvent 的插件、按启动逆序、
// 每个插件仅一次，重复触发（含 Stop）不再调用。
func TestUnloadPlugins(t *testing.T) {
	var order []string
	first := &recordPlugin{Meta: plugin.Meta{Name: "first"}, order: &order}
	middle := &recordPlugin{Meta: plugin.Meta{Name: "middle"}, order: &order}

	ania := NewAniaBot()
	ania.AddPlugin(first, &plainPlugin{}, middle) // 中间夹一个未实现卸载接口的插件
	ania.pluginsStarted.Store(true)

	ania.unloadPlugins(plugin.UnloadShutdown)

	if want := []string{"middle", "first"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("应按启动逆序执行: got %v, want %v", order, want)
	}
	if len(first.calls) != 1 || first.calls[0] != plugin.UnloadShutdown {
		t.Fatalf("first 应收到一次 shutdown 卸载: %+v", first.calls)
	}
	if len(middle.calls) != 1 || middle.calls[0] != plugin.UnloadShutdown {
		t.Fatalf("middle 应收到一次 shutdown 卸载: %+v", middle.calls)
	}

	// 重复触发不再调用（幂等）
	ania.unloadPlugins(plugin.UnloadShutdown)
	if len(first.calls) != 1 || len(middle.calls) != 1 {
		t.Fatalf("重复卸载不应再次调用: first=%v middle=%v", first.calls, middle.calls)
	}

	// Stop 走同一套逻辑，同样不重复
	ania.Stop()
	if len(first.calls) != 1 || len(middle.calls) != 1 {
		t.Fatalf("Stop 后不应重复调用: first=%v middle=%v", first.calls, middle.calls)
	}
}

// TestUnloadPluginsSkipBeforeStart 插件尚未完成 Start 时不触发卸载钩子。
func TestUnloadPluginsSkipBeforeStart(t *testing.T) {
	p := &recordPlugin{Meta: plugin.Meta{Name: "p"}}
	ania := NewAniaBot()
	ania.AddPlugin(p)

	ania.Stop()

	if len(p.calls) != 0 {
		t.Fatalf("Start 前不应触发卸载钩子: %+v", p.calls)
	}
}

// TestUnloadPluginsPanicIsolation 单个插件卸载钩子 panic 不影响其它插件，
// 且该插件同样标记为已触发、不重复执行。
func TestUnloadPluginsPanicIsolation(t *testing.T) {
	bad := &recordPlugin{Meta: plugin.Meta{Name: "bad"}, panic: true}
	good := &recordPlugin{Meta: plugin.Meta{Name: "good"}}

	ania := NewAniaBot()
	ania.AddPlugin(bad, good)
	ania.pluginsStarted.Store(true)

	ania.unloadPlugins(plugin.UnloadShutdown)

	if len(good.calls) != 1 || len(bad.calls) != 1 {
		t.Fatalf("panic 不应影响清理流程: good=%v bad=%v", good.calls, bad.calls)
	}
	ania.unloadPlugins(plugin.UnloadShutdown)
	if len(good.calls) != 1 || len(bad.calls) != 1 {
		t.Fatalf("panic 后重复卸载应被跳过: good=%v bad=%v", good.calls, bad.calls)
	}
}

// TestUnloadPluginsErrorContinues 钩子返回错误不阻断其它插件的清理。
func TestUnloadPluginsErrorContinues(t *testing.T) {
	bad := &recordPlugin{Meta: plugin.Meta{Name: "bad"}, err: errors.New("清理失败")}
	good := &recordPlugin{Meta: plugin.Meta{Name: "good"}}

	ania := NewAniaBot()
	ania.AddPlugin(bad, good)
	ania.pluginsStarted.Store(true)

	ania.unloadPlugins(plugin.UnloadShutdown)

	if len(good.calls) != 1 || len(bad.calls) != 1 {
		t.Fatalf("错误不应阻断其它插件: good=%v bad=%v", good.calls, bad.calls)
	}
}

// TestUnloadPluginMarketplace 市场卸载按 ID 命中运行实例（包路径 custom/plugins/<id>），
// 以 UnloadUninstall 原因触发一次，且后续退出清扫不重复。
func TestUnloadPluginMarketplace(t *testing.T) {
	p := &fake.FakePlugin{Meta: plugin.Meta{Name: "伪装的市场插件"}}
	ania := NewAniaBot()
	ania.AddPlugin(p)
	ania.pluginsStarted.Store(true)

	called, err := ania.UnloadPlugin(context.Background(), "fake")
	if err != nil || !called {
		t.Fatalf("应命中市场插件并触发卸载: called=%v err=%v", called, err)
	}
	if len(p.Calls) != 1 || p.Calls[0] != plugin.UnloadUninstall {
		t.Fatalf("原因应为 uninstall: %+v", p.Calls)
	}

	// 再次卸载（或退出清扫）不重复触发
	called, err = ania.UnloadPlugin(context.Background(), "fake")
	if err != nil || !called {
		t.Fatalf("重复卸载应返回已触发: called=%v err=%v", called, err)
	}
	ania.unloadPlugins(plugin.UnloadShutdown)
	if len(p.Calls) != 1 {
		t.Fatalf("退出清扫不应重复触发市场卸载过的插件: %+v", p.Calls)
	}
}

// TestUnloadPluginNotFound 未安装/未运行的插件返回错误，不触发任何钩子。
func TestUnloadPluginNotFound(t *testing.T) {
	rec := &recordPlugin{Meta: plugin.Meta{Name: "普通插件"}}
	ania := NewAniaBot()
	ania.AddPlugin(rec)
	ania.pluginsStarted.Store(true)

	if _, err := ania.UnloadPlugin(context.Background(), "not-installed"); err == nil {
		t.Fatal("未找到运行实例时应返回错误")
	}
	if len(rec.calls) != 0 {
		t.Fatalf("未命中插件不应触发卸载: %+v", rec.calls)
	}
}

// TestUnloadPluginBeforeStart 插件未完成 Start 时市场卸载不触发钩子。
func TestUnloadPluginBeforeStart(t *testing.T) {
	p := &fake.FakePlugin{Meta: plugin.Meta{Name: "伪装的市场插件"}}
	ania := NewAniaBot()
	ania.AddPlugin(p)

	called, err := ania.UnloadPlugin(context.Background(), "fake")
	if err != nil || called {
		t.Fatalf("Start 前不应触发卸载: called=%v err=%v", called, err)
	}
	if len(p.Calls) != 0 {
		t.Fatalf("Start 前不应触发卸载钩子: %+v", p.Calls)
	}
}

// TestIsMarketplacePlugin 包路径匹配：仅 custom/plugins/<id> 下的插件被视为市场插件。
func TestIsMarketplacePlugin(t *testing.T) {
	prefix := "github.com/jeanhua/AniaBot/custom/plugins/"
	cases := []struct {
		pkg  string
		id   string
		want bool
	}{
		{prefix + "example", "example", true},
		{prefix + "anti-withdrawal", "anti-withdrawal", true},
		{prefix + "example/internal/impl", "example", true}, // 类型定义在插件子包
		{prefix + "example", "example2", false},             // 同前缀不同 ID
		{prefix + "example2", "example", false},             // 尾部不一致
		{prefix + "example-extra", "example", false},        // ID 为前缀但不是同一插件
		{"github.com/jeanhua/AniaBot/bot/plugins/pluginsys", "example", false},
		{prefix + "example", "", false},
	}
	for _, c := range cases {
		if got := isMarketplacePlugin(c.pkg, c.id); got != c.want {
			t.Errorf("isMarketplacePlugin(%q, %q) = %v, want %v", c.pkg, c.id, got, c.want)
		}
	}
}

// plainPlugin 不实现 UnloadEvent 的插件（仅嵌入 Meta）。
type plainPlugin struct{ plugin.Meta }
