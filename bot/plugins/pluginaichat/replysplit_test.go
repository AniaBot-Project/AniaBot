package pluginaichat

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestSplitByBlankLine 验证按空行拆分的边界：单换行保留在段内、
// 含空白的行也算边界、首尾空白被裁剪、无空行时保持单段。
func TestSplitByBlankLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"无空行", "只有一段\n第二行", []string{"只有一段\n第二行"}},
		{"两个段落", "第一段\n\n第二段", []string{"第一段", "第二段"}},
		{"多空行合并为一个边界", "第一段\n\n\n\n第二段", []string{"第一段", "第二段"}},
		{"空白行算边界", "第一段\n \t \n第二段", []string{"第一段", "第二段"}},
		{"CRLF", "第一段\r\n\r\n第二段", []string{"第一段", "第二段"}},
		{"首尾空行", "\n\n第一段\n\n", []string{"第一段"}},
		{"段内单换行保留", "标题\n正文\n\n下一段", []string{"标题\n正文", "下一段"}},
		{"三段落", "一\n\n二\n\n三", []string{"一", "二", "三"}},
	}
	for _, c := range cases {
		got := splitByBlankLine(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%s: 段数应为 %d, got %d (%q)", c.name, len(c.want), len(got), got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: 第 %d 段应为 %q, got %q", c.name, i+1, c.want[i], got[i])
			}
		}
	}
}

// TestReplySegments 验证开关语义：关闭时整段一条；开启时按空行拆分；
// 整段均为空白时回退原文，交由调用方按原语义处理。
func TestReplySegments(t *testing.T) {
	text := "第一段\n\n第二段"
	if segs := replySegments(text, false); len(segs) != 1 || segs[0] != text {
		t.Fatalf("开关关闭时应整段一条, got %q", segs)
	}
	if segs := replySegments(text, true); len(segs) != 2 {
		t.Fatalf("开关开启时应拆成 2 条, got %q", segs)
	}
	if segs := replySegments("   \n\n  ", true); len(segs) != 1 || segs[0] != "   \n\n  " {
		t.Fatalf("全空白应回退原文, got %q", segs)
	}
}

// TestGroupCallbackSplitSend 验证群聊回调拆分发送：多条消息逐条发出，
// 仅首条带 @ 提及，发送失败时返回错误。
func TestGroupCallbackSplitSend(t *testing.T) {
	fb := &recordBot{}
	cbs := MakeGroupCallback(fb, message.FromUint64(1), message.FromUint64(2), slog.Default(), true)
	if _, err := cbs.SendText("第一段\n\n第二段\n\n第三段"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	sent := fb.sent()
	if len(sent) != 3 {
		t.Fatalf("应拆成 3 条消息, got %d (%v)", len(sent), sent)
	}
	if mentions := fb.groupMentions(); mentions != 1 {
		t.Fatalf("应仅首条带 @ 提及, got %d", mentions)
	}

	fb2 := &recordBot{}
	cbs2 := MakeGroupCallback(fb2, message.FromUint64(1), message.FromUint64(2), slog.Default(), false)
	if _, err := cbs2.SendText("第一段\n\n第二段"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb2.sent(); len(got) != 1 {
		t.Fatalf("开关关闭时应整段一条, got %v", got)
	}
}

// TestFriendCallbackSplitSend 验证私聊回调拆分发送。
func TestFriendCallbackSplitSend(t *testing.T) {
	fb := &recordBot{}
	cbs := MakeFriendCallback(fb, message.FromUint64(1), slog.Default(), true)
	if _, err := cbs.SendText("第一段\n\n第二段"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb.sent(); len(got) != 2 {
		t.Fatalf("应拆成 2 条消息, got %v", got)
	}
}

// TestClockSendTextSplit 验证定时任务收尾推送同样遵循拆分开关，
// 群聊仅首条带创建者 @ 提醒。
func TestClockSendTextSplit(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second, 100)
	fb := &recordBot{}
	m.bot = fb

	task := &ClockTask{ID: "1", TargetType: clockTargetFriend, TargetID: "5"}
	if !m.sendText(task, "一\n\n二") {
		t.Fatal("发送应成功")
	}
	if got := fb.sent(); len(got) != 1 {
		t.Fatalf("开关关闭时应整段一条, got %v", got)
	}

	p.cfg.Reply.SplitBlankLine = true
	if !m.sendText(task, "一\n\n二\n\n三") {
		t.Fatal("发送应成功")
	}
	got := fb.sent()
	if len(got) != 4 || got[1] != "一" || got[2] != "二" || got[3] != "三" {
		t.Fatalf("开启后应拆成 3 条, got %v", got)
	}

	groupTask := &ClockTask{ID: "2", TargetType: clockTargetGroup, TargetID: "9", CreatedBy: message.FromUint64(7)}
	if !m.sendText(groupTask, "一\n\n二") {
		t.Fatal("发送应成功")
	}
	if mentions := fb.groupMentions(); mentions != 1 {
		t.Fatalf("群聊应仅首条带 @ 提及, got %d", mentions)
	}
}
