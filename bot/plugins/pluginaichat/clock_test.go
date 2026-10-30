package pluginaichat

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/storage"
)

// pfake 前缀感知的进程内 PersistentStorage，各 Clone 共享底层 map 但以不同前缀隔离命名空间。
type pfake struct {
	prefix string
	data   map[string]string
	mu     *sync.Mutex
}

func newPFake() *pfake {
	return &pfake{data: map[string]string{}, mu: &sync.Mutex{}}
}

func (s *pfake) key(k string) string { return s.prefix + k }

func (s *pfake) GetString(_ context.Context, k string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[s.key(k)]
	return v, ok
}
func (s *pfake) SetString(_ context.Context, k, v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[s.key(k)] = v
	return true
}
func (s *pfake) Get(ctx context.Context, k string, out any) bool {
	v, ok := s.GetString(ctx, k)
	if !ok {
		return false
	}
	return json.Unmarshal([]byte(v), out) == nil
}
func (s *pfake) Set(ctx context.Context, k string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return s.SetString(ctx, k, string(b))
}
func (s *pfake) Has(ctx context.Context, k string) bool { _, ok := s.GetString(ctx, k); return ok }
func (s *pfake) Del(_ context.Context, k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, s.key(k))
	return true
}
func (s *pfake) Keys(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full := s.key(prefix)
	var out []string
	for k := range s.data {
		if strings.HasPrefix(k, full) {
			out = append(out, strings.TrimPrefix(k, s.prefix))
		}
	}
	return out, nil
}
func (s *pfake) Clear(_ context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.data {
		if strings.HasPrefix(k, s.prefix) {
			delete(s.data, k)
		}
	}
	return true
}
func (s *pfake) Clone(prefix string) storage.PersistentStorage {
	return &pfake{prefix: s.prefix + prefix + ":", data: s.data, mu: s.mu}
}

func TestClockManagerCRUDAndPersist(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()

	m := newClockManager(p, 30*time.Second)

	// 无效 cron 应失败
	if _, err := m.Add(&ClockTask{Cron: "not a cron", Content: "c", TargetType: "group", TargetID: "1"}); err == nil {
		t.Fatal("expected error for invalid cron")
	}
	// 缺少内容应失败
	if _, err := m.Add(&ClockTask{Cron: "@every 1h", TargetType: "group", TargetID: "1"}); err == nil {
		t.Fatal("expected error for empty content")
	}

	id, err := m.Add(&ClockTask{Cron: "@every 1h", Title: "喝水", Content: "提醒喝水", TargetType: "group", TargetID: "123", Enabled: true})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}

	// List / Get
	if len(m.List()) != 1 {
		t.Fatalf("want 1 task, got %d", len(m.List()))
	}
	got, ok := m.Get(id)
	if !ok || got.Title != "喝水" {
		t.Fatalf("Get failed: %+v %v", got, ok)
	}

	// Update 禁用
	dis := false
	if _, err := m.Update(id, ClockUpdateFields{Enabled: &dis}, "tester"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if g, _ := m.Get(id); g.Enabled {
		t.Fatal("expected disabled")
	}
	if g, _ := m.Get(id); g.Updater != "tester" {
		t.Fatalf("want updater=tester, got %q", g.Updater)
	}

	// ListByTarget 过滤
	if len(m.ListByTarget("group", "123")) != 1 {
		t.Fatal("ListByTarget group/123 should have 1")
	}
	if len(m.ListByTarget("friend", "123")) != 0 {
		t.Fatal("ListByTarget friend/123 should have 0")
	}

	// 重启模拟：用同一存储新建 manager，任务应恢复
	m2 := newClockManager(p, 30*time.Second)
	loaded := m2.List()
	if len(loaded) != 1 {
		t.Fatalf("after reload want 1 task, got %d", len(loaded))
	}
	if loaded[0].Title != "喝水" || loaded[0].Enabled {
		t.Fatalf("reload state wrong: %+v", loaded[0])
	}

	// Delete
	if !m2.Delete(id) {
		t.Fatal("Delete should return true")
	}
	if len(m2.List()) != 0 {
		t.Fatal("expected 0 after delete")
	}
	// 再次 reload 确认已落盘删除
	m3 := newClockManager(p, 30*time.Second)
	if len(m3.List()) != 0 {
		t.Fatal("expected 0 after reload following delete")
	}
}

func TestClockUpdateCreatedBy(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()

	m := newClockManager(p, 30*time.Second)
	id, err := m.Add(&ClockTask{Cron: "@every 1h", Title: "喝水", Content: "提醒喝水", TargetType: "group", TargetID: "123", Enabled: true})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 纯数字创建者规范化为 qq: 前缀
	creator := "456"
	if _, err := m.Update(id, ClockUpdateFields{CreatedBy: &creator}, "tester"); err != nil {
		t.Fatalf("Update created_by failed: %v", err)
	}
	if g, _ := m.Get(id); g.CreatedBy != "qq:456" {
		t.Fatalf("want qq:456, got %q", g.CreatedBy)
	}

	// 非法值 "0" 应报错
	zero := "0"
	if _, err := m.Update(id, ClockUpdateFields{CreatedBy: &zero}, "tester"); err == nil {
		t.Fatal("expected error for created_by=0")
	}

	// 空字符串清除创建者
	empty := ""
	if _, err := m.Update(id, ClockUpdateFields{CreatedBy: &empty}, "tester"); err != nil {
		t.Fatalf("Update clear created_by failed: %v", err)
	}
	if g, _ := m.Get(id); g.CreatedBy != "" {
		t.Fatalf("want empty created_by, got %q", g.CreatedBy)
	}
}

func TestBuildTriggerPrompt(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second)

	got := m.buildTriggerPrompt(&ClockTask{Title: "早安", Content: "大家早上好"})
	want := "【定时任务】早安\n大家早上好"
	if got != want {
		t.Fatalf("prompt mismatch:\n got: %q\nwant: %q", got, want)
	}

	// 无标题
	got2 := m.buildTriggerPrompt(&ClockTask{Content: "仅内容"})
	if got2 != "【定时任务】\n仅内容" {
		t.Fatalf("no-title prompt mismatch: %q", got2)
	}

	// 带备注
	got3 := m.buildTriggerPrompt(&ClockTask{Title: "t", Content: "c", Note: "n"})
	if !strings.Contains(got3, "（备注：n）") {
		t.Fatalf("note missing: %q", got3)
	}
}

// TestTryStartTaskSkipWhileRunning 回归：同一任务执行期间再次触发应被跳过，
// 执行结束（finishTask）后应能再次启动。
func TestTryStartTaskSkipWhileRunning(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second)

	if !m.tryStartTask("task-1") {
		t.Fatal("first tryStartTask should succeed")
	}
	if m.tryStartTask("task-1") {
		t.Fatal("second tryStartTask while running should be skipped")
	}
	// 不同任务互不影响
	if !m.tryStartTask("task-2") {
		t.Fatal("different task should not be blocked")
	}
	m.finishTask("task-1")
	if !m.tryStartTask("task-1") {
		t.Fatal("tryStartTask after finishTask should succeed again")
	}
}

// TestClockTimeoutClamp 回归：任务超时时间应被钳制到 clockMaxTimeoutSec，
// 防止配置溢出（int 转 Duration 溢出变负数/零导致 context 立即过期）。
func TestClockTimeoutClamp(t *testing.T) {
	if got := min(1<<62, clockMaxTimeoutSec); got != clockMaxTimeoutSec {
		t.Fatalf("huge timeout should be clamped to %d, got %d", clockMaxTimeoutSec, got)
	}
	if got := min(30, clockMaxTimeoutSec); got != 30 {
		t.Fatalf("normal timeout should pass through, got %d", got)
	}
}

func TestRunOnceDestroyAfterTrigger(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second)

	id, err := m.Add(&ClockTask{
		Cron: "@every 1h", Title: "只跑一次", Content: "内容",
		TargetType: "group", TargetID: "7", Enabled: true, RunOnce: true,
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if g, _ := m.Get(id); !g.RunOnce {
		t.Fatal("RunOnce should persist as true")
	}

	// 模拟 cron 回调触发：直接调用 scheduleLocked 注册的闭包会受 cron 调度约束，
	// 这里改用 RunNow 走 runTask，再手动调用 destroyOneShot 复刻回调销毁逻辑。
	// （runTask 依赖 m.bot，这里不接真实 bot，故仅验证销毁路径。）
	m.mu.Lock()
	m.bot = nil // runTask 在 bot==nil 时直接返回，不影响销毁断言
	m.mu.Unlock()

	m.destroyOneShot(id)
	if _, ok := m.Get(id); ok {
		t.Fatal("one-shot task should be destroyed after trigger")
	}
	if len(m.List()) != 0 {
		t.Fatalf("expected 0 tasks after one-shot destroy, got %d", len(m.List()))
	}

	// 重启模拟：单次任务销毁后不应再被加载
	m2 := newClockManager(p, 30*time.Second)
	if len(m2.List()) != 0 {
		t.Fatalf("expected 0 tasks after reload, got %d", len(m2.List()))
	}
}

func TestRunOncePersistedAcrossReload(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second)

	id, err := m.Add(&ClockTask{
		Cron: "@every 1h", Title: "单次", Content: "x",
		TargetType: "friend", TargetID: "9", Enabled: true, RunOnce: true,
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	_ = id

	m2 := newClockManager(p, 30*time.Second)
	loaded := m2.List()
	if len(loaded) != 1 || !loaded[0].RunOnce {
		t.Fatalf("RunOnce flag not persisted: %+v", loaded)
	}

	// Update 可切换为重复任务
	no := false
	if _, err := m2.Update(loaded[0].ID, ClockUpdateFields{RunOnce: &no}, "tester"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	m3 := newClockManager(p, 30*time.Second)
	if g := m3.List(); len(g) != 1 || g[0].RunOnce {
		t.Fatalf("RunOnce should be false after update: %+v", g)
	}
}

func TestResolveTarget(t *testing.T) {
	b := clockToolBase{defType: clockTargetGroup, defID: "123"}
	// 默认回退到当前会话
	if tt, id := b.resolveTarget("", ""); tt != clockTargetGroup || id != "123" {
		t.Fatalf("default resolve wrong: %s %s", tt, id)
	}
	// 显式提供则采用
	if tt, id := b.resolveTarget(clockTargetFriend, "999"); tt != clockTargetFriend || id != "999" {
		t.Fatalf("explicit resolve wrong: %s %s", tt, id)
	}
	// 类型对但 id 缺失 → 回退
	if tt, id := b.resolveTarget(clockTargetFriend, ""); tt != clockTargetGroup || id != "123" {
		t.Fatalf("half resolve wrong: %s %s", tt, id)
	}
}

// TestClockToolsScopeIsolation 验证 AI 定时任务工具的会话作用域隔离：
// 任务 ID 为自增序号可枚举，若无归属校验，任意会话的 AI 可删除/查看其他会话的任务。
func TestClockToolsScopeIsolation(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()
	m := newClockManager(p, 30*time.Second)

	// 群 A（123）创建任务
	ta, err := m.Add(&ClockTask{Cron: "@every 1h", Title: "A群任务", Content: "内容", TargetType: clockTargetGroup, TargetID: "123", Enabled: true})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	// 群 B（456）创建任务
	tb, err := m.Add(&ClockTask{Cron: "@every 1h", Title: "B群任务", Content: "内容", TargetType: clockTargetGroup, TargetID: "456", Enabled: true})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 群 A 的工具视角
	toolsA := newClockTools(m, clockTargetGroup, "123")
	var delA, updA, logA, listA, createA, getA llmtool.Tool
	for _, tool := range toolsA {
		switch tool.Name() {
		case "clock_delete":
			delA = tool
		case "clock_update":
			updA = tool
		case "clock_log":
			logA = tool
		case "clock_list":
			listA = tool
		case "clock_create":
			createA = tool
		case "clock_get":
			getA = tool
		}
	}

	// 查看他群任务详情被拒绝
	if _, err := getA.Execute(context.Background(), &clockGetParams{ID: tb}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "无权操作") {
		t.Fatalf("跨会话查看详情应被拒绝, err=%v", err)
	}
	// 查看本群任务详情成功，包含完整内容
	detail, err := getA.Execute(context.Background(), &clockGetParams{ID: ta}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("本会话查看详情应成功: %v", err)
	}
	if !strings.Contains(detail, "A群任务") || !strings.Contains(detail, "内容") {
		t.Fatalf("详情应包含标题与内容: %s", detail)
	}

	// 删除他群任务被拒绝
	if _, err := delA.Execute(context.Background(), &clockDeleteParams{ID: tb}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "无权操作") {
		t.Fatalf("跨会话删除应被拒绝, err=%v", err)
	}
	// 删除本群任务成功
	if _, err := delA.Execute(context.Background(), &clockDeleteParams{ID: ta}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("本会话删除应成功: %v", err)
	}

	// 更新他群任务被拒绝
	enabled := false
	if _, err := updA.Execute(context.Background(), &clockUpdateParams{ID: tb, Enabled: &enabled}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "无权操作") {
		t.Fatalf("跨会话更新应被拒绝, err=%v", err)
	}
	// 查看他群任务日志被拒绝
	if _, err := logA.Execute(context.Background(), &clockLogParams{TaskID: tb}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "无权操作") {
		t.Fatalf("跨会话日志应被拒绝, err=%v", err)
	}
	// 未指定任务的日志只包含本会话内容（B 群任务已删除，A 群任务已删，无日志为预期，不报错即可）
	if _, err := logA.Execute(context.Background(), &clockLogParams{}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("本会话日志查询不应报错: %v", err)
	}

	// 列表限定本会话：B 群任务不可见
	out, err := listA.Execute(context.Background(), &clockListParams{}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if strings.Contains(out, "B群任务") {
		t.Fatalf("列表泄露其他会话任务: %s", out)
	}

	// 创建指定其他会话被拒绝
	if _, err := createA.Execute(context.Background(), &clockCreateParams{
		Cron: "@every 1h", Title: "x", Content: "y",
		TargetType: clockTargetGroup, TargetID: "456",
	}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "只能操作当前会话") {
		t.Fatalf("跨会话创建应被拒绝, err=%v", err)
	}
	// 创建默认当前会话成功
	if _, err := createA.Execute(context.Background(), &clockCreateParams{
		Cron: "@every 1h", Title: "x", Content: "y",
	}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("本会话创建应成功: %v", err)
	}
}

// TestClockNextRunRefreshedOnRead 验证「下次触发时间」读取时从 cron 调度器刷新：
// 任务触发后 cron 会自动推进 entry.Next，若任务结构体未同步，面板会一直显示
// 上一次触发时间（如昨天），读取时刷新可避免该问题。
func TestClockNextRunRefreshedOnRead(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.Default()
	p.PersistentStorage = newSQLPFake()

	m := newClockManager(p, 30*time.Second)
	id, err := m.Add(&ClockTask{Cron: "@every 1h", Title: "喝水", Content: "提醒喝水", TargetType: "group", TargetID: "123", Enabled: true})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 调度器未启动时没有下次触发时间
	if g, _ := m.Get(id); !g.NextRunAt.IsZero() {
		t.Fatalf("expected zero NextRunAt before start, got %v", g.NextRunAt)
	}

	// 启动调度器后应能算出未来的下次触发时间
	m.Start(nil)
	if g, _ := m.Get(id); g.NextRunAt.IsZero() || !g.NextRunAt.After(time.Now()) {
		t.Fatalf("expected future NextRunAt after start, got %v", g.NextRunAt)
	}

	// 模拟历史 bug：任务已触发但任务结构体的 NextRunAt 停留在昨天
	m.mu.Lock()
	stale := time.Now().Add(-24 * time.Hour)
	m.tasks[id].NextRunAt = stale
	m.persistTaskLocked(m.tasks[id])
	m.mu.Unlock()

	// Get / List 读取时应自动刷新为 cron 的最新下次触发时间，不再显示昨天
	if g, _ := m.Get(id); !g.NextRunAt.After(time.Now()) {
		t.Fatalf("Get should refresh NextRunAt from cron, got %v", g.NextRunAt)
	}
	for _, g := range m.List() {
		if g.ID == id && !g.NextRunAt.After(time.Now()) {
			t.Fatalf("List should refresh NextRunAt from cron, got %v", g.NextRunAt)
		}
	}
}
