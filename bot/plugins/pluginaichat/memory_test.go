package pluginaichat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/storage"
)

// sqlPFake 包装 pfake 并附加 SQL 能力，使 newMemoryManager 探测走 SQL 后端。
type sqlPFake struct {
	*pfake
	db *sql.DB
}

func newSQLPFake() *sqlPFake {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	return &sqlPFake{pfake: newPFake(), db: db}
}

func (s *sqlPFake) SQLDB() *sql.DB                 { return s.db }
func (s *sqlPFake) SQLDialect() storage.SQLDialect { return storage.SQLDialectSQLite }

func newTestMemoryManager(maxEntries int) *memoryManager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newMemoryManager(newSQLPFake(), logger, maxEntries, nil)
}

func TestMemoryAddAndList(t *testing.T) {
	m := newTestMemoryManager(0)

	e, err := m.add("g:123", "456", "小明喜欢熬夜打榜", []string{"偏好"})
	if err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	if e.ID == "" {
		t.Fatal("add 未生成 ID")
	}

	entries := m.list("g:123")
	if len(entries) != 1 {
		t.Fatalf("期望 1 条记忆，实际 %d 条", len(entries))
	}
	if entries[0].Content != "小明喜欢熬夜打榜" || entries[0].UserID != "456" {
		t.Fatalf("记忆内容不符: %+v", entries[0])
	}

	// scope 隔离：其它群/私聊看不到
	if got := m.list("g:999"); len(got) != 0 {
		t.Fatalf("scope 隔离失效，g:999 看到 %d 条记忆", len(got))
	}
	if got := m.list("f:123"); len(got) != 0 {
		t.Fatalf("scope 隔离失效，f:123 看到 %d 条记忆", len(got))
	}
}

func TestMemoryAddDedup(t *testing.T) {
	m := newTestMemoryManager(0)

	first, err := m.add("g:123", "", "群规：不许发广告", nil)
	if err != nil {
		t.Fatalf("首次 add 失败: %v", err)
	}
	// 空白差异应被规范化去重
	second, err := m.add("g:123", "", "  群规：不许发广告 \n", nil)
	if err != nil {
		t.Fatalf("重复 add 失败: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("重复内容应返回已有条目，ID 不同: %s vs %s", first.ID, second.ID)
	}
	if got := m.list("g:123"); len(got) != 1 {
		t.Fatalf("去重失效，实际 %d 条", len(got))
	}
}

func TestMemoryAddEmpty(t *testing.T) {
	m := newTestMemoryManager(0)
	if _, err := m.add("g:123", "", "   ", nil); err == nil {
		t.Fatal("空内容应报错")
	}
}

func TestMemoryMaxEntries(t *testing.T) {
	m := newTestMemoryManager(2)

	if _, err := m.add("g:123", "", "第一条", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.add("g:123", "", "第二条", nil); err != nil {
		t.Fatal(err)
	}
	_, err := m.add("g:123", "", "第三条", nil)
	if !errors.Is(err, ErrMemoryFull) {
		t.Fatalf("达到上限应返回 ErrMemoryFull，实际: %v", err)
	}

	// 删除一条后可继续写入
	entries := m.list("g:123")
	if !m.remove("g:123", entries[0].ID) {
		t.Fatal("remove 失败")
	}
	if _, err := m.add("g:123", "", "第三条", nil); err != nil {
		t.Fatalf("删除后 add 仍失败: %v", err)
	}
}

func TestMemoryRemove(t *testing.T) {
	m := newTestMemoryManager(0)

	e, _ := m.add("g:123", "", "待删除", nil)
	if !m.remove("g:123", e.ID) {
		t.Fatal("remove 已存在 ID 应返回 true")
	}
	if got := m.list("g:123"); len(got) != 0 {
		t.Fatalf("删除后仍有 %d 条", len(got))
	}
	if m.remove("g:123", e.ID) {
		t.Fatal("remove 不存在 ID 应返回 false")
	}
	// 不影响其它 scope
	other, _ := m.add("g:999", "", "别的群的记忆", nil)
	if m.remove("g:123", other.ID) {
		t.Fatal("不应跨 scope 删除")
	}
}

func TestMemoryUpdate(t *testing.T) {
	m := newTestMemoryManager(0)

	e, _ := m.add("g:123", "456", "旧内容", []string{"旧标签"})
	created := e.CreatedAt

	if err := m.update("g:123", e.ID, "789", "新内容", []string{"新标签"}); err != nil {
		t.Fatalf("update 失败: %v", err)
	}
	entries := m.list("g:123")
	if len(entries) != 1 {
		t.Fatalf("update 后条目数不符: %d", len(entries))
	}
	got := entries[0]
	if got.Content != "新内容" || got.UserID != "789" || len(got.Tags) != 1 || got.Tags[0] != "新标签" {
		t.Fatalf("update 后内容不符: %+v", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("update 不应改变创建时间: %v -> %v", created, got.CreatedAt)
	}

	// ID 不存在
	if err := m.update("g:123", "deadbeef", "", "x", nil); err == nil {
		t.Fatal("更新不存在的 ID 应报错")
	}
	// 空内容
	if err := m.update("g:123", e.ID, "", "   ", nil); err == nil {
		t.Fatal("空内容应报错")
	}
	// 不影响其它 scope
	if err := m.update("g:999", e.ID, "", "跨 scope", nil); err == nil {
		t.Fatal("不应跨 scope 更新")
	}
}

func TestMemoryScopes(t *testing.T) {
	m := newTestMemoryManager(0)

	if got := m.scopes(); len(got) != 0 {
		t.Fatalf("空管理器应无 scope，实际 %v", got)
	}

	if _, err := m.add("g:123", "", "群记忆", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.add("f:456", "", "私聊记忆", nil); err != nil {
		t.Fatal(err)
	}

	got := m.scopes()
	if len(got) != 2 || got[0] != "f:456" || got[1] != "g:123" {
		t.Fatalf("scopes 不符（应排序）: %v", got)
	}
}

func TestFilterMemoryByRelevance(t *testing.T) {
	entries := []memoryEntry{
		{ID: "a", Content: "完全无关的内容"},
		{ID: "b", Content: "用户喜欢喝咖啡"},
		{ID: "c", Content: "随便记的一条", Tags: []string{"咖啡"}},
	}
	matched := filterMemoryByRelevance(entries, []string{"咖啡"}, nil)
	if len(matched) != 2 {
		t.Fatalf("零分条目应被过滤，期望 2 条，实际 %d 条", len(matched))
	}
	// tag 命中权重(20)高于正文命中(10)，c 应排最前
	if matched[0].ID != "c" || matched[1].ID != "b" {
		t.Fatalf("排序不符: %v", matched)
	}

	// 全部零分时返回空
	if got := filterMemoryByRelevance(entries, []string{"奶茶"}, nil); len(got) != 0 {
		t.Fatalf("无命中应返回空，实际 %d 条", len(got))
	}
}

func TestFormatMemoryLine(t *testing.T) {
	m := newTestMemoryManager(0)
	e, _ := m.add("g:123", "456", "内容", []string{"标签"})
	line := formatMemoryLine(e)
	for _, want := range []string{e.ID, "456", "标签", "内容"} {
		if !strings.Contains(line, want) {
			t.Fatalf("格式化结果缺少 %q: %s", want, line)
		}
	}
}

func TestMemoryContentTruncation(t *testing.T) {
	m := newTestMemoryManager(0)

	long := strings.Repeat("长", MaxContentRunes+100)
	e, err := m.add("g:123", "", long, nil)
	if err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	// 截断为 MaxContentRunes 个符文 + 省略标记
	if got := len([]rune(e.Content)); got != MaxContentRunes+1 {
		t.Fatalf("add 未截断超长内容，符文数 = %d", got)
	}
	entries := m.list("g:123")
	if len(entries) != 1 || entries[0].Content != e.Content {
		t.Fatalf("落盘内容未截断: %+v", entries)
	}

	// update 同样截断
	if err := m.update("g:123", e.ID, "", long, nil); err != nil {
		t.Fatalf("update 失败: %v", err)
	}
	if got := len([]rune(m.list("g:123")[0].Content)); got != MaxContentRunes+1 {
		t.Fatalf("update 未截断超长内容，符文数 = %d", got)
	}
}

// ---- 记忆向量混合检索 ----

// fakeEmbeddingsServer 测试用 embedding 服务：按文本关键词返回固定方向向量。
// 语义同义词（喜爱/喜欢）映射到同一方向，用于验证向量加分命中路径。
func fakeEmbeddingsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		var sb strings.Builder
		sb.WriteString(`{"object":"list","data":[`)
		for i, text := range req.Input {
			var x, y float64
			switch {
			case strings.Contains(text, "喜爱") || strings.Contains(text, "喜欢"):
				x, y = 1, 0
			case strings.Contains(text, "天气"):
				x, y = 0, 1
			default:
				x, y = 0.1, 0.1
			}
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"object":"embedding","index":%d,"embedding":[%g,%g]}`, i, x, y)
		}
		sb.WriteString(`],"model":"` + req.Model + `"}`)
		fmt.Fprint(w, sb.String())
	}))
	return srv
}

func newTestMemoryManagerWithEmbedder(store storage.PersistentStorage, baseURL string) *memoryManager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	emb := newEmbedder(baseURL, "test-key", "test-embed", nil, logger)
	return newMemoryManager(store, logger, 0, emb)
}

// TestMemoryAddStoresEmb 入库时计算语义向量并随条目持久化。
func TestMemoryAddStoresEmb(t *testing.T) {
	srv := fakeEmbeddingsServer(t)
	defer srv.Close()

	store := newSQLPFake()
	m := newTestMemoryManagerWithEmbedder(store, srv.URL)
	if _, err := m.add("g:123", "", "小明喜爱熬夜打榜", nil); err != nil {
		t.Fatal(err)
	}
	entries := m.list("g:123")
	if len(entries) != 1 || len(entries[0].Emb) == 0 {
		t.Fatalf("入库应计算向量: %+v", entries)
	}

	// 向量随条目持久化：重建管理器回读仍在（模拟重启）
	m2 := newTestMemoryManagerWithEmbedder(store, srv.URL)
	entries = m2.list("g:123")
	if len(entries) != 1 || len(entries[0].Emb) == 0 {
		t.Fatalf("重启后向量应保留: %+v", entries)
	}
}

// TestMemorySearchVectorOnlyHit 同义不同词（喜爱 vs 喜欢）无关键词命中时，
// 仅靠向量相似度加分即可命中；无关记忆仍被零分过滤。
func TestMemorySearchVectorOnlyHit(t *testing.T) {
	srv := fakeEmbeddingsServer(t)
	defer srv.Close()

	m := newTestMemoryManagerWithEmbedder(newSQLPFake(), srv.URL)
	if _, err := m.add("g:123", "", "小明喜爱熬夜打榜", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.add("g:123", "", "今天天气很好", nil); err != nil {
		t.Fatal(err)
	}

	tool := newMemoryTools(m, "g:123", "群聊（群号 123）")[1] // memory_search
	result, err := tool.Execute(context.Background(), &memorySearchParams{Query: "喜欢"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("search 失败: %v", err)
	}
	if !strings.Contains(result, "小明喜爱熬夜打榜") {
		t.Fatalf("向量命中丢失，结果: %s", result)
	}
	if strings.Contains(result, "今天天气很好") {
		t.Fatalf("无关记忆不应命中，结果: %s", result)
	}
}

// TestMemorySearchVectorDisabled 未启用 embedding（embedder=nil）时保持纯关键词，
// 同义不同词不命中（与历史行为一致）。
func TestMemorySearchVectorDisabled(t *testing.T) {
	m := newTestMemoryManager(0) // embedder=nil
	if _, err := m.add("g:123", "", "小明喜爱熬夜打榜", nil); err != nil {
		t.Fatal(err)
	}
	tool := newMemoryTools(m, "g:123", "群聊（群号 123）")[1]
	result, err := tool.Execute(context.Background(), &memorySearchParams{Query: "喜欢"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("search 失败: %v", err)
	}
	if strings.Contains(result, "小明喜爱熬夜打榜") {
		t.Fatalf("未启用向量时同义词不应命中，结果: %s", result)
	}
}

// TestMemorySearchLegacyEntryNoEmb 旧数据（Emb 缺失）+ 向量查询不崩溃，
// 无关键词命中时被过滤。
func TestMemorySearchLegacyEntryNoEmb(t *testing.T) {
	entries := []memoryEntry{
		{ID: "old", Content: "小明喜爱熬夜打榜"}, // 旧数据无 Emb
	}
	queryVec := []float32{1, 0}
	matched := filterMemoryByRelevance(entries, []string{"喜欢"}, queryVec)
	if len(matched) != 0 {
		t.Fatalf("Emb 缺失的旧数据无关键词命中时应被过滤，got %d 条", len(matched))
	}
}

// TestMemoryAutoInject 主动注入：纯关键词检索（中文切词）、条数上限、
// 字符预算与无命中兜底。
func TestMemoryAutoInject(t *testing.T) {
	m := newTestMemoryManager(0)
	if _, err := m.add("g:123", "456", "小明讨厌被半夜@", []string{"偏好"}); err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	if _, err := m.add("g:123", "", "群规：每周三晚上八点开例会", []string{"规则"}); err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	if _, err := m.add("g:123", "", "小美喜欢喝奶茶", nil); err != nil {
		t.Fatalf("add 失败: %v", err)
	}

	// 空消息 / 无命中 → 空串
	if got := m.autoInject("g:123", "", 3, nil); got != "" {
		t.Fatalf("空消息应返回空串: %q", got)
	}
	if got := m.autoInject("g:123", "今天天气不错", 3, nil); got != "" {
		t.Fatalf("无命中应返回空串: %q", got)
	}

	// 内容关键词命中（queryTerms 中文切词：整词+相邻二元组）
	got := m.autoInject("g:123", "小明你晚上在吗", 3, nil)
	if !strings.Contains(got, "小明讨厌被半夜@") {
		t.Fatalf("应命中内容关键词: %q", got)
	}
	if !strings.HasPrefix(got, "【长期记忆】") {
		t.Fatalf("应以【长期记忆】开头: %q", got)
	}

	// max 限制注入条数（分数相同时按创建顺序稳定保留）
	got = m.autoInject("g:123", "小明 小美", 1, nil)
	if strings.Count(got, "（记于") != 1 {
		t.Fatalf("max=1 应只注入 1 条: %q", got)
	}

	// tag 命中权重高于正文：查询「规则」应优先命中带规则标签的条目
	got = m.autoInject("g:123", "规则 例会 晚上", 3, nil)
	if !strings.Contains(got, "群规：每周三晚上八点开例会") {
		t.Fatalf("tag 命中条目应出现: %q", got)
	}
	idxRule := strings.Index(got, "群规：每周三晚上八点开例会")
	idxMilk := strings.Index(got, "小美喜欢喝奶茶")
	if idxRule < 0 || (idxMilk >= 0 && idxMilk < idxRule) {
		t.Fatalf("tag 命中条目应排在正文命中之前: %q", got)
	}

	// max<=0 时使用默认 3
	got = m.autoInject("g:123", "小明 小美 奶茶 规则", 0, nil)
	if strings.Count(got, "（记于") != 3 {
		t.Fatalf("默认应注入 3 条: %q", got)
	}
}

// TestMemoryAutoInjectRuneBudget 超长记忆时注入块受 memoryInjectMaxRunes 预算约束。
func TestMemoryAutoInjectRuneBudget(t *testing.T) {
	m := newTestMemoryManager(0)
	long := strings.Repeat("长", 1500)
	for range 3 {
		if _, err := m.add("g:123", "", long, nil); err != nil {
			t.Fatalf("add 失败: %v", err)
		}
	}
	got := m.autoInject("g:123", "长", 3, nil)
	if got == "" {
		t.Fatal("应命中并返回注入块")
	}
	if n := len([]rune(got)); n > memoryInjectMaxRunes+64 {
		t.Fatalf("注入块符文数超预算: %d > %d", n, memoryInjectMaxRunes+64)
	}
}

// TestMemoryAutoInjectSemantic 查询词与记忆内容无关键词重叠时，
// 语义相似度加分（queryVec 非 nil）也能让记忆被注入。
func TestMemoryAutoInjectSemantic(t *testing.T) {
	m := newTestMemoryManager(0)
	// 手工构造带向量的记忆（embedder=nil 时 add 不会计算向量，直接 update 写入）
	if _, err := m.add("g:123", "", "小明喜爱熬夜喝咖啡", nil); err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	// 手工为记忆补向量（embedder=nil 时 add/update 都不会计算向量，
	// 直接走底层 store 写入，模拟已有向量数据）
	entry := m.list("g:123")[0]
	entry.Emb = []float32{1, 0}
	if ok := m.store.update("g:123", entry); !ok {
		t.Fatal("store.update 失败")
	}

	// 查询词与内容零关键词重叠：纯关键词（queryVec=nil）不命中
	if got := m.autoInject("g:123", "他平时爱喝什么饮品", 3, nil); got != "" {
		t.Fatalf("无关键词重叠且 queryVec=nil 时不应注入: %q", got)
	}
	// 方向一致的查询向量：语义命中，应注入
	got := m.autoInject("g:123", "他平时爱喝什么饮品", 3, []float32{1, 0})
	if !strings.Contains(got, "小明喜爱熬夜喝咖啡") {
		t.Fatalf("语义命中应注入记忆: %q", got)
	}
}
