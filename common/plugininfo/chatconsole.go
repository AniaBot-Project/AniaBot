package plugininfo

import "time"

// 试聊室（面板内对话测试）相关数据结构。面板侧栏「试聊室」页把聊天搬到面板上：
// 管理员直接在面板里发消息，插件用真实对话管线（同一套系统提示词、记忆、知识库、
// 工具与上下文压缩）生成回复，并把回复、工具调用与 token 用量回显在面板中。
// 面板试聊不向任何平台发送消息，仅作为对话测试入口。

// 试聊目标类型（ChatConsoleTarget.Kind / ChatConsoleSession.TargetKind）。
const (
	// ChatConsoleTargetTest 通用试聊：不绑定任何会话 scope，只走基础对话与工具
	ChatConsoleTargetTest = "test"
	// ChatConsoleTargetGroup 绑定一个群聊会话：继承该群的 Prompt 覆盖、记忆、知识库、团队等
	ChatConsoleTargetGroup = "group"
	// ChatConsoleTargetFriend 绑定一个私聊会话：同上，按好友维度
	ChatConsoleTargetFriend = "friend"
)

// 试聊消息状态（ChatConsoleMessage.Status）。
const (
	ChatConsoleStatusRunning     = "running"     // AI 正在生成
	ChatConsoleStatusOK          = "ok"          // 正常完成
	ChatConsoleStatusError       = "error"       // 生成失败
	ChatConsoleStatusStopped     = "stopped"     // 被面板「停止」中止
	ChatConsoleStatusTimeout     = "timeout"     // 请求超时
	ChatConsoleStatusInterrupted = "interrupted" // 进程重启导致中断（启动时归一化）
)

// 试聊消息角色（ChatConsoleMessage.Role）。
const (
	ChatConsoleRoleUser      = "user"
	ChatConsoleRoleAssistant = "assistant"
	ChatConsoleRoleSystem    = "system" // 系统提示（自动批准、钩子拦截等），居中展示
)

// ChatConsoleTarget 试聊可选的对话目标，供面板「新建会话」选择器展示。
type ChatConsoleTarget struct {
	Kind     string `json:"kind"`               // test / group / friend
	ID       string `json:"id,omitempty"`       // 群号 / 用户 ID（test 时为空）
	Name     string `json:"name"`               // 展示名（群名 / 备注或昵称）
	Platform string `json:"platform,omitempty"` // 平台标识（如 qq、feishu）
}

// ChatConsoleSession 一个试聊会话。
type ChatConsoleSession struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`                 // 展示标题（默认取目标名）
	TargetKind string    `json:"target_kind"`           // test / group / friend
	TargetID   string    `json:"target_id,omitempty"`   // 目标会话 ID（test 为空）
	TargetName string    `json:"target_name,omitempty"` // 目标展示名
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Count      int       `json:"count"` // 消息条数（含系统提示）
	Busy       bool      `json:"busy"`  // 是否正在生成回复
}

// ChatConsoleSessionCreate 面板新建试聊会话的请求体。
type ChatConsoleSessionCreate struct {
	Title      string `json:"title,omitempty"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

// ChatConsoleAttachment AI 在回复中产出的图片 / 文件（data URI，面板直接展示或下载）。
type ChatConsoleAttachment struct {
	Kind string `json:"kind"`           // image / file
	Name string `json:"name,omitempty"` // 文件名（图片可空）
	URL  string `json:"url"`            // data: URI
	Size int    `json:"size,omitempty"` // 原始字节数
}

// ChatConsoleToolCall 一次工具调用的摘要（完整参数与结果便于在面板核对 AI 行为）。
type ChatConsoleToolCall struct {
	Name       string `json:"name"`
	Arguments  string `json:"arguments,omitempty"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// ChatConsoleUsage 一轮对话的 token 用量与 LLM 轮数。
type ChatConsoleUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	Iterations       int `json:"iterations"`
}

// ChatConsoleMessage 试聊会话里的一条消息。Seq 单调递增，面板按 Seq 增量轮询。
type ChatConsoleMessage struct {
	Seq            int64                   `json:"seq"`
	Role           string                  `json:"role"`   // user / assistant / system
	Text           string                  `json:"text"`   // 正文（生成中为已流式产出的部分）
	Status         string                  `json:"status"` // running / ok / error / stopped / timeout / interrupted
	Error          string                  `json:"error,omitempty"`
	Time           time.Time               `json:"time"`
	DurationMs     int64                   `json:"duration_ms,omitempty"`
	Attachments    []ChatConsoleAttachment `json:"attachments,omitempty"`
	ToolCalls      []ChatConsoleToolCall   `json:"tool_calls,omitempty"`
	ToolCallsTotal int                     `json:"tool_calls_total,omitempty"`
	Usage          *ChatConsoleUsage       `json:"usage,omitempty"`
}
