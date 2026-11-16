package pluginlog

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/core"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// newSendLogPlugin 构造一个挂了内存记录器的日志插件（不经过 Start）。
func newSendLogPlugin() (*LogPlugin, *msglog.Recorder) {
	p := NewPlugin()
	p.SetLogger(slog.Default())
	p.recorder = msglog.New(core.NewAniaMemoryStorage(slog.Default()), 0)
	return p, p.recorder
}

// TestOnAfterSendGroupMsg 群聊发送后事件记录为 Self 消息：内容 / 媒体 / 消息 ID
// 与收到的消息共用同一套日志结构，面板按 Self 标记区分展示。
func TestOnAfterSendGroupMsg(t *testing.T) {
	p, rec := newSendLogPlugin()
	ev := message.SentMessage{
		OutgoingMessage: message.OutgoingMessage{
			Kind:   message.SendKindGroup,
			Target: "qq:123",
			Segments: []message.OB11Segment{
				{Type: message.SegmentText, Data: message.TextMessage{Text: "你好"}.Marshal()},
				{Type: message.SegmentImage, Data: map[string]any{
					"url": "https://cdn.example.com/a.png", "file": "a.png", "file_id": "fid-1",
				}},
			},
		},
		MessageId: "qq:456",
		Success:   true,
	}
	if err := p.OnAfterSendMsg(context.Background(), nil, ev); err != nil {
		t.Fatalf("发送后事件返回错误: %v", err)
	}

	entries := rec.Recent(0)
	if len(entries) != 1 {
		t.Fatalf("应记录 1 条发送日志，实际 %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if !e.Self || e.Failed || e.Type != msglog.TypeGroup || e.GroupId != "qq:123" {
		t.Fatalf("发送日志字段错误: %+v", e)
	}
	if e.Nickname != selfNickname || e.MessageId != "qq:456" {
		t.Fatalf("发送者 / 消息 ID 错误: %+v", e)
	}
	if e.Text != "你好[图片]" {
		t.Fatalf("发送文本渲染错误: %q", e.Text)
	}
	if len(e.Media) != 1 || e.Media[0].Kind != msglog.MediaImage || e.Media[0].FileId != "fid-1" {
		t.Fatalf("发送媒体解析错误: %+v", e.Media)
	}
}

// TestOnAfterSendFriendMsgFailed 发送失败仍记录，并标记 Failed。
func TestOnAfterSendFriendMsgFailed(t *testing.T) {
	p, rec := newSendLogPlugin()
	ev := message.SentMessage{
		OutgoingMessage: message.OutgoingMessage{
			Kind:     message.SendKindFriend,
			Target:   "qq:9",
			Segments: []message.OB11Segment{{Type: message.SegmentText, Data: message.TextMessage{Text: "私聊"}.Marshal()}},
		},
		Success: false,
	}
	if err := p.OnAfterSendMsg(context.Background(), nil, ev); err != nil {
		t.Fatalf("发送后事件返回错误: %v", err)
	}
	e := rec.Recent(0)[0]
	if !e.Self || !e.Failed || e.Type != msglog.TypeFriend || e.UserId != "qq:9" || e.GroupId != "" {
		t.Fatalf("失败发送日志字段错误: %+v", e)
	}
	if e.MessageId != "" {
		t.Fatalf("失败时不应有消息 ID: %+v", e)
	}
}

// TestOnAfterSendForward 合并转发按节点还原为带昵称的文本，节点内的图片标记
// 为 InForward（面板放进转发卡片内展示）。
func TestOnAfterSendForward(t *testing.T) {
	p, rec := newSendLogPlugin()
	fwd := msgchain.Builder().GroupForward()
	fwd.Message("qq:1", "小明", msgchain.Builder().Group().Text("第一句").Build())
	fwd.Message("qq:2", "小红", msgchain.Builder().Group().ImageUrl("https://cdn.example.com/x.png").Build())
	forward := fwd.Build().GetForwardMsg()

	ev := message.SentMessage{
		OutgoingMessage: message.OutgoingMessage{
			Kind:    message.SendKindGroup,
			Target:  "qq:123",
			Forward: &forward,
		},
		MessageId: "qq:789",
		Success:   true,
	}
	if err := p.OnAfterSendMsg(context.Background(), nil, ev); err != nil {
		t.Fatalf("发送后事件返回错误: %v", err)
	}

	e := rec.Recent(0)[0]
	if !strings.Contains(e.Text, "<合并转发消息>") || !strings.Contains(e.Text, "小明") ||
		!strings.Contains(e.Text, "第一句") || !strings.Contains(e.Text, "小红") {
		t.Fatalf("合并转发送文本渲染错误: %q", e.Text)
	}
	if len(e.Media) != 1 || !e.Media[0].InForward || e.Media[0].Kind != msglog.MediaImage {
		t.Fatalf("合并转发内图片解析错误: %+v", e.Media)
	}
}

// TestOnAfterSendEmpty 空内容（如没有任何文本的流式消息）不产生日志。
func TestOnAfterSendEmpty(t *testing.T) {
	p, rec := newSendLogPlugin()
	ev := message.SentMessage{OutgoingMessage: message.OutgoingMessage{
		Kind: message.SendKindGroup, Target: "qq:123",
	}}
	if err := p.OnAfterSendMsg(context.Background(), nil, ev); err != nil {
		t.Fatalf("发送后事件返回错误: %v", err)
	}
	if entries := rec.Recent(0); len(entries) != 0 {
		t.Fatalf("空内容不应记录日志: %+v", entries)
	}
}
