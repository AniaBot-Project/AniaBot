package pluginaichat

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// send_message 工具：让 AI 在回合执行到一半时主动给用户发一条消息（进度同步、
// 中间结论、先回答简单问题），发送后继续执行，回合结束时照常给出最终回复。
//
// 与 SendText 回调的关系：框架不再自动发送工具轮中间文本，SendText 保留给
// 工具显式发送；send_message 就是它在会话工具层的薄封装，因此天然支持 |||
// 拆分多条、群聊 @ 发起人、合并转发等既有发送语义。
//
// 不做条数限制：长任务可能跨很多轮工具循环，关键节点该同步就得同步，卡一个
// 数字反而让模型在真正需要时发不出消息。防刷屏交给工具描述里的语义约束
// （只发有信息量的节点、不要逐步汇报、不能代替最终回复），由模型自行判断。
//
// 仅注册到主会话（getChat）：子代理的 SendText 被替换为丢弃（结果经工具结果
// 回灌主 AI），定时任务只统一推送最终回复，它们注册该工具没有意义。

const sendMessageToolName = "send_message"

// sendMessageParams send_message 工具参数。
type sendMessageParams struct {
	Text string `json:"text" desc:"要发送给用户的消息文本；可用 ||| 分成多条发送（代码块内除外）"`
}

// sendMessageTool 回合中途主动发消息工具（无状态，发送走当次调用的回调）。
type sendMessageTool struct {
	llmtool.BaseTool[sendMessageParams]
}

// newSendMessageTool 创建主会话的 send_message 工具。工具描述即发送时机语义：
// 适合关键节点与实质进展，不适合逐步汇报或替代最终回复。
func newSendMessageTool() llmtool.Tool {
	return &sendMessageTool{BaseTool: llmtool.MakeBaseTool(sendMessageToolName,
		"在当前回合执行过程中立刻给用户发送一条消息，用于同步进展或先给出部分结论。适合：长任务的关键节点（已排查清楚问题、完成一个阶段、开始耗时操作）、用户明显在等且有实质进展、或用户顺带问了可以先回答的简单问题。发送后你必须继续完成当前任务，并在回合结束时给出最终回复。不要把每个工具调用都汇报一遍，不要用中途消息代替最终回复，没有新进展不要重复发送；短任务直接给最终回复即可。群聊中会自动 @ 发起本次请求的用户。",
		sendMessageParams{})}
}

// Execute 发送一条中途消息。空文本或平台不支持时返回错误文本给模型（工具错误
// 会回填为工具结果，模型可据此调整）。发送不依赖请求级状态，不查任何配额。
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
	if _, err := callbacks.SendText(text); err != nil {
		return "", fmt.Errorf("发送失败: %w", err)
	}
	return "发送成功。注意：这是回合中途的主动消息，请继续完成当前任务，最终回复仍需在回合结束时给出", nil
}
