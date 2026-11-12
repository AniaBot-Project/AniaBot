package pluginlog

import (
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
)

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

// TestMsgLogDeleteSession 面板右键会话「删除会话记录」：插件把会话删除交给
// 记录器并返回删除条数；会话清空后侧栏不再出现。
func TestMsgLogDeleteSession(t *testing.T) {
	p, _ := newMediaTestPlugin(t, nil)
	p.recorder.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:123", UserId: "qq:1", Text: "第二条"})

	if n := p.MsgLogDeleteSession("g:qq:123"); n != 2 {
		t.Fatalf("删除条数 = %d, want 2", n)
	}
	if page := p.MsgLogPage(10, 0); len(page) != 0 {
		t.Fatalf("会话清空后不应再返回记录: %+v", page)
	}
	if sessions := p.MsgLogSessions(); len(sessions) != 0 {
		t.Fatalf("会话清空后应从侧栏消失: %+v", sessions)
	}
	if n := p.MsgLogDeleteSession("g:qq:123"); n != 0 {
		t.Fatalf("重复删除应返回 0, got %d", n)
	}
	if n := p.MsgLogDeleteSession(""); n != 0 {
		t.Fatalf("空会话键应返回 0, got %d", n)
	}

	// Start 之前记录器为空时安全返回 0，不 panic
	p.recorder = nil
	if n := p.MsgLogDeleteSession("g:qq:123"); n != 0 {
		t.Fatalf("记录器未初始化时应返回 0, got %d", n)
	}
}
