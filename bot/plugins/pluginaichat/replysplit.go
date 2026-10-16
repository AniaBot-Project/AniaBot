package pluginaichat

import "strings"

// 回复按空行拆分（plugin.ai_chat_bot.reply.split_blank_line）：
// 模型习惯用空行分段时，把每段作为独立消息发送，而不是挤在一条长消息里。

// splitByBlankLine 按空行拆分文本：只含空白字符的行视为消息边界，边界
// 前后内容各自成为一段；段内保留单个换行。返回片段均去除首尾空白且不含
// 空片段；文本中没有空行时返回单元素切片。
func splitByBlankLine(text string) []string {
	// 统一换行为 \n：模型输出可能带 \r\n（或罕见的 \r），避免漏判空行
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	var segments []string
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		if seg := strings.TrimSpace(strings.Join(para, "\n")); seg != "" {
			segments = append(segments, seg)
		}
		para = para[:0]
	}
	for _, line := range strings.Split(normalized, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		para = append(para, line)
	}
	flush()
	return segments
}

// replySegments 返回回复实际要发送的消息片段：拆分开关开启时按空行拆分，
// 否则整段一条。拆分结果为空（整段均为空白）时回退为原文，交由调用方按
// 原有语义处理（发送空白内容或跳过），不静默丢消息。
func replySegments(text string, split bool) []string {
	if split {
		if segs := splitByBlankLine(text); len(segs) > 0 {
			return segs
		}
	}
	return []string{text}
}
