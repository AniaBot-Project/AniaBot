package adapter

import (
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/spf13/viper"
)

// Adapter 平台适配器公共契约：任何平台（QQ、飞书、Telegram……）都必须实现的能力。
// 平台专属能力不进入本接口，而是以可选能力接口（如 QQExt）扩展，
// core 与插件通过类型断言探测，断言失败即平台不支持（退化处理）。
type Adapter interface {
	// Name 适配器名（与 Definition.Name 一致，如 "napcat"、"feishu"）
	Name() string
	// Platform 平台标识（如 "qq"、"feishu"），写入其产生的 Message/Notice.Platform，
	// core 按它对插件做 Meta.Platforms 过滤
	Platform() string
	SendMsg
	GetMsg
	SetTrigger(TriggerWrapper)
	Serve(*viper.Viper)
}

// ContactsExt 通讯录（群/好友列表）能力，可选接口。
// 平台支持枚举群聊/好友时实现（如 NapCat、飞书、Discord）；core 与 Web 面板
// 通过类型断言探测，断言失败即平台不支持（如 Telegram、QQ 官方无对应枚举 API）。
// 平台无好友概念（飞书/Discord 无法枚举私聊对端）时 GetFriendList 返回空列表。
type ContactsExt interface {
	// GetGroupList 获取群聊列表
	GetGroupList() (*[]message.GroupInfo, bool)
	// GetFriendList 获取好友列表（平台无好友概念时返回空列表）
	GetFriendList() (*[]message.Friend, bool)
}

// QQExt QQ（NapCat/OneBot v11）平台专属能力，可选接口。
// 合并转发、戳一戳、群签到、rkey、AI 语音等只有 QQ 具备的能力；
// 对应插件侧外观接口为 bot.QQ。
type QQExt interface {
	ContactsExt
	// SendGroupAIVoiceMsg 发送群AI语音消息
	SendGroupAIVoiceMsg(groupId message.QID, character, msg string) (msgId message.QID, success bool)
	// SendPokeMsg 发送戳一戳消息
	SendPokeMsg(userId message.QID, groupId *message.QID) (success bool)
	// SendGroupForwardMsg 发送群转发消息
	SendGroupForwardMsg(groupId message.QID, chain msgchain.GroupForwardChain) (msgId message.QID, success bool)
	// SendFriendForwardMsg 发送好友转发消息
	SendFriendForwardMsg(userId message.QID, chain msgchain.FriendForwardChain) (msgId message.QID, success bool)
	// SetMsgEmojiLike 设置消息表情点赞
	SetMsgEmojiLike(msgId message.QID, emojiId int, like bool) (success bool)
	// SendGroupSign 发送群签到消息
	SendGroupSign(groupId message.QID) (success bool)
	// GetNCrkey 获取NCRKEY
	GetNCrkey() ([]message.NCrkey, bool)
	// GetGroupUserInfo 获取群用户信息
	GetGroupUserInfo(groupId, userId message.QID) (info *message.GroupUserInfo, success bool)
	// GetGroupMemberList 获取群成员列表
	GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool)
	// GetForwardMsg 获取转发消息
	GetForwardMsg(msgId message.QID) (msgs *[]message.Message, success bool)
	// GetAICharacters 获取指定群的 AI 语音角色列表（角色按群提供，必须传群号）
	GetAICharacters(groupId message.QID) (*[]message.AICharacterGroup, bool)
	// GetPrivateFileURL 获取私聊文件URL
	GetPrivateFileURL(userId message.QID, fileId string) (string, bool)
}

// MediaRefresherExt 适配器侧媒体地址续期能力，可选接口。
// 与插件侧外观接口 bot.MediaRefresher 对应：QQ（NapCat / Luckylilia）的
// 图片、文件、语音地址带 rkey 等时效性鉴权参数，过期后由适配器向平台
// 重新申请，core 按 ID 前缀路由后转发调用。
type MediaRefresherExt interface {
	// RefreshMediaURL 重新获取媒体地址：kind 取 message.SegmentImage /
	// SegmentRecord / SegmentVideo / SegmentFile，fileId 为平台文件 ID；
	// groupId / userId 用于定位会话，可为空。平台不支持该类型时返回 false。
	RefreshMediaURL(kind, fileId, groupId, userId string) (string, bool)
}

type MessageHandler func(message.Message)
type GroupUploadHandler func(message.GroupUploadNotice)
type GroupAdminHandler func(message.GroupAdminNotice)
type GroupDecreaseHandler func(message.GroupDecreaseNotice)
type GroupIncreaseHandler func(message.GroupIncreaseNotice)
type GroupBanHandler func(message.GroupBanNotice)
type FriendAddHandler func(message.FriendAddNotice)
type GroupRecallHandler func(message.GroupRecallNotice)
type FriendRecallHandler func(message.FriendRecallNotice)
type PokeHandler func(message.PokeNotice)
type LuckyKingHandler func(message.LuckyKingNotice)
type HonorHandler func(message.HonorNotice)
type GroupMsgEmojiLikeHandler func(message.GroupMsgEmojiLikeNotice)
type EssenceHandler func(message.EssenceNotice)
type GroupCardHandler func(message.GroupCardNotice)
type PlatformEventHandler func(message.PlatformEvent)
type InteractionHandler func(message.InteractionEvent)

// TriggerWrapper 回调函数包装器。
// 消息与公共通知（成员变动/撤回/表情回应等可跨平台映射的事件）各平台按需触发；
// 平台自有事件（无法映射的）统一走 OnPlatformEvent。
type TriggerWrapper struct {
	OnGroupMsg          MessageHandler
	OnFriendMsg         MessageHandler
	OnGroupUpload       GroupUploadHandler
	OnGroupAdmin        GroupAdminHandler
	OnGroupDecrease     GroupDecreaseHandler
	OnGroupIncrease     GroupIncreaseHandler
	OnGroupBan          GroupBanHandler
	OnFriendAdd         FriendAddHandler
	OnGroupRecall       GroupRecallHandler
	OnFriendRecall      FriendRecallHandler
	OnPoke              PokeHandler
	OnLuckyKing         LuckyKingHandler
	OnHonor             HonorHandler
	OnGroupMsgEmojiLike GroupMsgEmojiLikeHandler
	OnEssence           EssenceHandler
	OnGroupCard         GroupCardHandler
	// OnPlatformEvent 平台特定事件（如飞书卡片回调），可选触发
	OnPlatformEvent PlatformEventHandler
	// OnInteraction 内联按钮点击回调（实现 InteractiveExt 的适配器触发），
	// core 按回调数据中的插件名前缀路由，可选触发
	OnInteraction InteractionHandler
}

// EventKeyer 事件幂等去重键提供者，可选接口。
// 事件订阅为 at-least-once 投递的平台（如飞书断线重连/ACK 丢失会重推同一事件）实现此接口，
// core 在事件进入插件链前按返回的键去重；无法提供稳定键时返回 false。
// 未实现此接口的适配器走 core 兜底：消息按「平台 + MessageId」去重（如 NapCat/OneBot），
// 通知不做兜底（避免组合键误伤）。
type EventKeyer interface {
	// MessageKey 消息去重键；消息无稳定 ID 时返回 false。
	// 注意：键应基于消息内容/消息 ID，而非投递事件 ID（飞书每次投递 event_id 可能不同）。
	MessageKey(msg message.Message) (string, bool)
	// NoticeKey 通知去重键；noticeType 为 NoticeType 字段值（如 "group_recall"）。
	// 无法提供稳定键时返回 false（core 不做组合兜底键）。
	NoticeKey(noticeType string, notice any) (string, bool)
}

// SelfIDProvider 机器人自身 ID 提供者，可选接口。
// 事件未携带 self_id（如飞书首次被 @ 前的空窗期）时，core 用其兜底填充
// msg.SelfId，使自消息过滤与 @ 提及检测生效。
type SelfIDProvider interface {
	SelfID() message.QID
}

// SegmentSupport 支持的消息段类型声明者，可选接口。
// 适配器声明其出站能渲染的通用段类型集合（值对应 message.SegmentXxx 常量）；
// core 在发送时对不支持的段类型告警（替代适配器出站静默丢弃），仅告警不阻断。
type SegmentSupport interface {
	SupportedSegments() []string
}

// StreamSenderExt 适配器侧流式发送能力（与插件侧外观接口 bot.StreamSender 对应）。
type StreamSenderExt interface {
	SendGroupStream(groupId message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool)
	SendFriendStream(userId message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool)
}

// MsgEditorExt 适配器侧消息编辑能力（与插件侧外观接口 bot.MsgEditor 对应）。
// 平台支持「先发后改」时实现（如 Telegram editMessageText）；
// core 经 BotWrapper 包装后暴露给插件类型断言。
type MsgEditorExt interface {
	// EditGroupMsg 编辑已发送的群聊消息（文本内容 + 可选 keyboard 段换按钮）
	EditGroupMsg(msgId message.QID, chain msgchain.GroupChain) bool
	// EditFriendMsg 编辑已发送的私聊消息
	EditFriendMsg(msgId message.QID, chain msgchain.FriendChain) bool
}

// InteractiveExt 适配器侧内联按钮能力（与插件侧外观接口 bot.Interactive 对应）。
// 实现方须在出站翻译 keyboard 段（message.ExtractKeyboard）为平台原生按钮，
// 入站把点击翻译为 InteractionEvent 经 TriggerWrapper.OnInteraction 上报；
// core 对未实现的平台自动剥离出站 keyboard 段。
type InteractiveExt interface {
	// SupportsKeyboard 平台是否支持内联按钮
	SupportsKeyboard() bool
}

// InteractionAnswerer 内联按钮点击应答能力，可选接口。
// 支持按钮的平台在回调被消费后应答（如 Telegram answerCallbackQuery，
// 消除客户端转圈；text 非空时作为提示展示）。core 在插件 OnInteraction
// 返回后经 InteractionEvent.CallbackId/AnswerText 调用。
type InteractionAnswerer interface {
	// AnswerInteraction 应答一次按钮点击；text 为空表示仅消除等待状态
	AnswerInteraction(callbackId, text string) bool
}

type SendMsg interface {
	// SendGroupMsg 发送群消息
	SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (msgId message.QID, success bool)
	// SendFriendMsg 发送私聊消息
	SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (msgId message.QID, success bool)
}

type GetMsg interface {
	// GetMsgDetail 获取消息详情
	GetMsgDetail(msgId message.QID) (msg *message.Message, success bool)
	// GetGroupDetail 获取群详情
	GetGroupDetail(groupId message.QID) (info *message.GroupInfo, success bool)
	// GetGroupMsgHistory 获取群消息历史
	GetGroupMsgHistory(groupId message.QID, count int, message_seq int) (*[]message.Message, bool)
	// GetFriendMsgHistory 获取私聊消息历史
	GetFriendMsgHistory(userId message.QID, count int, message_seq int) (*[]message.Message, bool)
}
