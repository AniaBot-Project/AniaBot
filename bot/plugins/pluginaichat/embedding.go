package pluginaichat

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/jeanhua/AniaBot/bot/version"
)

// embeddingTimeout embedding 请求超时：服务无响应时调用方（kb_add/kb_search）
// 不能永久挂起，超时后退回纯关键词检索。
const embeddingTimeout = 30 * time.Second

// embedInjectTimeout 自动注入路径的 embedding 超时：注入发生在每轮用户消息
// 处理流程内，超时过长会直接拖慢 AI 响应，超时后本轮退化为纯关键词注入。
const embedInjectTimeout = 10 * time.Second

// embedCacheCap 查询向量缓存容量上限。群聊消息重复率低，缓存主要兜住
// 重试/排队消息与相似问法，无需太大。
const embedCacheCap = 128

// embedder 封装 OpenAI 兼容的 embeddings 客户端（复用 openai-go/v3 的
// EmbeddingService，与 LLMClient 同一套 SDK）。
//
// 所有调用失败时静默返回 nil，由 knowledgeManager 退回纯关键词检索，
// 不阻塞文档写入与检索主流程。provider 若未实现 /v1/embeddings（如老版
// DeepSeek），自动退化为关键词检索，功能不致整体失效。
type embedder struct {
	client openai.Client
	model  string
	logger *slog.Logger

	// 查询向量缓存：自动注入每轮都要 embed 用户消息，缓存相同文本的向量
	// 避免重复付费/耗时。FIFO 淘汰，cap 见 embedCacheCap。
	cacheMu sync.Mutex
	cache   map[string][]float32
	cacheQ  []string
}

// newEmbedder 创建语义向量计算器；baseURL/apiKey/model 任一为空返回 nil。
// headers 为自定义请求头（plugin.ai_chat_bot.headers），与 LLM 请求保持一致，
// 走自定义网关时 embedding 请求同样需要携带。
func newEmbedder(baseURL, apiKey, model string, headers map[string]string, logger *slog.Logger) *embedder {
	if baseURL == "" || apiKey == "" || model == "" {
		return nil
	}
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		// 与 LLMClient 一致，覆盖 SDK 默认 UA 标识请求来源与版本
		option.WithHeader("User-Agent", version.UserAgent()),
	}
	for k, v := range headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	return &embedder{
		client: openai.NewClient(opts...),
		model:  model,
		logger: logger,
		cache:  make(map[string][]float32),
	}
}

// buildKBEmbedder 按 plugin.ai_chat_bot.kb.embedding 配置构造知识库与长期记忆
// 共享的语义向量计算器；未启用或配置不完整时返回 nil（对应功能自动退化为
// 纯关键词检索）。embedder 无状态且 openai.Client 并发安全，可跨功能共享。
func (p *AIChatPlugin) buildKBEmbedder() *embedder {
	if !p.cfg.Kb.Embedding.Enable {
		return nil
	}
	embBaseURL := p.cfg.Kb.Embedding.BaseURL
	embAPIKey := p.cfg.Kb.Embedding.APIKey
	embModel := p.cfg.Kb.Embedding.Model
	if embBaseURL == "" {
		embBaseURL = p.cfg.BaseURL
	}
	if embAPIKey == "" {
		embAPIKey = p.cfg.APIKey
	}
	if embModel == "" {
		embModel = "jina-embeddings-v3"
	}
	emb := newEmbedder(embBaseURL, embAPIKey, embModel, p.customHeaders, p.Logger.WithGroup("kb-embedding"))
	if emb == nil {
		p.Logger.Warn("向量检索未启用：Embedding 配置不完整（base_url/api_key/model 缺项）")
	}
	return emb
}

// EmbedMany 批量计算文本向量，与输入顺序一一对应；出错返回 nil。
// 内部强制请求超时：调用方即使传入 context.Background()（如 kb_add 入库路径），
// embedding 服务无响应时也会超时退回关键词检索，而不是永久挂起。
func (e *embedder) EmbedMany(ctx context.Context, texts []string) [][]float32 {
	return e.embedMany(ctx, texts, embeddingTimeout)
}

// embedMany 按指定超时批量计算文本向量。
func (e *embedder) embedMany(ctx context.Context, texts []string, timeout time.Duration) [][]float32 {
	if e == nil || len(texts) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(e.model),
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
	})
	if err != nil {
		if e.logger != nil {
			e.logger.Warn("embedding 调用失败，退回关键词检索", "error", err)
		}
		return nil
	}
	// 校验返回数量与顺序：兼容 provider 若缺失/乱序返回，向量会错配到错误文本块，
	// 不如整体退回关键词检索
	if len(resp.Data) != len(texts) {
		if e.logger != nil {
			e.logger.Warn("embedding 返回数量与输入不一致，退回关键词检索", "input", len(texts), "got", len(resp.Data))
		}
		return nil
	}
	out := make([][]float32, 0, len(resp.Data))
	for _, d := range resp.Data {
		vec := make([]float32, 0, len(d.Embedding))
		for _, v := range d.Embedding {
			vec = append(vec, float32(v))
		}
		out = append(out, vec)
	}
	return out
}

// EmbedOne 计算单个文本向量；出错返回 nil。
func (e *embedder) EmbedOne(ctx context.Context, text string) []float32 {
	vs := e.EmbedMany(ctx, []string{text})
	if len(vs) == 0 {
		return nil
	}
	return vs[0]
}

// EmbedOneCached 计算单个文本向量并缓存结果（key 为去首尾空白后的原文）。
// 供每轮对话的自动注入路径使用：相同/重试消息直接命中缓存，
// 不为每条用户消息重复支付 embedding 成本。超时收紧为 embedInjectTimeout，
// 服务缓慢时快速退化为纯关键词注入，不拖慢 AI 响应。出错返回 nil。
func (e *embedder) EmbedOneCached(ctx context.Context, text string) []float32 {
	if e == nil {
		return nil
	}
	key := strings.TrimSpace(text)
	if key == "" {
		return nil
	}
	e.cacheMu.Lock()
	if vec, ok := e.cache[key]; ok {
		e.cacheMu.Unlock()
		return vec
	}
	e.cacheMu.Unlock()

	vs := e.embedMany(ctx, []string{key}, embedInjectTimeout)
	if len(vs) == 0 {
		return nil
	}
	vec := vs[0]

	e.cacheMu.Lock()
	if len(e.cacheQ) >= embedCacheCap {
		// FIFO 淘汰最旧的一条
		delete(e.cache, e.cacheQ[0])
		e.cacheQ = e.cacheQ[1:]
	}
	e.cache[key] = vec
	e.cacheQ = append(e.cacheQ, key)
	e.cacheMu.Unlock()
	return vec
}
