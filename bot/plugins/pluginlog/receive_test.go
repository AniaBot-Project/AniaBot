package pluginlog

import (
	"context"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeRecvBot 收消息路径的测试桩：嵌入 bot.Bot 满足公共外观，只实现测试用到的
// 消息详情查询；实现 GetForwardMsg 时同时满足 bot.QQ 的合并转发展开能力。
type fakeRecvBot struct {
	bot.Bot
	forward *[]message.Message
	calls   int
}

func (f *fakeRecvBot) GetMsgDetail(msgId message.QID) (*message.Message, bool) {
	return nil, false
}

func (f *fakeRecvBot) GetForwardMsg(msgId message.QID) (*[]message.Message, bool) {
	f.calls++
	if f.forward == nil {
		return nil, false
	}
	return f.forward, true
}

// fakePlainBot 不具备合并转发展开能力的平台外观（只有基础消息查询）。
type fakePlainBot struct {
	bot.Bot
}

func (f *fakePlainBot) GetMsgDetail(msgId message.QID) (*message.Message, bool) {
	return nil, false
}

// TestOnGroupMsgForwardImages 收消息路径的回归测试：NapCat / Luckylilia 事件里的
// 转发段只带 id，文本靠 get_forward_msg 展开，节点里的图片也必须从同一份内容里
// 收集（面板据此把缩略图填回 [图片] 占位），且同一条转发只请求协议端一次。
func TestOnGroupMsgForwardImages(t *testing.T) {
	fwd := []message.Message{{
		Sender: message.MessageSender{Nickname: "O.ㅋ"},
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": "齐齐哈尔"}},
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/a.png"}},
			{Type: message.SegmentImage, Data: map[string]any{"url": "https://cdn.example.com/b.png"}},
		},
	}}
	fb := &fakeRecvBot{forward: &fwd}
	p, rec := newSendLogPlugin()

	msg := message.Message{
		MessageId: "qq:1",
		GroupId:   "qq:2",
		Sender:    message.MessageSender{UserId: "qq:3", Nickname: "群友"},
		Message:   []message.OB11Segment{{Type: message.SegmentForward, Data: map[string]any{"id": "qq:fwd"}}},
	}
	if _, err := p.OnGroupMsg(context.Background(), fb, command.Command{}, msg); err != nil {
		t.Fatalf("收群消息返回错误: %v", err)
	}

	entries := rec.Recent(0)
	if len(entries) != 1 {
		t.Fatalf("应记录 1 条日志，实际 %d", len(entries))
	}
	e := entries[0]
	if !strings.Contains(e.Text, "齐齐哈尔") || strings.Count(e.Text, "[图片]") != 2 {
		t.Fatalf("转发文本渲染错误: %q", e.Text)
	}
	if len(e.Media) != 2 {
		t.Fatalf("转发内图片数量 = %d, want 2: %+v", len(e.Media), e.Media)
	}
	for i, want := range []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.png"} {
		if e.Media[i].Kind != msglog.MediaImage || e.Media[i].URL != want || !e.Media[i].InForward {
			t.Fatalf("第 %d 张转发内图片错误: %+v", i, e.Media[i])
		}
	}
	if fb.calls != 1 {
		t.Fatalf("同一条转发应只请求协议端一次, calls=%d", fb.calls)
	}
}

// TestOnGroupMsgForwardUnsupported 平台没有合并转发展开能力（非 QQ）时不产生
// 媒体，日志照常记录并保留「图片」占位。
func TestOnGroupMsgForwardUnsupported(t *testing.T) {
	// 独立类型：不能带 GetForwardMsg，否则会被判定为具备合并转发展开能力
	fb := &fakePlainBot{}
	p, rec := newSendLogPlugin()

	msg := message.Message{
		MessageId: "qq:1",
		GroupId:   "qq:2",
		Sender:    message.MessageSender{UserId: "qq:3", Nickname: "群友"},
		Message:   []message.OB11Segment{{Type: message.SegmentForward, Data: map[string]any{"id": "qq:fwd"}}},
	}
	if _, err := p.OnGroupMsg(context.Background(), fb, command.Command{}, msg); err != nil {
		t.Fatalf("收群消息返回错误: %v", err)
	}
	e := rec.Recent(0)[0]
	if len(e.Media) != 0 {
		t.Fatalf("不支持展开时不应有媒体: %+v", e.Media)
	}
	if !strings.Contains(e.Text, "[转发消息]") {
		t.Fatalf("不支持展开时应保留转发占位: %q", e.Text)
	}
}
