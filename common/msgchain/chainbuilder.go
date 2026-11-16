package msgchain

import "github.com/jeanhua/AniaBot/common/model/message"

type GroupChainBuilder interface {
	Text(text string) GroupChainBuilder
	Face(faceId int) GroupChainBuilder
	ImageUrl(url string) GroupChainBuilder
	ImageBase64(bs64code string) GroupChainBuilder
	ImageLocal(path string) GroupChainBuilder
	VideoUrl(url string) GroupChainBuilder
	VideoLocal(path string) GroupChainBuilder
	VideoBase64(bs64code string) GroupChainBuilder
	FileUrl(name, url string) GroupChainBuilder
	FileLocal(name, path string) GroupChainBuilder
	FileBase64(name, bs64code string) GroupChainBuilder
	Reply(msgId message.QID) GroupChainBuilder
	RecordUrl(url string) GroupChainBuilder
	RecordLocal(path string) GroupChainBuilder
	RecordBase64(bs64code string) GroupChainBuilder
	Raw(rawMsg ...message.OB11Segment) GroupChainBuilder
	// Keyboard 附加内联按钮键盘（行 × 按钮，Button/Row/ButtonURL 构造）；
	// 平台不支持时 core 出站自动剥离，插件应先断言 bot.Interactive 探测
	Keyboard(rows ...[]message.InlineButton) GroupChainBuilder

	Mention(userId message.QID) GroupChainBuilder
	Build() GroupChain
}

type FriendChainBuilder interface {
	Text(text string) FriendChainBuilder
	Face(faceId int) FriendChainBuilder
	ImageUrl(url string) FriendChainBuilder
	ImageBase64(bs64code string) FriendChainBuilder
	ImageLocal(path string) FriendChainBuilder
	VideoUrl(url string) FriendChainBuilder
	VideoLocal(path string) FriendChainBuilder
	VideoBase64(bs64code string) FriendChainBuilder
	FileUrl(name, url string) FriendChainBuilder
	FileLocal(name, path string) FriendChainBuilder
	FileBase64(name, bs64code string) FriendChainBuilder
	Reply(msgId message.QID) FriendChainBuilder
	RecordUrl(url string) FriendChainBuilder
	RecordLocal(path string) FriendChainBuilder
	RecordBase64(bs64code string) FriendChainBuilder
	Raw(rawMsg ...message.OB11Segment) FriendChainBuilder
	// Keyboard 附加内联按钮键盘（行 × 按钮，Button/Row/ButtonURL 构造）；
	// 平台不支持时 core 出站自动剥离，插件应先断言 bot.Interactive 探测
	Keyboard(rows ...[]message.InlineButton) FriendChainBuilder

	Build() FriendChain
}

type GroupForwardChainBuilder interface {
	Message(userId message.QID, nickname string, c GroupChain)
	Build() GroupForwardChain
}
type FriendForwardChainBuilder interface {
	Message(userId message.QID, nickname string, c FriendChain)
	Build() FriendForwardChain
}
type FriendChain interface {
	GetFriendMsg() []message.OB11Segment
}

type GroupChain interface {
	GetGroupMsg() []message.OB11Segment
}

type GroupForwardChain interface {
	GetForwardMsg() message.ForwardMessageSegment
}

type FriendForwardChain interface {
	GetForwardMsg() message.ForwardMessageSegment
}

// segChain 已有段切片的链包装。
type segChain []message.OB11Segment

func (s segChain) GetGroupMsg() []message.OB11Segment  { return s }
func (s segChain) GetFriendMsg() []message.OB11Segment { return s }

// NewGroupChain 包装已有段切片为 GroupChain（core 出站剥离不支持段后重建链用）。
func NewGroupChain(segs []message.OB11Segment) GroupChain { return segChain(segs) }

// NewFriendChain 包装已有段切片为 FriendChain。
func NewFriendChain(segs []message.OB11Segment) FriendChain { return segChain(segs) }

// forwardChain 已有合并转发内容的链包装（发送前事件被插件改写后重建链用）。
type forwardChain message.ForwardMessageSegment

func (f forwardChain) GetForwardMsg() message.ForwardMessageSegment {
	return message.ForwardMessageSegment(f)
}

// NewGroupForwardChain 包装已有合并转发内容为 GroupForwardChain。
func NewGroupForwardChain(seg message.ForwardMessageSegment) GroupForwardChain {
	return forwardChain(seg)
}

// NewFriendForwardChain 包装已有合并转发内容为 FriendForwardChain。
func NewFriendForwardChain(seg message.ForwardMessageSegment) FriendForwardChain {
	return forwardChain(seg)
}
