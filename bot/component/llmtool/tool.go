package llmtool

import "context"

type Tool interface {
	Name() string
	Description() string
	Params() any
	Execute(ctx context.Context, params any, callbacks CallBackFuncs) (string, error)
}

type CallBackFuncs struct {
	SendText          func(text string) (string, error)
	SendImage         func(bs64content string) (string, error)
	SendFile          func(name, bs64content string) (string, error)
	GetPrivateFileURL func(fileId string) (string, error)
	// LoadImages 按哈希加载指定图片（哈希取自消息文本中的 [图片 <hash> url:<url>]
	// 标记，如当前消息、历史消息记录或合并转发内容）。hashes 为空时
	// 不加载任何图片，返回引导提示。nil 表示当前会话不支持加载图片。
	LoadImages       func(hashes []string) (string, error)
	TakeLoadedImages func() []string
	// LoadLocalImage 读取本地图片文件供 LLM 查看：主模型支持多模态时把 data URI
	// 推入待加载图片队列（下一轮上下文提供），否则交由备用识别模型描述。
	// 回调返回给 LLM 的提示文本；nil 表示当前会话不支持读取本地图片。
	LoadLocalImage func(path string) (string, error)
	// RequestApproval 请求人工批准一次危险操作（如 bash 三段式中既不在白名单也
	// 不在黑名单的命令）。返回 allowed=false 时 reason 说明原因（拒绝/超时/取消）。
	// nil 表示当前环境不支持审批（此时 bash 未列名命令默认放行，只认黑名单）。
	// 注意：本回调可能阻塞至审批超时（默认 120s），调用方与包装层不得持锁调用/包装它。
	RequestApproval func(ctx context.Context, toolName, summary string) (allowed bool, reason string)
	// LimitToolResult 工具结果的统一长度治理：成功结果超长时由宿主把完整内容
	// 落盘，返回"预览 + handle + 读取指引"；isError=true 时仅做头尾截断（错误
	// 必须当轮可见、可恢复）。返回替换后的回填文本。nil 表示不做治理。
	// 由 ToolOrchestrator 在结果回填前调用（见 aichat/toolexecutor.go）。
	LimitToolResult func(toolName, result string, isError bool) string
	// ReadToolOutput 读取被 LimitToolResult 落盘的工具结果，供 read_tool_output
	// 工具使用。handle 由落盘提示给出；offset 从 1 开始；fromEnd=true 时读末尾；
	// pattern 为字面子串过滤（非正则）。nil 表示当前会话不支持读取。
	ReadToolOutput func(handle string, offset, limit int, fromEnd bool, pattern string) (string, error)
}
