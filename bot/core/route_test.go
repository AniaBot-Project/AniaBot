package core

import (
	"testing"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/spf13/viper"
)

// fakeAdapter 最小 Adapter 实现，仅用于测试路由。
type fakeAdapter struct {
	name     string
	platform string
}

func (f *fakeAdapter) Name() string                      { return f.name }
func (f *fakeAdapter) Platform() string                  { return f.platform }
func (f *fakeAdapter) SetTrigger(adapter.TriggerWrapper) {}
func (f *fakeAdapter) Serve(*viper.Viper)                {}
func (f *fakeAdapter) SendGroupMsg(message.QID, msgchain.GroupChain) (message.QID, bool) {
	return "", true
}
func (f *fakeAdapter) SendFriendMsg(message.QID, msgchain.FriendChain) (message.QID, bool) {
	return "", true
}
func (f *fakeAdapter) GetMsgDetail(message.QID) (*message.Message, bool)     { return nil, false }
func (f *fakeAdapter) GetGroupDetail(message.QID) (*message.GroupInfo, bool) { return nil, false }
func (f *fakeAdapter) GetGroupMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}
func (f *fakeAdapter) GetFriendMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}

// TestRouteIDPrefix 验证多适配器按统一 ID 前缀路由：QQ qq: 前缀路由到 NapCat，
// 飞书 fs: 前缀路由到飞书适配器，旧版裸数字 QQ ID 仍兼容回退到 QQ。
func TestRouteIDPrefix(t *testing.T) {
	a := &AniaBot{}
	qq := &fakeAdapter{name: "napcat", platform: "qq"}
	fs := &fakeAdapter{name: "feishu", platform: "feishu"}
	a.adapters = []*adapterEntry{
		{def: adapter.Definition{Name: "napcat", Platform: "qq", IDPrefix: message.QQIDPrefix}, adapter: qq},
		{def: adapter.Definition{Name: "feishu", Platform: "feishu", IDPrefix: "fs:"}, adapter: fs},
	}

	if got := a.route("qq:123456789"); got != qq {
		t.Fatalf("qq: 前缀应路由到 QQ 适配器，got %v", got.Name())
	}
	if got := a.route("fs:oc_123456"); got != fs {
		t.Fatalf("fs: 前缀应路由到飞书适配器，got %v", got.Name())
	}
	if got := a.route("123456789"); got != qq {
		t.Fatalf("旧版无前缀数字 ID 应兼容回退到 QQ 适配器，got %v", got.Name())
	}
}

// TestRouteEmptyAdapter 无适配器时路由返回 nil（发送接口优雅失败）。
func TestRouteEmptyAdapter(t *testing.T) {
	a := &AniaBot{}
	if got := a.route("123"); got != nil {
		t.Fatalf("无适配器时应返回 nil")
	}
	if _, ok := a.SendGroupMsg("123", nil); ok {
		t.Fatalf("无适配器时发送应失败")
	}
}

// forwardTestAdapter 支持群聊合并转发的假 QQ 适配器：只覆写合并转发并记录调用，
// 其余 QQExt 方法由嵌入的接口桩满足（测试不会调用）。
type forwardTestAdapter struct {
	fakeAdapter
	adapter.QQExt
	calls int
}

func (f *forwardTestAdapter) SendGroupForwardMsg(message.QID, msgchain.GroupForwardChain) (message.QID, bool) {
	f.calls++
	return "fw:1", true
}

// TestSendGroupForwardMsgRouting 验证 core 的 bot.GroupForwardSender 实现按群 ID
// 前缀路由：QQ 适配器（含 QQExt）转发成功，非 QQ 适配器返回 false（调用方退化）。
func TestSendGroupForwardMsgRouting(t *testing.T) {
	a := &AniaBot{}
	qq := &forwardTestAdapter{fakeAdapter: fakeAdapter{name: "napcat", platform: "qq"}}
	fs := &fakeAdapter{name: "feishu", platform: "feishu"}
	a.adapters = []*adapterEntry{
		{def: adapter.Definition{Name: "napcat", Platform: "qq", IDPrefix: message.QQIDPrefix}, adapter: qq},
		{def: adapter.Definition{Name: "feishu", Platform: "feishu", IDPrefix: "fs:"}, adapter: fs},
	}

	fwd := msgchain.Builder().GroupForward()
	fwd.Message(message.FromUint64(1), "AI助手", msgchain.Builder().Group().Text("一").Build())
	if _, ok := a.SendGroupForwardMsg("qq:123", fwd.Build()); !ok || qq.calls != 1 {
		t.Fatalf("QQ 群应路由到适配器的合并转发, ok=%v calls=%d", ok, qq.calls)
	}
	if _, ok := a.SendGroupForwardMsg("fs:oc_1", fwd.Build()); ok || qq.calls != 1 {
		t.Fatalf("非 QQ 适配器不支持合并转发，应返回 false 且不误发, ok=%v calls=%d", ok, qq.calls)
	}
}
