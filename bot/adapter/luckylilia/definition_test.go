package luckylilia

import (
	"testing"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

// fakeBaseBot 最小 bot.Bot 实现，仅用于测试包装器。
type fakeBaseBot struct{}

func (f *fakeBaseBot) SendGroupMsg(message.QID, msgchain.GroupChain) (message.QID, bool) {
	return "", true
}
func (f *fakeBaseBot) SendFriendMsg(message.QID, msgchain.FriendChain) (message.QID, bool) {
	return "", true
}
func (f *fakeBaseBot) GetMsgDetail(message.QID) (*message.Message, bool)     { return nil, false }
func (f *fakeBaseBot) GetGroupDetail(message.QID) (*message.GroupInfo, bool) { return nil, false }
func (f *fakeBaseBot) GetGroupMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}
func (f *fakeBaseBot) GetFriendMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}
func (f *fakeBaseBot) GetPluginList() []plugininfo.PluginInfo { return nil }
func (f *fakeBaseBot) Stop()                                  {}
func (f *fakeBaseBot) Go(name string, fn func())              {}
func (f *fakeBaseBot) TrackWork(label string) func()          { return func() {} }

// plainAdapter 不具备 QQ 能力的适配器（模拟飞书等未来平台）。
type plainAdapter struct{}

func (p *plainAdapter) Name() string                      { return "plain" }
func (p *plainAdapter) Platform() string                  { return "other" }
func (p *plainAdapter) SetTrigger(adapter.TriggerWrapper) {}
func (p *plainAdapter) Serve(*viper.Viper)                {}
func (p *plainAdapter) SendGroupMsg(message.QID, msgchain.GroupChain) (message.QID, bool) {
	return "", true
}
func (p *plainAdapter) SendFriendMsg(message.QID, msgchain.FriendChain) (message.QID, bool) {
	return "", true
}
func (p *plainAdapter) GetMsgDetail(message.QID) (*message.Message, bool)     { return nil, false }
func (p *plainAdapter) GetGroupDetail(message.QID) (*message.GroupInfo, bool) { return nil, false }
func (p *plainAdapter) GetGroupMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}
func (p *plainAdapter) GetFriendMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}

// TestQQBotWrapper 验证 bot 外观包装机制：Luckylilia 适配器事件可断言为 bot.QQ，
// 不具备 QQ 能力的适配器（如飞书）则断言失败——插件据此优雅退化。
func TestQQBotWrapper(t *testing.T) {
	base := &fakeBaseBot{}

	// Luckylilia（实现 QQExt）→ 包装后断言 bot.QQ 成功
	wrapped := adapter.WrapBot(base, NewLuckyliliaWebSocketAdapter())
	if _, ok := wrapped.(bot.QQ); !ok {
		t.Fatal("Luckylilia 适配器包装后的 bot 应可断言为 bot.QQ")
	}

	// 非 QQ 适配器 → 断言失败（原样返回 base）
	if _, ok := adapter.WrapBot(base, &plainAdapter{}).(bot.QQ); ok {
		t.Fatal("非 QQ 适配器包装后的 bot 不应断言为 bot.QQ")
	}
}

// TestRegisterDefinition 验证 init() 注册的定义：名称/平台/ID 前缀与工厂行为。
func TestRegisterDefinition(t *testing.T) {
	var def *adapter.Definition
	for _, d := range adapter.Definitions() {
		if d.Name == "luckylilia" {
			def = &d
			break
		}
	}
	if def == nil {
		t.Fatal("luckylilia 适配器应已注册")
	}
	if def.Platform != "qq" {
		t.Fatalf("platform = %q, want qq", def.Platform)
	}
	if def.IDPrefix != idPrefix {
		t.Fatalf("idPrefix = %q, want %q", def.IDPrefix, idPrefix)
	}
	a, err := def.New(viper.New())
	if err != nil {
		t.Fatalf("New 不应返回错误: %v", err)
	}
	if a.Name() != "luckylilia" || a.Platform() != "qq" {
		t.Fatalf("adapter = %s/%s, want luckylilia/qq", a.Name(), a.Platform())
	}
}

// TestNewLuckyliliaHttpAdapterClientReady 回归：httpClient 必须在构造时就绪，
// Serve 尚未运行（如首次启动等待设置向导）时插件调用发送接口应优雅失败而非 nil panic。
func TestNewLuckyliliaHttpAdapterClientReady(t *testing.T) {
	a, ok := NewLuckyliliaHttpAdapter().(*luckyliliaHttpAdapter)
	if !ok {
		t.Fatal("NewLuckyliliaHttpAdapter 应返回 *luckyliliaHttpAdapter")
	}
	if a.httpClient == nil {
		t.Fatal("httpClient 应在构造时就绪，否则 Serve 前的发送调用会 nil panic")
	}
	// Serve 前调用 postAndCheck：请求会失败（无 baseUrl），但不应 panic
	if a.postAndCheck("http://127.0.0.1:0/ping", nil, nil) {
		t.Fatal("对不可达地址的请求应返回 false")
	}
}
