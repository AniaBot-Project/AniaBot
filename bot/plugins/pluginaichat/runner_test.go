package pluginaichat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// newRunnerTestPlugin 构造运行器测试用的最小插件实例。
func newRunnerTestPlugin() *AIChatPlugin {
	p := &AIChatPlugin{Meta: plugin.Meta{Name: "AI对话插件"}}
	p.Logger = testLogger()
	return p
}

// ---- 回合运行器：上下文与预算 ----

// TestNewTurnContextDetachesFrameworkDeadline 回合上下文必须脱离框架事件 deadline
// （否则 bot.msg_event_timeout_sec 仍会掐断长回合），但保留 /stop 与插件关停取消。
func TestNewTurnContextDetachesFrameworkDeadline(t *testing.T) {
	p := &AIChatPlugin{}
	p.runCtx, p.runCancel = context.WithCancel(context.Background())

	parent, cancelParent := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelParent()
	runCtx, cancelRun := p.newTurnContext(parent)
	defer cancelRun()

	time.Sleep(60 * time.Millisecond)
	if runCtx.Err() != nil {
		t.Fatalf("回合上下文不应继承框架事件 deadline: %v", runCtx.Err())
	}
	cancelRun()
	if runCtx.Err() == nil {
		t.Fatal("cancelRun 应取消回合上下文")
	}

	// 插件关停（OnUnload 取消 runCtx）必须能中止回合
	shutdownCtx, shutdownCancel := p.newTurnContext(context.Background())
	defer shutdownCancel()
	p.runCancel()
	deadline := time.Now().Add(time.Second)
	for shutdownCtx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if shutdownCtx.Err() == nil {
		t.Fatal("插件关停应取消回合上下文")
	}
}

// TestNewTurnContextHardTimeout 硬截止配置生效；0 表示不限制。
func TestNewTurnContextHardTimeout(t *testing.T) {
	p := newRunnerTestPlugin()
	p.runCtx = context.Background()
	p.turnHardTimeout = 30 * time.Millisecond
	ctx, cancel := p.newTurnContext(context.Background())
	defer cancel()
	time.Sleep(80 * time.Millisecond)
	if ctx.Err() == nil {
		t.Fatal("配置硬截止后回合应到期取消")
	}

	p.turnHardTimeout = 0
	ctx2, cancel2 := p.newTurnContext(context.Background())
	defer cancel2()
	time.Sleep(60 * time.Millisecond)
	if ctx2.Err() != nil {
		t.Fatalf("硬截止为 0 时不应自行取消: %v", ctx2.Err())
	}
}

// ---- 长任务受理提示 ----

// TestTurnAckSentWhenNoVisibleOutput 回合运行超过阈值且无可见输出时应先发一条受理提示。
func TestTurnAckSentWhenNoVisibleOutput(t *testing.T) {
	p := newRunnerTestPlugin()
	p.taskAckAfter = 20 * time.Millisecond
	rb := &recordBot{}
	ctx, act := withTurnActivity(context.Background())
	_ = ctx
	stop := p.scheduleTurnAck(act, rb, message.FromUint64(1), false)
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !containsSent(rb, "正在处理") {
		time.Sleep(5 * time.Millisecond)
	}
	if !containsSent(rb, "正在处理") {
		t.Fatalf("长任务应发送受理提示, got %v", rb.sent())
	}
}

// TestTurnAckSuppressedWhenVisible 已有可见输出时不再发受理提示。
func TestTurnAckSuppressedWhenVisible(t *testing.T) {
	p := newRunnerTestPlugin()
	p.taskAckAfter = 20 * time.Millisecond
	rb := &recordBot{}
	ctx, act := withTurnActivity(context.Background())
	markTurnVisible(ctx)
	stop := p.scheduleTurnAck(act, rb, message.FromUint64(1), false)
	defer stop()
	time.Sleep(80 * time.Millisecond)
	if containsSent(rb, "正在处理") {
		t.Fatalf("已有可见输出时不应发送受理提示, got %v", rb.sent())
	}
}

// containsSent 判断 recordBot 是否发送过包含某子串的消息。
func containsSent(rb *recordBot, sub string) bool {
	for _, s := range rb.sent() {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ---- /stop 对后台任务的取消（旧缺口：无活跃回合时取消不到子代理）----

// TestStopSessionWorkCancelsSubagentsWithoutActiveTurn 没有活跃回合时 /stop
// 仍应取消该会话全部后台子代理。
func TestStopSessionWorkCancelsSubagentsWithoutActiveTurn(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	canceled := make(chan struct{})
	p.getAsyncGroup(id, true).add("t-test-1", func() { close(canceled) }, "深度调研")

	stopped, n, denied := p.stopSessionWork(id, true, message.FromUint64(2), true)
	if denied != "" {
		t.Fatalf("管理员停止不应被拒绝: %s", denied)
	}
	if stopped {
		t.Fatal("没有活跃回合时 stopped 应为 false")
	}
	if n != 1 {
		t.Fatalf("应取消 1 个后台子代理, got %d", n)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("后台子代理的 cancel 应被调用")
	}
	if p.lookupAsyncGroup(id, true) != nil {
		t.Fatal("取消后会话子代理组应被摘除")
	}
}

// TestStopSessionWorkDeniedForNonRequester 非触发者（非管理员）不能停止会话工作。
func TestStopSessionWorkDeniedForNonRequester(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	p.setActiveContext(id, true, newActiveRequest(func() {}, message.FromUint64(100), time.Now()))
	defer p.clearActiveContext(id, true)

	stopped, n, denied := p.stopSessionWork(id, true, message.FromUint64(200), false)
	if denied == "" || stopped || n != 0 {
		t.Fatalf("非触发者应被拒绝: stopped=%v n=%d denied=%q", stopped, n, denied)
	}
}

// ---- /tasks 渲染 ----

// TestRenderTasksIncludesRunningAndRecent 任务列表应包含运行中的子代理与最近任务记录。
func TestRenderTasksIncludesRunningAndRecent(t *testing.T) {
	store, _ := newTaskTestStore(t)
	p := newRunnerTestPlugin()
	p.taskStore = store
	id := message.FromUint64(1001)
	p.getAsyncGroup(id, true).add("t-running-1", func() {}, "深度调研任务")

	recent := taskFixture(sessionKey(id, true), time.Now().Unix())
	recent.ID = "t-recent-1"
	recent.Status = taskStatusCompleted
	recent.StopReason = taskStopCompleted
	recent.Kind = taskKindSubagent
	recent.Title = "写一份报告"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Create(ctx, recent); err != nil {
		t.Fatalf("Create: %v", err)
	}

	out := p.renderTasks(id, true)
	for _, want := range []string{"运行中", "t-running-1", "最近任务", "t-recent-1", "完成"} {
		if !strings.Contains(out, want) {
			t.Fatalf("任务列表应包含 %q, got:\n%s", want, out)
		}
	}
}

// TestHandleStopByIDCancelsSubagent 按 ID 取消运行中的后台子代理。
func TestHandleStopByIDCancelsSubagent(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	canceled := make(chan struct{})
	p.getAsyncGroup(id, true).add("t-cancel-1", func() { close(canceled) }, "任务")
	rb := &recordBot{}

	handled, err := p.handleStopByID(rb, testGroupMsg(1001, "/stop t-cancel-1"), "t-cancel-1")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("应按 ID 取消子代理")
	}
	if !containsSent(rb, "已取消任务") {
		t.Fatalf("应提示已取消, got %v", rb.sent())
	}
}

// ---- 看门狗配置解析 ----

func TestParseRepeatToolWarn(t *testing.T) {
	cases := []struct {
		raw  string
		want []int
	}{
		{"3,5,8", []int{3, 5, 8}},
		{" 8, 3 ,x,0,-1,5 ", []int{3, 5, 8}},
		{"", nil},
		{"x", nil},
	}
	for _, c := range cases {
		got := parseRepeatToolWarn(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("%q: got %v, want %v", c.raw, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q: got %v, want %v", c.raw, got, c.want)
			}
		}
	}
}

// TestWatchdogOptionsFromConfig 配置映射：软截止仅在主回合注入，子代理不注入。
func TestWatchdogOptionsFromConfig(t *testing.T) {
	p := &AIChatPlugin{}
	p.cfg.Turn.ProgressReminderAfter = 6
	p.cfg.Turn.RepeatToolWarn = "3,5,8"
	p.cfg.Turn.NoProgressLimit = 12
	p.turnSoftTimeout = 10 * time.Minute

	main := p.watchdogOptions(true)
	if main.SoftDeadline.IsZero() {
		t.Fatal("主回合应注入软截止")
	}
	if main.ProgressReminderAfter != 6 || main.NoProgressLimit != 12 || len(main.RepeatWarnThresholds) != 3 {
		t.Fatalf("看门狗配置映射错误: %+v", main)
	}
	sub := p.watchdogOptions(false)
	if !sub.SoftDeadline.IsZero() {
		t.Fatal("子代理不应注入软截止提醒")
	}
}

// ---- task_handoff 参数校验 ----

func TestHandoffToolRequiresRemaining(t *testing.T) {
	tool := &handoffTool{}
	if _, err := tool.Execute(context.Background(), &handoffParams{Summary: "已完成一半"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("remaining 为空时应报错")
	}
}
