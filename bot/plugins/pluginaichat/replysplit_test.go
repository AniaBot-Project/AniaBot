package pluginaichat

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestReplySegments 验证拆分语义：有 ||| 分隔符时按标记拆成多条；没有分隔符
// 时整段一条（即使包含空行）；整段均为空白时回退原文，交由调用方按原有语义
// 处理，不静默丢消息。
func TestReplySegments(t *testing.T) {
	blank := "第一段\n\n第二段"
	if segs := replySegments(blank); len(segs) != 1 || segs[0] != blank {
		t.Fatalf("没有分隔符时应整段一条, got %q", segs)
	}
	if segs := replySegments("一|||二"); len(segs) != 2 || segs[0] != "一" || segs[1] != "二" {
		t.Fatalf("应按 ||| 拆分, got %q", segs)
	}
	if segs := replySegments("   \n\n  "); len(segs) != 1 || segs[0] != "   \n\n  " {
		t.Fatalf("全空白应回退原文, got %q", segs)
	}
	fenced := "```\na|||b\n```"
	if segs := replySegments(fenced); len(segs) != 1 || segs[0] != fenced {
		t.Fatalf("围栏内 ||| 不应拆分, got %q", segs)
	}
}

// TestSplitBySeparator 验证 ||| 拆分的边界：段内保留单个换行、换行包围的
// 分隔符同样生效、空片段被丢弃、代码围栏内的 ||| 不拆、无分隔符时返回 nil。
func TestSplitBySeparator(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"无分隔符", "只有一段\n第二行", nil},
		{"普通竖线不拆", "| a | b |", nil},
		{"空行不是边界", "第一段\n\n第二段", nil},
		{"两条", "第一段|||第二段", []string{"第一段", "第二段"}},
		{"三条", "一|||二|||三", []string{"一", "二", "三"}},
		{"换行包围的分隔符", "第一段\n|||\n第二段", []string{"第一段", "第二段"}},
		{"段内保留单个换行", "标题\n正文|||下一段", []string{"标题\n正文", "下一段"}},
		{"多余空白与空片段被丢弃", "一|||  |||二", []string{"一", "二"}},
		{"连续分隔符", "一||||||二", []string{"一", "二"}},
		{"CRLF", "一\r\n|||\r\n二", []string{"一", "二"}},
		{"围栏内不拆", "```\na|||b\n```", nil},
		{"仅围栏外拆分", "说明|||\n```\na|||b\n```\n|||结尾", []string{"说明", "```\na|||b\n```", "结尾"}},
		{"只有分隔符", "|||", nil},
	}
	for _, c := range cases {
		got := splitBySeparator(c.in)
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

// TestGroupCallbackSeparatorSend 验证群聊回调按 ||| 拆分发送：只有首条携带
// @ 提及；空行只是普通换行，不再作为消息边界。
func TestGroupCallbackSeparatorSend(t *testing.T) {
	fb := &recordBot{}
	cbs := MakeGroupCallback(fb, message.FromUint64(1), message.FromUint64(2), message.FromUint64(3), slog.Default())
	if _, err := cbs.SendText("第一段|||第二段|||第三段"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if sent := fb.sent(); len(sent) != 3 {
		t.Fatalf("应拆成 3 条消息, got %d (%v)", len(sent), sent)
	}
	if mentions := fb.groupMentions(); mentions != 1 {
		t.Fatalf("应仅首条带 @ 提及, got %d", mentions)
	}

	// 空行不再拆分：仍是一条消息
	fb2 := &recordBot{}
	cbs2 := MakeGroupCallback(fb2, message.FromUint64(1), message.FromUint64(2), message.FromUint64(3), slog.Default())
	if _, err := cbs2.SendText("第一段\n\n第二段"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb2.sent(); len(got) != 1 {
		t.Fatalf("空行不应再拆分, got %v", got)
	}
}

// TestFriendCallbackSeparatorSend 验证私聊回调按 ||| 拆分发送。
func TestFriendCallbackSeparatorSend(t *testing.T) {
	fb := &recordBot{}
	cbs := MakeFriendCallback(fb, message.FromUint64(1), slog.Default())
	if _, err := cbs.SendText("一|||二"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	got := fb.sent()
	if len(got) != 2 || got[0] != "一" || got[1] != "二" {
		t.Fatalf("应拆成 2 条消息, got %v", got)
	}
}

// TestClockSendTextSplit 验证定时任务收尾推送同样按 ||| 拆分，群聊仅首条
// 带创建者 @ 提醒。
func TestClockSendTextSplit(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second)
	fb := &recordBot{}
	m.bot = fb

	task := &ClockTask{ID: "1", TargetType: clockTargetFriend, TargetID: "5"}
	if !m.sendText(task, "一|||二|||三") {
		t.Fatal("发送应成功")
	}
	if got := fb.sent(); len(got) != 3 || got[0] != "一" || got[1] != "二" || got[2] != "三" {
		t.Fatalf("应拆成 3 条, got %v", got)
	}

	groupTask := &ClockTask{ID: "2", TargetType: clockTargetGroup, TargetID: "9", CreatedBy: message.FromUint64(7)}
	if !m.sendText(groupTask, "一|||二") {
		t.Fatal("发送应成功")
	}
	if mentions := fb.groupMentions(); mentions != 1 {
		t.Fatalf("群聊应仅首条带 @ 提及, got %d", mentions)
	}
}
