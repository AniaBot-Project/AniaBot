package msglog

import (
	"regexp"
	"strings"
)

// 历史日志净化：早期版本把 file 段的 file 源（Bot 用 file 工具发送文件时的
// base64:// 内联负载）原样写进了 Text，一条日志可达数 MB，面板每翻一页都要
// 传输这些字节。写入侧已改为只记文件名，这里在读取侧兜底清理旧条目，避免
// 升级后仍被存量数据拖慢。
//
// 只处理形如 [文件:…] / [录音:…] / [视频:…] 的段标记里含内联负载的情况，
// 正常的 http(s) 地址不受影响。

var (
	// 只认真正的内联负载前缀：base64:// 或 data:<mime>/，避免把普通地址里
	// 恰好出现的 "data:" 子串当成内联负载整段抹掉。
	inlineMarkRe = regexp.MustCompile(`\[(文件|录音|视频):[^\]]*(?:base64://|data:[a-z0-9.+-]+/)[^\]]*\]`)
)

// 单条日志的媒体名 / 地址长度上限：超长基本只可能是被误写进来的内联负载。
const maxMediaFieldLen = 512

// sanitizeEntry 清理一条从存储读出的日志：抹掉内联负载残留，避免旧数据
// 继续占用面板带宽与浏览器内存。不修改存储里的原始条目。
func sanitizeEntry(e *Entry) {
	if strings.Contains(e.Text, "base64://") || strings.Contains(e.Text, "data:") {
		e.Text = inlineMarkRe.ReplaceAllString(e.Text, "[$1]")
	}
	for i := range e.Media {
		m := &e.Media[i]
		if len(m.Name) > maxMediaFieldLen ||
			strings.Contains(m.Name, "base64://") || strings.Contains(m.Name, "data:") {
			m.Name = "文件"
		}
		if len(m.URL) > maxMediaFieldLen {
			m.URL = ""
		}
	}
}
