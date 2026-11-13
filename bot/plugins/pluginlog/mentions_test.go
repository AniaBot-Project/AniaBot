package pluginlog

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/core"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeMembersBot 群成员枚举的测试桩：嵌入 bot.Bot 满足公共外观，只覆写
// GetGroupMemberList（其余方法被调用即 panic，测试不会触发）。
type fakeMembersBot struct {
	bot.Bot
	mu      sync.Mutex
	calls   int
	fail    bool
	members []message.GroupUserInfo
}

func (f *fakeMembersBot) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fail {
		return nil, false
	}
	list := append([]message.GroupUserInfo(nil), f.members...)
	return &list, true
}

func (f *fakeMembersBot) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newMentionTestPlugin 构造带记录器与 bot 的插件实例，并写入一条含 @ 的群消息日志。
func newMentionTestPlugin(t *testing.T, b bot.Bot, text string) *LogPlugin {
	t.Helper()
	p := NewPlugin()
	p.Logger = slog.Default()
	p.recorder = msglog.New(core.NewAniaMemoryStorage(slog.Default()), 0)
	p.bot = b
	p.recorder.Add(msglog.Entry{
		Type:    msglog.TypeGroup,
		GroupId: "qq:123",
		UserId:  "qq:456",
		Text:    text,
	})
	return p
}

// TestRewriteMentions [at:id:…] 占位按昵称表还原：命中的换成 [at:昵称]，
// 未命中保留 ID；@全体成员 / @我 不受影响。
func TestRewriteMentions(t *testing.T) {
	names := map[string]string{"qq:123": "小明"}
	cases := map[string]string{
		"[at:id:qq:123] 你好":                       "[at:小明] 你好",
		"[at:id:qq:999] 你好":                       "[at:id:qq:999] 你好",
		"[at:id:123] 旧格式裸数字 ID":                   "[at:小明] 旧格式裸数字 ID",
		"[at:全体成员] [at我]":                         "[at:全体成员] [at我]",
		"引用里也要还原：\n  [at:id:qq:123] 收到\n</reply>": "引用里也要还原：\n  [at:小明] 收到\n</reply>",
	}
	for in, want := range cases {
		if got := rewriteMentions(in, names); got != want {
			t.Errorf("rewriteMentions(%q) = %q, want %q", in, got, want)
		}
	}
	// 没有昵称表时完全不动文本
	if got := rewriteMentions("[at:id:qq:123]", nil); got != "[at:id:qq:123]" {
		t.Errorf("空昵称表应原样返回, got %q", got)
	}
}

// TestSanitizeMentionName 昵称里的方括号 / 换行会破坏 [at:…] 占位与排版，
// 需要清理并限长。
func TestSanitizeMentionName(t *testing.T) {
	if got := sanitizeMentionName("  [开发]小明\n二号  "); got != "【开发】小明 二号" {
		t.Errorf("sanitizeMentionName = %q", got)
	}
	long := strings.Repeat("长", mentionNameMaxRunes+10)
	if got := strings.Count(sanitizeMentionName(long), "长"); got != mentionNameMaxRunes {
		t.Errorf("昵称长度未限幅: %d", got)
	}
	if got := sanitizeMentionName("   "); got != "" {
		t.Errorf("空白昵称应返回空串, got %q", got)
	}
}

// TestMentionNamerCache 群成员表按群缓存：第二次取用缓存、失败后冷却期内不重试。
func TestMentionNamerCache(t *testing.T) {
	fb := &fakeMembersBot{members: []message.GroupUserInfo{
		{UserID: "qq:123", Nickname: "小明"},
		{UserID: "qq:456", Card: "群名片"},
	}}
	var n mentionNamer
	names := n.names(fb, "qq:1", time.Second)
	if names["qq:123"] != "小明" || names["qq:456"] != "群名片" {
		t.Fatalf("昵称表错误: %+v", names)
	}
	if fb.callCount() != 1 {
		t.Fatalf("首次取用应查一次, calls=%d", fb.callCount())
	}
	// 缓存有效期内不再打扰协议端
	if got := n.names(fb, "qq:1", time.Second); got["qq:123"] != "小明" {
		t.Fatalf("缓存命中失败: %+v", got)
	}
	if fb.callCount() != 1 {
		t.Fatalf("缓存期内不应重复查询, calls=%d", fb.callCount())
	}

	// 查询失败：返回空表，冷却期内不再重试
	fail := &fakeMembersBot{fail: true}
	var n2 mentionNamer
	if got := n2.names(fail, "qq:2", time.Second); len(got) != 0 {
		t.Fatalf("失败时应返回空表: %+v", got)
	}
	if got := n2.names(fail, "qq:2", time.Second); len(got) != 0 {
		t.Fatalf("失败冷却期内仍应返回空表: %+v", got)
	}
	if fail.callCount() != 1 {
		t.Fatalf("失败冷却期内不应重试, calls=%d", fail.callCount())
	}
}

// slowMembersBot 首次查询卡在 release 上，用于验证并发请求会等同一次查询
// 而不是重复打平台（面板先拉会话列表、再拉消息的常见顺序）。
type slowMembersBot struct {
	fakeMembersBot
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *slowMembersBot) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.fakeMembersBot.GetGroupMemberList(groupId, noCache)
}

// TestMentionNamerWaitsInflight 已有查询在跑时，后续请求在预算内等它出结果，
// 且不会重复发起查询。
func TestMentionNamerWaitsInflight(t *testing.T) {
	fb := &slowMembersBot{
		fakeMembersBot: fakeMembersBot{members: []message.GroupUserInfo{{UserID: "qq:123", Nickname: "小明"}}},
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	var n mentionNamer
	if got := n.names(fb, "qq:1", 0); got != nil {
		t.Fatalf("首次调用不等待，应返回空表: %+v", got)
	}
	<-fb.started
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(fb.release)
	}()
	names := n.names(fb, "qq:1", 2*time.Second)
	if names["qq:123"] != "小明" {
		t.Fatalf("应等到进行中的查询结果: %+v", names)
	}
	if fb.callCount() != 1 {
		t.Fatalf("不应重复发起查询, calls=%d", fb.callCount())
	}
}

// TestMsgLogReadLocalizesMentions 面板读取日志时把 @ 还原成昵称，但留存的
// 原始日志仍是 ID 占位（AI 与后续读取看到的都是稳定 ID）。
func TestMsgLogReadLocalizesMentions(t *testing.T) {
	fb := &fakeMembersBot{members: []message.GroupUserInfo{{UserID: "qq:123", Nickname: "小明"}}}
	p := newMentionTestPlugin(t, fb, "[at:id:qq:123] 你好")

	items := p.MsgLogPage(10, 0)
	if len(items) != 1 || items[0].Text != "[at:小明] 你好" {
		t.Fatalf("面板日志未还原昵称: %+v", items)
	}
	// 会话预览（侧栏）同样还原
	sessions := p.MsgLogSessions()
	if len(sessions) != 1 || !strings.Contains(sessions[0].LastText, "[at:小明]") {
		t.Fatalf("会话预览未还原昵称: %+v", sessions)
	}
	// 按会话读取同样还原
	page := p.MsgLogSessionPage("g:qq:123", 10, 0, 0)
	if len(page) != 1 || page[0].Text != "[at:小明] 你好" {
		t.Fatalf("按会话读取未还原昵称: %+v", page)
	}
	// 留存内容不变
	stored := p.recorder.Recent(10)
	if len(stored) != 1 || stored[0].Text != "[at:id:qq:123] 你好" {
		t.Fatalf("留存日志不应被改写: %+v", stored)
	}
}

// TestMsgLogReadKeepsIDWithoutMembers 平台不支持群成员枚举（或未连接）时，
// 日志保持 ID 占位，不影响面板展示。
func TestMsgLogReadKeepsIDWithoutMembers(t *testing.T) {
	p := newMentionTestPlugin(t, nil, "[at:id:qq:123] 你好")
	items := p.MsgLogPage(10, 0)
	if len(items) != 1 || items[0].Text != "[at:id:qq:123] 你好" {
		t.Fatalf("无群成员能力时应保留 ID: %+v", items)
	}
}
