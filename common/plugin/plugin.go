package plugin

import (
	"context"
	"log/slog"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/pluginconfig"
	"github.com/jeanhua/AniaBot/common/storage"
	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"
)

type Plugin interface {
	GetMeta() *Meta
	DI
	BasicEvent
	StartupEvent
	NoticeEvent
	PanicEvent
}

type DI interface {
	SetStorage(s storage.Storage)
	SetPersistentStorage(s storage.PersistentStorage)
	SetRestyClient(*resty.Client)
	SetLogger(*slog.Logger)
	SetConfig(SystemConfig)
	SetConfigEditor(ConfigEditor)
}

// ConfigEditor 配置中心读写能力，由 core 在 DI 时注入（configstore.Store 实现）。
// 配置为点分键（与历史 viper 键一致，如 plugin.ai_chat_bot.base_url），
// 值为 JSON 解码后的原生类型。改动写入数据库，重启后生效。
// 仅供需要读写框架配置的插件使用（如 AI 配置管理工具）；普通插件读配置
// 仍应使用 Start 传入的 viper 或 ConfigSchema 结构体绑定。
type ConfigEditor interface {
	Get(key string) (any, bool)
	Set(key string, val any) error
	Delete(key string) bool
	All() map[string]any
}

type BasicEvent interface {
	// OnGroupMsg 收到群聊消息触发事件
	OnGroupMsg(context.Context, bot.Bot, command.Command, message.Message) (bool, error)
	// OnFriendMsg 收到私聊消息触发事件
	OnFriendMsg(context.Context, bot.Bot, command.Command, message.Message) (bool, error)
}

type CronManager interface {
	// AddFunc 添加定时任务
	AddFunc(spec string, cmd func()) (cron.EntryID, error)
}

type StartupEvent interface {
	// Start 插件初始化事件
	Start(ctx context.Context, cfg *viper.Viper) error
	// StartCron 初始化cron事件
	StartCron(ctx context.Context, bot bot.Bot, c CronManager) error
	// Awake Bot启动完成事件
	Awake(ctx context.Context, bot bot.Bot) error
}

type NoticeEvent interface {
	// OnGroupUpload 处理群文件上传
	OnGroupUpload(context.Context, bot.Bot, message.GroupUploadNotice) error
	// OnGroupAdmin 处理群管理员变动
	OnGroupAdmin(context.Context, bot.Bot, message.GroupAdminNotice) error
	// OnGroupDecrease 处理群成员减少
	OnGroupDecrease(context.Context, bot.Bot, message.GroupDecreaseNotice) error
	// OnGroupIncrease 处理群成员增加
	OnGroupIncrease(context.Context, bot.Bot, message.GroupIncreaseNotice) error
	// OnGroupBan 处理群禁言
	OnGroupBan(context.Context, bot.Bot, message.GroupBanNotice) error
	// OnFriendAdd 处理好友添加
	OnFriendAdd(context.Context, bot.Bot, message.FriendAddNotice) error
	// OnGroupRecall 处理群消息撤回
	OnGroupRecall(context.Context, bot.Bot, message.GroupRecallNotice) error
	// OnFriendRecall 处理好友消息撤回
	OnFriendRecall(context.Context, bot.Bot, message.FriendRecallNotice) error
	// OnPoke 处理戳一戳
	OnPoke(context.Context, bot.Bot, message.PokeNotice) error
	// OnLuckyKing 处理运气王
	OnLuckyKing(context.Context, bot.Bot, message.LuckyKingNotice) error
	// OnHonor 处理群荣誉变更
	OnHonor(context.Context, bot.Bot, message.HonorNotice) error
	// OnGroupMsgEmojiLike 处理群消息表情回应
	OnGroupMsgEmojiLike(context.Context, bot.Bot, message.GroupMsgEmojiLikeNotice) error
	// OnEssence 处理群精华消息变更
	OnEssence(context.Context, bot.Bot, message.EssenceNotice) error
	// OnGroupCard 处理群名片变更
	OnGroupCard(context.Context, bot.Bot, message.GroupCardNotice) error
}

const (
	LevelLog        = -1000 // 日志层插件Order参考
	LevelNormal     = 0     // 普通插件Order参考
	LevelPostHandle = 1000  // 后置处理层Order参考
)

type PanicEvent interface {
	// OnPanic 处理插件运行时panic
	OnPanic(ctx context.Context, bot bot.Bot, name string, err any)
}

// UnloadReason 插件卸载原因，作为 UnloadEvent.OnUnload 的参数，
// 让插件区分「重启后的正常退出」与「被彻底卸载」两种清理场景。
type UnloadReason string

const (
	// UnloadShutdown Bot 退出或重启（/exit、/reboot、面板重启、自动更新，
	// 以及插件市场流水线完成后的重启）：全部插件都会收到，适合落盘、
	// 释放资源等收尾操作；插件重启后会重新加载，不要在此删除持久化数据。
	UnloadShutdown UnloadReason = "shutdown"
	// UnloadUninstall 插件被插件市场卸载：仅被卸载的插件收到，
	// 适合清理插件自身的数据等只应在卸载时执行的操作。
	UnloadUninstall UnloadReason = "uninstall"
)

// UnloadEvent 可选接口：插件实现后，框架在插件被卸载前调用一次其清理钩子
// （每个插件最多触发一次），用于释放资源、清理数据等收尾操作。
// 触发时机见 UnloadReason；钩子可能与运行期事件并发执行，实现需自行保证
// 并发安全，并在超时时间内返回（超时后框架继续卸载流程）。
type UnloadEvent interface {
	// OnUnload 插件卸载事件
	OnUnload(ctx context.Context, reason UnloadReason) error
}

// PlatformEventHandler 可选接口：插件实现后可接收平台特定事件——
// 无法映射为公共事件（消息/通知）的平台自有事件，如飞书卡片回调、
// 机器人被拉进群等。事件经 core 按 Meta.Platforms 过滤后广播（不中断），
// Data 的类型由产生事件的平台适配器包定义，插件按需类型断言。
type PlatformEventHandler interface {
	// OnPlatformEvent 处理平台特定事件
	OnPlatformEvent(ctx context.Context, bot bot.Bot, event message.PlatformEvent) error
}

// InteractionHandler 可选接口：插件实现后可接收内联按钮点击回调。
// 插件在消息链中附加 keyboard 段时，回调数据应以 Meta.CallbackData 打包
// （"插件名:载荷"），框架按前缀把点击路由回本插件的 OnInteraction，
// 载荷部分填入 ev.Data；handler 返回后框架统一应答平台（ev.AnswerText
// 非空时作为提示文本展示在点击者客户端）。
type InteractionHandler interface {
	// OnInteraction 处理内联按钮点击
	OnInteraction(ctx context.Context, bot bot.Bot, ev *message.InteractionEvent) error
}

type SystemConfig struct {
	AdminId message.QID
}

// ConfigRegistrar 可选接口：插件实现后，框架启动时会收集其声明的配置字段
// （键、显示名、类型、默认值等元信息），自动补齐缺失的默认值，
// 并让 Web 控制面板动态渲染对应表单——新增插件无需改动面板代码。
//
// 注意：该方法是纯元信息声明，框架在依赖注入（DI）之前调用，
// 实现中不应依赖 Logger/Storage 等注入字段。
//
// 新插件建议优先实现 ConfigSchemaProvider（结构体标签声明 + 自动填充），
// 本接口保留用于框架自身字段与需要动态生成字段的场景。两者可共存，
// 键相同时后注册者原位覆盖。
type ConfigRegistrar interface {
	ConfigFields() []pluginconfig.Field
}

// ConfigSchemaProvider 可选接口：插件返回配置结构体指针（字段带
// pluginconfig 的 cfg 标签），框架启动时自动完成：
//  1. 反射注册字段元信息（面板渲染 + 默认值补齐，等价于 ConfigRegistrar）；
//  2. 在 Start 之前把配置中心的值填充进结构体——插件 Start 里直接读
//     结构体字段，无需再手写 cfg.Get* 逐个读取。
//
// 注意：该方法框架在依赖注入（DI）之前调用，实现中不应依赖
// Logger/Storage 等注入字段；且每次调用必须返回同一指针
// （推荐返回插件结构体上某个字段的地址，如 return &p.cfg）。
type ConfigSchemaProvider interface {
	ConfigSchema() any
}
