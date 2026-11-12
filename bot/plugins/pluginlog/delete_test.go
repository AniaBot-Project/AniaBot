package pluginlog

import "testing"

// TestMsgLogDelete 面板右键删除：插件把删除交给记录器，删除后分页与会话列表都
// 不再返回该条；重复删除与记录器未初始化时返回 false。
func TestMsgLogDelete(t *testing.T) {
	p, id := newMediaTestPlugin(t, nil)

	if page := p.MsgLogPage(10, 0); len(page) != 1 || page[0].ID != id {
		t.Fatalf("删除前分页异常: %+v", page)
	}
	if !p.MsgLogDelete(id) {
		t.Fatal("删除应成功")
	}
	if page := p.MsgLogPage(10, 0); len(page) != 0 {
		t.Fatalf("删除后不应再返回该条: %+v", page)
	}
	if p.MsgLogDelete(id) {
		t.Fatal("重复删除应返回 false")
	}
	if sessions := p.MsgLogSessions(); len(sessions) != 0 {
		t.Fatalf("删除后会话应消失: %+v", sessions)
	}

	// Start 之前记录器为空时安全返回 false，不 panic
	p.recorder = nil
	if p.MsgLogDelete(id) {
		t.Fatal("记录器未初始化时应返回 false")
	}
}
