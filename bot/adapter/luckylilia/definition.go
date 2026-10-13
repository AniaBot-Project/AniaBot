package luckylilia

import (
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/aitool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/pluginconfig"
	"github.com/spf13/viper"
)

// luckyliliaConfigFields Luckylilia（LLBot，OneBot v11 QQ 协议端）平台配置字段（面板动态渲染）。
// 配置键独立于 napcat 的 bot.adapter.* 命名空间，两个适配器可同时启用（两个 QQ 账号并存）。
var luckyliliaConfigFields = []pluginconfig.Field{
	{Key: "bot.platform.luckylilia.enable", Label: "启用 QQ(Luckylilia) 平台", Type: "bool", Group: "平台适配器", Help: "是否启用 Luckylilia（LLBot，OneBot v11）平台；与 QQ(NapCat) 可并存，ID 前缀为 lil:", Default: false},
	{Key: "bot.luckylilia.mode", Label: "连接模式", Type: "select", Options: []string{"ws", "http"}, Group: "Luckylilia 适配器", Help: "ws（WebSocket，推荐）或 http（需同时配置下方「本地监听端口」和「LLBot HTTP 地址」），重启后生效", Default: "ws"},
	{Key: "bot.luckylilia.token", Label: "Token", Type: "password", Group: "Luckylilia 适配器", Sensitive: true, Help: "LLBot 侧设置了 token 时填写（Authorization: Bearer 方式携带）"},
	{Key: "bot.luckylilia.ws.address", Label: "WS 地址", Type: "string", Group: "Luckylilia 适配器", Help: "LLBot WebSocket 正向服务地址", Default: "ws://localhost:3001"},
	{Key: "bot.luckylilia.ws.worker_count", Label: "处理线程数", Type: "int", Group: "Luckylilia 适配器", Help: "0 为自动调整", Default: 0},
	{Key: "bot.luckylilia.ws.worker_queue_size", Label: "消息队列大小", Type: "int", Group: "Luckylilia 适配器", Help: "超出限制的消息将被丢弃", Default: 1024},
	{Key: "bot.luckylilia.request_timeout_sec", Label: "请求超时(秒)", Type: "int", Group: "Luckylilia 适配器", Help: "等待 LLBot 回包的时长；超时按失败处理（实际可能仍在执行）；0 或负数回退默认 10 秒，重启后生效", Default: 10},
	{Key: "bot.luckylilia.upload_timeout_sec", Label: "上传超时(秒)", Type: "int", Group: "Luckylilia 适配器", Help: "发送文件/视频/语音等需先上传内容的消息时的等待时长，大文件或慢网络下可调大；0 或负数回退默认 120 秒，重启后生效", Default: 120},
	{Key: "bot.luckylilia.http.listen_port", Label: "本地监听端口", Type: "int", Group: "Luckylilia 适配器", Help: "HTTP 模式下 Bot 接收 LLBot 事件上报的本地端口；LLBot 侧需添加「HTTP 客户端」，上报地址填 http://<Bot 主机 IP>:<此端口>", Default: 6689},
	{Key: "bot.luckylilia.http.target_url", Label: "LLBot HTTP 地址", Type: "string", Group: "Luckylilia 适配器", Help: "HTTP 模式下 LLBot 的「HTTP 服务器」地址，Bot 通过它调用 LLBot 接口（发消息等）", Default: "http://localhost:6690"},
}

// init 注册 Luckylilia（QQ 平台）适配器定义与 bot 外观包装器。
// 通过 cmd/main.go 空白导入本包触发注册，框架无需任何平台硬编码。
func init() {
	adapter.Register(adapter.Definition{
		Name:         "luckylilia",
		Platform:     "qq",
		IDPrefix:     idPrefix,
		ConfigFields: luckyliliaConfigFields,
		New: func(cfg *viper.Viper) (adapter.Adapter, error) {
			return NewAdapter(cfg), nil
		},
	})

	// QQ 平台专属能力包装：事件来源适配器实现 adapter.QQExt 时，
	// 插件在事件回调里可对收到的 bot 断言 bot.QQ 使用 QQ 专属方法。
	adapter.RegisterBotWrapper(func(base bot.Bot, src adapter.Adapter) bot.Bot {
		qq, ok := src.(adapter.QQExt)
		if !ok {
			return base
		}
		return &lilBot{Bot: base, qq: qq}
	})
}

// lilBot QQ 平台事件的外观 bot：嵌入公共 bot.Bot，额外暴露 QQ 专属能力。
type lilBot struct {
	bot.Bot
	qq adapter.QQExt
}

func (q *lilBot) SendGroupAIVoiceMsg(groupId message.QID, character, msg string) (message.QID, bool) {
	return q.qq.SendGroupAIVoiceMsg(groupId, character, msg)
}

func (q *lilBot) SendPokeMsg(userId message.QID, groupId *message.QID) bool {
	return q.qq.SendPokeMsg(userId, groupId)
}

func (q *lilBot) SendGroupForwardMsg(groupId message.QID, chain msgchain.GroupForwardChain) (message.QID, bool) {
	return q.qq.SendGroupForwardMsg(groupId, chain)
}

func (q *lilBot) SendFriendForwardMsg(userId message.QID, chain msgchain.FriendForwardChain) (message.QID, bool) {
	return q.qq.SendFriendForwardMsg(userId, chain)
}

func (q *lilBot) SetMsgEmojiLike(msgId message.QID, emojiId int, like bool) bool {
	return q.qq.SetMsgEmojiLike(msgId, emojiId, like)
}

func (q *lilBot) SendGroupSign(groupId message.QID) bool {
	return q.qq.SendGroupSign(groupId)
}

func (q *lilBot) GetForwardMsg(msgId message.QID) (*[]message.Message, bool) {
	return q.qq.GetForwardMsg(msgId)
}

func (q *lilBot) GetGroupUserInfo(groupId, userId message.QID) (*message.GroupUserInfo, bool) {
	return q.qq.GetGroupUserInfo(groupId, userId)
}

func (q *lilBot) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	return q.qq.GetGroupMemberList(groupId, noCache)
}

func (q *lilBot) GetFriendList() (*[]message.Friend, bool) {
	return q.qq.GetFriendList()
}

func (q *lilBot) GetGroupList() (*[]message.GroupInfo, bool) {
	return q.qq.GetGroupList()
}

func (q *lilBot) GetAIChatacter() (*[]message.AIChatacter, bool) {
	return q.qq.GetAIChatacter()
}

func (q *lilBot) GetPrivateFileURL(userId message.QID, fileId string) (string, bool) {
	return q.qq.GetPrivateFileURL(userId, fileId)
}

func (q *lilBot) GetNCrkey() ([]message.NCrkey, bool) {
	return q.qq.GetNCrkey()
}

// AITools 实现 aitool.Provider：向 AI 对话会话注入 QQ 平台专属工具
// （AI 语音、戳一戳、群签到、群成员信息等，见 aitool.QQTools）。
func (q *lilBot) AITools(ctx aitool.Context) []aitool.Tool {
	return aitool.QQTools(ctx, q.qq)
}

// SendGroupStream/SendFriendStream 显式覆盖为不支持：lilBot 嵌入 bot.Bot 接口，
// 不覆盖会把 *AniaBot 的 StreamSender 方法提升上来，使插件断言 bot.StreamSender
// 意外成功。OneBot v11 无消息编辑 API，QQ 平台流式回复退化为一次性。
func (q *lilBot) SendGroupStream(groupId message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	return nil, false
}

func (q *lilBot) SendFriendStream(userId message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	return nil, false
}

func (n *luckyliliaWebSocketAdapter) Name() string     { return "luckylilia" }
func (n *luckyliliaWebSocketAdapter) Platform() string { return "qq" }
func (n *luckyliliaHttpAdapter) Name() string          { return "luckylilia" }
func (n *luckyliliaHttpAdapter) Platform() string      { return "qq" }

// onebot11Segments OneBot v11 支持的全部通用段类型（出站原样透传给 LLBot）。
var onebot11Segments = []string{
	message.SegmentText, message.SegmentFace, message.SegmentImage, message.SegmentRecord,
	message.SegmentVideo, message.SegmentMention, message.SegmentReply, message.SegmentForward,
	message.SegmentFile, message.SegmentJson, message.SegmentMusic,
}

// SupportedSegments 实现 adapter.SegmentSupport：OneBot v11 全量段。
func (n *luckyliliaWebSocketAdapter) SupportedSegments() []string { return onebot11Segments }
func (n *luckyliliaHttpAdapter) SupportedSegments() []string      { return onebot11Segments }
