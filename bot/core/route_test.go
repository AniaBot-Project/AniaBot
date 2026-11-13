package core

import (
	"testing"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/bot"
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

// mediaRefreshAdapter 支持媒体地址续期的假 QQ 适配器：记录最后一次调用参数。
type mediaRefreshAdapter struct {
	fakeAdapter
	adapter.MediaRefresherExt
	calls                         int
	kind, fileID, groupID, userID string
}

func (m *mediaRefreshAdapter) RefreshMediaURL(kind, fileId, groupId, userId string) (string, bool) {
	m.calls++
	m.kind, m.fileID, m.groupID, m.userID = kind, fileId, groupId, userId
	return "https://cdn.example.com/fresh?rkey=1", true
}

// TestRefreshMediaURLRouting 验证 core 的 bot.MediaRefresher 实现按会话 ID 前缀
// 路由：群消息用群 ID，私聊用用户 ID；平台不支持续期时返回 false。
func TestRefreshMediaURLRouting(t *testing.T) {
	a := &AniaBot{}
	qq := &mediaRefreshAdapter{fakeAdapter: fakeAdapter{name: "napcat", platform: "qq"}}
	fs := &fakeAdapter{name: "feishu", platform: "feishu"}
	a.adapters = []*adapterEntry{
		{def: adapter.Definition{Name: "napcat", Platform: "qq", IDPrefix: message.QQIDPrefix}, adapter: qq},
		{def: adapter.Definition{Name: "feishu", Platform: "feishu", IDPrefix: "fs:"}, adapter: fs},
	}

	got, ok := a.RefreshMediaURL(message.SegmentImage, "fid-1", "qq:123", "qq:456")
	if !ok || got != "https://cdn.example.com/fresh?rkey=1" || qq.calls != 1 ||
		qq.kind != message.SegmentImage || qq.fileID != "fid-1" || qq.groupID != "qq:123" {
		t.Fatalf("群消息续期路由错误: %q ok=%v calls=%d params=%+v", got, ok, qq.calls, qq)
	}

	// 私聊：群 ID 为空时用用户 ID 路由
	if _, ok := a.RefreshMediaURL(message.SegmentImage, "fid-2", "", "qq:456"); !ok || qq.calls != 2 {
		t.Fatalf("私聊续期路由错误: ok=%v calls=%d", ok, qq.calls)
	}

	// 非 QQ 平台不具备该能力
	if _, ok := a.RefreshMediaURL(message.SegmentImage, "fid-3", "fs:oc_1", ""); ok {
		t.Fatal("飞书适配器不应支持媒体地址续期")
	}
	if _, ok := a.RefreshMediaURL(message.SegmentImage, "fid-4", "", ""); ok {
		t.Fatal("缺少会话 ID 时不应成功")
	}
}

// memberListAdapter 支持群成员枚举的假 QQ 适配器：只覆写成员列表并记录调用，
// 其余 QQExt 方法由嵌入的接口桩满足（测试不会调用）。
type memberListAdapter struct {
	fakeAdapter
	adapter.QQExt
	calls   int
	groupID message.QID
}

func (m *memberListAdapter) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	m.calls++
	m.groupID = groupId
	list := []message.GroupUserInfo{{UserID: "qq:1", Nickname: "小明"}}
	return &list, true
}

// core 需实现 bot.GroupMembers：面板消息日志的 @ 昵称还原依赖它。
var _ bot.GroupMembers = (*AniaBot)(nil)

// TestGetGroupMemberListRouting 验证 core 的 bot.GroupMembers 实现按群 ID 前缀
// 路由到适配器：QQ 适配器（含 QQExt）返回成员表，非 QQ 适配器返回 false。
func TestGetGroupMemberListRouting(t *testing.T) {
	a := &AniaBot{}
	qq := &memberListAdapter{fakeAdapter: fakeAdapter{name: "napcat", platform: "qq"}}
	fs := &fakeAdapter{name: "feishu", platform: "feishu"}
	a.adapters = []*adapterEntry{
		{def: adapter.Definition{Name: "napcat", Platform: "qq", IDPrefix: message.QQIDPrefix}, adapter: qq},
		{def: adapter.Definition{Name: "feishu", Platform: "feishu", IDPrefix: "fs:"}, adapter: fs},
	}

	members, ok := a.GetGroupMemberList("qq:123", false)
	if !ok || members == nil || len(*members) != 1 || qq.calls != 1 || qq.groupID != "qq:123" {
		t.Fatalf("QQ 群成员列表路由错误: ok=%v members=%v calls=%d group=%q", ok, members, qq.calls, qq.groupID)
	}
	if _, ok := a.GetGroupMemberList("fs:oc_1", false); ok || qq.calls != 1 {
		t.Fatalf("非 QQ 适配器不支持群成员枚举，应返回 false, ok=%v calls=%d", ok, qq.calls)
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
	fwd.Message(message.FromUint64(1), "AniaBot", msgchain.Builder().Group().Text("一").Build())
	if _, ok := a.SendGroupForwardMsg("qq:123", fwd.Build()); !ok || qq.calls != 1 {
		t.Fatalf("QQ 群应路由到适配器的合并转发, ok=%v calls=%d", ok, qq.calls)
	}
	if _, ok := a.SendGroupForwardMsg("fs:oc_1", fwd.Build()); ok || qq.calls != 1 {
		t.Fatalf("非 QQ 适配器不支持合并转发，应返回 false 且不误发, ok=%v calls=%d", ok, qq.calls)
	}
}
