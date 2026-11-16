package plugin

import (
	"context"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// SendMsgInterceptor 可选接口：插件实现后可在 Bot 发送消息（其他插件或框架
// 自身通过 bot.Bot 发出的群聊 / 私聊消息、合并转发、流式回复）之前处理事件。
//
// 事件按插件 Order 从小到大依次广播；返回 false 表示取消本次发送，后续插件
// 不再收到该事件，消息也不会提交给平台（发送后事件同样不触发）。插件可直接
// 修改 ev 里的消息段 / 合并转发内容来改写将要发出的消息。
//
// 注意：发送前事件在发送消息的调用方 goroutine 中同步执行，耗时过长会拖慢
// 发送；框架为单次事件分发提供了带超时的 context。在事件回调里再次发送
// 消息不会再触发发送事件（框架按 goroutine 屏蔽嵌套分发），避免无限递归。
type SendMsgInterceptor interface {
	// OnBeforeSendMsg 消息提交平台前触发
	OnBeforeSendMsg(ctx context.Context, b bot.Bot, ev *message.OutgoingMessage) (bool, error)
}

// SendMsgObserver 可选接口：插件实现后可在 Bot 发送消息之后处理事件。
//
// 事件广播制（不中断），平台返回后触发；成功与失败都会触发，ev.Success /
// ev.MessageId 为平台结果。流式回复在消息结束时触发一次，内容为最终文本。
// 同样地，在事件回调里再次发送消息不会再触发发送事件，避免无限递归。
type SendMsgObserver interface {
	// OnAfterSendMsg 消息提交平台后触发
	OnAfterSendMsg(ctx context.Context, b bot.Bot, ev message.SentMessage) error
}
