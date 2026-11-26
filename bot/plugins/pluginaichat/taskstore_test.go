package pluginaichat

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---- 测试辅助 ----

// newTaskTestStore 打开内存 SQLite 并建好后台任务表；必须单连接，
// 否则每个连接各见一个独立的内存库。
func newTaskTestStore(t *testing.T) (*taskStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	s := newTaskStore(db, testLogger())
	if err := s.EnsureTables(context.Background()); err != nil {
		db.Close()
		t.Fatalf("ensure task tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return s, db
}

// taskFixture 返回一条字段齐全的任务记录（不含 ID，由 Create 生成）。
func taskFixture(sessionKey string, createdAt int64) *agentTask {
	return &agentTask{
		SessionKey: sessionKey,
		ParentID:   "",
		Kind:       taskKindSubagent,
		Status:     taskStatusRunning,
		Title:      "后台调研",
		Prompt:     "调研 Go 并发模型并输出摘要",
		Result:     "进行中",
		Error:      "",
		StopReason: "",
		Tokens:     42,
		Meta:       `{"phase":"tool","tools":3}`,
		CreatedAt:  createdAt,
		StartedAt:  createdAt + 1,
		UpdatedAt:  createdAt + 2,
		FinishedAt: 0,
	}
}

// assertTaskFieldsEqual 逐字段比较任务记录（结构体字段均可比较）。
func assertTaskFieldsEqual(t *testing.T, got, want *agentTask) {
	t.Helper()
	if got == nil || want == nil {
		t.Fatalf("任务记录为 nil: got=%v want=%v", got, want)
	}
	if *got != *want {
		t.Fatalf("任务字段不符:\n got %+v\nwant %+v", got, want)
	}
}

// ---- 建表 ----

// TestTaskEnsureTablesIdempotent 建表幂等：重复 EnsureTables 不报错，
// 表与两个索引都存在。
func TestTaskEnsureTablesIdempotent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)

	if err := s.EnsureTables(ctx); err != nil {
		t.Fatalf("第二次 EnsureTables 报错: %v", err)
	}

	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ania_agent_task'`).Scan(&tables); err != nil {
		t.Fatalf("查询 sqlite_master: %v", err)
	}
	if tables != 1 {
		t.Fatalf("ania_agent_task 表数量 = %d, want 1", tables)
	}
	for _, idx := range []string{"idx_agent_task_session", "idx_agent_task_status"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil {
			t.Fatalf("查询索引 %s: %v", idx, err)
		}
		if n != 1 {
			t.Fatalf("索引 %s 数量 = %d, want 1", idx, n)
		}
	}
}

// ---- CRUD ----

// TestTaskCreateGetUpdateRoundTrip Create/Get/Update 全字段往返，覆盖空串与 0 值，
// 并验证 ID 自动生成、时间戳补齐、重复 ID 报错、Create 不覆盖显式时间戳。
func TestTaskCreateGetUpdateRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskTestStore(t)

	want := &agentTask{
		SessionKey: "g:1001",
		ParentID:   "t-parent-001",
		Kind:       taskKindHandoff,
		Status:     taskStatusRunning,
		Title:      "整理周报",
		Prompt:     "汇总本周变更并输出周报",
		Result:     "",
		Error:      "",
		StopReason: "",
		Tokens:     1024,
		Meta:       `{"phase":"running","tools":5}`,
		CreatedAt:  1_700_000_000,
		StartedAt:  1_700_000_100,
		UpdatedAt:  1_700_000_200,
		FinishedAt: 0,
	}
	if err := s.Create(ctx, want); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if want.ID == "" || !strings.HasPrefix(want.ID, "t-") {
		t.Fatalf("空 ID 未自动生成: %q", want.ID)
	}
	if want.CreatedAt != 1_700_000_000 || want.UpdatedAt != 1_700_000_200 {
		t.Fatalf("Create 不应覆盖非 0 时间戳: %+v", want)
	}

	got, ok, err := s.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get 未找到刚创建的任务 %s", want.ID)
	}
	assertTaskFieldsEqual(t, got, want)

	// 不存在的 ID：bool 为 false，不报错
	missing, ok, err := s.Get(ctx, "t-not-exist")
	if err != nil || ok || missing != nil {
		t.Fatalf("Get 不存在 ID = (%v, %v, %v), want (nil, false, nil)", missing, ok, err)
	}

	// 重复 ID 返回错误（非 Upsert）
	dup := *want
	if err := s.Create(ctx, &dup); err == nil {
		t.Fatal("重复 ID 的 Create 应返回错误")
	}

	// 全字段更新为「空串 / 0 值 + 终态」；CreatedAt 保持不变、UpdatedAt 置 now
	before := time.Now().Unix()
	updated := &agentTask{
		ID:         want.ID,
		SessionKey: "g:1001",
		ParentID:   "",
		Kind:       taskKindHandoff,
		Status:     taskStatusCompleted,
		Title:      "",
		Prompt:     "",
		Result:     "",
		Error:      "",
		StopReason: taskStopCompleted,
		Tokens:     0,
		Meta:       "",
		CreatedAt:  0,
		StartedAt:  0,
		UpdatedAt:  0,
		FinishedAt: 1_700_000_500,
	}
	if err := s.Update(ctx, updated); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after := time.Now().Unix()
	if updated.UpdatedAt < before || updated.UpdatedAt > after {
		t.Fatalf("Update 后 UpdatedAt = %d, want [%d, %d]", updated.UpdatedAt, before, after)
	}

	got, ok, err = s.Get(ctx, want.ID)
	if err != nil || !ok {
		t.Fatalf("更新后 Get = (%v, %v, %v)", got, ok, err)
	}
	updated.CreatedAt = want.CreatedAt // Update 不改写 created_at，落库值仍是创建时间
	assertTaskFieldsEqual(t, got, updated)
}

// TestTaskUpdateProgress 执行中进度增量更新只改 meta/updated_at：状态、结果、
// token 等字段保持不动；任务收尾（finished_at 非 0）后进度写不再生效，
// 迟到的进度不会把终态覆盖回「运行中」。
func TestTaskUpdateProgress(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskTestStore(t)

	if _, err := s.UpdateProgress(ctx, "", `{"tool_calls_total":0}`); err == nil {
		t.Fatal("空任务 ID 的 UpdateProgress 应返回错误")
	}

	task := taskFixture("g:1001", 1_700_000_000)
	if err := s.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := time.Now().Unix()
	n, err := s.UpdateProgress(ctx, task.ID, `{"tool_calls_total":1}`)
	if err != nil || n != 1 {
		t.Fatalf("UpdateProgress = (%d, %v), want (1, nil)", n, err)
	}

	got, ok, err := s.Get(ctx, task.ID)
	if err != nil || !ok {
		t.Fatalf("Get = (%v, %v, %v)", got, ok, err)
	}
	if got.Meta != `{"tool_calls_total":1}` {
		t.Fatalf("进度写后 Meta = %q, want 进度 JSON", got.Meta)
	}
	if got.Status != taskStatusRunning || got.Result != task.Result || got.Tokens != task.Tokens ||
		got.StopReason != "" || got.Error != "" || got.FinishedAt != 0 {
		t.Fatalf("进度写不应改动其它字段: %+v", got)
	}
	if got.UpdatedAt < before {
		t.Fatalf("进度写后 UpdatedAt = %d, want >= %d", got.UpdatedAt, before)
	}

	// 收尾：状态、结果、token 与 finished_at 落库
	task.Status = taskStatusCompleted
	task.StopReason = taskStopCompleted
	task.Result = "调研结论"
	task.Tokens = 120
	task.Meta = `{"iterations":2}`
	task.FinishedAt = time.Now().Unix()
	if err := s.Update(ctx, task); err != nil {
		t.Fatalf("Update: %v", err)
	}

	n, err = s.UpdateProgress(ctx, task.ID, `{"tool_calls_total":9}`)
	if err != nil || n != 0 {
		t.Fatalf("收尾后的 UpdateProgress = (%d, %v), want (0, nil)", n, err)
	}
	final, ok, err := s.Get(ctx, task.ID)
	if err != nil || !ok {
		t.Fatalf("收尾后 Get = (%v, %v, %v)", final, ok, err)
	}
	if final.Status != taskStatusCompleted || final.Meta != `{"iterations":2}` || final.Result != "调研结论" {
		t.Fatalf("迟到的进度写覆盖了终态: %+v", final)
	}
}

// TestTaskCreateFillsDefaultsAndEmptyFields Create 为空 ID 自动生成 ID、
// CreatedAt/UpdatedAt 为 0 时填 now；未提供的文本字段落盘为空串、数值为 0，
// 读取时不会变成 NULL 扫描错误。
func TestTaskCreateFillsDefaultsAndEmptyFields(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskTestStore(t)

	if err := s.Create(ctx, nil); err == nil {
		t.Fatal("Create(nil) 应返回错误")
	}

	before := time.Now().Unix()
	task := &agentTask{
		SessionKey: "f:42",
		Kind:       taskKindClock,
		Status:     taskStatusQueued,
	}
	if err := s.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	after := time.Now().Unix()
	if task.ID == "" || !strings.HasPrefix(task.ID, "t-") {
		t.Fatalf("空 ID 未自动生成: %q", task.ID)
	}
	if task.CreatedAt < before || task.CreatedAt > after {
		t.Fatalf("CreatedAt = %d, want [%d, %d]", task.CreatedAt, before, after)
	}
	if task.UpdatedAt < before || task.UpdatedAt > after {
		t.Fatalf("UpdatedAt = %d, want [%d, %d]", task.UpdatedAt, before, after)
	}

	got, ok, err := s.Get(ctx, task.ID)
	if err != nil || !ok {
		t.Fatalf("Get = (%v, %v, %v)", got, ok, err)
	}
	// Create 未做默认化的字段（空串/0）应原样落盘，且 Create 回写的时间戳一致
	assertTaskFieldsEqual(t, got, task)
}

// ---- 会话列表 ----

// TestTaskListBySessionOrderAndLimit 按 created_at 倒序、limit 生效
// （limit<=0 取默认 50）、会话之间隔离。
func TestTaskListBySessionOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskTestStore(t)

	var gTasks []*agentTask
	for i := int64(1); i <= 5; i++ {
		task := taskFixture("g:1", 1_700_000_000+i*10)
		if err := s.Create(ctx, task); err != nil {
			t.Fatalf("Create g:1 #%d: %v", i, err)
		}
		gTasks = append(gTasks, task)
	}
	for i := int64(1); i <= 2; i++ {
		if err := s.Create(ctx, taskFixture("f:1", 1_700_000_000+i*10)); err != nil {
			t.Fatalf("Create f:1 #%d: %v", i, err)
		}
	}

	list, err := s.ListBySession(ctx, "g:1", 0)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("g:1 任务数 = %d, want 5", len(list))
	}
	// 倒序：最新的 created_at 在前
	for i := 1; i < len(list); i++ {
		if list[i-1].CreatedAt < list[i].CreatedAt {
			t.Fatalf("未按 created_at 倒序: [%d]=%d, [%d]=%d",
				i-1, list[i-1].CreatedAt, i, list[i].CreatedAt)
		}
	}
	// 内容与创建记录一一对应（ID 集合相同）
	seen := make(map[string]struct{}, len(gTasks))
	for _, task := range gTasks {
		seen[task.ID] = struct{}{}
	}
	for _, task := range list {
		if _, ok := seen[task.ID]; !ok {
			t.Fatalf("ListBySession 返回了其他会话的任务 %s", task.ID)
		}
	}

	// limit 生效
	limited, err := s.ListBySession(ctx, "g:1", 2)
	if err != nil {
		t.Fatalf("ListBySession limit=2: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit=2 返回 %d 条, want 2", len(limited))
	}
	if limited[0].CreatedAt != 1_700_000_050 || limited[1].CreatedAt != 1_700_000_040 {
		t.Fatalf("limit=2 的最新两条不符: %d, %d", limited[0].CreatedAt, limited[1].CreatedAt)
	}
	// 负数 limit 走默认（50 条上限内返回全部）
	neg, err := s.ListBySession(ctx, "g:1", -3)
	if err != nil || len(neg) != 5 {
		t.Fatalf("负 limit 返回 %d 条(错误 %v), want 5", len(neg), err)
	}
	// 会话隔离
	other, err := s.ListBySession(ctx, "g:2", 0)
	if err != nil {
		t.Fatalf("ListBySession g:2: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("g:2 不应有任务, got %d", len(other))
	}
	fList, err := s.ListBySession(ctx, "f:1", 10)
	if err != nil || len(fList) != 2 {
		t.Fatalf("f:1 任务数 = %d(错误 %v), want 2", len(fList), err)
	}
}

// ---- 未结束任务 ----

// TestTaskListRunning 只返回 queued/running/waiting 三态，按 created_at 正序。
func TestTaskListRunning(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskTestStore(t)

	cases := []struct {
		status    string
		createdAt int64
	}{
		{taskStatusQueued, 300},
		{taskStatusRunning, 100},
		{taskStatusWaiting, 200},
		{taskStatusCompleted, 400},
		{taskStatusFailed, 500},
		{taskStatusTimeout, 600},
		{taskStatusCancelled, 700},
		{taskStatusInterrupted, 800},
	}
	for _, c := range cases {
		task := taskFixture("g:1", c.createdAt)
		task.Status = c.status
		if err := s.Create(ctx, task); err != nil {
			t.Fatalf("Create %s: %v", c.status, err)
		}
	}

	list, err := s.ListRunning(ctx)
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListRunning 返回 %d 条, want 3", len(list))
	}
	wantStatuses := []string{taskStatusRunning, taskStatusWaiting, taskStatusQueued}
	wantCreated := []int64{100, 200, 300}
	for i, task := range list {
		if task.Status != wantStatuses[i] {
			t.Fatalf("第 %d 条状态 = %q, want %q", i, task.Status, wantStatuses[i])
		}
		if task.CreatedAt != wantCreated[i] {
			t.Fatalf("第 %d 条 created_at = %d, want %d（应按 created_at 正序）",
				i, task.CreatedAt, wantCreated[i])
		}
	}
}

// TestTaskMarkInterrupted 启动恢复：三态统一标记为 interrupted，
// 返回影响行数；已结束任务不受影响；重复调用返回 0。
func TestTaskMarkInterrupted(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskTestStore(t)

	var active []*agentTask
	for i, status := range []string{taskStatusQueued, taskStatusRunning, taskStatusWaiting} {
		task := taskFixture("g:1", 1_700_000_000+int64(i))
		task.Status = status
		task.StartedAt = 1_700_000_100
		task.StopReason = ""
		task.FinishedAt = 0
		if err := s.Create(ctx, task); err != nil {
			t.Fatalf("Create %s: %v", status, err)
		}
		active = append(active, task)
	}
	// 已结束的任务不应被改写
	done := taskFixture("g:1", 1_700_000_010)
	done.Status = taskStatusCompleted
	done.StopReason = taskStopCompleted
	done.FinishedAt = 1_700_000_200
	if err := s.Create(ctx, done); err != nil {
		t.Fatalf("Create completed: %v", err)
	}

	before := time.Now().Unix()
	n, err := s.MarkInterrupted(ctx)
	after := time.Now().Unix()
	if err != nil {
		t.Fatalf("MarkInterrupted: %v", err)
	}
	if n != 3 {
		t.Fatalf("影响行数 = %d, want 3", n)
	}
	for _, want := range active {
		got, ok, err := s.Get(ctx, want.ID)
		if err != nil || !ok {
			t.Fatalf("Get %s = (%v, %v, %v)", want.ID, got, ok, err)
		}
		if got.Status != taskStatusInterrupted {
			t.Fatalf("状态 = %q, want %q", got.Status, taskStatusInterrupted)
		}
		if got.StopReason != taskStopAborted {
			t.Fatalf("stop_reason = %q, want %q", got.StopReason, taskStopAborted)
		}
		if got.FinishedAt < before || got.FinishedAt > after {
			t.Fatalf("finished_at = %d, want [%d, %d]", got.FinishedAt, before, after)
		}
		if got.UpdatedAt < before || got.UpdatedAt > after {
			t.Fatalf("updated_at = %d, want [%d, %d]", got.UpdatedAt, before, after)
		}
	}
	gotDone, ok, err := s.Get(ctx, done.ID)
	if err != nil || !ok {
		t.Fatalf("Get completed = (%v, %v, %v)", gotDone, ok, err)
	}
	assertTaskFieldsEqual(t, gotDone, done)

	// 没有未结束任务后再次调用返回 0
	if n, err := s.MarkInterrupted(ctx); err != nil || n != 0 {
		t.Fatalf("再次 MarkInterrupted = (%d, %v), want (0, nil)", n, err)
	}
}

// ---- 清理 ----

// TestTaskDeleteFinishedBefore 只删终态且 finished_at 早于 cutoff 的记录；
// 未结束任务、finished_at=0、finished_at>=cutoff 的记录都保留。
func TestTaskDeleteFinishedBefore(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	const cutoff = int64(1_700_001_000)

	type deleteCase struct {
		task       *agentTask
		wantDelete bool
	}
	cases := []deleteCase{
		// 终态且早于 cutoff：删除
		{task: taskFixture("g:1", 1_700_000_001), wantDelete: true},
		{task: taskFixture("g:1", 1_700_000_002), wantDelete: true},
		{task: taskFixture("g:1", 1_700_000_003), wantDelete: true},
		{task: taskFixture("g:1", 1_700_000_004), wantDelete: true},
		{task: taskFixture("g:1", 1_700_000_005), wantDelete: true},
		// 已结束但不够旧：保留
		{task: taskFixture("g:1", 1_700_000_006), wantDelete: false},
		// 未结束：即使 finished_at 异常早也保留
		{task: taskFixture("g:1", 1_700_000_007), wantDelete: false},
		// 终态但 finished_at=0（未落结束时间）：按「未结束」保留
		{task: taskFixture("g:1", 1_700_000_008), wantDelete: false},
	}
	statuses := []string{
		taskStatusCompleted, taskStatusFailed, taskStatusTimeout,
		taskStatusCancelled, taskStatusInterrupted,
		taskStatusCompleted, taskStatusRunning, taskStatusCompleted,
	}
	finished := []int64{cutoff - 100, cutoff - 1, cutoff - 500, cutoff - 200,
		cutoff - 300, cutoff, cutoff - 100, 0}
	for i := range cases {
		cases[i].task.Status = statuses[i]
		cases[i].task.FinishedAt = finished[i]
		if err := s.Create(ctx, cases[i].task); err != nil {
			t.Fatalf("Create case %d: %v", i, err)
		}
	}

	n, err := s.DeleteFinishedBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteFinishedBefore: %v", err)
	}
	if n != 5 {
		t.Fatalf("删除行数 = %d, want 5", n)
	}

	var remain int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ania_agent_task`).Scan(&remain); err != nil {
		t.Fatalf("统计剩余任务: %v", err)
	}
	if remain != 3 {
		t.Fatalf("剩余任务数 = %d, want 3", remain)
	}
	for i, c := range cases {
		_, ok, err := s.Get(ctx, c.task.ID)
		if err != nil {
			t.Fatalf("Get case %d: %v", i, err)
		}
		if ok == c.wantDelete {
			t.Fatalf("case %d(status=%s, finished_at=%d) 存在 = %v, wantDeleted = %v",
				i, c.task.Status, c.task.FinishedAt, ok, c.wantDelete)
		}
	}
}

// ---- ID 生成 ----

// TestTaskNewID ID 非空、格式为 t-<36 进制纳秒>-<4 位 hex>，1000 次无重复。
func TestTaskNewID(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := newTaskID()
		if id == "" {
			t.Fatal("newTaskID 返回空串")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("第 %d 次生成重复 ID: %s", i, id)
		}
		seen[id] = struct{}{}

		parts := strings.Split(id, "-")
		if len(parts) != 3 || parts[0] != "t" {
			t.Fatalf("ID 格式不符: %q", id)
		}
		if _, err := strconv.ParseInt(parts[1], 36, 64); err != nil {
			t.Fatalf("纳秒部分不是 36 进制: %q (%v)", parts[1], err)
		}
		if len(parts[2]) != 4 {
			t.Fatalf("随机后缀长度 = %d, want 4: %q", len(parts[2]), id)
		}
		if _, err := hex.DecodeString(parts[2]); err != nil {
			t.Fatalf("随机后缀不是 hex: %q (%v)", parts[2], err)
		}
	}
}
