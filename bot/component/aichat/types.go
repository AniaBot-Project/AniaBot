package aichat

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// MessageRole 消息角色
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleDeveloper MessageRole = "developer"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// ContentPartType 内容片段类型
type ContentPartType int

const (
	ContentPartText ContentPartType = iota
	ContentPartImageURL
)

// ContentPart 消息内容片段（文本或图片）
type ContentPart struct {
	Type     ContentPartType `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL string          `json:"image_url,omitempty"`
}

// Message 对话消息
type Message struct {
	Role       MessageRole        `json:"role"`
	Parts      []ContentPart      `json:"parts,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"` // tool 结果消息使用
	ToolCalls  []llmtool.ToolCall `json:"tool_calls,omitempty"`   // assistant 消息使用
	// ReasoningContent 保存 API 返回的推理过程内容（如 DeepSeek 的 reasoning_content），
	// 在多轮对话（特别是 tool calling）中需要原样传回，否则 API 会报错。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ThinkingBlocks 保存 Anthropic 格式的思考块（含 signature / redacted data，
	// JSON 数组，元素见 thinkingBlock），tool calling 多轮中 Anthropic 要求原样回传；
	// 仅 anthropic 格式写入，其他格式恒为空。
	ThinkingBlocks json.RawMessage `json:"thinking_blocks,omitempty"`
}

// MarshalJSON / UnmarshalJSON 让 ContentPartType 以字符串编码，
// 便于持久化存储时可读、可迁移。
func (t ContentPartType) MarshalJSON() ([]byte, error) {
	switch t {
	case ContentPartImageURL:
		return []byte(`"image_url"`), nil
	default:
		return []byte(`"text"`), nil
	}
}

func (t *ContentPartType) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case `"image_url"`:
		*t = ContentPartImageURL
	default:
		*t = ContentPartText
	}
	return nil
}

// WatchdogOptions 工具循环保护配置（零值 = 全部关闭）。
//
// 由插件层（pluginaichat）按回合预算与配置填充后放进 ChatOptions.Watchdog；
// 组件层只负责在工具循环里执行：提醒以 user 角色追加进消息（随本轮返回，
// 插件层会持久化，故后续回合模型仍能看到），终止则返回 *WatchdogAbortError。
type WatchdogOptions struct {
	// SoftDeadline 软截止时刻：到达后向模型注入一次「收尾或转后台」提醒，
	// 不中断本轮；零值表示不启用。
	SoftDeadline time.Time
	// SoftDeadlineReminder 软截止提醒的文本；空表示使用默认文案。默认文案会点名
	// task_handoff，只有注册了该工具的会话适用；其余会话（定时任务 / 主动互动 /
	// 面板试聊）由插件层传入不带工具名的通用文案，避免诱导模型调用不存在的工具。
	SoftDeadlineReminder string
	// ProgressReminderAfter 连续工具轮数达到该值后注入一次「考虑委派子代理」
	// 提醒；0 表示关闭。
	ProgressReminderAfter int
	// ProgressReminder 工具轮数提醒的文本；空表示使用默认文案。默认文案会点名
	// subagent_run，只有注册了该工具的会话适用；没有子代理工具的会话（子代理
	// 自身 / 主动互动 / 面板试聊）由插件层传入通用文案。
	ProgressReminder string
	// RepeatWarnThresholds 相同工具+参数重复次数达到各阈值时各提醒一次，
	// 如 [3,5,8]；空/nil 表示关闭。
	RepeatWarnThresholds []int
	// NoProgressLimit 相同工具+参数重复达到该次数时终止工具循环；0 表示
	// 只提醒不终止。
	NoProgressLimit int
}

// ChatOptions LLM 调用参数
type ChatOptions struct {
	MaxToken           *int
	MaxCompletionToken *int
	Temperature        *float64
	TopP               *float64
	TopK               *int // 非标准参数，部分兼容 API 使用
	Tools              []llmtool.ToolDef
	ReasoningEffort    *string // "low", "medium", "high"

	// Watchdog 工具循环保护配置（零值 = 全部关闭），由插件层按回合预算填充。
	// 仅 ToolOrchestrator 使用；同一 ChatBot 的 Chat 串行调用，状态按次隔离。
	Watchdog WatchdogOptions

	// Steer 运行中追加输入（可选）：工具循环每次发起 LLM 请求前调用，返回的
	// 文本会原封不动地作为 user 消息追加到对话尾部、下一步立即生效（用于子代理
	// steering 与主会话插话：运行期间补充指令/新消息，不打断当前工具批）。
	// 文本需自带来源/角色说明前缀（如子代理的「[追加指令]」、主会话的
	// 「【用户插话】」），编排层不再追加。并发安全由实现方保证；nil 表示不启用。
	Steer func() []string

	// OnStreamDelta 流式文本增量回调（nil = 一次性生成）。
	// 注意：在流读取的同一 goroutine 串行调用，回调内勿做重 IO；
	// 回调收到的 delta 尚未去除 <think> 块（由调用方在缓冲上统一处理）。
	OnStreamDelta func(string)
	// OnStreamRoundEnd 工具调用轮结束回调（toolexecutor 在工具边界调用）：
	// 调用方应清空已展示内容的累积缓冲，让下一轮的增量从零开始、整体覆盖
	// 同一条流式消息（覆盖语义见 OnStreamDelta 的使用方）；回调本身不发送内容。
	OnStreamRoundEnd func()
	// OnStreamRestart 流式中途失败后重试/切换备用模型前的重启回调（可选）：
	// 调用方应重置已展示的流式缓冲，让重试从头生成的内容整体覆盖旧输出
	// （打字机效果是整体覆盖而非追加，不会拼接出重复文本）；仅 OnStreamDelta
	// 非空时使用，nil 时保留旧行为——首字节后失败不重试。
	OnStreamRestart func()

	// PreToolGate 请求级工具门禁（可选）：每次工具调用前在该工具的 goroutine 内
	// 调用，实现必须并发安全。block=true 时工具不执行，result 作为该工具的
	// 结果消息回填（工具循环继续，语义等同工具报错），并照常触发工具观察者。
	// 用于计划模式、PreToolUse 钩子、人工审批等调用前拦截场景。
	PreToolGate func(ctx context.Context, call llmtool.ToolCall) (block bool, result string)

	// PersistUserText 会话历史（窗口落盘）中保存的用户消息文本；为空时保存
	// userInput 原值。调用方在请求前向用户消息追加了只应作用于当轮的注入
	// （记忆/知识库片段、待办提醒、计划模式附言等）时，用它传入注入前的原始
	// 用户消息：注入只进入当轮请求，不随历史回放（既白占上下文，又可能引用
	// 已失效的记忆 ID 或过期资料）。非空时，Chat 内部 UserPromptSubmit 钩子
	// 追加的上下文同样不写入历史（钩子每轮都会重新触发，同属当轮注入）。
	PersistUserText string

	// requestHeaders 本次请求的动态请求头（由 LLMClient 在发起请求前按
	// WithHeaderProvider 填充，同名覆盖客户端构造时的静态自定义头）。
	// 仅组件内部使用：调用方传入的 ChatOptions 无需设置该字段。
	requestHeaders map[string]string
}

func TextPart(text string) ContentPart {
	return ContentPart{Type: ContentPartText, Text: text}
}

func ImageURLPart(url string) ContentPart {
	return ContentPart{Type: ContentPartImageURL, ImageURL: url}
}

func TextMessage(role MessageRole, text string) Message {
	return Message{
		Role:  role,
		Parts: []ContentPart{TextPart(text)},
	}
}
