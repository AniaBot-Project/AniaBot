package pluginaichat

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
)

// memPersistent 最小内存 PersistentStorage 实现：仅支持计数/历史测试用到的
// 键值读写与 Clone 前缀，其余方法经内嵌接口兜底（测试中不会触达）。
type memPersistent struct {
	storage.PersistentStorage
	mu     sync.Mutex
	data   map[string]string
	prefix string
}

func newMemPersistent() *memPersistent {
	return &memPersistent{data: map[string]string{}}
}

func (m *memPersistent) key(k string) string { return m.prefix + k }

func (m *memPersistent) Clone(prefix string) storage.PersistentStorage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &memPersistent{data: m.data, prefix: m.prefix + prefix}
}

func (m *memPersistent) GetString(_ context.Context, key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[m.key(key)]
	return v, ok
}

func (m *memPersistent) SetString(_ context.Context, key, val string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.key(key)] = val
	return true
}

func (m *memPersistent) Has(_ context.Context, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[m.key(key)]
	return ok
}

func (m *memPersistent) Del(_ context.Context, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.key(key))
	return true
}

// Get/Set 用 JSON 编解码（持久化配置计数等 KV 用途）。
func (m *memPersistent) Get(_ context.Context, key string, out any) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.data[m.key(key)]
	if !ok {
		return false
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false
	}
	return true
}

func (m *memPersistent) Set(_ context.Context, key string, val any) bool {
	raw, err := json.Marshal(val)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.key(key)] = string(raw)
	return true
}

// newNoMentionPlugin 构造带未@计数持久层与默认阈值 30 的测试插件。
func newNoMentionPlugin(store storage.PersistentStorage) *AIChatPlugin {
	p := &AIChatPlugin{}
	p.Logger = testLogger()
	p.cfg.Session.NoMentionClear = 30
	if store != nil {
		p.noMentionStore = store.Clone(noMentionKeyPrefix)
	}
	return p
}

func TestNoMentionCountPersistAcrossRestart(t *testing.T) {
	store := newMemPersistent()
	gid := message.FromUint64(123)

	p1 := newNoMentionPlugin(store)
	for i := 1; i <= 30; i++ {
		p1.incrNoMention(gid)
	}
	if got := p1.noMentionValue(gid); got != 30 {
		t.Fatalf("计数 = %d, want 30", got)
	}
	// 每 10 条落盘一次，30 已达阈值应已落盘
	nmStore := store.Clone(noMentionKeyPrefix)
	if got, ok := nmStore.GetString(context.Background(), gid.String()); !ok || got != "30" {
		t.Fatalf("持久化计数 = %q, %v, want 30, true", got, ok)
	}

	// 模拟重启：新插件实例从同一持久层恢复，计数继续累计
	p2 := newNoMentionPlugin(store)
	if got := p2.incrNoMention(gid); got != 31 {
		t.Fatalf("重启后首次计数 = %d, want 31", got)
	}
}

func TestNoMentionResetDeletesPersisted(t *testing.T) {
	store := newMemPersistent()
	gid := message.FromUint64(456)
	p := newNoMentionPlugin(store)
	for range 30 {
		p.incrNoMention(gid)
	}
	nmStore := store.Clone(noMentionKeyPrefix)
	if !nmStore.Has(context.Background(), gid.String()) {
		t.Fatal("计数应已落盘")
	}
	p.resetNoMention(gid)
	if got := p.noMentionValue(gid); got != 0 {
		t.Fatalf("重置后计数 = %d, want 0", got)
	}
	if nmStore.Has(context.Background(), gid.String()) {
		t.Fatal("重置后持久化记录应删除")
	}
}

func TestNoMentionThresholdDisabled(t *testing.T) {
	store := newMemPersistent()
	p := newNoMentionPlugin(store)
	p.cfg.Session.NoMentionClear = 0
	gid := message.FromUint64(789)
	for range 100 {
		p.incrNoMention(gid)
	}
	if got := p.noMentionValue(gid); got != 0 {
		t.Fatalf("阈值关闭时不应计数, got %d", got)
	}
	if nmStore := store.Clone(noMentionKeyPrefix); nmStore.Has(context.Background(), gid.String()) {
		t.Fatal("阈值关闭时不应落盘")
	}
}

// TestNoMentionClearRetainedAndAppliedAtMentionAfterRestart 覆盖「部分群超过
// 30 轮未@也不清空」的核心场景：会话未驻留（重启后尚未创建/被闲置回收淘汰）时
// 计数必须保留而非静默清零，并在下次 @ 重建会话后补清历史。
func TestNoMentionClearRetainedAndAppliedAtMentionAfterRestart(t *testing.T) {
	store := newMemPersistent()
	gid := message.FromUint64(10086)

	// 第一段进程：会话未驻留，计数达阈值后 tryClear 无法清空，计数应保留并落盘
	p1 := newNoMentionPlugin(store)
	for range 30 {
		p1.incrNoMention(gid)
	}
	if p1.tryClearNoMentionChat(context.Background(), gid) {
		t.Fatal("无会话驻留时不应清空")
	}
	if got := p1.noMentionValue(gid); got != 30 {
		t.Fatalf("清空失败后计数应保留, got %d", got)
	}
	nmStore := store.Clone(noMentionKeyPrefix)
	if got, ok := nmStore.GetString(context.Background(), gid.String()); !ok || got != "30" {
		t.Fatalf("计数应落盘 = %q, %v", got, ok)
	}

	// 模拟重启 + 会话重建（历史从持久层回放）：@ 消息路径补清
	p2 := newNoMentionPlugin(store)
	historyStore := newSQLHistoryStore(newTestSQLDB(t), gid.String(), testLogger())
	if err := historyStore.Append(context.Background(), []aichat.Message{aichat.TextMessage(aichat.RoleUser, "旧对话")}); err != nil {
		t.Fatal(err)
	}
	chat, err := aichat.NewChatBot("http://127.0.0.1:1", "k", "m", "prompt", 0, nil, historyStore)
	if err != nil {
		t.Fatal(err)
	}
	chat.LoadHistory(context.Background())

	tokenBefore := p2.sessionToken(sessionKey(gid, true))
	p2.applyNoMentionClear(context.Background(), gid, chat)

	// 历史应被清空（持久层删除），下次 @ 即开新对话
	if msgs, _ := historyStore.Load(context.Background()); len(msgs) != 0 {
		t.Fatalf("补清后持久化历史应清空, got %d 条", len(msgs))
	}
	// 新会话：${session} 请求头取值换新
	if got := p2.sessionToken(sessionKey(gid, true)); got == tokenBefore {
		t.Fatalf("自动清空后会话 token 应换新, still %q", got)
	}
	if got := p2.noMentionValue(gid); got != 0 {
		t.Fatalf("补清后计数应清零, got %d", got)
	}
	if nmStore.Has(context.Background(), gid.String()) {
		t.Fatal("补清后持久化计数应删除")
	}
}

// TestNoMentionIncrConcurrentNoLostUpdate 并发未@消息计数不应丢更新。
func TestNoMentionIncrConcurrentNoLostUpdate(t *testing.T) {
	store := newMemPersistent()
	p := newNoMentionPlugin(store)
	gid := message.FromUint64(2024)
	const goroutines = 16
	const perGoroutine = 25
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				p.incrNoMention(gid)
			}
		})
	}
	wg.Wait()
	if got := p.noMentionValue(gid); got != goroutines*perGoroutine {
		t.Fatalf("并发计数丢失: got %d, want %d", got, goroutines*perGoroutine)
	}
}

// TestNoMentionClearConfigTag 校验阈值配置字段的默认值与面板键路径。
func TestNoMentionClearConfigTag(t *testing.T) {
	field, ok := reflect.TypeFor[sessionConfig]().FieldByName("NoMentionClear")
	if !ok {
		t.Fatal("sessionConfig.NoMentionClear 字段不存在")
	}
	if raw, ok := field.Tag.Lookup("default"); !ok || raw != "30" {
		t.Fatalf("default 标签 = %q, want 30", raw)
	}
	if raw, ok := field.Tag.Lookup("cfg"); !ok || raw != "no_mention_clear" {
		t.Fatalf("cfg 标签 = %q, want no_mention_clear", raw)
	}
}
