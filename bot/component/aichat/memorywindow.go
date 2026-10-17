package aichat

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// CompressorFunc 上下文压缩函数；返回压缩后的消息与本次压缩的 token 用量
// （用量由 ChatBot.Chat 并入当次请求统计，失败路径返回零值）。
type CompressorFunc func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error)

type messageWindow struct {
	messages         []Message
	maxContextTokens int
	llmClient        *LLMClient
	compressor       CompressorFunc
	// compressorClient 上下文压缩专用 LLM 客户端（可独立配置更便宜的模型）；
	// nil 时复用主对话 llmClient。
	compressorClient *LLMClient
	lastPromptTokens int
	// forceCompress 上一轮以「上下文超限」类错误失败，本轮必须先压缩
	// （见 forceCompressNext）
	forceCompress bool
	// compressUsage 最近一次成功压缩的 token 用量，由 ChatBot.Chat 取走并入统计
	compressUsage TokenUsage
	store         HistoryStore
	// hookRunner/hookBase 钩子执行器与会话身份，由 ChatBot.SetHookRunner 注入；
	// nil 时 PreCompact 埋点跳过
	hookRunner HookRunner
	hookBase   agenthook.Payload
}

func newMessageWindow(maxContextTokens int, llmClient *LLMClient, compressor CompressorFunc, store HistoryStore, compressorClient ...*LLMClient) *messageWindow {
	w := &messageWindow{
		maxContextTokens: maxContextTokens,
		llmClient:        llmClient,
		compressor:       compressor,
		store:            store,
	}
	if len(compressorClient) > 0 {
		w.compressorClient = compressorClient[0]
	}
	return w
}

// setHookRunner 注入钩子执行器与会话身份（PreCompact 埋点），传 nil 取消。
func (w *messageWindow) setHookRunner(r HookRunner, base agenthook.Payload) {
	w.hookRunner = r
	w.hookBase = base
}

// load 从持久化存储回放历史；存储为空或未注入时保持空窗口。
func (w *messageWindow) load(ctx context.Context) {
	if w.store == nil {
		return
	}
	msgs, err := w.store.Load(ctx)
	if err != nil {
		// 加载失败不应阻断对话，按空历史继续，后续 Save 会覆盖
		return
	}
	// 落盘副本已剔除图片片段（见 degradeImagesForPersist）；回放前再过一遍
	// 降级以清洗旧版本落盘的文本内嵌 data URI 标记（MB 级 base64 会随历史轮轮重发）
	w.messages = degradeImagesForPersist(msgs)
}

// persistAppend 将新追加的消息增量落盘；store 未注入或增量为空时为空操作。
// 落盘副本中的图片片段一律降级为文本标记（degradeImagesForPersist）：
// 远程 http(s) 链接重启后失效无保留价值；data URI（base64 内联图片）体积
// 可达 MB 级，原样落盘会撑大单行体积。仅影响落盘数据，内存中当前会话的
// 消息仍保留图片供本轮对话使用。
// 使用独立的后台 context，避免请求被 /stop 取消时丢失刚写入的历史。
func (w *messageWindow) persistAppend(msgs []Message) {
	if w.store == nil || len(msgs) == 0 {
		return
	}
	if err := w.store.Append(context.Background(), degradeImagesForPersist(msgs)); err != nil {
		// 落盘失败仅记录，不影响内存中的对话
		_ = err
	}
}

// persistReplace 将当前历史全量覆盖落盘（上下文压缩/截断重排后调用）；
// store 未注入时为空操作。图片降级语义同 persistAppend。
func (w *messageWindow) persistReplace() {
	if w.store == nil {
		return
	}
	if err := w.store.Replace(context.Background(), degradeImagesForPersist(w.messages)); err != nil {
		// 落盘失败仅记录，不影响内存中的对话
		_ = err
	}
}

// inlineDataURIRe 匹配消息文本标记里内联的 data URI（url:data:<mime>;base64,<payload>）。
// 旧版本会把适配器内联图片的完整 data URI 写进 [图片 <hash> url:...] 标记并随之落盘，
// MB 级 base64 会在回放与压缩重写时反复进入上下文；此处统一清洗为仅保留哈希的标记。
var inlineDataURIRe = regexp.MustCompile(` url:data:[^;\s\]]+;base64,[A-Za-z0-9+/=]+`)

// degradeImagesForPersist 将待落盘消息中的所有图片片段（http(s) 与 data URI）
// 替换为文本标记，并剔除文本标记里内联的 data URI，返回新切片，不修改原消息。
func degradeImagesForPersist(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	for i := range msgs {
		msg := msgs[i]
		if len(msg.Parts) == 0 {
			out[i] = msg
			continue
		}
		changed := false
		newParts := make([]ContentPart, 0, len(msg.Parts))
		for _, p := range msg.Parts {
			if p.Type == ContentPartImageURL {
				// 降级为带哈希的文本标记，与消息文本中的图片标识一致
				newParts = append(newParts, TextPart("[图片 "+message.ImageHash(p.ImageURL)+"]"))
				changed = true
				continue
			}
			if p.Type == ContentPartText && strings.Contains(p.Text, "url:data:") {
				if cleaned := inlineDataURIRe.ReplaceAllString(p.Text, ""); cleaned != p.Text {
					newParts = append(newParts, TextPart(cleaned))
					changed = true
					continue
				}
			}
			newParts = append(newParts, p)
		}
		if changed {
			msg.Parts = newParts
		}
		out[i] = msg
	}
	return out
}

func (w *messageWindow) append(msgs ...Message) {
	w.messages = append(w.messages, msgs...)
	w.persistAppend(msgs)
}

func (w *messageWindow) history() []Message {
	return w.messages
}

// clear 清空内存窗口，并删除持久化历史（重启后也不再恢复）。
// 返回存储层的错误：调用方需要知道清空是否真正生效（内存已空但 DB 未清时，
// 会话淘汰/进程重启会把旧上下文整段回放）。
func (w *messageWindow) clear(ctx context.Context) error {
	w.messages = nil
	w.lastPromptTokens = 0
	w.compressUsage = TokenUsage{}
	w.forceCompress = false
	if w.store == nil {
		return nil
	}
	if err := w.store.Clear(ctx); err != nil {
		return fmt.Errorf("清除持久化历史失败: %w", err)
	}
	return nil
}

func (w *messageWindow) RecordUsage(usage TokenUsage) {
	// 压缩判断需要的是当前上下文的真实大小（最后一次调用的 prompt token），
	// 而非跨工具轮次累加的 PromptTokens——累加值会虚高，导致过早触发有损压缩
	if usage.LastPromptTokens > 0 {
		w.lastPromptTokens = usage.LastPromptTokens
	} else if usage.PromptTokens > 0 {
		w.lastPromptTokens = usage.PromptTokens
	}
}

func (w *messageWindow) needsCompression() bool {
	if w.maxContextTokens <= 0 {
		return false
	}
	threshold := int(float64(w.maxContextTokens) * 0.8)
	if w.lastPromptTokens > 0 {
		return w.lastPromptTokens > threshold
	}
	// 上游未上报 usage 时用字符数粗估兜底：缺少真实 token 统计不代表上下文很小，
	// 若不兜底历史会无限增长，撑大落盘 key 并拖慢每轮的全量读写。
	return w.estimateTokens() > threshold
}

// forceCompressNext 标记「上一轮以超限类错误失败，下一轮必须先压缩」。
// 失败请求不更新 lastPromptTokens，若历史恰好在阈值之下、单轮工具循环却把
// 请求撑过模型窗口，就会形成「每轮都失败且永不触发压缩」的循环；
// markOverflowFailure 在失败路径调用，使下一轮 Chat 强制执行一次压缩/截断。
func (w *messageWindow) forceCompressNext() {
	w.forceCompress = true
}

// takeForceCompress 取走并清除强制压缩标记。
func (w *messageWindow) takeForceCompress() bool {
	v := w.forceCompress
	w.forceCompress = false
	return v
}

// estimateTokens 用字符数粗估当前历史的 token 量，仅用于 usage 缺失时的兜底判断。
// 约 2 个字符折 1 token（中英文混合的保守估计）；图片片段按固定值估算，
// 不统计 data URI 的 base64 长度（与真实多模态 token 计量无关）。
func (w *messageWindow) estimateTokens() int {
	total := 0
	for _, m := range w.messages {
		for _, p := range m.Parts {
			if p.Type == ContentPartImageURL {
				total += 1000 // 多模态图片的约值
				continue
			}
			total += utf8.RuneCountInString(p.Text)
		}
		total += utf8.RuneCountInString(m.ReasoningContent)
		// Anthropic thinking 块原始 JSON（含签名，回放前也占历史体积）同样计入
		total += len(m.ThinkingBlocks)
		for _, tc := range m.ToolCalls {
			total += utf8.RuneCountInString(tc.Name) + utf8.RuneCountInString(tc.Arguments)
		}
	}
	return total / 2
}

func (w *messageWindow) MaybeCompress(ctx context.Context) error {
	if !w.needsCompression() && !w.takeForceCompress() {
		return nil
	}
	if w.compressor == nil || w.llmClient == nil {
		return nil
	}

	// PreCompact 钩子（仅通知）：压缩是对话存续的必要环节，不允许钩子阻断
	if w.hookRunner != nil {
		_ = w.hookRunner.Run(ctx, agenthook.EventPreCompact, w.hookBase)
	}

	// 压缩客户端可选独立配置：未设置时复用主对话 client
	client := w.llmClient
	if w.compressorClient != nil {
		client = w.compressorClient
	}

	compressed, compressUsage, err := w.compressor(ctx, client, w.messages)
	if err != nil {
		// 压缩失败（网络抖动/限流恰好在上下文最满时最易发生）不能阻断对话：
		// 直接丢弃最旧一半历史降级，保证本轮用户消息能正常处理与落盘。
		// 截断后 lastPromptTokens 置 0，若仍超阈值下一轮会再走压缩/截断，逐步收敛。
		w.truncateOldestHalf()
		return nil
	}

	w.messages = compressed
	w.lastPromptTokens = 0
	w.compressUsage = compressUsage
	// 压缩后历史发生改变，需落盘覆盖旧记录
	w.persistReplace()
	return nil
}

// markOverflowFailure 记录「本轮请求以超限类错误失败」：下一轮 Chat 构建前
// 强制执行一次压缩/截断。供 ChatBot 在错误路径调用（见 forceCompressNext）。
func (w *messageWindow) markOverflowFailure() {
	w.forceCompressNext()
	// 失败请求没有真实 usage，清掉旧值让下一轮走字符数粗估兜底
	w.lastPromptTokens = 0
}

// takeCompressUsage 取走最近一次压缩的 token 用量（取后清零），
// 由 ChatBot.Chat 并入当次请求的 usage 返回，供 token 统计与配额计量。
func (w *messageWindow) takeCompressUsage() TokenUsage {
	u := w.compressUsage
	w.compressUsage = TokenUsage{}
	return u
}

// truncateOldestHalf 压缩失败时的降级策略：丢弃最旧一半历史（至少保留 1 条）。
// 切点需对齐 tool_call 边界：保留区的第一条若是 tool 结果消息，说明其对应的
// assistant tool_calls 在被丢弃的一半里——孤立的 tool 消息会被 OpenAI 兼容 API
// 拒绝（400：tool 消息必须跟在带 tool_calls 的 assistant 之后），且截断已落盘，
// 此后每轮请求都会失败（sticky 400）。这里把切点后移越过所有孤立 tool 消息；
// 极端情况下全部跳过即清空历史（等效新对话，请求合法）。
func (w *messageWindow) truncateOldestHalf() {
	keep := max(len(w.messages)/2, 1)
	start := len(w.messages) - keep
	for start < len(w.messages) && w.messages[start].Role == RoleTool {
		start++
	}
	w.messages = w.messages[start:]
	w.lastPromptTokens = 0
	w.persistReplace()
}

// ExtractMessageText 提取消息中的纯文本内容
func ExtractMessageText(msg Message) string {
	var parts []string
	for _, p := range msg.Parts {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// FormatMessagesForSummary 将消息格式化为摘要用的文本
func FormatMessagesForSummary(msgs []Message) string {
	var buf strings.Builder
	for _, m := range msgs {
		if m.Role == RoleTool {
			continue
		}
		if len(m.ToolCalls) > 0 {
			continue
		}
		text := ExtractMessageText(m)
		if text == "" {
			continue
		}
		roleName := string(m.Role)
		switch m.Role {
		case RoleUser:
			roleName = "用户"
		case RoleAssistant:
			roleName = "助手"
		case RoleSystem:
			roleName = "系统"
		}
		buf.WriteString(fmt.Sprintf("[%s]: %s\n", roleName, text))
	}
	return buf.String()
}

// NewContextCompressor 创建上下文压缩函数。
// 压缩结果以 user 角色消息保存（"之前的对话摘要"），而不是 system 角色：
// 请求构建时会在最前注入一份完整 system prompt（含 skills 注册表、场景提示），
// 若摘要也作为 system 消息进入历史，同一份 basePrompt 会在请求里出现两次，
// 既浪费 token 又稀释指令；且压缩后再次压缩时 basePrompt 会被反复带进摘要。
// 压缩器也不再拼接 basePrompt，摘要只含对话本身。
func NewContextCompressor(basePrompt string) CompressorFunc {
	return func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error) {
		text := FormatMessagesForSummary(oldMsgs)
		if text == "" {
			return []Message{TextMessage(RoleUser, "[对话摘要]（无内容）")}, TokenUsage{}, nil
		}

		compressPrompt := "你是一个对话摘要助手。请对以下历史对话进行简洁的摘要，保留关键信息、用户意图、讨论结论和重要上下文。工具调用细节和中间推理过程可以省略。"

		tempBuilder := NewMessageBuilder(compressPrompt)
		summaryMessages := tempBuilder.BuildChatMessages(text, nil)

		summary, usage, err := client.GenerateSingleWithUsage(ctx, summaryMessages, ChatOptions{})
		if err != nil {
			return nil, usage, err
		}

		summary = RemoveThinkContent(summary)

		return []Message{TextMessage(RoleUser, "以下是之前的对话摘要：\n"+summary)}, usage, nil
	}
}
