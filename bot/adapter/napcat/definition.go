package napcat

import (
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/aitool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/pluginconfig"
	"github.com/spf13/viper"
)

// napcatConfigFields NapCat 平台配置字段（面板动态渲染）。
// 与历史 viper 键一一对应，确保存量配置迁移后语义不变。
var napcatConfigFields = []pluginconfig.Field{
	{Key: "bot.platform.napcat.enable", Label: "启用 QQ(NapCat) 平台", Type: "bool", Group: "平台适配器", Help: "是否启用 QQ 平台；关闭后 Bot 将不连接 NapCat", Default: true},
	{Key: "bot.adapter.mode", Label: "连接模式", Type: "select", Options: []string{"ws", "http"}, Group: "NapCat 适配器", Help: "ws（WebSocket，推荐）或 http（需同时配置下方「本地监听端口」和「NapCat HTTP 地址」），重启后生效", Default: "ws"},
	{Key: "bot.adapter.token", Label: "Token", Type: "password", Group: "NapCat 适配器", Sensitive: true, Help: "NapCat 侧设置了 token 时填写"},
	{Key: "bot.adapter.ws.address", Label: "WS 地址", Type: "string", Group: "NapCat 适配器", Help: "NapCat WebSocket Server 地址", Default: "ws://localhost:4455"},
	{Key: "bot.adapter.ws.worker_count", Label: "处理线程数", Type: "int", Group: "NapCat 适配器", Help: "0 为自动调整", Default: 0},
	{Key: "bot.adapter.ws.worker_queue_size", Label: "消息队列大小", Type: "int", Group: "NapCat 适配器", Help: "超出限制的消息将被丢弃", Default: 1024},
	{Key: "bot.adapter.http.listen_port", Label: "本地监听端口", Type: "int", Group: "NapCat 适配器", Help: "HTTP 模式下 Bot 接收 NapCat 事件上报的本地端口；NapCat 侧需添加「HTTP 客户端」，上报地址填 http://<Bot 主机 IP>:<此端口>", Default: 6679},
	{Key: "bot.adapter.http.target_url", Label: "NapCat HTTP 地址", Type: "string", Group: "NapCat 适配器", Help: "HTTP 模式下 NapCat 的「HTTP 服务器」地址，Bot 通过它调用 NapCat 接口（发消息等）", Default: "http://localhost:6680"},
	{Key: "bot.adapter.request_timeout_sec", Label: "请求超时(秒)", Type: "int", Group: "NapCat 适配器", Help: "等待 NapCat 回包的时长；超时按失败处理（实际可能仍在执行）；0 或负数回退默认 10 秒，重启后生效", Default: 10},
	{Key: "bot.adapter.upload_timeout_sec", Label: "上传超时(秒)", Type: "int", Group: "NapCat 适配器", Help: "发送文件/视频/语音等需先上传内容的消息时的等待时长，大文件或慢网络下可调大；0 或负数回退默认 120 秒，重启后生效", Default: 120},
}

// init 注册 NapCat（QQ 平台）适配器定义与 bot 外观包装器。
// 通过 cmd/main.go 空白导入本包触发注册，框架无需任何平台硬编码。
func init() {
	adapter.Register(adapter.Definition{
		Name:         "napcat",
		Platform:     "qq",
		IDPrefix:     message.QQIDPrefix,
		ConfigFields: napcatConfigFields,
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
		return &qqBot{Bot: base, qq: qq}
	})
}

// qqBot QQ 平台事件的外观 bot：嵌入公共 bot.Bot，额外暴露 QQ 专属能力。
type qqBot struct {
	bot.Bot
	qq adapter.QQExt
}

func (q *qqBot) SendGroupAIVoiceMsg(groupId message.QID, character, msg string) (message.QID, bool) {
	return q.qq.SendGroupAIVoiceMsg(groupId, character, msg)
}

func (q *qqBot) SendPokeMsg(userId message.QID, groupId *message.QID) bool {
	return q.qq.SendPokeMsg(userId, groupId)
}

func (q *qqBot) SendGroupForwardMsg(groupId message.QID, chain msgchain.GroupForwardChain) (message.QID, bool) {
	return q.qq.SendGroupForwardMsg(groupId, chain)
}

func (q *qqBot) SendFriendForwardMsg(userId message.QID, chain msgchain.FriendForwardChain) (message.QID, bool) {
	return q.qq.SendFriendForwardMsg(userId, chain)
}

func (q *qqBot) SetMsgEmojiLike(msgId message.QID, emojiId int, like bool) bool {
	return q.qq.SetMsgEmojiLike(msgId, emojiId, like)
}

func (q *qqBot) SendGroupSign(groupId message.QID) bool {
	return q.qq.SendGroupSign(groupId)
}

func (q *qqBot) GetForwardMsg(msgId message.QID) (*[]message.Message, bool) {
	return q.qq.GetForwardMsg(msgId)
}

func (q *qqBot) GetGroupUserInfo(groupId, userId message.QID) (*message.GroupUserInfo, bool) {
	return q.qq.GetGroupUserInfo(groupId, userId)
}

func (q *qqBot) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	return q.qq.GetGroupMemberList(groupId, noCache)
}

func (q *qqBot) GetFriendList() (*[]message.Friend, bool) {
	return q.qq.GetFriendList()
}

func (q *qqBot) GetGroupList() (*[]message.GroupInfo, bool) {
	return q.qq.GetGroupList()
}

func (q *qqBot) GetAIChatacter() (*[]message.AIChatacter, bool) {
	return q.qq.GetAIChatacter()
}

func (q *qqBot) GetPrivateFileURL(userId message.QID, fileId string) (string, bool) {
	return q.qq.GetPrivateFileURL(userId, fileId)
}

func (q *qqBot) GetNCrkey() ([]message.NCrkey, bool) {
	return q.qq.GetNCrkey()
}

// AITools 实现 aitool.Provider：向 AI 对话会话注入 QQ 平台专属工具
// （AI 语音、戳一戳、群签到、群成员信息等，见 aitool.QQTools）。
func (q *qqBot) AITools(ctx aitool.Context) []aitool.Tool {
	return aitool.QQTools(ctx, q.qq)
}

// SendGroupStream/SendFriendStream 显式覆盖为不支持：qqBot 嵌入 bot.Bot 接口，
// 不覆盖会把 *AniaBot 的 StreamSender 方法提升上来，使插件断言 bot.StreamSender
// 意外成功。OneBot v11 无消息编辑 API，QQ 平台流式回复退化为一次性。
func (q *qqBot) SendGroupStream(groupId message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	return nil, false
}

func (q *qqBot) SendFriendStream(userId message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	return nil, false
}

func (n *napcatWebSocketAdapter) Name() string     { return "napcat" }
func (n *napcatWebSocketAdapter) Platform() string { return "qq" }
func (n *napcatHttpAdapter) Name() string          { return "napcat" }
func (n *napcatHttpAdapter) Platform() string      { return "qq" }

// onebot11Segments OneBot v11 支持的全部通用段类型（出站原样透传给 NapCat）。
var onebot11Segments = []string{
	message.SegmentText, message.SegmentFace, message.SegmentImage, message.SegmentRecord,
	message.SegmentVideo, message.SegmentMention, message.SegmentReply, message.SegmentForward,
	message.SegmentFile, message.SegmentJson, message.SegmentMusic,
}

// SupportedSegments 实现 adapter.SegmentSupport：OneBot v11 全量段。
func (n *napcatWebSocketAdapter) SupportedSegments() []string { return onebot11Segments }
func (n *napcatHttpAdapter) SupportedSegments() []string      { return onebot11Segments }
