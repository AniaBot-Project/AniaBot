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
			"url":     "https://cdn.example.com/a.png?x=1",
			"file":    "a.png",
			"file_id": "fid-img",
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
	if media[0].Kind != msglog.MediaImage || media[0].URL != "https://cdn.example.com/a.png?x=1" ||
		media[0].FileId != "fid-img" {
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

// TestSegmentKindOf 媒体续期接口按消息段类型区分平台接口，映射必须与提取时一致。
func TestSegmentKindOf(t *testing.T) {
	cases := map[string]string{
		msglog.MediaImage:  message.SegmentImage,
		msglog.MediaRecord: message.SegmentRecord,
		msglog.MediaVideo:  message.SegmentVideo,
		msglog.MediaFile:   message.SegmentFile,
		"":                 "",
	}
	for in, want := range cases {
		if got := segmentKindOf(in); got != want {
			t.Errorf("segmentKindOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMatchMediaURL 续期后重新拉到的消息里，媒体顺序 / 数量可能变化：
// 有文件 ID 时按「类型 + 文件 ID」精确匹配，没有时才退回位置匹配。
func TestMatchMediaURL(t *testing.T) {
	old := []msglog.Media{
		{Kind: msglog.MediaImage, URL: "https://old/1", FileId: "fid-1"},
		{Kind: msglog.MediaFile, URL: "https://old/2", FileId: "fid-2"},
	}
	fresh := []msglog.Media{
		{Kind: msglog.MediaFile, URL: "https://new/2", FileId: "fid-2"},
		{Kind: msglog.MediaImage, URL: "https://new/1", FileId: "fid-1"},
	}
	if got := matchMediaURL(fresh, old, 0); got != "https://new/1" {
		t.Fatalf("图片匹配错误: %q", got)
	}
	if got := matchMediaURL(fresh, old, 1); got != "https://new/2" {
		t.Fatalf("文件匹配错误: %q", got)
	}
	// 文件 ID 对不上时不要按位置猜（顺序可能已变化）
	if got := matchMediaURL([]msglog.Media{{Kind: msglog.MediaImage, URL: "https://new/x", FileId: "fid-x"}}, old, 0); got != "" {
		t.Fatalf("文件 ID 不匹配时应放弃, got %q", got)
	}
	// 没有文件 ID 的老日志：数量一致才按位置匹配
	noID := []msglog.Media{{Kind: msglog.MediaImage, URL: "https://old/1"}}
	if got := matchMediaURL([]msglog.Media{{Kind: msglog.MediaImage, URL: "https://new/1"}}, noID, 0); got != "https://new/1" {
		t.Fatalf("位置匹配错误: %q", got)
	}
	if got := matchMediaURL([]msglog.Media{{Kind: msglog.MediaImage, URL: "https://new/1"}, {Kind: msglog.MediaImage, URL: "https://new/2"}}, noID, 0); got != "" {
		t.Fatalf("数量变化时应放弃位置匹配, got %q", got)
	}
	if got := matchMediaURL(fresh, old, 5); got != "" {
		t.Fatalf("越界下标应返回空, got %q", got)
	}
}
