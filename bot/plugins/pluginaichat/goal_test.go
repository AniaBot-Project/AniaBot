package pluginaichat

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
	_ "modernc.org/sqlite"
)

// ---- 测试辅助 ----

// newGoalTestStore 打开内存 SQLite 并建好目标表；必须单连接。
func newGoalTestStore(t *testing.T) (*goalStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	s := newGoalStore(db, testLogger())
	if err := s.EnsureTables(context.Background()); err != nil {
		db.Close()
		t.Fatalf("ensure goal tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return s, db
}

// newGoalTestPlugin 构造带目标存储的测试插件（嵌入字段不能在字面量里直接赋值）。
func newGoalTestPlugin(t *testing.T) *AIChatPlugin {
	store, _ := newGoalTestStore(t)
	p := &AIChatPlugin{Meta: plugin.Meta{Name: "AI对话插件"}, goalStore: store}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.goalArmed = sync.Map{}
	p.cfg.Goal.Enable = true
	p.cfg.Goal.MaxRounds = 5
	return p
}

// syncGoBot 把 b.Go 变成本协程同步执行（带 recover），便于确定性测试运行器。
type syncGoBot struct{ *recordBot }

func (b syncGoBot) Go(_ string, f func()) {
	defer func() { _ = recover() }()
	f()
}

// noopGoBot 丢弃 b.Go 的后台执行：只关心目标状态迁移、不想真的跑回合时使用。
type noopGoBot struct{ *recordBot }

func (noopGoBot) Go(string, func()) {}

// blockingGoalStore 包装真实存储，在第一次 Get 时阻塞：续跑驱动此时已进入
// goalMu 保护的「读-改-写」段（读到目标快照之前），用它把临界区固定住。
// 只阻塞第一次：后续 Get（例如 /goal clear 的读取）照常返回，否则会掩盖被测的
// 串行化行为。
type blockingGoalStore struct {
	*goalStore
	entered chan struct{}
	release chan struct{}
	first   atomic.Bool
}

func (s *blockingGoalStore) Get(ctx context.Context, sessionKey string) (*agentGoal, bool, error) {
	if s.first.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return s.goalStore.Get(ctx, sessionKey)
}

// ---- 存储层 ----

func TestGoalStoreRoundTrip(t *testing.T) {
	store, _ := newGoalTestStore(t)
	ctx := context.Background()
	key := "g:qq:1"

	if _, ok, err := store.Get(ctx, key); err != nil || ok {
		t.Fatalf("空库 Get: ok=%v err=%v", ok, err)
	}
	g := &agentGoal{SessionKey: key, Objective: "把模块 A 迁移到 B", Phase: goalPhaseActive, MaxRounds: 10}
	if err := store.Upsert(ctx, g); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, ok, err := store.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Objective != g.Objective || got.Phase != goalPhaseActive || got.MaxRounds != 10 || got.RoundsStarted != 0 {
		t.Fatalf("往返字段不一致: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("时间戳未填充: %+v", got)
	}

	got.Phase = goalPhasePaused
	got.RoundsStarted = 3
	if err := store.Upsert(ctx, got); err != nil {
		t.Fatalf("更新 Upsert: %v", err)
	}
	again, _, _ := store.Get(ctx, key)
	if again.Phase != goalPhasePaused || again.RoundsStarted != 3 {
		t.Fatalf("更新未生效: %+v", again)
	}

	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("paused 不应出现在 active 列表: %+v", active)
	}
	again.Phase = goalPhaseActive
	_ = store.Upsert(ctx, again)
	if active, _ = store.ListActive(ctx); len(active) != 1 {
		t.Fatalf("active 列表应有 1 条, got %d", len(active))
	}

	if err := store.Clear(ctx, key); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok, _ := store.Get(ctx, key); ok {
		t.Fatal("Clear 后不应再读到目标")
	}
}

// ---- 工具 ----

func TestGoalToolsLifecycle(t *testing.T) {
	p := newGoalTestPlugin(t)
	tools := newGoalTools(p, message.FromUint64(1001), true)
	if len(tools) != 3 {
		t.Fatalf("目标工具数 = %d, want 3", len(tools))
	}
	create, update, status := tools[0], tools[1], tools[2]
	if create.Name() != "goal_create" || update.Name() != "goal_update" || status.Name() != "goal_status" {
		t.Fatalf("工具名不符: %s/%s/%s", create.Name(), update.Name(), status.Name())
	}

	out, err := create.Execute(context.Background(), &goalCreateParams{Objective: "完成多阶段迁移", MaxRounds: 3}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(out, "3 轮") {
		t.Fatalf("create: out=%q err=%v", out, err)
	}
	if !p.goalIsArmed(sessionKey(message.FromUint64(1001), true)) {
		t.Fatal("create 后目标应被武装")
	}
	if g, ok, _ := p.loadGoal(sessionKey(message.FromUint64(1001), true)); !ok || g.Phase != goalPhaseActive || g.MaxRounds != 3 {
		t.Fatalf("目标未正确落库: %+v ok=%v", g, ok)
	}

	// 重复创建应被拒绝（已有进行中的目标）
	if out, _ = create.Execute(context.Background(), &goalCreateParams{Objective: "另一个"}, llmtool.CallBackFuncs{}); !strings.Contains(out, "已有进行中的目标") {
		t.Fatalf("重复创建应被拒绝, got %q", out)
	}

	// 暂停
	if out, err = update.Execute(context.Background(), &goalUpdateParams{Action: "pause"}, llmtool.CallBackFuncs{}); err != nil || !strings.Contains(out, "paused") {
		t.Fatalf("pause: out=%q err=%v", out, err)
	}
	if p.goalIsArmed(sessionKey(message.FromUint64(1001), true)) {
		t.Fatal("pause 后应解除武装")
	}
	// 恢复（轮数余额内）
	if out, err = update.Execute(context.Background(), &goalUpdateParams{Action: "resume"}, llmtool.CallBackFuncs{}); err != nil || !strings.Contains(out, "active") {
		t.Fatalf("resume: out=%q err=%v", out, err)
	}
	// blocked 必须给原因
	if _, err = update.Execute(context.Background(), &goalUpdateParams{Action: "blocked"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("blocked 缺少 reason 应报错")
	}
	if out, err = update.Execute(context.Background(), &goalUpdateParams{Action: "blocked", Reason: "缺少上游权限"}, llmtool.CallBackFuncs{}); err != nil || !strings.Contains(out, "blocked") {
		t.Fatalf("blocked: out=%q err=%v", out, err)
	}
	// 完成后可建立新目标
	if out, err = update.Execute(context.Background(), &goalUpdateParams{Action: "complete", Evidence: "验收通过"}, llmtool.CallBackFuncs{}); err != nil || !strings.Contains(out, "complete") {
		t.Fatalf("complete: out=%q err=%v", out, err)
	}
	if out, err = create.Execute(context.Background(), &goalCreateParams{Objective: "新目标"}, llmtool.CallBackFuncs{}); err != nil || !strings.Contains(out, "已建立") {
		t.Fatalf("完成后应可建新目标: out=%q err=%v", out, err)
	}
	// status 文本
	if out, _ = status.Execute(context.Background(), &goalStatusParams{}, llmtool.CallBackFuncs{}); !strings.Contains(out, "新目标") {
		t.Fatalf("status 应包含目标内容, got %q", out)
	}
	// clear
	if out, err = update.Execute(context.Background(), &goalUpdateParams{Action: "clear"}, llmtool.CallBackFuncs{}); err != nil || !strings.Contains(out, "已清除") {
		t.Fatalf("clear: out=%q err=%v", out, err)
	}
}

func TestGoalToolDisabledWithoutStore(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	tools := newGoalTools(p, message.FromUint64(1), false)
	out, err := tools[0].Execute(context.Background(), &goalCreateParams{Objective: "x"}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(out, "未启用") {
		t.Fatalf("无存储时 create 应提示未启用: out=%q err=%v", out, err)
	}
}

// ---- 续跑驱动 ----

func TestMaybeDriveGoal(t *testing.T) {
	id := message.FromUint64(1001)
	key := sessionKey(id, true)

	t.Run("未武装不续跑", func(t *testing.T) {
		p := newGoalTestPlugin(t)
		g := &agentGoal{SessionKey: key, Objective: "目标", Phase: goalPhaseActive, MaxRounds: 5}
		if err := p.saveGoal(g); err != nil {
			t.Fatalf("save: %v", err)
		}
		rb := &recordBot{}
		p.maybeDriveGoal(syncGoBot{rb}, id, true)
		if p.pendingCount(key) != 0 {
			t.Fatal("未武装时不应入队续跑消息")
		}
	})

	t.Run("已武装且空闲时开启下一轮", func(t *testing.T) {
		p := newGoalTestPlugin(t)
		g := &agentGoal{SessionKey: key, Objective: "目标", Phase: goalPhaseActive, MaxRounds: 5}
		if err := p.saveGoal(g); err != nil {
			t.Fatalf("save: %v", err)
		}
		p.armGoal(key)
		rb := &recordBot{}
		p.maybeDriveGoal(syncGoBot{rb}, id, true)
		got, _, _ := p.loadGoal(key)
		if got.RoundsStarted != 1 {
			t.Fatalf("应推进 1 轮, got %d", got.RoundsStarted)
		}
	})

	t.Run("有运行中子代理时不续跑", func(t *testing.T) {
		p := newGoalTestPlugin(t)
		g := &agentGoal{SessionKey: key, Objective: "目标", Phase: goalPhaseActive, MaxRounds: 5}
		_ = p.saveGoal(g)
		p.armGoal(key)
		p.getAsyncGroup(id, true).add("t-sub", func() {}, "调研")
		rb := &recordBot{}
		p.maybeDriveGoal(syncGoBot{rb}, id, true)
		if got, _, _ := p.loadGoal(key); got.RoundsStarted != 0 {
			t.Fatalf("子代理运行中不应续跑, got rounds=%d", got.RoundsStarted)
		}
	})

	t.Run("达到轮数上限自动阻塞并通知", func(t *testing.T) {
		p := newGoalTestPlugin(t)
		g := &agentGoal{SessionKey: key, Objective: "目标", Phase: goalPhaseActive, MaxRounds: 2, RoundsStarted: 2}
		_ = p.saveGoal(g)
		p.armGoal(key)
		rb := &recordBot{}
		p.maybeDriveGoal(syncGoBot{rb}, id, true)
		got, _, _ := p.loadGoal(key)
		if got.Phase != goalPhaseBlocked || got.BlockedCode != goalBlockRoundLimit {
			t.Fatalf("达到上限应阻塞: %+v", got)
		}
		if p.goalIsArmed(key) {
			t.Fatal("阻塞后应解除武装")
		}
		if !containsSent(rb, "最大自动续跑轮数") {
			t.Fatalf("应通知用户, got %v", rb.sent())
		}
	})
}

// ---- /goal 命令 ----

func TestHandleGoalCommand(t *testing.T) {
	p := newGoalTestPlugin(t)
	id := message.FromUint64(1001)
	key := sessionKey(id, true)
	_ = p.saveGoal(&agentGoal{SessionKey: key, Objective: "目标", Phase: goalPhaseActive, MaxRounds: 5})
	p.armGoal(key)

	// 无参数 = 查看状态
	rb := &recordBot{}
	handled, err := p.handleGoalCommand(rb, commandWithArgs(""), testGroupMsg(1001, "/goal"))
	if err != nil || handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if !containsSent(rb, "【长任务目标】") {
		t.Fatalf("应输出状态, got %v", rb.sent())
	}

	// pause
	rb = &recordBot{}
	if _, err := p.handleGoalCommand(rb, commandWithArgs("pause"), testGroupMsg(1001, "/goal pause")); err != nil {
		t.Fatalf("pause err: %v", err)
	}
	if got, _, _ := p.loadGoal(key); got.Phase != goalPhasePaused {
		t.Fatalf("pause 后状态 = %s", got.Phase)
	}
	// resume 应重新武装
	rb = &recordBot{}
	if _, err := p.handleGoalCommand(rb, commandWithArgs("resume"), testGroupMsg(1001, "/goal resume")); err != nil {
		t.Fatalf("resume err: %v", err)
	}
	if !p.goalIsArmed(key) {
		t.Fatal("resume 后应重新武装")
	}
	// clear
	if _, err := p.handleGoalCommand(&recordBot{}, commandWithArgs("clear"), testGroupMsg(1001, "/goal clear")); err != nil {
		t.Fatalf("clear err: %v", err)
	}
	if _, ok, _ := p.loadGoal(key); ok {
		t.Fatal("clear 后目标应不存在")
	}
}

// TestGoalClearSerializedWithDrive 防回归：续跑驱动进入「读-改-写」段（持有
// goalMu、尚未写回）时，用户发送 /goal clear，清除必须等这段结束后再执行。
//
// 修复前的线上交错是：驱动读到目标快照 → /goal clear 删除该行 → 驱动 saveGoal，
// 而 goalStore.Upsert 在行缺失时会补 INSERT，于是 clear 之后目标又被复活（CI 上
// TestHandleGoalCommand 的偶发失败即由此而来）。这里用阻塞存储把驱动固定在临界区
// 内，断言清除不会插队；去掉任一方的 goalMu，该用例都会失败。
func TestGoalClearSerializedWithDrive(t *testing.T) {
	real, _ := newGoalTestStore(t)
	store := &blockingGoalStore{
		goalStore: real,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	p := &AIChatPlugin{Meta: plugin.Meta{Name: "AI对话插件"}, goalStore: store}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.goalArmed = sync.Map{}
	p.cfg.Goal.Enable = true
	p.cfg.Goal.MaxRounds = 5

	id := message.FromUint64(1001)
	key := sessionKey(id, true)
	if err := p.saveGoal(&agentGoal{SessionKey: key, Objective: "目标", Phase: goalPhaseActive, MaxRounds: 5}); err != nil {
		t.Fatalf("save: %v", err)
	}
	p.armGoal(key)

	driveDone := make(chan struct{})
	go func() {
		defer close(driveDone)
		p.maybeDriveGoal(noopGoBot{&recordBot{}}, id, true)
	}()
	<-store.entered // 驱动已读到目标快照，即将写回

	clearErr := make(chan error, 1)
	cleared := make(chan struct{})
	go func() {
		defer close(cleared)
		_, err := p.handleGoalCommand(&recordBot{}, commandWithArgs("clear"), testGroupMsg(1001, "/goal clear"))
		clearErr <- err
	}()

	select {
	case <-cleared:
		// 未串行化：驱动还在读-改-写段内，清除就已经完成了。
		close(store.release)
		<-driveDone
		t.Fatalf("清除不应在续跑驱动的读-改-写完成前执行（clear err: %v）", <-clearErr)
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	<-driveDone
	<-cleared
	if err := <-clearErr; err != nil {
		t.Fatalf("clear err: %v", err)
	}

	if _, ok, err := p.loadGoal(key); err != nil {
		t.Fatalf("load: %v", err)
	} else if ok {
		t.Fatal("clear 之后目标被续跑驱动复活")
	}
	if p.goalIsArmed(key) {
		t.Fatal("clear 之后目标不应仍处于武装状态")
	}
}

// commandWithArgs 构造带一个参数的命令。
func commandWithArgs(arg string) command.Command {
	c := command.Command{Name: "goal"}
	if arg != "" {
		c.Args = []string{arg}
	}
	return c
}
