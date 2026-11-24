package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/openai/openai-go/v3"
)

// LLMClient LLM 客户端外壳：持有具体 API 格式后端（llmBackend），
// 统一提供应用层重试与备用模型切换语义。
type LLMClient struct {
	backend  llmBackend
	model    string
	retry    *retryConfig
	fallback *LLMClient
	// requestTimeout 单次 LLM HTTP 请求（含流式）的总超时；0 表示不限制。
	// 每次尝试（含重试）独立计时，重试等待仍尊重调用方 ctx。
	requestTimeout time.Duration
	// streamIdleTimeout 流式响应空闲超时；0 表示不限制。按「连续多久没有
	// 新数据块」计时，不限制请求总时长。
	streamIdleTimeout time.Duration
	// headerProvider 动态请求头来源（可选）：每次请求前调用，返回值作为本次
	// 请求的额外头（同名覆盖构造时的静态头）。用于会话内会变化的自定义头
	// （如会话标识在 #新对话 后换新），无需重建客户端。
	headerProvider func() map[string]string
}

// retryConfig 应用层重试配置：maxAttempts 为最大尝试次数（<=1 表示不重试），
// baseDelay 为退避基准时长，每次重试等待 base×2^n 加随机抖动。
type retryConfig struct {
	maxAttempts int
	baseDelay   time.Duration
}

// PromptCacheConfig 上游 prompt 缓存配置。
//
// chat_completions / responses 格式由提供方自动做前缀缓存（无需配置）；
// anthropic 格式必须显式声明 cache_control 断点才会启用缓存，
// 因此本配置仅 anthropic 后端生效，其余后端忽略。
type PromptCacheConfig struct {
	// Enable 是否启用：anthropic 格式下为 system 与最后一条消息设置
	// cache_control 断点；关闭时请求体与旧行为完全一致。
	Enable bool
	// TTL 缓存保留时长（"5m" | "1h"），仅 anthropic 格式有效；
	// 空值使用提供方默认（5m）。
	TTL string
}

// llmClientConfig 收集 NewLLMClient 的可选参数。
type llmClientConfig struct {
	maxAttempts       int
	baseDelay         time.Duration
	apiFormat         string
	fallbackBaseURL   string
	fallbackAPIKey    string
	fallbackModel     string
	fallbackFormat    string
	promptCache       PromptCacheConfig
	headers           map[string]string
	headerProvider    func() map[string]string
	requestTimeout    time.Duration
	streamIdleTimeout time.Duration
}

// LLMClientOption 配置 LLMClient 的可选参数（函数选项模式）。
type LLMClientOption func(*llmClientConfig)

// WithRetry 配置应用层重试：429/5xx/网络错误时指数退避重试，最多 maxAttempts 次
// （0 或 1 表示不重试）。注意 openai-go SDK 已内置 408/409/429/5xx 的指数退避重试
// （默认 2 次），本选项补充 SDK 不覆盖的网络错误与长尾故障，并自定义退避节奏；
// 两层叠加下最坏请求次数为 maxAttempts×(1+SDK 重试次数)。
func WithRetry(maxAttempts int, baseDelay time.Duration) LLMClientOption {
	return func(c *llmClientConfig) {
		c.maxAttempts = maxAttempts
		c.baseDelay = baseDelay
	}
}

// WithAPIFormat 指定 LLM API 格式（APIFormatChatCompletions / APIFormatResponses /
// APIFormatAnthropic），空值等同 chat_completions。
func WithAPIFormat(format string) LLMClientOption {
	return func(c *llmClientConfig) {
		c.apiFormat = format
	}
}

// WithPromptCache 配置上游 prompt 缓存（见 PromptCacheConfig）。
// 仅 anthropic 格式生效；chat_completions / responses 为自动前缀缓存，忽略此选项。
func WithPromptCache(cfg PromptCacheConfig) LLMClientOption {
	return func(c *llmClientConfig) {
		c.promptCache = cfg
	}
}

// WithHeaders 附加自定义请求头，应用到全部 API 格式的主客户端与备用客户端；
// 与默认头（User-Agent / Content-Type 等）同名时覆盖默认值。空 map 无效果。
func WithHeaders(headers map[string]string) LLMClientOption {
	return func(c *llmClientConfig) {
		c.headers = headers
	}
}

// WithHeaderProvider 动态自定义请求头：每次发起 LLM 请求前调用 provider，
// 返回的头作为本次请求的额外头下发（与 WithHeaders 同名时覆盖静态值）。
// 用于会话内会变化的自定义头（如会话标识在 #新对话 后换新），调用方无需
// 重建客户端；provider 必须并发安全。返回空 map 时本次请求只用静态头。
func WithHeaderProvider(provider func() map[string]string) LLMClientOption {
	return func(c *llmClientConfig) {
		c.headerProvider = provider
	}
}

// WithRequestTimeout 单次 LLM HTTP 请求（含流式）的总超时；0 表示不限制。
//
// 每次尝试独立计时（重试与备用模型切换都会重新获得完整时长），重试等待仍
// 尊重调用方 ctx。超时错误包裹 context.DeadlineExceeded：上层可用 errors.Is
// 识别，并像网络错误一样进入重试/备用模型切换逻辑。
func WithRequestTimeout(d time.Duration) LLMClientOption {
	return func(c *llmClientConfig) {
		c.requestTimeout = d
	}
}

// WithStreamIdleTimeout 流式响应空闲超时：超过该时长无新增量即中断本次请求
// （按可重试错误处理）；0 表示不限制。
//
// 只按「没有新数据块」计时，不限制请求总时长：模型思考时间长但只要仍在持续
// 出字就不会被中断。anthropic 格式的非流式 Generate 内部也走流式通道，同样
// 受此保护；chat_completions / responses 的非流式请求没有增量概念，不适用。
// 生效范围：Generate、GenerateStream、GenerateSingleWithUsage（后两者经由
// 前者，语义一致），并透传给备用模型客户端。
func WithStreamIdleTimeout(d time.Duration) LLMClientOption {
	return func(c *llmClientConfig) {
		c.streamIdleTimeout = d
	}
}

// WithFallback 配置备用模型：主模型重试耗尽或遇到不可重试错误时，改用备用模型
// 再请求一次（备用客户端内部自带同等重试）。fallbackModel 为空表示不启用。
// baseURL / apiKey / format 留空时回退到主模型配置。
func WithFallback(baseURL, apiKey, model, format string) LLMClientOption {
	return func(c *llmClientConfig) {
		c.fallbackBaseURL = baseURL
		c.fallbackAPIKey = apiKey
		c.fallbackModel = model
		c.fallbackFormat = format
	}
}

func NewLLMClient(baseURL, apiKey, model string, opts ...LLMClientOption) (*LLMClient, error) {
	cfg := llmClientConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	backend, err := newLLMBackend(cfg.apiFormat, baseURL, apiKey, model, cfg.promptCache, cfg.headers)
	if err != nil {
		return nil, err
	}
	c := &LLMClient{
		backend:           backend,
		model:             model,
		headerProvider:    cfg.headerProvider,
		requestTimeout:    cfg.requestTimeout,
		streamIdleTimeout: cfg.streamIdleTimeout,
	}
	if cfg.maxAttempts > 1 {
		c.retry = &retryConfig{maxAttempts: cfg.maxAttempts, baseDelay: cfg.baseDelay}
	}
	if cfg.fallbackModel != "" {
		fbBaseURL, fbAPIKey, fbFormat := cfg.fallbackBaseURL, cfg.fallbackAPIKey, cfg.fallbackFormat
		if fbBaseURL == "" {
			fbBaseURL = baseURL
		}
		if fbAPIKey == "" {
			fbAPIKey = apiKey
		}
		fb, err := NewLLMClient(fbBaseURL, fbAPIKey, cfg.fallbackModel,
			WithRetry(cfg.maxAttempts, cfg.baseDelay),
			WithAPIFormat(fbFormat),
			WithPromptCache(cfg.promptCache),
			WithHeaders(cfg.headers),                     // 不传 WithFallback 防止递归
			WithHeaderProvider(cfg.headerProvider),       // 动态头同样适用于备用模型
			WithRequestTimeout(cfg.requestTimeout),       // 请求超时同样适用于备用模型
			WithStreamIdleTimeout(cfg.streamIdleTimeout), // 流空闲超时同样适用于备用模型
		)
		if err != nil {
			return nil, fmt.Errorf("create fallback client: %w", err)
		}
		c.fallback = fb
	}
	return c, nil
}

// withRequestHeaders 按 headerProvider 生成本次请求的动态头（无 provider 时原样返回）。
func (c *LLMClient) withRequestHeaders(opts ChatOptions) ChatOptions {
	if c.headerProvider == nil {
		return opts
	}
	opts.requestHeaders = c.headerProvider()
	return opts
}

type GenerateResponse struct {
	Content   string
	ToolCalls []llmtool.ToolCall
	// ReasoningContent 保存 API 返回的推理过程内容（如 DeepSeek 的 reasoning_content），
	// 在 tool calling 多轮对话中需要原样传回。
	ReasoningContent string
	// ThinkingBlocks 保存 Anthropic 格式的思考块原始信息（JSON 数组，元素见
	// thinkingBlock），tool calling 多轮中 Anthropic 要求原样回传；仅 anthropic
	// 格式写入。
	ThinkingBlocks json.RawMessage
	// Truncated 输出因达到 max_tokens 上限被截断（chat_completions 的
	// finish_reason=length / anthropic 的 stop_reason=max_tokens / responses 的
	// incomplete_details.reason=max_output_tokens）。截断的工具调用参数 JSON
	// 不完整，不可执行，由编排层回填明确的截断提示。
	Truncated bool
}

func (c *LLMClient) Generate(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, error) {
	// 动态请求头在请求开始时定格：同一次请求的重试沿用同一组值
	opts = c.withRequestHeaders(opts)
	// 应用层重试循环：SDK 已内置 408/409/429/5xx 的指数退避重试，本层补充
	// SDK 不重试的网络错误，并叠加自定义退避节奏。所有等待都尊重 ctx 剩余
	// deadline：取消/超时立即返回，绝不拖长请求。
	for attempt := 0; ; attempt++ {
		resp, usage, err := c.generateOnce(ctx, messages, opts)
		if err == nil {
			return resp, usage, nil
		}

		if ctx.Err() != nil {
			// 调用方取消/超时不重试，原样返回（上层据此区分超时与普通错误）
			return GenerateResponse{}, TokenUsage{}, ctx.Err()
		}

		if !retryableLLMError(err) || c.retry == nil || attempt+1 >= c.retry.maxAttempts {
			// 重试耗尽或不可重试：改用备用模型再请求一次（仅一次，备用客户端
			// 内部自带同等重试）。备用模型也失败时返回其错误。
			if c.fallback != nil {
				fbResp, fbUsage, fbErr := c.fallback.Generate(ctx, messages, opts)
				if fbErr == nil {
					return fbResp, fbUsage, nil
				}
				if ctx.Err() != nil {
					return GenerateResponse{}, TokenUsage{}, ctx.Err()
				}
				return GenerateResponse{}, TokenUsage{}, fmt.Errorf("LLM generation failed (fallback): %w", fbErr)
			}
			return GenerateResponse{}, TokenUsage{}, fmt.Errorf("LLM generation failed: %w", err)
		}

		delay := retryDelay(c.retry.baseDelay, attempt)
		select {
		case <-ctx.Done():
			return GenerateResponse{}, TokenUsage{}, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// generateOnce 执行一次非流式生成尝试（重试循环内的一次 HTTP 请求）。
//
// 请求保护：派生带总超时的 context（WithRequestTimeout；0=不限制）。对内部
// 走流式通道聚合的非流式后端（anthropic），同样挂流空闲看门狗——上游半挂
// 时不必等到请求总超时。两类超时都改写为包裹 context.DeadlineExceeded 的
// 可识别错误，再交给外壳按可重试错误处理。
func (c *LLMClient) generateOnce(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, error) {
	reqCtx, cancel := withRequestTimeout(ctx, c.requestTimeout)
	defer cancel()

	if !c.streamsInternally() {
		resp, usage, err := c.backend.generate(reqCtx, messages, opts)
		return resp, usage, c.normalizeRequestError(ctx, reqCtx, err)
	}

	streamCtx, idleCancel, idle := newStreamIdleWatchdog(reqCtx, c.streamIdleTimeout)
	defer idleCancel()
	resp, usage, err := c.backend.generate(streamCtx, messages, opts)
	idleFired := idle.stop()
	if err == nil {
		return resp, usage, nil
	}
	if ctx.Err() != nil {
		return resp, usage, err
	}
	if idleFired {
		return resp, usage, streamIdleTimeoutError(c.streamIdleTimeout)
	}
	return resp, usage, c.normalizeRequestError(ctx, reqCtx, err)
}

// streamsInternally 后端是否在非流式 generate 内部走流式通道（见 internalStreamer）。
func (c *LLMClient) streamsInternally() bool {
	b, ok := c.backend.(internalStreamer)
	return ok && b.streamsInternally()
}

// GenerateSingleWithUsage 单次生成（不带工具），返回内容与 token 用量。
// 供调用方把压缩器、图片描述等辅助 LLM 调用的消耗计入统计。
// 请求超时/流空闲超时语义与 Generate 一致（内部经其实现）。
func (c *LLMClient) GenerateSingleWithUsage(ctx context.Context, messages []Message, opts ChatOptions) (string, TokenUsage, error) {
	resp, usage, err := c.Generate(ctx, messages, opts)
	if err != nil {
		return "", usage, err
	}
	return resp.Content, usage, nil
}

// GenerateStream 流式生成：与 Generate 相同的重试/备用模型语义。
// 已输出首字节后的失败是否重试取决于输出是否已展示给调用方：
//   - opts.OnStreamDelta 为 nil（一次性生成，如 anthropic 内部聚合流式）时内容
//     从未展示，失败可安全从头重试；
//   - opts.OnStreamDelta 非空且提供 opts.OnStreamRestart 时，重试/切换备用前先
//     调用 OnStreamRestart 让调用方重置已展示缓冲，打字机整体覆盖旧输出；
//   - 两者都不满足时保留旧行为——首字节后失败不重试、不切备用，返回已积累的
//     部分内容与错误，避免用户看到重复拼接的输出。
//
// 请求保护：每次尝试独立应用请求总超时与流空闲超时（见 generateStreamOnce），
// 两类超时都是包裹 context.DeadlineExceeded 的可识别错误，按普通可重试错误
// 进入上面的重试/备用逻辑。
func (c *LLMClient) GenerateStream(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, error) {
	// 动态请求头在请求开始时定格：同一次请求的重试沿用同一组值
	opts = c.withRequestHeaders(opts)
	for attempt := 0; ; attempt++ {
		resp, usage, started, err := c.generateStreamOnce(ctx, messages, opts)
		if err == nil {
			return resp, usage, nil
		}

		if ctx.Err() != nil {
			// 取消/超时不重试，原样返回（上层据此区分超时与普通错误）
			return GenerateResponse{}, TokenUsage{}, ctx.Err()
		}

		// 流式可见且未提供重启回调：首字节后不重试不切备用，避免重复拼接输出
		if started && opts.OnStreamDelta != nil && opts.OnStreamRestart == nil {
			return resp, usage, fmt.Errorf("LLM stream failed after partial output: %w", err)
		}

		// 重试/切换备用前通知调用方重置已展示缓冲（流式整体覆盖，非追加）
		restart := func() {
			if started && opts.OnStreamRestart != nil {
				opts.OnStreamRestart()
			}
		}

		if !retryableLLMError(err) || c.retry == nil || attempt+1 >= c.retry.maxAttempts {
			if c.fallback != nil {
				restart()
				fbResp, fbUsage, fbErr := c.fallback.GenerateStream(ctx, messages, opts)
				if fbErr == nil {
					return fbResp, fbUsage, nil
				}
				if ctx.Err() != nil {
					return GenerateResponse{}, TokenUsage{}, ctx.Err()
				}
				return GenerateResponse{}, TokenUsage{}, fmt.Errorf("LLM generation failed (fallback): %w", fbErr)
			}
			return GenerateResponse{}, TokenUsage{}, fmt.Errorf("LLM generation failed: %w", err)
		}

		restart()
		delay := retryDelay(c.retry.baseDelay, attempt)
		select {
		case <-ctx.Done():
			return GenerateResponse{}, TokenUsage{}, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// generateStreamOnce 执行一次流式生成尝试（重试循环内的一次 HTTP 请求）。
//
// 请求总超时用 context.WithTimeout 包一层；流空闲看门狗在流读取层实现——
// 后端每读到一个块调用 notifyStreamActivity 重置计时，连续超过
// streamIdleTimeout 没有新增量才取消本请求（不会误杀「思考久但仍在出字」）。
// 看门狗取消时后端只会看到 context.Canceled，这里按 fired 标记改写成包裹
// context.DeadlineExceeded 的可识别超时错误。
func (c *LLMClient) generateStreamOnce(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, bool, error) {
	reqCtx, reqCancel := withRequestTimeout(ctx, c.requestTimeout)
	defer reqCancel()

	streamCtx, idleCancel, idle := newStreamIdleWatchdog(reqCtx, c.streamIdleTimeout)
	defer idleCancel()

	resp, usage, started, err := c.backend.generateStream(streamCtx, messages, opts)
	idleFired := idle.stop()
	if err == nil {
		return resp, usage, started, nil
	}
	if ctx.Err() != nil {
		// 调用方取消/超时优先，由外层原样返回 ctx.Err()
		return resp, usage, started, err
	}
	if idleFired {
		return resp, usage, started, streamIdleTimeoutError(c.streamIdleTimeout)
	}
	return resp, usage, started, c.normalizeRequestError(ctx, reqCtx, err)
}

// retryableLLMError 判断错误是否值得重试：HTTP 429/5xx 可重试；
// SDK 对网络/传输错误不包装、原样返回原始错误，此时也视为可重试。
// 同时识别 openai-go 与 anthropic-sdk-go 两种 SDK 的错误类型。
func retryableLLMError(err error) bool {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	var antErr *anthropic.Error
	if errors.As(err, &antErr) {
		return antErr.StatusCode == 429 || antErr.StatusCode >= 500
	}
	return true
}

// retryDelay 计算第 attempt 次重试的等待时间：base×2^attempt 加随机抖动
// （0~delay），并设 30s 上限避免长时间空等。
func retryDelay(base time.Duration, attempt int) time.Duration {
	delay := base << attempt
	if delay <= 0 {
		delay = base
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	if delay > 0 {
		delay += time.Duration(rand.Int63n(int64(delay)))
	}
	return delay
}

func (c *LLMClient) GenerateSingle(ctx context.Context, messages []Message, opts ChatOptions) (string, error) {
	content, _, err := c.GenerateSingleWithUsage(ctx, messages, opts)
	return content, err
}
