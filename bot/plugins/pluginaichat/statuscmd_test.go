package pluginaichat

import (
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// 会话状态快照与渲染（/status）：状态采集只读内存态，渲染退化为纯函数，
// 便于覆盖各优先级分支。

func TestRenderSessionStatusIdle(t *testing.T) {
	st := sessionStatusSnapshot{IsGroup: true, Target: message.FromUint64(123), QueueCap: maxPendingMessages}
	out := renderSessionStatus(st)
	for _, want := range []string{"群聊 qq:123", "AI 状态：空闲", "排队消息：无"} {
		if !strings.Contains(out, want) {
			t.Fatalf("空闲状态应包含 %q, 实际:\n%s", want, out)
		}
	}
	if strings.Contains(out, "并发槽位") {
		t.Fatalf("未占满槽位时不应提示并发槽位, 实际:\n%s", out)
	}
}

func TestRenderSessionStatusActive(t *testing.T) {
	now := time.Now()
	st := sessionStatusSnapshot{
		Target:    message.FromUint64(456),
		Active:    true,
		Elapsed:   75 * time.Second,
		Stage:     stageWaitingModel,
		StageAt:   now.Add(-3 * time.Second),
		QueueCap:  maxPendingMessages,
		Queued:    2,
		Subagents: 1,
		SlotsUsed: 2,
		SlotsCap:  2,
	}
	out := renderSessionStatus(st)
	for _, want := range []string{
		"AI 状态：响应中（已持续 1 分 15 秒）",
		"最近阶段：等待模型回复",
		"排队消息：2 条（上限 20）",
		"异步子代理：1 个运行中",
		"并发槽位：已占满（2/2）",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("响应中状态应包含 %q, 实际:\n%s", want, out)
		}
	}
}

func TestRenderSessionStatusQueueOnly(t *testing.T) {
	st := sessionStatusSnapshot{IsGroup: true, Target: message.FromUint64(789), QueueCap: maxPendingMessages, Queued: 3}
	out := renderSessionStatus(st)
	if !strings.Contains(out, "AI 状态：排队中") {
		t.Fatalf("仅有排队消息时应显示排队中, 实际:\n%s", out)
	}
	if !strings.Contains(out, "排队消息：3 条") {
		t.Fatalf("应显示排队条数, 实际:\n%s", out)
	}
}

func TestRenderSessionStatusWaitingSubagent(t *testing.T) {
	st := sessionStatusSnapshot{Target: message.FromUint64(1), Subagents: 2, QueueCap: maxPendingMessages}
	out := renderSessionStatus(st)
	if !strings.Contains(out, "等待子代理结果") {
		t.Fatalf("子代理运行中应提示等待结果, 实际:\n%s", out)
	}
}

func TestRenderSessionStatusPlanMode(t *testing.T) {
	st := sessionStatusSnapshot{Target: message.FromUint64(1), QueueCap: maxPendingMessages, PlanOn: true}
	if out := renderSessionStatus(st); !strings.Contains(out, "计划模式：开启") {
		t.Fatalf("计划模式开启时应提示, 实际:\n%s", out)
	}
}

func TestFormatStatusDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "不到 1 秒"},
		{0, "不到 1 秒"},
		{900 * time.Millisecond, "不到 1 秒"},
		{time.Second, "1 秒"},
		{59 * time.Second, "59 秒"},
		{60 * time.Second, "1 分"},
		{75 * time.Second, "1 分 15 秒"},
		{3600 * time.Second, "1 小时"},
		{3601 * time.Second, "1 小时"},
		{3*time.Hour + 25*time.Minute, "3 小时 25 分"},
	}
	for _, c := range cases {
		if got := formatStatusDuration(c.d); got != c.want {
			t.Fatalf("formatStatusDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestCollectSessionStatus 状态采集：活跃请求、排队消息、子代理与计划模式
// 都应反映到快照中；未登记活跃请求且锁空闲时判为空闲。
func TestCollectSessionStatus(t *testing.T) {
	p := &AIChatPlugin{
		Meta:        plugin.Meta{Name: "AI对话插件"},
		lockStorage: newLockFake(),
		rateCh:      make(chan struct{}, 2),
		planManager: newPlanManager(),
	}
	p.Logger = testLogger()
	id := message.FromUint64(10086)
	key := sessionKey(id, true)

	// 空闲
	st := p.collectSessionStatus(id, true)
	if st.Active || st.Queued != 0 || st.PlanOn || st.SlotsCap != 2 || st.SlotsUsed != 0 {
		t.Fatalf("空会话快照不符: %+v", st)
	}

	// 活跃请求 + 排队消息 + 子代理 + 计划模式
	p.setActiveContext(id, true, newActiveRequest(func() {}, id, time.Now().Add(-time.Minute)))
	defer p.clearActiveContext(id, true)
	p.enqueuePending(id, true, makePendingMsg(1))
	p.enqueuePending(id, true, makePendingMsg(2))
	p.getAsyncGroup(id, true).add("sub-1", func() {}, "任务")
	p.planManager.Set(key, true)

	st = p.collectSessionStatus(id, true)
	if !st.Active {
		t.Fatal("应识别为响应中")
	}
	if st.Elapsed < 59*time.Second {
		t.Fatalf("已持续时长应约 1 分钟, got %v", st.Elapsed)
	}
	if st.Queued != 2 {
		t.Fatalf("排队消息数 = %d, want 2", st.Queued)
	}
	if st.Subagents != 1 {
		t.Fatalf("运行中子代理数 = %d, want 1", st.Subagents)
	}
	if !st.PlanOn {
		t.Fatal("应识别出计划模式开启")
	}
}

// TestStatusCommandPrivateWithChatID 回归：私聊消息带着平台会话 ID（微信私聊、
// Telegram 私聊都如此）时，/status 必须按私聊回复并走 SendFriendMsg——按
// GroupId 非空推断群聊会让回复以群聊身份写入消息日志，面板「消息日志」于是
// 把同一个会话拆成「群聊」+「私聊」两条。
func TestStatusCommandPrivateWithChatID(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = testLogger()
	fb := &recordBot{}
	msg := message.Message{
		MessageType: "private",
		GroupId:     "wx:u@im.wechat",
		UserId:      "wx:u@im.wechat",
		Sender:      message.MessageSender{UserId: "wx:u@im.wechat"},
	}
	if _, err := p.handleStatusCommand(fb, msg); err != nil {
		t.Fatalf("/status 返回错误: %v", err)
	}
	if len(fb.groups) != 0 {
		t.Fatalf("私聊 /status 不应按群聊发送, got %v", fb.groups)
	}
	if len(fb.friend) != 1 || !strings.Contains(fb.friend[0], "【会话状态】私聊 wx:u@im.wechat") {
		t.Fatalf("私聊 /status 回复错误: %v", fb.friend)
	}
}

// TestSessionLockHeldWhenActiveContextMissing 建会话/回放历史阶段尚未登记
// activeContexts，但锁已被持有：/status 应仍显示响应中（准备会话）。
func TestSessionLockHeldWhenActiveContextMissing(t *testing.T) {
	p := &AIChatPlugin{
		Meta:        plugin.Meta{Name: "AI对话插件"},
		lockStorage: newLockFake(),
		rateCh:      make(chan struct{}, 2),
	}
	p.Logger = testLogger()
	id := message.FromUint64(20001)
	lock := p.tryLock(id, true)
	if lock == nil {
		t.Fatal("应能获取会话锁")
	}
	defer lock.release()

	st := p.collectSessionStatus(id, true)
	if !st.Active {
		t.Fatal("锁被持有时应判为响应中")
	}
	if st.Stage != stageBusy {
		t.Fatalf("阶段 = %q, want %q", st.Stage, stageBusy)
	}
}

// TestSessionLockHeldNilStorage 降载模式（锁存储未初始化）不应 panic。
func TestSessionLockHeldNilStorage(t *testing.T) {
	p := &AIChatPlugin{}
	if p.sessionLockHeld("g:1") {
		t.Fatal("锁存储未初始化时应返回 false")
	}
}
