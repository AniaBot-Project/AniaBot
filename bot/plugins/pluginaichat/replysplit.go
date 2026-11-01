package pluginaichat

import "strings"

// 回复拆分：AI 在回复中用 ||| 表示「下一条消息从这里开始」，拆成多条消息
// 逐条发送，而不是挤在一条长消息里。分隔符写进提示词、AI 输出即生效，没有
// 开关；代码围栏（``` 块）内的 ||| 是正文，不参与拆分。

// replySeparator 是回复中的消息边界标记：模型连续输出 ||| 表示下一条消息开始。
const replySeparator = "|||"

// splitBySeparator 按 ||| 拆分文本。只有代码围栏（TrimSpace 后以 ``` 开头的
// 行所包住的内容）之外的 ||| 才算消息边界，避免把示例代码、表格内容拆散；
// 段内保留单个换行，片段去除首尾空白并丢弃空片段。文本中没有生效的分隔符
// 时返回 nil，由调用方回退为原文，保证不拆分时的语义与以前完全一致。
func splitBySeparator(text string) []string {
	// 统一换行为 \n：模型输出可能带 \r\n（或罕见的 \r）
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	var segments []string
	var current []string
	splitAny := false
	flush := func() {
		if seg := strings.TrimSpace(strings.Join(current, "\n")); seg != "" {
			segments = append(segments, seg)
		}
		current = current[:0]
	}

	inFence := false
	for _, line := range strings.Split(normalized, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			current = append(current, line)
			continue
		}
		if inFence || !strings.Contains(line, replySeparator) {
			current = append(current, line)
			continue
		}
		splitAny = true
		parts := strings.Split(line, replySeparator)
		for i, part := range parts {
			if i > 0 {
				flush()
			}
			current = append(current, part)
		}
	}
	flush()

	if !splitAny || len(segments) == 0 {
		return nil
	}
	return segments
}

// replySegments 返回回复实际要发送的消息片段：按 ||| 分隔符拆分，只有一条
// 时就是整段原文。拆分结果为空（整段均为空白或分隔符）时回退为原文，交由
// 调用方按原有语义处理（发送空白内容或跳过），不静默丢消息。
func replySegments(text string) []string {
	if segs := splitBySeparator(text); len(segs) > 0 {
		return segs
	}
	return []string{text}
}

// streamDisplayText 把回复原文转换成流式展示用的文本。流式始终只维持一条
// 消息、无法真的拆成多条，因此把 ||| 显示为换行，避免把标记原样露给用户；
// 文本中没有分隔符时原样返回，不改变原有展示行为。
func streamDisplayText(text string) string {
	if !strings.Contains(text, replySeparator) {
		return text
	}
	segs := splitBySeparator(text)
	if len(segs) == 0 {
		return text
	}
	return strings.Join(segs, "\n\n")
}
