package pluginaichat

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// recordForwardBot 在 recordBot 基础上支持群聊合并转发（bot.GroupForwardSender）：
// 记录每次合并转发的内容，可注入发送失败用于验证回退。
type recordForwardBot struct {
	recordBot
	fmu         sync.Mutex
	forwards    []message.ForwardMessageSegment
	failForward bool
}

func (b *recordForwardBot) SendGroupForwardMsg(_ message.QID, chain msgchain.GroupForwardChain) (message.QID, bool) {
	b.fmu.Lock()
	defer b.fmu.Unlock()
	if b.failForward {
		return "", false
	}
	b.forwards = append(b.forwards, chain.GetForwardMsg())
	return "1", true
}

// forwarded 返回收到的合并转发消息（含节点）。
func (b *recordForwardBot) forwarded() []message.ForwardMessageSegment {
	b.fmu.Lock()
	defer b.fmu.Unlock()
	return append([]message.ForwardMessageSegment{}, b.forwards...)
}

// nodeTexts 提取合并转发各节点的文本。
func nodeTexts(fwd message.ForwardMessageSegment) []string {
	out := make([]string, 0, len(fwd.Messages))
	for _, node := range fwd.Messages {
		out = append(out, chainText(node.Data.Content))
	}
	return out
}

// nodeMentions 统计合并转发各节点的 @ 提及段总数。
func nodeMentions(fwd message.ForwardMessageSegment) int {
	n := 0
	for _, node := range fwd.Messages {
		for _, s := range node.Data.Content {
			if s.Type == "at" {
				n++
			}
		}
	}
	return n
}

// TestGroupReplyForward 群聊拆分超过阈值且平台支持合并转发时，整段作为一条
// 合并转发消息发出（每段一个节点），不再逐条发送；节点发送者为机器人自身。
func TestGroupReplyForward(t *testing.T) {
	fb := &recordForwardBot{}
	cbs := MakeGroupCallback(fb, message.FromUint64(1), message.FromUint64(2), message.FromUint64(3), slog.Default(), true)
	if _, err := cbs.SendText("一\n\n二\n\n三\n\n四"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb.sent(); len(got) != 0 {
		t.Fatalf("超过阈值不应逐条发送, got %v", got)
	}
	forwards := fb.forwarded()
	if len(forwards) != 1 {
		t.Fatalf("应发出 1 条合并转发, got %d", len(forwards))
	}
	if texts := nodeTexts(forwards[0]); len(texts) != 4 || texts[0] != " 一" || texts[3] != "四" {
		t.Fatalf("每段应各占一个节点, got %q", texts)
	}
	if mentions := nodeMentions(forwards[0]); mentions != 1 {
		t.Fatalf("应仅首节点带 @ 提及, got %d", mentions)
	}
	if node := forwards[0].Messages[0]; node.Data.UserId != message.FromUint64(3) || node.Data.Nickname != forwardNodeNickname {
		t.Fatalf("节点发送者应为机器人自身, got %q/%q", node.Data.UserId, node.Data.Nickname)
	}
}

// TestGroupReplyForwardThreshold 阈值边界：片段数不超过阈值（3 条）时仍逐条发送。
func TestGroupReplyForwardThreshold(t *testing.T) {
	fb := &recordForwardBot{}
	cbs := MakeGroupCallback(fb, message.FromUint64(1), message.FromUint64(2), message.FromUint64(3), slog.Default(), true)
	if _, err := cbs.SendText("一\n\n二\n\n三"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb.sent(); len(got) != 3 {
		t.Fatalf("等于阈值应逐条发送 3 条, got %v", got)
	}
	if got := fb.forwarded(); len(got) != 0 {
		t.Fatalf("等于阈值不应合并转发, got %d", len(got))
	}
}

// TestGroupReplyForwardUnsupported 平台不支持合并转发（非 QQ）时保持逐条发送。
func TestGroupReplyForwardUnsupported(t *testing.T) {
	fb := &recordBot{}
	cbs := MakeGroupCallback(fb, message.FromUint64(1), message.FromUint64(2), message.FromUint64(3), slog.Default(), true)
	if _, err := cbs.SendText("一\n\n二\n\n三\n\n四"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb.sent(); len(got) != 4 {
		t.Fatalf("平台不支持时应逐条发送 4 条, got %v", got)
	}
}

// TestGroupReplyForwardFallbackOnFailure 合并转发发送失败时回退逐条发送，内容不丢。
func TestGroupReplyForwardFallbackOnFailure(t *testing.T) {
	fb := &recordForwardBot{failForward: true}
	cbs := MakeGroupCallback(fb, message.FromUint64(1), message.FromUint64(2), message.FromUint64(3), slog.Default(), true)
	if _, err := cbs.SendText("一\n\n二\n\n三\n\n四"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb.sent(); len(got) != 4 {
		t.Fatalf("合并转发失败应回退逐条发送 4 条, got %v", got)
	}
}

// TestFriendReplyNoForward 私聊回复不做合并转发（拆分后仍逐条发送）。
func TestFriendReplyNoForward(t *testing.T) {
	fb := &recordForwardBot{}
	cbs := MakeFriendCallback(fb, message.FromUint64(1), slog.Default(), true)
	if _, err := cbs.SendText("一\n\n二\n\n三\n\n四"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got := fb.sent(); len(got) != 4 {
		t.Fatalf("私聊应逐条发送 4 条, got %v", got)
	}
	if got := fb.forwarded(); len(got) != 0 {
		t.Fatalf("私聊不应合并转发, got %d", len(got))
	}
}

// TestClockSendTextForward 定时任务的群聊收尾推送同样遵循合并转发：
// 拆分超过阈值时一条合并转发（首节点 @ 创建者），私聊仍逐条发送。
func TestClockSendTextForward(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second, 100)
	fb := &recordForwardBot{}
	m.bot = fb

	p.cfg.Reply.SplitBlankLine = true
	groupTask := &ClockTask{ID: "1", TargetType: clockTargetGroup, TargetID: "9", CreatedBy: message.FromUint64(7)}
	if !m.sendText(groupTask, "一\n\n二\n\n三\n\n四") {
		t.Fatal("发送应成功")
	}
	if got := fb.sent(); len(got) != 0 {
		t.Fatalf("超过阈值不应逐条发送, got %v", got)
	}
	forwards := fb.forwarded()
	if len(forwards) != 1 {
		t.Fatalf("应发出 1 条合并转发, got %d", len(forwards))
	}
	if texts := nodeTexts(forwards[0]); len(texts) != 4 {
		t.Fatalf("应有 4 个节点, got %q", texts)
	}
	if mentions := nodeMentions(forwards[0]); mentions != 1 {
		t.Fatalf("应仅首节点 @ 创建者, got %d", mentions)
	}

	// 后台路径无事件来源：节点发送者 ID 为空，交给平台按自身身份兜底
	if node := forwards[0].Messages[0]; node.Data.UserId != "" {
		t.Fatalf("后台路径节点发送者 ID 应为空, got %q", node.Data.UserId)
	}

	friendTask := &ClockTask{ID: "2", TargetType: clockTargetFriend, TargetID: "5"}
	if !m.sendText(friendTask, "一\n\n二\n\n三\n\n四") {
		t.Fatal("发送应成功")
	}
	if got := fb.sent(); len(got) != 4 {
		t.Fatalf("私聊任务应逐条发送 4 条, got %v", got)
	}
	if got := fb.forwarded(); len(got) != 1 {
		t.Fatalf("私聊任务不应再合并转发, got %d", len(got))
	}
}

// TestBatchSelfID 批量取机器人自身 ID：取首个非空 self_id，全空时返回空串。
func TestBatchSelfID(t *testing.T) {
	if got := batchSelfID([]message.Message{{}, {SelfId: message.FromUint64(9)}}); got != message.FromUint64(9) {
		t.Fatalf("应取首个非空 self_id, got %q", got)
	}
	if got := batchSelfID([]message.Message{{}, {}}); got != "" {
		t.Fatalf("全空时应为空串, got %q", got)
	}
}
