package pluginlog

import (
	"regexp"
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

// TestExtractMediaForward 合并转发记录内部的图片追加在顶层媒体之后并标记
// InForward，顺序与 FriendlyText 写出 [图片] 占位的顺序一致（含多层嵌套），
// 面板据此把图片放回对应转发节点；内部的文件 / 语音不收集。
func TestExtractMediaForward(t *testing.T) {
	deep := message.Message{
		Sender: message.MessageSender{UserId: message.FromUint64(303)},
		Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/deep.png"}},
		},
	}
	middle := message.Message{
		Sender: message.MessageSender{UserId: message.FromUint64(202)},
		Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: map[string]any{
				"url": "https://cdn.example.com/mid-1.png", "file_id": "fid-mid-1",
			}},
			{Type: message.SegmentForward, Data: map[string]any{
				"id": "inner", "content": []message.Message{deep},
			}},
			{Type: message.SegmentFile, Data: map[string]any{
				"file": "https://cdn.example.com/inner.pdf", "name": "inner.pdf",
			}},
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/mid-2.png"}},
		},
	}
	segs := []message.OB11Segment{
		{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/top.png"}},
		{Type: message.SegmentForward, Data: map[string]any{
			"id": "outer", "content": []message.Message{middle},
		}},
	}
	media := extractMedia(segs)
	if len(media) != 4 {
		t.Fatalf("媒体数量 = %d, want 4: %+v", len(media), media)
	}
	if media[0].Kind != msglog.MediaImage || media[0].URL != "https://cdn.example.com/top.png" || media[0].InForward {
		t.Fatalf("顶层图片解析错误: %+v", media[0])
	}
	wantURLs := []string{
		"https://cdn.example.com/mid-1.png",
		"https://cdn.example.com/deep.png",
		"https://cdn.example.com/mid-2.png",
	}
	for i, want := range wantURLs {
		got := media[i+1]
		if got.Kind != msglog.MediaImage || got.URL != want || !got.InForward {
			t.Fatalf("第 %d 张转发内图片 = %+v, want url %s 且 InForward", i+1, got, want)
		}
	}
	if media[1].FileId != "fid-mid-1" {
		t.Fatalf("转发内图片应保留文件 ID 供续期: %+v", media[1])
	}
}

// TestExtractMediaForwardCap 转发内的图片单独计数，不挤占顶层图片额度。
func TestExtractMediaForwardCap(t *testing.T) {
	nested := make([]message.Message, 0, maxForwardMediaPerMessage+5)
	for i := 0; i < maxForwardMediaPerMessage+5; i++ {
		nested = append(nested, message.Message{
			Message: []message.OB11Segment{{Type: message.SegmentImage, Data: map[string]any{
				"url": "https://cdn.example.com/f.png",
			}}},
		})
	}
	segs := []message.OB11Segment{
		{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/top.png"}},
		{Type: message.SegmentForward, Data: map[string]any{"id": "fwd", "content": nested}},
	}
	media := extractMedia(segs)
	if len(media) != 1+maxForwardMediaPerMessage {
		t.Fatalf("媒体数量 = %d, want %d", len(media), 1+maxForwardMediaPerMessage)
	}
	if media[0].InForward || !media[1].InForward {
		t.Fatalf("顶层与转发内媒体标记错误: %+v", media[:2])
	}
}

// TestForwardMediaOrderMatchesText 转发内图片的收集顺序必须与 FriendlyText 写出
// [图片] 占位的顺序一致：面板是按顺序把图片填回转发节点的，顺序错了会张冠李戴。
func TestForwardMediaOrderMatchesText(t *testing.T) {
	deep := message.Message{
		Sender: message.MessageSender{UserId: message.FromUint64(303)},
		Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/deep.png"}},
		},
	}
	middle := message.Message{
		Sender: message.MessageSender{UserId: message.FromUint64(202)},
		Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/mid-1.png"}},
			{Type: message.SegmentText, Data: map[string]any{"text": "夹在中间的转发"}},
			{Type: message.SegmentForward, Data: map[string]any{"id": "inner", "content": []message.Message{deep}}},
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/mid-2.png"}},
		},
	}
	msg := message.Message{
		Message: []message.OB11Segment{{Type: message.SegmentForward, Data: map[string]any{
			"id": "outer", "content": []message.Message{middle},
		}}},
	}
	text := msg.FriendlyText(true)
	re := regexp.MustCompile(`\[图片 [^\]]*?url:([^\]]+)\]`)
	var textURLs []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		textURLs = append(textURLs, m[1])
	}
	var mediaURLs []string
	for _, m := range extractMedia(msg.Message) {
		if !m.InForward {
			t.Fatalf("这条消息没有顶层图片，媒体都应来自转发: %+v", m)
		}
		mediaURLs = append(mediaURLs, m.URL)
	}
	if len(textURLs) != 3 || len(mediaURLs) != 3 {
		t.Fatalf("文本图片 = %d, 媒体 = %d, want 3/3\ntext=%q", len(textURLs), len(mediaURLs), text)
	}
	for i := range textURLs {
		if textURLs[i] != mediaURLs[i] {
			t.Fatalf("第 %d 张图片顺序不一致：文本 %q, 媒体 %q", i, textURLs[i], mediaURLs[i])
		}
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
