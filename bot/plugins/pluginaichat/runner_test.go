package pluginaichat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
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

// noGoBot 忽略后台任务（只记录发送），用于验证"入队"而不触发消费者。
// （syncGoBot 定义在 goal_test.go，两处测试共用。）
type noGoBot struct{ *recordBot }

func (noGoBot) Go(string, func()) {}

// ---- 停止原因映射（Query 日志 stop_reason） ----

func TestStopReasonFromError(t *testing.T) {
	var watchdogErr error = &aichat.WatchdogAbortError{Reason: "重复调用"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"成功", nil, taskStopCompleted},
		{"看门狗终止", watchdogErr, taskStopWatchdog},
		{"硬截止", context.DeadlineExceeded, taskStopTimeout},
		{"用户停止", context.Canceled, taskStopUserStop},
		{"普通错误", errors.New("boom"), taskStopError},
	}
	for _, c := range cases {
		if got := stopReasonFromError(c.err); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// ---- 子代理命名 / steering / 结构化结果 ----

func TestValidSubagentName(t *testing.T) {
	cases := map[string]bool{
		"a":                     true,
		"research-1":            true,
		"abc123":                true,
		"":                      false,
		"1abc":                  false,
		"AB":                    false,
		"a_b":                   false,
		"a b":                   false,
		strings.Repeat("a", 33): false,
	}
	for name, want := range cases {
		if got := validSubagentName(name); got != want {
			t.Fatalf("validSubagentName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestLaunchRejectsInvalidOrDuplicateName 非法名字与重名在启动前直接拒绝（不会拉起子代理）。
func TestLaunchRejectsInvalidOrDuplicateName(t *testing.T) {
	p := newRunnerTestPlugin()
	p.cfg.Subagent.Enable = true
	id := message.FromUint64(1001)
	rb := &recordBot{}

	out := p.launchAsyncSubagent(context.Background(), rb, id, true, "任务", launchSubagentOptions{Name: "Bad_Name"})
	if !strings.Contains(out, "不合法") {
		t.Fatalf("非法名字应被拒绝, got %q", out)
	}
	// 名字已被运行中的子代理占用
	p.getAsyncGroup(id, true).addEntry(asyncSubagentEntry{id: "t-1", name: "research", task: "任务", steer: make(chan string, 1)})
	out = p.launchAsyncSubagent(context.Background(), rb, id, true, "任务", launchSubagentOptions{Name: "research"})
	if !strings.Contains(out, "已被当前会话运行中的子代理占用") {
		t.Fatalf("重名应被拒绝, got %q", out)
	}
}

// TestSubagentSendSteersRunningAgent 运行中子代理可按名字/ID 追加指令，指令进入 steer 通道。
func TestSubagentSendSteersRunningAgent(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	steer := make(chan string, 2)
	p.getAsyncGroup(id, true).addEntry(asyncSubagentEntry{id: "t-steer", name: "writer", task: "写文档", steer: steer})

	if !p.sendSubagentMessage(id, true, "writer", "请补充结论") {
		t.Fatal("按名字追加指令失败")
	}
	if !p.sendSubagentMessage(id, true, "t-steer", "并按 markdown 输出") {
		t.Fatal("按 ID 追加指令失败")
	}
	if got := <-steer; got != "请补充结论" {
		t.Fatalf("第一条指令 = %q", got)
	}
	if got := <-steer; got != "并按 markdown 输出" {
		t.Fatalf("第二条指令 = %q", got)
	}
	if p.sendSubagentMessage(id, true, "missing", "x") {
		t.Fatal("不存在的目标应返回 false")
	}
}

// TestOnSubagentCompleteStructured 完成通知首行包含机器可读状态/任务 ID/名字。
func TestOnSubagentCompleteStructured(t *testing.T) {
	p := newRunnerTestPlugin()
	id := message.FromUint64(1001)
	rb := noGoBot{&recordBot{}}
	p.onSubagentComplete(rb, id, true, "t-done", "writer", "写一份调研报告", "报告正文", nil)
	pending := p.drainPending(id, true)
	if len(pending) != 1 {
		t.Fatalf("应入队 1 条合成消息, got %d", len(pending))
	}
	text := pending[0].Message[0].Data["text"].(string)
	for _, want := range []string{"【子代理执行完成】", "状态=completed", "任务ID: t-done", "名称: writer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("完成通知缺少 %q: %s", want, text)
		}
	}

	// 失败通知
	rb2 := noGoBot{&recordBot{}}
	p.onSubagentComplete(rb2, id, true, "t-fail", "", "任务", "", errors.New("模型报错"))
	pending = p.drainPending(id, true)
	if len(pending) != 1 || !strings.Contains(pending[0].Message[0].Data["text"].(string), "状态=failed") {
		t.Fatalf("失败通知结构不符: %+v", pending)
	}
}

// ---- /resume 参数与状态校验 ----

// newResumeTestPlugin 构造带任务表的测试插件。
func newResumeTestPlugin(t *testing.T) *AIChatPlugin {
	store, _ := newTaskTestStore(t)
	p := newRunnerTestPlugin()
	p.taskStore = store
	return p
}

func TestHandleResumeRequiresTaskID(t *testing.T) {
	p := newResumeTestPlugin(t)
	rb := &recordBot{}
	if _, err := p.handleResumeCommand(rb, testGroupMsg(1001, "/resume"), ""); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !containsSent(rb, "用法") {
		t.Fatalf("缺少任务 ID 应提示用法, got %v", rb.sent())
	}
}

func TestHandleResumeUnknownTask(t *testing.T) {
	p := newResumeTestPlugin(t)
	rb := &recordBot{}
	if _, err := p.handleResumeCommand(rb, testGroupMsg(1001, "/resume t-x"), "t-x"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !containsSent(rb, "未找到任务") {
		t.Fatalf("未知任务应提示未找到, got %v", rb.sent())
	}
}

func TestHandleResumeRejectsOtherSessionAndNonInterrupted(t *testing.T) {
	p := newResumeTestPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	other := taskFixture(sessionKey(message.FromUint64(2002), true), time.Now().Unix())
	other.ID = "t-other"
	other.Status = taskStatusInterrupted
	other.Kind = taskKindSubagent
	if err := p.taskStore.Create(ctx, other); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rb := &recordBot{}
	if _, err := p.handleResumeCommand(rb, testGroupMsg(1001, "/resume t-other"), "t-other"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !containsSent(rb, "只能恢复本会话") {
		t.Fatalf("跨会话恢复应被拒绝, got %v", rb.sent())
	}

	done := taskFixture(sessionKey(message.FromUint64(1001), true), time.Now().Unix())
	done.ID = "t-done"
	done.Status = taskStatusCompleted
	done.Kind = taskKindSubagent
	if err := p.taskStore.Create(ctx, done); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rb2 := &recordBot{}
	if _, err := p.handleResumeCommand(rb2, testGroupMsg(1001, "/resume t-done"), "t-done"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !containsSent(rb2, "只有被中断") {
		t.Fatalf("非中断任务应被拒绝, got %v", rb2.sent())
	}
}

// ---- task_handoff 参数校验 ----

func TestHandoffToolRequiresRemaining(t *testing.T) {
	tool := &handoffTool{}
	if _, err := tool.Execute(context.Background(), &handoffParams{Summary: "已完成一半"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("remaining 为空时应报错")
	}
}
