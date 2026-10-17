package discord

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestResolveDeleterQueryBounded 审计日志查询必须受并发闸门限制：
// 批量删除（purge）会同时产生大量查询，无上限会撞 Discord 速率限制并影响正常发送。
// 闸门满时跳过查询、操作者留空（通知照常投递，不阻塞事件循环）。
func TestResolveDeleterQueryBounded(t *testing.T) {
	a := NewAdapter(nil)
	if cap(a.auditSem) != discordAuditMaxConcurrent {
		t.Fatalf("并发上限应为 %d, got %d", discordAuditMaxConcurrent, cap(a.auditSem))
	}
	if discordAuditMaxConcurrent < 1 {
		t.Fatal("并发上限必须为正")
	}
	// 占满闸门后：查询应被立即跳过（不留空即会退到 2s 睡眠 + REST 调用）
	for i := 0; i < discordAuditMaxConcurrent; i++ {
		a.auditSem <- struct{}{}
	}
	start := time.Now()
	got := a.resolveDeleter(&discordgo.MessageDelete{
		Message: &discordgo.Message{ChannelID: "1", GuildID: "2"},
	}, message.QID("dc:1:9"))
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("闸门满时不应等待/查询，耗时 %v", elapsed)
	}
	if got != "" {
		t.Fatalf("闸门满时操作者应留空, got %q", got)
	}
	// 释放后闸门恢复可用（容量语义正确）
	<-a.auditSem
	if len(a.auditSem) != discordAuditMaxConcurrent-1 {
		t.Fatalf("释放一个令牌后占用数应为 %d, got %d", discordAuditMaxConcurrent-1, len(a.auditSem))
	}
}

func TestMsgIDRoundTrip(t *testing.T) {
	id := msgID("111", "222")
	if id.String() != "dc:111:222" {
		t.Fatalf("msgID = %q", id)
	}
	c, m, ok := parseMsgID(id.String())
	if !ok || c != "111" || m != "222" {
		t.Fatalf("parseMsgID = %q %q %v", c, m, ok)
	}
}

func TestParseMsgIDReject(t *testing.T) {
	cases := []string{
		"",           // 空
		"111:222",    // 无前缀
		"tg:111:222", // 其他平台前缀
		"123456",     // QQ 裸数字
		"dc:111",     // 缺消息段
		"dc::222",    // 空频道段
		"dc:111:",    // 空消息段
		"dc:",        // 全空
	}
	for _, c := range cases {
		if _, _, ok := parseMsgID(c); ok {
			t.Fatalf("parseMsgID(%q) 应拒绝", c)
		}
	}
}

func TestParseChannelID(t *testing.T) {
	c, ok := parseChannelID(message.QID("dc:111"))
	if !ok || c != "111" {
		t.Fatalf("parseChannelID = %q %v", c, ok)
	}
	for _, q := range []string{"", "dc:", "123456", "tg:111", "dc:111:222"} {
		if _, ok := parseChannelID(message.QID(q)); ok {
			t.Fatalf("parseChannelID(%q) 应拒绝", q)
		}
	}
}

func TestMessageKey(t *testing.T) {
	a := NewAdapter(nil)
	key, ok := a.MessageKey(message.Message{MessageId: msgID("1", "2")})
	if !ok || key != "msg:1:2" {
		t.Fatalf("MessageKey = %q %v", key, ok)
	}
	if _, ok := a.MessageKey(message.Message{}); ok {
		t.Fatal("空消息 ID 应返回 false")
	}
}

func TestNoticeKey(t *testing.T) {
	a := NewAdapter(nil)
	key, ok := a.NoticeKey("group_recall", message.GroupRecallNotice{MessageId: msgID("1", "2")})
	if !ok || key != "recall:1:2" {
		t.Fatalf("group_recall NoticeKey = %q %v", key, ok)
	}
	key, ok = a.NoticeKey("friend_recall", message.FriendRecallNotice{MessageId: msgID("3", "4")})
	if !ok || key != "recall:3:4" {
		t.Fatalf("friend_recall NoticeKey = %q %v", key, ok)
	}
	// 非撤回通知不去重
	if _, ok := a.NoticeKey("poke", message.PokeNotice{}); ok {
		t.Fatal("poke 通知应返回 false")
	}
}

func TestSelfIDBeforeReady(t *testing.T) {
	a := NewAdapter(nil)
	if id := a.SelfID(); id != "" {
		t.Fatalf("READY 前 SelfID 应为空，得到 %q", id)
	}
	a.selfID = "999"
	if id := a.SelfID(); id != "dc:999" {
		t.Fatalf("SelfID = %q", id)
	}
}

func TestSupportedSegments(t *testing.T) {
	a := NewAdapter(nil)
	segs := a.SupportedSegments()
	want := map[string]bool{
		message.SegmentText: true, message.SegmentMention: true, message.SegmentImage: true,
		message.SegmentReply: true, message.SegmentFile: true, message.SegmentRecord: true,
		message.SegmentVideo: true,
	}
	if len(segs) != len(want) {
		t.Fatalf("SupportedSegments 数量 = %d", len(segs))
	}
	for _, s := range segs {
		if !want[s] {
			t.Fatalf("意外段类型 %q", s)
		}
	}
}
