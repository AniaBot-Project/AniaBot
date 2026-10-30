package pluginaichat

import (
	"sync"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
)

// usageAcc token 用量累加器（goroutine 安全）：归集主请求 LLM 循环之外派生的
// 消耗（异步子代理 / team_run 成员 / 备用图片识别等），供收尾时并入统计。
// 只累计计费字段（tokens / cached / iterations）；LastPromptTokens 表示
// 「当前上下文真实大小」，派生调用的该值无意义，不累加。
type usageAcc struct {
	mu sync.Mutex
	u  aichat.TokenUsage
}

func (a *usageAcc) add(u aichat.TokenUsage) {
	a.mu.Lock()
	a.u.PromptTokens += u.PromptTokens
	a.u.CompletionTokens += u.CompletionTokens
	a.u.TotalTokens += u.TotalTokens
	a.u.CachedTokens += u.CachedTokens
	a.u.Iterations += u.Iterations
	a.mu.Unlock()
}

// take 取出累计值并清零
func (a *usageAcc) take() aichat.TokenUsage {
	a.mu.Lock()
	u := a.u
	a.u = aichat.TokenUsage{}
	a.mu.Unlock()
	return u
}

// mergeTokenUsage 把 src 的计费字段（tokens / cached / iterations）并入 dst 返回。
// 不合并 LastPromptTokens：派生调用（压缩/子代理/图片识别）的该值不代表
// 主会话当前上下文大小，并入会污染压缩阈值判断。
func mergeTokenUsage(dst, src aichat.TokenUsage) aichat.TokenUsage {
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.TotalTokens += src.TotalTokens
	dst.CachedTokens += src.CachedTokens
	dst.Iterations += src.Iterations
	return dst
}

// addExtraUsage 把一次派生 LLM 调用的用量累加到会话级暂存，在下一次
// finishQuery 时并入该会话的 Query 日志（takeExtraUsage）。
//
// 归属语义：同步派生（team_run 成员等在主请求内完成的）计入当次请求；
// 异步派生（异步子代理在主请求结束后才完成的）计入该会话的下一条 Query
// 日志——统计口径是「会话粒度的总成本」，而非严格按请求归属。
// Query 日志不可用（记录器未初始化，如持久化不支持 SQL）时不暂存，
// 避免累计器无限增长；配额计量不受影响，走各调用点独立的 quotaManager.Add。
func (p *AIChatPlugin) addExtraUsage(sessionKey string, u aichat.TokenUsage) {
	if p.queryLogger == nil {
		return
	}
	if u.TotalTokens <= 0 && u.PromptTokens <= 0 {
		return
	}
	v, _ := p.extraUsage.LoadOrStore(sessionKey, &usageAcc{})
	v.(*usageAcc).add(u)
}

// takeExtraUsage 取出会话暂存的派生用量（取后清零）；无暂存时返回零值。
func (p *AIChatPlugin) takeExtraUsage(sessionKey string) aichat.TokenUsage {
	v, ok := p.extraUsage.LoadAndDelete(sessionKey)
	if !ok {
		return aichat.TokenUsage{}
	}
	return v.(*usageAcc).take()
}
