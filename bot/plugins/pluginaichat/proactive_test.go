package pluginaichat

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"

	_ "modernc.org/sqlite"
)

// ---- 测试脚手架 ----

func newTestIntentionStore(t *testing.T) *intentionStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	store, err := newIntentionStore(db, storage.SQLDialectSQLite, testLogger())
	if err != nil {
		db.Close()
		t.Fatalf("ensure tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store
}

// newTestProactiveManager 构造一个不上报、不落盘的调度器，用于闸门 / 现场逻辑测试。
func newTestProactiveManager(t *testing.T) *proactiveManager {
	t.Helper()
	return &proactiveManager{
		logger:      testLogger(),
		store:       newTestIntentionStore(t),
		scopes:      map[string]*scopeActivity{},
		overrides:   map[string]string{},
		lastIdle:    map[string]time.Time{},
		running:     map[string]struct{}{},
		awaiting:    map[string]struct{}{},
		baseScopes:  map[string]struct{}{},
		idle:        90 * time.Minute,
		minGap:      60 * time.Minute,
		expire:      30 * time.Minute,
		outcomeWin:  10 * time.Minute,
		cooldown:    6 * time.Hour,
		strikes:     3,
		dailyLimit:  3,
		globalDaily: 20,
		maxContext:  30,
		timeout:     120 * time.Second,
		tick:        time.Minute,
	}
}

// testNoon 返回「本地时间今天 12:00」，避开跨日边界造成的测试抖动。
func testNoon() time.Time {
	base := time.Now()
	return time.Date(base.Year(), base.Month(), base.Day(), 12, 0, 0, 0, base.Location())
}

func mustInsert(t *testing.T, store *intentionStore, it *ProactiveIntention) {
	t.Helper()
	if !store.insert(it) {
		t.Fatalf("insert intention %s failed", it.ID)
	}
}

// ---- scope / 时段解析 ----

func TestParseScope(t *testing.T) {
	cases := []struct {
		in      string
		id      message.QID
		isGroup bool
		ok      bool
	}{
		{"g:123", "123", true, true},
		{"f:456", "456", false, true},
		{"g:tg:-100123", "tg:-100123", true, true}, // 平台 ID 本身含冒号
		{"123", "", false, false},
		{"x:123", "", false, false},
		{"g:", "", false, false},
	}
	for _, c := range cases {
		id, isGroup, ok := parseScope(c.in)
		if ok != c.ok || id != c.id || isGroup != c.isGroup {
			t.Fatalf("parseScope(%q) = (%q,%v,%v), want (%q,%v,%v)",
				c.in, id, isGroup, ok, c.id, c.isGroup, c.ok)
		}
	}
}

func TestParseQuietHours(t *testing.T) {
	cases := []struct {
		spec      string
		s, e      int
		ok        bool
		at        time.Time
		inQuiet   bool
		expectEnd string
	}{
		{"23:00-08:00", 23 * 60, 8 * 60, true, time.Date(2026, 1, 1, 23, 30, 0, 0, time.Local), true, "08:00"},
		{"23:00-08:00", 23 * 60, 8 * 60, true, time.Date(2026, 1, 1, 7, 59, 0, 0, time.Local), true, "08:00"},
		{"23:00-08:00", 23 * 60, 8 * 60, true, time.Date(2026, 1, 1, 8, 0, 0, 0, time.Local), false, "08:00"},
		{"23:00-08:00", 23 * 60, 8 * 60, true, time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local), false, "08:00"},
		{"09:00~18:30", 9 * 60, 18*60 + 30, true, time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local), true, "18:30"},
		{"09:00-18:30", 9 * 60, 18*60 + 30, true, time.Date(2026, 1, 1, 18, 30, 0, 0, time.Local), false, "18:30"},
		{"", 0, 0, false, time.Now(), false, ""},
		{"abc", 0, 0, false, time.Now(), false, ""},
		{"9:00-9:00", 0, 0, false, time.Now(), false, ""},
	}
	for _, c := range cases {
		s, e, ok := parseQuietHours(c.spec)
		if ok != c.ok || s != c.s || e != c.e {
			t.Fatalf("parseQuietHours(%q) = (%d,%d,%v), want (%d,%d,%v)", c.spec, s, e, ok, c.s, c.e, c.ok)
		}
		if !ok {
			continue
		}
		if got := inQuietRange(c.at, s, e); got != c.inQuiet {
			t.Fatalf("inQuietRange(%q, %s) = %v, want %v", c.spec, c.at.Format("15:04"), got, c.inQuiet)
		}
		end := quietEndTime(c.at, e)
		if end.Format("15:04") != c.expectEnd {
			t.Fatalf("quietEndTime(%s, %s) = %s, want %s",
				c.spec, c.at.Format("15:04"), end.Format("15:04"), c.expectEnd)
		}
		if !end.After(c.at) {
			t.Fatalf("quietEndTime 必须晚于当前时刻: %s <= %s", end, c.at)
		}
	}
}

// ---- 意图存储 ----

func TestIntentionStoreRoundTrip(t *testing.T) {
	store := newTestIntentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	it := &ProactiveIntention{
		ID:        "aaaa0001",
		Scope:     "g:123",
		Source:    proactiveSourceAI,
		Prompt:    "问问面试结果",
		Note:      "备注",
		DueAt:     now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour),
		Status:    proactiveStatusPending,
		CreatedBy: "ai",
		CreatedAt: now.Add(-time.Hour),
	}
	mustInsert(t, store, it)

	due := store.due(now, 10)
	if len(due) != 1 || due[0].ID != it.ID {
		t.Fatalf("due = %#v, want 1 条 %s", due, it.ID)
	}
	if due[0].Prompt != "问问面试结果" || due[0].Source != proactiveSourceAI || due[0].CreatedBy != "ai" {
		t.Fatalf("字段回读不一致: %#v", due[0])
	}
	if !due[0].DueAt.Equal(it.DueAt) || !due[0].ExpiresAt.Equal(it.ExpiresAt) {
		t.Fatalf("时间字段回读不一致: %#v", due[0])
	}

	// 未来到点的意图不应出现在 due 里
	future := &ProactiveIntention{
		ID: "aaaa0002", Scope: "g:123", Source: proactiveSourceIdle,
		DueAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour),
		Status: proactiveStatusPending, CreatedAt: now,
	}
	mustInsert(t, store, future)
	if got := store.due(now, 10); len(got) != 1 {
		t.Fatalf("due 应只返回到点意图, got %d", len(got))
	}

	// 更新状态后不再出现在 due / pending
	it.Status = proactiveStatusSent
	it.SentAt = now
	if !store.update(it) {
		t.Fatal("update 失败")
	}
	if got := store.due(now, 10); len(got) != 0 {
		t.Fatalf("已发送的意图不应再被取出, got %d", len(got))
	}
	if got := store.pendingByScope("g:123"); len(got) != 1 || got[0].ID != future.ID {
		t.Fatalf("pendingByScope = %#v", got)
	}
	if got := store.recentByScope("g:123", 10); len(got) != 2 {
		t.Fatalf("recentByScope = %d 条, want 2", len(got))
	}
}

func TestIntentionStoreOutcomeFeedback(t *testing.T) {
	store := newTestIntentionStore(t)
	now := testNoon()
	scope := "g:123"

	// 两条已发送且都被忽略：验证连续忽略计数
	mustInsert(t, store, &ProactiveIntention{
		ID: "b1", Scope: scope, Source: proactiveSourceIdle,
		DueAt: now.Add(-3 * time.Hour), ExpiresAt: now, Status: proactiveStatusSent,
		CreatedAt: now.Add(-3 * time.Hour), SentAt: now.Add(-3 * time.Hour), Outcome: proactiveOutcomeIgnored,
	})
	mustInsert(t, store, &ProactiveIntention{
		ID: "b2", Scope: scope, Source: proactiveSourceIdle,
		DueAt: now.Add(-2 * time.Hour), ExpiresAt: now, Status: proactiveStatusSent,
		CreatedAt: now.Add(-2 * time.Hour), SentAt: now.Add(-2 * time.Hour), Outcome: proactiveOutcomeIgnored,
	})
	if got := store.consecutiveIgnored(scope, 3); got != 2 {
		t.Fatalf("consecutiveIgnored = %d, want 2", got)
	}

	// 再放一条刚发出、尚未结算的
	mustInsert(t, store, &ProactiveIntention{
		ID: "b3", Scope: scope, Source: proactiveSourceAI,
		DueAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Status: proactiveStatusSent,
		CreatedAt: now.Add(-time.Minute), SentAt: now.Add(-time.Minute),
	})

	if got := store.countSentSince(scope, startOfDay(now)); got != 3 {
		t.Fatalf("countSentSince(scope) = %d, want 3", got)
	}
	if got := store.countSentSince("", startOfDay(now)); got != 3 {
		t.Fatalf("countSentSince(全局) = %d, want 3", got)
	}
	if last, ok := store.lastSentAt(scope); !ok || !last.Equal(now.Add(-time.Minute)) {
		t.Fatalf("lastSentAt = %v/%v", last, ok)
	}
	if got := store.consecutiveIgnored(scope, 3); got != 0 {
		t.Fatalf("最近一条待观察时 consecutiveIgnored = %d, want 0（不能把更早的算作连续）", got)
	}
	if got := store.awaitingScopes(now.Add(-10 * time.Minute)); len(got) != 1 || got[0] != scope {
		t.Fatalf("awaitingScopes = %#v", got)
	}

	// 观察窗口内有人说话 → 最近一条记为 replied
	if n := store.markReplied(scope, now.Add(-10*time.Minute)); n != 1 {
		t.Fatalf("markReplied = %d, want 1", n)
	}
	if got := store.consecutiveIgnored(scope, 3); got != 0 {
		t.Fatalf("回应后 consecutiveIgnored = %d, want 0", got)
	}
	if got := store.awaitingScopes(now.Add(-10 * time.Minute)); len(got) != 0 {
		t.Fatalf("结算后 awaitingScopes 应为空, got %#v", got)
	}

	// 再插一条比窗口更晚发出的待观察意图，超窗口后应被结算为 ignored
	mustInsert(t, store, &ProactiveIntention{
		ID: "b4", Scope: scope, Source: proactiveSourceIdle,
		DueAt: now.Add(-20 * time.Minute), ExpiresAt: now, Status: proactiveStatusSent,
		CreatedAt: now.Add(-20 * time.Minute), SentAt: now.Add(-20 * time.Minute),
	})
	if n := store.markIgnored(now.Add(-10 * time.Minute)); n != 1 {
		t.Fatalf("markIgnored = %d, want 1", n)
	}
	b4Ignored := false
	for _, item := range store.recentByScope(scope, 4) {
		if item.ID == "b4" {
			b4Ignored = item.Outcome == proactiveOutcomeIgnored
		}
	}
	if !b4Ignored {
		t.Fatal("b4 应被结算为 ignored")
	}
}

func TestIntentionStoreExpireStale(t *testing.T) {
	store := newTestIntentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	mustInsert(t, store, &ProactiveIntention{
		ID: "c1", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute),
		Status: proactiveStatusPending, CreatedAt: now.Add(-time.Hour),
	})
	mustInsert(t, store, &ProactiveIntention{
		ID: "c2", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: now, ExpiresAt: now.Add(time.Hour),
		Status: proactiveStatusPending, CreatedAt: now,
	})
	if n := store.expireStale(now); n != 1 {
		t.Fatalf("expireStale = %d, want 1", n)
	}
	if got := store.pendingByScope("g:1"); len(got) != 1 || got[0].ID != "c2" {
		t.Fatalf("过期后 pendingByScope = %#v", got)
	}
}

func TestIntentionStorePrune(t *testing.T) {
	store := newTestIntentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-proactiveRetention - time.Hour)

	mustInsert(t, store, &ProactiveIntention{
		ID: "p1", Scope: "g:1", Source: proactiveSourceIdle,
		DueAt: old, ExpiresAt: old, Status: proactiveStatusSent,
		CreatedAt: old, SentAt: old, Outcome: proactiveOutcomeIgnored,
	})
	// 待执行的意图即使很旧也不清理
	mustInsert(t, store, &ProactiveIntention{
		ID: "p2", Scope: "g:1", Source: proactiveSourceAI,
		DueAt: old, ExpiresAt: now.Add(time.Hour), Status: proactiveStatusPending,
		CreatedAt: old,
	})
	if n := store.prune(now); n != 1 {
		t.Fatalf("prune = %d, want 1", n)
	}
	if got := store.pendingByScope("g:1"); len(got) != 1 || got[0].ID != "p2" {
		t.Fatalf("待执行意图应保留, got %#v", got)
	}
}

// ---- 会话开关 ----

func TestScopeOverridePrecedence(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}

	if !m.enabled("g:1") {
		t.Fatal("白名单内会话应默认启用")
	}
	if m.enabled("g:2") {
		t.Fatal("白名单外会话不应启用")
	}

	// 指令关闭优先于白名单；开启优先于「不在白名单」
	m.setScopeState("g:1", proactiveScopeOff)
	if m.enabled("g:1") {
		t.Fatal("off 应覆盖白名单")
	}
	m.setScopeState("g:2", proactiveScopeOn)
	if !m.enabled("g:2") {
		t.Fatal("on 应对白名单外会话生效")
	}

	scopes := m.enabledScopes()
	if len(scopes) != 1 || scopes[0] != "g:2" {
		t.Fatalf("enabledScopes = %#v, want [g:2]", scopes)
	}

	// auto 清空覆盖，回到白名单
	m.setScopeState("g:1", "")
	m.setScopeState("g:2", "")
	if !m.enabled("g:1") || m.scopeState("g:1") != "auto" {
		t.Fatal("auto 应回到白名单默认值")
	}
	if m.enabled("g:2") {
		t.Fatal("g:2 清空覆盖后应回到未启用")
	}
}

// ---- 闸门 ----

func newGateIntent(source string, now time.Time) *ProactiveIntention {
	return &ProactiveIntention{
		ID:        "d1",
		Scope:     "g:1",
		Source:    source,
		DueAt:     now,
		ExpiresAt: now.Add(2 * time.Hour),
		Status:    proactiveStatusPending,
		CreatedAt: now.Add(-10 * time.Minute),
	}
}

func TestGateQuietHours(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	m.quietOn, m.quietStart, m.quietEnd = true, 23*60, 8*60

	now := time.Date(2026, 1, 1, 23, 30, 0, 0, time.Local)
	postpone := newGateIntent(proactiveSourceIdle, now)
	postpone.ExpiresAt = now.Add(12 * time.Hour) // 撑到次日 08:00 之后
	res := m.gate(postpone, now)
	if res.action != gatePostpone {
		t.Fatalf("免打扰时段应顺延, got %v (%s)", res.action, res.reason)
	}
	want := time.Date(2026, 1, 2, 8, 0, 0, 0, time.Local).Sub(now)
	if res.delay != want {
		t.Fatalf("顺延时长 = %v, want %v", res.delay, want)
	}

	// 有效期不足以撑到时段结束 → 直接作废
	it := newGateIntent(proactiveSourceIdle, now)
	it.ExpiresAt = now.Add(10 * time.Minute)
	if res := m.gate(it, now); res.action != gateCancel {
		t.Fatalf("有效期不足应作废, got %v", res.action)
	}

	// 白天不受影响
	day := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	if res := m.gate(newGateIntent(proactiveSourceIdle, day), day); res.action != gateAllow {
		t.Fatalf("白天不应被免打扰拦截, got %v (%s)", res.action, res.reason)
	}
}

func TestGateDailyLimitAndGap(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	now := testNoon()

	// 已达单会话每日上限
	for i := 0; i < 3; i++ {
		mustInsert(t, m.store, &ProactiveIntention{
			ID: "e" + string(rune('1'+i)), Scope: "g:1", Source: proactiveSourceIdle,
			Status: proactiveStatusSent, DueAt: now, ExpiresAt: now.Add(time.Hour),
			CreatedAt: now.Add(-5 * time.Minute), SentAt: now.Add(-5 * time.Minute),
		})
	}
	if res := m.gate(newGateIntent(proactiveSourceIdle, now), now); res.action != gateCancel ||
		!strings.Contains(res.reason, "上限") {
		t.Fatalf("超过每日上限应作废, got %v (%s)", res.action, res.reason)
	}

	// 上限放大后，最小间隔生效（上次发言在 5 分钟前）
	m.dailyLimit = 10
	if res := m.gate(newGateIntent(proactiveSourceIdle, now), now); res.action != gatePostpone ||
		!strings.Contains(res.reason, "太近") {
		t.Fatalf("间隔不足应顺延, got %v (%s)", res.action, res.reason)
	}
}

func TestGateCooldownAfterIgnored(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	m.minGap = 0     // 单独验证冷却：否则会先被最小间隔拦下
	m.dailyLimit = 0 // 否则会先被每日上限拦下
	m.globalDaily = 0
	now := testNoon()

	for i := 0; i < 3; i++ {
		mustInsert(t, m.store, &ProactiveIntention{
			ID: "f" + string(rune('1'+i)), Scope: "g:1", Source: proactiveSourceIdle,
			Status: proactiveStatusSent, DueAt: now, ExpiresAt: now.Add(time.Hour),
			CreatedAt: now.Add(-time.Duration(3-i) * 10 * time.Minute),
			SentAt:    now.Add(-time.Duration(3-i) * 10 * time.Minute),
			Outcome:   proactiveOutcomeIgnored,
		})
	}
	check := newGateIntent(proactiveSourceIdle, now)
	check.ExpiresAt = now.Add(24 * time.Hour) // 撑过冷却期，才能验证到冷却分支
	if res := m.gate(check, now); res.action != gatePostpone ||
		!strings.Contains(res.reason, "冷却") {
		t.Fatalf("连续被忽略应进入冷却, got %v (%s)", res.action, res.reason)
	}
}

func TestGateIdleCancelledWhenConversationRevived(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	now := time.Now()

	// 意图创建于 10 分钟前，5 分钟前有人说话（已过 2 分钟活窗）→ 冷场前提消失
	it := newGateIntent(proactiveSourceIdle, now)
	it.CreatedAt = now.Add(-10 * time.Minute)
	m.mu.Lock()
	m.scopes["g:1"] = &scopeActivity{
		Scope: "g:1", ID: "1", IsGroup: true,
		LastHumanAt: now.Add(-5 * time.Minute),
	}
	m.mu.Unlock()

	if res := m.gate(it, now); res.action != gateCancel {
		t.Fatalf("冷场期间有人说话应作废, got %v (%s)", res.action, res.reason)
	}

	// AI 预约的意图不因「有人说话」作废（可能正是在等这件事）
	ai := newGateIntent(proactiveSourceAI, now)
	ai.CreatedAt = now.Add(-10 * time.Minute)
	if res := m.gate(ai, now); res.action != gateAllow {
		t.Fatalf("AI 预约意图不应因说话作废, got %v (%s)", res.action, res.reason)
	}
}

func TestGatePostponesWhenChatting(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	now := time.Now()
	m.mu.Lock()
	m.scopes["g:1"] = &scopeActivity{
		Scope: "g:1", ID: "1", IsGroup: true,
		LastHumanAt: now.Add(-30 * time.Second),
	}
	m.mu.Unlock()

	if res := m.gate(newGateIntent(proactiveSourceAI, now), now); res.action != gatePostpone {
		t.Fatalf("正在聊天时应顺延, got %v (%s)", res.action, res.reason)
	}
}

func TestGatePostponesRightAfterBotSpoke(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	now := time.Now()
	m.mu.Lock()
	m.scopes["g:1"] = &scopeActivity{
		Scope: "g:1", ID: "1", IsGroup: true,
		LastBotAt: now.Add(-time.Minute),
	}
	m.mu.Unlock()

	res := m.gate(newGateIntent(proactiveSourceAI, now), now)
	if res.action != gatePostpone || !strings.Contains(res.reason, "刚发过言") {
		t.Fatalf("刚说过话应顺延, got %v (%s)", res.action, res.reason)
	}
}

// ---- 冷场触发 ----

func TestConsiderIdleQueuesOncePerSilence(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	m.idle = 30 * time.Minute
	now := time.Now()
	m.mu.Lock()
	m.scopes["g:1"] = &scopeActivity{
		Scope: "g:1", ID: "1", IsGroup: true,
		LastHumanAt: now.Add(-time.Hour),
	}
	m.mu.Unlock()

	m.considerIdle(now)
	m.considerIdle(now.Add(time.Minute))
	if got := m.store.pendingByScope("g:1"); len(got) != 1 {
		t.Fatalf("同一段冷场只应生成 1 条意图, got %d", len(got))
	}

	// 已有待执行意图时，新的一段冷场也不应重复排队
	m.mu.Lock()
	m.scopes["g:1"].LastHumanAt = now.Add(-31 * time.Minute)
	m.mu.Unlock()
	m.considerIdle(now.Add(2 * time.Minute))
	if got := m.store.pendingByScope("g:1"); len(got) != 1 {
		t.Fatalf("已有待执行意图时不应重复排队, got %d", len(got))
	}

	// 执行掉（或取消）后再来一轮冷场 → 允许再次生成
	pending := m.store.pendingByScope("g:1")
	pending[0].Status = proactiveStatusCancelled
	if !m.store.update(&pending[0]) {
		t.Fatal("更新意图失败")
	}
	m.mu.Lock()
	m.scopes["g:1"].LastHumanAt = now.Add(-40 * time.Minute)
	m.mu.Unlock()
	m.considerIdle(now.Add(4 * time.Minute))
	if got := m.store.pendingByScope("g:1"); len(got) != 1 {
		t.Fatalf("新一轮冷场应再生成 1 条, got %d", len(got))
	}
}

// ---- 现场记录与反馈 ----

func TestNoteMessageMarksReplied(t *testing.T) {
	m := newTestProactiveManager(t)
	m.plugin = &AIChatPlugin{}
	m.plugin.cfg.Proactive.Enable = true
	m.baseScopes["g:123"] = struct{}{}
	now := time.Now()

	mustInsert(t, m.store, &ProactiveIntention{
		ID: "g1", Scope: "g:123", Source: proactiveSourceIdle,
		DueAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Status: proactiveStatusSent, CreatedAt: now.Add(-time.Minute), SentAt: now.Add(-time.Minute),
	})
	m.awaiting["g:123"] = struct{}{}

	msg := message.Message{
		GroupId: "123",
		Sender:  message.MessageSender{UserId: "456", Nickname: "小明"},
		Message: []message.OB11Segment{{Type: "text", Data: map[string]any{"text": " 那就明天见吧 "}}},
	}
	m.noteMessage(msg, true)

	items := m.store.recentByScope("g:123", 1)
	if len(items) != 1 || items[0].Outcome != proactiveOutcomeReplied {
		t.Fatalf("有人说话后应结算为 replied, got %#v", items)
	}

	snap := m.snapshot("g:123")
	if snap == nil || len(snap.Context) == 0 {
		t.Fatal("现场应记录到这条消息")
	}
	if !strings.Contains(snap.Context, "小明: 那就明天见吧") {
		t.Fatalf("现场文本 = %q", snap.Context)
	}

	// 未启用的会话不记录任何现场
	m.noteMessage(message.Message{
		GroupId: "999",
		Sender:  message.MessageSender{UserId: "456"},
		Message: []message.OB11Segment{{Type: "text", Data: map[string]any{"text": "hi"}}},
	}, true)
	if snap := m.snapshot("g:999"); snap != nil {
		t.Fatal("未启用会话不应记录现场")
	}
}

func TestNoteActivityRingBufferBounded(t *testing.T) {
	m := newTestProactiveManager(t)
	m.plugin = &AIChatPlugin{}
	m.plugin.cfg.Proactive.Enable = true
	m.baseScopes["g:123"] = struct{}{}
	m.maxContext = 3

	for i := 0; i < 10; i++ {
		m.noteMessage(message.Message{
			GroupId: "123",
			Sender:  message.MessageSender{UserId: "456", Nickname: "小明"},
			Message: []message.OB11Segment{{Type: "text", Data: map[string]any{"text": string(rune('a' + i))}}},
		}, true)
	}
	snap := m.snapshot("g:123")
	if snap == nil {
		t.Fatal("现场为空")
	}
	if lines := strings.Count(strings.TrimSpace(snap.Context), "\n") + 1; lines != 3 {
		t.Fatalf("环形缓冲应只保留 3 条, got %d: %q", lines, snap.Context)
	}
	if !strings.Contains(snap.Context, "j") {
		t.Fatalf("应保留最新消息, got %q", snap.Context)
	}
}

// ---- 调度 ----

// TestRunDueCancelsWhenLimitReached 到期意图在闸门处被取消时，状态应落盘为 cancelled。
func TestRunDueCancelsWhenLimitReached(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	now := testNoon()

	for i := 0; i < 3; i++ {
		mustInsert(t, m.store, &ProactiveIntention{
			ID: "h" + string(rune('1'+i)), Scope: "g:1", Source: proactiveSourceIdle,
			Status: proactiveStatusSent, DueAt: now, ExpiresAt: now.Add(time.Hour),
			CreatedAt: now.Add(-time.Duration(i+1) * time.Minute), SentAt: now.Add(-time.Minute),
		})
	}
	mustInsert(t, m.store, &ProactiveIntention{
		ID: "h9", Scope: "g:1", Source: proactiveSourceIdle,
		Status: proactiveStatusPending, DueAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-20 * time.Minute),
	})

	m.runDue(now)

	for _, item := range m.store.recentByScope("g:1", 10) {
		if item.ID != "h9" {
			continue
		}
		if item.Status != proactiveStatusCancelled {
			t.Fatalf("超过每日上限时应作废, got %s", item.Status)
		}
		if !strings.Contains(item.Reason, "上限") {
			t.Fatalf("应记录作废原因, got %q", item.Reason)
		}
		return
	}
	t.Fatal("找不到那条意图")
}

// ---- 提示词 ----

func TestBuildPromptMentionsSkipOutput(t *testing.T) {
	m := newTestProactiveManager(t)
	m.idle = 90 * time.Minute

	idle := m.buildPrompt(&ProactiveIntention{Source: proactiveSourceIdle}, "123", true)
	if !strings.Contains(idle, "【主动互动】") || !strings.Contains(idle, "skip_output") {
		t.Fatalf("冷场提示词缺少关键要素: %s", idle)
	}
	if !strings.Contains(idle, "安静了 1 小时 30 分钟") {
		t.Fatalf("冷场提示词应说明安静时长: %s", idle)
	}

	ai := m.buildPrompt(&ProactiveIntention{Source: proactiveSourceAI, Prompt: "问问面试结果"}, "456", false)
	if !strings.Contains(ai, "问问面试结果") {
		t.Fatalf("AI 预约提示词应包含意图内容: %s", ai)
	}
}

// ---- 工具 ----

func TestProactiveCreateRejectsDisabledScope(t *testing.T) {
	m := newTestProactiveManager(t)
	tool := newProactiveCreateTool(m, "g:1", true)
	out, err := tool.Execute(context.Background(), &proactiveCreateParams{
		DelayMinutes: 10, Prompt: "问问进展",
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if !strings.Contains(out, "未开启主动互动") {
		t.Fatalf("未启用会话应拒绝创建: %s", out)
	}
	if got := m.store.pendingByScope("g:1"); len(got) != 0 {
		t.Fatalf("拒绝后不应写入意图, got %d", len(got))
	}
}

func TestProactiveCreateAndCancel(t *testing.T) {
	m := newTestProactiveManager(t)
	m.baseScopes["g:1"] = struct{}{}
	create := newProactiveCreateTool(m, "g:1", true)
	out, err := create.Execute(context.Background(), &proactiveCreateParams{
		DelayMinutes: 15, Prompt: "问问面试结果", Note: "小明",
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if !strings.Contains(out, "已记下") {
		t.Fatalf("创建返回异常: %s", out)
	}
	pending := m.store.pendingByScope("g:1")
	if len(pending) != 1 {
		t.Fatalf("应写入 1 条意图, got %d", len(pending))
	}
	if pending[0].Source != proactiveSourceAI || pending[0].Prompt != "问问面试结果" {
		t.Fatalf("意图字段异常: %#v", pending[0])
	}
	if d := pending[0].DueAt.Sub(pending[0].CreatedAt); d != 15*time.Minute {
		t.Fatalf("到点时间 = %v, want 15m", d)
	}

	cancel := &proactiveCancelTool{
		BaseTool:          llmtool.MakeBaseTool("proactive_cancel", "test", proactiveCancelParams{}),
		proactiveToolBase: proactiveToolBase{mgr: m, scope: "g:1"},
	}
	out, err = cancel.Execute(context.Background(), &proactiveCancelParams{ID: pending[0].ID}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(out, "已取消") {
		t.Fatalf("取消失败: %v %s", err, out)
	}
	if got := m.store.pendingByScope("g:1"); len(got) != 0 {
		t.Fatalf("取消后不应还有待执行意图, got %d", len(got))
	}
}
