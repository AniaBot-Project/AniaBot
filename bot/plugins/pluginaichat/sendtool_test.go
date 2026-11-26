package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// TestSendMessageToolSends 工具成功路径：经 SendText 回调发出文本、标记回合已
// 产生可见输出（抑制受理提示）、返回提示模型继续执行的结果文本。
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
	ctx, act := withTurnActivity(context.Background())
	out, err := tool.Execute(ctx, &sendMessageParams{Text: "  已定位到问题，正在修复  "}, cbs)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(sent) != 1 || sent[0] != "已定位到问题，正在修复" {
		t.Fatalf("应发送去空白后的文本, got %v", sent)
	}
	if !act.visible.Load() {
		t.Fatal("工具发出消息后应标记回合已有可见输出")
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

// TestSendMessageToolFailureDoesNotMarkVisible 发送失败时不标记回合已有可见输出
// （否则长任务受理提示会被误吞，用户长时间收不到任何反馈）。
func TestSendMessageToolFailureDoesNotMarkVisible(t *testing.T) {
	tool := newSendMessageTool()
	cbs := llmtool.CallBackFuncs{SendText: func(string) (string, error) {
		return "", errors.New("平台发送失败")
	}}
	ctx, act := withTurnActivity(context.Background())
	if _, err := tool.Execute(ctx, &sendMessageParams{Text: "进展"}, cbs); err == nil {
		t.Fatal("发送失败应返回错误")
	}
	if act.visible.Load() {
		t.Fatal("发送失败不应标记回合已有可见输出")
	}
}

// TestSendMessageToolBudget 单次回合内的发送配额：用完后再调用返回上限错误；
// 新回合（新配额）重新可用。
func TestSendMessageToolBudget(t *testing.T) {
	tool := newSendMessageTool()
	sent := 0
	cbs := llmtool.CallBackFuncs{SendText: func(string) (string, error) {
		sent++
		return "发送成功", nil
	}}
	ctx := withSendMessageBudget(context.Background(), 2)
	for i := 0; i < 2; i++ {
		if _, err := tool.Execute(ctx, &sendMessageParams{Text: fmt.Sprintf("进展 %d", i)}, cbs); err != nil {
			t.Fatalf("第 %d 条不应超限: %v", i+1, err)
		}
	}
	if _, err := tool.Execute(ctx, &sendMessageParams{Text: "第三条"}, cbs); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超出配额应返回上限错误, got %v", err)
	}
	if sent != 2 {
		t.Fatalf("超限调用不应发出消息, sent=%d", sent)
	}

	// 新请求 = 新配额
	if _, err := tool.Execute(withSendMessageBudget(context.Background(), 1), &sendMessageParams{Text: "下一轮"}, cbs); err != nil {
		t.Fatalf("新请求的配额应重新可用: %v", err)
	}
}
