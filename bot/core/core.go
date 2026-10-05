package core

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/bot/adminpanel"
	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/consollog"
	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/bot/component/sysrestart"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/bot/core/configstore"
	"github.com/jeanhua/AniaBot/bot/marketplace"
	"github.com/jeanhua/AniaBot/bot/utils"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/pluginconfig"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/jeanhua/AniaBot/common/storage"
	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"
)

// adapterEntry 一个已启用的平台适配器实例。
// evBot 是该平台能力包装后的 bot 外观：事件分发时传给插件，
// 插件可通过类型断言探测平台专属能力（如 bot.QQ）。
type adapterEntry struct {
	def     adapter.Definition
	adapter adapter.Adapter
	evBot   bot.Bot
}

type AniaBot struct {
	ctx    context.Context
	cancel context.CancelFunc

	// adapters 已启用的平台适配器（可多开：QQ + 飞书……）
	adapters []*adapterEntry
	plugins  []plugin.Plugin
	cfg      *viper.Viper

	configStore *configstore.Store

	storage     storage.Storage
	persistent  storage.PersistentStorage
	restyClient *resty.Client

	logger *slog.Logger

	// 插件名称集合
	pluginSet map[string]struct{}

	// 卸载钩子调度：unloadedPlugins 记录已触发过 OnUnload 的插件名
	// （市场卸载单独触发后，退出/重启清扫不再重复调用），
	// pluginsStarted 保证插件 Start 之前的 Stop 不会误触发钩子。
	unloadMu        sync.Mutex
	unloadedPlugins map[string]struct{}
	pluginsStarted  atomic.Bool

	// goroutine数目
	goroutineNum atomic.Int32

	// 启动时间
	startTime time.Time

	// 适配器是否已开始连接（首次启动等待向导时不会启动）
	adapterStarted atomic.Bool
}

const (
	StartEventTimeout     = time.Minute
	StartCronEventTimeout = time.Minute
	AwakeEventTimeout     = time.Minute

	// MsgEventTimeout 单条消息事件处理超时的默认值（bot.msg_event_timeout_sec
	// 未配置/非法时的兜底）；实际超时见 AniaBot.msgEventTimeout
	MsgEventTimeout    = time.Minute * 5
	NoticeEventTimeout = time.Minute * 5

	// msgEventMaxTimeoutSec 消息处理超时的配置上限（秒）：先限幅 int 再乘
	// time.Second，防止超大配置值 int64 溢出为负 duration（context 立即过期）
	msgEventMaxTimeoutSec = 86400
)

// msgEventTimeout 单条消息事件（如一次 AI 回复）的处理超时：取面板配置
// bot.msg_event_timeout_sec，缺失/非法时兜底 MsgEventTimeout（5 分钟）。
// 每次事件读取，配置在重启后随 viper 快照生效。
func (ania *AniaBot) msgEventTimeout() time.Duration {
	sec := 0
	if ania.cfg != nil { // 测试等场景下未注入配置时直接走默认值
		sec = ania.cfg.GetInt("bot.msg_event_timeout_sec")
	}
	if sec <= 0 {
		return MsgEventTimeout
	}
	if sec > msgEventMaxTimeoutSec {
		sec = msgEventMaxTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

//go:embed logo.txt
var LogoASCII string

type Option func(*AniaBot)

func WithStorage(storage storage.Storage) Option {
	return func(ania *AniaBot) {
		ania.storage = storage
	}
}

func WithPersistentStorage(storage storage.PersistentStorage) Option {
	return func(ania *AniaBot) {
		ania.persistent = storage
	}
}

func WithConfig(config *viper.Viper) Option {
	return func(ania *AniaBot) {
		ania.cfg = config
	}
}

func WithResty(restyClient *resty.Client) Option {
	return func(ania *AniaBot) {
		ania.restyClient = restyClient
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(ania *AniaBot) {
		ania.logger = logger
		inlogger = logger
		// 注入的自定义 logger 同样设为 slog 默认，保证依赖 slog.Default()
		// 的组件（tasklog/querylog/平台适配器等）与框架共用同一输出。
		slog.SetDefault(logger)
	}
}

func NewAniaBot(option ...Option) *AniaBot {
	ctx, cancel := context.WithCancel(context.Background())
	ania := &AniaBot{
		ctx:             ctx,
		cancel:          cancel,
		pluginSet:       map[string]struct{}{},
		unloadedPlugins: map[string]struct{}{},
		plugins:         make([]plugin.Plugin, 0),
	}
	for _, op := range option {
		op(ania)
	}
	if ania.logger == nil {
		ania.logger = Logger()
	}
	return ania
}

// addAdapter 注册一个已创建好的适配器：计算平台能力包装的 bot 外观，并注入事件回调。
func (ania *AniaBot) addAdapter(def adapter.Definition, a adapter.Adapter) {
	e := &adapterEntry{def: def, adapter: a}
	e.evBot = adapter.WrapBot(ania, a)
	a.SetTrigger(ania.makeTrigger(e))
	ania.adapters = append(ania.adapters, e)
}

// makeTrigger 构造某个适配器的事件回调包装器：各回调携带来源适配器，
// 以便分发时按平台过滤插件并向插件传入平台能力包装的 bot。
func (ania *AniaBot) makeTrigger(e *adapterEntry) adapter.TriggerWrapper {
	return adapter.TriggerWrapper{
		OnGroupMsg:          func(msg message.Message) { ania.onGroupEvent(e, msg) },
		OnFriendMsg:         func(msg message.Message) { ania.onFriendEvent(e, msg) },
		OnGroupUpload:       func(n message.GroupUploadNotice) { ania.onGroupUploadEvent(e, n) },
		OnGroupAdmin:        func(n message.GroupAdminNotice) { ania.onGroupAdminEvent(e, n) },
		OnGroupDecrease:     func(n message.GroupDecreaseNotice) { ania.onGroupDecreaseEvent(e, n) },
		OnGroupIncrease:     func(n message.GroupIncreaseNotice) { ania.onGroupIncreaseEvent(e, n) },
		OnGroupBan:          func(n message.GroupBanNotice) { ania.onGroupBanEvent(e, n) },
		OnFriendAdd:         func(n message.FriendAddNotice) { ania.onFriendAddEvent(e, n) },
		OnGroupRecall:       func(n message.GroupRecallNotice) { ania.onGroupRecallEvent(e, n) },
		OnFriendRecall:      func(n message.FriendRecallNotice) { ania.onFriendRecallEvent(e, n) },
		OnPoke:              func(n message.PokeNotice) { ania.onPokeEvent(e, n) },
		OnLuckyKing:         func(n message.LuckyKingNotice) { ania.onLuckyKingEvent(e, n) },
		OnHonor:             func(n message.HonorNotice) { ania.onHonorEvent(e, n) },
		OnGroupMsgEmojiLike: func(n message.GroupMsgEmojiLikeNotice) { ania.onGroupMsgEmojiLikeEvent(e, n) },
		OnEssence:           func(n message.EssenceNotice) { ania.onEssenceEvent(e, n) },
		OnGroupCard:         func(n message.GroupCardNotice) { ania.onGroupCardEvent(e, n) },
		OnPlatformEvent:     func(ev message.PlatformEvent) { ania.onPlatformEvent(e, ev) },
		OnInteraction:       func(ev message.InteractionEvent) { ania.onInteraction(e, ev) },
	}
}

// route 按统一 ID 前缀路由到对应平台适配器；未迁移的裸数字 QQ ID
// 仍回退到 QQ 平台，保证升级期间兼容旧数据。
func (ania *AniaBot) route(id message.QID) adapter.Adapter {
	if len(ania.adapters) == 0 {
		return nil
	}
	s := string(id)
	for _, e := range ania.adapters {
		if e.def.IDPrefix != "" && strings.HasPrefix(s, e.def.IDPrefix) {
			return e.adapter
		}
	}
	// 旧版 QQ 无前缀数字 ID 兼容：未迁移时仍路由到 QQ（NapCat）。
	if message.NormalizeQQID(string(id)) != string(id) {
		for _, e := range ania.adapters {
			if e.def.Platform == "qq" {
				return e.adapter
			}
		}
	}
	for _, e := range ania.adapters {
		if e.def.IDPrefix == "" {
			return e.adapter
		}
	}
	return ania.adapters[0].adapter
}

// AdapterStatuses 汇总各已启用适配器的连接状态（供 Web 面板展示）。
func (ania *AniaBot) AdapterStatuses() []adminpanel.AdapterStatus {
	out := make([]adminpanel.AdapterStatus, 0, len(ania.adapters))
	for _, e := range ania.adapters {
		st := adminpanel.AdapterStatus{Name: e.def.Name, Platform: e.def.Platform}
		type statuser interface{ AdapterStatus() (string, string) }
		if s, ok := e.adapter.(statuser); ok {
			st.State, st.Detail = s.AdapterStatus()
		} else {
			type connChecker interface{ Connected() bool }
			if c, ok := e.adapter.(connChecker); ok {
				if c.Connected() {
					st.State = "connected"
				} else {
					st.State = "reconnecting"
				}
			} else {
				st.State = "unknown"
			}
		}
		out = append(out, st)
	}
	return out
}

func (ania *AniaBot) Run() {
	ania.startTime = time.Now()
	sort.SliceStable(ania.plugins, func(i, j int) bool {
		return ania.plugins[i].GetMeta().Order < ania.plugins[j].GetMeta().Order
	})

	// 持久化存储（重启不丢失；配置中心的载体，必须最先初始化。
	// 驱动与位置由环境变量引导：ANIABOT_STORE_DRIVER / ANIABOT_SQLITE_PATH / ANIABOT_MYSQL_DSN）
	if ania.persistent == nil {
		store, err := newPersistentStorage(context.Background(), Logger().WithGroup("Persistent"))
		if err != nil {
			Logger().Error("初始化持久化存储失败", "error", err)
			os.Exit(1)
		}
		ania.persistent = store
	}

	// 旧版 QQ ID 为裸数字，升级后统一迁移到 qq: 前缀。
	// 必须在配置中心读取前执行，保证 admin_id、插件配置和 AI 持久化数据
	// 都以新 ID 格式加载。
	if err := migrateQQIDPrefix(context.Background(), ania.persistent, Logger().WithGroup("Migration")); err != nil {
		Logger().Error("QQ ID 前缀迁移失败，部分旧数据可能仍是无前缀格式", "error", err)
	}

	// 操作日志（面板「操作日志」页数据源）：记录面板与 AI 工具的管理操作，
	// 独立 __oplog 命名空间，SQL 后端走 ania_op_log 行级存储。
	oplog.Init(ania.persistent, 20000, Logger().WithGroup("oplog"))

	// 收集配置字段元信息（框架 + 各平台适配器 + 各插件的 ConfigRegistrar /
	// ConfigSchemaProvider 声明），面板表单基于该注册表动态渲染。
	pluginconfig.Register(frameworkConfigFields...)
	for _, d := range adapter.Definitions() {
		pluginconfig.Register(d.ConfigFields...)
	}
	var schemas []struct {
		name   string
		schema any
	}
	for _, p := range ania.plugins {
		if r, ok := p.(plugin.ConfigRegistrar); ok {
			pluginconfig.Register(r.ConfigFields()...)
		}
		if sp, ok := p.(plugin.ConfigSchemaProvider); ok {
			if schema := sp.ConfigSchema(); schema != nil {
				if err := pluginconfig.RegisterStruct(schema); err != nil {
					Logger().Error("注册插件配置结构体失败", "plugin", p.GetMeta().Name, "error", err)
				} else {
					schemas = append(schemas, struct {
						name   string
						schema any
					}{p.GetMeta().Name, schema})
				}
			}
		}
	}

	// 配置中心：全部配置存于数据库（首启写入默认值并进入设置向导）。
	if ania.cfg == nil {
		cs := configstore.New(ania.persistent, Logger().WithGroup("Config"))
		if err := cs.Init(); err != nil {
			Logger().Error("初始化配置中心失败", "error", err)
			os.Exit(1)
		}
		ania.configStore = cs

		// 补齐缺失的默认值后再构建 viper，保证插件 Start 读到的配置已含默认值。
		cs.EnsureDefaults(pluginconfig.Defaults())
		ania.cfg = cs.ToViper()
	}

	// 适配器：按注册表创建（各平台包 init() 中 adapter.Register，以
	// bot.platform.<name>.enable 开关启用）。创建早于插件 Start，
	// 保证 setup_pending 期间插件调用发送接口时适配器已就绪。
	for _, d := range adapter.Definitions() {
		if !ania.cfg.GetBool("bot.platform." + d.Name + ".enable") {
			continue
		}
		a, err := d.New(ania.cfg)
		if err != nil {
			Logger().Error("创建适配器失败，已跳过该平台", "platform", d.Name, "error", err)
			continue
		}
		ania.addAdapter(d, a)
		Logger().Info("已启用平台适配器", "platform", d.Name)
	}
	if len(ania.adapters) == 0 {
		Logger().Error("未启用任何平台适配器：请在 Web 面板的「平台适配器」配置中启用至少一个平台")
		os.Exit(1)
	}

	// 自动初始化：把配置填充进各插件声明的配置结构体（Start 之前完成）。
	for _, e := range schemas {
		if err := pluginconfig.Load(ania.cfg, e.schema); err != nil {
			Logger().Error("加载插件配置失败", "plugin", e.name, "error", err)
		}
	}

	// 缓存存储（易失，支持 TTL/列表；默认 memory，可配置 redis）
	if ania.storage == nil {
		store, err := newCacheStorage(context.Background(), ania.cfg, Logger().WithGroup("Cache"))
		if err != nil {
			Logger().Error("初始化缓存存储失败", "error", err)
			os.Exit(1)
		}
		ania.storage = store
	}

	// resty
	if ania.restyClient == nil {
		ania.restyClient = resty.New()
	}

	// 初始化事件
	Logger().Info("开始初始化插件...")
	for _, p := range ania.plugins {
		Logger().Info("初始化插件: ", "name", p.GetMeta().Name)
		safeExecute("初始化", p, func(p plugin.Plugin) {
			// DI
			encodeName := base64.StdEncoding.EncodeToString([]byte(p.GetMeta().Name))
			p.SetStorage(ania.storage.Clone(encodeName))
			p.SetPersistentStorage(ania.persistent.Clone(encodeName))
			p.SetRestyClient(ania.restyClient)
			p.SetLogger(Logger().WithGroup(p.GetMeta().Name))
			p.SetConfig(plugin.SystemConfig{
				AdminId: message.FromString(ania.cfg.GetString("bot.admin_id")),
			})
			// 配置中心读写能力（AI 配置管理工具等场景）；持久化存储不可用时保持 nil
			if ania.configStore != nil {
				p.SetConfigEditor(ania.configStore)
			}

			// start
			startCtx, cancel := context.WithTimeout(ania.ctx, StartEventTimeout)
			err := p.Start(startCtx, ania.cfg)
			logError(err, p, "初始化")
			cancel()
		})
	}

	// 插件已完成 Start（含设置向导未完成、跳过 Awake 的场景），此后 Stop
	// 或进程重启才会触发插件卸载钩子；注册重启前回调，让 /reboot、面板重启、
	// 自动更新与插件市场流水线的进程重启同样先执行卸载清理。
	ania.pluginsStarted.Store(true)
	sysrestart.OnPreRestart(func() { ania.unloadPlugins(plugin.UnloadShutdown) })

	// 收集实现了 agenthook.Handler 的插件（AI 代理生命周期 Go 钩子），注入给实现
	// agenthook.HandlerRegistry 的插件（AI 对话插件）——与 startAdminPanel 的
	// 「可选接口 + 类型断言」source 收集同款惯例
	var hookHandlers []agenthook.Handler
	for _, p := range ania.plugins {
		if h, ok := p.(agenthook.Handler); ok {
			hookHandlers = append(hookHandlers, h)
		}
	}
	if len(hookHandlers) > 0 {
		for _, p := range ania.plugins {
			if r, ok := p.(agenthook.HandlerRegistry); ok {
				r.SetGoHookHandlers(hookHandlers)
			}
		}
		Logger().Info("已注入 AI 代理 Go 钩子", "handlers", len(hookHandlers))
	}

	// 初始化cron
	c := cron.New()
	Logger().Info("开始初始化cron...")
	for _, p := range ania.plugins {
		safeExecute("初始化cron", p, func(p plugin.Plugin) {
			startCtx, cancel := context.WithTimeout(ania.ctx, StartCronEventTimeout)
			err := p.StartCron(startCtx, ania, c)
			logError(err, p, "初始化cron")
			cancel()
		})
	}
	Logger().Info("初始化cron完成")
	c.Start()
	defer c.Stop()

	fmt.Println(LogoASCII)
	Logger().Info("Bot启动完成...")
	oplog.Record(oplog.CategorySystem, "start", "AniaBot 启动完成")

	// 首次启动（设置向导未完成）时适配器不会连接，跳过 Awake：
	// 插件的 Awake 多依赖连接，此时触发只会发送失败；
	// 向导完成重启后会正常执行。
	setupPending := ania.configStore != nil && ania.configStore.SetupPending()
	if !setupPending {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		go func() {
			<-timer.C
			Logger().Info("Awake...")
			for _, p := range ania.plugins {
				safeExecute("Awake", p, func(p plugin.Plugin) {
					awakeCtx, cancel := context.WithTimeout(ania.ctx, AwakeEventTimeout)
					err := p.Awake(awakeCtx, ania)
					logError(err, p, "Awake")
					cancel()
				})
			}
		}()
	}

	// Web 控制面板（配置修改重启后生效）
	if ania.configStore != nil && ania.cfg.GetBool("bot.admin_panel.enable") {
		ania.startAdminPanel()
	}

	// 首次启动（设置向导未完成）时不连接适配器：保持面板可访问，
	// 等用户在向导中填写连接配置并重启后再连接，避免控制台刷重连日志。
	if setupPending {
		listen := ania.cfg.GetString("bot.admin_panel.listen")
		if listen == "" {
			listen = "127.0.0.1:7700"
		}
		Logger().Info("首次启动：暂不连接平台适配器，请在 Web 控制面板完成设置向导（完成后重启 Bot 生效）", "url", "http://"+listen)
		<-ania.ctx.Done()
		return
	}

	// 启动全部平台适配器（各自在 goroutine 中运行连接循环）
	ania.adapterStarted.Store(true)
	for _, e := range ania.adapters {
		go func(a adapter.Adapter) {
			defer func() {
				if err := recover(); err != nil {
					Logger().Error("适配器运行异常", "platform", a.Name(), "error", err)
				}
			}()
			a.Serve(ania.cfg)
		}(e.adapter)
	}
	<-ania.ctx.Done()
}

// startAdminPanel 启动 Web 控制面板（goroutine 内运行 HTTP 服务）。
func (ania *AniaBot) startAdminPanel() {
	// 查找提供定时任务执行日志的插件（如 AI 对话插件的 clock 功能）
	var taskLogFn func(f tasklog.Filter) []tasklog.Entry
	var clockSrc adminpanel.ClockTaskSource
	var msgLogFn func(limit int, beforeID uint64) []msglog.Entry
	var skillSrc adminpanel.SkillSource
	var memorySrc adminpanel.MemorySource
	var kbSrc adminpanel.KnowledgeBaseSource
	var teamSrc adminpanel.TeamSource
	var quotaSrc adminpanel.QuotaSource
	var queryLogFn func(f querylog.Filter) []querylog.Entry
	for _, p := range ania.plugins {
		if src, ok := p.(adminpanel.TaskLogSource); ok {
			taskLogFn = src.TaskLogQuery
		}
		if src, ok := p.(adminpanel.ClockTaskSource); ok {
			clockSrc = src
		}
		if src, ok := p.(adminpanel.MsgLogSource); ok {
			msgLogFn = src.MsgLogPage
		}
		if src, ok := p.(adminpanel.SkillSource); ok {
			skillSrc = src
		}
		if src, ok := p.(adminpanel.MemorySource); ok {
			memorySrc = src
		}
		if src, ok := p.(adminpanel.KnowledgeBaseSource); ok {
			kbSrc = src
		}
		if src, ok := p.(adminpanel.TeamSource); ok {
			teamSrc = src
		}
		if src, ok := p.(adminpanel.QuotaSource); ok {
			quotaSrc = src
		}
		if src, ok := p.(adminpanel.QueryLogSource); ok {
			queryLogFn = src.QueryLogRecent
		}
		if taskLogFn != nil && clockSrc != nil && msgLogFn != nil && skillSrc != nil && memorySrc != nil && kbSrc != nil && teamSrc != nil && quotaSrc != nil && queryLogFn != nil {
			break
		}
	}

	// 面板通讯录来源（群/好友列表）：收集所有实现 adapter.ContactsExt 的适配器，
	// 支持多平台并列展示；无枚举 API 的平台（Telegram、QQ 官方）自然缺席。
	contacts := make([]adminpanel.ContactSource, 0, len(ania.adapters))
	// 面板扫码登录来源（如微信）：收集所有实现 adminpanel.QRLoginSource 的适配器
	qrLogins := make([]adminpanel.QRLoginChannel, 0, len(ania.adapters))
	for _, e := range ania.adapters {
		if ce, ok := e.adapter.(adapter.ContactsExt); ok {
			contacts = append(contacts, adminpanel.ContactSource{
				Name:     e.def.Name,
				Platform: e.def.Platform,
				Contacts: ce,
			})
		}
		if qs, ok := e.adapter.(adminpanel.QRLoginSource); ok {
			qrLogins = append(qrLogins, adminpanel.QRLoginChannel{
				Name:     e.def.Name,
				Platform: e.def.Platform,
				Source:   qs,
			})
		}
	}

	srv := adminpanel.NewServer(adminpanel.Options{
		Listen:     ania.cfg.GetString("bot.admin_panel.listen"),
		Config:     ania.configStore,
		Persistent: ania.persistent,
		Bot:        ania,
		Contacts:   contacts,
		Adapter: func() string {
			if ania.configStore != nil && ania.configStore.SetupPending() {
				return "setup_pending"
			}
			if !ania.adapterStarted.Load() {
				return "not_started"
			}
			statuses := ania.AdapterStatuses()
			if len(statuses) == 0 {
				return "unknown"
			}
			// 任一已连接即视为 connected
			for _, st := range statuses {
				if st.State == "connected" {
					return "connected"
				}
			}
			return "reconnecting"
		},
		AdapterDetail: func() string {
			statuses := ania.AdapterStatuses()
			if len(statuses) == 0 {
				return ""
			}
			parts := make([]string, 0, len(statuses))
			for _, st := range statuses {
				parts = append(parts, fmt.Sprintf("%s:%s", st.Platform, st.State))
			}
			return strings.Join(parts, " ")
		},
		AdapterStatuses: ania.AdapterStatuses,
		TaskLogs:        taskLogFn,
		Clocks:          clockSrc,
		MsgLogs:         msgLogFn,
		Skills:          skillSrc,
		Memories:        memorySrc,
		Knowledge:       kbSrc,
		Teams:           teamSrc,
		Quota:           quotaSrc,
		QueryLogs:       queryLogFn,
		ConsoleLogs:     consollog.Page,
		QRLogins:        qrLogins,
		Marketplace:     marketplace.New(ania.configStore, Logger().WithGroup("Marketplace"), marketplace.WithUnloader(ania)),
		Logger:          Logger().WithGroup("AdminPanel"),
	})
	go srv.Run()
}

// GoroutineNum 返回当前由框架追踪的 goroutine 数目。
func (ania *AniaBot) GoroutineNum() int32 {
	return ania.goroutineNum.Load()
}

// StartTime 返回 Bot 启动时间。
func (ania *AniaBot) StartTime() time.Time {
	return ania.startTime
}

func (ania *AniaBot) GetPluginList() []plugininfo.PluginInfo {
	pluginList := make([]plugininfo.PluginInfo, 0, len(ania.plugins))
	for _, p := range ania.plugins {
		pluginList = append(pluginList, plugininfo.PluginInfo{
			Name:      p.GetMeta().Name,
			HelpWords: p.GetMeta().HelpWords,
			AdminOnly: p.GetMeta().AdminOnly,
			ShowFor:   p.GetMeta().ShowFor,
			Author:    p.GetMeta().Author,
			Version:   p.GetMeta().Version,
		})
	}
	return pluginList
}

// supportsPlatform 插件是否声明支持指定平台。
func (ania *AniaBot) supportsPlatform(p plugin.Plugin, platform string) bool {
	return p.GetMeta().SupportsPlatform(platform)
}

// fillSelfID 事件自身未携带 self_id（如飞书首次被 @ 前的空窗期）时用适配器兜底填充，
// 使自消息过滤与 @ 提及检测（utils.HasMention/ExtraMessageStr 比较 msg.SelfId）生效。
func (ania *AniaBot) fillSelfID(e *adapterEntry, msg *message.Message) {
	if msg.SelfId != "" {
		return
	}
	if p, ok := e.adapter.(adapter.SelfIDProvider); ok {
		msg.SelfId = p.SelfID()
	}
}

// segWarn 段类型不受支持告警的计数节流（key: platform|segment，第 1 次与每 100 次告警）。
var segWarn sync.Map

// checkSegmentSupport 适配器声明了 SupportedSegments 时，对不支持的段类型计数告警
// （替代适配器出站静默丢弃，仅告警不阻断）。
func (ania *AniaBot) checkSegmentSupport(a adapter.Adapter, segs []message.OB11Segment) {
	ss, ok := a.(adapter.SegmentSupport)
	if !ok || len(segs) == 0 {
		return
	}
	supported := make(map[string]bool, len(ss.SupportedSegments()))
	for _, t := range ss.SupportedSegments() {
		supported[t] = true
	}
	for _, seg := range segs {
		if supported[seg.Type] {
			continue
		}
		key := a.Platform() + "|" + seg.Type
		v, _ := segWarn.LoadOrStore(key, &atomic.Int64{})
		n := v.(*atomic.Int64).Add(1)
		if n == 1 || n%100 == 0 {
			Logger().Warn("消息段不受当前平台支持，将被忽略", "platform", a.Platform(), "segment", seg.Type, "times", n)
		}
	}
}

func (ania *AniaBot) onGroupEvent(e *adapterEntry, msg message.Message) {
	defer func() {
		if err := recover(); err != nil {
			Logger().Error("群聊消息事件触发错误: ", err)
		}
	}()
	ania.fillSelfID(e, &msg)
	if msg.Sender.UserId == msg.SelfId {
		return
	}
	// 事件幂等去重：at-least-once 投递的平台重推同一事件时跳过（见 dedup.go）
	if key, ok := ania.messageDedupKey(e, msg); ok && !ania.tryClaimEvent(key) {
		return
	}

	cmd := utils.ParseCommand(msg)

	for _, p := range ania.plugins {
		if !ania.supportsPlatform(p, e.def.Platform) {
			continue
		}
		next, panicked := safeExecuteWithReturn("群聊消息事件", p, func(p plugin.Plugin) bool {
			msgCtx, cancel := context.WithTimeout(ania.ctx, ania.msgEventTimeout())
			next, err := p.OnGroupMsg(msgCtx, e.evBot, cmd, msg)
			logError(err, p, "群聊消息事件")
			cancel()
			return next
		})
		if panicked {
			next = true // 插件 panic 不应阻断后续插件，继续传播（与通知事件一致）
		}
		if !next {
			break
		}
	}
}

func (ania *AniaBot) onFriendEvent(e *adapterEntry, msg message.Message) {
	defer func() {
		if err := recover(); err != nil {
			Logger().Error("私聊消息事件触发错误: ", err)
		}
	}()
	ania.fillSelfID(e, &msg)
	if msg.Sender.UserId == msg.SelfId {
		return
	}
	// 事件幂等去重：at-least-once 投递的平台重推同一事件时跳过（见 dedup.go）
	if key, ok := ania.messageDedupKey(e, msg); ok && !ania.tryClaimEvent(key) {
		return
	}

	cmd := utils.ParseCommand(msg)

	for _, p := range ania.plugins {
		if !ania.supportsPlatform(p, e.def.Platform) {
			continue
		}
		next, panicked := safeExecuteWithReturn("私聊消息事件", p, func(p plugin.Plugin) bool {
			msgCtx, cancel := context.WithTimeout(ania.ctx, ania.msgEventTimeout())
			next, err := p.OnFriendMsg(msgCtx, e.evBot, cmd, msg)
			logError(err, p, "私聊消息事件")
			cancel()
			return next
		})
		if panicked {
			next = true // 插件 panic 不应阻断后续插件，继续传播（与通知事件一致）
		}
		if !next {
			break
		}
	}
}

func (ania *AniaBot) AddPlugin(plugins ...plugin.Plugin) {
	for _, p := range plugins {
		meta := p.GetMeta()
		if meta.ShowFor == 1 {
			panic("插件必须指定ShowFor")
		}
		if _, ok := ania.pluginSet[meta.Name]; ok {
			panic("插件名称相同，请检查插件是否重复加载")
		}
		ania.pluginSet[meta.Name] = struct{}{}
		ania.plugins = append(ania.plugins, p)
		Logger().Info("已添加插件: ", "name", p.GetMeta().Name)
	}
}

// Stop 停止 Bot：先调用插件的卸载钩子（reason=UnloadShutdown），
// 再取消运行上下文使 Run() 返回。进程重启路径（sysrestart）由注册的
// 重启前回调触发同样的清理。
func (ania *AniaBot) Stop() {
	ania.unloadPlugins(plugin.UnloadShutdown)
	ania.cancel()
}

// SendGroupMsg 发送群聊消息（按群 ID 前缀路由到对应平台适配器；
// 平台不支持内联按钮时先剥离 keyboard 段）。
func (ania *AniaBot) SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (msgId message.QID, success bool) {
	a := ania.route(groupId)
	if a == nil {
		return "", false
	}
	chain = msgchain.NewGroupChain(ania.stripUnsupportedKeyboard(a, chain.GetGroupMsg()))
	ania.checkSegmentSupport(a, chain.GetGroupMsg())
	return a.SendGroupMsg(groupId, chain)
}

// SendFriendMsg 发送私聊消息（按用户 ID 前缀路由到对应平台适配器；
// 平台不支持内联按钮时先剥离 keyboard 段）。
func (ania *AniaBot) SendFriendMsg(userID message.QID, chain msgchain.FriendChain) (msgId message.QID, success bool) {
	a := ania.route(userID)
	if a == nil {
		return "", false
	}
	chain = msgchain.NewFriendChain(ania.stripUnsupportedKeyboard(a, chain.GetFriendMsg()))
	ania.checkSegmentSupport(a, chain.GetFriendMsg())
	return a.SendFriendMsg(userID, chain)
}

// SendGroupStream 实现 bot.StreamSender：按群 ID 前缀路由，适配器不支持时返回 false。
func (ania *AniaBot) SendGroupStream(groupId message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	a := ania.route(groupId)
	if a == nil {
		return nil, false
	}
	if se, ok := a.(adapter.StreamSenderExt); ok {
		return se.SendGroupStream(groupId, chain)
	}
	return nil, false
}

// SendFriendStream 实现 bot.StreamSender：按用户 ID 前缀路由，适配器不支持时返回 false。
func (ania *AniaBot) SendFriendStream(userID message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	a := ania.route(userID)
	if a == nil {
		return nil, false
	}
	if se, ok := a.(adapter.StreamSenderExt); ok {
		return se.SendFriendStream(userID, chain)
	}
	return nil, false
}

// GetMsgDetail 获取消息详情（按消息 ID 前缀路由到对应平台适配器）。
func (ania *AniaBot) GetMsgDetail(msgId message.QID) (*message.Message, bool) {
	a := ania.route(msgId)
	if a == nil {
		return nil, false
	}
	return a.GetMsgDetail(msgId)
}

// GetGroupDetail 获取群聊详情（按群 ID 前缀路由到对应平台适配器）。
func (ania *AniaBot) GetGroupDetail(groupId message.QID) (*message.GroupInfo, bool) {
	a := ania.route(groupId)
	if a == nil {
		return nil, false
	}
	return a.GetGroupDetail(groupId)
}

// GetGroupMsgHistory 获取群聊消息历史（按群 ID 前缀路由到对应平台适配器）。
func (ania *AniaBot) GetGroupMsgHistory(groupId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	a := ania.route(groupId)
	if a == nil {
		return nil, false
	}
	return a.GetGroupMsgHistory(groupId, count, message_seq)
}

// GetFriendMsgHistory 获取私聊消息历史（按用户 ID 前缀路由到对应平台适配器）。
func (ania *AniaBot) GetFriendMsgHistory(userId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	a := ania.route(userId)
	if a == nil {
		return nil, false
	}
	return a.GetFriendMsgHistory(userId, count, message_seq)
}
