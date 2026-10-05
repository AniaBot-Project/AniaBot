package aichat

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/version"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// responsesBackend OpenAI Responses API 格式后端。
//
// 与 Chat Completions 的主要差异：system 走 instructions 参数；对话历史以
// input 项（message / function_call / function_call_output）表达；tool call
// 以 function_call 输出项返回（call_id 作为工具调用 ID）。
type responsesBackend struct {
	client openai.Client
	model  string
}

// newResponsesBackend 构造后端。headers 为自定义请求头，追加在默认头之后，
// 与默认头同名时生效者为准（可覆盖 User-Agent 等）。
func newResponsesBackend(baseURL, apiKey, model string, headers map[string]string) *responsesBackend {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		// 覆盖 SDK 默认 UA，标识请求来源与版本
		option.WithHeader("User-Agent", version.UserAgent()),
	}
	for k, v := range headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	return &responsesBackend{
		client: openai.NewClient(opts...),
		model:  model,
	}
}

func (b *responsesBackend) generate(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, error) {
	params, err := b.buildParams(messages, opts)
	if err != nil {
		return GenerateResponse{}, TokenUsage{}, err
	}

	resp, err := b.client.Responses.New(ctx, params)
	if err != nil {
		if ctx.Err() != nil {
			return GenerateResponse{}, TokenUsage{}, ctx.Err()
		}
		return GenerateResponse{}, TokenUsage{}, err
	}

	return parseResponsesOutput(resp), responsesTokenUsage(resp.Usage), nil
}

func (b *responsesBackend) generateStream(ctx context.Context, messages []Message, opts ChatOptions) (GenerateResponse, TokenUsage, bool, error) {
	params, err := b.buildParams(messages, opts)
	if err != nil {
		return GenerateResponse{}, TokenUsage{}, false, err
	}

	stream := b.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	acc := newResponsesStreamAccumulator(opts.OnStreamDelta)
	started := false
	for stream.Next() {
		event := stream.Current()
		acc.Add(event)
		started = true // 任何流事件都算已开始输出
	}
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			return acc.Result(), acc.usage, started, ctx.Err()
		}
		return acc.Result(), acc.usage, started, err
	}

	resp := acc.Result()
	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		return resp, acc.usage, started, fmt.Errorf("no content returned from LLM")
	}
	return resp, acc.usage, started, nil
}

// buildParams 组装 Responses 请求参数。
func (b *responsesBackend) buildParams(messages []Message, opts ChatOptions) (responses.ResponseNewParams, error) {
	input, instructions, err := convertResponsesInput(messages)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(b.model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
	}
	if instructions != "" {
		params.Instructions = openai.String(instructions)
	}
	if opts.MaxToken != nil {
		params.MaxOutputTokens = openai.Int(int64(*opts.MaxToken))
	}
	if opts.MaxCompletionToken != nil {
		params.MaxOutputTokens = openai.Int(int64(*opts.MaxCompletionToken))
	}
	if opts.Temperature != nil {
		params.Temperature = openai.Float(*opts.Temperature)
	}
	if opts.TopP != nil {
		params.TopP = openai.Float(*opts.TopP)
	}
	// top_k 非 Responses API 参数，忽略
	if opts.ReasoningEffort != nil {
		switch *opts.ReasoningEffort {
		case "low":
			params.Reasoning.Effort = shared.ReasoningEffortLow
		case "medium":
			params.Reasoning.Effort = shared.ReasoningEffortMedium
		case "high":
			params.Reasoning.Effort = shared.ReasoningEffortHigh
		}
	}
	if len(opts.Tools) > 0 {
		tools := make([]responses.ToolUnionParam, 0, len(opts.Tools))
		for _, td := range opts.Tools {
			tools = append(tools, responses.ToolUnionParam{
				OfFunction: &responses.FunctionToolParam{
					Name:        td.Function.Name,
					Description: openai.String(td.Function.Description),
					Parameters:  td.Function.Parameters,
				},
			})
		}
		params.Tools = tools
	}
	return params, nil
}

// convertResponsesInput 把内部消息模型转换为 Responses input 项数组 + instructions。
// assistant 消息拆为 output message 项（文本）+ function_call 项（工具调用）；
// tool 结果转为 function_call_output 项。
func convertResponsesInput(messages []Message) (responses.ResponseInputParam, string, error) {
	var items responses.ResponseInputParam
	var instructions []string

	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			if text := ExtractMessageText(msg); text != "" {
				instructions = append(instructions, text)
			}

		case RoleUser:
			hasImage := false
			for _, p := range msg.Parts {
				if p.Type == ContentPartImageURL {
					hasImage = true
					break
				}
			}
			if !hasImage {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					ExtractMessageText(msg), responses.EasyInputMessageRoleUser))
				continue
			}
			parts := make(responses.ResponseInputMessageContentListParam, 0, len(msg.Parts))
			for _, p := range msg.Parts {
				switch p.Type {
				case ContentPartText:
					parts = append(parts, responses.ResponseInputContentParamOfInputText(p.Text))
				case ContentPartImageURL:
					// image_url 同时支持 http(s) 与 data: URI，原样透传
					img := responses.ResponseInputContentParamOfInputImage("")
					if p.ImageURL != "" {
						img.OfInputImage.ImageURL = openai.String(p.ImageURL)
					}
					parts = append(parts, img)
				}
			}
			items = append(items, responses.ResponseInputItemParamOfMessage(
				parts, responses.EasyInputMessageRoleUser))

		case RoleAssistant:
			if text := ExtractMessageText(msg); text != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					text, responses.EasyInputMessageRoleAssistant))
			}
			for _, tc := range msg.ToolCalls {
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(
					tc.Arguments, tc.ID, tc.Name))
			}

		case RoleTool:
			content := ""
			if len(msg.Parts) > 0 && msg.Parts[0].Type == ContentPartText {
				content = msg.Parts[0].Text
			}
			item := responses.ResponseInputItemParamOfFunctionCallOutput(content)
			item.OfFunctionCallOutput.CallID = param.NewOpt(msg.ToolCallID)
			items = append(items, item)

		default:
			return nil, "", fmt.Errorf("unknown message role: %s", msg.Role)
		}
	}
	return items, strings.Join(instructions, "\n"), nil
}

// parseResponsesOutput 解析非流式响应：文本经 OutputText 聚合，function_call
// 输出项转为工具调用。
func parseResponsesOutput(resp *responses.Response) GenerateResponse {
	result := GenerateResponse{
		Content: resp.OutputText(),
		// incomplete_details.reason=max_output_tokens：输出因 token 上限截断
		Truncated: resp.IncompleteDetails.Reason == "max_output_tokens",
	}
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			result.ToolCalls = append(result.ToolCalls, llmtool.ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments.OfString,
			})
		}
	}
	return result
}

// responsesTokenUsage 转换 token 用量，缓存命中取 input_tokens_details.cached_tokens。
func responsesTokenUsage(u responses.ResponseUsage) TokenUsage {
	return TokenUsage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		TotalTokens:      int(u.TotalTokens),
		CachedTokens:     int(u.InputTokensDetails.CachedTokens),
	}
}

// responsesStreamAccumulator Responses 流式事件累积器：文本增量、按 output_index
// 组装的 function_call 参数增量，usage 取 response.completed 携带的最终响应。
type responsesStreamAccumulator struct {
	content   strings.Builder
	toolCalls map[int64]*llmtool.ToolCall
	toolOrder []int64
	usage     TokenUsage
	// truncated 响应因 max_output_tokens 上限未完整生成（response.incomplete）
	truncated bool
	onDelta   func(string)
}

func newResponsesStreamAccumulator(onDelta func(string)) *responsesStreamAccumulator {
	return &responsesStreamAccumulator{
		toolCalls: map[int64]*llmtool.ToolCall{},
		onDelta:   onDelta,
	}
}

func (a *responsesStreamAccumulator) Add(event responses.ResponseStreamEventUnion) {
	switch event.Type {
	case "response.output_text.delta":
		a.content.WriteString(event.Delta)
		if a.onDelta != nil && event.Delta != "" {
			a.onDelta(event.Delta)
		}
	case "response.output_item.added":
		// function_call 输出项建立：携带 call_id 与 name
		if event.Item.Type == "function_call" {
			a.toolCalls[event.OutputIndex] = &llmtool.ToolCall{ID: event.Item.CallID, Name: event.Item.Name}
			a.toolOrder = append(a.toolOrder, event.OutputIndex)
		}
	case "response.function_call_arguments.delta":
		if tc, ok := a.toolCalls[event.OutputIndex]; ok {
			tc.Arguments += event.Delta
		}
	case "response.completed", "response.incomplete":
		a.usage = responsesTokenUsage(event.Response.Usage)
		// 输出因 max_output_tokens 上限截断：内容/工具参数都不完整
		if event.Response.IncompleteDetails.Reason == "max_output_tokens" {
			a.truncated = true
		}
		// completed 事件携带完整响应：以其中的 function_call 参数为最终值（按 call_id 匹配）
		for _, item := range event.Response.Output {
			if item.Type != "function_call" {
				continue
			}
			args := item.Arguments.OfString
			if args == "" {
				continue
			}
			for _, idx := range a.toolOrder {
				if tc := a.toolCalls[idx]; tc.ID == item.CallID {
					tc.Arguments = args
				}
			}
		}
	}
}

func (a *responsesStreamAccumulator) Result() GenerateResponse {
	resp := GenerateResponse{Content: a.content.String(), Truncated: a.truncated}
	order := append([]int64(nil), a.toolOrder...)
	slices.Sort(order)
	for _, idx := range order {
		resp.ToolCalls = append(resp.ToolCalls, *a.toolCalls[idx])
	}
	return resp
}
