package aichat

import (
	"context"
	"fmt"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

type ChatBot struct {
	llmClient        *LLMClient
	msgBuilder       *MessageBuilder
	toolOrchestrator *ToolOrchestrator
	window           *messageWindow
	// hookRunner/hookBase 钩子执行器与会话身份（SessionKey/AgentKind），
	// 由 SetHookRunner 注入；nil 时 UserPromptSubmit 埋点跳过
	hookRunner HookRunner
	hookBase   agenthook.Payload
}

// chatBotConfig 收集 NewChatBot 的可选参数。
type chatBotConfig struct {
	clientOpts       []LLMClientOption // 主对话 LLM 客户端的可选配置（重试/备用模型）
	compressorClient *LLMClient        // 上下文压缩专用客户端；nil 复用主对话客户端
	scene            string            // 场景提示词，组装时排在 available_skills 之后
}

// ChatBotOption 配置 ChatBot 的可选参数（函数选项模式）。
type ChatBotOption func(*chatBotConfig)

// WithClientOptions 为主对话 LLM 客户端附加可选配置（如 WithRetry / WithFallback）。
func WithClientOptions(opts ...LLMClientOption) ChatBotOption {
	return func(c *chatBotConfig) {
		c.clientOpts = append(c.clientOpts, opts...)
	}
}

// WithCompressorClient 指定上下文压缩专用 LLM 客户端（可独立配置更便宜的模型）。
// 未设置时压缩复用主对话客户端。
func WithCompressorClient(client *LLMClient) ChatBotOption {
	return func(c *chatBotConfig) {
		c.compressorClient = client
	}
}

// WithScenePrompt 注入场景提示词（群聊/私聊、会话 ID、群信息等每会话不同的内容）。
// 组装时排在 available_skills 之后，使「静态提示词 + skills」保持跨会话稳定的
// 前缀缓存共享段（见 MessageBuilder.buildSystemPrompt）。
func WithScenePrompt(scene string) ChatBotOption {
	return func(c *chatBotConfig) {
		c.scene = scene
	}
}

func NewChatBot(baseURL, apiKey, model, prompt string, maxContextTokens int, toolExecutor ToolExecutor, historyStore HistoryStore, opts ...ChatBotOption) (*ChatBot, error) {
	cfg := chatBotConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	llmClient, err := NewLLMClient(baseURL, apiKey, model, cfg.clientOpts...)
	if err != nil {
		return nil, err
	}

	msgBuilder := NewMessageBuilder(prompt)
	msgBuilder.SetScene(cfg.scene)
	toolOrchestrator := NewToolOrchestrator(toolExecutor, msgBuilder)

	compressor := NewContextCompressor(prompt)
	window := newMessageWindow(maxContextTokens, llmClient, compressor, historyStore, cfg.compressorClient)

	return &ChatBot{
		llmClient:        llmClient,
		msgBuilder:       msgBuilder,
		toolOrchestrator: toolOrchestrator,
		window:           window,
	}, nil
}

// LoadHistory 从持久化存储回放历史对话，重启后调用以恢复上下文。
// historyStore 未注入时为空操作。
func (b *ChatBot) LoadHistory(ctx context.Context) {
	b.window.load(ctx)
}

// SetSystemPrompt 运行时更新静态系统提示词（面板修改 Prompt 覆盖后，驻留会话
// 下一轮立即生效）。场景提示词经 SetScenePrompt 单独更新。
func (b *ChatBot) SetSystemPrompt(prompt string) {
	b.msgBuilder.SetPrompt(prompt)
}

// SetScenePrompt 运行时更新场景提示词（群名/人数、记忆/知识库开关变化后下一轮生效）。
func (b *ChatBot) SetScenePrompt(scene string) {
	b.msgBuilder.SetScene(scene)
}

func (b *ChatBot) Chat(ctx context.Context, userInput string, callbacks llmtool.CallBackFuncs, opts ChatOptions) (string, TokenUsage, error) {
	// 压缩检查：在构建消息之前，确保上下文不超限
	if err := b.window.MaybeCompress(ctx); err != nil {
		return "", TokenUsage{}, err
	}
	// 本轮若触发了上下文压缩，其 token 消耗并入当次请求统计（只算 token 字段，
	// 不计 Iterations 与 LastPromptTokens——压缩不是工具循环轮次，其 prompt
	// 大小也不代表当前上下文长度）
	compressUsage := b.window.takeCompressUsage()

	// UserPromptSubmit 钩子：可阻断本轮输入（返回 PromptBlockedError，由插件层
	// 把原因告知用户后继续正常流程），或注入附加上下文——尾部注入：拼到用户
	// 消息前而非 system，保持上游前缀缓存稳定（同记忆/知识库注入的约束）
	if b.hookRunner != nil {
		payload := b.hookBase
		payload.Prompt = userInput
		if res := b.hookRunner.Run(ctx, agenthook.EventUserPromptSubmit, payload); res.Block {
			reason := res.Reason
			if reason == "" {
				reason = "请求已被钩子阻断"
			}
			return "", TokenUsage{}, &PromptBlockedError{Reason: reason}
		} else if res.Context != "" {
			userInput = res.Context + "\n\n" + userInput
		}
	}

	messages := b.msgBuilder.BuildChatMessages(userInput, b.window.history())
	// 记录构建完成时的真实长度：messages = [system, ...history, user]，因此
	// builtLen-1 是本轮用户消息的下标，updatedMessages 从该处起的后缀即
	// 本轮新增（用户消息 + AI 回复 + 工具消息），需完整写回历史
	builtLen := len(messages)

	response, updatedMessages, usage, err := b.toolOrchestrator.ExecuteWithTools(ctx, b.llmClient, messages, callbacks, opts)
	usage.PromptTokens += compressUsage.PromptTokens
	usage.CompletionTokens += compressUsage.CompletionTokens
	usage.TotalTokens += compressUsage.TotalTokens
	usage.CachedTokens += compressUsage.CachedTokens
	if err != nil {
		return "", usage, fmt.Errorf("chat execution failed: %w", err)
	}

	b.window.RecordUsage(usage)

	if start := builtLen - 1; start < len(updatedMessages) {
		newMessages := updatedMessages[start:]
		// 落盘/回放用注入前的原始用户文本（PersistUserText）：记忆/知识库等注入
		// 及 UserPromptSubmit 钩子上下文只作用于当轮请求，不随历史回放（否则旧
		// 注入白占上下文、且可能引用已失效的记忆 ID 或过期资料）
		if opts.PersistUserText != "" {
			newMessages = append([]Message(nil), newMessages...)
			newMessages[0] = TextMessage(RoleUser, b.msgBuilder.withTimePrefix(opts.PersistUserText))
		}
		b.window.append(newMessages...)
	}

	response = RemoveThinkContent(response)

	return response, usage, nil
}

// GetSingleImageDesc 生成单张图片的描述文本，返回描述与本次调用的 token 用量
// （用量由调用方并入所属请求/会话的统计与配额）。
func (b *ChatBot) GetSingleImageDesc(ctx context.Context, userInput string, imageURL string, opts ChatOptions) (string, TokenUsage, error) {
	messages := b.msgBuilder.BuildVisionMessages(userInput, imageURL)
	return b.llmClient.GenerateSingleWithUsage(ctx, messages, opts)
}

func (b *ChatBot) ClearHistory(ctx context.Context) error {
	b.window.clear()
	return nil
}

func (b *ChatBot) ClearDynamicTools() int {
	if b.toolOrchestrator != nil && b.toolOrchestrator.executor != nil {
		if session, ok := b.toolOrchestrator.executor.(*llmtool.SessionToolExecutor); ok {
			return session.ClearDynamicMCPTools()
		}
	}
	return 0
}

func (b *ChatBot) SetToolOrchestrator(orchestrator *ToolOrchestrator) {
	b.toolOrchestrator = orchestrator
}

// SetToolObserver 设置工具调用观察者（每次工具执行完成后回调），传 nil 取消。
// 由调用方保证同一 ChatBot 的 Chat 调用串行（插件层按会话加锁）。
func (b *ChatBot) SetToolObserver(fn func(ToolCallInfo)) {
	b.toolOrchestrator.SetToolObserver(fn)
}

// SetHookRunner 注入钩子执行器与会话身份（sessionKey 与 agentKind：
// main/subagent/clock），同时下发到工具编排器（PostToolUse）与消息窗口
// （PreCompact）；传 nil 取消。由调用方保证同一 ChatBot 的 Chat 调用串行。
func (b *ChatBot) SetHookRunner(r HookRunner, sessionKey, agentKind string) {
	base := agenthook.Payload{SessionKey: sessionKey, AgentKind: agentKind}
	b.hookRunner = r
	b.hookBase = base
	b.toolOrchestrator.SetHookRunner(r, base)
	b.window.setHookRunner(r, base)
}

func (b *ChatBot) SetSkillManager(manager *llmtool.SkillManager) {
	b.msgBuilder.WithSkillManager(manager)
}

// SetMaxIterations 设置工具调用循环的最大轮数（子代理等场景使用比主对话更小的上限）。
func (b *ChatBot) SetMaxIterations(max int) {
	b.toolOrchestrator.SetMaxIterations(max)
}
