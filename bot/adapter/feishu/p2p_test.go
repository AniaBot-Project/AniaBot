package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// TestOnReceiveP2PNoGroupID 回归：单聊消息不写 GroupId。GroupId 在框架里是
// 「群聊」标志，填上单聊会话 ID 会让同一会话在面板「消息日志」里被拆成
// 群聊 + 私聊两条，/status、/plan 等指令也会按群聊回复。
func TestOnReceiveP2PNoGroupID(t *testing.T) {
	a := NewAdapter(nil)
	delivered := make(chan message.Message, 1)
	a.SetTrigger(adapter.TriggerWrapper{
		OnFriendMsg: func(m message.Message) { delivered <- m },
	})

	if err := a.onReceive(context.Background(), receiveEvent("om_p2p_nogid")); err != nil {
		t.Fatalf("处理单聊消息失败: %v", err)
	}
	select {
	case m := <-delivered:
		if m.MessageType != "private" || m.SubType != "friend" {
			t.Fatalf("消息类型 = %q/%q, want private/friend", m.MessageType, m.SubType)
		}
		if m.GroupId != "" {
			t.Fatalf("单聊消息不应带 GroupId, got %q", m.GroupId)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("单聊消息应被分发")
	}
}

// TestApiMessageToMessageChatType 消息详情/历史按缓存的 chat_type 区分：单聊不把
// 会话 ID 写进 GroupId（且标注为私聊），群聊照常带群号。
func TestApiMessageToMessageChatType(t *testing.T) {
	a := NewAdapter(nil)
	a.chatTypes.Store("oc_p2p", "p2p")
	a.chatTypes.Store("oc_group", "group")

	p2pMsg := &larkim.Message{
		MessageId: strPtr("om_p2p"),
		ChatId:    strPtr("oc_p2p"),
		MsgType:   strPtr("text"),
		Body:      &larkim.MessageBody{Content: strPtr(`{"text":"hi"}`)},
	}
	got := a.apiMessageToMessage(p2pMsg)
	if got == nil {
		t.Fatal("单聊消息翻译为空")
	}
	if got.MessageType != "private" || got.SubType != "friend" {
		t.Fatalf("单聊类型 = %q/%q, want private/friend", got.MessageType, got.SubType)
	}
	if got.GroupId != "" {
		t.Fatalf("单聊消息不应带 GroupId, got %q", got.GroupId)
	}

	groupMsg := &larkim.Message{
		MessageId: strPtr("om_group"),
		ChatId:    strPtr("oc_group"),
		MsgType:   strPtr("text"),
		Body:      &larkim.MessageBody{Content: strPtr(`{"text":"hi"}`)},
	}
	got = a.apiMessageToMessage(groupMsg)
	if got == nil {
		t.Fatal("群消息翻译为空")
	}
	if got.MessageType != "group" || got.GroupId != "fs:oc_group" {
		t.Fatalf("群消息类型/群号 = %q/%q, want group/fs:oc_group", got.MessageType, got.GroupId)
	}
}
