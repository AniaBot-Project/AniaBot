package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/utils"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// waitDeliver 等待一次异步分发，超时返回 false。
func waitDeliver(t *testing.T, ch <-chan struct{}, timeout time.Duration) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// testAdapter 构造测试用适配器：预置机器人自身信息（自提及判定/自 ID 用）。
func testAdapter() *telegramAdapter {
	a := NewAdapter(nil)
	a.self = &User{ID: 100, Username: "mybot", FirstName: "Bot", LastName: "Ania"}
	return a
}

func textMsg(chatID int64, chatType string, userID int64, text string) *Message {
	return &Message{
		MessageID: 1,
		Date:      1700000000,
		Chat:      Chat{ID: chatID, Type: chatType},
		From:      &User{ID: userID, FirstName: "张三", Username: "zhangsan"},
		Text:      text,
	}
}

func testTrigger(delivered chan<- struct{}) adapter.TriggerWrapper {
	return adapter.TriggerWrapper{
		OnGroupMsg:          func(message.Message) { delivered <- struct{}{} },
		OnFriendMsg:         func(message.Message) { delivered <- struct{}{} },
		OnGroupIncrease:     func(message.GroupIncreaseNotice) { delivered <- struct{}{} },
		OnGroupDecrease:     func(message.GroupDecreaseNotice) { delivered <- struct{}{} },
		OnGroupMsgEmojiLike: func(message.GroupMsgEmojiLikeNotice) { delivered <- struct{}{} },
		OnPlatformEvent:     func(message.PlatformEvent) { delivered <- struct{}{} },
	}
}

// TestUpdateToMessagePrivate 私聊消息：MessageType/SubType/ID 前缀/SelfId。
func TestUpdateToMessagePrivate(t *testing.T) {
	a := testAdapter()
	m := textMsg(111, "private", 222, "hi")
	msg := a.updateToMessage(m)
	if msg == nil {
		t.Fatal("updateToMessage 不应返回 nil")
	}
	if msg.MessageType != "private" || msg.SubType != "friend" {
		t.Fatalf("MessageType/SubType = %s/%s, want private/friend", msg.MessageType, msg.SubType)
	}
	if msg.MessageId.String() != "tg:111:1" {
		t.Fatalf("MessageId = %s, want tg:111:1", msg.MessageId)
	}
	// 私聊不写 GroupId：框架用它判断群聊，填上会让私聊在消息日志里被拆成
	// 群聊 + 私聊，指令也按群聊回复
	if msg.GroupId != "" || msg.UserId.String() != "tg:222" {
		t.Fatalf("GroupId/UserId = %s/%s, want 空/tg:222", msg.GroupId, msg.UserId)
	}
	if msg.SelfId.String() != "tg:100" {
		t.Fatalf("SelfId = %s, want tg:100", msg.SelfId)
	}
	if msg.Sender.Nickname != "张三" {
		t.Fatalf("Sender.Nickname = %q, want 张三", msg.Sender.Nickname)
	}
	if msg.RawMessage != "hi" {
		t.Fatalf("RawMessage = %q, want hi", msg.RawMessage)
	}
	if msg.Platform != "telegram" {
		t.Fatalf("Platform = %q, want telegram", msg.Platform)
	}
}

// TestUpdateToMessageGroupChannel group/supergroup/channel 均映射为群消息。
func TestUpdateToMessageGroupChannel(t *testing.T) {
	a := testAdapter()
	for _, ct := range []string{"group", "supergroup", "channel"} {
		msg := a.updateToMessage(textMsg(-100, ct, 222, "hi"))
		if msg == nil {
			t.Fatalf("chatType=%s: updateToMessage 不应返回 nil", ct)
		}
		if msg.MessageType != "group" {
			t.Fatalf("chatType=%s: MessageType = %s, want group", ct, msg.MessageType)
		}
		if msg.MessageId.String() != "tg:-100:1" {
			t.Fatalf("chatType=%s: MessageId = %s, want tg:-100:1", ct, msg.MessageId)
		}
		if msg.GroupId.String() != "tg:-100" {
			t.Fatalf("chatType=%s: GroupId = %s, want tg:-100", ct, msg.GroupId)
		}
	}
}

// TestSplitEntitiesMentionSelf @bot 自身（大小写不敏感）→ at 段 qq=SelfId。
func TestSplitEntitiesMentionSelf(t *testing.T) {
	a := testAdapter()
	for _, text := range []string{"@MyBot hello", "hi @MYBOT", "@mybot"} {
		off := strings.Index(text, "@")
		if off < 0 {
			t.Fatalf("测试文本异常: %q", text)
		}
		msg := textMsg(-100, "group", 222, text)
		msg.Entities = []MessageEntity{{Type: "mention", Offset: off, Length: len("@MyBot")}}
		segs := a.messageToSegments(msg)
		var atSeg *message.OB11Segment
		for i := range segs {
			if segs[i].Type == message.SegmentMention {
				atSeg = &segs[i]
				break
			}
		}
		if atSeg == nil {
			t.Fatalf("%q: 期望包含 at 段, got %+v", text, segs)
		}
		if qq, _ := atSeg.Data["qq"].(string); qq != "tg:100" {
			t.Fatalf("%q: at 段 qq = %q, want tg:100", text, qq)
		}
	}
}

// TestSplitEntitiesMentionOther 非 bot 的 @username 保留为文本。
func TestSplitEntitiesMentionOther(t *testing.T) {
	a := testAdapter()
	msg := textMsg(-100, "group", 222, "@alice hi")
	msg.Entities = []MessageEntity{{Type: "mention", Offset: 0, Length: 6}}
	segs := a.messageToSegments(msg)
	if len(segs) != 1 || segs[0].Type != message.SegmentText {
		t.Fatalf("期望纯文本段, got %+v", segs)
	}
	if text, _ := segs[0].Data["text"].(string); text != "@alice hi" {
		t.Fatalf("文本 = %q, want @alice hi（原文保留）", text)
	}
}

// TestSplitEntitiesTextMention text_mention（携带 user 对象）→ at 段 qq=tg:<user_id>。
func TestSplitEntitiesTextMention(t *testing.T) {
	a := testAdapter()
	msg := textMsg(-100, "group", 222, "找李四")
	msg.Entities = []MessageEntity{{Type: "text_mention", Offset: 1, Length: 2, User: &User{ID: 999, FirstName: "李四"}}}
	segs := a.messageToSegments(msg)
	if len(segs) != 2 {
		t.Fatalf("期望 [text, at], got %+v", segs)
	}
	if segs[1].Type != message.SegmentMention {
		t.Fatalf("末段应为 at, got %s", segs[1].Type)
	}
	if qq, _ := segs[1].Data["qq"].(string); qq != "tg:999" {
		t.Fatalf("at 段 qq = %q, want tg:999", qq)
	}
	if text, _ := segs[0].Data["text"].(string); text != "找" {
		t.Fatalf("前段文本 = %q, want 找", text)
	}
}

// TestSplitEntitiesUTF16 实体偏移按 UTF-16 code unit 计数：CJK/emoji 不切错位置。
func TestSplitEntitiesUTF16(t *testing.T) {
	a := testAdapter()
	msg := textMsg(-100, "group", 222, "你好@MyBot👋再见")
	// 你好 = 2 单位，@MyBot = 6 单位，👋 = 2 单位
	msg.Entities = []MessageEntity{{Type: "mention", Offset: 2, Length: 6}}
	segs := a.messageToSegments(msg)
	if len(segs) != 3 {
		t.Fatalf("期望 [text, at, text], got %+v", segs)
	}
	if text, _ := segs[0].Data["text"].(string); text != "你好" {
		t.Fatalf("前段 = %q, want 你好", text)
	}
	if segs[1].Type != message.SegmentMention {
		t.Fatalf("中段应为 at, got %s", segs[1].Type)
	}
	if text, _ := segs[2].Data["text"].(string); text != "👋再见" {
		t.Fatalf("后段 = %q, want 👋再见", text)
	}
}

// TestSplitEntitiesAfterCJKEntity 回归：前一个实体含 CJK 字符时（字节数 > UTF-16
// 单位数），其后实体不得被误判重叠而跳过——曾导致「加粗中文 + @bot」的 mention
// 实体丢失，群聊 at 触发失效。
func TestSplitEntitiesAfterCJKEntity(t *testing.T) {
	a := testAdapter()
	msg := textMsg(-100, "group", 222, "你好 @MyBot 在吗")
	// 你好 = 2 单位（6 字节），空格 1，@MyBot 从第 3 单位起共 6 单位
	msg.Entities = []MessageEntity{
		{Type: "bold", Offset: 0, Length: 2},
		{Type: "mention", Offset: 3, Length: 6},
	}
	segs := a.messageToSegments(msg)
	var atSeg *message.OB11Segment
	for i := range segs {
		if segs[i].Type == message.SegmentMention {
			atSeg = &segs[i]
			break
		}
	}
	if atSeg == nil {
		t.Fatalf("CJK bold 实体后的 mention 被丢弃, got %+v", segs)
	}
	if qq, _ := atSeg.Data["qq"].(string); qq != "tg:100" {
		t.Fatalf("at 段 qq = %q, want tg:100", qq)
	}
	// 全文内容应保持完整（text 段按序拼接还原）
	var sb strings.Builder
	for _, s := range segs {
		if s.Type == message.SegmentText {
			sb.WriteString(s.Data["text"].(string))
		} else if s.Type == message.SegmentMention {
			sb.WriteString("@MyBot")
		}
	}
	if sb.String() != "你好 @MyBot 在吗" {
		t.Fatalf("还原文本 = %q, want 你好 @MyBot 在吗", sb.String())
	}
}

// TestSplitEntitiesTextMentionCJK 回归：CJK 昵称 text_mention 后的实体不丢失。
func TestSplitEntitiesTextMentionCJK(t *testing.T) {
	a := testAdapter()
	msg := textMsg(-100, "group", 222, "张三说@MyBot好")
	// 张三 = 2 单位（text_mention），说 1 单位，@MyBot 从第 3 单位起共 6 单位
	msg.Entities = []MessageEntity{
		{Type: "text_mention", Offset: 0, Length: 2, User: &User{ID: 999, FirstName: "张三"}},
		{Type: "mention", Offset: 3, Length: 6},
	}
	segs := a.messageToSegments(msg)
	mentions := 0
	for _, s := range segs {
		if s.Type == message.SegmentMention {
			mentions++
		}
	}
	if mentions != 2 {
		t.Fatalf("期望 2 个 at 段（text_mention + mention），got %d: %+v", mentions, segs)
	}
}

// TestMessageToSegmentsReply reply_to_message → 首段 reply。
func TestMessageToSegmentsReply(t *testing.T) {
	a := testAdapter()
	m := textMsg(-100, "group", 222, "hi")
	m.ReplyToMessage = &Message{MessageID: 42, Chat: Chat{ID: -100, Type: "group"}}
	segs := a.messageToSegments(m)
	if len(segs) == 0 || segs[0].Type != message.SegmentReply {
		t.Fatalf("期望 reply 段在前, got %+v", segs)
	}
	if id, _ := segs[0].Data["id"].(string); id != "tg:-100:42" {
		t.Fatalf("reply id = %q, want tg:-100:42", id)
	}
}

// TestMessageToSegmentsMedia 各媒体段 Data 键断言（client==nil 时图片下载静默失败，
// url 键不补齐，file 键保留 file_id）。
func TestMessageToSegmentsMedia(t *testing.T) {
	a := testAdapter()
	cases := []struct {
		name string
		msg  *Message
		want message.OB11Segment
	}{
		{
			"photo",
			&Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"},
				Photo: []PhotoSize{{FileID: "small", FileSize: 10}, {FileID: "big", FileSize: 100}}},
			message.OB11Segment{Type: message.SegmentImage, Data: map[string]any{"file": "big", "url": "", "summary": ""}},
		},
		{
			"document",
			&Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"},
				Document: &Document{FileID: "doc1", FileName: "a.pdf"}},
			message.OB11Segment{Type: message.SegmentFile, Data: map[string]any{"file": "doc1", "file_id": "doc1", "name": "a.pdf", "url": ""}},
		},
		{
			"voice",
			&Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"},
				Voice: &Voice{FileID: "voice1"}},
			message.OB11Segment{Type: message.SegmentRecord, Data: map[string]any{"file": "voice1"}},
		},
		{
			"audio",
			&Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"},
				Audio: &Audio{FileID: "audio1", FileName: "song.mp3"}},
			message.OB11Segment{Type: message.SegmentRecord, Data: map[string]any{"file": "audio1"}},
		},
		{
			"video",
			&Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"},
				Video: &Video{FileID: "video1"}},
			message.OB11Segment{Type: message.SegmentVideo, Data: map[string]any{"file": "video1", "url": "video1"}},
		},
	}
	for _, c := range cases {
		segs := a.messageToSegments(c.msg)
		if len(segs) != 1 {
			t.Fatalf("%s: 期望 1 段, got %+v", c.name, segs)
		}
		got := segs[0]
		if got.Type != c.want.Type {
			t.Fatalf("%s: Type = %s, want %s", c.name, got.Type, c.want.Type)
		}
		for k, want := range c.want.Data {
			if got.Data[k] != want {
				t.Fatalf("%s: Data[%q] = %#v, want %#v (全量 %#v)", c.name, k, got.Data[k], want, got.Data)
			}
		}
	}
	// 图片取最大尺寸
	if segs := a.messageToSegments(&Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"},
		Photo: []PhotoSize{{FileID: "small", FileSize: 10}, {FileID: "big", FileSize: 100}}}); len(segs) == 1 {
		if file, _ := segs[0].Data["file"].(string); file != "big" {
			t.Fatalf("图片应取最大尺寸, got %q", file)
		}
	} else {
		t.Fatal("photo 应翻译为 1 段")
	}
	// 占位符
	for name, m := range map[string]*Message{
		"sticker":    {MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"}, Sticker: &Sticker{FileID: "s"}},
		"animation":  {MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"}, Animation: &Animation{FileID: "a"}},
		"video_note": {MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"}, VideoNote: &VideoNote{FileID: "v"}},
	} {
		segs := a.messageToSegments(m)
		if len(segs) != 1 || segs[0].Type != message.SegmentText {
			t.Fatalf("%s: 期望文本占位段, got %+v", name, segs)
		}
	}
}

// TestMessageToSegmentsCaption 媒体消息的 caption 作为文本（含实体）。
func TestMessageToSegmentsCaption(t *testing.T) {
	a := testAdapter()
	m := &Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"},
		Caption: "看这张图", Photo: []PhotoSize{{FileID: "p1", FileSize: 100}}}
	segs := a.messageToSegments(m)
	if len(segs) != 2 {
		t.Fatalf("期望 [text, image], got %+v", segs)
	}
	if text, _ := segs[0].Data["text"].(string); text != "看这张图" {
		t.Fatalf("caption 段 = %q", text)
	}
}

// TestMessageToSegmentsEmpty 无文本无媒体的消息返回 nil（调用方丢弃）。
func TestMessageToSegmentsEmpty(t *testing.T) {
	a := testAdapter()
	if segs := a.messageToSegments(&Message{MessageID: 1, Date: 1, Chat: Chat{ID: -100, Type: "group"}}); segs != nil {
		t.Fatalf("空消息应返回 nil, got %+v", segs)
	}
}

// TestHandleUpdateDispatch 更新分发：消息按会话类型走对应回调。
func TestHandleUpdateDispatch(t *testing.T) {
	a := testAdapter()
	delivered := make(chan struct{}, 8)
	a.SetTrigger(testTrigger(delivered))

	a.handleUpdate(&Update{UpdateID: 1, Message: textMsg(111, "private", 222, "hi")})
	a.handleUpdate(&Update{UpdateID: 2, Message: textMsg(-100, "group", 222, "hi")})
	a.handleUpdate(&Update{UpdateID: 3, ChannelPost: textMsg(-100, "channel", 222, "post")})

	for i := range 3 {
		if !waitDeliver(t, delivered, 2*time.Second) {
			t.Fatalf("第 %d 条消息应被分发", i+1)
		}
	}
}

// TestHandleUpdateBotFilter 其他机器人的消息被过滤（防 bot 循环）。
func TestHandleUpdateBotFilter(t *testing.T) {
	a := testAdapter()
	delivered := make(chan struct{}, 2)
	a.SetTrigger(testTrigger(delivered))

	m := textMsg(-100, "group", 222, "hi")
	m.From.IsBot = true
	a.handleUpdate(&Update{UpdateID: 1, Message: m})

	if waitDeliver(t, delivered, 500*time.Millisecond) {
		t.Fatal("bot 消息不应触发回调")
	}
}

// TestHandleUpdateMemberChange 成员加入/离开 → 群成员变动通知（SubType/操作者/平台标识）。
func TestHandleUpdateMemberChange(t *testing.T) {
	a := testAdapter()
	var increases []message.GroupIncreaseNotice
	var decreases []message.GroupDecreaseNotice
	a.SetTrigger(adapter.TriggerWrapper{
		OnGroupIncrease: func(n message.GroupIncreaseNotice) { increases = append(increases, n) },
		OnGroupDecrease: func(n message.GroupDecreaseNotice) { decreases = append(decreases, n) },
	})
	done := make(chan struct{})
	go func() {
		// 加入（邀请者 from=111 邀请 222、333）
		join := textMsg(-100, "group", 111, "")
		join.NewChatMembers = []User{{ID: 222}, {ID: 333}}
		a.handleUpdate(&Update{UpdateID: 1, Message: join})
		// 主动退群（from==left）
		leave := textMsg(-100, "group", 222, "")
		leave.LeftChatMember = &User{ID: 222}
		a.handleUpdate(&Update{UpdateID: 2, Message: leave})
		// 被踢（from==111 踢 333）
		kick := textMsg(-100, "group", 111, "")
		kick.LeftChatMember = &User{ID: 333}
		a.handleUpdate(&Update{UpdateID: 3, Message: kick})
		close(done)
	}()
	<-done

	if len(increases) != 2 || len(decreases) != 2 {
		t.Fatalf("通知数量 = inc:%d dec:%d, want 2/2", len(increases), len(decreases))
	}
	if increases[0].SubType != "invite" || increases[0].OperatorId.String() != "tg:111" ||
		increases[0].UserId.String() != "tg:222" || increases[0].GroupId.String() != "tg:-100" {
		t.Fatalf("increase[0] = %+v, want invite/tg:111/tg:222/tg:-100", increases[0])
	}
	if increases[0].Platform != "telegram" || increases[0].NoticeType != "group_increase" {
		t.Fatalf("increase[0] 平台/类型 = %s/%s", increases[0].Platform, increases[0].NoticeType)
	}
	if decreases[0].SubType != "leave" || decreases[0].UserId.String() != "tg:222" {
		t.Fatalf("decrease[0] = %+v, want leave/tg:222（主动退群）", decreases[0])
	}
	if decreases[1].SubType != "kick" || decreases[1].OperatorId.String() != "tg:111" {
		t.Fatalf("decrease[1] = %+v, want kick/tg:111（被踢）", decreases[1])
	}
}

// TestHandleUpdateMyChatMember bot 被加入/移除 → 平台特定事件。
func TestHandleUpdateMyChatMember(t *testing.T) {
	a := testAdapter()
	delivered := make(chan struct{}, 4)
	a.SetTrigger(testTrigger(delivered))

	add := &Update{UpdateID: 1, MyChatMember: &ChatMemberUpdated{
		Chat: Chat{ID: -100, Type: "group"},
		From: User{ID: 111},
		NewChatMember: struct {
			Status string `json:"status"`
		}{Status: "member"},
	}}
	a.handleUpdate(add)
	if !waitDeliver(t, delivered, 2*time.Second) {
		t.Fatal("bot_added 应分发")
	}

	remove := &Update{UpdateID: 2, MyChatMember: &ChatMemberUpdated{
		Chat: Chat{ID: -100, Type: "group"},
		NewChatMember: struct {
			Status string `json:"status"`
		}{Status: "kicked"},
	}}
	a.handleUpdate(remove)
	if !waitDeliver(t, delivered, 2*time.Second) {
		t.Fatal("bot_removed 应分发")
	}
}

// TestHandleUpdateReaction 表情回应 → 群表情回应通知（emoji 与 custom_emoji 两种）。
func TestHandleUpdateReaction(t *testing.T) {
	a := testAdapter()
	var got *message.GroupMsgEmojiLikeNotice
	a.SetTrigger(adapter.TriggerWrapper{
		OnGroupMsgEmojiLike: func(n message.GroupMsgEmojiLikeNotice) { got = &n },
	})
	ev := &Update{UpdateID: 1, MessageReaction: &MessageReactionUpdated{
		Chat:      Chat{ID: -100, Type: "supergroup"},
		MessageID: 42,
		User:      &User{ID: 222},
		NewReaction: []Reaction{
			{Type: "emoji", Emoji: "👍"},
			{Type: "custom_emoji", CustomEmojiID: "ce_1"},
		},
	}}
	done := make(chan struct{})
	go func() { a.handleUpdate(ev); close(done) }()
	<-done
	if got == nil {
		t.Fatal("表情回应通知未分发")
	}
	if got.MessageId.String() != "tg:-100:42" {
		t.Fatalf("MessageId = %s, want tg:-100:42", got.MessageId)
	}
	if len(got.Likes) != 2 || got.Likes[0].EmojiId != "👍" || got.Likes[1].EmojiId != "ce_1" {
		t.Fatalf("Likes = %+v, want [👍 ce_1]", got.Likes)
	}
	if got.Platform != "telegram" || got.NoticeType != "group_msg_emoji_like" {
		t.Fatalf("通知平台/类型 = %s/%s", got.Platform, got.NoticeType)
	}
}

// TestGroupMentionEndToEnd 端到端：Telegram 原始消息（supergroup + mention 实体）
// → 适配器翻译 → utils.ParseCommand 应识别为 Mention（@bot 触发 aichat 的前提）。
// 防止 at 段 qq 与 msg.SelfId 格式不一致导致群内艾特静默失效。
func TestGroupMentionEndToEnd(t *testing.T) {
	a := testAdapter()
	cmds := make(chan command.Command, 2)
	a.SetTrigger(adapter.TriggerWrapper{
		OnGroupMsg: func(msg message.Message) { cmds <- utils.ParseCommand(msg) },
	})
	for _, tc := range []struct {
		text     string
		entity   MessageEntity
		entities []MessageEntity
	}{
		{text: "@MyBot 你好", entity: MessageEntity{Type: "mention", Offset: 0, Length: len("@MyBot")}},
		{text: "@mybot", entity: MessageEntity{Type: "mention", Offset: 0, Length: len("@mybot")}},
		// text_mention：Telegram 在用户从群成员列表选择 bot 展示名时生成
		{text: "你好", entities: []MessageEntity{{Type: "text_mention", Offset: 0, Length: 0, User: &User{ID: 100, FirstName: "Bot"}}}},
	} {
		m := textMsg(-100, "supergroup", 222, tc.text)
		m.MessageID = 42
		if tc.entity.Type != "" {
			m.Entities = []MessageEntity{tc.entity}
		} else {
			m.Entities = tc.entities
		}
		a.handleUpdate(&Update{UpdateID: 1, Message: m})
		select {
		case cmd := <-cmds:
			if !cmd.Mention {
				t.Fatalf("%q: 群内艾特应识别为 Mention, cmd=%+v", tc.text, cmd)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: 群消息未分发到回调", tc.text)
		}
	}
}

// TestMessageKeyNoticeKey core 层去重键：消息按 tg:<chat>:<msgid> 全量键，
// 通知无稳定键返回 false。
func TestMessageKeyNoticeKey(t *testing.T) {
	a := NewAdapter(nil)
	if key, ok := a.MessageKey(message.Message{MessageId: message.QID("tg:-100:42")}); !ok || key != "msg:-100:42" {
		t.Fatalf("MessageKey = (%q,%v), want (msg:-100:42,true)", key, ok)
	}
	if _, ok := a.MessageKey(message.Message{MessageId: ""}); ok {
		t.Fatal("空 MessageId 应返回 false")
	}
	if _, ok := a.NoticeKey("group_increase", message.GroupIncreaseNotice{}); ok {
		t.Fatal("NoticeKey 应恒返回 false（靠适配器级 update_id 去重）")
	}
}

// TestSegmentsPlainText / appendTextSeg 文本段合并与纯文本提取。
func TestSegmentsPlainText(t *testing.T) {
	segs := []message.OB11Segment{
		{Type: message.SegmentText, Data: map[string]any{"text": "a"}},
		{Type: message.SegmentMention, Data: map[string]any{"qq": "tg:100"}},
		{Type: message.SegmentText, Data: map[string]any{"text": "b"}},
	}
	if got := segmentsPlainText(segs); got != "ab" {
		t.Fatalf("segmentsPlainText = %q, want ab（at 段不计入）", got)
	}
	merged := appendTextSeg(nil, "a")
	merged = appendTextSeg(merged, "b")
	if len(merged) != 1 {
		t.Fatalf("相邻文本应合并, got %d 段", len(merged))
	}
	if text, _ := merged[0].Data["text"].(string); text != "ab" {
		t.Fatalf("合并文本 = %q, want ab", text)
	}
}

// TestParseMsgID / msgID / parseChatID ID 编解码。
func TestParseMsgID(t *testing.T) {
	if c, m, ok := parseMsgID("tg:-100:42"); !ok || c != -100 || m != 42 {
		t.Fatalf("parseMsgID(tg:-100:42) = (%d,%d,%v)", c, m, ok)
	}
	if _, _, ok := parseMsgID("qq:12345"); ok {
		t.Fatal("非 tg: 前缀应返回 false")
	}
	if _, _, ok := parseMsgID("tg:-100"); ok {
		t.Fatal("缺消息 ID 应返回 false")
	}
	if _, _, ok := parseMsgID("tg:abc:42"); ok {
		t.Fatal("非法 chat_id 应返回 false")
	}
	if c, ok := parseChatID(message.QID("tg:-100")); !ok || c != -100 {
		t.Fatalf("parseChatID(tg:-100) = (%d,%v)", c, ok)
	}
	if _, ok := parseChatID(message.QID("100")); ok {
		t.Fatal("无前缀 ID 应返回 false")
	}
}
