package aichat

import (
	"context"
	"encoding/json"

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

// ChatOptions LLM 调用参数
type ChatOptions struct {
	MaxToken           *int
	MaxCompletionToken *int
	Temperature        *float64
	TopP               *float64
	TopK               *int // 非标准参数，部分兼容 API 使用
	Tools              []llmtool.ToolDef
	ReasoningEffort    *string // "low", "medium", "high"

	// OnStreamDelta 流式文本增量回调（nil = 一次性生成）。
	// 注意：在流读取的同一 goroutine 串行调用，回调内勿做重 IO；
	// 回调收到的 delta 尚未去除 <think> 块（由调用方在缓冲上统一处理）。
	OnStreamDelta func(string)
	// OnStreamRoundEnd 工具调用轮结束回调（toolexecutor 在工具边界调用）：
	// 调用方应 End 当前流式消息；下一轮首个增量创建新消息。
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
	// （记忆/知识库片段、待办提醒、钩子上下文等）时，用它传入注入前的原始
	// 用户消息，避免注入内容随历史回放：既白占上下文，又可能引用已失效的
	// 记忆 ID 或过期资料。
	PersistUserText string
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
