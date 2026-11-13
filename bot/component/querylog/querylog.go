// Package querylog 提供 AI 查询（Query）执行日志的持久化记录能力。
//
// 一次 Query 指从用户触发 AI 回复（群里 @ 机器人或私聊）到 AI 最终响应的完整过程。
// 持久化存储固定为 SQL 后端（sqlite/mysql），日志每条一行（ania_query_log 表，
// 过滤条件下推 WHERE、清除按条件走范围删除）；探测或建表失败时降级为禁用后端
// （写入静默丢弃），仅记录错误日志。日志 ID 为自增序号的 base36，面板游标分页语义不变。
//
// 日志不做容量淘汰：一旦写入即长期保留，由管理员在面板手动导出或清除。
package querylog

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/common/storage"
)

// Status 查询执行状态
type Status string

const (
	StatusRunning     Status = "running"     // 已触发，执行中
	StatusSuccess     Status = "success"     // 执行成功
	StatusSkipped     Status = "skipped"     // 主动互动中 AI 判断无需发言（保持安静）
	StatusStopped     Status = "stopped"     // 用户主动停止（/stop）
	StatusTimeout     Status = "timeout"     // 请求超时
	StatusError       Status = "error"       // 执行出错
	StatusInterrupted Status = "interrupted" // 进程重启，执行中断
)

// Kind 日志类型。普通对话留空（升级前的历史记录同样为空），主动互动等非常规
// 触发用非空值标记；面板据此区分展示与筛选。
const (
	KindChat      = "chat"      // 普通对话（仅作为筛选值，普通记录不写该字段）
	KindProactive = "proactive" // 主动互动（AI 自行发起的发言）
)

// 会话类型（Entry.ChatType）。group / friend 是线上平台的群聊 / 私聊会话；
// panel 是面板「试聊室」里的调试对话——它复用同一套对话管线，同样计入 Token
// 统计，但不属于任何线上会话，单独成类便于筛选与区分。
const (
	ChatTypeGroup  = "group"
	ChatTypeFriend = "friend"
	ChatTypePanel  = "panel"
)

// 用户输入、工具参数、工具结果、最终回复均由 AI 与工具生成，始终完整记录，
// 不做长度截断（前端展示时对超长内容折叠）。Truncate 仅供其它展示场景复用。

// ToolCallRecord 一次工具调用的执行记录（如 bash 命令的执行详情）
type ToolCallRecord struct {
	Name       string `json:"name"`
	Arguments  string `json:"arguments,omitempty"`
	Result     string `json:"result,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// StatRow 一条日志的统计字段（不含完整 payload 与工具明细），供 Token 统计聚合。
// SQL 后端从冗余统计列读取，避免为了统计反复解析大体积 payload。
type StatRow struct {
	Time             time.Time
	Kind             string
	ChatType         string
	TargetID         string
	Status           Status
	Iterations       int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
}

// Entry 一条 Query 日志
type Entry struct {
	ID               string           `json:"id"`
	Time             time.Time        `json:"time"`
	Kind             string           `json:"kind,omitempty"` // 空为普通对话，见 Kind* 常量
	ChatType         string           `json:"chat_type"`      // group / friend / panel
	TargetID         string           `json:"target_id"`      // 目标会话 ID / 用户 ID（试聊为绑定目标或其会话标题）
	Senders          []string         `json:"senders,omitempty"`
	Query            string           `json:"query"` // 合并后的用户输入（可能含排队消息）
	Status           Status           `json:"status"`
	DurationMs       int64            `json:"duration_ms"`
	Iterations       int              `json:"iterations"` // LLM 调用轮数
	ToolCalls        []ToolCallRecord `json:"tool_calls,omitempty"`
	ToolCallsTotal   int              `json:"tool_calls_total,omitempty"` // 工具调用总数（明细被截断时大于 len(ToolCalls)）
	PromptTokens     int              `json:"prompt_tokens,omitempty"`
	CompletionTokens int              `json:"completion_tokens,omitempty"`
	TotalTokens      int              `json:"total_tokens,omitempty"`
	CachedTokens     int              `json:"cached_tokens,omitempty"` // 命中上游 prompt 缓存的 token 数（提供方支持时才有）
	Reply            string           `json:"reply,omitempty"`         // 最终回复
	Note             string           `json:"note,omitempty"`          // 附加说明（如主动互动保持安静的原因）
	Error            string           `json:"error,omitempty"`
}

// KindValue 返回日志类型；空值归一为普通对话（兼容升级前的历史记录）。
func (e Entry) KindValue() string {
	if e.Kind == "" {
		return KindChat
	}
	return e.Kind
}

// Truncate 按符文数截断字符串，超出时追加省略标记。max<=0 时不截断。
func Truncate(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// backend 日志存储后端。调用方（Logger）持有 mu 并保证写路径串行，
// 后端实现无需额外加锁。错误内部记录日志（后端持有 logger）。
type backend interface {
	// maxSeq 返回已分配的最大序号（启动初始化计数器用）；无记录时返回 0。
	maxSeq() uint64
	// insert 追加一条日志。
	insert(seq uint64, e Entry)
	// load 按序号读取一条日志（Update 定位用）。
	load(seq uint64) (Entry, bool)
	// overwrite 按序号覆盖一条日志（Update 落盘用）。
	overwrite(seq uint64, e Entry)
	// clear 清除满足条件的日志（零值条件清除全部），返回删除条数。
	clear(f Filter) int
	// stats 返回满足条件的轻量统计行（不含完整 payload），供 Token 统计聚合。
	stats(f Filter) []StatRow
	// recent 返回最近 limit 条日志（新在前）；limit<=0 时返回全部。
	recent(limit int) []Entry
	// query 按条件过滤日志（新在前），beforeSeq>0 时仅返回序号更小的记录，
	// 最多 limit 条（limit<=0 不限）。f.match 为最终判定。
	query(f Filter, beforeSeq uint64, limit int) []Entry
	// markRunningInterrupted 把所有 running 状态日志标记为 interrupted
	//（进程重启后无法正常收尾的遗留执行），返回更新条数。
	markRunningInterrupted(now time.Time) int
}

// Logger 持久化 Query 日志记录器。
//
// 内部用互斥串行化 Record/Update；读取（Recent/Query）每次直接从存储加载
// 最新快照，不持有内存缓存，保证与落盘状态一致。日志 ID 为自增序号的 base36。
type Logger struct {
	backend backend
	logger  *slog.Logger
	mu      sync.Mutex
	seq     uint64
}

// New 创建日志记录器。持久化存储固定为 SQL 后端（sqlite/mysql），日志走
// ania_query_log 行级存储；探测或建表失败时降级为禁用后端（写入静默丢弃、
// 读取返回空），仅记录错误日志。日志不做容量淘汰，全部保留。
func New(store storage.PersistentStorage, logger *slog.Logger) *Logger {
	l := &Logger{logger: logger}
	if l.logger == nil {
		l.logger = slog.Default()
	}
	db, dialect, ok := storage.SQLBackend(store)
	if !ok {
		l.logger.Error("持久化存储不支持 SQL，Query 日志禁用")
		l.backend = disabledBackend{}
		return l
	}
	if err := storage.EnsureTables(context.Background(), db, dialect, queryLogTables...); err != nil {
		l.logger.Error("创建 Query 日志表失败，Query 日志禁用", "error", err.Error())
		l.backend = disabledBackend{}
		return l
	}
	// 升级前的旧库没有 kind 列：按需补列（失败时该列不参与下推与写入）
	hasKind := ensureKindColumn(context.Background(), db, dialect, l.logger)
	// Token 统计依赖冗余统计列，旧库同样按需补列并回填一次
	hasStats, statsAdded := ensureStatsColumns(context.Background(), db, dialect, l.logger)
	be := newSQLBackend(db, l.logger, hasKind, hasStats, dialect)
	if statsAdded {
		be.backfillStats()
	}
	l.backend = be
	l.seq = l.backend.maxSeq()
	return l
}

// disabledBackend SQL 探测/建表失败时的占位后端：写入静默丢弃、读取返回空。
type disabledBackend struct{}

func (disabledBackend) maxSeq() uint64                       { return 0 }
func (disabledBackend) insert(uint64, Entry)                 {}
func (disabledBackend) load(uint64) (Entry, bool)            { return Entry{}, false }
func (disabledBackend) overwrite(uint64, Entry)              {}
func (disabledBackend) clear(Filter) int                     { return 0 }
func (disabledBackend) stats(Filter) []StatRow               { return nil }
func (disabledBackend) recent(int) []Entry                   { return nil }
func (disabledBackend) query(Filter, uint64, int) []Entry    { return nil }
func (disabledBackend) markRunningInterrupted(time.Time) int { return 0 }

// Record 追加一条日志并落盘，返回写入的 Entry（含分配的 ID）。
func (l *Logger) Record(e Entry) Entry {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	e.ID = strconv.FormatUint(l.seq, 36)
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Status == "" {
		e.Status = StatusRunning
	}

	l.backend.insert(l.seq, e)
	return e
}

// Update 按日志 ID 更新一条已存在的记录（如 running → success）。未找到时忽略。
func (l *Logger) Update(id string, mutate func(*Entry)) {
	if id == "" {
		return
	}
	// ID 即序号的 base36，直接定位记录，无需全量扫描
	n, err := strconv.ParseUint(id, 36, 64)
	if err != nil || n == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.backend.load(n)
	if !ok {
		return
	}
	mutate(&e)
	l.backend.overwrite(n, e)
}

// MarkRunningInterrupted 将遗留的 running 状态日志统一标记为 interrupted
// （进程重启导致执行中断，如等待工具审批时重启，未能正常收尾）。返回更新条数。
func (l *Logger) MarkRunningInterrupted() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backend.markRunningInterrupted(time.Now())
}

// interruptEntry 把一条 running 日志回填为中断状态：状态、说明，并补记耗时
// （从触发到重启时刻）。
func interruptEntry(e *Entry, now time.Time) {
	e.Status = StatusInterrupted
	e.Error = "进程重启，执行中断"
	if !e.Time.IsZero() {
		e.DurationMs = now.Sub(e.Time).Milliseconds()
	}
}

// Recent 返回最近 limit 条日志（新在前）。limit<=0 时返回全部。
func (l *Logger) Recent(limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backend.recent(limit)
}

// Filter Query 日志的查询条件，零值字段不参与过滤。
type Filter struct {
	Kind     string    // chat / proactive（空不过滤；chat 同时命中未标记的历史记录）
	ChatType string    // group / friend / panel
	TargetID string    // 目标会话 ID / 用户 ID（精确匹配）
	Sender   string    // 触发人 QQ（精确匹配，命中批次内任一发言者）
	Start    time.Time // 起始时间（含），零值不限
	End      time.Time // 截止时间（含），零值不限
	Keyword  string    // 用户输入内容包含的关键词（不区分大小写）
	Before   string    // 分页游标：仅返回比该日志 ID 更旧的记录（空为从最新开始）
	Limit    int       // 返回条数上限，<=0 返回全部（导出用）
}

// match 判断一条日志是否满足过滤条件。
func (f Filter) match(e Entry) bool {
	if f.Kind != "" && e.KindValue() != f.Kind {
		return false
	}
	if f.ChatType != "" && e.ChatType != f.ChatType {
		return false
	}
	if f.TargetID != "" && e.TargetID != f.TargetID {
		return false
	}
	if f.Sender != "" {
		hit := slices.Contains(e.Senders, f.Sender)
		if !hit {
			return false
		}
	}
	if !f.Start.IsZero() && e.Time.Before(f.Start) {
		return false
	}
	if !f.End.IsZero() && e.Time.After(f.End) {
		return false
	}
	if f.Keyword != "" && !strings.Contains(strings.ToLower(e.Query), strings.ToLower(f.Keyword)) {
		return false
	}
	return true
}

// Query 按条件过滤日志（新在前），最多返回 f.Limit 条（<=0 返回全部）。
// f.Before 非空时作为分页游标，仅返回比它更旧的记录。
func (l *Logger) Query(f Filter) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backend.query(f, parseBeforeSeq(f.Before), f.Limit)
}

// Clear 清除满足条件的日志（零值条件清除全部），返回删除条数。
func (l *Logger) Clear(f Filter) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backend.clear(f)
}

// Stats 返回满足条件的轻量统计行（按时间升序，不含完整 payload），
// 供 Token 统计按全部留存日志聚合。f.Before/Limit 不参与统计。
func (l *Logger) Stats(f Filter) []StatRow {
	f.Before, f.Limit = "", 0
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backend.stats(f)
}

// parseBeforeSeq 把分页游标（日志 ID，序号的 base36）解析为序号；非法时为 0（不生效）。
func parseBeforeSeq(before string) uint64 {
	if before == "" {
		return 0
	}
	n, err := strconv.ParseUint(before, 36, 64)
	if err != nil {
		return 0
	}
	return n
}
