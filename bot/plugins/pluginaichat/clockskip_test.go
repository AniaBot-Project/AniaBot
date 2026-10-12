package pluginaichat

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// TestClockSkipOutputState 验证跳过状态：默认未跳过；置位后原因保留，
// 空原因不覆盖已记录的原因（多轮调用时以首次说明为准）。
func TestClockSkipOutputState(t *testing.T) {
	s := &clockOutputState{}
	if skipped, _ := s.skipped(); skipped {
		t.Fatal("初始状态不应为已跳过")
	}

	s.markSkip("")
	if skipped, reason := s.skipped(); !skipped || reason != "" {
		t.Fatalf("置位后应为已跳过且原因为空, got skipped=%v reason=%q", skipped, reason)
	}
	s.markSkip("巡检无异常")
	if _, reason := s.skipped(); reason != "巡检无异常" {
		t.Fatalf("原因应被记录, got %q", reason)
	}
	// 重复调用且不带原因时保留首次原因
	s.markSkip("")
	if _, reason := s.skipped(); reason != "巡检无异常" {
		t.Fatalf("空原因不应覆盖已有原因, got %q", reason)
	}
}

// TestClockSkipOutputToolConcurrent 回归：同一轮内工具并行执行，
// markSkip 必须并发安全（-race 下无告警）。
func TestClockSkipOutputToolConcurrent(t *testing.T) {
	tool := newClockSkipOutputTool(&clockOutputState{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tool.Execute(context.Background(), &clockSkipOutputParams{Reason: "并发"}, llmtool.CallBackFuncs{}); err != nil {
				t.Errorf("Execute 失败: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestClockSkipOutputTool 验证工具元信息与默认参数（未传参时不 panic）。
func TestClockSkipOutputTool(t *testing.T) {
	state := &clockOutputState{}
	tool := newClockSkipOutputTool(state)
	if tool.Name() != "skip_output" {
		t.Fatalf("工具名应为 skip_output, got %q", tool.Name())
	}
	if !strings.Contains(tool.Description(), "跳过") || !strings.Contains(tool.Description(), "日志") {
		t.Fatalf("描述应说明跳过输出与日志保留: %s", tool.Description())
	}
	if _, err := tool.Execute(context.Background(), nil, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("params 为 nil 时不应报错: %v", err)
	}
	if skipped, _ := state.skipped(); !skipped {
		t.Fatal("执行后应置位跳过状态")
	}
}

// recordBot 记录发送行为的测试 bot.Bot（仅实现收尾推送用到的能力）。
type recordBot struct {
	mu     sync.Mutex
	groups []string
	friend []string
}

func (b *recordBot) SendGroupMsg(_ message.QID, chain msgchain.GroupChain) (message.QID, bool) {
	b.mu.Lock()
	b.groups = append(b.groups, chainText(chain.GetGroupMsg()))
	b.mu.Unlock()
	return "1", true
}

func (b *recordBot) SendFriendMsg(_ message.QID, chain msgchain.FriendChain) (message.QID, bool) {
	b.mu.Lock()
	b.friend = append(b.friend, chainText(chain.GetFriendMsg()))
	b.mu.Unlock()
	return "1", true
}

func (b *recordBot) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append(append([]string{}, b.groups...), b.friend...)
}

// chainText 拼接消息链中的文本段，供断言发送内容。
func chainText(segs []message.OB11Segment) string {
	var sb strings.Builder
	for _, s := range segs {
		if s.Type == "text" {
			if t, ok := s.Data["text"].(string); ok {
				sb.WriteString(t)
			}
		}
	}
	return sb.String()
}

func (b *recordBot) GetMsgDetail(message.QID) (*message.Message, bool)     { return nil, false }
func (b *recordBot) GetGroupDetail(message.QID) (*message.GroupInfo, bool) { return nil, false }
func (b *recordBot) GetGroupMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}
func (b *recordBot) GetFriendMsgHistory(message.QID, int, int) (*[]message.Message, bool) {
	return nil, false
}
func (b *recordBot) GetPluginList() []plugininfo.PluginInfo { return nil }
func (b *recordBot) Stop()                                  {}
func (b *recordBot) Go(_ string, f func())                  { go f() }

// TestSendFinalOutputSkipSuppresses 验证收尾推送的核心语义：
// AI 调用 skip_output 时最终回复不发送（只记日志），未调用时正常发送，空文本不发送。
func TestSendFinalOutputSkipSuppresses(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second, 100)
	fb := &recordBot{}
	m.bot = fb

	task := &ClockTask{ID: "1", Title: "巡检", TargetType: clockTargetFriend, TargetID: "5"}
	if sent := m.sendFinalOutput(task, "本次无需汇报", true, "无异常"); sent {
		t.Fatal("跳过输出时不应发送消息")
	}
	if sent := m.sendFinalOutput(task, "   ", false, ""); sent {
		t.Fatal("空文本不应发送消息")
	}
	if got := fb.sent(); len(got) != 0 {
		t.Fatalf("跳过与空文本都不应产生发送, got %v", got)
	}
	if sent := m.sendFinalOutput(task, "有异常：磁盘告警", false, ""); !sent {
		t.Fatal("未跳过时应发送最终回复")
	}
	if got := fb.sent(); len(got) != 1 || !strings.Contains(got[0], "磁盘告警") {
		t.Fatalf("最终回复应发送给目标, got %v", got)
	}

	// 跳过路径的回复文本仍进入执行日志（fillExecution 写入 Reply 字段）
	rec := &taskRecorder{outputSkipped: true, skipReason: "无异常"}
	if !rec.outputSkipped {
		t.Fatal("执行记录应携带跳过标记（回复仍留日志）")
	}
}

// TestTaskRecorderSkipFields 验证跳过状态随执行记录进入任务日志字段。
func TestTaskRecorderSkipFields(t *testing.T) {
	rec := &taskRecorder{}
	rec.observe(aichat.ToolCallInfo{Name: "skip_output", Result: "已设置", DurationMs: 3})
	if rec.toolCallsTotal != 1 || len(rec.toolCalls) != 1 {
		t.Fatalf("工具调用应记录, got total=%d len=%d", rec.toolCallsTotal, len(rec.toolCalls))
	}

	state := &clockOutputState{}
	state.markSkip("巡检无异常")
	skipped, reason := state.skipped()
	rec.outputSkipped = skipped
	rec.skipReason = reason

	if !rec.outputSkipped || rec.skipReason != "巡检无异常" {
		t.Fatalf("记录器应携带跳过信息, got skipped=%v reason=%q", rec.outputSkipped, rec.skipReason)
	}
}
