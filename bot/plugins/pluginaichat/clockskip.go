package pluginaichat

import (
	"context"
	"strings"
	"sync"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// clockOutputState 一次定时任务执行的输出控制状态：AI 调用 skip_output 后置位，
// 任务收尾时据此抑制最终回复推送（执行日志正常保留）。同一轮内的多个工具调用
// 并行执行，状态读写需并发安全。
type clockOutputState struct {
	mu     sync.Mutex
	skip   bool
	reason string
}

// markSkip 记录「本次执行不推送最终回复」；reason 为空时保留已记录的原因。
func (s *clockOutputState) markSkip(reason string) {
	s.mu.Lock()
	s.skip = true
	if reason != "" {
		s.reason = reason
	}
	s.mu.Unlock()
}

// skipped 返回是否已请求跳过输出及原因。
func (s *clockOutputState) skipped() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skip, s.reason
}

// clockSkipOutputParams skip_output 的参数。
type clockSkipOutputParams struct {
	Reason string `json:"reason,omitempty" desc:"跳过原因，仅记入执行日志，便于后续排查"`
}

// clockSkipOutputTool 定时任务专用工具：调用后本次执行的最终回复不再推送给目标。
//
// 定时任务只把「最终回复」推送给触发对象，巡检类任务在无需汇报时本可以什么都不说，
// 但模型通常仍会输出一段总结。本工具让 AI 显式声明「本次保持安静」，把最终输出
// 留在执行日志里而不发送给目标。
type clockSkipOutputTool struct {
	llmtool.BaseTool[clockSkipOutputParams]
	output *clockOutputState
}

// 两种场景共用同一个工具与状态，描述按场景区分：定时任务是「无需向目标汇报」，
// 主动互动是「此刻没话可说」。
const (
	clockSkipOutputDesc = "跳过本次定时任务的最终输出：调用后本次执行结束时不会向目标发送最终回复（只保留执行日志），适用于巡检/监控类任务确认无需汇报时保持安静。" +
		"请在完成全部检查、确认确实无需汇报后再调用（若委派了子代理，等结果汇总后再决定），一经调用不可撤销；" +
		"确有内容要发送给目标时不要调用本工具。调用后可直接结束本次执行，之后输出的文本不会被发送。"

	proactiveSkipOutputDesc = "跳过本次主动发言：调用后本次执行结束时不向会话发送任何内容（跳过原因记入意图记录，可在 /proactive log 查看），用于判断此刻确实没有可说的话题。" +
		"这是主动开口前保持安静的唯一出口；已经决定要说的话就不要调用本工具。调用后可直接结束本次执行，之后输出的文本不会被发送。"
)

// newSkipOutputTool 构造 skip_output 工具：desc 由调用场景给出，output 承载
// 「本轮不输出最终回复」的状态。
func newSkipOutputTool(output *clockOutputState, desc string) llmtool.Tool {
	return &clockSkipOutputTool{
		BaseTool: llmtool.MakeBaseTool("skip_output", desc, clockSkipOutputParams{}),
		output:   output,
	}
}

// newClockSkipOutputTool 定时任务场景的 skip_output。
func newClockSkipOutputTool(output *clockOutputState) llmtool.Tool {
	return newSkipOutputTool(output, clockSkipOutputDesc)
}

// newProactiveSkipOutputTool 主动互动场景的 skip_output（描述不再提「任务」与
// 「执行日志」，主动互动只有意图记录）。
func newProactiveSkipOutputTool(output *clockOutputState) llmtool.Tool {
	return newSkipOutputTool(output, proactiveSkipOutputDesc)
}

func (t *clockSkipOutputTool) Execute(_ context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	reason := ""
	if p, ok := params.(*clockSkipOutputParams); ok && p != nil {
		reason = strings.TrimSpace(p.Reason)
	}
	t.output.markSkip(reason)
	return "已设置：本次执行的最终回复不会发送（原因已记录）。如无其他操作，可直接结束本次执行。", nil
}
