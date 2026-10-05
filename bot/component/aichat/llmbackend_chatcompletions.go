package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/version"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// chatCompletionsBackend OpenAI Chat Completions 格式后端（默认格式，
// 兼容 DeepSeek / 智谱 / SiliconFlow 等绝大多数 OpenAI 兼容 API）。
type chatCompletionsBackend struct {
	client openai.Client
	model  string
}

// newChatCompletionsBackend 构造后端。headers 为自定义请求头，追加在默认头之后，
// 与默认头同名时生效者为准（可覆盖 User-Agent 等）。
func newChatCompletionsBackend(baseURL, apiKey, model string, headers map[string]string) *chatCompletionsBackend {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		// 覆盖 SDK 默认 UA，标识请求来源与版本
		option.WithHeader("User-Agent", version.UserAgent()),
	}
	for k, v := range headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	return &chatCompletionsBackend{
		client: openai.NewClient(opts...),
		model:  model,
	}
}

func (b *chatCompletionsBackend) generate(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, error) {
	apiMessages, err := b.convertMessages(messages)
	if err != nil {
		return GenerateResponse{}, TokenUsage{}, err
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(b.model),
		Messages: apiMessages,
	}

	b.applyOptions(&params, opts)

	completion, err := b.client.Chat.Completions.New(ctx, params)
	if err != nil {
		if ctx.Err() != nil {
			return GenerateResponse{}, TokenUsage{}, ctx.Err()
		}
		return GenerateResponse{}, TokenUsage{}, err
	}

	if len(completion.Choices) == 0 {
		return GenerateResponse{}, TokenUsage{}, fmt.Errorf("no choices returned from LLM")
	}

	resp, usage := b.parseResponse(completion)
	return resp, usage, nil
}

// generateStream 单次流式请求：累积内容/reasoning/tool calls（按 Index 组装）/usage，
// 返回 started 表示是否已收到任何数据块（首字节前失败可安全重试）。
func (b *chatCompletionsBackend) generateStream(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, bool, error) {
	apiMessages, err := b.convertMessages(messages)
	if err != nil {
		return GenerateResponse{}, TokenUsage{}, false, err
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(b.model),
		Messages: apiMessages,
	}
	b.applyOptions(&params, opts)
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}

	stream := b.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	acc := newStreamAccumulator()
	started := false
	for stream.Next() {
		chunk := stream.Current()
		acc.Add(chunk)
		// 真实数据块（有 choices 或 usage）才算已开始输出；
		// SDK 对流内 error 事件不产出 chunk（Next 直接返回 false 并置 Err）
		if len(chunk.Choices) > 0 || chunk.Usage.PromptTokens > 0 ||
			chunk.Usage.CompletionTokens > 0 || chunk.Usage.TotalTokens > 0 {
			started = true
		}
		// 增量回调在流读取 goroutine 串行调用
		if opts.OnStreamDelta != nil && len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			opts.OnStreamDelta(chunk.Choices[0].Delta.Content)
		}
	}
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			return acc.Result(), acc.usage, started, ctx.Err()
		}
		return acc.Result(), acc.usage, started, err
	}

	resp := acc.Result()
	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		return resp, acc.usage, started, fmt.Errorf("no choices returned from LLM")
	}
	return resp, acc.usage, started, nil
}

// streamAccumulator 流式块累积器：内容、reasoning_content、按 Index 组装的
// tool calls（首块带 ID/Name，后续块为 Arguments 增量）与末块 usage。
type streamAccumulator struct {
	content   strings.Builder
	reasoning strings.Builder
	toolCalls map[int64]*llmtool.ToolCall
	toolOrder []int64
	usage     TokenUsage
	// finishReason 末块携带的停止原因："length" 表示输出因 max_tokens 截断
	finishReason string
}

func newStreamAccumulator() *streamAccumulator {
	return &streamAccumulator{toolCalls: map[int64]*llmtool.ToolCall{}}
}

func (a *streamAccumulator) Add(chunk openai.ChatCompletionChunk) {
	for _, choice := range chunk.Choices {
		if choice.FinishReason != "" {
			a.finishReason = choice.FinishReason
		}
		d := choice.Delta
		if d.Content != "" {
			a.content.WriteString(d.Content)
		}
		// reasoning_content 为扩展字段，从块原始 JSON 提取
		if raw := d.RawJSON(); raw != "" {
			var rawMap map[string]any
			if err := json.Unmarshal([]byte(raw), &rawMap); err == nil {
				if rc, ok := rawMap["reasoning_content"].(string); ok && rc != "" {
					a.reasoning.WriteString(rc)
				}
			}
		}
		for _, tc := range d.ToolCalls {
			cur, ok := a.toolCalls[tc.Index]
			if !ok {
				cur = &llmtool.ToolCall{ID: tc.ID, Name: tc.Function.Name}
				a.toolCalls[tc.Index] = cur
				a.toolOrder = append(a.toolOrder, tc.Index)
				continue
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				cur.Arguments += tc.Function.Arguments
			}
		}
	}
	// usage 提取：OpenAI 标准约定在独立末块（空 Choices）返回；但部分提供方
	// （DeepSeek 部分响应、智谱等）把 usage 附带在 finish_reason 块（Choices 非空），
	// 也有网关忽略 stream_options 直接附带在最后一个内容块——这些形态下仅靠
	// 「空 Choices」判定会丢失全部用量（流式平台 token 统计为 0 的根因），
	// 因此任何块只要 usage 字段非零就提取。
	if chunk.Usage.PromptTokens > 0 {
		a.usage.PromptTokens = int(chunk.Usage.PromptTokens)
	}
	if chunk.Usage.CompletionTokens > 0 {
		a.usage.CompletionTokens = int(chunk.Usage.CompletionTokens)
	}
	if chunk.Usage.TotalTokens > 0 {
		a.usage.TotalTokens = int(chunk.Usage.TotalTokens)
	}
	if cached := extractCachedTokens(&chunk.Usage); cached > 0 {
		a.usage.CachedTokens = cached
	}
}

func (a *streamAccumulator) Result() GenerateResponse {
	resp := GenerateResponse{
		Content:          a.content.String(),
		ReasoningContent: a.reasoning.String(),
		// 输出撞上 max_tokens：内容/工具参数都被拦腰截断
		Truncated: a.finishReason == "length",
	}
	// 按 Index 升序输出（模型的规范顺序），与流到达顺序无关
	order := append([]int64(nil), a.toolOrder...)
	slices.Sort(order)
	for _, idx := range order {
		resp.ToolCalls = append(resp.ToolCalls, *a.toolCalls[idx])
	}
	return resp
}

func (b *chatCompletionsBackend) applyOptions(params *openai.ChatCompletionNewParams, opts ChatOptions) {
	if opts.MaxToken != nil {
		params.MaxTokens = openai.Int(int64(*opts.MaxToken))
	}
	if opts.MaxCompletionToken != nil {
		params.MaxCompletionTokens = openai.Int(int64(*opts.MaxCompletionToken))
	}
	if opts.Temperature != nil {
		params.Temperature = openai.Float(*opts.Temperature)
	}
	if opts.TopP != nil {
		params.TopP = openai.Float(*opts.TopP)
	}
	if opts.TopK != nil {
		// top_k 非 OpenAI 标准参数，openai-go SDK 未内置字段，
		// 经 ExtraFields 原样下发（DeepSeek 等兼容 API 支持）
		params.SetExtraFields(map[string]any{"top_k": *opts.TopK})
	}
	if opts.ReasoningEffort != nil {
		switch *opts.ReasoningEffort {
		case "low":
			params.ReasoningEffort = shared.ReasoningEffortLow
		case "medium":
			params.ReasoningEffort = shared.ReasoningEffortMedium
		case "high":
			params.ReasoningEffort = shared.ReasoningEffortHigh
		}
	}
	if len(opts.Tools) > 0 {
		apiTools := make([]openai.ChatCompletionToolUnionParam, 0, len(opts.Tools))
		for _, td := range opts.Tools {
			apiTools = append(apiTools, b.convertToolDef(td))
		}
		params.Tools = apiTools
	}
}

func (b *chatCompletionsBackend) convertMessages(messages []Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	result := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		apiMsg, err := b.convertMessage(msg)
		if err != nil {
			return nil, err
		}
		result = append(result, apiMsg)
	}
	return result, nil
}

func (b *chatCompletionsBackend) convertMessage(msg Message) (openai.ChatCompletionMessageParamUnion, error) {
	switch msg.Role {
	case RoleSystem:
		text := ""
		if len(msg.Parts) > 0 && msg.Parts[0].Type == ContentPartText {
			text = msg.Parts[0].Text
		}
		return openai.SystemMessage(text), nil

	case RoleUser:
		hasImage := false
		for _, p := range msg.Parts {
			if p.Type == ContentPartImageURL {
				hasImage = true
				break
			}
		}

		if !hasImage {
			// 拼接全部文本片段：多片段的用户消息（图片落盘后为文本标记、
			// 或本身含多段文本）只取首段会静默丢内容
			return openai.UserMessage(ExtractMessageText(msg)), nil
		}

		parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(msg.Parts))
		for _, p := range msg.Parts {
			switch p.Type {
			case ContentPartText:
				parts = append(parts, openai.TextContentPart(p.Text))
			case ContentPartImageURL:
				parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL: p.ImageURL,
				}))
			}
		}
		return openai.UserMessage(parts), nil

	case RoleAssistant:
		text := ""
		if len(msg.Parts) > 0 && msg.Parts[0].Type == ContentPartText {
			text = msg.Parts[0].Text
		}

		if len(msg.ToolCalls) == 0 {
			msgUnion := openai.AssistantMessage(text)
			if msg.ReasoningContent != "" {
				if asst := msgUnion.OfAssistant; asst != nil {
					asst.SetExtraFields(map[string]any{
						"reasoning_content": msg.ReasoningContent,
					})
				}
			}
			return msgUnion, nil
		}

		// Assistant message with tool calls
		toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: tc.ID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				},
			})
		}

		assistant := openai.ChatCompletionAssistantMessageParam{}
		if text != "" {
			assistant.Content.OfString = openai.String(text)
		}
		assistant.ToolCalls = toolCalls
		if msg.ReasoningContent != "" {
			assistant.SetExtraFields(map[string]any{
				"reasoning_content": msg.ReasoningContent,
			})
		}
		return openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}, nil

	case RoleTool:
		content := ""
		if len(msg.Parts) > 0 && msg.Parts[0].Type == ContentPartText {
			content = msg.Parts[0].Text
		}
		return openai.ToolMessage(content, msg.ToolCallID), nil

	default:
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("unknown message role: %s", msg.Role)
	}
}

func (b *chatCompletionsBackend) convertToolDef(td llmtool.ToolDef) openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        td.Function.Name,
				Description: openai.String(td.Function.Description),
				Parameters:  td.Function.Parameters,
			},
		},
	}
}

func (b *chatCompletionsBackend) parseResponse(completion *openai.ChatCompletion) (GenerateResponse, TokenUsage) {
	choice := completion.Choices[0]

	resp := GenerateResponse{
		Content: choice.Message.Content,
		// finish_reason=length：输出因 max_tokens 截断
		Truncated: choice.FinishReason == "length",
	}

	// 从原始响应 JSON 中提取 reasoning_content（DeepSeek 等 API 的推理过程字段）
	if raw := choice.Message.RawJSON(); raw != "" {
		var rawMap map[string]any
		if err := json.Unmarshal([]byte(raw), &rawMap); err == nil {
			if rc, ok := rawMap["reasoning_content"].(string); ok && rc != "" {
				resp.ReasoningContent = rc
			}
		}
	}

	for _, tc := range choice.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, llmtool.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	usage := TokenUsage{}
	if completion.Usage.PromptTokens > 0 {
		usage.PromptTokens = int(completion.Usage.PromptTokens)
	}
	if completion.Usage.CompletionTokens > 0 {
		usage.CompletionTokens = int(completion.Usage.CompletionTokens)
	}
	if completion.Usage.TotalTokens > 0 {
		usage.TotalTokens = int(completion.Usage.TotalTokens)
	}
	usage.CachedTokens = extractCachedTokens(&completion.Usage)

	return resp, usage
}

// extractCachedTokens 提取命中 prompt 缓存的 token 数。优先取 OpenAI 标准字段
// prompt_tokens_details.cached_tokens；DeepSeek 等提供方返回的是扩展字段
// prompt_cache_hit_tokens，SDK 未建模，需从原始 JSON 兜底解析。均无时返回 0。
func extractCachedTokens(usage *openai.CompletionUsage) int {
	if usage.PromptTokensDetails.CachedTokens > 0 {
		return int(usage.PromptTokensDetails.CachedTokens)
	}
	if raw := usage.RawJSON(); raw != "" {
		var rawMap map[string]any
		if err := json.Unmarshal([]byte(raw), &rawMap); err == nil {
			if hit, ok := rawMap["prompt_cache_hit_tokens"].(float64); ok && hit > 0 {
				return int(hit)
			}
		}
	}
	return 0
}
