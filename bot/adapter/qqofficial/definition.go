// Package qqofficial QQ 官方机器人平台适配器（QQ 开放平台 API v2）。
//
// 接入方式：AppID/AppSecret 换取 access_token → WebSocket 网关接收事件
// （intents = GROUP_AND_C2C_EVENT 1<<25，覆盖群@消息/单聊消息/好友与群机器人事件）
// → REST OpenAPI 发送消息。与 NapCat（OneBot v11 协议端）不同，这是腾讯官方
// 的机器人平台：ID 为 per-AppID 的 openid 字符串（框架内加 "qo:" 前缀），
// 消息发送以「被动回复」为主（携带事件的 msg_id，群 5 分钟内 5 次、单聊 60 分钟内
// 4 次），主动消息有频控与配额限制。
//
// 平台限制（与 NapCat 的差异，见 docs）：
//   - 无消息历史/单条消息查询/群资料 API：GetMsgDetail/历史由内存缓存兜底，
//     GetGroupDetail 不可用
//   - 出站不能 @ 群成员（at 段退化为不可见），可开启 bot.qqofficial.auto_quote
//     让回复自动引用触发消息（默认关闭）
//   - 无消息编辑 API：不支持流式回复（StreamSenderExt 未实现，自动退化一次性发送）
//   - 媒体（图片/视频/语音/文件）需先经 /files 上传换取 file_info 再发 msg_type=7
package qqofficial

import (
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/pluginconfig"
	"github.com/spf13/viper"
)

// idPrefix QQ 官方平台 ID 的框架统一前缀：所有 openid（群/用户/消息 ID）
// 在框架内表示为 "qo:" 前缀的字符串，core 按前缀路由到本适配器。
// 注意 openid 是 per-AppID 的：同一用户在不同机器人 AppID 下 openid 不同，
// 且同一用户在群聊场景（member_openid）与单聊场景（user_openid）下也不同。
const idPrefix = "qo:"

// Platform 平台标识。
const Platform = "qqofficial"

// qqOfficialConfigFields QQ 官方平台配置字段（面板动态渲染）。
var qqOfficialConfigFields = []pluginconfig.Field{
	{Key: "bot.platform.qqofficial.enable", Label: "启用 QQ 官方平台", Type: "bool", Group: "平台适配器", Help: "是否启用 QQ 官方机器人平台（QQ 开放平台 API v2）；关闭后 Bot 不连接官方网关", Default: false},
	{Key: "bot.qqofficial.app_id", Label: "AppID", Type: "string", Group: "QQ 官方适配器", Help: "QQ 开放平台（q.qq.com）管理端「开发 → 开发设置」中的机器人 AppID"},
	{Key: "bot.qqofficial.app_secret", Label: "AppSecret", Type: "password", Group: "QQ 官方适配器", Sensitive: true, Help: "机器人 AppSecret，用于换取 access_token（Token 鉴权已废弃，请勿填写旧 Token）"},
	{Key: "bot.qqofficial.sandbox", Label: "沙箱环境", Type: "bool", Group: "QQ 官方适配器", Help: "开启后连接沙箱环境（sandbox.api.sgroup.qq.com），联调时使用；机器人未上架前只能连接沙箱", Default: false},
	{Key: "bot.qqofficial.api_base", Label: "API 地址", Type: "string", Group: "QQ 官方适配器", Help: "OpenAPI 地址，默认正式环境；沙箱开关优先于此配置", Default: "https://api.sgroup.qq.com"},
	{Key: "bot.qqofficial.auto_quote", Label: "自动引用触发消息", Type: "bool", Group: "QQ 官方适配器", Help: "开启后回复自动以引用气泡关联触发的提问消息（官方群消息无法 @ 成员，引用用于表达回复关系）；关闭则直接发送，不带引用", Default: false},
	{Key: "bot.qqofficial.markdown", Label: "Markdown 渲染", Type: "bool", Group: "QQ 官方适配器", Help: "开启后 AI 的文本回复以 msg_type=2 Markdown 消息发送（标题/加粗/列表等富文本渲染）；发送失败自动降级纯文本", Default: false},
}

// init 注册 QQ 官方适配器定义。
func init() {
	adapter.Register(adapter.Definition{
		Name:         "qqofficial",
		Platform:     Platform,
		IDPrefix:     idPrefix,
		ConfigFields: qqOfficialConfigFields,
		New: func(cfg *viper.Viper) (adapter.Adapter, error) {
			return NewAdapter(cfg), nil
		},
	})
}
