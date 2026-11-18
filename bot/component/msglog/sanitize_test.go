package msglog

import (
	"strings"
	"testing"
)

// TestSanitizeEntryInlinePayload 旧日志里被写进 Text / Media.Name 的内联
// base64 负载在读取时被抹掉，正常地址与文本不受影响。
func TestSanitizeEntryInlinePayload(t *testing.T) {
	payload := strings.Repeat("QUJD", 1000)
	e := Entry{
		Text: "[文件:base64://" + payload + " fileId: url:]正常文字[文件:a.zip fileId:fid url:https://cdn.example.com/a.zip]",
		Media: []Media{
			{Kind: MediaFile, Name: "base64://" + payload},
			{Kind: MediaFile, Name: "a.zip", URL: "https://cdn.example.com/a.zip"},
			{Kind: MediaFile, Name: "b.zip", URL: "https://cdn.example.com/" + payload},
		},
	}
	sanitizeEntry(&e)
	if strings.Contains(e.Text, "base64") {
		t.Fatalf("Text 仍含内联负载: %.80s", e.Text)
	}
	if !strings.Contains(e.Text, "正常文字") || !strings.Contains(e.Text, "[文件:a.zip fileId:fid url:https://cdn.example.com/a.zip]") {
		t.Fatalf("正常内容被误伤: %q", e.Text)
	}
	if e.Media[0].Name != "文件" {
		t.Fatalf("内联负载文件名未清理: %q", e.Media[0].Name)
	}
	if e.Media[1].Name != "a.zip" || e.Media[1].URL == "" {
		t.Fatalf("正常媒体被误伤: %+v", e.Media[1])
	}
	if e.Media[2].URL != "" {
		t.Fatalf("超长地址未清理: %d 字节", len(e.Media[2].URL))
	}
}

// TestSanitizeEntryKeepsNormalText 不含内联负载的日志原样返回（不引入额外分配）。
func TestSanitizeEntryKeepsNormalText(t *testing.T) {
	text := "[图片][文件:a.zip fileId:fid url:https://cdn.example.com/a.zip]"
	e := Entry{Text: text, Media: []Media{{Kind: MediaFile, Name: "a.zip", URL: "https://cdn.example.com/a.zip"}}}
	sanitizeEntry(&e)
	if e.Text != text {
		t.Fatalf("文本被改动: %q", e.Text)
	}
}
