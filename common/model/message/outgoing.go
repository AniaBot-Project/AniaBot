package message

// SendKind 一次发送的目标类型。
type SendKind string

const (
	// SendKindGroup 群聊：Target 为群 ID。
	SendKindGroup SendKind = "group"
	// SendKindFriend 私聊：Target 为用户 ID。
	SendKindFriend SendKind = "friend"
)

// OutgoingMessage 一次即将提交给平台的消息，是发送前事件
// （plugin.SendMsgInterceptor.OnBeforeSendMsg）的载荷。
//
// Segments 与 Forward 二选一：普通消息使用 Segments；合并转发使用 Forward
// （此时 Segments 为空）。插件可以直接修改这两个字段来改写将要发出的内容，
// 或让 OnBeforeSendMsg 返回 false 取消本次发送。
type OutgoingMessage struct {
	Kind     SendKind               // 发送目标类型
	Target   QID                    // 目标群 ID（群聊）或用户 ID（私聊）
	Segments []OB11Segment          // 普通消息段（合并转发时为空）
	Forward  *ForwardMessageSegment // 合并转发内容（非 nil 表示本条是合并转发）
}

// SentMessage 一次已经提交给平台的消息，是发送后事件
// （plugin.SendMsgObserver.OnAfterSendMsg）的载荷。
//
// 内嵌的 OutgoingMessage 是实际发出的最终内容：若发送前事件改写过消息，
// 这里看到的是改写后的结果。MessageId 为平台返回的消息 ID（失败 / 流式
// 回复等拿不到时为空），Success 表示平台是否确认发送成功。
type SentMessage struct {
	OutgoingMessage
	MessageId QID
	Success   bool
}

// ToMessages 把合并转发内容的节点还原成普通消息列表。
//
// 发送合并转发时只有节点（发送者 + 内容），没有完整的 Message；这里按节点
// 信息补出 Message，便于复用 FriendlyText 渲染 / 媒体提取（如消息日志记录
// Bot 发出的合并转发）。未带内容的空节点会被跳过。
func (f ForwardMessageSegment) ToMessages() []Message {
	if len(f.Messages) == 0 {
		return nil
	}
	out := make([]Message, 0, len(f.Messages))
	for _, node := range f.Messages {
		if len(node.Data.Content) == 0 {
			continue
		}
		out = append(out, Message{
			Sender: MessageSender{
				UserId:   node.Data.UserId,
				Nickname: node.Data.Nickname,
			},
			Message: node.Data.Content,
		})
	}
	return out
}
