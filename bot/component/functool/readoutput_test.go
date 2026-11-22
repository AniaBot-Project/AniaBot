package functool

import (
	"context"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// TestReadToolOutputCallsCallback 工具把参数原样转交宿主回调，并透传读回文本。
func TestReadToolOutputCallsCallback(t *testing.T) {
	tool := NewReadToolOutputTool()
	var gotHandle string
	var gotOffset, gotLimit int
	var gotFromEnd bool
	var gotPattern string
	cbs := llmtool.CallBackFuncs{
		ReadToolOutput: func(handle string, offset, limit int, fromEnd bool, pattern string) (string, error) {
			gotHandle, gotOffset, gotLimit, gotFromEnd, gotPattern = handle, offset, limit, fromEnd, pattern
			return "CONTENT", nil
		},
	}
	out, err := tool.Execute(context.Background(), &ReadToolOutputParams{
		Handle:  " bash_ab12cd34 ",
		Offset:  7,
		Limit:   30,
		FromEnd: true,
		Pattern: " error ",
	}, cbs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "CONTENT" {
		t.Fatalf("out = %q, want CONTENT", out)
	}
	if gotHandle != "bash_ab12cd34" || gotOffset != 7 || gotLimit != 30 || !gotFromEnd || gotPattern != "error" {
		t.Fatalf("参数转交不符: handle=%q offset=%d limit=%d fromEnd=%v pattern=%q",
			gotHandle, gotOffset, gotLimit, gotFromEnd, gotPattern)
	}
}

// TestReadToolOutputValidation 空 handle 报错；回调缺失时返回明确提示而不是 panic。
func TestReadToolOutputValidation(t *testing.T) {
	tool := NewReadToolOutputTool()
	if _, err := tool.Execute(context.Background(), &ReadToolOutputParams{}, llmtool.CallBackFuncs{}); err == nil ||
		!strings.Contains(err.Error(), "handle") {
		t.Fatalf("空 handle 应报错，got %v", err)
	}
	out, err := tool.Execute(context.Background(), &ReadToolOutputParams{Handle: "bash_ab12cd34"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("未注册回调不应返回错误: %v", err)
	}
	if !strings.Contains(out, "不支持") {
		t.Fatalf("未注册回调应返回明确提示: %q", out)
	}
}

// TestReadToolOutputParams 工具名与参数 schema 是确定契约（提示词与模型依赖）。
func TestReadToolOutputParams(t *testing.T) {
	tool := NewReadToolOutputTool()
	if tool.Name() != "read_tool_output" {
		t.Fatalf("name = %q", tool.Name())
	}
	if _, ok := tool.Params().(*ReadToolOutputParams); !ok {
		t.Fatalf("params 类型不符: %T", tool.Params())
	}
	if !strings.Contains(tool.Description(), "handle") {
		t.Fatalf("描述应说明 handle: %q", tool.Description())
	}
}
