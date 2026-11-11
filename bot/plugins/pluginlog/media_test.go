package pluginlog

import (
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/common/model/message"
)

func TestExtractMedia(t *testing.T) {
	segs := []message.OB11Segment{
		{Type: message.SegmentText, Data: map[string]any{"text": "看这张图"}},
		{Type: message.SegmentImage, Data: map[string]any{
			"url":  "https://cdn.example.com/a.png?x=1",
			"file": "a.png",
		}},
		{Type: message.SegmentImage, Data: map[string]any{
			"url": "data:image/png;base64,AAAA", // 内联负载不落日志
		}},
		{Type: message.SegmentFile, Data: map[string]any{
			"file":      "https://cdn.example.com/report.pdf",
			"name":      "周报.pdf",
			"file_id":   "fid-1",
			"file_size": "2048",
			"url":       "https://cdn.example.com/report.pdf?token=1",
		}},
		{Type: message.SegmentFile, Data: map[string]any{
			"file":      "a.zip",
			"file_id":   "fid-2",
			"file_size": float64(4096),
		}},
		{Type: message.SegmentFile, Data: map[string]any{
			"file": "https://cdn.example.com/photo.jpg",
			"name": "photo.jpg",
		}},
		{Type: message.SegmentRecord, Data: map[string]any{
			"url": "https://cdn.example.com/voice.amr",
		}},
		{Type: message.SegmentVideo, Data: map[string]any{
			"file": "https://cdn.example.com/v.mp4",
		}},
		{Type: message.SegmentMention, Data: map[string]any{"qq": "qq:1"}},
	}
	media := extractMedia(segs)
	if len(media) != 6 {
		t.Fatalf("媒体数量 = %d, want 6: %+v", len(media), media)
	}
	if media[0].Kind != msglog.MediaImage || media[0].URL != "https://cdn.example.com/a.png?x=1" {
		t.Fatalf("图片解析错误: %+v", media[0])
	}
	if media[1].Kind != msglog.MediaFile || media[1].Name != "周报.pdf" || media[1].Size != 2048 ||
		media[1].FileId != "fid-1" || media[1].URL != "https://cdn.example.com/report.pdf?token=1" {
		t.Fatalf("文件解析错误: %+v", media[1])
	}
	if media[2].Kind != msglog.MediaFile || media[2].Name != "a.zip" || media[2].Size != 4096 || media[2].URL != "" {
		t.Fatalf("无链接文件解析错误: %+v", media[2])
	}
	// 图片文件按图片处理（排在文件之前，插在中间）
	if media[3].Kind != msglog.MediaImage || media[3].URL != "https://cdn.example.com/photo.jpg" {
		t.Fatalf("图片文件解析错误: %+v", media[3])
	}
	if media[4].Kind != msglog.MediaRecord || media[4].URL != "https://cdn.example.com/voice.amr" {
		t.Fatalf("语音解析错误: %+v", media[4])
	}
	if media[5].Kind != msglog.MediaVideo || media[5].URL != "https://cdn.example.com/v.mp4" {
		t.Fatalf("视频解析错误: %+v", media[5])
	}
}

func TestExtractMediaCap(t *testing.T) {
	segs := make([]message.OB11Segment, 0, maxMediaPerMessage+5)
	for i := 0; i < maxMediaPerMessage+5; i++ {
		segs = append(segs, message.OB11Segment{Type: message.SegmentImage, Data: map[string]any{
			"url": "https://cdn.example.com/a.png",
		}})
	}
	if got := extractMedia(segs); len(got) != maxMediaPerMessage {
		t.Fatalf("媒体数量 = %d, want %d", len(got), maxMediaPerMessage)
	}
}

func TestIsHTTPURL(t *testing.T) {
	for _, s := range []string{"https://a.com/x.png", "http://a.com/x"} {
		if !isHTTPURL(s) {
			t.Errorf("%q 应识别为 http(s) 地址", s)
		}
	}
	for _, s := range []string{"", "data:image/png;base64,AA", "base64://AA", "file:///tmp/a.png", "ftp://a.com/x", "https://"} {
		if isHTTPURL(s) {
			t.Errorf("%q 不应识别为 http(s) 地址", s)
		}
	}
}
