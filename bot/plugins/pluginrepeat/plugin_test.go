package pluginrepeat

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeanhua/AniaBot/common/pluginconfig"
	"github.com/spf13/viper"
)

// startWithConfig 模拟框架的配置加载流程：先按 cfg 标签填充结构体，再调用 Start。
func startWithConfig(t *testing.T, kv map[string]any) *RepeatPlugin {
	t.Helper()
	p := NewPlugin()
	// Logger 由框架注入，测试环境手动设置
	p.Logger = slog.Default()
	v := viper.New()
	for k, val := range kv {
		v.Set(k, val)
	}
	if err := pluginconfig.Load(v, &p.cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := p.Start(context.Background(), v); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return p
}

func TestRepeatDefaultTrigger(t *testing.T) {
	p := startWithConfig(t, nil)
	if !p.enable.Load() {
		t.Fatal("配置键缺失时应默认启用")
	}
	if got := p.triggerThreshold(); got != defaultTriggerCount {
		t.Fatalf("默认触发条数 = %d, want %d", got, defaultTriggerCount)
	}

	for i := 1; i <= 2; i++ {
		if p.observe("g1", "hello") {
			t.Fatalf("第 %d 条相同消息不应触发复读", i)
		}
	}
	if !p.observe("g1", "hello") {
		t.Fatal("第 3 条相同消息应触发复读")
	}
	if p.observe("g1", "hello") {
		t.Fatal("已复读过，消息未变化时不应再次触发")
	}
}

func TestRepeatConfigurableTrigger(t *testing.T) {
	p := startWithConfig(t, map[string]any{"plugin.repeat.trigger_count": 2})
	if got := p.triggerThreshold(); got != 2 {
		t.Fatalf("触发条数 = %d, want 2", got)
	}
	if p.observe("g1", "x") {
		t.Fatal("第 1 条消息不应触发复读")
	}
	if !p.observe("g1", "x") {
		t.Fatal("第 2 条相同消息应触发复读")
	}
}

func TestRepeatIllegalTriggerFallsBack(t *testing.T) {
	p := startWithConfig(t, map[string]any{"plugin.repeat.trigger_count": 1})
	if got := p.triggerThreshold(); got != minTriggerCount {
		t.Fatalf("非法触发条数应回落到 %d，实际 %d", minTriggerCount, got)
	}
	if p.observe("g1", "x") {
		t.Fatal("第 1 条消息不应触发复读")
	}
	if !p.observe("g1", "x") {
		t.Fatal("第 2 条相同消息应触发复读")
	}
}

func TestRepeatDisabledByConfig(t *testing.T) {
	p := startWithConfig(t, map[string]any{"plugin.repeat.enable": false})
	if p.enable.Load() {
		t.Fatal("配置为关闭时插件应处于关闭状态")
	}
}

func TestRepeatEmptyRawMessageIgnored(t *testing.T) {
	p := startWithConfig(t, nil)
	for i := 0; i < 5; i++ {
		if p.observe("g1", "") {
			t.Fatal("空 RawMessage 不应触发复读")
		}
	}
	// 空消息不参与计数：随后的文字消息仍从 1 开始累计
	for i := 1; i <= 2; i++ {
		if p.observe("g1", "hi") {
			t.Fatalf("第 %d 条文字消息不应触发复读", i)
		}
	}
	if !p.observe("g1", "hi") {
		t.Fatal("空消息不应打断计数，第 3 条相同消息本应触发")
	}
}

func TestRepeatCountersIsolatedPerGroup(t *testing.T) {
	p := startWithConfig(t, nil)
	p.observe("g1", "same")
	p.observe("g2", "same")
	if p.observe("g1", "same") {
		t.Fatal("不同群的相同消息不应合并计数")
	}
	if !p.observe("g1", "same") {
		t.Fatal("同群第 3 条相同消息应触发复读")
	}
	if p.observe("g2", "same") {
		t.Fatal("g2 只收到 2 条，不应触发")
	}
}

func TestConfigSchemaRegistered(t *testing.T) {
	p := NewPlugin()
	if err := pluginconfig.RegisterStruct(p.ConfigSchema()); err != nil {
		t.Fatalf("RegisterStruct: %v", err)
	}
	fields := map[string]pluginconfig.Field{}
	for _, f := range pluginconfig.Fields() {
		fields[f.Key] = f
	}

	enable, ok := fields[enableConfigKey]
	if !ok {
		t.Fatalf("未注册配置键 %s", enableConfigKey)
	}
	if enable.Type != "bool" || enable.Default != true {
		t.Fatalf("开关字段注册信息异常: %+v", enable)
	}

	trigger, ok := fields["plugin.repeat.trigger_count"]
	if !ok {
		t.Fatal("未注册配置键 plugin.repeat.trigger_count")
	}
	if trigger.Type != "int" || trigger.Default != int64(defaultTriggerCount) {
		t.Fatalf("触发条数字段注册信息异常: %+v", trigger)
	}
}

// stubEditor 内存版配置中心，用于验证命令开关的回写。
type stubEditor struct {
	values map[string]any
	fail   bool
}

func (s *stubEditor) Get(key string) (any, bool) {
	v, ok := s.values[key]
	return v, ok
}

func (s *stubEditor) Set(key string, val any) error {
	if s.fail {
		return errors.New("write failed")
	}
	s.values[key] = val
	return nil
}

func (s *stubEditor) Delete(key string) bool {
	_, ok := s.values[key]
	delete(s.values, key)
	return ok
}

func (s *stubEditor) All() map[string]any { return s.values }

func TestSetEnablePersistsToConfig(t *testing.T) {
	p := startWithConfig(t, nil)
	editor := &stubEditor{values: map[string]any{}}
	p.ConfigEditor = editor

	p.setEnable(false)
	if p.enable.Load() {
		t.Fatal("关闭后运行时状态应为关闭")
	}
	if got, _ := editor.Get(enableConfigKey); got != false {
		t.Fatalf("配置中心中的开关 = %v, want false", got)
	}
	if p.cfg.Enable {
		t.Fatal("结构体中的开关应同步为 false")
	}

	// 写配置失败时仅记录日志，运行时开关仍然生效
	p.ConfigEditor = &stubEditor{values: map[string]any{}, fail: true}
	p.setEnable(true)
	if !p.enable.Load() {
		t.Fatal("写配置失败不应影响本次运行的开关")
	}
}
