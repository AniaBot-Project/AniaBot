package pluginaichat

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// 后台任务注册表的行级存储 schema：所有「离开当前回合继续存在的工作」
// （子代理 / 交接 / 工作流 / 追问 / 团队运行与团队成员 / 团队任务板）统一落一行，
// 按 id 主键读写，session_key/status 只用于收窄查询。普通回合已不再写入
// （其完整过程由 Query 日志记录，避免两套表重复），turn 仅为旧记录兼容。
// 时间戳统一 unix 秒（INTEGER/BIGINT），不用 CURRENT_TIMESTAMP：
// 任务状态机在 Go 侧推进，数据库只做持久化，时间由调用方掌握便于测试与恢复判断。
//
// 与对话历史/知识库的取舍一致：任务需要按 id 单行更新状态、按会话倒序列出、
// 按状态批量扫未结束任务，关系表比整段 JSON 更合适；Meta 只作恢复提示透传，
// 不参与查询。

const (
	// 任务种类：一条任务记录描述哪种后台工作。
	//
	// turn 是升级前遗留的类型：普通回合曾与 Query 日志一起各写一行，现已不再
	// 写入任务表（避免重复）；面板数据源会排除它，仅 /tasks 兼容旧数据时展示。
	taskKindTurn       = "turn"        // 历史遗留的会话回合记录
	taskKindSubagent   = "subagent"    // 子代理
	taskKindHandoff    = "handoff"     // 回合交接出的后台任务
	taskKindWorkflow   = "workflow"    // 后台工作流（DAG 多子代理编排）
	taskKindTeamTask   = "team_task"   // 团队任务板条目
	taskKindTeamRun    = "team_run"    // Agent 团队一次并行执行（父记录）
	taskKindTeamMember = "team_member" // 团队成员代理（team_run 的子记录）
	taskKindFollowup   = "followup"    // 对已完成子代理/团队成员的追问（ParentID 指向原任务）
)

const (
	// 任务状态机：queued → running → (completed|failed|timeout|cancelled|
	// interrupted)，waiting 表示已启动但阻塞在等待（如等子任务/工具）。
	taskStatusQueued      = "queued"
	taskStatusRunning     = "running"
	taskStatusWaiting     = "waiting"
	taskStatusCompleted   = "completed"
	taskStatusFailed      = "failed"
	taskStatusTimeout     = "timeout"
	taskStatusCancelled   = "cancelled"
	taskStatusInterrupted = "interrupted"
)

const (
	// 任务结束原因：与状态互补，说明「为什么结束」。
	taskStopCompleted = "completed"
	taskStopAborted   = "aborted"
	taskStopMaxTokens = "max_tokens"
	taskStopError     = "error"
	taskStopWatchdog  = "watchdog"
	taskStopTimeout   = "timeout"
	taskStopUserStop  = "user_stop"
)

const (
	// taskListDefaultLimit ListBySession 未显式给 limit（<=0）时的默认条数。
	taskListDefaultLimit = 50

	// taskIDRandomBytes 任务 ID 随机后缀的字节数（2 字节 → 4 位十六进制）。
	taskIDRandomBytes = 2
)

// agentTask 一条后台任务记录（子代理/交接/工作流/团队/任务板；turn 为旧记录）。
type agentTask struct {
	ID         string // "t-<unixnano 36 进制>-<4 位随机 hex>"
	SessionKey string // "g:<id>" / "f:<id>"
	ParentID   string // 父任务 ID，可为空
	Kind       string // turn|subagent|handoff|workflow|team_task|team_run|team_member|followup
	Status     string // queued|running|waiting|completed|failed|timeout|cancelled|interrupted
	Title      string
	Prompt     string // 任务指令/摘要（可为空）
	Result     string // 结果文本（可为空）
	Error      string // 脱敏错误（可为空）
	StopReason string // completed|aborted|max_tokens|error|watchdog|timeout|user_stop
	Tokens     int64
	Meta       string // JSON 字符串（阶段/工具计数/恢复提示），可为空
	CreatedAt  int64  // unix 秒
	StartedAt  int64  // unix 秒，0=未开始
	UpdatedAt  int64  // unix 秒
	FinishedAt int64  // unix 秒，0=未结束
}

// taskTables 返回后台任务表的双方言 DDL。
//
// SQLite 使用 TEXT/INTEGER；MySQL 的字符串主键与索引列显式 utf8mb4_bin
// （字节序、大小写敏感，与 ania_kv / ania_chat_session 对齐），大文本用
// MEDIUMTEXT（prompt/result/meta 可能较长），时间戳统一 BIGINT unix 秒。
// 两个索引分别服务「按会话列任务」与「按状态扫未结束任务/清理」。
func taskTables() []storage.TableDDL {
	return []storage.TableDDL{
		{
			Name: "ania_agent_task",
			SQLite: []string{
				`CREATE TABLE IF NOT EXISTS ania_agent_task (` +
					`id TEXT NOT NULL, ` +
					`session_key TEXT NOT NULL, ` +
					`parent_id TEXT NOT NULL DEFAULT '', ` +
					`kind TEXT NOT NULL, ` +
					`status TEXT NOT NULL, ` +
					`title TEXT NOT NULL, ` +
					`prompt TEXT NOT NULL DEFAULT '', ` +
					`result TEXT NOT NULL DEFAULT '', ` +
					`error TEXT NOT NULL DEFAULT '', ` +
					`stop_reason TEXT NOT NULL DEFAULT '', ` +
					`tokens INTEGER NOT NULL DEFAULT 0, ` +
					`meta TEXT NOT NULL DEFAULT '', ` +
					`created_at INTEGER NOT NULL, ` +
					`started_at INTEGER NOT NULL DEFAULT 0, ` +
					`updated_at INTEGER NOT NULL, ` +
					`finished_at INTEGER NOT NULL DEFAULT 0, ` +
					`PRIMARY KEY (id))`,
				`CREATE INDEX IF NOT EXISTS idx_agent_task_session ON ania_agent_task(session_key, status)`,
				`CREATE INDEX IF NOT EXISTS idx_agent_task_status ON ania_agent_task(status, updated_at)`,
			},
			MySQL: []string{
				`CREATE TABLE IF NOT EXISTS ania_agent_task (` +
					`id VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
					`session_key VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
					`parent_id VARCHAR(255) COLLATE utf8mb4_bin NOT NULL DEFAULT '', ` +
					`kind VARCHAR(32) COLLATE utf8mb4_bin NOT NULL, ` +
					`status VARCHAR(255) COLLATE utf8mb4_bin NOT NULL, ` +
					`title MEDIUMTEXT NOT NULL, ` +
					`prompt MEDIUMTEXT NOT NULL, ` +
					`result MEDIUMTEXT NOT NULL, ` +
					`error MEDIUMTEXT NOT NULL, ` +
					`stop_reason VARCHAR(32) COLLATE utf8mb4_bin NOT NULL DEFAULT '', ` +
					`tokens BIGINT NOT NULL DEFAULT 0, ` +
					`meta MEDIUMTEXT NOT NULL, ` +
					`created_at BIGINT NOT NULL, ` +
					`started_at BIGINT NOT NULL DEFAULT 0, ` +
					`updated_at BIGINT NOT NULL, ` +
					`finished_at BIGINT NOT NULL DEFAULT 0, ` +
					`PRIMARY KEY (id), ` +
					`KEY idx_agent_task_session (session_key, status), ` +
					`KEY idx_agent_task_status (status, updated_at)` +
					`) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
			},
		},
	}
}

// taskStore 后台任务行级存储。表不可用时（SQL 探测/建表失败）由调用方降级为
// 内存任务列表；本类型只负责在有连接时如实读写。SQLite 单连接下读取遵循
// "收集→关闭 rows→解析"纪律。
type taskStore struct {
	db     *sql.DB
	logger *slog.Logger
}

func newTaskStore(db *sql.DB, logger *slog.Logger) *taskStore {
	return &taskStore{db: db, logger: logger}
}

// EnsureTables 创建任务表与索引（幂等）。方言由连接驱动推断，见 taskDialect。
// 失败时返回错误，由调用方记日志并降级，不阻断启动。
func (s *taskStore) EnsureTables(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("创建后台任务表失败：数据库连接为空")
	}
	dialect, err := taskDialect(s.db)
	if err != nil {
		return err
	}
	return storage.EnsureTables(ctx, s.db, dialect, taskTables()...)
}

// taskDialect 从连接驱动推断 SQL 方言。
//
// 构造签名按契约只接收 db/logger，而 storage.EnsureTables 需要方言；内置
// 持久化后端的驱动固定为 modernc.org/sqlite（"sqlite"）与
// go-sql-driver/mysql（"mysql"），按驱动具体类型名判定即可
// （*sqlite.Driver / *mysql.MySQLDriver），不需要额外建连探测。
// 未知驱动返回错误，调用方按降级约定处理（任务表不可用）。
func taskDialect(db *sql.DB) (storage.SQLDialect, error) {
	name := fmt.Sprintf("%T", db.Driver())
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "sqlite"):
		return storage.SQLDialectSQLite, nil
	case strings.Contains(lower, "mysql"):
		return storage.SQLDialectMySQL, nil
	default:
		return "", fmt.Errorf("无法识别的 SQL 驱动 %q：后台任务表仅支持 sqlite/mysql", name)
	}
}

// lastTaskIDNano 保证同一进程内 ID 的纳秒部分单调递增。
//
// Windows 的墙上时钟粒度可能粗到毫秒，批量创建任务时多次 time.Now 会落在同一
// 纳秒；契约固定随机后缀只有 4 位 hex（16 bit），1000 次创建内仅靠它防碰撞
// 概率不足（生日碰撞）。这里用 CAS 把重复的纳秒顺延 +1：格式仍是 36 进制纳秒，
// 但同进程内不会重复，跨进程再由随机后缀兜底。
var lastTaskIDNano atomic.Int64

// newTaskID 生成任务 ID："t-<unixnano 36 进制>-<4 位随机 hex>"。
// 36 进制纳秒保证同类批量创建时天然有序可读，随机后缀避免跨进程同纳秒碰撞；
// crypto/rand 失败极罕见，退化用纳秒低位凑齐 4 位十六进制（与 newKbID 的
// 退化策略一致）。
func newTaskID() string {
	now := time.Now().UnixNano()
	for {
		last := lastTaskIDNano.Load()
		if now <= last {
			now = last + 1
		}
		if lastTaskIDNano.CompareAndSwap(last, now) {
			break
		}
	}
	var b [taskIDRandomBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "t-" + strconv.FormatInt(now, 36) + "-" + fmt.Sprintf("%04x", uint16(now))
	}
	return "t-" + strconv.FormatInt(now, 36) + "-" + hex.EncodeToString(b[:])
}

// taskColumns 查询列顺序，必须与 scanTask 的扫描顺序一一对应。
const taskColumns = `id, session_key, parent_id, kind, status, title, prompt, result, ` +
	`error, stop_reason, tokens, meta, created_at, started_at, updated_at, finished_at`

// taskInsertSQL 全字段插入；ID 与时间戳由 Create 补齐后写入。
const taskInsertSQL = `INSERT INTO ania_agent_task (` + taskColumns + `) ` +
	`VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// taskUpdateSQL 按 ID 全字段更新（created_at 不变，updated_at 由 Update 置为 now）。
const taskUpdateSQL = `UPDATE ania_agent_task SET ` +
	`session_key = ?, parent_id = ?, kind = ?, status = ?, title = ?, prompt = ?, ` +
	`result = ?, error = ?, stop_reason = ?, tokens = ?, meta = ?, ` +
	`started_at = ?, finished_at = ?, updated_at = ? WHERE id = ?`

// taskProgressSQL 执行中的进度增量更新：只写 meta 与 updated_at，其余字段保持
// 不动（子代理每完成几步工具调用就回写一次，整行覆盖会把并发推进的字段写回旧值）。
// 条件 finished_at = 0 是竞态护栏：任务收尾（Update 写入 finished_at）之后迟到的
// 进度写自动失效，绝不会用「运行中」的快照覆盖终态。
const taskProgressSQL = `UPDATE ania_agent_task SET meta = ?, updated_at = ? ` +
	`WHERE id = ? AND finished_at = 0`

// taskRowScanner 同时适配 *sql.Row 与 *sql.Rows（避免依赖其他文件的同名接口）。
type taskRowScanner interface {
	Scan(dest ...any) error
}

// scanTask 把一行任务记录扫描为 agentTask（空串与 0 值原样保留）。
func scanTask(row taskRowScanner) (*agentTask, error) {
	var t agentTask
	err := row.Scan(&t.ID, &t.SessionKey, &t.ParentID, &t.Kind, &t.Status, &t.Title,
		&t.Prompt, &t.Result, &t.Error, &t.StopReason, &t.Tokens, &t.Meta,
		&t.CreatedAt, &t.StartedAt, &t.UpdatedAt, &t.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// collectTasks 收集查询结果并关闭 rows。SQLite 单连接下必须先关闭 rows 再发
// 下一条语句，因此统一在这里收尾，调用方拿到的已是脱离连接的数据。
func collectTasks(rows *sql.Rows) ([]*agentTask, error) {
	defer rows.Close()
	tasks := make([]*agentTask, 0, 8)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

// Create 插入一条任务记录：空 ID 自动生成，CreatedAt/UpdatedAt 为 0 时填 now。
// 生成/补齐后的值会回写到 t，调用方可直接继续使用。重复 ID 返回错误（不 Upsert），
// 避免覆盖历史任务的终态。
func (s *taskStore) Create(ctx context.Context, t *agentTask) error {
	if t == nil {
		return fmt.Errorf("创建后台任务失败：记录为空")
	}
	now := time.Now().Unix()
	if t.ID == "" {
		t.ID = newTaskID()
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}
	if t.UpdatedAt == 0 {
		t.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, taskInsertSQL,
		t.ID, t.SessionKey, t.ParentID, t.Kind, t.Status, t.Title, t.Prompt,
		t.Result, t.Error, t.StopReason, t.Tokens, t.Meta,
		t.CreatedAt, t.StartedAt, t.UpdatedAt, t.FinishedAt)
	if err != nil {
		s.logger.Error("创建后台任务失败", "id", t.ID, "error", err)
		return err
	}
	return nil
}

// Update 按 ID 全字段更新任务（CreatedAt 保持不变），UpdatedAt 置为 now 并回写。
// 权限不在 WHERE 里做：任务 ID 是插件内部生成的不可猜测串，调用方持有即代表
// 有权更新。0 行影响不视为错误：MySQL 默认只统计实际变更的行，同一秒内重复
// 更新相同内容时 RowsAffected 为 0（记录是否存在由 Get 的 bool 区分）。
func (s *taskStore) Update(ctx context.Context, t *agentTask) error {
	if t == nil {
		return fmt.Errorf("更新后台任务失败：记录为空")
	}
	t.UpdatedAt = time.Now().Unix()
	_, err := s.db.ExecContext(ctx, taskUpdateSQL,
		t.SessionKey, t.ParentID, t.Kind, t.Status, t.Title, t.Prompt,
		t.Result, t.Error, t.StopReason, t.Tokens, t.Meta,
		t.StartedAt, t.FinishedAt, t.UpdatedAt, t.ID)
	if err != nil {
		s.logger.Error("更新后台任务失败", "id", t.ID, "error", err)
		return err
	}
	return nil
}

// UpdateProgress 增量更新一条未结束任务的执行进度（仅 meta 与 updated_at），
// 返回实际更新行数；任务已收尾（finished_at 非 0）或不存在时返回 0（不是错误）。
//
// 用于子代理/团队成员执行中按间隔回写工具步骤（见 subagentTrace.flushProgress）：
// 面板「后台任务」页轮询时即可看到进度，不必等整个任务结束。只改 meta 是有意的
// ——状态、结果、token 等字段仍由终态写入独占，进度写永远不会让它们回退。
func (s *taskStore) UpdateProgress(ctx context.Context, id, meta string) (int64, error) {
	if id == "" {
		return 0, fmt.Errorf("更新后台任务进度失败：任务 ID 为空")
	}
	res, err := s.db.ExecContext(ctx, taskProgressSQL, meta, time.Now().Unix(), id)
	if err != nil {
		s.logger.Error("更新后台任务进度失败", "id", id, "error", err)
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Get 按 ID 读取一条任务；不存在返回 (nil, false, nil)。
func (s *taskStore) Get(ctx context.Context, id string) (*agentTask, bool, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM ania_agent_task WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		s.logger.Error("读取后台任务失败", "id", id, "error", err)
		return nil, false, err
	}
	return t, true, nil
}

// ListBySession 按 created_at 倒序列出会话的任务（limit<=0 取默认 50）。
// 同一秒创建的任务用 id 倒序兜底，保证结果稳定。
func (s *taskStore) ListBySession(ctx context.Context, sessionKey string, limit int) ([]*agentTask, error) {
	if limit <= 0 {
		limit = taskListDefaultLimit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM ania_agent_task WHERE session_key = ? `+
			`ORDER BY created_at DESC, id DESC LIMIT ?`, sessionKey, limit)
	if err != nil {
		s.logger.Error("列出会话后台任务失败", "session", sessionKey, "error", err)
		return nil, err
	}
	tasks, err := collectTasks(rows)
	if err != nil {
		s.logger.Error("列出会话后台任务失败", "session", sessionKey, "error", err)
		return nil, err
	}
	return tasks, nil
}

// ListRunning 列出未结束的任务（queued/running/waiting），按 created_at 正序，
// 供启动恢复扫描使用。同一秒创建的任务用 id 正序兜底。
func (s *taskStore) ListRunning(ctx context.Context) ([]*agentTask, error) {
	in, args := taskInStatuses(taskActiveStatuses())
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM ania_agent_task WHERE status IN (`+in+`) `+
			`ORDER BY created_at ASC, id ASC`, args...)
	if err != nil {
		s.logger.Error("列出未结束后台任务失败", "error", err)
		return nil, err
	}
	tasks, err := collectTasks(rows)
	if err != nil {
		s.logger.Error("列出未结束后台任务失败", "error", err)
		return nil, err
	}
	return tasks, nil
}

// MarkInterrupted 启动恢复：把仍处于 queued/running/waiting 的任务标记为
// interrupted（stop_reason=aborted，补 finished_at/updated_at），返回影响行数。
// 进程重启后不可能再有人推进这些任务，统一落终态避免永远显示"运行中"。
func (s *taskStore) MarkInterrupted(ctx context.Context) (int64, error) {
	now := time.Now().Unix()
	in, args := taskInStatuses(taskActiveStatuses())
	args = append([]any{taskStatusInterrupted, taskStopAborted, now, now}, args...)
	res, err := s.db.ExecContext(ctx,
		`UPDATE ania_agent_task SET status = ?, stop_reason = ?, finished_at = ?, updated_at = ? `+
			`WHERE status IN (`+in+`)`, args...)
	if err != nil {
		s.logger.Error("标记中断后台任务失败", "error", err)
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteFinishedBefore 删除已经结束（终态）且 finished_at 早于 cutoff 的任务，
// 返回删除行数。finished_at=0 表示未结束（即使状态列异常也保留），避免误删
// 仍在推进的任务；未结束状态的任务无论 finished_at 为何都不删。
func (s *taskStore) DeleteFinishedBefore(ctx context.Context, cutoff int64) (int64, error) {
	in, args := taskInStatuses(taskFinishedStatuses())
	args = append(args, cutoff)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM ania_agent_task WHERE status IN (`+in+`) AND finished_at > 0 AND finished_at < ?`,
		args...)
	if err != nil {
		s.logger.Error("清理已完成后台任务失败", "cutoff", cutoff, "error", err)
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// taskFilter 后台任务日志的筛选条件（面板导出/清除用）。Status/Kind 下推到 SQL；
// Keyword 在 Go 侧做不区分大小写的最终判定——各数据库 LIKE 的大小写与排序规则
// 不同，面板列表本身也按 Go 侧口径过滤，两边结果才一致。
type taskFilter struct {
	Status  string
	Kind    string
	Keyword string
}

// matchKeyword 关键词是否命中标题 / 任务 ID / 会话键 / 任务指令（不区分大小写）。
func (f taskFilter) matchKeyword(t *agentTask) bool {
	kw := strings.ToLower(strings.TrimSpace(f.Keyword))
	if kw == "" || t == nil {
		return true
	}
	for _, s := range []string{t.Title, t.ID, t.SessionKey, t.Prompt} {
		if strings.Contains(strings.ToLower(s), kw) {
			return true
		}
	}
	return false
}

// ListFiltered 按条件列出后台任务（updated_at 倒序；limit<=0 返回全部）。
// 升级前的 turn 行不返回（与面板列表口径一致）。带关键词时先取全部候选再在 Go
// 侧过滤与限条，避免 SQL LIMIT 截掉后面才命中的记录。
func (s *taskStore) ListFiltered(ctx context.Context, f taskFilter, limit int) ([]*agentTask, error) {
	where := []string{"kind <> ?"}
	args := []any{taskKindTurn}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	q := `SELECT ` + taskColumns + ` FROM ania_agent_task WHERE ` +
		strings.Join(where, " AND ") + ` ORDER BY updated_at DESC, id DESC`
	if limit > 0 && f.Keyword == "" {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		s.logger.Error("按条件列出后台任务失败", "error", err)
		return nil, err
	}
	tasks, err := collectTasks(rows)
	if err != nil {
		s.logger.Error("按条件列出后台任务失败", "error", err)
		return nil, err
	}
	if f.Keyword == "" {
		return tasks, nil
	}
	matched := make([]*agentTask, 0, len(tasks))
	for _, t := range tasks {
		if f.matchKeyword(t) {
			matched = append(matched, t)
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

// taskDeleteChunk 单条 DELETE 语句的 ID 数上限：SQLite 的变量上限默认 999，
// 留出余量后分块删除，避免任务很多时拼出超长 IN 列表。
const taskDeleteChunk = 400

// DeleteFinishedFiltered 删除满足条件的**已结束**任务，返回删除条数。
// 运行中/排队中的任务一律保留：它们仍被运行时写入，删除任务行会让进度写与
// 收尾写落空；turn 历史行也不删（不属于面板「后台任务」日志）。
func (s *taskStore) DeleteFinishedFiltered(ctx context.Context, f taskFilter) (int64, error) {
	tasks, err := s.ListFiltered(ctx, f, 0)
	if err != nil {
		return 0, err
	}
	finished := make(map[string]struct{}, len(taskFinishedStatuses()))
	for _, st := range taskFinishedStatuses() {
		finished[st] = struct{}{}
	}
	ids := make([]any, 0, len(tasks))
	for _, t := range tasks {
		if _, ok := finished[t.Status]; ok {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var removed int64
	for start := 0; start < len(ids); start += taskDeleteChunk {
		end := min(start+taskDeleteChunk, len(ids))
		part := ids[start:end]
		holders := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM ania_agent_task WHERE id IN (`+holders+`)`, part...)
		if err != nil {
			s.logger.Error("清除后台任务失败", "error", err)
			return removed, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, nil
}

// taskActiveStatuses 尚未结束的任务状态：ListRunning 与 MarkInterrupted 的判定集。
func taskActiveStatuses() []string {
	return []string{taskStatusQueued, taskStatusRunning, taskStatusWaiting}
}

// taskFinishedStatuses 已经结束的任务状态：DeleteFinishedBefore 的判定集。
func taskFinishedStatuses() []string {
	return []string{
		taskStatusCompleted, taskStatusFailed, taskStatusTimeout,
		taskStatusCancelled, taskStatusInterrupted,
	}
}

// taskInStatuses 为 status IN (...) 生成占位符与参数。状态集合只来自包内常量，
// 不存在 SQL 注入面。
func taskInStatuses(statuses []string) (string, []any) {
	holders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, st := range statuses {
		holders[i] = "?"
		args[i] = st
	}
	return strings.Join(holders, ", "), args
}
