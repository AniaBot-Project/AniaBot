package message

import (
	"strings"
	"testing"
)

// TestFriendlyTextNicknameFallback 昵称不可得（飞书通讯录查询失败/权限缺失）时兜底显示「用户」，
// 避免出现 [nickname: id:fs:ou_xxx]: 的空昵称前缀。
func TestFriendlyTextNicknameFallback(t *testing.T) {
	msg := Message{
		Message: []OB11Segment{{Type: SegmentText, Data: map[string]any{"text": "你好"}}},
		Sender:  MessageSender{UserId: FromUint64(123456)},
	}
	text := msg.FriendlyText(true)
	want := "[nickname:用户 id:qq:123456]: 你好"
	if !strings.HasPrefix(text, want) {
		t.Fatalf("空昵称应兜底为「用户」, got %q", text)
	}

	// 有昵称时保持原样
	msg.Sender.Nickname = "小明"
	text = msg.FriendlyText(true)
	if !strings.HasPrefix(text, "[nickname:小明 id:qq:123456]: ") {
		t.Fatalf("有昵称时应显示昵称, got %q", text)
	}

	// 群名片优先于昵称
	msg.Sender.Card = "群名片"
	text = msg.FriendlyText(true)
	if !strings.HasPrefix(text, "[nickname:群名片 id:qq:123456]: ") {
		t.Fatalf("群名片应优先, got %q", text)
	}
}

// TestFriendlyTextSelfMention 艾特机器人自己时用 [at我] 占位（不暴露机器人自身 ID），
// 使 AI 明确知道本条消息 @ 了自己；其他 @ 目标仍显示 [at:id:…]，发送者自 @ 跳过。
func TestFriendlyTextSelfMention(t *testing.T) {
	msg := Message{
		SelfId: FromUint64(999),
		Sender: MessageSender{UserId: FromUint64(123), Nickname: "小明"},
		Message: []OB11Segment{
			{Type: SegmentMention, Data: map[string]any{"qq": "999"}},
			{Type: SegmentText, Data: map[string]any{"text": "你好"}},
			{Type: SegmentMention, Data: map[string]any{"qq": "888"}},
			{Type: SegmentMention, Data: map[string]any{"qq": "123"}},
			{Type: SegmentMention, Data: map[string]any{"qq": "all"}},
		},
	}
	text := msg.FriendlyText(true)
	want := "[nickname:小明 id:qq:123]: [at我]你好[at:id:qq:888][at:全体成员]"
	if text != want {
		t.Fatalf("FriendlyText = %q, want %q", text, want)
	}
}

// TestFriendlyTextNestedForward 合并转发内再嵌套合并转发时应逐层透传回调并递归展开，
// 内层转发不再退化为 [转发消息] 占位（GitHub issue #11）。
func TestFriendlyTextNestedForward(t *testing.T) {
	innerTextMsg := Message{
		Sender: MessageSender{UserId: FromUint64(303), Nickname: "内层发送者"},
		Message: []OB11Segment{
			{Type: SegmentText, Data: map[string]any{"text": "内层实际内容"}},
			{Type: SegmentImage, Data: ImageMessage{Url: "https://example.com/inner.png"}.Marshal()},
		},
	}
	outerTextMsg := Message{
		Sender: MessageSender{UserId: FromUint64(202), Nickname: "外层发送者"},
		Message: []OB11Segment{
			{Type: SegmentForward, Data: map[string]any{"id": "inner_fwd_1"}},
		},
	}
	root := Message{
		Sender: MessageSender{UserId: FromUint64(101), Nickname: "根发送者"},
		Message: []OB11Segment{
			{Type: SegmentForward, Data: map[string]any{"id": "outer_fwd_1"}},
		},
	}
	getForward := func(id QID) (*[]Message, bool) {
		switch id {
		case QID("outer_fwd_1"):
			return &[]Message{outerTextMsg}, true
		case QID("inner_fwd_1"):
			return &[]Message{innerTextMsg}, true
		}
		return nil, false
	}
	text := root.FriendlyText(true,
		WithGetForwardMsgFunc(getForward),
		WithGetImageOCRFunc(func(url string) string { return "图片OCR识别内容" }))
	for _, want := range []string{"<合并转发消息>", "外层发送者", "内层实际内容", "图片OCR识别内容"} {
		if !strings.Contains(text, want) {
			t.Fatalf("嵌套转发应递归展开, 缺少 %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "[转发消息]") {
		t.Fatalf("内层转发不应退化为占位符, got %q", text)
	}
}

// TestFriendlyTextForwardNodesOnOwnLines 合并转发的每个节点应独占一行并缩进一级：
// 上一条以图片标记等无换行内容结尾时，下一条的发送者前缀不能挤到同一行。
func TestFriendlyTextForwardNodesOnOwnLines(t *testing.T) {
	makeNode := func(id uint64, nickname, text string) Message {
		return Message{
			Sender: MessageSender{UserId: FromUint64(id), Nickname: nickname},
			Message: []OB11Segment{
				{Type: SegmentText, Data: map[string]any{"text": text}},
				{Type: SegmentImage, Data: ImageMessage{Url: "https://example.com/pic.png"}.Marshal()},
			},
		}
	}
	root := Message{
		Sender: MessageSender{UserId: FromUint64(101), Nickname: "外层"},
		Message: []OB11Segment{{
			Type: SegmentForward,
			Data: map[string]any{"id": "fwd_1", "content": []Message{
				makeNode(201, "甲", "第一条"),
				makeNode(202, "乙", "第二条"),
			}},
		}},
	}
	text := root.FriendlyText(true)
	for _, want := range []string{"\n  [nickname:甲 id:qq:201]: 第一条", "\n  [nickname:乙 id:qq:202]: 第二条"} {
		if !strings.Contains(text, want) {
			t.Fatalf("每个转发节点应独占一行并缩进, 缺少 %q, got %q", want, text)
		}
	}
	if !strings.Contains(text, "\n</合并转发消息>") {
		t.Fatalf("结束标签应独占一行, got %q", text)
	}
}

// TestFriendlyTextReplyIndent 引用内容应缩进一级，且引用内部的合并转发、
// 图片识别结果随嵌套深度逐层累加缩进（引用的合并转发节点 = 两层）。
func TestFriendlyTextReplyIndent(t *testing.T) {
	forwarded := Message{
		Sender: MessageSender{UserId: FromUint64(201), Nickname: "转发的"},
		Message: []OB11Segment{
			{Type: SegmentText, Data: map[string]any{"text": "转发内容"}},
		},
	}
	quoted := Message{
		Sender: MessageSender{UserId: FromUint64(202), Nickname: "被引用的"},
		Message: []OB11Segment{
			{Type: SegmentText, Data: map[string]any{"text": "引用正文"}},
			{Type: SegmentImage, Data: ImageMessage{Url: "https://example.com/q.png"}.Marshal()},
			{Type: SegmentForward, Data: map[string]any{"id": "fwd_1", "content": []Message{forwarded}}},
		},
	}
	root := Message{
		Sender: MessageSender{UserId: FromUint64(101), Nickname: "发送者"},
		Message: []OB11Segment{
			{Type: SegmentReply, Data: map[string]any{"id": "m_1"}},
			{Type: SegmentText, Data: map[string]any{"text": "正文"}},
		},
	}
	text := root.FriendlyText(true,
		WithGetMsgFunc(func(id QID) (*Message, bool) { return &quoted, true }),
		WithGetImageOCRFunc(func(url string) string { return "图片识别内容" }))

	for _, want := range []string{
		"<reply>\n",
		"\n  [nickname:被引用的 id:qq:202]: 引用正文",
		"\n  <图片消息 ",                           // 引用内的图片消息块整体缩进一级
		"\n    图片识别内容",                         // 识别结果正文比块标签再深一级
		"\n  </图片消息 ",                          // 结束标签同层
		"\n  <合并转发消息>",                         // 引用内的合并转发块整体缩进一级
		"\n    [nickname:转发的 id:qq:201]: 转发内容", // 转发节点 = 引用(1) + 转发(1) = 两层
		"\n  </合并转发消息>",
		"\n</reply>\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("嵌套块缩进不正确, 缺少 %q, got %q", want, text)
		}
	}
	// 顶层正文不应被缩进
	if !strings.Contains(text, "[nickname:发送者 id:qq:101]: <reply>") {
		t.Fatalf("顶层内容不应缩进, got %q", text)
	}
}

// TestFriendlyTextForwardInlineContent 合并转发段内联携带 content（NapCat 展开
// 转发内容时的格式，嵌套层 id 仅供查看、无法再拉取）时应直接展开内联内容，
// 不再尝试按 id 拉取而显示「无法获取详情」（GitHub issue #11）。
func TestFriendlyTextForwardInlineContent(t *testing.T) {
	inner := Message{
		Sender: MessageSender{UserId: FromUint64(303), Nickname: "最内层"},
		Message: []OB11Segment{
			{Type: SegmentText, Data: map[string]any{"text": "内层实际内容"}},
			{Type: SegmentImage, Data: ImageMessage{Url: "https://example.com/nested.png"}.Marshal()},
		},
	}
	middle := Message{
		Sender: MessageSender{UserId: FromUint64(202), Nickname: "中间层"},
		Message: []OB11Segment{{
			Type: SegmentForward,
			Data: map[string]any{"id": "view-only-id", "content": []Message{inner}},
		}},
	}
	root := Message{
		Sender: MessageSender{UserId: FromUint64(101), Nickname: "外层"},
		Message: []OB11Segment{{
			Type: SegmentForward,
			Data: map[string]any{"id": "outer-view-id", "content": []Message{middle}},
		}},
	}
	// 内层 id 无法拉取时也应正常展开内联内容（NapCat 嵌套转发的真实形态）
	text := root.FriendlyText(true,
		WithGetForwardMsgFunc(func(QID) (*[]Message, bool) { return nil, false }),
		WithGetImageOCRFunc(func(url string) string { return "图片OCR识别内容" }))
	for _, want := range []string{"外层", "中间层", "内层实际内容", "图片OCR识别内容"} {
		if !strings.Contains(text, want) {
			t.Fatalf("内联 content 应被展开, 缺少 %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "[转发消息") {
		t.Fatalf("内联 content 存在时不应出现转发占位, got %q", text)
	}
}

// TestFriendlyTextStrippedSegmentPlaceholder 剔除内联负载后的图片/录音/视频段
// （无 url/file 键）仍保留占位符，使历史文本体现「此处有媒体」而非整段隐形。
func TestFriendlyTextStrippedSegmentPlaceholder(t *testing.T) {
	m := Message{
		Message: []OB11Segment{
			{Type: SegmentImage, Data: map[string]any{"summary": "[图片]"}},
			{Type: SegmentRecord, Data: map[string]any{}},
			{Type: SegmentVideo, Data: map[string]any{}},
			{Type: SegmentText, Data: TextMessage{Text: "after"}.Marshal()},
		},
	}
	got := m.FriendlyText(true)
	for _, want := range []string{"[图片]", "[录音]", "[视频]", "after"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FriendlyText 缺少 %s: %q", want, got)
		}
	}
}
