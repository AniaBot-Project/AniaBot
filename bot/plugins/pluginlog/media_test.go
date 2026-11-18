package pluginlog

import (
	"regexp"
	"strings"
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

// TestExtractMediaForwardByID 收消息事件里的转发段通常只带 id（NapCat /
// Luckylilia 都是如此）：节点里的图片要按 id 拉到内容后才能收集，顺序必须与
// FriendlyText 写出的 [图片] 占位一致。
func TestExtractMediaForwardByID(t *testing.T) {
	content := []message.Message{
		{Sender: message.MessageSender{Nickname: "小明"}, Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": "齐齐哈尔"}},
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/1.png"}},
			{Type: message.SegmentImage, Data: map[string]any{
				"url": "https://cdn.example.com/2.png", "file_id": "fid-2",
			}},
		}},
	}
	msg := message.Message{Message: []message.OB11Segment{
		{Type: message.SegmentForward, Data: map[string]any{"id": "qq:fwd"}},
	}}
	// 文本渲染按 id 展开（与收消息路径一致），占位数量应与收集到的图片一致
	text := msg.FriendlyText(false,
		message.WithGetForwardMsgFunc(func(id message.QID) (*[]message.Message, bool) {
			return &content, id == "qq:fwd"
		}),
		message.WithNoSenderPrefix(),
	)
	if !strings.Contains(text, "齐齐哈尔") || strings.Count(text, "[图片]") != 2 {
		t.Fatalf("转发文本渲染错误: %q", text)
	}

	media := extractMediaWithFetch(msg.Message, func(id message.QID) ([]message.Message, bool) {
		if id != "qq:fwd" {
			t.Fatalf("按错误的转发 id 拉取: %q", id)
		}
		return content, true
	})
	if len(media) != 2 {
		t.Fatalf("转发内图片数量 = %d, want 2: %+v", len(media), media)
	}
	for i, want := range []string{"https://cdn.example.com/1.png", "https://cdn.example.com/2.png"} {
		if media[i].Kind != msglog.MediaImage || media[i].URL != want || !media[i].InForward {
			t.Fatalf("第 %d 张转发内图片 = %+v, want %s 且 InForward", i, media[i], want)
		}
	}
	if media[1].FileId != "fid-2" {
		t.Fatalf("转发内图片应保留文件 ID 供续期: %+v", media[1])
	}
}

// TestExtractMediaForwardInlinePrefersContent 有内联内容时不再按 id 拉取：
// 发送路径的转发载荷自带内容，再请求一次既浪费又可能与文本渲染时的内容不一致。
func TestExtractMediaForwardInlinePrefersContent(t *testing.T) {
	segs := []message.OB11Segment{{Type: message.SegmentForward, Data: map[string]any{
		"id": "qq:fwd",
		"content": []message.Message{{Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/inline.png"}},
		}}},
	}}}
	media := extractMediaWithFetch(segs, func(message.QID) ([]message.Message, bool) {
		t.Fatal("有内联内容时不应再按 id 拉取")
		return nil, false
	})
	if len(media) != 1 || !media[0].InForward || media[0].URL != "https://cdn.example.com/inline.png" {
		t.Fatalf("内联内容解析错误: %+v", media)
	}
}

// TestExtractMediaForwardUnavailable 拉不到转发内容（非 QQ 平台、转发被删除或
// 权限不足）时不产生媒体，节点里的「图片」保留为文字占位。
func TestExtractMediaForwardUnavailable(t *testing.T) {
	segs := []message.OB11Segment{{Type: message.SegmentForward, Data: map[string]any{"id": "qq:fwd"}}}
	if media := extractMedia(segs); len(media) != 0 {
		t.Fatalf("没有内联内容且无拉取能力时不应有媒体: %+v", media)
	}
	if media := extractMediaWithFetch(segs, func(message.QID) ([]message.Message, bool) {
		return nil, false
	}); len(media) != 0 {
		t.Fatalf("拉取失败时不应有媒体: %+v", media)
	}
}

// fakeForwardSource 合并转发展开的测试桩，记录按 id 拉取的次数。
type fakeForwardSource struct {
	msgs  map[string][]message.Message
	calls []message.QID
}

func (f *fakeForwardSource) GetForwardMsg(msgId message.QID) (*[]message.Message, bool) {
	f.calls = append(f.calls, msgId)
	msgs, ok := f.msgs[msgId.String()]
	if !ok {
		return nil, false
	}
	return &msgs, true
}

// TestForwardContentCacheReusesFetch 收消息路径上文本展开与媒体提取共用同一次
// 按 id 拉取：一条只带 id 的转发只请求协议端一次，图片顺序与文本占位一致。
func TestForwardContentCacheReusesFetch(t *testing.T) {
	src := &fakeForwardSource{msgs: map[string][]message.Message{
		"qq:fwd": {{Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/a.png"}},
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/b.png"}},
		}}},
	}}
	msg := message.Message{Message: []message.OB11Segment{
		{Type: message.SegmentForward, Data: map[string]any{"id": "qq:fwd"}},
	}}
	cache := newForwardContentCache(src)
	text := msg.FriendlyText(false, message.WithGetForwardMsgFunc(cache.get), message.WithNoSenderPrefix())
	media := extractMediaWithFetch(msg.Message, cache.fetch)

	if len(src.calls) != 1 || src.calls[0] != "qq:fwd" {
		t.Fatalf("同一条转发应只拉取一次, calls=%v", src.calls)
	}
	if got := strings.Count(text, "[图片]"); got != 2 || len(media) != 2 {
		t.Fatalf("文本占位 %d / 媒体 %d，应都是 2\ntext=%q", got, len(media), text)
	}
	for i, want := range []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.png"} {
		if media[i].URL != want || !media[i].InForward {
			t.Fatalf("第 %d 张图片顺序错误: %+v", i, media[i])
		}
	}

	// 没有 QQ 能力时 cache 为 nil：fetch 只返回空，不 panic
	var nilCache *forwardContentCache
	if _, ok := nilCache.fetch("qq:fwd"); ok {
		t.Fatal("nil 缓存不应返回内容")
	}
}

// TestIsHTTPURL 只接受能直接交给浏览器 / 代理访问的 http(s) 地址。
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

// TestExtractMediaInlineFilePayload Bot 用 file 工具发送文件时 file 源是
// base64:// 内联负载：文件名不能取成 payload（否则 MB 级 base64 进日志与面板
// 响应），大小按 base64 长度估算，且解析过程不解码内容。
func TestExtractMediaInlineFilePayload(t *testing.T) {
	// "hello world" 的 base64，长度 16（含一个 = 补位）→ 原始 11 字节
	payload := "aGVsbG8gd29ybGQ="
	segs := []message.OB11Segment{
		{Type: message.SegmentFile, Data: map[string]any{
			"file": "base64://" + payload, "name": "报告.pdf",
		}},
		{Type: message.SegmentFile, Data: map[string]any{
			"file": "base64://" + payload,
		}},
	}
	media := extractMedia(segs)
	if len(media) != 2 {
		t.Fatalf("媒体数量 = %d, want 2: %+v", len(media), media)
	}
	if media[0].Name != "报告.pdf" || media[0].Size != int64(len("hello world")) {
		t.Fatalf("内联文件元信息错误: %+v", media[0])
	}
	// 没有 name 时不能退化成 base64 内容
	if media[1].Name != "文件" || media[1].Size != int64(len("hello world")) {
		t.Fatalf("缺 name 的内联文件解析错误: %+v", media[1])
	}
	for _, m := range media {
		if strings.Contains(m.Name, "base64") || m.URL != "" {
			t.Fatalf("内联负载泄漏进媒体字段: %+v", m)
		}
	}
}
