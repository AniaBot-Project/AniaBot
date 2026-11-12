package msglog_test

import (
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/core"
	"log/slog"
)

func newStore() *core.AniaMemoryStorage {
	return core.NewAniaMemoryStorage(slog.Default())
}

func TestAddAndRecent(t *testing.T) {
	r := msglog.New(newStore(), 0)
	r.Add(msglog.Entry{Type: msglog.TypeGroup, Text: "第一条"})
	r.Add(msglog.Entry{Type: msglog.TypeFriend, Text: "第二条"})
	r.Add(msglog.Entry{Type: msglog.TypeNotice, Title: "戳一戳", Text: "第三条"})

	entries := r.Recent(0)
	if len(entries) != 3 {
		t.Fatalf("期望 3 条日志，实际 %d", len(entries))
	}
	// 新条目在前
	if entries[0].Text != "第三条" || entries[2].Text != "第一条" {
		t.Fatalf("顺序错误: %+v", entries)
	}
	// ID 单调递增
	for i, e := range entries {
		if e.ID != uint64(3-i) {
			t.Fatalf("ID 不连续: %+v", entries)
		}
	}
	if entries[0].Time.IsZero() {
		t.Fatal("Time 未自动填充")
	}

	if got := r.Recent(2); len(got) != 2 || got[0].Text != "第三条" {
		t.Fatalf("Recent(2) 错误: %+v", got)
	}
}

func TestMaxEntries(t *testing.T) {
	r := msglog.New(newStore(), 3)
	for range 5 {
		r.Add(msglog.Entry{Type: msglog.TypeGroup, Text: "msg"})
	}
	entries := r.Recent(0)
	if len(entries) != 3 {
		t.Fatalf("容量上限 3，实际 %d", len(entries))
	}
	// 最旧的两条被淘汰，保留 ID 3/4/5
	if entries[0].ID != 5 || entries[2].ID != 3 {
		t.Fatalf("淘汰错误: %+v", entries)
	}
}

func TestPage(t *testing.T) {
	r := msglog.New(newStore(), 10)
	for _, text := range []string{"a", "b", "c", "d", "e"} {
		r.Add(msglog.Entry{Type: msglog.TypeGroup, Text: text})
	}

	// 第一页：最新两条
	p1 := r.Page(2, 0)
	if len(p1) != 2 || p1[0].Text != "e" || p1[1].Text != "d" {
		t.Fatalf("page1 wrong: %+v", p1)
	}
	// 第二页：以 p1 最旧一条为游标
	p2 := r.Page(2, p1[1].ID)
	if len(p2) != 2 || p2[0].Text != "c" || p2[1].Text != "b" {
		t.Fatalf("page2 wrong: %+v", p2)
	}
	// 第三页：只剩一条
	p3 := r.Page(2, p2[1].ID)
	if len(p3) != 1 || p3[0].Text != "a" {
		t.Fatalf("page3 wrong: %+v", p3)
	}
	// 游标超过最新 ID 时从最新开始
	p4 := r.Page(2, 999)
	if len(p4) != 2 || p4[0].Text != "e" {
		t.Fatalf("page with future cursor wrong: %+v", p4)
	}
}

func TestPageWithEviction(t *testing.T) {
	r := msglog.New(newStore(), 3)
	for _, text := range []string{"a", "b", "c", "d", "e"} {
		r.Add(msglog.Entry{Type: msglog.TypeGroup, Text: text})
	}
	// 容量 3，只剩 e/d/c；游标指向已淘汰的 b（ID=2）时换算偏移越界，应为空
	if page := r.Page(2, 2); len(page) != 0 {
		t.Fatalf("evicted cursor should yield empty page, got %+v", page)
	}
	// 游标指向现存最旧的 c（ID=3）时，更旧的已被淘汰，应为空
	if page := r.Page(2, 3); len(page) != 0 {
		t.Fatalf("oldest cursor should yield empty page, got %+v", page)
	}
}

// TestGet 按 ID 取单条日志（面板媒体地址续期依赖它拿到原消息 ID / 文件 ID）；
// 已淘汰的 ID 与不存在的 ID 都应返回 false，避免拿到错位的别条日志。
func TestGet(t *testing.T) {
	r := msglog.New(newStore(), 3)
	for _, text := range []string{"a", "b", "c", "d", "e"} {
		r.Add(msglog.Entry{Type: msglog.TypeGroup, Text: text, MessageId: "qq:" + text})
	}

	e, ok := r.Get(5)
	if !ok || e.Text != "e" || e.MessageId != "qq:e" {
		t.Fatalf("Get(5) = %+v, %v", e, ok)
	}
	if e, ok := r.Get(3); !ok || e.Text != "c" {
		t.Fatalf("Get(3) = %+v, %v", e, ok)
	}
	for _, id := range []uint64{0, 1, 2, 99} {
		if _, ok := r.Get(id); ok {
			t.Fatalf("Get(%d) 不应命中", id)
		}
	}
}

func TestSessionKey(t *testing.T) {
	cases := []struct {
		name string
		e    msglog.Entry
		want string
	}{
		{"群消息", msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:123", UserId: "qq:1"}, "g:qq:123"},
		{"群通知并入群会话", msglog.Entry{Type: msglog.TypeNotice, GroupId: "qq:123", UserId: "qq:1"}, "g:qq:123"},
		{"好友消息", msglog.Entry{Type: msglog.TypeFriend, UserId: "qq:2"}, "f:qq:2"},
		{"好友通知并入私聊", msglog.Entry{Type: msglog.TypeNotice, UserId: "qq:2"}, "f:qq:2"},
		{"系统通知", msglog.Entry{Type: msglog.TypeNotice}, "n:system"},
	}
	for _, c := range cases {
		if got := c.e.SessionKey(); got != c.want {
			t.Errorf("%s: SessionKey() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSessions(t *testing.T) {
	r := msglog.New(newStore(), 0)
	r.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:1", UserId: "qq:10", Nickname: "小明", Text: "早上好"})
	r.Add(msglog.Entry{Type: msglog.TypeFriend, UserId: "qq:20", Nickname: "小红", Text: "在吗"})
	r.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:1", UserId: "qq:11", Nickname: "阿白", Text: "图片来了",
		Media: []msglog.Media{{Kind: msglog.MediaImage, URL: "https://example.com/a.png"}}})
	r.Add(msglog.Entry{Type: msglog.TypeNotice, GroupId: "qq:1", UserId: "qq:11", Title: "群成员增加", Text: "阿白 加入了群聊"})
	r.Add(msglog.Entry{Type: msglog.TypeNotice, Title: "新好友", Text: "有人添加了好友"})

	sessions := r.Sessions()
	if len(sessions) != 3 {
		t.Fatalf("期望 3 个会话（群/私聊/系统），实际 %d: %+v", len(sessions), sessions)
	}
	// 按最近一条日志倒序：系统通知 → 群 → 私聊
	if sessions[0].Key != "n:system" || sessions[1].Key != "g:qq:1" || sessions[2].Key != "f:qq:20" {
		t.Fatalf("会话顺序错误: %+v", sessions)
	}
	group := sessions[1]
	if group.Kind != msglog.SessionGroup || group.GroupId != "qq:1" || group.UserId != "" {
		t.Fatalf("群会话字段错误: %+v", group)
	}
	// 群通知与群消息合并：两条消息 + 一条通知
	if group.Count != 3 {
		t.Fatalf("群会话条数 = %d, want 3", group.Count)
	}
	if group.LastKind != msglog.TypeNotice || group.Title != "群成员增加" || group.Name != "阿白" {
		t.Fatalf("群会话最近一条摘要错误: %+v", group)
	}
	if sessions[2].Name != "小红" || sessions[2].Kind != msglog.SessionFriend {
		t.Fatalf("私聊会话摘要错误: %+v", sessions[2])
	}

	// 新增一条图片消息后，最后一条媒体类型应更新
	r.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:1", UserId: "qq:12", Nickname: "星野", Text: "看这张",
		Media: []msglog.Media{{Kind: msglog.MediaImage, URL: "https://example.com/b.png"}}})
	sessions = r.Sessions()
	if sessions[0].Key != "g:qq:1" || sessions[0].LastMedia != msglog.MediaImage || sessions[0].Name != "星野" {
		t.Fatalf("增加消息后摘要未更新: %+v", sessions[0])
	}
}

func TestPageSession(t *testing.T) {
	r := msglog.New(newStore(), 20)
	// 两个群交错发言
	for i := 0; i < 6; i++ {
		group := "qq:1"
		if i%2 == 1 {
			group = "qq:2"
		}
		r.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: group, UserId: "qq:9", Text: "m" + string(rune('a'+i))})
	}
	r.Add(msglog.Entry{Type: msglog.TypeNotice, GroupId: "qq:1", Title: "戳一戳", Text: "通知"})

	// 群 1 共有 4 条（3 条消息 + 1 条通知）
	first := r.PageSession("g:qq:1", 2, 0, 0)
	if len(first) != 2 || first[0].Text != "通知" || first[1].Text != "me" {
		t.Fatalf("第一页错误: %+v", first)
	}
	second := r.PageSession("g:qq:1", 2, first[1].ID, 0)
	if len(second) != 2 || second[0].Text != "mc" || second[1].Text != "ma" {
		t.Fatalf("第二页错误: %+v", second)
	}
	if page := r.PageSession("g:qq:1", 2, second[1].ID, 0); len(page) != 0 {
		t.Fatalf("更早的日志应为空, got %+v", page)
	}
	// 增量：只返回比游标更新的日志
	after := r.PageSession("g:qq:1", 10, 0, first[1].ID)
	if len(after) != 1 || after[0].Text != "通知" {
		t.Fatalf("after 增量错误: %+v", after)
	}
	// 未知会话
	if page := r.PageSession("g:qq:404", 10, 0, 0); len(page) != 0 {
		t.Fatalf("未知会话应为空, got %+v", page)
	}
}

// TestSessionRebuild 模拟 redis 驱动下的重启：新 Recorder 首次查询会话时
// 从存储重建索引，历史日志仍能按会话分页。
func TestSessionRebuild(t *testing.T) {
	store := newStore()
	r1 := msglog.New(store, 0)
	r1.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:1", UserId: "qq:9", Nickname: "小明", Text: "重启前1"})
	r1.Add(msglog.Entry{Type: msglog.TypeFriend, UserId: "qq:2", Nickname: "小红", Text: "重启前2"})
	r1.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:1", UserId: "qq:9", Nickname: "小明", Text: "重启前3"})

	r2 := msglog.New(store, 0)
	sessions := r2.Sessions()
	if len(sessions) != 2 {
		t.Fatalf("重建后会话数 = %d, want 2: %+v", len(sessions), sessions)
	}
	page := r2.PageSession("g:qq:1", 10, 0, 0)
	if len(page) != 2 || page[0].Text != "重启前3" || page[1].Text != "重启前1" {
		t.Fatalf("重建后按会话分页错误: %+v", page)
	}
	// 重建后再追加，新消息仍进入同一会话
	r2.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:1", UserId: "qq:9", Nickname: "小明", Text: "重启后"})
	page = r2.PageSession("g:qq:1", 10, 0, 0)
	if len(page) != 3 || page[0].Text != "重启后" {
		t.Fatalf("重建后增量错误: %+v", page)
	}
}

// TestSessionIndexWithEviction 容量淘汰后，会话索引的条数与分页不应包含已删除日志。
func TestSessionIndexWithEviction(t *testing.T) {
	r := msglog.New(newStore(), 3)
	for _, text := range []string{"a", "b", "c", "d", "e"} {
		r.Add(msglog.Entry{Type: msglog.TypeGroup, GroupId: "qq:1", UserId: "qq:9", Text: text})
	}
	sessions := r.Sessions()
	if len(sessions) != 1 || sessions[0].Count != 3 {
		t.Fatalf("淘汰后会话条数错误: %+v", sessions)
	}
	page := r.PageSession("g:qq:1", 10, 0, 0)
	if len(page) != 3 || page[0].Text != "e" || page[2].Text != "c" {
		t.Fatalf("淘汰后分页错误: %+v", page)
	}
}

// TestRestartWithSameStore 模拟 redis 驱动下的重启：同一存储新建 Recorder，
// 日志与 ID 计数器都应续接。
func TestRestartWithSameStore(t *testing.T) {
	store := newStore()
	r1 := msglog.New(store, 0)
	r1.Add(msglog.Entry{Type: msglog.TypeGroup, Text: "重启前"})

	r2 := msglog.New(store, 0)
	r2.Add(msglog.Entry{Type: msglog.TypeFriend, Text: "重启后"})

	entries := r2.Recent(0)
	if len(entries) != 2 {
		t.Fatalf("重启后期望 2 条日志，实际 %d", len(entries))
	}
	if entries[0].Text != "重启后" || entries[1].Text != "重启前" {
		t.Fatalf("重启后顺序错误: %+v", entries)
	}
	if entries[0].ID != 2 || entries[1].ID != 1 {
		t.Fatalf("重启后 ID 未续接: %+v", entries)
	}
}
