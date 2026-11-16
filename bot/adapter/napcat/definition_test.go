package napcat

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

// forwardBaseBot 额外实现合并转发能力的 base bot：QQ 平台外观应把合并转发
// 交给它（core 的发送事件分发入口），而不是直接调用适配器。
type forwardBaseBot struct {
	fakeBaseBot
	groupCalls  int
	friendCalls int
}

func (f *forwardBaseBot) SendGroupForwardMsg(message.QID, msgchain.GroupForwardChain) (message.QID, bool) {
	f.groupCalls++
	return "base:fw", true
}

func (f *forwardBaseBot) SendFriendForwardMsg(message.QID, msgchain.FriendForwardChain) (message.QID, bool) {
	f.friendCalls++
	return "base:fw", true
}

// TestQQBotForwardDelegation 验证 QQ 平台外观的合并转发优先走 core 的
// bot.GroupForwardSender / bot.FriendForwardSender 实现（发送事件据此分发）；
// base 不支持时才回退到适配器。
func TestQQBotForwardDelegation(t *testing.T) {
	base := &forwardBaseBot{}
	wrapped := adapter.WrapBot(base, NewNapcatWebSocketAdapter())
	qb, ok := wrapped.(bot.QQ)
	if !ok {
		t.Fatal("NapCat 适配器包装后的 bot 应可断言为 bot.QQ")
	}

	fwd := msgchain.Builder().GroupForward()
	if _, ok := qb.SendGroupForwardMsg("qq:1", fwd.Build()); !ok || base.groupCalls != 1 {
		t.Fatalf("群合并转发应交给 core（base）: ok=%v calls=%d", ok, base.groupCalls)
	}
	if _, ok := qb.SendFriendForwardMsg("qq:2", msgchain.Builder().FriendForward().Build()); !ok || base.friendCalls != 1 {
		t.Fatalf("私聊合并转发应交给 core（base）: ok=%v calls=%d", ok, base.friendCalls)
	}
}

// TestQQBotWrapper 验证 bot 外观包装机制：NapCat 适配器事件可断言为 bot.QQ，
// 不具备 QQ 能力的适配器（如飞书）则断言失败——插件据此优雅退化。
func TestQQBotWrapper(t *testing.T) {
	base := &fakeBaseBot{}

	// NapCat（实现 QQExt）→ 包装后断言 bot.QQ 成功
	wrapped := adapter.WrapBot(base, NewNapcatWebSocketAdapter())
	if _, ok := wrapped.(bot.QQ); !ok {
		t.Fatal("NapCat 适配器包装后的 bot 应可断言为 bot.QQ")
	}

	// 非 QQ 适配器 → 断言失败（原样返回 base）
	if _, ok := adapter.WrapBot(base, &plainAdapter{}).(bot.QQ); ok {
		t.Fatal("非 QQ 适配器包装后的 bot 不应断言为 bot.QQ")
	}
}
