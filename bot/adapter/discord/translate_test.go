package discord

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// newTestAdapter 构造无网络适配器（session 为 nil：附件下载早退，不影响翻译）。
func newTestAdapter() *discordAdapter {
	return NewAdapter(nil)
}

func segTexts(t *testing.T, segs []message.OB11Segment) (texts []string) {
	t.Helper()
	for _, s := range segs {
		if s.Type == message.SegmentText {
			texts = append(texts, s.Data["text"].(string))
		}
	}
	return texts
}

func atQQs(t *testing.T, segs []message.OB11Segment) (qs []string) {
	t.Helper()
	for _, s := range segs {
		if s.Type == message.SegmentMention {
			qs = append(qs, s.Data["qq"].(string))
		}
	}
	return qs
}

func TestSplitContentPlain(t *testing.T) {
	segs := splitContent("你好世界")
	if len(segs) != 1 || segs[0].Type != message.SegmentText || segs[0].Data["text"] != "你好世界" {
		t.Fatalf("segs = %+v", segs)
	}
}

func TestSplitContentMentionInline(t *testing.T) {
	segs := splitContent("hi <@123> 在吗")
	if len(segs) != 3 {
		t.Fatalf("段数 = %d: %+v", len(segs), segs)
	}
	if segs[0].Type != message.SegmentText || segs[0].Data["text"] != "hi " {
		t.Fatalf("首段 = %+v", segs[0])
	}
	if segs[1].Type != message.SegmentMention || segs[1].Data["qq"] != "dc:123" {
		t.Fatalf("at 段 = %+v", segs[1])
	}
	if segs[2].Type != message.SegmentText || segs[2].Data["text"] != " 在吗" {
		t.Fatalf("尾段 = %+v", segs[2])
	}
}

func TestSplitContentLegacyMention(t *testing.T) {
	segs := splitContent("<@!456>")
	qs := atQQs(t, segs)
	if len(qs) != 1 || qs[0] != "dc:456" {
		t.Fatalf("旧形态提及 qs = %v", qs)
	}
}

func TestSplitContentRoleChannelEmoji(t *testing.T) {
	segs := splitContent("<@&11> <#22> <:wave:33> <a:dance:44>")
	texts := segTexts(t, segs)
	if len(texts) != 1 {
		t.Fatalf("应合并为一段文本: %v", texts)
	}
	want := "@role #channel :wave: :dance:"
	if texts[0] != want {
		t.Fatalf("文本 = %q, want %q", texts[0], want)
	}
	if qs := atQQs(t, segs); len(qs) != 0 {
		t.Fatalf("不应产生 at 段: %v", qs)
	}
}

func TestTranslateGuildMessage(t *testing.T) {
	a := newTestAdapter()
	a.selfID = "999"
	m := &discordgo.Message{
		ID:        "1001",
		ChannelID: "2002",
		GuildID:   "3003",
		Content:   "hello <@999>",
		Timestamp: time.Unix(1700000000, 0),
		Author:    &discordgo.User{ID: "444", Username: "u1", GlobalName: "U One"},
		Member:    &discordgo.Member{Nick: "Nick"},
	}
	msg := a.translateMessage(m)
	if msg == nil {
		t.Fatal("翻译结果为空")
	}
	if msg.MessageType != "group" || msg.SubType != "" {
		t.Fatalf("类型 = %q/%q", msg.MessageType, msg.SubType)
	}
	if msg.MessageId != "dc:2002:1001" || msg.GroupId != "dc:2002" || msg.UserId != "dc:444" {
		t.Fatalf("ID = %q %q %q", msg.MessageId, msg.GroupId, msg.UserId)
	}
	if msg.Sender.Nickname != "Nick" {
		t.Fatalf("昵称优先级 Member.Nick 未生效: %q", msg.Sender.Nickname)
	}
	if msg.SelfId != "dc:999" || msg.Platform != Platform {
		t.Fatalf("SelfId/Platform = %q/%q", msg.SelfId, msg.Platform)
	}
	// 自提及应产出 qq==SelfId 的 at 段（core 提及检测依赖）
	qs := atQQs(t, msg.Message)
	if len(qs) != 1 || qs[0] != "dc:999" {
		t.Fatalf("自提及 at 段 = %v", qs)
	}
}

func TestTranslateDM(t *testing.T) {
	a := newTestAdapter()
	m := &discordgo.Message{
		ID:        "1",
		ChannelID: "5",
		Content:   "dm",
		Timestamp: time.Now(),
		Author:    &discordgo.User{ID: "7", Username: "u"},
	}
	msg := a.translateMessage(m)
	if msg == nil || msg.MessageType != "private" || msg.SubType != "friend" {
		t.Fatalf("DM 消息类型错误: %+v", msg)
	}
	// DM 频道 ID 不是群号：私聊不写 GroupId，否则会被当成群聊
	if msg.GroupId != "" {
		t.Fatalf("DM 消息不应带 GroupId, got %q", msg.GroupId)
	}
	if msg.Sender.Nickname != "u" {
		t.Fatalf("昵称应回落 Username: %q", msg.Sender.Nickname)
	}
}

func TestTranslateNicknamePrecedence(t *testing.T) {
	a := newTestAdapter()
	mk := func(u *discordgo.User, member *discordgo.Member) *discordgo.Message {
		return &discordgo.Message{ID: "1", ChannelID: "2", GuildID: "3", Content: "x", Timestamp: time.Now(), Author: u, Member: member}
	}
	if n := a.translateMessage(mk(&discordgo.User{ID: "1", Username: "un", GlobalName: "gn"}, nil)).Sender.Nickname; n != "gn" {
		t.Fatalf("应优先 GlobalName: %q", n)
	}
	if n := a.translateMessage(mk(&discordgo.User{ID: "1", Username: "un"}, &discordgo.Member{})).Sender.Nickname; n != "un" {
		t.Fatalf("应回落 Username: %q", n)
	}
}

func TestTranslateReply(t *testing.T) {
	a := newTestAdapter()
	base := &discordgo.Message{ID: "10", ChannelID: "20", GuildID: "30", Content: "reply", Timestamp: time.Now(), Author: &discordgo.User{ID: "1"}}
	// ReferencedMessage 内联形态
	m1 := *base
	m1.ReferencedMessage = &discordgo.Message{ID: "9", ChannelID: "20"}
	msg := a.translateMessage(&m1)
	if msg.Message[0].Type != message.SegmentReply || msg.Message[0].Data["id"] != "dc:20:9" {
		t.Fatalf("reply 段 = %+v", msg.Message[0])
	}
	// 仅 MessageReference（跨频道引用缺失 ChannelID 时回落本频道）
	m2 := *base
	m2.MessageReference = &discordgo.MessageReference{MessageID: "8"}
	msg = a.translateMessage(&m2)
	if msg.Message[0].Type != message.SegmentReply || msg.Message[0].Data["id"] != "dc:20:8" {
		t.Fatalf("reference reply 段 = %+v", msg.Message[0])
	}
}

func TestTranslateMentionEveryone(t *testing.T) {
	a := newTestAdapter()
	m := &discordgo.Message{ID: "1", ChannelID: "2", GuildID: "3", Content: "@everyone 注意", Timestamp: time.Now(), Author: &discordgo.User{ID: "1"}, MentionEveryone: true}
	msg := a.translateMessage(m)
	qs := atQQs(t, msg.Message)
	if len(qs) != 1 || qs[0] != "all" {
		t.Fatalf("at-all 段 = %v", qs)
	}
}

func TestTranslateAttachments(t *testing.T) {
	a := newTestAdapter()
	m := &discordgo.Message{
		ID: "1", ChannelID: "2", GuildID: "3", Timestamp: time.Now(),
		Author: &discordgo.User{ID: "1"},
		Attachments: []*discordgo.MessageAttachment{
			{URL: "https://cdn/x.png", Filename: "x.png", ContentType: "image/png"},
			{URL: "https://cdn/v.mp4", Filename: "v.mp4", ContentType: "video/mp4"},
			{URL: "https://cdn/a.ogg", Filename: "a.ogg", ContentType: "audio/ogg"},
			{URL: "https://cdn/f.zip", Filename: "f.zip", ContentType: "application/zip"},
		},
	}
	msg := a.translateMessage(m)
	types := []string{}
	for _, s := range msg.Message {
		types = append(types, s.Type)
	}
	want := []string{message.SegmentImage, message.SegmentVideo, message.SegmentRecord, message.SegmentFile}
	if len(types) != len(want) {
		t.Fatalf("段类型 = %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("段类型 = %v, want %v", types, want)
		}
	}
	// 文件段携带文件名
	if msg.Message[3].Data["name"] != "f.zip" {
		t.Fatalf("文件段 name = %v", msg.Message[3].Data["name"])
	}
}

func TestTranslateSticker(t *testing.T) {
	a := newTestAdapter()
	m := &discordgo.Message{
		ID: "1", ChannelID: "2", GuildID: "3", Timestamp: time.Now(),
		Author:       &discordgo.User{ID: "1"},
		StickerItems: []*discordgo.StickerItem{{ID: "s1"}},
	}
	msg := a.translateMessage(m)
	if msg == nil {
		t.Fatal("贴纸消息不应丢弃")
	}
	texts := segTexts(t, msg.Message)
	if len(texts) != 1 || texts[0] != "[贴纸]" {
		t.Fatalf("贴纸文本 = %v", texts)
	}
}

func TestTranslateEmptyDropped(t *testing.T) {
	a := newTestAdapter()
	m := &discordgo.Message{ID: "1", ChannelID: "2", GuildID: "3", Timestamp: time.Now(), Author: &discordgo.User{ID: "1"}}
	if msg := a.translateMessage(m); msg != nil {
		t.Fatalf("空内容消息应丢弃: %+v", msg)
	}
}
