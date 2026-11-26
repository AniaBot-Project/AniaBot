package pluginaichat

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// send_message 工具：让 AI 在回合执行到一半时主动给用户发一条消息（进度同步、
// 中间结论、先回答简单问题），发送后继续执行，回合结束时照常给出最终回复。
//
// 与 SendText 回调的关系：框架不再自动发送工具轮中间文本，SendText 保留给
// 工具显式发送；send_message 就是它在会话工具层的薄封装，因此天然支持 |||
// 拆分多条、群聊 @ 发起人、合并转发等既有发送语义。
//
// 仅注册到主会话（getChat）：子代理的 SendText 被替换为丢弃（结果经工具结果
// 回灌主 AI），定时任务只统一推送最终回复，它们注册该工具没有意义。

const (
	sendMessageToolName = "send_message"
	// sendMessageMaxPerTurn 单次回合（一批请求，含其中多轮工具循环）内主动发
	// 消息的条数上限：防止长任务里频繁发送刷屏。按批次（processChatBatch →
	// chat.Chat）重置，经请求 ctx 传递。
	sendMessageMaxPerTurn = 8
)

// sendMessageParams send_message 工具参数。
type sendMessageParams struct {
	Text string `json:"text" desc:"要发送给用户的消息文本；可用 ||| 分成多条发送（代码块内除外）"`
}

// sendMessageTool 回合中途主动发消息工具（无状态，发送走当次调用的回调）。
type sendMessageTool struct {
	llmtool.BaseTool[sendMessageParams]
}

// newSendMessageTool 创建主会话的 send_message 工具。
func newSendMessageTool() llmtool.Tool {
	return &sendMessageTool{BaseTool: llmtool.MakeBaseTool(sendMessageToolName,
		"在当前回合执行过程中立刻给用户发送一条消息（进度同步、中间结论、或先回答简单问题）。发送后你仍必须继续完成当前任务，并在回合结束时给出最终回复；不要用它代替最终回复，短任务也不需要，避免刷屏。群聊中会自动 @ 发起本次请求的用户。每个回合最多发送 8 条",
		sendMessageParams{})}
}

// Execute 发送一条中途消息。预算耗尽、空文本或平台不支持时返回错误文本给模型
// （工具错误会回填为工具结果，模型可据此调整）。
func (t *sendMessageTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p, _ := params.(*sendMessageParams)
	if p == nil {
		return "", fmt.Errorf("参数解析失败")
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return "", fmt.Errorf("text 不能为空")
	}
	if callbacks.SendText == nil {
		return "", fmt.Errorf("当前会话不支持主动发送消息")
	}
	if budget, ok := ctx.Value(sendMessageBudgetKey{}).(*sendMessageBudget); ok {
		if !budget.take() {
			return "", fmt.Errorf("本次回合的主动消息条数已达上限（%d 条），请继续执行并直接给出最终回复，下一回合再发送", sendMessageMaxPerTurn)
		}
	}
	if _, err := callbacks.SendText(text); err != nil {
		return "", fmt.Errorf("发送失败: %w", err)
	}
	return "发送成功。注意：这是回合中途的主动消息，请继续完成当前任务，最终回复仍需在回合结束时给出", nil
}

// ---- 单次回合内的主动消息配额（经请求 ctx 传递） ----

// sendMessageBudgetKey 请求 ctx 中挂 sendMessageBudget 的键。
type sendMessageBudgetKey struct{}

// sendMessageBudget 单次回合内剩余可发送条数（工具批次并行执行，需加锁）。
type sendMessageBudget struct {
	mu   sync.Mutex
	left int
}

// withSendMessageBudget 给本次请求挂上主动消息配额；n<=0 时不挂（不加限制）。
func withSendMessageBudget(ctx context.Context, n int) context.Context {
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, sendMessageBudgetKey{}, &sendMessageBudget{left: n})
}

// take 消耗一个配额；返回 false 表示已用完。
func (b *sendMessageBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left <= 0 {
		return false
	}
	b.left--
	return true
}
