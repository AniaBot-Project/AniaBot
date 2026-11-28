package aichat

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// 工具循环保护（WatchdogOptions）的运行时实现与单请求内上下文保护。
//
// 提醒消息一律以 user 角色追加进 messages：接口上只有 user 消息允许自由插入，
// 且它们随本轮返回值进入插件层的会话历史（有意为之——同一会话后续回合里
// 模型仍能记得「刚才被提醒过」，避免换个回合继续原地打转）。

// WatchdogAbortError 循环保护终止工具循环时返回的错误。插件层用 errors.As
// 识别后给用户更准确的文案（例如「任务因重复执行相同工具调用被终止」）。
type WatchdogAbortError struct{ Reason string }

func (e *WatchdogAbortError) Error() string { return e.Reason }

// 循环保护提醒文案。
const (
	// repeatReminderFormat 相同工具调用重复提醒（%d 为当前连续重复次数）。
	repeatReminderFormat = "[系统提醒] 你连续 %d 次执行相同的工具调用，请检查参数或换一种方案；若已完成请直接给出最终回复。"
	// progressReminder 多轮工具调用后的委派提醒。
	progressReminder = "[系统提醒] 任务已进行多轮工具调用；如果存在可独立完成的子任务，考虑交给 subagent_run 后台执行。"
	// softDeadlineReminder 软截止到达后的收尾/转后台提醒。
	softDeadlineReminder = "[系统提醒] 本轮已运行较久：能收尾就收尾；如果还有较多工作，调用 task_handoff 把剩余工作转入后台，并给出进度摘要。"
)

// reminderText 返回实际注入的提醒文本：插件层按会话已注册的工具定制措辞
// （WatchdogOptions.ProgressReminder / SoftDeadlineReminder），未定制时用默认文案。
func reminderText(custom, fallback string) string {
	if strings.TrimSpace(custom) != "" {
		return custom
	}
	return fallback
}

// watchdogState 单次 ExecuteWithTools 调用的循环保护状态：不跨请求复用，
// 保证同一 ChatBot 串行调用下互不影响。
type watchdogState struct {
	cfg WatchdogOptions
	// lastSignature/repeatCount 连续相同工具调用签名及其重复次数；
	// 签名变化即重置（连续语义，不同工具交替不算重复）。
	lastSignature string
	repeatCount   int
	// warnedThresholds 当前重复串中已提醒过的阈值（每个阈值在每次连续重复
	// 中只提醒一次；签名变化后清零，允许新的重复串重新提醒）。
	warnedThresholds map[int]bool
	// toolRounds 本轮已执行的工具轮数（用于委派提醒）。
	toolRounds int
	// progressReminded/softDeadlineReminded 两类提醒整轮只注入一次。
	progressReminded     bool
	softDeadlineReminded bool
}

func newWatchdogState(cfg WatchdogOptions) *watchdogState {
	return &watchdogState{cfg: cfg, warnedThresholds: map[int]bool{}}
}

// observeRound 记录一轮工具调用的签名，返回当前连续重复次数。
// 签名相同 → 重复次数 +1；签名变化 → 重置为 1 并清空本轮重复串的提醒记录。
func (s *watchdogState) observeRound(calls []llmtool.ToolCall) int {
	sig := toolCallsSignature(calls)
	if sig == s.lastSignature {
		s.repeatCount++
	} else {
		s.lastSignature = sig
		s.repeatCount = 1
		s.warnedThresholds = map[int]bool{}
	}
	return s.repeatCount
}

// repeatWarnings 返回本轮需要注入的重复提醒文本（每个阈值只注入一次）。
func (s *watchdogState) repeatWarnings() []string {
	var out []string
	for _, threshold := range s.cfg.RepeatWarnThresholds {
		if threshold <= 0 || s.warnedThresholds[threshold] {
			continue
		}
		if s.repeatCount != threshold {
			continue
		}
		s.warnedThresholds[threshold] = true
		out = append(out, fmt.Sprintf(repeatReminderFormat, s.repeatCount))
	}
	return out
}

// shouldAbort 判断是否达到无进展终止阈值（NoProgressLimit；0=只用提醒不终止）。
func (s *watchdogState) shouldAbort() bool {
	return s.cfg.NoProgressLimit > 0 && s.repeatCount >= s.cfg.NoProgressLimit
}

// abortError 构造无进展终止错误：Reason 含连续重复次数与工具名，便于插件层
// 拼成用户可读提示。
func (s *watchdogState) abortError(calls []llmtool.ToolCall) error {
	return &WatchdogAbortError{Reason: fmt.Sprintf(
		"检测到连续 %d 次相同的工具调用（%s），判定为无进展，已终止本轮工具循环",
		s.repeatCount, toolNamesForReason(calls))}
}

// toolCallsSignature 计算一轮工具调用的签名：排序后的 name + "\x00" + arguments
// 列表拼接。同一轮内多个工具调用的顺序不影响签名（并行执行语义上无序）。
func toolCallsSignature(calls []llmtool.ToolCall) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, call.Name+"\x00"+call.Arguments)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00\x00")
}

// toolNamesForReason 提取本轮工具名（去重、保持出现顺序），用于终止原因文案。
func toolNamesForReason(calls []llmtool.ToolCall) string {
	seen := make(map[string]bool, len(calls))
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if seen[call.Name] {
			continue
		}
		seen[call.Name] = true
		names = append(names, call.Name)
	}
	return strings.Join(names, ",")
}

// 单请求内上下文保护常量。
const (
	// contextPruneRatio 单次 LLM 调用返回的 prompt 用量超过窗口的该比例时，
	// 触发工具结果裁剪（窗口级压缩由 ChatBot 在回合开始时处理，这里只做
	// 工具循环内部的兜底，防止几轮巨型工具输出把窗口直接撑爆）。
	contextPruneRatio = 0.85
	// contextPruneKeepRecent 最近 N 条消息永不裁剪：通常包含当轮用户输入与
	// 最近一轮工具往返，模型仍需要它们才能继续推理。
	contextPruneKeepRecent = 6
	// contextPruneKeptRunes 被裁剪的工具结果保留的总字符数（头尾各一半）。
	contextPruneKeptRunes = 2000
	// contextPruneMarker 裁剪标记：已裁剪消息再次触发保护时直接跳过。
	contextPruneMarker = "[工具结果过长，已裁剪]"
)

// pruneContextMessages 单请求内上下文保护：prompt 用量超过
// maxContextTokens×85% 时，把「较旧的大工具结果消息」裁剪为头尾摘要。
//
// 只修改消息内容、保持 tool_call 配对（Role/ToolCallID 不变），最近
// contextPruneKeepRecent 条消息完全不动。返回同一底层数组（原地改写局部
// 副本），不修改调用方历史：ChatBot 只把本轮新增消息写回历史，这里裁剪的是
// 当轮请求上下文，历史本身由回合开始时的窗口压缩负责。
func (o *ToolOrchestrator) pruneContextMessages(messages []Message, promptTokens int) []Message {
	if o.maxContextTokens <= 0 {
		return messages
	}
	threshold := int(float64(o.maxContextTokens) * contextPruneRatio)
	if promptTokens <= threshold {
		return messages
	}
	keepFrom := len(messages) - contextPruneKeepRecent
	for i := 0; i < keepFrom && i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != RoleTool || len(msg.Parts) == 0 {
			continue
		}
		text := ExtractMessageText(msg)
		if utf8.RuneCountInString(text) <= contextPruneKeptRunes || strings.HasPrefix(text, contextPruneMarker) {
			continue
		}
		messages[i] = Message{
			Role:       msg.Role,
			ToolCallID: msg.ToolCallID, // 保持与 assistant tool_calls 的配对
			Parts:      []ContentPart{TextPart(summarizeToolResult(text))},
		}
	}
	return messages
}

// summarizeToolResult 把过长工具结果压缩为「标记 + 原始长度 + 头尾片段」。
func summarizeToolResult(text string) string {
	runes := []rune(text)
	half := contextPruneKeptRunes / 2
	if len(runes) <= contextPruneKeptRunes {
		return text
	}
	head := string(runes[:half])
	tail := string(runes[len(runes)-half:])
	return fmt.Sprintf("%s 原始 %d 字符；开头：%s；结尾：%s",
		contextPruneMarker, len(runes), head, tail)
}
