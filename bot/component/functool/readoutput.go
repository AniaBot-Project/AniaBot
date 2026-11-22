package functool

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// ReadToolOutputParams read_tool_output 工具参数。
//
// 工具结果超过统一内联上限时，宿主会把完整内容写入会话临时文件，回填文本以
// 「【工具结果已落盘】handle=…」开头；本工具按 handle 分段读回。之所以不自带
// read_file/bash：文件工具与 bash 默认关闭，且落盘目录不在 file_tools.root 的
// 允许范围内，专用工具在任何会话都可用。
type ReadToolOutputParams struct {
	Handle  string `json:"handle" desc:"被截断的工具结果中给出的 handle（形如 bash_a1b2c3d4）"`
	Offset  int    `json:"offset,omitempty" desc:"起始行号（从 1 开始），默认 1；pattern 非空时忽略"`
	Limit   int    `json:"limit,omitempty" desc:"最多读取的行数，默认 200，最大 2000"`
	FromEnd bool   `json:"from_end,omitempty" desc:"true 时读取文件末尾的 limit 行（查看日志/测试结果结尾常用）"`
	Pattern string `json:"pattern,omitempty" desc:"可选：按字面子串过滤行（不区分大小写，不是正则），最多返回 200 个匹配行并附行号"`
}

// ReadToolOutputTool 读取被落盘的工具结果。
type ReadToolOutputTool struct {
	llmtool.BaseTool[ReadToolOutputParams]
}

// NewReadToolOutputTool 创建 read_tool_output 工具。仅在工具结果落盘功能启用时
// 注册到共享执行器（见 pluginaichat 的 Start）。
func NewReadToolOutputTool() *ReadToolOutputTool {
	return &ReadToolOutputTool{
		BaseTool: llmtool.MakeBaseTool(
			"read_tool_output",
			"读取因超长而被落盘的工具结果。当某个工具结果以「【工具结果已落盘】handle=…」开头时，完整内容已保存，用本工具分段读取：先读开头（offset/limit）、按关键词搜行（pattern），日志或测试结果可用 from_end=true 看结尾。不要为了查看完整输出而重复执行原工具（可能重复产生副作用、浪费上下文）；handle 提示已过期时再重新执行原工具。",
			ReadToolOutputParams{},
		),
	}
}

func (t *ReadToolOutputTool) Execute(_ context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*ReadToolOutputParams)
	if !ok {
		return "", fmt.Errorf("read_tool_output: 参数类型错误")
	}
	handle := strings.TrimSpace(p.Handle)
	if handle == "" {
		return "", fmt.Errorf("read_tool_output: handle 不能为空（使用工具结果落盘提示中给出的 handle）")
	}
	if callbacks.ReadToolOutput == nil {
		return "当前会话不支持读取落盘的工具结果", nil
	}
	return callbacks.ReadToolOutput(handle, p.Offset, p.Limit, p.FromEnd, strings.TrimSpace(p.Pattern))
}
