package pluginaichat

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// registerTeamRunForTest 登记一次团队运行，返回（是否被取消的通道, 注销函数）。
func registerTeamRunForTest(p *AIChatPlugin, id message.QID, isGroup bool) (canceled <-chan struct{}, unregister func()) {
	ch := make(chan struct{})
	ctx, unregister := p.registerTeamRun(context.Background(), id, isGroup)
	context.AfterFunc(ctx, func() { close(ch) })
	return ch, unregister
}

// TestStopSessionBackgroundCancelsWork 会话后台工作（异步子代理/工作流 + Agent
// 团队运行，含同一轮并行发起的多个团队运行）可被统一取消，且会话级工作表被清理；
// 没有后台工作时是空操作。
func TestStopSessionBackgroundCancelsWork(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromString("2001")

	subCanceled := make(chan struct{})
	p.getAsyncGroup(id, true).add("t-sub-1", func() { close(subCanceled) }, "调研")
	team1, unregister1 := registerTeamRunForTest(p, id, true)
	team2, unregister2 := registerTeamRunForTest(p, id, true)
	defer unregister1()
	defer unregister2()

	subagents, teams := p.stopSessionBackground(id, true)
	if subagents != 1 || teams != 2 {
		t.Fatalf("stopSessionBackground = (%d, %d), want (1, 2)", subagents, teams)
	}
	<-subCanceled
	<-team1
	<-team2
	if _, ok := p.asyncSubagents.Load(sessionKey(id, true)); ok {
		t.Fatal("取消后异步子代理组应被清理")
	}
	if _, ok := p.teamRuns.Load(sessionKey(id, true)); ok {
		t.Fatal("取消后团队运行登记应被清理")
	}

	// 没有后台工作时是空操作
	if subagents, teams := p.stopSessionBackground(id, true); subagents != 0 || teams != 0 {
		t.Fatalf("无后台工作时 stopSessionBackground = (%d, %d), want (0, 0)", subagents, teams)
	}
}

// TestNoteNewConversationRequest 只有携带 #新对话 的消息才立即停止后台工作；
// 普通消息不受影响（历史清空仍留到批次开始时执行）。
func TestNoteNewConversationRequest(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromString("2001")

	subCanceled := make(chan struct{})
	p.getAsyncGroup(id, true).add("t-sub-1", func() { close(subCanceled) }, "调研")
	teamCanceled, unregister := registerTeamRunForTest(p, id, true)
	defer unregister()

	if p.noteNewConversationRequest(id, true, testGroupMsg(2001, "你好")) {
		t.Fatal("普通消息不应被识别为开新对话")
	}
	select {
	case <-subCanceled:
		t.Fatal("普通消息不应停止后台子代理")
	default:
	}
	if _, ok := p.teamRuns.Load(sessionKey(id, true)); !ok {
		t.Fatal("普通消息不应停止团队运行")
	}

	if !p.noteNewConversationRequest(id, true, testGroupMsg(2001, "#新对话")) {
		t.Fatal("携带 #新对话 的消息应被识别")
	}
	<-subCanceled
	<-teamCanceled
}

// TestNewConversationPausesGoal #新对话 会暂停进行中的长任务目标：否则旧目标会在
// 新对话里自动续跑，把刚清空的上下文又顶回旧任务。目标仍可用 `/goal resume` 恢复。
func TestNewConversationPausesGoal(t *testing.T) {
	p := newGoalTestPlugin(t)
	id := message.FromUint64(1001)
	key := sessionKey(id, true)
	if err := p.saveGoal(&agentGoal{SessionKey: key, Objective: "旧目标", Phase: goalPhaseActive, MaxRounds: 5}); err != nil {
		t.Fatalf("save goal: %v", err)
	}
	p.armGoal(key)

	p.noteNewConversationRequest(id, true, testGroupMsg(1001, "#新对话"))

	g, ok, err := p.loadGoal(key)
	if err != nil || !ok {
		t.Fatalf("读取目标失败: ok=%v err=%v", ok, err)
	}
	if g.Phase != goalPhasePaused {
		t.Fatalf("目标应被暂停, phase=%s", g.Phase)
	}
	if p.goalIsArmed(key) {
		t.Fatal("目标应解除武装，避免自动续跑")
	}
}

// TestProcessChatBatchNewConversationStopsWork 端到端：#新对话 批次开始时兜底
// 停止仍在跑的后台工作（覆盖「消息在插话通道被暂缓到下一批」等路径）。
func TestProcessChatBatchNewConversationStopsWork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fakePlainChatReply(w, "收到")
	}))
	defer srv.Close()

	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	chat, err := aichat.NewChatBot(srv.URL, "k", "m", "系统提示", 128000, nil, nil)
	if err != nil {
		t.Fatalf("NewChatBot 失败: %v", err)
	}
	rb := &recordBot{}
	id := message.FromString("2001")

	subCanceled := make(chan struct{})
	p.getAsyncGroup(id, true).add("t-sub-1", func() { close(subCanceled) }, "调研")
	teamCanceled, unregister := registerTeamRunForTest(p, id, true)
	defer unregister()

	// 普通批次：后台工作保持运行
	if ok := p.processChatBatch(context.Background(), rb, id, true, chat,
		[]message.Message{testGroupMsg(2001, "你好")}); !ok {
		t.Fatal("普通批次应成功")
	}
	select {
	case <-subCanceled:
		t.Fatal("普通批次不应停止后台子代理")
	default:
	}

	// #新对话 批次：兜底取消后台工作
	if ok := p.processChatBatch(context.Background(), rb, id, true, chat,
		[]message.Message{testGroupMsg(2001, "#新对话")}); !ok {
		t.Fatal("#新对话 批次应成功")
	}
	<-subCanceled
	<-teamCanceled
	if _, ok := p.asyncSubagents.Load(sessionKey(id, true)); ok {
		t.Fatal("取消后异步子代理组应被清理")
	}
}
