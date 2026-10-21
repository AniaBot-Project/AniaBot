package pluginaichat

// 主动互动（proactive）—— 意图队列实现。
//
// 与「一问一答」（纯反应式）和 AI 定时任务（开环：到点必发）不同，主动互动把
// 「想到要说」与「真的说」拆成两步：
//
//	触发源（冷场 / AI 自建 / 管理员手动）→ 意图（ania_intention 表）→ tick 调度
//	→ 闸门（时段 / 配额 / 间隔 / 冷却 / 现场活跃度）→ 一次性 ChatBot 生成
//	→ 发送 → 观察回应（replied / ignored）→ 连续被忽略进入冷却
//
// 关键设计：
//   - 意图有独立生命周期（due_at / expires_at / status），触发后不立即发，
//     而是等到一个「合适的时机」，过期作废，避免提起已经冷掉的话题；
//   - 闸门里既有硬规则（免打扰、每日上限、最小间隔），也有现场复核（有人
//     正在聊天就推迟、冷场意图期间有人说话就作废），再交给 AI 自己用
//     skip_output 决定「此刻没什么可说的」；
//   - 发送后进入回应观察窗口，无人回应记 ignored，连续被忽略触发冷却——
//     这是让机器人「越说越有分寸」的反馈环，而不是一堆写死的阈值。
//
// 默认关闭（plugin.ai_chat_bot.proactive.enable=false），开启后还需按会话
// 白名单启用（配置项 proactive.scopes，或管理员发送 /proactive on）。

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/utils"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
)

// 意图来源：决定闸门怎么判断「现场是否已经不需要这句话了」。
const (
	proactiveSourceIdle   = "idle"   // 冷场：会话安静超过配置时长，AI 自行找话题
	proactiveSourceAI     = "ai"     // AI 在对话中用 proactive_create 自建（跟进某件事）
	proactiveSourceManual = "manual" // 管理员 /proactive now 立即触发
)

// 意图状态。
const (
	proactiveStatusPending   = "pending"   // 等待到点
	proactiveStatusSent      = "sent"      // 已发送，等待观察回应
	proactiveStatusCancelled = "cancelled" // 主动取消（现场不再需要 / 免打扰 / 达上限 / AI 决定不说）
	proactiveStatusExpired   = "expired"   // 超过 expires_at 仍未发出
	proactiveStatusFailed    = "failed"    // 生成或发送失败且重试耗尽
)

// 回应结果（反馈环）。
const (
	proactiveOutcomeReplied = "replied" // 观察窗口内有人说话
	proactiveOutcomeIgnored = "ignored" // 观察窗口内无人回应
)

// 会话开关状态。
const (
	proactiveScopeOn  = "on"
	proactiveScopeOff = "off"
)

const (
	proactiveScopesKey = "plugin.ai_chat_bot.proactive.scopes"

	// proactiveDefaultTick 调度间隔：一分钟一轮，单轮开销只有几条 SQL。
	proactiveDefaultTick = time.Minute
	// proactiveTickBatch 单轮最多处理到期的意图数：防止一次积压把并发打满。
	proactiveTickBatch = 3
	// proactiveMinAfterBot 机器人刚说过话（普通回复或主动发言）后的静默期：
	// 人不会在刚说完一句之后又马上主动补一句。
	proactiveMinAfterBot = 5 * time.Minute
	// proactiveLiveWindow 现场活跃窗口：这段时间内有人说话就推迟发言。
	proactiveLiveWindow = 2 * time.Minute
	// proactiveJitterMax 冷场意图的随机抖动上限：固定时间点触发会显得机械。
	proactiveJitterMax = 3 * time.Minute
	// proactivePostponeStep 闸门推迟 / 会话忙时的默认顺延步长。
	proactivePostponeStep = 5 * time.Minute
	// proactiveMaxPendingScope 单会话待执行意图上限，防止刷屏式排队。
	proactiveMaxPendingScope = 5
	// proactiveMaxAttempts 单条意图的最大执行次数（失败重试）。
	proactiveMaxAttempts = 2
	// proactiveMaxPromptRunes 意图文本落库上限（会进上下文，不能无限长）。
	proactiveMaxPromptRunes = 2000
	// proactiveMaxReplyRunes 记录已发送内容的符文上限。
	proactiveMaxReplyRunes = 1000
	// proactiveMaxTextRunes 现场消息单条文本的截断长度。
	proactiveMaxTextRunes = 200
	// proactiveRetention 历史意图保留时长（待执行的不清理）。
	proactiveRetention = 90 * 24 * time.Hour
	// proactivePruneEvery 清理历史意图的间隔。
	proactivePruneEvery = 6 * time.Hour
)

// ProactiveIntention 一条「主动发言意图」。
//
// ID 为 8 位十六进制随机串（与记忆条目同规格），scope 与长期记忆 / 知识库
// 使用同一套命名（g:会话ID / f:用户ID），群与私聊互不串扰。
type ProactiveIntention struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Source    string    `json:"source"`
	Prompt    string    `json:"prompt"` // 到点后要做什么 / 说什么；冷场意图为空，由执行时现场生成
	Note      string    `json:"note,omitempty"`
	Priority  int       `json:"priority"`
	DueAt     time.Time `json:"due_at"`     // 最早可执行时间
	ExpiresAt time.Time `json:"expires_at"` // 超过该时间作废
	Status    string    `json:"status"`
	CreatedBy string    `json:"created_by,omitempty"` // ai / system / 用户 ID
	CreatedAt time.Time `json:"created_at"`
	SentAt    time.Time `json:"sent_at"`
	Outcome   string    `json:"outcome,omitempty"`
	OutcomeAt time.Time `json:"outcome_at"`
	Reason    string    `json:"reason,omitempty"` // 闸门拒绝 / 顺延 / AI 跳过 / 失败原因
	Reply     string    `json:"reply,omitempty"`  // 实际发出的内容（截断）
	Attempts  int       `json:"attempts"`
}

// observedMessage 现场观察到的一条消息（仅保留最近若干条）。
type observedMessage struct {
	At     time.Time
	Sender string
	Text   string
}

// scopeActivity 一个会话的现场状态。
//
// 只对「已启用主动互动」的会话记录：未启用的会话不占用内存，也不会因为
// 看着别人聊天而产生行为。进程重启后清空——重启本身就该视为一次断档，
// 从当下重新计时，而不是一启动就对着几个小时前的冷场发言。
type scopeActivity struct {
	Scope       string
	ID          message.QID
	IsGroup     bool
	LastHumanAt time.Time
	LastBotAt   time.Time
	Messages    []observedMessage
}

// contextBlock 把最近消息拼成给 AI 看的现场记录。
func (a *scopeActivity) contextBlock() string {
	if a == nil || len(a.Messages) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, m := range a.Messages {
		sb.WriteString("[")
		sb.WriteString(m.At.Format("15:04"))
		sb.WriteString("] ")
		sb.WriteString(m.Sender)
		sb.WriteString(": ")
		sb.WriteString(m.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// scopeSnapshot 现场的只读快照（带锁读取时拷贝）。
type scopeSnapshot struct {
	LastHumanAt time.Time
	LastBotAt   time.Time
	Context     string
}

// proactiveManager 主动互动调度器：意图持久化 + tick 调度 + 闸门 + 执行 + 反馈。
//
// 生命周期与插件一致：Start 时构造（配置开启且 SQL 可用），Awake 拿到 bot 后
// 启动 tick goroutine；插件没有 Stop 钩子，goroutine 与进程同生命周期
// （与 clockManager 自带 cron、chatJanitor 的惯例一致）。
type proactiveManager struct {
	plugin *AIChatPlugin
	logger *slog.Logger
	store  *intentionStore

	idle        time.Duration // 冷场阈值，<=0 表示不按冷场触发
	minGap      time.Duration // 两次主动发言的最小间隔
	quietOn     bool
	quietStart  int // 免打扰起始（当日分钟数）
	quietEnd    int // 免打扰结束（当日分钟数），小于起始表示跨午夜
	quietSpec   string
	dailyLimit  int // 单会话每日上限，<=0 不限
	globalDaily int // 全局每日上限，<=0 不限
	expire      time.Duration
	outcomeWin  time.Duration
	cooldown    time.Duration
	strikes     int
	timeout     time.Duration
	tick        time.Duration
	maxContext  int
	baseScopes  map[string]struct{}

	// opMu 串行化会话开关的读-改-写（不持 m.mu，避免配置中心 I/O 阻塞消息处理）
	opMu     sync.Mutex
	mu       sync.Mutex
	scopes   map[string]*scopeActivity
	lastIdle map[string]time.Time // scope -> 已为其生成过冷场意图的 lastHumanAt
	running  map[string]struct{}  // 正在执行的意图 ID
	awaiting map[string]struct{}  // 有待观察回应的 scope

	bot  bot.Bot
	stop chan struct{}

	// lastPrune 上次清理历史意图的时间（仅 tick goroutine 读写）
	lastPrune time.Time
}

// newProactiveManager 按配置构造调度器；关闭或 SQL 不可用时返回 nil。
func newProactiveManager(p *AIChatPlugin) *proactiveManager {
	cfg := p.cfg.Proactive
	if !cfg.Enable {
		return nil
	}
	db, dialect, ok := storage.SQLBackend(p.PersistentStorage)
	if !ok {
		p.Logger.Error("主动互动需要 SQL 持久化后端（sqlite/mysql），当前后端不支持，功能禁用")
		return nil
	}
	logger := p.Logger.WithGroup("proactive")
	store, err := newIntentionStore(db, dialect, logger)
	if err != nil {
		p.Logger.Error("初始化主动互动失败（建表失败），功能禁用", "error", err.Error())
		return nil
	}

	m := &proactiveManager{
		plugin:      p,
		logger:      logger,
		store:       store,
		scopes:      map[string]*scopeActivity{},
		lastIdle:    map[string]time.Time{},
		running:     map[string]struct{}{},
		awaiting:    map[string]struct{}{},
		baseScopes:  map[string]struct{}{},
		stop:        make(chan struct{}),
		tick:        proactiveDefaultTick,
		idle:        time.Duration(cfg.IdleMinutes) * time.Minute,
		minGap:      time.Duration(cfg.MinGapMinutes) * time.Minute,
		dailyLimit:  cfg.DailyLimit,
		globalDaily: cfg.GlobalDailyLimit,
		expire:      time.Duration(cfg.ExpireMinutes) * time.Minute,
		outcomeWin:  time.Duration(cfg.OutcomeMinutes) * time.Minute,
		cooldown:    time.Duration(cfg.CooldownMinutes) * time.Minute,
		strikes:     cfg.StrikeLimit,
		timeout:     time.Duration(cfg.TimeoutSec) * time.Second,
		maxContext:  cfg.MaxContextMessages,
	}
	if m.idle < 0 {
		m.idle = 0
	}
	if m.minGap < 0 {
		m.minGap = 0
	}
	if m.expire <= 0 {
		m.expire = 30 * time.Minute
	}
	if m.outcomeWin <= 0 {
		m.outcomeWin = 10 * time.Minute
	}
	if m.cooldown <= 0 {
		m.cooldown = 6 * time.Hour
	}
	if m.strikes < 0 {
		m.strikes = 0
	}
	if m.timeout <= 0 {
		m.timeout = 120 * time.Second
	}
	if m.maxContext <= 0 {
		m.maxContext = 30
	}
	if start, end, specOK := parseQuietHours(cfg.QuietHours); specOK {
		m.quietOn, m.quietStart, m.quietEnd, m.quietSpec = true, start, end, cfg.QuietHours
	} else if strings.TrimSpace(cfg.QuietHours) != "" {
		p.Logger.Warn("免打扰时段格式无法解析，按不启用处理", "value", cfg.QuietHours)
	}

	// 会话白名单：格式 g:会话ID / f:用户ID（与记忆、知识库的 scope 一致）。
	// 运行时以内存集合为准，/proactive on|off 会同步回写配置中心（见 setScopeEnabled）。
	scopes := normalizeScopes(cfg.Scopes, p.Logger)
	m.baseScopes = make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		m.baseScopes[scope] = struct{}{}
	}
	p.cfg.Proactive.Scopes = scopes

	m.logger.Info("已启用主动互动",
		"scopes", len(m.baseScopes),
		"idle_minutes", int(m.idle.Minutes()),
		"min_gap_minutes", int(m.minGap.Minutes()),
		"daily_limit", m.dailyLimit,
		"quiet_hours", m.quietSpec)
	return m
}

// Start 保存发送用的 bot 并启动调度循环。
func (m *proactiveManager) Start(b bot.Bot) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.bot = b
	m.mu.Unlock()

	// 恢复重启前尚在观察窗口内的会话，避免重启后漏记「有人回应」
	m.refreshAwaiting()

	go func() {
		ticker := time.NewTicker(m.tick)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.tickOnce()
			}
		}
	}()
	m.logger.Info("主动互动调度已启动", "tick", m.tick.String())
}

// tickOnce 执行一轮调度：结算观察窗口 → 生成冷场意图 → 处理到期意图。
// 任何一步失败都只记日志，绝不影响消息主流程。
func (m *proactiveManager) tickOnce() {
	if m == nil || m.plugin == nil || !m.plugin.cfg.Proactive.Enable {
		return
	}
	now := time.Now()
	m.store.expireStale(now)
	m.settleOutcomes(now)
	m.considerIdle(now)
	m.runDue(now)

	// 定期清理历史意图，防止表无限增长（待执行的意图不受影响）
	if now.Sub(m.lastPrune) > proactivePruneEvery {
		m.lastPrune = now
		if n := m.store.prune(now.Add(-proactiveRetention)); n > 0 {
			m.logger.Info("清理历史主动互动记录", "count", n)
		}
	}
}

// settleOutcomes 结算观察窗口：发送后超过窗口仍无人回应，记为被忽略。
func (m *proactiveManager) settleOutcomes(now time.Time) {
	n := m.store.markIgnored(now.Add(-m.outcomeWin))
	if n <= 0 {
		return
	}
	m.logger.Info("主动发言在观察窗口内无人回应", "count", n, "window", m.outcomeWin.String())
	m.refreshAwaiting()
}

// refreshAwaiting 从存储重新加载「仍在观察窗口内」的会话集合。
func (m *proactiveManager) refreshAwaiting() {
	scopes := m.store.awaitingScopes(time.Now().Add(-m.outcomeWin))
	m.mu.Lock()
	m.awaiting = make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		m.awaiting[s] = struct{}{}
	}
	m.mu.Unlock()
}

// ---- 会话开关 ----

// enabled 判断某会话是否允许主动互动。
func (m *proactiveManager) enabled(scope string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.baseScopes[scope]
	return ok
}

// scopeState 返回会话开关状态（on / off），供指令与状态展示使用。
func (m *proactiveManager) scopeState(scope string) string {
	if m.enabled(scope) {
		return proactiveScopeOn
	}
	return proactiveScopeOff
}

// enabledScopes 返回当前允许主动互动的全部会话（排序后）。
func (m *proactiveManager) enabledScopes() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.baseScopes))
	for s := range m.baseScopes {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// setScopeEnabled 开启 / 关闭某会话的主动互动。
//
// 直接改写配置项 plugin.ai_chat_bot.proactive.scopes（控制面板即时可见、重启后
// 保留），同时更新内存里的实时集合——两者保持一致，不再有「藏在 KV 里的第二份
// 名单」。改写前先读一次配置中心再合并，避免覆盖掉管理员刚在面板里手动改过的
// 其他会话。返回是否发生变更、以及是否成功落盘（配置中心不可用时只对本次运行生效）。
func (m *proactiveManager) setScopeEnabled(scope string, on bool) (changed, persisted bool) {
	if m == nil || scope == "" {
		return false, false
	}
	if _, _, ok := parseScope(scope); !ok {
		return false, false
	}

	// 读-改-写需要串行化（同一时刻可能有两个管理员同时在操作）
	m.opMu.Lock()
	defer m.opMu.Unlock()

	list := m.currentScopes()
	if containsString(list, scope) == on {
		return false, true
	}
	if on {
		list = append(list, scope)
	} else {
		list = removeString(list, scope)
	}
	list = normalizeScopes(list, m.logger)
	m.applyScopes(list)
	persisted = m.persistScopes(list)

	if !on {
		// 关闭时丢弃该会话的现场记录，重新开启后从新的一轮现场开始计时
		m.mu.Lock()
		delete(m.scopes, scope)
		delete(m.lastIdle, scope)
		m.mu.Unlock()
	}
	return true, persisted
}

// reloadScopes 从配置中心重新读取白名单（面板改完不必重启）。
func (m *proactiveManager) reloadScopes() (int, bool) {
	if m == nil || m.plugin == nil || m.plugin.ConfigEditor == nil {
		return 0, false
	}
	raw, ok := m.plugin.ConfigEditor.Get(proactiveScopesKey)
	if !ok {
		raw = []string{}
	}
	list := normalizeScopes(toStringSlice(raw), m.logger)
	m.applyScopes(list)
	return len(list), true
}

// currentScopes 读取当前生效的白名单：优先配置中心（面板可能刚改过），
// 配置中心不可用时退回到内存集合。
func (m *proactiveManager) currentScopes() []string {
	if m.plugin != nil && m.plugin.ConfigEditor != nil {
		raw, ok := m.plugin.ConfigEditor.Get(proactiveScopesKey)
		if !ok {
			return nil
		}
		return toStringSlice(raw)
	}
	return m.enabledScopes()
}

// applyScopes 用新名单替换内存集合，并同步插件配置结构体（与其他插件
// 「改运行时状态 + 回写配置」的惯例一致）。
func (m *proactiveManager) applyScopes(list []string) {
	set := make(map[string]struct{}, len(list))
	for _, scope := range list {
		set[scope] = struct{}{}
	}
	m.mu.Lock()
	m.baseScopes = set
	m.mu.Unlock()
	if m.plugin != nil {
		m.plugin.cfg.Proactive.Scopes = append([]string(nil), list...)
	}
}

// persistScopes 把名单写回配置中心。
func (m *proactiveManager) persistScopes(list []string) bool {
	if m.plugin == nil || m.plugin.ConfigEditor == nil {
		m.logger.Warn("配置中心不可用，主动互动会话名单仅本次运行生效")
		return false
	}
	if list == nil {
		list = []string{}
	}
	if err := m.plugin.ConfigEditor.Set(proactiveScopesKey, list); err != nil {
		m.logger.Error("回写主动互动会话名单失败", "error", err.Error())
		return false
	}
	return true
}

// normalizeScopes 规范化名单：去空白、去重、过滤非法项并按字典序排序。
func normalizeScopes(list []string, logger *slog.Logger) []string {
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, raw := range list {
		scope := strings.TrimSpace(raw)
		if scope == "" {
			continue
		}
		if _, _, ok := parseScope(scope); !ok {
			if logger != nil {
				logger.Warn("主动互动会话名单条目格式非法，已跳过（应为 g:会话ID 或 f:用户ID）", "value", raw)
			}
			continue
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

// toStringSlice 把配置中心返回的 JSON 数组（[]any / []string）转成字符串切片。
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), val...)
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(val) == "" {
			return nil
		}
		return []string{val}
	default:
		return nil
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func removeString(list []string, want string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s != want {
			out = append(out, s)
		}
	}
	return out
}

// ---- 现场记录 ----

// noteMessage 记录一条来自用户的消息（仅启用的会话）。
//
// 由 OnGroupMsg / OnFriendMsg 在最早的位置调用：机器人本来就收得到全部群消息，
// 这是最真实、零成本的「现场信号」。
func (m *proactiveManager) noteMessage(msg message.Message, isGroup bool) {
	if m == nil || m.plugin == nil || !m.plugin.cfg.Proactive.Enable {
		return
	}
	id := msg.GroupId
	if !isGroup {
		id = msg.Sender.UserId
	}
	if id == "" {
		return
	}
	scope := sessionKey(id, isGroup)
	if !m.enabled(scope) {
		return
	}
	text, _ := utils.ExtraMessageStr(msg)
	text = sanitizeLine(text)
	now := time.Now()

	m.mu.Lock()
	act := m.scopeLocked(scope, id, isGroup, now)
	act.LastHumanAt = now
	if text != "" {
		act.Messages = append(act.Messages, observedMessage{
			At:     now,
			Sender: senderLabel(msg.Sender),
			Text:   text,
		})
		if over := len(act.Messages) - m.maxContext; over > 0 {
			act.Messages = append([]observedMessage(nil), act.Messages[over:]...)
		}
	}
	_, watching := m.awaiting[scope]
	m.mu.Unlock()

	// 有人说话 = 上一次主动发言有人接：结算反馈环（仅在有观察中的意图时查库）
	if watching {
		if n := m.store.markReplied(scope, now.Add(-m.outcomeWin)); n > 0 {
			m.logger.Info("主动发言收到回应", "scope", scope, "count", n)
			m.refreshAwaiting()
		}
	}
}

// noteBotSpoke 记录机器人刚刚发过言（普通回复与主动发言都算）。
func (m *proactiveManager) noteBotSpoke(id message.QID, isGroup bool) {
	if m == nil || id == "" {
		return
	}
	scope := sessionKey(id, isGroup)
	if !m.enabled(scope) {
		return
	}
	now := time.Now()
	m.mu.Lock()
	act := m.scopeLocked(scope, id, isGroup, now)
	act.LastBotAt = now
	m.mu.Unlock()
}

// scopeLocked 返回会话现场（不存在则创建）。调用方需持有 m.mu。
// 新建时把 LastHumanAt 设为当前时间，保证重启后要等满一个冷场窗口才会触发。
func (m *proactiveManager) scopeLocked(scope string, id message.QID, isGroup bool, now time.Time) *scopeActivity {
	act, ok := m.scopes[scope]
	if ok {
		return act
	}
	act = &scopeActivity{Scope: scope, ID: id, IsGroup: isGroup, LastHumanAt: now}
	m.scopes[scope] = act
	return act
}

// snapshot 取现场快照；会话无记录时返回 nil（视为「还没开始观察」）。
func (m *proactiveManager) snapshot(scope string) *scopeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	act, ok := m.scopes[scope]
	if !ok {
		return nil
	}
	return &scopeSnapshot{
		LastHumanAt: act.LastHumanAt,
		LastBotAt:   act.LastBotAt,
		Context:     act.contextBlock(),
	}
}

// ---- 冷场触发 ----

// considerIdle 为「安静了足够久」的会话生成一条冷场意图。
//
// 只负责「值不值得生成」：是否真的发得出去由闸门与 AI 自己判断。
// 同一段冷场只生成一次（以 lastHumanAt 为标记），有人说话后才会重新计。
func (m *proactiveManager) considerIdle(now time.Time) {
	if m.idle <= 0 {
		return
	}
	for _, scope := range m.enabledScopes() {
		snap := m.snapshot(scope)
		if snap == nil {
			// 该会话还没收到过消息：建立一个以当前时间为起点的现场，等满窗口再说
			m.ensureActivity(scope, now)
			continue
		}
		if snap.LastHumanAt.IsZero() || now.Sub(snap.LastHumanAt) < m.idle {
			continue
		}
		if m.idleQueued(scope, snap.LastHumanAt) {
			continue
		}
		// 已有待执行意图（AI 自建或上次未执行完）时不重复排队
		if pending := m.store.pendingByScope(scope); len(pending) > 0 {
			m.markIdleQueued(scope, snap.LastHumanAt)
			continue
		}
		m.markIdleQueued(scope, snap.LastHumanAt)
		m.queueIdle(scope, now)
	}
}

// ensureActivity 为尚未产生过消息的已启用会话建立现场记录。
func (m *proactiveManager) ensureActivity(scope string, now time.Time) {
	id, isGroup, ok := parseScope(scope)
	if !ok {
		return
	}
	m.mu.Lock()
	m.scopeLocked(scope, id, isGroup, now)
	m.mu.Unlock()
}

func (m *proactiveManager) idleQueued(scope string, lastHumanAt time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	last, ok := m.lastIdle[scope]
	return ok && last.Equal(lastHumanAt)
}

func (m *proactiveManager) markIdleQueued(scope string, lastHumanAt time.Time) {
	m.mu.Lock()
	m.lastIdle[scope] = lastHumanAt
	m.mu.Unlock()
}

// queueIdle 写入一条冷场意图。DueAt 带随机抖动：整点触发太像机器人。
func (m *proactiveManager) queueIdle(scope string, now time.Time) {
	it := &ProactiveIntention{
		ID:        newIntentionID(),
		Scope:     scope,
		Source:    proactiveSourceIdle,
		DueAt:     now.Add(jitter(proactiveJitterMax)),
		ExpiresAt: now.Add(m.expire),
		Status:    proactiveStatusPending,
		CreatedBy: "system",
		CreatedAt: now,
	}
	if !m.store.insert(it) {
		m.logger.Error("写入冷场意图失败", "scope", scope)
		return
	}
	m.logger.Info("已生成冷场意图", "scope", scope, "id", it.ID,
		"due", it.DueAt.Format("15:04:05"), "expire", it.ExpiresAt.Format("15:04:05"))
}

// ---- 到期执行 ----

// runDue 处理到期的待执行意图。
func (m *proactiveManager) runDue(now time.Time) {
	items := m.store.due(now, proactiveTickBatch)
	for i := range items {
		it := items[i]
		if m.isRunning(it.ID) {
			continue
		}
		res := m.gate(&it, now)
		switch res.action {
		case gateCancel:
			m.closeIntention(&it, proactiveStatusCancelled, res.reason)
		case gatePostpone:
			m.postpone(&it, now.Add(res.delay), res.reason)
		case gateAllow:
			m.launch(&it, false)
		}
	}
}

// gateAction 闸门动作。
type gateAction int

const (
	gateAllow gateAction = iota
	gatePostpone
	gateCancel
)

// gateResult 闸门判定结果。
type gateResult struct {
	action gateAction
	reason string
	delay  time.Duration
}

// gate 判定一条意图此刻能否执行。
//
// 顺序按「代价从低到高」：开关 → 免打扰 → 每日上限 → 最小间隔 → 冷却 →
// 现场活跃度。硬规则直接取消（不会好了），时间冲突类推迟（到点还可以再试）。
func (m *proactiveManager) gate(it *ProactiveIntention, now time.Time) gateResult {
	if !m.enabled(it.Scope) {
		return gateResult{action: gateCancel, reason: "会话未启用主动互动"}
	}

	// 免打扰：能等到时段结束就顺延，否则作废（半夜想到的话不必早上再说）
	if m.quietOn && inQuietRange(now, m.quietStart, m.quietEnd) {
		end := quietEndTime(now, m.quietEnd)
		if end.Before(it.ExpiresAt) {
			return gateResult{
				action: gatePostpone,
				reason: "免打扰时段，顺延到 " + end.Format("15:04"),
				delay:  end.Sub(now),
			}
		}
		return gateResult{action: gateCancel, reason: "免打扰时段内已过期"}
	}

	// 每日上限：单会话与全局各自独立
	dayStart := startOfDay(now)
	if m.dailyLimit > 0 && m.store.countSentSince(it.Scope, dayStart) >= m.dailyLimit {
		return gateResult{action: gateCancel, reason: fmt.Sprintf("本会话今日主动发言已达上限（%d 次）", m.dailyLimit)}
	}
	if m.globalDaily > 0 && m.store.countSentSince("", dayStart) >= m.globalDaily {
		return gateResult{action: gateCancel, reason: fmt.Sprintf("全局今日主动发言已达上限（%d 次）", m.globalDaily)}
	}

	// 最小间隔
	if m.minGap > 0 {
		if last, ok := m.store.lastSentAt(it.Scope); ok {
			if gap := m.minGap - now.Sub(last); gap > 0 {
				return gateResult{action: gatePostpone, reason: "距上次主动发言时间太近", delay: gap}
			}
		}
	}

	// 连续被忽略 → 冷却（让机器人对冷场更谨慎）
	if m.strikes > 0 && m.store.consecutiveIgnored(it.Scope, m.strikes) >= m.strikes {
		if last, ok := m.store.lastSentAt(it.Scope); ok {
			until := last.Add(m.cooldown)
			if until.After(now) {
				if until.Before(it.ExpiresAt) {
					return gateResult{action: gatePostpone, reason: "近期主动发言无人回应，冷却中", delay: until.Sub(now)}
				}
				return gateResult{action: gateCancel, reason: "近期主动发言无人回应，仍处于冷却期"}
			}
		}
	}

	// 现场活跃度
	if snap := m.snapshot(it.Scope); snap != nil {
		if !snap.LastBotAt.IsZero() {
			if gap := proactiveMinAfterBot - now.Sub(snap.LastBotAt); gap > 0 {
				return gateResult{action: gatePostpone, reason: "刚发过言，稍后再说", delay: gap}
			}
		}
		if !snap.LastHumanAt.IsZero() {
			if gap := proactiveLiveWindow - now.Sub(snap.LastHumanAt); gap > 0 {
				return gateResult{action: gatePostpone, reason: "当前正在聊天，不插话", delay: proactivePostponeStep}
			}
			// 冷场意图排队期间已经有人说话了：这句话的前提没了，作废
			if it.Source == proactiveSourceIdle && snap.LastHumanAt.After(it.CreatedAt) {
				return gateResult{action: gateCancel, reason: "现场已恢复活跃，本次冷场意图作废"}
			}
		}
	}
	return gateResult{action: gateAllow}
}

func (m *proactiveManager) isRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

// launch 在后台执行一条意图（独立 goroutine + 崩溃恢复，不阻塞 tick）。
func (m *proactiveManager) launch(it *ProactiveIntention, forced bool) {
	m.mu.Lock()
	if _, ok := m.running[it.ID]; ok {
		m.mu.Unlock()
		return
	}
	m.running[it.ID] = struct{}{}
	b := m.bot
	m.mu.Unlock()

	run := func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, it.ID)
			m.mu.Unlock()
		}()
		m.execute(it, forced)
	}
	if b != nil {
		b.Go("proactive:"+it.ID, run)
		return
	}
	go run()
}

// closeIntention 结束一条意图并落盘最终状态。
func (m *proactiveManager) closeIntention(it *ProactiveIntention, status, reason string) {
	it.Status = status
	it.Reason = reason
	if !m.store.update(it) {
		m.logger.Error("更新意图状态失败", "id", it.ID, "status", status)
	}
	m.logger.Info("主动意图结束", "id", it.ID, "scope", it.Scope, "source", it.Source,
		"status", status, "reason", reason)
}

// postpone 推迟一条意图；超过有效期则直接作废。
func (m *proactiveManager) postpone(it *ProactiveIntention, due time.Time, reason string) {
	if !it.ExpiresAt.IsZero() && due.After(it.ExpiresAt) {
		m.closeIntention(it, proactiveStatusCancelled, reason+"（顺延已超过有效期）")
		return
	}
	it.DueAt = due
	it.Reason = reason
	if !m.store.update(it) {
		m.logger.Error("推迟意图失败", "id", it.ID)
		return
	}
	m.logger.Debug("意图顺延", "id", it.ID, "due", due.Format("15:04:05"), "reason", reason)
}

// ---- 小工具 ----

// parseScope 拆分 scope（g:xxx / f:xxx）为会话 ID 与是否群聊。
// 平台 ID 本身可能含冒号（如 tg:-100123），因此只在第一个冒号处切分。
func parseScope(scope string) (message.QID, bool, bool) {
	prefix, rest, found := strings.Cut(scope, ":")
	if !found || rest == "" {
		return "", false, false
	}
	switch prefix {
	case "g":
		return message.QID(rest), true, true
	case "f":
		return message.QID(rest), false, true
	default:
		return "", false, false
	}
}

// parseQuietHours 解析 "23:00-08:00" 形式的免打扰时段（也接受 ~ 或 至 分隔）。
// 起止相同视为未配置，避免整天静默。
func parseQuietHours(spec string) (start, end int, ok bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, false
	}
	parts := strings.FieldsFunc(spec, func(r rune) bool {
		return r == '-' || r == '~' || r == '—' || r == '至'
	})
	if len(parts) != 2 {
		return 0, 0, false
	}
	s, ok1 := parseClockMinutes(parts[0])
	e, ok2 := parseClockMinutes(parts[1])
	if !ok1 || !ok2 || s == e {
		return 0, 0, false
	}
	return s, e, true
}

func parseClockMinutes(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	h, m, found := strings.Cut(s, ":")
	if !found {
		m = "0"
	}
	hh, err1 := strconv.Atoi(strings.TrimSpace(h))
	mm, err2 := strconv.Atoi(strings.TrimSpace(m))
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// inQuietRange 判断给定时刻是否落在免打扰时段内（支持跨午夜）。
func inQuietRange(now time.Time, start, end int) bool {
	cur := now.Hour()*60 + now.Minute()
	if start < end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}

// quietEndTime 返回免打扰结束的时刻（已过结束点则取次日对应时刻）。
func quietEndTime(now time.Time, end int) time.Time {
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	target := base.Add(time.Duration(end) * time.Minute)
	if !target.After(now) {
		target = target.Add(24 * time.Hour)
	}
	return target
}

// startOfDay 返回本地时区当天零点，用于每日上限统计。
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// jitter 返回 [0, max) 内的随机时长；max <= 0 返回 0。
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max)))
}

// sanitizeLine 把消息文本压成一行并截断，用于现场记录。
func sanitizeLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	return truncateRunes(s, proactiveMaxTextRunes)
}

// senderLabel 取发送者的展示名：群名片优先，其次昵称，最后 ID。
func senderLabel(s message.MessageSender) string {
	if s.Card != "" {
		return s.Card
	}
	if s.Nickname != "" {
		return s.Nickname
	}
	return s.UserId.String()
}

// newIntentionID 生成 8 位十六进制意图 ID（与记忆条目同规格）。
func newIntentionID() string {
	return newMemoryID()
}

// noteBotSpoke 供消息主流程调用：机器人刚说过话，短时间内不应再主动开口。
// manager 未启用时零开销。
func (p *AIChatPlugin) noteBotSpoke(id message.QID, isGroup bool) {
	if p.proactiveManager == nil {
		return
	}
	p.proactiveManager.noteBotSpoke(id, isGroup)
}

// truncateRunes 按符文截断并追加省略号。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
