package pluginaichat

import (
	"sync"
	"testing"

	"github.com/jeanhua/AniaBot/common/model/message"
)

func makePendingMsg(id int) message.Message {
	return message.Message{
		MessageId: message.FromUint64(uint64(id)),
		Sender:    message.MessageSender{UserId: message.FromUint64(uint64(1000 + id))},
	}
}

func TestPendingQueueEnqueueAndDrain(t *testing.T) {
	p := &AIChatPlugin{}

	first, ok := p.enqueuePending("123", true, makePendingMsg(1))
	if !ok || !first {
		t.Fatalf("首条入队应为 first=true ok=true, 实际 first=%v ok=%v", first, ok)
	}

	first, ok = p.enqueuePending("123", true, makePendingMsg(2))
	if !ok || first {
		t.Fatalf("第二条入队应为 first=false ok=true, 实际 first=%v ok=%v", first, ok)
	}

	items := p.drainPending("123", true)
	if len(items) != 2 {
		t.Fatalf("drain 应返回 2 条消息, 实际 %d 条", len(items))
	}
	if items[0].MessageId != message.FromUint64(1) || items[1].MessageId != message.FromUint64(2) {
		t.Fatalf("drain 应保持入队顺序")
	}

	// drain 后队列应为空
	if items := p.drainPending("123", true); len(items) != 0 {
		t.Fatalf("drain 后队列应为空, 实际 %d 条", len(items))
	}
}

func TestPendingQueueFull(t *testing.T) {
	p := &AIChatPlugin{}

	for i := range maxPendingMessages {
		if _, ok := p.enqueuePending("456", false, makePendingMsg(i)); !ok {
			t.Fatalf("第 %d 条入队应成功", i+1)
		}
	}

	if _, ok := p.enqueuePending("456", false, makePendingMsg(maxPendingMessages)); ok {
		t.Fatalf("队列已满时应返回 ok=false")
	}

	if items := p.drainPending("456", false); len(items) != maxPendingMessages {
		t.Fatalf("drain 应返回 %d 条消息, 实际 %d 条", maxPendingMessages, len(items))
	}
}

func TestPendingQueueGroupFriendIsolation(t *testing.T) {
	p := &AIChatPlugin{}

	p.enqueuePending("789", true, makePendingMsg(1))
	p.enqueuePending("789", false, makePendingMsg(2))

	// 群聊与好友的 QID 数值相同时队列应相互隔离
	if items := p.drainPending("789", true); len(items) != 1 {
		t.Fatalf("群聊队列应只有 1 条消息, 实际 %d 条", len(items))
	}
	if items := p.drainPending("789", false); len(items) != 1 {
		t.Fatalf("好友队列应只有 1 条消息, 实际 %d 条", len(items))
	}
}

// TestMessageTargetPrivateWithChatID 回归：私聊消息即便带 GroupId（微信、飞书、
// Telegram 的私聊会话都会填平台会话 ID），也必须判成私聊并取发送者作目标，
// 否则回复走 SendGroupMsg，面板「消息日志」把同一会话拆成群聊 + 私聊两条。
func TestMessageTargetPrivateWithChatID(t *testing.T) {
	private := message.Message{
		MessageType: "private",
		GroupId:     "wx:u@im.wechat",
		UserId:      "wx:u@im.wechat",
		Sender:      message.MessageSender{UserId: "wx:u@im.wechat"},
	}
	if id, isGroup := messageTarget(private); isGroup || id != "wx:u@im.wechat" {
		t.Fatalf("私聊应判为好友会话并取发送者: id=%v isGroup=%v", id, isGroup)
	}

	group := message.Message{
		MessageType: "group",
		GroupId:     "qq:123",
		UserId:      "qq:456",
		Sender:      message.MessageSender{UserId: "qq:456"},
	}
	if id, isGroup := messageTarget(group); !isGroup || id != "qq:123" {
		t.Fatalf("群聊应判为群会话并取群号: id=%v isGroup=%v", id, isGroup)
	}
}

func TestPendingQueueConcurrent(t *testing.T) {
	p := &AIChatPlugin{}
	const goroutines = 50

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.enqueuePending("999", true, makePendingMsg(i))
		}(i)
	}
	wg.Wait()

	items := p.drainPending("999", true)
	if len(items) != maxPendingMessages {
		t.Fatalf("并发入队超过上限时应截断到 %d 条, 实际 %d 条", maxPendingMessages, len(items))
	}
}
