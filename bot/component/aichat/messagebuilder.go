package aichat

import (
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/utils"
	"github.com/jeanhua/AniaBot/common/model/message"
)

type MessageBuilder struct {
	prompt       string // 静态配置提示词（或会话覆盖版），不含场景描述
	scene        string // 场景提示词，拼在 available_skills 之后
	skillManager *llmtool.SkillManager
}

func NewMessageBuilder(prompt string) *MessageBuilder {
	return &MessageBuilder{prompt: prompt}
}

func NewMessageBuilderWithSkill(prompt string, manager *llmtool.SkillManager) *MessageBuilder {
	return &MessageBuilder{
		prompt:       prompt,
		skillManager: manager,
	}
}

func (b *MessageBuilder) WithSkillManager(manager *llmtool.SkillManager) {
	b.skillManager = manager
}

// SetPrompt 运行时更新静态系统提示词（面板修改 Prompt 覆盖后，驻留会话下一轮立即生效）。
func (b *MessageBuilder) SetPrompt(prompt string) {
	b.prompt = prompt
}

// SetScene 运行时更新场景提示词（群名/人数等变化后下一轮生效），与 SetPrompt 配套。
func (b *MessageBuilder) SetScene(scene string) {
	b.scene = scene
}

// buildSystemPrompt 按「静态提示词 + available_skills + 场景描述」组装 system prompt。
// 场景描述含会话 ID/群名等每会话不同的内容，必须排在 skills 之后：上游前缀缓存
// 按最长公共前缀命中，场景夹在静态段与 skills 之间会把 skills 挡在共享前缀之外。
func (b *MessageBuilder) buildSystemPrompt() string {
	prompt := b.prompt
	if b.skillManager != nil {
		if skillBlock := b.skillManager.BuildAvailableSkillsPrompt(); skillBlock != "" {
			prompt += "\n\n" + skillBlock
		}
	}
	return prompt + b.scene
}

// timePrefix 本条消息的时间前缀（"[RFC3339] "）。请求构建与历史落盘两侧
// 必须复用同一次取值：落盘副本若重新生成时间戳（RFC3339 为秒级精度，而一次
// LLM 请求通常跨秒），与真正发送给上游的内容不再逐字节一致，下一轮请求的
// prompt 前缀缓存会在上一轮用户消息处整段分叉（assistant 回复与全部工具往返
// 每轮全价重算）。
func (b *MessageBuilder) timePrefix() string {
	return "[" + utils.GetFormattedTime() + "] "
}

func (b *MessageBuilder) withTimePrefix(input string) string {
	return b.timePrefix() + input
}

// BuildChatMessages 用调用方提供的原始输入组装消息（内部渲染时间前缀）。
func (b *MessageBuilder) BuildChatMessages(userInput string, history []Message) []Message {
	return b.buildChatMessages(b.withTimePrefix(userInput), history)
}

// buildChatMessages 组装消息数组；userText 为已渲染（含时间前缀与当轮注入）
// 的用户文本，ChatBot 落盘补丁路径据此复用请求中实际使用的同一份字符串。
func (b *MessageBuilder) buildChatMessages(userText string, history []Message) []Message {
	messages := make([]Message, 0, 1+len(history)+1)
	messages = append(messages, TextMessage(RoleSystem, b.buildSystemPrompt()))
	messages = append(messages, history...)
	messages = append(messages, TextMessage(RoleUser, userText))
	return messages
}

// BuildImageContextMessage 构造加载图片的上下文消息。每张图片前附带 [图片 <hash>]
// 文本标签，与消息文本中的图片标记一致，便于 AI 区分和引用具体图片。
func (b *MessageBuilder) BuildImageContextMessage(imageURLs []string) Message {
	parts := make([]ContentPart, 0, 2*len(imageURLs)+1)
	parts = append(parts, TextPart("以下是用户要求加载的图片，请结合图片内容继续回答。"))
	for _, imageURL := range imageURLs {
		parts = append(parts, TextPart("[图片 "+message.ImageHash(imageURL)+"]"))
		parts = append(parts, ImageURLPart(imageURL))
	}
	return Message{Role: RoleUser, Parts: parts}
}

func (b *MessageBuilder) BuildVisionMessages(userInput, imageURL string) []Message {
	parts := []ContentPart{
		TextPart(b.withTimePrefix(userInput)),
		ImageURLPart(imageURL),
	}

	return []Message{
		{
			Role:  RoleSystem,
			Parts: []ContentPart{TextPart(b.buildSystemPrompt())},
		},
		{
			Role:  RoleUser,
			Parts: parts,
		},
	}
}

func (b *MessageBuilder) BuildToolMessage(toolCallID, name, result string) Message {
	return Message{
		Role:       RoleTool,
		ToolCallID: toolCallID,
		Parts:      []ContentPart{TextPart(result)},
	}
}

func (b *MessageBuilder) BuildAIMessage(content string, toolCalls []llmtool.ToolCall) Message {
	msg := Message{
		Role: RoleAssistant,
	}

	if content != "" {
		msg.Parts = append(msg.Parts, TextPart(content))
	}

	msg.ToolCalls = toolCalls

	return msg
}

func (b *MessageBuilder) BuildAIMessageWithReasoning(content string, toolCalls []llmtool.ToolCall, reasoningContent string) Message {
	msg := b.BuildAIMessage(content, toolCalls)
	msg.ReasoningContent = reasoningContent
	return msg
}

func (b *MessageBuilder) BuildToolLimitMessage() Message {
	return TextMessage(
		RoleUser,
		"<system>你的Tool Call连续调用已经达到限制，请先基于当前获取结果回答用户问题，如果需要更多Tool Call，请先向用户发送请求，得到用户允许后重新刷新限额</system>",
	)
}
