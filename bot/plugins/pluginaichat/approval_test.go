package pluginaichat

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

func newTestApprovalManager(tools []string, timeout time.Duration) *approvalManager {
	set := make(map[string]struct{}, len(tools))
	for _, name := range tools {
		set[name] = struct{}{}
	}
	return &approvalManager{
		tools:        set,
		timeout:      timeout,
		admins:       []message.QID{message.FromUint64(999)},
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		pending:      make(map[string]*approvalRequest),
		adminPending: make(map[string]*approvalRequest),
	}
}

// testSKey 测试用群会话键（QID 带平台前缀，不能用字面量 "g:1" 代替）
var testSKey = sessionKey(message.FromUint64(1), true)

func TestParseApprovalReply(t *testing.T) {
	cases := []struct {
		text      string
		wantAllow bool
		wantOK    bool
	}{
		{"允许", true, true}, {"同意", true, true}, {"批准", true, true},
		{"allow", true, true}, {"Approve", true, true}, {"yes", true, true}, {"Y", true, true},
		{"拒绝", false, true}, {"deny", false, true}, {"reject", false, true}, {"No", false, true},
		{" 允许 ", true, true}, // 去空白
		{"允许吧", false, false}, {"", false, false}, {"随便", false, false}, {"好的", false, false},
	}
	for _, tc := range cases {
		allow, ok := parseApprovalReply(tc.text)
		if allow != tc.wantAllow || ok != tc.wantOK {
			t.Errorf("%q: got (%v,%v), want (%v,%v)", tc.text, allow, ok, tc.wantAllow, tc.wantOK)
		}
	}
}

// TestApprovalRequestAndReply 审批全流程：提示消息发出 → 发送者回复「允许」→ 放行；
// 管理员亦可批；无关人员回复不消费。
func TestApprovalRequestAndReply(t *testing.T) {
	m := newTestApprovalManager([]string{"file"}, time.Second*5)
	requester := message.FromUint64(123)

	var prompts []string
	var mu sync.Mutex
	sendPrompt := func(text string) {
		mu.Lock()
		prompts = append(prompts, text)
		mu.Unlock()
	}

	done := make(chan struct{})
	var allowed bool
	var reason string
	go func() {
		allowed, reason = m.request(context.Background(), testSKey, "file", "读取 /tmp/a.txt", requester, sendPrompt)
		close(done)
	}()

	// 等 pending 注册
	for range 100 {
		m.mu.Lock()
		_, ok := m.pending[testSKey]
		m.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// 无关人员的审批回复：消费并提示无权，但不影响待批请求
	if consumed, hint := m.tryHandleReply(message.FromUint64(1), true, message.FromUint64(777), "允许"); !consumed || hint == "" {
		t.Fatal("无关人员的审批回复应被消费并提示无权")
	}
	// 非审批内容不消费
	if consumed, _ := m.tryHandleReply(message.FromUint64(1), true, requester, "好的"); consumed {
		t.Fatal("非审批内容不应被消费")
	}
	// 请求者批准
	if consumed, _ := m.tryHandleReply(message.FromUint64(1), true, requester, "允许"); !consumed {
		t.Fatal("请求者的「允许」应被消费")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审批未结束")
	}
	if !allowed || reason != "" {
		t.Fatalf("批准后应放行, allowed=%v reason=%q", allowed, reason)
	}
	mu.Lock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "【工具审批】") || !strings.Contains(prompts[0], "file") {
		t.Fatalf("审批提示不符: %v", prompts)
	}
	mu.Unlock()
}

// TestApprovalAdminOnlyForSyntheticRequester requester=0（子代理/定时任务路径）
// 时仅管理员可批。
func TestApprovalAdminOnlyForSyntheticRequester(t *testing.T) {
	m := newTestApprovalManager(nil, time.Second*5)
	done := make(chan struct{})
	var allowed bool
	go func() {
		allowed, _ = m.request(context.Background(), testSKey, "bash", "执行命令：ls", message.FromUint64(0), func(string) {})
		close(done)
	}()
	for range 100 {
		m.mu.Lock()
		_, ok := m.pending[testSKey]
		m.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if consumed, hint := m.tryHandleReply(message.FromUint64(1), true, message.FromUint64(555), "允许"); !consumed || !strings.Contains(hint, "管理员") {
		t.Fatal("requester=0 时普通用户的审批回复应被消费并提示仅管理员可批")
	}
	if consumed, _ := m.tryHandleReply(message.FromUint64(1), true, message.FromUint64(999), "允许"); !consumed {
		t.Fatal("管理员应可审批")
	}
	<-done
	if !allowed {
		t.Fatal("管理员批准后应放行")
	}
}

// TestApprovalTimeoutAutoDeny 超时无回复自动拒绝。
func TestApprovalTimeoutAutoDeny(t *testing.T) {
	m := newTestApprovalManager(nil, 30*time.Millisecond)
	allowed, reason := m.request(context.Background(), testSKey, "file", "s", message.FromUint64(1), func(string) {})
	if allowed || !strings.Contains(reason, "超时") {
		t.Fatalf("超时应自动拒绝, allowed=%v reason=%q", allowed, reason)
	}
	// 超时后 pending 已清理，后续消息不消费
	if consumed, _ := m.tryHandleReply(message.FromUint64(1), true, message.FromUint64(1), "允许"); consumed {
		t.Fatal("超时结束后不应再消费回复")
	}
}

// TestApprovalSerializedPerSession 同会话并行工具触发的多个审批逐个提示：
// 第二个审批的提示在第一个了结后才发出。
func TestApprovalSerializedPerSession(t *testing.T) {
	m := newTestApprovalManager(nil, time.Second*5)
	requester := message.FromUint64(1)

	var prompts []string
	var mu sync.Mutex
	sendPrompt := func(text string) {
		mu.Lock()
		prompts = append(prompts, text)
		mu.Unlock()
	}

	done1 := make(chan struct{})
	go func() {
		m.request(context.Background(), testSKey, "file", "第一个", requester, sendPrompt)
		close(done1)
	}()
	// 等第一个审批挂起
	for range 100 {
		m.mu.Lock()
		_, ok := m.pending[testSKey]
		m.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	done2 := make(chan struct{})
	go func() {
		m.request(context.Background(), testSKey, "file", "第二个", requester, sendPrompt)
		close(done2)
	}()
	// 第二个不应在第一个了结前发提示
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(prompts) != 1 {
		t.Fatalf("第二个审批应等待第一个了结, prompts=%v", prompts)
	}
	mu.Unlock()

	m.tryHandleReply(message.FromUint64(1), true, requester, "拒绝")
	<-done1
	// 第一个了结后第二个提示发出
	for range 100 {
		mu.Lock()
		n := len(prompts)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "第二个") {
		t.Fatalf("第二个审批提示不符: %v", prompts)
	}
	mu.Unlock()
	m.tryHandleReply(message.FromUint64(1), true, requester, "允许")
	<-done2
}

// TestGateLegOrdering 门禁顺序固定为 计划模式 → 钩子 → 审批：被前腿否决的工具
// 不应再打扰后腿（钩子不执行、不发起审批）。
func TestGateLegOrdering(t *testing.T) {
	var legs []string
	var mu sync.Mutex
	record := func(s string) { mu.Lock(); legs = append(legs, s); mu.Unlock() }

	// 钩子腿：阻断 hookblocked，记录执行
	hm := agenthook.NewManager(nil, "files.hooks_json", nil)
	hm.SetEnabled(true)
	hm.SetGoHandlers([]agenthook.Handler{hookRecordFunc(func(ev agenthook.Event, p agenthook.Payload) agenthook.Result {
		record("hook:" + p.ToolName)
		if p.ToolName == "hookblocked" {
			return agenthook.Result{Block: true, Reason: "钩子拒绝"}
		}
		return agenthook.Result{}
	})})

	am := newTestApprovalManager([]string{"apprtool"}, time.Second)
	// 审批腿：记录并立即由管理员批准（pending 注册先于 sendPrompt，可同步回复）
	p := &AIChatPlugin{planManager: newPlanManager(), hookManager: hm, approvalManager: am}
	requester := message.FromUint64(1)
	sendPrompt := func(text string) {
		record("approval")
		am.tryHandleReply(message.FromUint64(1), true, message.FromUint64(999), "允许")
	}
	gate := p.buildPreToolGate(testSKey, agenthook.AgentKindMain, requester, sendPrompt, nil)
	call := func(name string) (bool, string) {
		return gate(context.Background(), llmtool.ToolCall{Name: name, Arguments: `{"a":  1}`})
	}

	// 1. 计划模式腿阻断：钩子/审批不执行
	p.planManager.Set(testSKey, true)
	if blocked, res := call("bash"); !blocked || !strings.Contains(res, "计划模式") {
		t.Fatalf("计划模式应阻断 bash, got %v %q", blocked, res)
	}
	mu.Lock()
	if len(legs) != 0 {
		t.Fatalf("计划模式阻断后不应执行后续腿, legs=%v", legs)
	}
	mu.Unlock()
	p.planManager.Set(testSKey, false)

	// 2. 钩子腿阻断：审批不执行
	if blocked, res := call("hookblocked"); !blocked || !strings.Contains(res, "钩子拒绝") {
		t.Fatalf("钩子应阻断, got %v %q", blocked, res)
	}
	mu.Lock()
	if len(legs) != 1 || legs[0] != "hook:hookblocked" {
		t.Fatalf("钩子阻断后不应发起审批, legs=%v", legs)
	}
	mu.Unlock()

	// 3. 前两腿放行 → 审批腿执行（管理员批准 → 放行）
	blocked, _ := call("apprtool")
	mu.Lock()
	got := append([]string(nil), legs...)
	mu.Unlock()
	if blocked {
		t.Fatal("管理员批准后应放行")
	}
	want := []string{"hook:hookblocked", "hook:apprtool", "approval"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("门禁腿执行顺序不符: got %v, want %v", got, want)
	}

	// 4. 三门全放行
	if blocked, _ := call("time"); blocked {
		t.Fatal("只读工具应全部放行")
	}
}

type hookRecordFunc func(ev agenthook.Event, p agenthook.Payload) agenthook.Result

func (f hookRecordFunc) OnAgentHook(_ context.Context, ev agenthook.Event, p agenthook.Payload) agenthook.Result {
	return f(ev, p)
}

// TestAdminApprovalOnlyAdminCanApprove 配置修改类工具恒走管理员审批腿：
// requester 非 0 时请求者本人也不能批准，仅管理员可批（无管理员私聊通道时
// 提示回退到发起会话）。
func TestAdminApprovalOnlyAdminCanApprove(t *testing.T) {
	am := newTestApprovalManager(nil, time.Second*5)
	p := &AIChatPlugin{approvalManager: am}
	gate := p.buildPreToolGate(testSKey, agenthook.AgentKindMain, message.FromUint64(123), func(string) {}, nil)

	done := make(chan struct{})
	var blocked bool
	var result string
	go func() {
		blocked, result = gate(context.Background(), llmtool.ToolCall{Name: "config_set", Arguments: `{"key":"a","value":"b"}`})
		close(done)
	}()
	// 等 pending 注册
	for range 100 {
		am.mu.Lock()
		_, ok := am.pending[testSKey]
		am.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// 请求者本人不能批准（管理员审批语义）：审批回复被消费并提示仅管理员可批
	if consumed, hint := am.tryHandleReply(message.FromUint64(1), true, message.FromUint64(123), "允许"); !consumed || !strings.Contains(hint, "管理员") {
		t.Fatal("配置修改需管理员审批，请求者本人的审批回复应被消费并提示")
	}
	// 管理员批准后放行
	if consumed, _ := am.tryHandleReply(message.FromUint64(1), true, message.FromUint64(999), "允许"); !consumed {
		t.Fatal("管理员应可审批")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审批未结束")
	}
	if blocked {
		t.Fatalf("管理员批准后应放行, result=%q", result)
	}
}

// TestAdminApprovalViaPrivateChat 管理员审批提示私聊发给管理员，管理员在私聊中
// 回复「允许」放行；发起会话只收到「已通知管理员」提示。
func TestAdminApprovalViaPrivateChat(t *testing.T) {
	am := newTestApprovalManager(nil, time.Second*5)
	p := &AIChatPlugin{approvalManager: am}

	var adminPrompts, originPrompts []string
	var mu sync.Mutex
	gate := p.buildPreToolGate(testSKey, agenthook.AgentKindMain, message.FromUint64(123),
		func(text string) {
			mu.Lock()
			originPrompts = append(originPrompts, text)
			mu.Unlock()
		},
		func(text string) bool {
			mu.Lock()
			adminPrompts = append(adminPrompts, text)
			mu.Unlock()
			return true
		})

	done := make(chan struct{})
	var blocked bool
	go func() {
		blocked, _ = gate(context.Background(), llmtool.ToolCall{Name: "config_set", Arguments: `{"key":"a"}`})
		close(done)
	}()
	// 等管理员私聊索引登记
	adminSKey := sessionKey(message.FromUint64(999), false)
	for range 100 {
		am.mu.Lock()
		_, ok := am.adminPending[adminSKey]
		am.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// 管理员私聊回复放行
	if consumed, _ := am.tryHandleReply(message.FromUint64(999), false, message.FromUint64(999), "允许"); !consumed {
		t.Fatal("管理员私聊中的「允许」应被消费")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审批未结束")
	}
	if blocked {
		t.Fatal("管理员批准后应放行")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(adminPrompts) != 1 || !strings.Contains(adminPrompts[0], "【工具审批】") || !strings.Contains(adminPrompts[0], "config_set") {
		t.Fatalf("审批提示应发给管理员私聊: %v", adminPrompts)
	}
	if len(originPrompts) != 1 || !strings.Contains(originPrompts[0], "已通知管理员") {
		t.Fatalf("发起会话应收到已通知提示: %v", originPrompts)
	}
}

// TestAdminApprovalAnyAdminCanApprove 配置多个管理员时审批提示发给全部管理员，
// 任一管理员在私聊中回复「允许」均可批准。
func TestAdminApprovalAnyAdminCanApprove(t *testing.T) {
	am := newTestApprovalManager(nil, time.Second*5)
	am.admins = []message.QID{message.FromUint64(999), message.FromUint64(888)}
	p := &AIChatPlugin{approvalManager: am}

	var adminPrompts []string
	var mu sync.Mutex
	gate := p.buildPreToolGate(testSKey, agenthook.AgentKindMain, message.FromUint64(123),
		func(string) {},
		func(text string) bool {
			mu.Lock()
			adminPrompts = append(adminPrompts, text)
			mu.Unlock()
			return true
		})

	done := make(chan struct{})
	var blocked bool
	go func() {
		blocked, _ = gate(context.Background(), llmtool.ToolCall{Name: "config_set", Arguments: `{}`})
		close(done)
	}()

	adminKeys := []string{
		sessionKey(message.FromUint64(999), false),
		sessionKey(message.FromUint64(888), false),
	}
	for range 100 {
		ok := true
		am.mu.Lock()
		for _, key := range adminKeys {
			if _, exists := am.adminPending[key]; !exists {
				ok = false
			}
		}
		am.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// 第二位管理员批准（第一位未回复）
	if consumed, _ := am.tryHandleReply(message.FromUint64(888), false, message.FromUint64(888), "允许"); !consumed {
		t.Fatal("任一管理员私聊中的「允许」应被消费")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审批未结束")
	}
	if blocked {
		t.Fatal("任一管理员批准后应放行")
	}

	mu.Lock()
	defer mu.Unlock()
	// 提示闭包只调用一次（由闭包自身分发给全部管理员）
	if len(adminPrompts) != 1 || !strings.Contains(adminPrompts[0], "config_set") {
		t.Fatalf("审批提示应发给管理员通道: %v", adminPrompts)
	}
	am.mu.Lock()
	for _, key := range adminKeys {
		if _, ok := am.adminPending[key]; ok {
			am.mu.Unlock()
			t.Fatalf("审批结束后应清理管理员私聊索引 %s", key)
		}
	}
	am.mu.Unlock()
}

// TestAdminApprovalOriginReplyStillWorks 提示发给管理员私聊后，管理员在发起
// 会话（群聊）中回复「允许」同样可批；非管理员回复不消费。
func TestAdminApprovalOriginReplyStillWorks(t *testing.T) {
	am := newTestApprovalManager(nil, time.Second*5)
	p := &AIChatPlugin{approvalManager: am}
	gate := p.buildPreToolGate(testSKey, agenthook.AgentKindMain, message.FromUint64(123),
		func(string) {}, func(string) bool { return true })

	done := make(chan struct{})
	var blocked bool
	go func() {
		blocked, _ = gate(context.Background(), llmtool.ToolCall{Name: "config_set", Arguments: `{}`})
		close(done)
	}()
	for range 100 {
		am.mu.Lock()
		_, ok := am.pending[testSKey]
		am.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// 非管理员在发起会话的审批回复：消费并提示，不影响待批请求
	if consumed, hint := am.tryHandleReply(message.FromUint64(1), true, message.FromUint64(123), "允许"); !consumed || hint == "" {
		t.Fatal("非管理员的审批回复应被消费并提示")
	}
	// 管理员在发起会话回复放行
	if consumed, _ := am.tryHandleReply(message.FromUint64(1), true, message.FromUint64(999), "允许"); !consumed {
		t.Fatal("管理员在发起会话的回复应被消费")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审批未结束")
	}
	if blocked {
		t.Fatal("管理员批准后应放行")
	}
}

// TestAdminApprovalFallbackToOrigin 管理员私聊发送失败时回退到发起会话提示。
func TestAdminApprovalFallbackToOrigin(t *testing.T) {
	am := newTestApprovalManager(nil, time.Second*5)
	p := &AIChatPlugin{approvalManager: am}
	var originPrompts []string
	var mu sync.Mutex
	gate := p.buildPreToolGate(testSKey, agenthook.AgentKindMain, message.FromUint64(123),
		func(text string) {
			mu.Lock()
			originPrompts = append(originPrompts, text)
			mu.Unlock()
		},
		func(string) bool { return false })

	done := make(chan struct{})
	var blocked bool
	go func() {
		blocked, _ = gate(context.Background(), llmtool.ToolCall{Name: "config_set", Arguments: `{}`})
		close(done)
	}()
	for range 100 {
		am.mu.Lock()
		_, ok := am.pending[testSKey]
		am.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	if len(originPrompts) != 1 || !strings.Contains(originPrompts[0], "【工具审批】") {
		t.Fatalf("私聊失败应回退到发起会话提示: %v", originPrompts)
	}
	mu.Unlock()
	am.tryHandleReply(message.FromUint64(1), true, message.FromUint64(999), "允许")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审批未结束")
	}
	if blocked {
		t.Fatal("管理员批准后应放行")
	}
}

// TestAdminApprovalRefusedBlocks 管理员拒绝时配置修改工具被阻断。
func TestAdminApprovalRefusedBlocks(t *testing.T) {
	am := newTestApprovalManager(nil, time.Second*5)
	p := &AIChatPlugin{approvalManager: am}
	gate := p.buildPreToolGate(testSKey, agenthook.AgentKindMain, message.FromUint64(123), func(string) {}, nil)

	done := make(chan struct{})
	var blocked bool
	var result string
	go func() {
		blocked, result = gate(context.Background(), llmtool.ToolCall{Name: "config_file_set", Arguments: `{"name":"hooks","content":"{}"}`})
		close(done)
	}()
	for range 100 {
		am.mu.Lock()
		_, ok := am.pending[testSKey]
		am.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	am.tryHandleReply(message.FromUint64(1), true, message.FromUint64(999), "拒绝")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("审批未结束")
	}
	if !blocked || !strings.Contains(result, "未获管理员批准") {
		t.Fatalf("管理员拒绝后应阻断, blocked=%v result=%q", blocked, result)
	}
}

// TestSummarizeApprovalArgs 审批摘要：config_file_set 展示完整内容（压缩空白），
// 其他工具走通用截断摘要。
func TestSummarizeApprovalArgs(t *testing.T) {
	args := `{"name":  "hooks",  "content":   "{\"a\":1}"}`
	got := summarizeApprovalArgs(llmtool.ToolCall{Name: "config_file_set", Arguments: args})
	if got != `{"name": "hooks", "content": "{\"a\":1}"}` {
		t.Fatalf("config_file_set 摘要应展示完整内容, got %q", got)
	}
	// 超长内容截断并提示
	long := strings.Repeat("x", 5000)
	got = summarizeApprovalArgs(llmtool.ToolCall{Name: "config_file_set", Arguments: `"` + long + `"`})
	if !strings.Contains(got, "已截断") || len([]rune(got)) > 4200 {
		t.Fatalf("超长内容应截断, len=%d", len([]rune(got)))
	}
	// 其他工具沿用 300 符文通用摘要
	short := summarizeApprovalArgs(llmtool.ToolCall{Name: "file", Arguments: args})
	if short == got || !strings.Contains(short, "hooks") {
		t.Fatalf("普通工具摘要异常: %q", short)
	}
}
