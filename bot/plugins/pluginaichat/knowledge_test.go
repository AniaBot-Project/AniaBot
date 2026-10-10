package pluginaichat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

func newTestKnowledgeManager(maxDocs int) *knowledgeManager {
	return newTestKnowledgeManagerWith(newSQLPFake(), maxDocs)
}

// newTestKnowledgeManagerWith 基于给定存储构造知识库管理器（测试用）。
// 非 SQL 存储上 newKnowledgeManager 返回 nil（功能禁用），测试中视为致命错误。
func newTestKnowledgeManagerWith(store storage.PersistentStorage, maxDocs int) *knowledgeManager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	km := newKnowledgeManager(store, logger, maxDocs, nil)
	if km == nil {
		panic("newKnowledgeManager 返回 nil（SQL 后端不可用）")
	}
	return km
}

func TestKbAddAndList(t *testing.T) {
	km := newTestKnowledgeManager(0)

	d, err := km.add("global", "Docker 部署指南", "第一步：安装 docker。", []string{"部署"}, "url:https://example.com")
	if err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	if d.ID == "" {
		t.Fatal("add 未生成 ID")
	}
	if d.Scope != "global" {
		t.Fatalf("scope 不符: %s", d.Scope)
	}

	docs := km.list("global")
	if len(docs) != 1 {
		t.Fatalf("期望 1 篇文档，实际 %d 篇", len(docs))
	}
	got := docs[0]
	if got.Title != "Docker 部署指南" || got.Source != "url:https://example.com" || len(got.Tags) != 1 {
		t.Fatalf("文档内容不符: %+v", got)
	}

	// 全局库与会话库相互隔离
	if got := km.list("g:123"); len(got) != 0 {
		t.Fatalf("scope 隔离失效，g:123 看到 %d 篇文档", len(got))
	}
}

func TestKbAddDedup(t *testing.T) {
	km := newTestKnowledgeManager(0)

	first, err := km.add("global", "标题", "正文内容", nil, "")
	if err != nil {
		t.Fatalf("首次 add 失败: %v", err)
	}
	// 空白差异应被规范化去重
	second, err := km.add("global", " 标题 \n", "  正文内容 \n", nil, "url:https://x")
	if err != nil {
		t.Fatalf("重复 add 失败: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("重复内容应返回已有文档，ID 不同: %s vs %s", first.ID, second.ID)
	}
	if got := km.list("global"); len(got) != 1 {
		t.Fatalf("去重失效，实际 %d 篇", len(got))
	}
}

func TestKbAddEmpty(t *testing.T) {
	km := newTestKnowledgeManager(0)
	if _, err := km.add("global", "标题", "   ", nil, ""); err == nil {
		t.Fatal("空内容应报错")
	}
}

func TestKbMaxDocs(t *testing.T) {
	km := newTestKnowledgeManager(2)

	if _, err := km.add("global", "", "第一篇", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := km.add("global", "", "第二篇", nil, ""); err != nil {
		t.Fatal(err)
	}
	_, err := km.add("global", "", "第三篇", nil, "")
	if !errors.Is(err, ErrKBFull) {
		t.Fatalf("达到上限应返回 ErrKBFull，实际: %v", err)
	}

	// 删除一篇后可继续写入
	docs := km.list("global")
	if !km.remove("global", docs[0].ID) {
		t.Fatal("remove 失败")
	}
	if _, err := km.add("global", "", "第三篇", nil, ""); err != nil {
		t.Fatalf("删除后 add 仍失败: %v", err)
	}
}

func TestKbRemove(t *testing.T) {
	km := newTestKnowledgeManager(0)

	d, _ := km.add("g:123", "", "待删除", nil, "")
	if !km.remove("g:123", d.ID) {
		t.Fatal("remove 已存在 ID 应返回 true")
	}
	if got := km.list("g:123"); len(got) != 0 {
		t.Fatalf("删除后仍有 %d 篇", len(got))
	}
	// 行级存储下删除最后一行后作用域自然消失，面板不再列出该空作用域
	if got := km.store.count("g:123"); got != 0 {
		t.Fatalf("删除最后一篇后不应残留行，实际 %d", got)
	}
	if got := km.scopes(); len(got) != 0 {
		t.Fatalf("空管理器应无 scope，实际 %v", got)
	}
	if km.remove("g:123", d.ID) {
		t.Fatal("remove 不存在 ID 应返回 false")
	}
	// 不影响其它 scope（含全局库）
	other, _ := km.add("global", "", "全局文档", nil, "")
	if km.remove("g:123", other.ID) {
		t.Fatal("不应跨 scope 删除")
	}
	if got := km.store.count("global"); got != 1 {
		t.Fatalf("不应跨 scope 删除，global 应仍剩 1 篇，实际 %d", got)
	}
}

// TestKbMigrateLegacyKV 验证旧版「每 scope 一个 JSON 数组」的 kb: 数据
// 在构造管理器时被一次性搬入行级表，残留的空数组键被清理，且迁移幂等
// （重复构造不会重复入库）。
func TestKbMigrateLegacyKV(t *testing.T) {
	store := newSQLPFake()
	// 预置旧版数据：global 两篇文档（含向量与标签），g:123 一个空数组残留键
	legacy := store.Clone("kb:")
	if !legacy.Set(context.Background(), "global", []kbDoc{
		{
			ID:        "aaaabbbb",
			Scope:     "global",
			Title:     "旧文档一",
			Content:   "旧版正文一",
			Tags:      []string{"旧"},
			Source:    "url:https://old",
			CreatedAt: time.Unix(1700000000, 0).UTC(),
			Emb:       [][]float32{{0.1, 0.2}},
		},
		{
			// 历史脏数据：缺 ID，迁移时应补发
			Scope:     "global",
			Title:     "旧文档二",
			Content:   "旧版正文二",
			CreatedAt: time.Unix(1700000100, 0).UTC(),
		},
	}) {
		t.Fatal("预置旧版数据失败")
	}
	if !legacy.Set(context.Background(), "g:123", []kbDoc{}) {
		t.Fatal("预置空数组键失败")
	}

	km := newTestKnowledgeManagerWith(store, 0)

	docs := km.list("global")
	if len(docs) != 2 {
		t.Fatalf("迁移后应有两篇文档，实际 %d 篇", len(docs))
	}
	if docs[0].ID != "aaaabbbb" || docs[0].Title != "旧文档一" || !framesEqual(docs[0].Emb, [][]float32{{0.1, 0.2}}) {
		t.Fatalf("迁移后文档一内容不符: %+v", docs[0])
	}
	if docs[1].ID == "" || docs[1].Title != "旧文档二" {
		t.Fatalf("缺 ID 的旧文档应补发 ID 并保留内容: %+v", docs[1])
	}
	if !docs[0].CreatedAt.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Fatalf("迁移应保留创建时间: %v", docs[0].CreatedAt)
	}

	// 迁移完成后旧键应全部删除（含空数组残留键），作用域列表只由行级表决定
	if keys, _ := legacy.Keys(context.Background(), ""); len(keys) != 0 {
		t.Fatalf("迁移后旧键应清空，实际 %v", keys)
	}
	if got := km.scopes(); len(got) != 1 || got[0] != "global" {
		t.Fatalf("作用域列表不符: %v", got)
	}

	// 幂等：同一存储上再构造一次，不得重复入库
	km2 := newTestKnowledgeManagerWith(store, 0)
	if got := len(km2.list("global")); got != 2 {
		t.Fatalf("迁移应幂等，重复构造后仍应为 2 篇，实际 %d", got)
	}
}

// framesEqual 比较嵌套向量是否逐元素相等（nil 与空切片视为相等）。
func framesEqual(a, b [][]float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// TestKbDisabledWithoutSQL 非 SQL 持久化后端上知识库功能整体禁用
// （构造返回 nil，调用方按 nil 判空）。
func TestKbDisabledWithoutSQL(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if km := newKnowledgeManager(newPFake(), logger, 0, nil); km != nil {
		t.Fatal("非 SQL 存储上 newKnowledgeManager 应返回 nil")
	}
}

func TestKbUpdate(t *testing.T) {
	km := newTestKnowledgeManager(0)

	d, _ := km.add("g:123", "旧标题", "旧内容", []string{"旧标签"}, "url:https://old")
	created := d.CreatedAt

	if err := km.update("g:123", d.ID, "新标题", "新内容", []string{"新标签"}, "url:https://new"); err != nil {
		t.Fatalf("update 失败: %v", err)
	}
	docs := km.list("g:123")
	if len(docs) != 1 {
		t.Fatalf("update 后条数不符: %d", len(docs))
	}
	got := docs[0]
	if got.Title != "新标题" || got.Content != "新内容" || got.Source != "url:https://new" || len(got.Tags) != 1 {
		t.Fatalf("update 后内容不符: %+v", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("update 不应改变创建时间: %v -> %v", created, got.CreatedAt)
	}

	// ID 不存在 / 空内容
	if err := km.update("g:123", "deadbeef", "", "x", nil, ""); err == nil {
		t.Fatal("更新不存在的 ID 应报错")
	}
	if err := km.update("g:123", d.ID, "", "   ", nil, ""); err == nil {
		t.Fatal("空内容应报错")
	}
	// 不影响其它 scope
	if err := km.update("global", d.ID, "", "跨 scope", nil, ""); err == nil {
		t.Fatal("不应跨 scope 更新")
	}
}

func TestKbScopes(t *testing.T) {
	km := newTestKnowledgeManager(0)

	if got := km.scopes(); len(got) != 0 {
		t.Fatalf("空管理器应无 scope，实际 %v", got)
	}
	if _, err := km.add("g:123", "", "群文档", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := km.add("global", "", "全局文档", nil, ""); err != nil {
		t.Fatal(err)
	}
	got := km.scopes()
	// 字典序排序：'g:1' 中 ':'(0x3A) 小于 'global' 中的 'l'，故 g:123 在前
	if len(got) != 2 || got[0] != "g:123" || got[1] != "global" {
		t.Fatalf("scopes 不符（应排序）: %v", got)
	}
}

func TestKbContentTruncation(t *testing.T) {
	km := newTestKnowledgeManager(0)

	long := strings.Repeat("长", KbMaxContentRunes+100)
	d, err := km.add("global", "", long, nil, "")
	if err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	if got := len([]rune(d.Content)); got != KbMaxContentRunes+1 {
		t.Fatalf("add 未截断超长内容，符文数 = %d", got)
	}

	if err := km.update("global", d.ID, "", long, nil, ""); err != nil {
		t.Fatalf("update 失败: %v", err)
	}
	if got := len([]rune(km.list("global")[0].Content)); got != KbMaxContentRunes+1 {
		t.Fatalf("update 未截断超长内容，符文数 = %d", got)
	}
}

func TestKbChunkText(t *testing.T) {
	if got := chunkText(""); got != nil {
		t.Fatalf("空内容应返回 nil，实际 %v", got)
	}
	// 短内容原样返回
	if got := chunkText("短的正文"); len(got) != 1 || got[0] != "短的正文" {
		t.Fatalf("短内容应原样返回: %v", got)
	}
	// 长内容切成多块
	long := strings.Repeat("这是一个很长的段落内容。", kbChunkSize/10+10)
	chunks := chunkText(long)
	if len(chunks) < 2 {
		t.Fatalf("长内容应切成多块，实际 %d 块", len(chunks))
	}
	// 各块不超限，且所有块拼回后覆盖原文
	for i, c := range chunks {
		if len([]rune(c)) > kbChunkSize {
			t.Fatalf("块 %d 超长: %d", i, len([]rune(c)))
		}
	}
	if !strings.Contains(strings.Join(chunks, ""), "这是一个很长的段落内容。") {
		t.Fatal("切片丢失了原文内容")
	}
}

func TestKbQueryTerms(t *testing.T) {
	terms := queryTerms("docker 部署")
	joined := strings.Join(terms, ",")
	for _, want := range []string{"docker", "部署", "部", "署"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("queryTerms 缺少 %q: %v", want, terms)
		}
	}
	// 去重
	seen := map[string]int{}
	for _, tm := range terms {
		seen[tm]++
	}
	for tm, n := range seen {
		if n > 1 {
			t.Fatalf("term %q 重复 %d 次: %v", tm, n, terms)
		}
	}
}

func TestKbSearchRelevant(t *testing.T) {
	km := newTestKnowledgeManager(0)
	docker, err := km.add("global", "Docker 部署指南", "第一步：安装 docker 引擎。第二步：docker compose up -d 部署服务。", []string{"部署", "docker"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := km.add("global", "天气查询", "今日北京天气晴朗，气温 25 度。", nil, ""); err != nil {
		t.Fatal(err)
	}

	results := km.search("global", "docker 部署", 5)
	if len(results) == 0 {
		t.Fatal("应检索到相关文档")
	}
	if results[0].DocID != docker.ID {
		t.Fatalf("最相关文档应为 Docker 指南，实际 %+v", results[0])
	}
	if results[0].Chunk == "" {
		t.Fatal("应返回命中的内容块")
	}

	// 无关 query 返回空
	if got := km.search("global", "美食推荐", 5); len(got) != 0 {
		t.Fatalf("无关 query 应无命中，实际 %+v", got)
	}
}

func TestKbSearchScopeAndGlobal(t *testing.T) {
	km := newTestKnowledgeManager(0)
	local, err := km.add("g:123", "本地资料", "本群私有资料库：活动安排。", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := km.add("global", "全局资料", "全站公开资料：机器人使用说明。", nil, ""); err != nil {
		t.Fatal(err)
	}

	// 会话 scope 检索命中会话库
	results := km.search("g:123", "本地 私有 活动安排", 5)
	if len(results) == 0 {
		t.Fatal("会话库应命中")
	}
	if results[0].DocID != local.ID {
		t.Fatalf("会话库命中应优先，实际 %+v", results[0])
	}

	// 全局文档也应被会话 scope 检索到
	if got := km.search("g:123", "全站 公开 使用说明", 5); len(got) != 1 {
		t.Fatalf("全局文档应可被检索，实际 %+v", got)
	}
}

func TestKbSearchEmpty(t *testing.T) {
	km := newTestKnowledgeManager(0)
	if got := km.search("global", "随便什么", 5); got != nil {
		t.Fatalf("空库应返回 nil，实际 %v", got)
	}
	km.add("global", "", "只有一条内容", nil, "")
	if got := km.searchImpl("global", "   ", 5, nil); got != nil {
		t.Fatalf("空 query 应返回 nil，实际 %v", got)
	}
}

func TestKbAutoInject(t *testing.T) {
	km := newTestKnowledgeManager(0)

	// 无相关文档时不注入
	if got := km.autoInject("global", "帮我查下今天的天气", 30, nil); got != "" {
		t.Fatalf("无相关文档应返回空串，实际 %q", got)
	}

	km.add("global", "部署教程", "如何部署 AniaBot：make linux 编译，配置环境变量启动。", nil, "")
	injected := km.autoInject("global", "AniaBot 怎么部署？", 30, nil)
	if injected == "" {
		t.Fatal("命中相关文档时应注入上下文")
	}
	if !strings.Contains(injected, "部署教程") {
		t.Fatalf("注入内容应含文档标题: %q", injected)
	}
}

// TestKbAutoInjectSemantic 查询词与文档无关键词重叠时，语义相似度
// 达到 kbInjectMinSim 的块（queryVec 非 nil）也应被注入。
func TestKbAutoInjectSemantic(t *testing.T) {
	km := newTestKnowledgeManager(0)
	doc, err := km.add("global", "饮食偏好", "小明喜爱熬夜喝咖啡。", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// 手工为文档补向量（embedder=nil 时 add 不计算向量，直接写底层存储）
	cur, ok := km.store.get("global", doc.ID)
	if !ok {
		t.Fatal("读取文档失败")
	}
	cur.Emb = [][]float32{{1, 0}}
	if !km.store.update("global", cur) {
		t.Fatal("补写向量失败")
	}

	// 零关键词重叠 + queryVec=nil：不注入
	if got := km.autoInject("global", "他平时爱喝什么饮品", 30, nil); got != "" {
		t.Fatalf("无关键词重叠且 queryVec=nil 时不应注入: %q", got)
	}
	// 方向一致的查询向量：sim=1 >= kbInjectMinSim，纯语义命中应注入
	injected := km.autoInject("global", "他平时爱喝什么饮品", 30, []float32{1, 0})
	if !strings.Contains(injected, "小明喜爱熬夜喝咖啡") {
		t.Fatalf("语义命中应注入文档片段: %q", injected)
	}
	// 方向相反的向量：sim<=0 无加分，仍不注入
	if got := km.autoInject("global", "他平时爱喝什么饮品", 30, []float32{0, 1}); got != "" {
		t.Fatalf("相似度不足时不应注入: %q", got)
	}
}

func TestKbCosineSimilarity(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 0, 0}, []float32{1, 0, 0}); got < 0.99 {
		t.Fatalf("相同向量余弦应≈1，实际 %v", got)
	}
	if got := cosineSimilarity([]float32{1, 0}, []float32{0, 1}); got > 1e-9 {
		t.Fatalf("正交向量余弦应≈0，实际 %v", got)
	}
	if got := cosineSimilarity([]float32{1, 0}, []float32{1, 0, 0}); got != 0 {
		t.Fatalf("维度不一致应返回 0，实际 %v", got)
	}
	if got := cosineSimilarity(nil, []float32{1}); got != 0 {
		t.Fatalf("空向量应返回 0，实际 %v", got)
	}
}
