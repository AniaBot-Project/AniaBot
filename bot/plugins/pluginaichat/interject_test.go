package pluginaichat

import (
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestBuildInterjectionSteer 运行中插话的格式化规则：队列为空不注入；用户消息
// 带【用户插话】前缀与发送者信息；合成消息（子代理结果等）标为后台消息；
// 注入后队列被排空（不会重复处理）。
func TestBuildInterjectionSteer(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	rb := &recordBot{}
	reg := newImageRegistry()
	steer := p.buildInterjectionSteer(rb, id, true, reg, nil)

	if got := steer(); got != nil {
		t.Fatalf("队列为空时应返回 nil, got %v", got)
	}

	p.enqueuePending(id, true, testGroupMsg(1001, "帮我加个导出功能"))
	got := steer()
	if len(got) != 1 {
		t.Fatalf("应注入 1 条插话, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "【用户插话】") {
		t.Fatalf("用户消息应带用户插话前缀: %s", got[0])
	}
	if !strings.Contains(got[0], "帮我加个导出功能") {
		t.Fatalf("插话应包含原消息文本: %s", got[0])
	}
	if !strings.Contains(got[0], "[nickname:测试 id:qq:1001]") {
		t.Fatalf("群聊插话应保留发送者前缀: %s", got[0])
	}
	if n := p.pendingCount(sessionKey(id, true)); n != 0 {
		t.Fatalf("注入后队列应被排空, got %d", n)
	}
	if again := steer(); again != nil {
		t.Fatalf("已注入的消息不应重复注入: %v", again)
	}

	// 合成消息（Sender.UserId=0）标为后台消息，不冒充用户发言
	p.enqueuePending(id, true, message.Message{
		Sender:  message.MessageSender{Nickname: "子代理", UserId: message.FromUint64(0)},
		Message: []message.OB11Segment{{Type: message.SegmentText, Data: map[string]any{"text": "【子代理执行完成】状态=completed"}}},
	})
	got = steer()
	if len(got) != 1 || !strings.Contains(got[0], "【后台消息】") {
		t.Fatalf("合成消息应标为后台消息: %v", got)
	}
}

// TestBuildInterjectionSteerMultiple 一次注入多条时保持先后顺序，逐条独立成
// user 消息（历史里可追溯谁在什么时候插的话）。
func TestBuildInterjectionSteerMultiple(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	steer := p.buildInterjectionSteer(&recordBot{}, id, false, nil, nil)

	p.enqueuePending(id, false, testGroupMsg(1001, "第一条"))
	p.enqueuePending(id, false, testGroupMsg(1001, "第二条"))
	got := steer()
	if len(got) != 2 {
		t.Fatalf("应逐条注入 2 条插话, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "第一条") || !strings.Contains(got[1], "第二条") {
		t.Fatalf("插话顺序不符: %v", got)
	}
}

// TestBuildInterjectionSteerHoldsNewConversation #新对话 是批次级语义（整批开始前
// 清空历史），不能在回合中途执行：它及其之后的消息必须放回队列，等本批结束后
// 作为下一批处理；它之前的消息照常插话。
func TestBuildInterjectionSteerHoldsNewConversation(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	steer := p.buildInterjectionSteer(&recordBot{}, id, true, nil, nil)

	p.enqueuePending(id, true, testGroupMsg(1001, "补充说明"))
	p.enqueuePending(id, true, testGroupMsg(1001, "#新对话"))
	p.enqueuePending(id, true, testGroupMsg(1001, "新话题的第一句"))

	got := steer()
	if len(got) != 1 || !strings.Contains(got[0], "补充说明") {
		t.Fatalf("#新对话 之前的消息应正常插话, got %v", got)
	}
	// 再次排空（下一次 LLM 请求前）：队列头是 #新对话，整段继续保留，不注入
	if again := steer(); again != nil {
		t.Fatalf("#新对话 及其之后的消息不应在回合中途注入, got %v", again)
	}

	pending := p.drainPending(id, true)
	if len(pending) != 2 {
		t.Fatalf("#新对话 及其之后的消息应保留在队列, got %d 条", len(pending))
	}
	first := pending[0].Message[0].Data["text"].(string)
	second := pending[1].Message[0].Data["text"].(string)
	if !strings.Contains(first, "#新对话") || !strings.Contains(second, "新话题的第一句") {
		t.Fatalf("保留消息的顺序不符: %q, %q", first, second)
	}
}
