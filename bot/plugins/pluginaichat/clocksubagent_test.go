package pluginaichat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// ---- clockSubagentSet ----

func TestClockSubagentSetMarkFinishedAndCollect(t *testing.T) {
	set := newClockSubagentSet()
	done1 := make(chan struct{})
	done2 := make(chan struct{})
	set.add(&clockSubagent{id: "1", task: "任务一", startTime: time.Now(), done: done1})
	set.add(&clockSubagent{id: "2", task: "任务二", startTime: time.Now().Add(time.Millisecond), done: done2})

	if !set.hasPending() || set.pendingCount() != 2 {
		t.Fatalf("初始应有 2 个运行中子代理, pending=%d", set.pendingCount())
	}

	set.markFinished("1", "结果一", nil)
	if set.pendingCount() != 1 {
		t.Fatalf("完成后应剩 1 个运行中子代理, pending=%d", set.pendingCount())
	}

	// 重复 markFinished 不应 panic（close 已关闭的 channel）
	set.markFinished("1", "重复", nil)

	finished := set.collectFinished()
	if len(finished) != 1 || finished[0].id != "1" || finished[0].result != "结果一" {
		t.Fatalf("collectFinished 应只取出已完成的子代理, got %+v", finished)
	}
	// 已取出的不再重复返回
	if again := set.collectFinished(); len(again) != 0 {
		t.Fatalf("已完成子代理不应重复收集, got %d 条", len(again))
	}

	set.markFinished("2", "", errors.New("执行失败"))
	finished = set.collectFinished()
	if len(finished) != 1 || finished[0].err == nil {
		t.Fatalf("失败结果应携带 err, got %+v", finished)
	}
	if set.hasPending() {
		t.Fatalf("全部完成后不应有运行中子代理")
	}
}

func TestClockSubagentSetWaitAll(t *testing.T) {
	set := newClockSubagentSet()
	done := make(chan struct{})
	set.add(&clockSubagent{id: "1", task: "任务", startTime: time.Now(), done: done})

	// 50ms 后完成，waitAll 应解除阻塞
	go func() {
		time.Sleep(50 * time.Millisecond)
		set.markFinished("1", "ok", nil)
	}()
	start := time.Now()
	set.waitAll(context.Background(), clockSubagentWaitReserve)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waitAll 应在子代理完成后及时返回, 耗时 %v", elapsed)
	}
	if set.hasPending() {
		t.Fatalf("完成后不应有运行中子代理")
	}
}

func TestClockSubagentSetWaitAllBudgetExhausted(t *testing.T) {
	set := newClockSubagentSet()
	set.add(&clockSubagent{id: "1", task: "任务", startTime: time.Now(), done: make(chan struct{})})

	// 父上下文 deadline 已不足 reserve：应立即返回，不再等待
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	set.waitAll(ctx, clockSubagentWaitReserve)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("预算不足时 waitAll 应立即返回, 耗时 %v", elapsed)
	}
	if !set.hasPending() {
		t.Fatalf("未完成的子代理应仍处于运行中状态")
	}
}

func TestClockSubagentSetCancel(t *testing.T) {
	set := newClockSubagentSet()
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	set.add(&clockSubagent{id: "1", task: "一", startTime: time.Now(), cancel: cancel1, done: make(chan struct{})})

	if !set.cancelOne("1") {
		t.Fatalf("cancelOne 应命中运行中的子代理")
	}
	if ctx1.Err() == nil {
		t.Fatalf("cancelOne 应取消子代理 ctx")
	}
	if set.cancelOne("1") {
		// 子代理被取消但未 markFinished 前仍视为运行中，重复取消同样命中（幂等 cancel）
		t.Log("重复 cancelOne 命中运行中子代理，符合预期")
	}
	if set.cancelOne("999") {
		t.Fatalf("cancelOne 不存在的 ID 应返回 false")
	}

	// cancelPending 取消全部
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	set.add(&clockSubagent{id: "2", task: "二", startTime: time.Now(), cancel: cancel2, done: make(chan struct{})})
	set.cancelPending()
	if ctx2.Err() == nil {
		t.Fatalf("cancelPending 应取消所有运行中子代理")
	}
}

// ---- buildClockSubagentReport ----

func TestBuildClockSubagentReport(t *testing.T) {
	finished := []*clockSubagent{
		{id: "1", task: "调研 A", result: "【子代理执行完成】结果 A"},
		{id: "2", task: "调研 B", err: errors.New("子代理执行超时（10s），已中止")},
	}
	report := buildClockSubagentReport(finished, 0)
	for _, want := range []string{"【子代理执行结果汇总】", "ID: 1", "调研 A", "结果 A", "ID: 2", "执行失败", "已中止"} {
		if !strings.Contains(report, want) {
			t.Fatalf("报告应包含 %q, got:\n%s", want, report)
		}
	}
	if strings.Contains(report, "仍在执行") {
		t.Fatalf("pending=0 时不应提示仍有子代理执行中, got:\n%s", report)
	}

	report = buildClockSubagentReport(finished, 2)
	if !strings.Contains(report, "2 个子代理仍在执行") {
		t.Fatalf("pending>0 时应提示剩余数量, got:\n%s", report)
	}
}

// TestClockSubagentSetHasLaunched 收尾判断依据：只要启动过子代理即为 true，
// 与是否仍有运行中条目（hasPending）无关——这正是结果不再被丢弃的关键。
func TestClockSubagentSetHasLaunched(t *testing.T) {
	set := newClockSubagentSet()
	if set.hasLaunched() {
		t.Fatal("未启动任何子代理时不应为 true")
	}

	done := make(chan struct{})
	set.add(&clockSubagent{id: "1", task: "任务", startTime: time.Now(), done: done})
	if !set.hasLaunched() || !set.hasPending() {
		t.Fatal("启动后两者均应为 true")
	}

	set.markFinished("1", "结果", nil)
	if !set.hasLaunched() {
		t.Fatal("子代理完成后 hasLaunched 仍应为 true（结果待回喂）")
	}
	if set.hasPending() {
		t.Fatal("子代理完成后 hasPending 应为 false")
	}

	// 重新添加第二个子代理后仍为 true
	set.add(&clockSubagent{id: "2", task: "任务二", startTime: time.Now(), done: make(chan struct{})})
	if !set.hasLaunched() {
		t.Fatal("再次启动子代理后应仍为 true")
	}
}

// ---- 事件取消与总预算（bridgeRunContext / eventBudget） ----

// TestBridgeRunContextKeepsCancellationAndDropsDeadline 运行上下文必须保留框架的
// 关停/卸载取消（core.Stop() 取消 ania.ctx 时要能中止在跑的 AI 请求），同时剥离
// 事件 deadline（事件总时长由插件按 eventBudget 自行结算）。
//
// 旧实现用 context.WithoutCancel(ctx)，把取消一并剥离：Stop/卸载/重启都无法
// 中止在跑请求。
func TestBridgeRunContextKeepsCancellationAndDropsDeadline(t *testing.T) {
	p := &AIChatPlugin{msgEventTimeout: 5 * time.Minute}

	parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
	defer cancelParent()

	runCtx, deadline, cancelRun := p.bridgeRunContext(parent)
	defer cancelRun()

	if deadline.IsZero() {
		t.Fatal("应回报框架事件的绝对截止时刻，供总预算结算")
	}
	if _, ok := runCtx.Deadline(); ok {
		t.Fatal("运行上下文不应继承事件 deadline（单批预算由插件派生）")
	}
	if runCtx.Err() != nil {
		t.Fatalf("未取消时不应处于取消态: %v", runCtx.Err())
	}

	// 框架关停/卸载取消父 ctx：运行上下文必须随之取消
	cancelParent()
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("父上下文取消后运行上下文未取消：框架关停无法中止在跑的 AI 请求")
	}
}

// TestEventBudgetFollowsParentDeadline 总预算以框架事件 deadline 为准。
func TestEventBudgetFollowsParentDeadline(t *testing.T) {
	p := &AIChatPlugin{msgEventTimeout: 5 * time.Minute}

	if got := p.eventBudget(time.Time{}); got != p.msgEventTimeout {
		t.Fatalf("框架未设 deadline 时应退回配置值, got %v", got)
	}

	deadline := time.Now().Add(90 * time.Second)
	got := p.eventBudget(deadline)
	if got <= 0 || got > 91*time.Second {
		t.Fatalf("应返回父截止时刻的剩余预算, got %v", got)
	}
	if got < 88*time.Second {
		t.Fatalf("剩余预算明显偏小, got %v", got)
	}

	// 已过期的截止时刻：预算为 0
	if got := p.eventBudget(time.Now().Add(-time.Second)); got != 0 {
		t.Fatalf("已过期的截止时刻应返回 0, got %v", got)
	}
}

// TestPendingDrainHonorsTotalBudget 一次消息事件的总时长必须有上界：排空循环每批
// 都会派生新预算，若不做累计结算，只要持续有新消息入队就能无限延长（旧实现即
// 如此，配置项 bot.msg_event_timeout_sec 失去意义）。
func TestPendingDrainHonorsTotalBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // 每批固定耗时，便于让总预算先耗尽
		fakePlainChatReply(w, "回复")
	}))
	defer srv.Close()

	const budget = 500 * time.Millisecond
	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.msgEventTimeout = budget
	p.rateCh = make(chan struct{}, 2)
	p.lockStorage = newLockFake()

	rb := &recordBot{}
	key := sessionKey(message.FromUint64(1001), true)

	chat, err := aichat.NewChatBot(srv.URL, "k", "m", "系统提示", 128000, nil, nil)
	if err != nil {
		t.Fatalf("NewChatBot 失败: %v", err)
	}

	// 队列持续有消息涌入：模拟"响应期间不断有人 @ 机器人"，
	// 若总预算不结算，排空循环会一直跟着队列跑下去。
	const sessionID = uint64(1001)
	p.enqueuePending(message.FromUint64(sessionID), true, testGroupMsg(sessionID, "在吗"))
	refillStop := make(chan struct{})
	var refillWG sync.WaitGroup
	refillWG.Add(1)
	go func() {
		defer refillWG.Done()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-refillStop:
				return
			case <-ticker.C:
				p.enqueuePending(message.FromUint64(sessionID), true, testGroupMsg(sessionID, "在吗"))
			}
		}
	}()

	parent, cancelParent := context.WithTimeout(context.Background(), budget)
	defer cancelParent()
	runCtx, eventDeadline, cancelRun := p.bridgeRunContext(parent)
	defer cancelRun()

	start := time.Now()
	// 与 OnGroupMsg 相同的排空循环：每批独立预算，总时长受 eventDeadline 结算
	batch := p.drainPending(message.FromUint64(sessionID), true)
	for len(batch) > 0 {
		next, ok := p.drainChatBatches(runCtx, eventDeadline, rb, message.FromUint64(sessionID), true, chat, batch)
		if !ok {
			break
		}
		batch = next
	}
	elapsed := time.Since(start)
	// 若总预算未结算，循环会一直跟着 refiller 跑下去（远超声明的预算）
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("排空循环未受总预算约束，耗时 %v（预算 %v）", elapsed, budget)
	}
	close(refillStop)
	refillWG.Wait()
	// refiller 已停：此时若总预算结算生效，剩余队列应已被丢弃
	if p.hasPending(key) {
		t.Fatal("总预算耗尽后剩余排队消息应被丢弃")
	}
	sent := rb.sent()
	// 关键不变量：用户确实被告知（不是静默丢弃）。文案可能是插件自己的
	//「本次处理已达时间上限」，也可能是某一批的预算恰与框架 deadline 同刻到期时
	// 由 processChatBatch 输出的「请求超时」——后者语义同样正确（确实是超时）。
	notified := false
	for _, s := range sent {
		if strings.Contains(s, "时间上限") || strings.Contains(s, "请求超时") {
			notified = true
			break
		}
	}
	if !notified {
		t.Fatalf("预算耗尽必须告知用户, got %v", sent)
	}
	// 不应以框架取消文案收尾（会被误读为用户主动停止）
	if n := len(sent); n > 0 && strings.Contains(sent[n-1], "已被停止") {
		t.Fatalf("最后一条提示不应是框架取消文案, got %v", sent)
	}
}

func testGroupMsg(uid uint64, text string) message.Message {
	return message.Message{
		MessageType: "group",
		GroupId:     message.FromUint64(uid),
		Sender:      message.MessageSender{UserId: message.FromUint64(uid), Nickname: "测试"},
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": text}},
		},
	}
}

// TestReclaimSessionQueueReleasesMaps 会话淘汰时必须回收旁挂状态：
// pendingMsgs/sessionInject/noMentionLoaded 都是随会话增长的结构，不回收会随
// 历史会话数无界增长，且清扫器每 10s 都要 Range 全表扫描空条目。
// 队列非空时不删（可能有待消费消息）。
func TestReclaimSessionQueueReleasesMaps(t *testing.T) {
	p := &AIChatPlugin{}
	const key = "g:qq:1001"

	// 空队列 → 回收
	p.getPendingQueue(message.FromUint64(1001), true)
	p.sessionInject.Store(key, "会话启动注入")
	p.noMentionLoaded.Store(key, struct{}{})
	if _, ok := p.pendingMsgs.Load(key); !ok {
		t.Fatal("前置条件：队列应存在")
	}
	p.reclaimSessionQueue(key)
	if _, ok := p.pendingMsgs.Load(key); ok {
		t.Error("空队列应被回收")
	}
	if _, ok := p.sessionInject.Load(key); ok {
		t.Error("待注入上下文应被回收")
	}
	if _, ok := p.noMentionLoaded.Load(key); ok {
		t.Error("未@计数标记应被回收（下次加载会从持久层恢复）")
	}

	// 非空队列 → 保留队列，其余照常回收
	p.enqueuePending(message.FromUint64(1001), true, testGroupMsg(1001, "排队消息"))
	p.sessionInject.Store(key, "x")
	p.reclaimSessionQueue(key)
	if _, ok := p.pendingMsgs.Load(key); !ok {
		t.Error("非空队列不应被回收（消息会被静默丢弃）")
	}
	if _, ok := p.sessionInject.Load(key); ok {
		t.Error("待注入上下文仍应被回收")
	}
}

// fakePlainChatReply 以固定文本应答一次 chat completions 请求（测试用最小响应体）。
func fakePlainChatReply(w http.ResponseWriter, reply string) {
	body, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "m",
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// ---- 收尾回喂 ----

// TestDrainClockSubagentsFeedsBackAlreadyFinished 全部子代理在任务 AI 自己的轮次
// 结束前就已完成时，结果仍必须回喂给 AI 合成最终回复。
//
// 旧实现以 set.hasPending() 作为是否收尾的条件，而 hasPending 只统计未完成的
// 条目：子代理先完成的情况下 pending 为 0，drain 根本不执行，已产出的结果被
// 静默丢弃（token 已消耗、用户只收到"已委派子代理"的中间回复）。
func TestDrainClockSubagentsFeedsBackAlreadyFinished(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		fakePlainChatReply(w, "最终汇总回复")
	}))
	defer srv.Close()

	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, time.Minute, 100)
	chat, err := aichat.NewChatBot(srv.URL, "k", "m", "系统提示", 128000, nil, nil)
	if err != nil {
		t.Fatalf("NewChatBot 失败: %v", err)
	}

	// 子代理已被启动且全部完成：hasPending()==false，hasLaunched()==true
	set := newClockSubagentSet()
	done := make(chan struct{})
	set.add(&clockSubagent{id: "1", task: "调研天气", startTime: time.Now(), done: done})
	set.markFinished("1", "今天晴，25 度", nil)
	if set.hasPending() {
		t.Fatal("前置条件不成立：应无运行中子代理")
	}
	if !set.hasLaunched() {
		t.Fatal("hasLaunched 应报告本次执行启动过子代理")
	}

	task := &ClockTask{ID: "t1", Title: "早报", TargetType: clockTargetGroup, TargetID: "12345"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, _ := m.drainClockSubagents(ctx, task, chat, llmtool.CallBackFuncs{}, set, "上一版回复", aichat.TokenUsage{})

	if len(bodies) == 0 {
		t.Fatal("已完成的子代理结果未被回喂：drainClockSubagents 没有发起汇总请求")
	}
	sent := ""
	if msgs, ok := bodies[0]["messages"].([]any); ok {
		for _, mm := range msgs {
			if m2, ok := mm.(map[string]any); ok && m2["role"] == "user" {
				if s, ok := m2["content"].(string); ok {
					sent = s
				}
			}
		}
	}
	if !strings.Contains(sent, "今天晴，25 度") {
		t.Fatalf("回喂内容缺少子代理结果, got: %s", sent)
	}
	if !strings.Contains(sent, "【子代理执行结果汇总】") {
		t.Fatalf("回喂内容缺少汇总头, got: %s", sent)
	}
	if resp != "最终汇总回复" {
		t.Fatalf("应返回汇总后的最终回复, got %q", resp)
	}
}

// TestDrainClockSubagentsNoopWithoutSubagents 未启动过子代理时不应发起汇总请求。
func TestDrainClockSubagentsNoopWithoutSubagents(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fakePlainChatReply(w, "不该被调用")
	}))
	defer srv.Close()

	p := &AIChatPlugin{
		Meta: plugin.Meta{Name: "AI对话插件"},
		cfg:  aiChatConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxContextTokens: 128000},
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, time.Minute, 100)
	chat, err := aichat.NewChatBot(srv.URL, "k", "m", "系统提示", 128000, nil, nil)
	if err != nil {
		t.Fatalf("NewChatBot 失败: %v", err)
	}
	set := newClockSubagentSet()
	if set.hasLaunched() {
		t.Fatal("空集合不应报告启动过子代理")
	}
	resp, _ := m.drainClockSubagents(context.Background(), &ClockTask{ID: "t2"}, chat, llmtool.CallBackFuncs{}, set, "原回复", aichat.TokenUsage{})
	if calls != 0 {
		t.Fatalf("未启动子代理时不应发起 LLM 请求, got %d", calls)
	}
	if resp != "原回复" {
		t.Fatalf("无子代理时应原样返回, got %q", resp)
	}
}

// ---- newClockSubagentTools ----

func TestNewClockSubagentTools(t *testing.T) {
	p := &AIChatPlugin{}
	task := &ClockTask{ID: "t1", TargetType: clockTargetGroup, TargetID: "12345"}
	tools := newClockSubagentTools(p, nil, task, newClockSubagentSet(), &usageAcc{})
	if len(tools) != 3 {
		t.Fatalf("应创建 3 个子代理工具, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	for _, want := range []string{"subagent_run", "subagent_list", "subagent_cancel"} {
		if !names[want] {
			t.Fatalf("缺少工具 %s", want)
		}
	}
}
