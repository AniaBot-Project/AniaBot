package message

import "testing"

// TestMessageIsGroup 群聊判定以 message_type 为准：私聊即使带 GroupId（微信、
// 飞书、Telegram、Discord 的私聊会话都会填）也不能判成群聊；message_type 缺失
// 的自定义适配器退回按 GroupId 推断。
func TestMessageIsGroup(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want bool
	}{
		{"群聊", Message{MessageType: "group", GroupId: "qq:1"}, true},
		{"私聊", Message{MessageType: "private", GroupId: "qq:2"}, false},
		{"私聊带会话 ID（微信私聊形态）", Message{MessageType: "private", GroupId: "wx:u@im.wechat"}, false},
		{"message_type 缺失但带群号", Message{GroupId: "qq:3"}, true},
		{"message_type 缺失且无群号", Message{}, false},
	}
	for _, c := range cases {
		if got := c.msg.IsGroup(); got != c.want {
			t.Errorf("%s: IsGroup() = %v, want %v", c.name, got, c.want)
		}
	}
}
