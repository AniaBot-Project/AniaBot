package pluginaichat

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// ---- 测试辅助 ----

// newContextTestPlugin 构造带 SQL 历史、任务清单与计划模式的测试插件。
func newContextTestPlugin(t *testing.T) (*AIChatPlugin, *sql.DB) {
	t.Helper()
	db := newTestSQLDB(t)
	p := newEvictTestPlugin()
	p.historyDB = db
	p.todoManager = newTodoManager()
	p.planManager = newPlanManager()
	p.cfg.Prompt = "你是一个测试助手"
	p.cfg.MaxContextTokens = 128000
	return p, db
}

// seedContextHistory 往持久化历史写入消息。
func seedContextHistory(t *testing.T, db *sql.DB, key string, msgs ...aichat.Message) {
	t.Helper()
	store := newSQLHistoryStore(db, key, testLogger())
	if err := store.Append(context.Background(), msgs); err != nil {
		t.Fatalf("seed history %s: %v", key, err)
	}
}

// attachContextChat 构造驻留内存的会话（带场景提示词），返回 ChatBot。
func attachContextChat(t *testing.T, p *AIChatPlugin, key string, history ...aichat.Message) *aichat.ChatBot {
	t.Helper()
	id, isGroup, ok := parseSessionKey(key)
	if !ok {
		t.Fatalf("invalid session key %q", key)
	}
	chat, err := aichat.NewChatBot("http://127.0.0.1:1", "k", "m", p.cfg.Prompt, p.cfg.MaxContextTokens, nil, nil,
		aichat.WithScenePrompt("\n\n【当前对话场景】\n你正在群聊中"))
	if err != nil {
		t.Fatalf("new chat: %v", err)
	}
	if len(history) > 0 {
		chat.AppendHistory(history...)
	}
	p.chats.Store(key, newChatEntry(chat, id, isGroup, p.cfg.Prompt))
	return chat
}

// sectionKeys 返回系统提示词分段的 key 集合。
func sectionKeys(sections []plugininfo.ContextSection) map[string]bool {
	out := make(map[string]bool, len(sections))
	for _, s := range sections {
		out[s.Key] = true
	}
	return out
}

// ---- 会话列表 ----

func TestContextSessionsMergesPersistedAndResident(t *testing.T) {
	p, db := newContextTestPlugin(t)
	seedContextHistory(t, db, "g:qq:1001",
		aichat.TextMessage(aichat.RoleUser, "你好"),
		aichat.TextMessage(aichat.RoleAssistant, "你好呀"))
	attachContextChat(t, p, "f:qq:1002")
	// 试聊室历史（panelchat:）由试聊室页管理，不进入上下文管理列表
	seedContextHistory(t, db, "panelchat:abc", aichat.TextMessage(aichat.RoleUser, "试聊"))

	sessions := p.ContextSessions()
	if len(sessions) != 2 {
		t.Fatalf("会话数 = %d, want 2: %+v", len(sessions), sessions)
	}
	byKey := make(map[string]plugininfo.ContextSession, len(sessions))
	for _, s := range sessions {
		byKey[s.Key] = s
	}
	group, ok := byKey["g:qq:1001"]
	if !ok {
		t.Fatalf("缺少群聊会话: %+v", sessions)
	}
	if group.Kind != "group" || group.Target != "qq:1001" || group.Resident {
		t.Fatalf("群聊会话摘要不符: %+v", group)
	}
	if group.MessageCount != 2 {
		t.Fatalf("群聊消息数 = %d, want 2", group.MessageCount)
	}
	if group.CreatedAt == 0 || group.UpdatedAt == 0 {
		t.Fatalf("群聊时间戳未解析: %+v", group)
	}
	friend, ok := byKey["f:qq:1002"]
	if !ok {
		t.Fatalf("缺少私聊会话: %+v", sessions)
	}
	if friend.Kind != "friend" || !friend.Resident || friend.LastActive == 0 {
		t.Fatalf("私聊会话摘要不符: %+v", friend)
	}
}

func TestContextSessionsSortsBusyFirst(t *testing.T) {
	p, db := newContextTestPlugin(t)
	seedContextHistory(t, db, "g:qq:1101", aichat.TextMessage(aichat.RoleUser, "普通会话"))
	seedContextHistory(t, db, "g:qq:1102", aichat.TextMessage(aichat.RoleUser, "在跑的会话"))

	id, isGroup, _ := parseSessionKey("g:qq:1102")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.setActiveContext(id, isGroup, newActiveRequest(cancel, message.FromUint64(1), time.Now()))

	sessions := p.ContextSessions()
	if len(sessions) != 2 {
		t.Fatalf("会话数 = %d, want 2", len(sessions))
	}
	if sessions[0].Key != "g:qq:1102" || !sessions[0].Busy {
		t.Fatalf("执行中的会话应排在最前: %+v", sessions)
	}
}

// ---- 详情 ----

func TestContextDetailLiveAndPersisted(t *testing.T) {
	p, db := newContextTestPlugin(t)
	attachContextChat(t, p, "g:qq:2001", aichat.TextMessage(aichat.RoleUser, "第一条消息"))

	live, err := p.ContextDetail("g:qq:2001")
	if err != nil {
		t.Fatalf("ContextDetail(live): %v", err)
	}
	if live.Source != plugininfo.ContextSourceLive {
		t.Fatalf("source = %q, want live", live.Source)
	}
	if len(live.Messages) != 1 || live.Messages[0].Text != "第一条消息" {
		t.Fatalf("内存消息不符: %+v", live.Messages)
	}
	keys := sectionKeys(live.System)
	if !keys[plugininfo.ContextSectionPrompt] || !keys[plugininfo.ContextSectionScene] {
		t.Fatalf("系统提示词分段不全: %+v", live.System)
	}
	if live.SystemTokens == 0 || live.HistoryTokens == 0 || live.TotalTokens == 0 {
		t.Fatalf("估算 token 未计算: %+v", live)
	}
	if live.Hint == "" {
		t.Fatal("live 快照应带说明文字")
	}

	// 未驻留会话：只读持久化历史，系统提示词按当前配置重建
	seedContextHistory(t, db, "f:qq:3001", aichat.TextMessage(aichat.RoleUser, "旧对话"))
	persisted, err := p.ContextDetail("f:qq:3001")
	if err != nil {
		t.Fatalf("ContextDetail(persisted): %v", err)
	}
	if persisted.Source != plugininfo.ContextSourcePersisted {
		t.Fatalf("source = %q, want persisted", persisted.Source)
	}
	if len(persisted.Messages) != 1 || persisted.Messages[0].Text != "旧对话" {
		t.Fatalf("持久化消息不符: %+v", persisted.Messages)
	}
	if !strings.Contains(persisted.Hint, "未驻留") {
		t.Fatalf("persisted 说明文字不符: %q", persisted.Hint)
	}
	if len(persisted.Tools) != 0 {
		t.Fatalf("未驻留会话不应有工具定义: %+v", persisted.Tools)
	}
}

func TestContextDetailRejectsUnknownSession(t *testing.T) {
	p, _ := newContextTestPlugin(t)
	if _, err := p.ContextDetail("g:qq:9999"); err == nil {
		t.Fatal("不存在的会话应报错")
	}
	if _, err := p.ContextDetail("bogus"); err == nil {
		t.Fatal("非法会话键应报错")
	}
}

func TestContextMessageViewsMapsToolName(t *testing.T) {
	msgs := []aichat.Message{
		{
			Role:      aichat.RoleAssistant,
			ToolCalls: []llmtool.ToolCall{{ID: "c1", Name: "get_time", Arguments: `{}`}},
		},
		{
			Role:       aichat.RoleTool,
			ToolCallID: "c1",
			Parts:      []aichat.ContentPart{aichat.TextPart("12:00")},
		},
	}
	views := contextMessageViews(msgs)
	if len(views) != 2 {
		t.Fatalf("消息数 = %d, want 2", len(views))
	}
	if len(views[0].ToolCalls) != 1 || views[0].ToolCalls[0].Name != "get_time" {
		t.Fatalf("工具调用未保留: %+v", views[0])
	}
	if views[1].ToolName != "get_time" {
		t.Fatalf("tool 结果未补齐工具名: %+v", views[1])
	}
	if views[1].EstTokens == 0 {
		t.Fatalf("token 估算为 0: %+v", views[1])
	}
}

func TestParseSQLTimestamp(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"2026-01-02 03:04:05", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix()},
		{"2026-01-02T03:04:05Z", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix()},
		{"", 0},
		{"not-a-time", 0},
	}
	for _, c := range cases {
		if got := parseSQLTimestamp(c.in); got != c.want {
			t.Errorf("parseSQLTimestamp(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ---- 清空 / 删除 ----

func TestContextClearPurgesHistoryAndSessionState(t *testing.T) {
	p, db := newContextTestPlugin(t)
	key := "g:qq:4001"
	chat := attachContextChat(t, p, key, aichat.TextMessage(aichat.RoleUser, "内存里的旧上下文"))
	seedContextHistory(t, db, key, aichat.TextMessage(aichat.RoleUser, "持久化的旧上下文"))
	if err := p.todoManager.replace(key, []todoItem{{Content: "旧任务", Status: todoStatusPending}}); err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	p.planManager.Set(key, true)
	before := p.sessionToken(key)

	res, err := p.ContextClear(key, false)
	if err != nil {
		t.Fatalf("ContextClear: %v", err)
	}
	if !res.OK || res.Terminated || res.Deleted {
		t.Fatalf("清空结果不符: %+v", res)
	}
	if n, ok := chat.HistoryCount(); !ok || n != 0 {
		t.Fatalf("内存窗口未清空: n=%d ok=%v", n, ok)
	}
	if _, ok := p.contextSessionMeta(key); ok {
		t.Fatal("持久化历史未清空")
	}
	if _, ok := p.chats.Load(key); ok {
		t.Fatal("会话对象应已从内存移除（下次发言按空历史重建）")
	}
	if items := p.todoManager.snapshot(key); len(items) != 0 {
		t.Fatalf("任务清单未清空: %+v", items)
	}
	if !p.planManager.IsOn(key) {
		t.Fatal("清空上下文应保留计划模式（与上下文无关的用户设置）")
	}
	if after := p.sessionToken(key); after == before {
		t.Fatal("清空上下文后应换新会话 token")
	}
	// 重复清空：会话已不存在，返回明确错误而不是静默成功
	if _, err := p.ContextClear(key, false); err == nil {
		t.Fatal("对已清空的会话再次清空应返回错误")
	}
}

func TestContextClearBusyRequiresTerminate(t *testing.T) {
	p, db := newContextTestPlugin(t)
	key := "g:qq:5001"
	seedContextHistory(t, db, key, aichat.TextMessage(aichat.RoleUser, "在跑的对话"))
	id, isGroup, _ := parseSessionKey(key)

	var cancelled atomic.Bool
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.setActiveContext(id, isGroup, newActiveRequest(func() {
		cancelled.Store(true)
		cancel()
	}, message.FromUint64(1), time.Now()))

	if _, err := p.ContextClear(key, false); !errors.Is(err, plugininfo.ErrContextBusy) {
		t.Fatalf("忙时未确认终止应返回 ErrContextBusy, got %v", err)
	}
	if cancelled.Load() {
		t.Fatal("未确认终止时不应取消在跑请求")
	}

	res, err := p.ContextClear(key, true)
	if err != nil {
		t.Fatalf("ContextClear(terminate): %v", err)
	}
	if !res.OK || !res.Terminated {
		t.Fatalf("终止清空结果不符: %+v", res)
	}
	if !cancelled.Load() {
		t.Fatal("确认后应终止在跑请求")
	}
	if _, ok := p.contextSessionMeta(key); ok {
		t.Fatal("终止后应清空持久化历史")
	}
}

func TestContextDeleteClearsGoalAndPlanMode(t *testing.T) {
	p, db := newContextTestPlugin(t)
	key := "g:qq:6001"
	seedContextHistory(t, db, key, aichat.TextMessage(aichat.RoleUser, "旧上下文"))
	p.planManager.Set(key, true)
	p.armGoal(key)

	store, _ := newGoalTestStore(t)
	p.goalStore = store
	p.cfg.Goal.Enable = true
	ctx := context.Background()
	if err := store.Upsert(ctx, &agentGoal{
		SessionKey: key, Objective: "旧目标", Phase: goalPhaseActive, MaxRounds: 5,
	}); err != nil {
		t.Fatalf("seed goal: %v", err)
	}

	res, err := p.ContextDelete(key, false)
	if err != nil {
		t.Fatalf("ContextDelete: %v", err)
	}
	if !res.OK || !res.Deleted {
		t.Fatalf("删除结果不符: %+v", res)
	}
	if _, ok, _ := store.Get(ctx, key); ok {
		t.Fatal("删除会话应一并清除长任务目标")
	}
	if p.goalIsArmed(key) {
		t.Fatal("删除会话应复位目标续跑武装状态")
	}
	if p.planManager.IsOn(key) {
		t.Fatal("删除会话应复位计划模式")
	}
}

func TestContextClearAllReportsBusySessions(t *testing.T) {
	p, db := newContextTestPlugin(t)
	busyKey := "g:qq:7001"
	seedContextHistory(t, db, busyKey, aichat.TextMessage(aichat.RoleUser, "在跑的会话"))
	seedContextHistory(t, db, "f:qq:7002", aichat.TextMessage(aichat.RoleUser, "空闲的会话"))

	id, isGroup, _ := parseSessionKey(busyKey)
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.setActiveContext(id, isGroup, newActiveRequest(cancel, message.FromUint64(1), time.Now()))

	// 未确认终止：执行中的会话计入失败，其余照常清空
	res := p.ContextClearAll(false)
	if res.OK {
		t.Fatalf("有失败项时 OK 应为 false: %+v", res)
	}
	if res.Cleared != 1 || len(res.Failed) != 1 || res.Failed[0].Key != busyKey {
		t.Fatalf("清空结果不符: %+v", res)
	}

	// 确认终止：全部清空
	res = p.ContextClearAll(true)
	if !res.OK || res.Cleared != 1 || res.Terminated != 1 {
		t.Fatalf("终止后清空结果不符: %+v", res)
	}
	if len(p.ContextSessions()) != 0 {
		t.Fatalf("全部清空后不应再有会话: %+v", p.ContextSessions())
	}
}

// ---- 锁与并发 ----

// TestTryLockOnlyDoesNotConsumeRateSlot 管理锁（面板清空 / 删除用）不占用对话并发
// 槽位，release 也不能替它归还槽位——否则会误吞其他会话的并发名额。
func TestTryLockOnlyDoesNotConsumeRateSlot(t *testing.T) {
	p := newEvictTestPlugin()
	id := message.FromUint64(1)
	lock := p.tryLockOnly(id, true)
	if lock == nil {
		t.Fatal("tryLockOnly 应能拿到空闲会话锁")
	}
	// 先占满全部并发槽位，再释放管理锁
	for i := 0; i < cap(p.rateCh); i++ {
		p.rateCh <- struct{}{}
	}
	before := len(p.rateCh)
	lock.release()
	if got := len(p.rateCh); got != before {
		t.Fatalf("管理锁 release 不应归还并发槽位: 释放前 %d，释放后 %d", before, got)
	}
	// 清空占位，避免影响其他用例（本用例独占插件实例）
	for i := 0; i < cap(p.rateCh); i++ {
		<-p.rateCh
	}
}

// TestWaitContextLockDegradesWithoutStorage 没有会话锁存储（降载模式）时不应阻塞：
// 直接返回「无需持锁」。
func TestWaitContextLockDegradesWithoutStorage(t *testing.T) {
	p := &AIChatPlugin{}
	lock, ok := p.waitContextLock(message.FromUint64(1), true)
	if !ok || lock != nil {
		t.Fatalf("无锁存储时应直接放行: lock=%v ok=%v", lock, ok)
	}
}

// TestContextClearIgnoresRateLimit 其他会话占满并发槽位时，面板清空仍应成功
// （管理操作只与会话锁互斥，不参与对话限流）。
func TestContextClearIgnoresRateLimit(t *testing.T) {
	p, db := newContextTestPlugin(t)
	key := "g:qq:9001"
	seedContextHistory(t, db, key, aichat.TextMessage(aichat.RoleUser, "旧上下文"))
	for i := 0; i < cap(p.rateCh); i++ {
		p.rateCh <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(p.rateCh); i++ {
			<-p.rateCh
		}
	}()

	res, err := p.ContextClear(key, false)
	if err != nil {
		t.Fatalf("并发槽位占满时清空失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("清空结果不符: %+v", res)
	}
}

// ---- 导出 ----

func TestContextExportSelectsSessions(t *testing.T) {
	p, db := newContextTestPlugin(t)
	seedContextHistory(t, db, "g:qq:8001", aichat.TextMessage(aichat.RoleUser, "会话甲"))
	seedContextHistory(t, db, "f:qq:8002", aichat.TextMessage(aichat.RoleUser, "会话乙"))

	all := p.ContextExport(nil)
	if len(all) != 2 {
		t.Fatalf("导出全部会话 = %d, want 2", len(all))
	}
	one := p.ContextExport([]string{"f:qq:8002"})
	if len(one) != 1 || one[0].Key != "f:qq:8002" {
		t.Fatalf("按会话键导出不符: %+v", one)
	}
	if len(one[0].Messages) != 1 || one[0].Messages[0].Text != "会话乙" {
		t.Fatalf("导出内容不符: %+v", one[0].Messages)
	}
	// 不存在的会话跳过，不影响其余会话
	none := p.ContextExport([]string{"g:qq:not-exist"})
	if len(none) != 0 {
		t.Fatalf("不存在的会话应跳过: %+v", none)
	}
}
