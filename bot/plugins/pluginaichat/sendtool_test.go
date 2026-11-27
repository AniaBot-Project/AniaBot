package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// TestSendMessageToolSends 工具成功路径：经 SendText 回调发出文本、返回提示模型
// 继续执行的结果文本。
func TestSendMessageToolSends(t *testing.T) {
	tool := newSendMessageTool()
	if tool.Name() != "send_message" {
		t.Fatalf("工具名 = %q, want send_message", tool.Name())
	}
	if !strings.Contains(tool.Description(), "最终回复") {
		t.Fatalf("工具描述应说明仍需给出最终回复: %s", tool.Description())
	}

	var sent []string
	cbs := llmtool.CallBackFuncs{SendText: func(s string) (string, error) {
		sent = append(sent, s)
		return "发送成功", nil
	}}
	out, err := tool.Execute(context.Background(), &sendMessageParams{Text: "  已定位到问题，正在修复  "}, cbs)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(sent) != 1 || sent[0] != "已定位到问题，正在修复" {
		t.Fatalf("应发送去空白后的文本, got %v", sent)
	}
	if !strings.Contains(out, "继续完成") {
		t.Fatalf("返回结果应提示继续执行: %q", out)
	}
}

// TestSendMessageToolRejectsInvalid 空文本、参数为 nil、回调缺失时返回错误。
func TestSendMessageToolRejectsInvalid(t *testing.T) {
	tool := newSendMessageTool()
	cbs := llmtool.CallBackFuncs{SendText: func(string) (string, error) { return "", nil }}

	if _, err := tool.Execute(context.Background(), nil, cbs); err == nil {
		t.Fatal("params 为 nil 时应报错")
	}
	if _, err := tool.Execute(context.Background(), &sendMessageParams{Text: "   "}, cbs); err == nil {
		t.Fatal("空文本应报错")
	}
	if _, err := tool.Execute(context.Background(), &sendMessageParams{Text: "hi"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("回调缺失时应报错")
	}
}

// TestSendMessageToolSendFailure 平台发送失败时把错误回填给模型。
func TestSendMessageToolSendFailure(t *testing.T) {
	tool := newSendMessageTool()
	cbs := llmtool.CallBackFuncs{SendText: func(string) (string, error) {
		return "", errors.New("平台发送失败")
	}}
	if _, err := tool.Execute(context.Background(), &sendMessageParams{Text: "进展"}, cbs); err == nil ||
		!strings.Contains(err.Error(), "平台发送失败") {
		t.Fatalf("发送失败应返回错误, got %v", err)
	}
}

// TestSendMessageToolNoTurnCap 中途发消息不做条数限制：长任务在关键节点多次
// 同步进展不应被拦下（是否该发由工具描述的语义约束，而不是硬上限）。
func TestSendMessageToolNoTurnCap(t *testing.T) {
	tool := newSendMessageTool()
	sent := 0
	cbs := llmtool.CallBackFuncs{SendText: func(string) (string, error) {
		sent++
		return "发送成功", nil
	}}
	for i := 0; i < 20; i++ {
		if _, err := tool.Execute(context.Background(), &sendMessageParams{Text: fmt.Sprintf("进展 %d", i)}, cbs); err != nil {
			t.Fatalf("第 %d 条不应被限制: %v", i+1, err)
		}
	}
	if sent != 20 {
		t.Fatalf("全部调用都应发出消息, sent=%d", sent)
	}
	if strings.Contains(tool.Description(), "最多") {
		t.Fatalf("工具描述不应再声明条数上限: %s", tool.Description())
	}
}
