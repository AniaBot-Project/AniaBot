package aichat

import (
	"context"
	"fmt"
)

// 支持的 LLM API 格式。
const (
	// APIFormatChatCompletions OpenAI Chat Completions 格式（默认，兼容绝大多数 OpenAI 兼容 API）
	APIFormatChatCompletions = "chat_completions"
	// APIFormatResponses OpenAI Responses API 格式
	APIFormatResponses = "responses"
	// APIFormatAnthropic Anthropic Messages API 格式
	APIFormatAnthropic = "anthropic"
)

// llmBackend 具体 API 格式的后端：负责各自的消息转换、请求发送与响应解析。
// 重试与备用模型切换由 LLMClient 外壳统一处理。
type llmBackend interface {
	// generate 单次生成。
	generate(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, error)
	// generateStream 流式生成；started 表示是否已收到任何数据块（首字节前失败可安全重试）。
	generateStream(ctx context.Context, messages []Message, opts ChatOptions) (resp GenerateResponse, usage TokenUsage, started bool, err error)
}

// normalizeAPIFormat 归一化 API 格式配置：空值等同 chat_completions；未知格式报错。
func normalizeAPIFormat(format string) (string, error) {
	switch format {
	case "", APIFormatChatCompletions:
		return APIFormatChatCompletions, nil
	case APIFormatResponses, APIFormatAnthropic:
		return format, nil
	}
	return "", fmt.Errorf("unknown api format: %q (expect %s | %s | %s)",
		format, APIFormatChatCompletions, APIFormatResponses, APIFormatAnthropic)
}

// newLLMBackend 按格式构造对应后端。cache 为上游 prompt 缓存配置，
// 仅 anthropic 后端使用（chat_completions / responses 为自动前缀缓存）；
// headers 为自定义请求头，三种格式统一附加（同名覆盖默认头）。
func newLLMBackend(format, baseURL, apiKey, model string, cache PromptCacheConfig, headers map[string]string) (llmBackend, error) {
	f, err := normalizeAPIFormat(format)
	if err != nil {
		return nil, err
	}
	switch f {
	case APIFormatResponses:
		return newResponsesBackend(baseURL, apiKey, model, headers), nil
	case APIFormatAnthropic:
		return newAnthropicBackend(baseURL, apiKey, model, cache, headers), nil
	default:
		return newChatCompletionsBackend(baseURL, apiKey, model, headers), nil
	}
}
