package pluginaichat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/storage"
)

// kbScopeGlobal 全局知识库作用域。与记忆系统不同，知识库除按会话隔离（g:/f:）
// 外还支持一份全局共享库，所有会话都可检索。
const kbScopeGlobal = "global"

// KbMaxContentRunes 单条文档内容的符文数上限，超出部分截断。
// 文档可比记忆条目长得多（支持整篇文章/URL 正文），但也要避免单行体积失控。
const KbMaxContentRunes = 8000

// kbChunkSize / kbChunkOverlap 检索块大小与块间重叠。
// 长文档切片入库，检索命中块而非整篇，避免无关内容占用上下文。
const (
	kbChunkSize    = 600
	kbChunkOverlap = 60
)

// kbDoc 一条知识库文档。
type kbDoc struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"` // global / g:会话ID / f:用户ID
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Source    string    `json:"source,omitempty"` // manual / url:https://...
	CreatedAt time.Time `json:"created_at"`
	// Emb 各检索块（与 chunkText(content) 顺序对齐）的语义向量，仅启用向量检索时非空。
	// float32 半精度即可满足余弦相似度精度需求，减小单行体积。
	Emb [][]float32 `json:"emb,omitempty"`
}

// ErrKBFull 单 scope 文档条数达到上限时返回，提示先清理旧文档。
var ErrKBFull = errors.New("知识库文档条数已达上限")

// kbStore 知识库存储后端：SQL 逐行存取（ania_kb_doc 表）。
// 去重、上限、截断与语义向量计算等逻辑留在 knowledgeManager 层，后端只做存取。
type kbStore interface {
	// list 读取指定 scope 的全部文档（按创建时间升序）；无记录或失败时返回 nil。
	list(scope string) []kbDoc
	// listMeta 读取指定 scope 的全部文档但不含向量（Emb 恒为空），
	// 供面板列表用——向量列单篇可达数百 KB，展示时加载纯属浪费。
	listMeta(scope string) []kbDoc
	// listDigest 读取指定 scope 的轻量列表（仅 ID/标题/内容，不含向量），
	// 供新增去重比较用——避免为去重加载 MB 级的向量数据。
	listDigest(scope string) []kbDoc
	// get 读取指定 scope 中一篇文档；不存在时返回 false。
	get(scope, id string) (kbDoc, bool)
	// insert 追加一篇文档（调用方已完成去重与上限检查）。
	insert(scope string, d kbDoc) bool
	// update 按 ID 覆盖一篇文档的可变字段；ID 不存在时返回 false。
	update(scope string, d kbDoc) bool
	// remove 按 ID 删除一篇文档；ID 不存在时返回 false。
	remove(scope, id string) bool
	// count 返回指定 scope 的文档条数（不加载文档内容，供面板统计用）。
	count(scope string) int
	// scopes 列出已有文档的全部 scope（排序后返回）。
	scopes() []string
}

// knowledgeManager 知识库管理器：按作用域（global / g:会话ID / f:用户ID）存取文档。
//
// 与 memoryManager 同构：持久化固定为 SQL 行级存储（ania_kb_doc 表，每篇文档一行），
// 增删改只写单行、读取量与被读文档成正比。所有变更在 mu 保护下串行落盘；
// 存储错误内部记录日志，不拖垮主对话流程。
type knowledgeManager struct {
	store kbStore
	// legacy 旧版整段 JSON 数据所在的 KV 子命名空间（kb:），仅启动时做一次性
	// 迁移用，迁移完成后不再读写。
	legacy   storage.PersistentStorage
	logger   *slog.Logger
	maxDocs  int       // 单 scope 文档条数上限，<=0 表示不限制
	embedder *embedder // 可选语义向量计算；nil 表示仅关键词检索

	mu sync.Mutex
}

// newKnowledgeManager 创建知识库管理器。持久化固定为 SQL 行级存储
// （ania_kb_doc 表）；探测或建表失败时返回 nil（调用方按 nil 判空，
// 知识库功能整体禁用），仅记录错误日志。构造时顺带把旧版 kb: 命名空间
// 中的整段 JSON 文档一次性迁移进新表。
func newKnowledgeManager(store storage.PersistentStorage, logger *slog.Logger, maxDocs int, emb *embedder) *knowledgeManager {
	db, dialect, ok := storage.SQLBackend(store)
	if !ok {
		logger.Error("持久化存储不支持 SQL，知识库功能禁用")
		return nil
	}
	if err := storage.EnsureTables(context.Background(), db, dialect, kbTables...); err != nil {
		logger.Error("创建知识库表失败，知识库功能禁用", "error", err.Error())
		return nil
	}
	km := &knowledgeManager{
		store:    newSQLKbStore(db, logger),
		legacy:   store.Clone("kb:"),
		logger:   logger,
		maxDocs:  maxDocs,
		embedder: emb,
	}
	km.migrateLegacyKV()
	km.startBackfill()
	return km
}

// startBackfill 在 embedder 可用时启动后台 goroutine，为启用向量检索之前
// 写入、因而缺少语义向量的存量文档补算 embedding。失败文档静默跳过，
// 下次重启再试；不阻塞插件启动。
func (km *knowledgeManager) startBackfill() {
	if km.embedder == nil {
		return
	}
	go km.backfillEmbeddings()
}

// backfillEmbeddings 遍历所有 scope，为缺向量（或向量数与检索块数不一致）
// 的文档逐篇补算并落盘。写回时锁内按 ID 重新定位，避免与并发的
// update/remove 互相覆盖。
func (km *knowledgeManager) backfillEmbeddings() {
	filled := 0
	for _, scope := range km.scopes() {
		for _, doc := range km.list(scope) {
			chunks := chunkText(doc.Content)
			if len(doc.Emb) == len(chunks) {
				continue
			}
			emb := km.embedder.EmbedMany(context.Background(), chunks)
			if len(emb) != len(chunks) {
				continue // 计算失败静默跳过，下次重启再试
			}
			km.mu.Lock()
			cur, exists := km.store.get(scope, doc.ID)
			if !exists {
				km.mu.Unlock()
				continue // 文档已被并发删除
			}
			cur.Emb = emb
			saved := km.store.update(scope, cur)
			km.mu.Unlock()
			if !saved {
				km.logger.Warn("回填知识库向量落盘失败", "scope", scope, "id", doc.ID)
			} else {
				filled++
			}
			time.Sleep(backfillInterval)
		}
	}
	if filled > 0 {
		km.logger.Info("存量知识库向量回填完成", "filled", filled)
	}
}

// normalizeDoc 规范化标题+内容用于去重比较。
func normalizeDoc(title, content string) string {
	return strings.Join(strings.Fields(title), " ") + "\n" + strings.Join(strings.Fields(content), " ")
}

// list 读取指定 scope 的全部文档（含向量）；无记录或读取失败时返回 nil。
func (km *knowledgeManager) list(scope string) []kbDoc {
	km.mu.Lock()
	defer km.mu.Unlock()
	return km.store.list(scope)
}

// listMeta 读取指定 scope 的全部文档但不含向量（供面板列表展示）。
func (km *knowledgeManager) listMeta(scope string) []kbDoc {
	km.mu.Lock()
	defer km.mu.Unlock()
	return km.store.listMeta(scope)
}

// add 追加一条文档，返回写入后的文档（含生成的 ID）。
// 标题+内容与已有文档重复（规范化后相同）时不重复写入，返回已有文档；
// 达到 maxDocs 上限时返回 ErrKBFull；超长内容按 KbMaxContentRunes 截断。
func (km *knowledgeManager) add(scope, title, content string, tags []string, source string) (kbDoc, error) {
	content = tasklog.Truncate(strings.TrimSpace(content), KbMaxContentRunes)
	if content == "" {
		return kbDoc{}, errors.New("文档内容不能为空")
	}
	title = strings.TrimSpace(title)

	// 计算语义向量放在锁外，避免 embedding API 网络耗时阻塞其它读写。
	// 失败时返回 nil，退回纯关键词检索。
	var emb [][]float32
	if km.embedder != nil {
		emb = km.embedder.EmbedMany(context.Background(), chunkText(content))
	}

	km.mu.Lock()
	defer km.mu.Unlock()

	norm := normalizeDoc(title, content)
	for _, d := range km.store.listDigest(scope) {
		if normalizeDoc(d.Title, d.Content) == norm {
			// 已存在相同文档，不重复写入
			if full, ok := km.store.get(scope, d.ID); ok {
				return full, nil
			}
			return d, nil
		}
	}
	if km.maxDocs > 0 && km.store.count(scope) >= km.maxDocs {
		return kbDoc{}, fmt.Errorf("%w（%d 条），请先删除部分文档", ErrKBFull, km.maxDocs)
	}

	doc := kbDoc{
		ID:        newKbID(),
		Scope:     scope,
		Title:     title,
		Content:   content,
		Tags:      tags,
		Source:    source,
		CreatedAt: time.Now(),
		Emb:       emb,
	}
	if ok := km.store.insert(scope, doc); !ok {
		km.logger.Error("保存知识库文档失败", "scope", scope, "title", title)
		return kbDoc{}, errors.New("文档保存失败，请查看日志")
	}
	return doc, nil
}

// remove 按 ID 删除指定 scope 中的一条文档；ID 不存在时返回 false。
// 行级存储下删除最后一行后该作用域自然消失，面板不会再列出空作用域。
func (km *knowledgeManager) remove(scope, id string) bool {
	km.mu.Lock()
	defer km.mu.Unlock()

	return km.store.remove(scope, id)
}

// update 按 ID 更新指定 scope 中一条文档的标题、内容、标签与来源；
// ID 不存在时返回错误。创建时间保留不变；超长内容按 KbMaxContentRunes 截断。
func (km *knowledgeManager) update(scope, id, title, content string, tags []string, source string) error {
	content = tasklog.Truncate(strings.TrimSpace(content), KbMaxContentRunes)
	if content == "" {
		return errors.New("文档内容不能为空")
	}
	title = strings.TrimSpace(title)

	var emb [][]float32
	if km.embedder != nil {
		emb = km.embedder.EmbedMany(context.Background(), chunkText(content))
	}

	km.mu.Lock()
	defer km.mu.Unlock()

	doc, ok := km.store.get(scope, id)
	if !ok {
		return fmt.Errorf("文档不存在: %s", id)
	}
	doc.Title = title
	doc.Content = content
	doc.Tags = tags
	doc.Source = source
	doc.Emb = emb
	if !km.store.update(scope, doc) {
		km.logger.Error("更新知识库文档后落盘失败", "scope", scope, "id", id)
		return errors.New("文档保存失败，请查看日志")
	}
	return nil
}

// scopes 列出当前已有文档的全部作用域，排序后返回。
// 供 Web 面板的知识库管理页使用。行级存储下条数为 0 的作用域不存在行，
// 天然不会被列出。
func (km *knowledgeManager) scopes() []string {
	km.mu.Lock()
	defer km.mu.Unlock()

	return km.store.scopes()
}

// count 返回指定 scope 的文档条数（不加载文档内容，供面板统计用）。
func (km *knowledgeManager) count(scope string) int {
	km.mu.Lock()
	defer km.mu.Unlock()

	return km.store.count(scope)
}

// newKbID 生成短随机 ID（8 位十六进制），与记忆 ID 同款退化策略。
func newKbID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// ---- 切片 ----

// chunkText 把长文档切成若干检索块。
// 按换行分块（中文文档段落通常以换行分隔），块超长时硬切，块间保留
// overlap 字符避免信息被切断。短内容原样返回。空内容返回 nil。
func chunkText(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	runes := []rune(content)
	if len(runes) <= kbChunkSize {
		return []string{content}
	}

	lines := strings.Split(content, "\n")
	chunks := make([]string, 0, len(runes)/kbChunkSize+1)
	buf := make([]rune, 0, kbChunkSize)
	for _, line := range lines {
		lr := []rune(line)
		if len(buf) > 0 && len(buf)+len(lr)+1 > kbChunkSize {
			// 当前块已满，落盘并携带末尾 overlap 作为下一块前缀
			chunks = append(chunks, string(buf))
			keep := min(len(buf), kbChunkOverlap)
			buf = append(buf[:0], buf[len(buf)-keep:]...)
			buf = append(buf, '\n')
		} else if len(buf) > 0 {
			buf = append(buf, '\n')
		}
		buf = append(buf, lr...)
		// 单行（超长段落）硬切
		for len(buf) > kbChunkSize {
			chunks = append(chunks, string(buf[:kbChunkSize]))
			keep := min(len(buf), kbChunkOverlap)
			buf = append(buf[:0], buf[len(buf)-keep:]...)
		}
	}
	if len(buf) > 0 {
		chunks = append(chunks, string(buf))
	}
	return chunks
}

// ---- 检索 ----

// kbChunkResult 一个命中的检索块及其来源信息。
type kbChunkResult struct {
	DocID string
	Title string
	Scope string
	Chunk string
	Score int
	// Sim 该块与查询向量的余弦相似度；未启用向量检索或无向量时为 0。
	// 自动注入用它放宽纯语义命中的准入门槛。
	Sim float64
}

// queryTerms 从检索词生成匹配词条集合。
// 英文/数字按整词匹配；中文按相邻二元组切分（2~3 字短词补充单字），
// 兼顾召回与精度，无需引入分词库。去重后返回。
func queryTerms(query string) []string {
	seen := make(map[string]struct{}, 8)
	terms := make([]string, 0, 8)
	add := func(t string) {
		if t == "" {
			return
		}
		if _, ok := seen[t]; ok {
			return
		}
		seen[t] = struct{}{}
		terms = append(terms, t)
	}
	for field := range strings.FieldsSeq(strings.ToLower(query)) {
		runes := []rune(field)
		if len(runes) == 0 {
			continue
		}
		add(field) // 整词
		if len(runes) <= 1 {
			continue
		}
		// 相邻二元组
		for i := 0; i < len(runes)-1; i++ {
			add(string(runes[i : i+2]))
		}
		// 2~3 字短词补充单字，提升召回（如「部署」→「部」「署」）
		if len(runes) <= 3 {
			for _, r := range runes {
				add(string(r))
			}
		}
	}
	return terms
}

// kbCandidate 一个待评分的文档块。
type kbCandidate struct {
	doc    kbDoc
	chunk  string
	local  bool // 是否属于当前会话 scope（而非全局）
	embIdx int  // 该块在 doc.Emb 中的下标；无向量时为 -1
}

// termHits 判断 term 是否命中候选块（正文/标题/tag 任一）。
func termHits(c kbCandidate, term string) bool {
	if strings.Contains(strings.ToLower(c.chunk), term) {
		return true
	}
	if c.doc.Title != "" && strings.Contains(strings.ToLower(c.doc.Title), term) {
		return true
	}
	for _, tag := range c.doc.Tags {
		if strings.Contains(strings.ToLower(tag), term) {
			return true
		}
	}
	return false
}

// scoreChunk 计算候选块与词条的匹配得分。
// 标题命中权重最高，tag 次之，正文最低；配合局部 IDF 抑制「这个」「怎么」等泛化词。
func scoreChunk(c kbCandidate, terms []string, df map[string]int, total float64) int {
	text := strings.ToLower(c.chunk)
	title := strings.ToLower(c.doc.Title)
	score := 0
	for _, t := range terms {
		if df[t] == 0 {
			continue
		}
		// 平滑 IDF：仅出现在少量块中的 term 区分度强，权重高
		idf := math.Log((total+1)/float64(df[t]+1)) + 1
		if strings.Contains(text, t) {
			score += int(idf * 10)
		}
		if title != "" && strings.Contains(title, t) {
			score += int(idf * 30)
		}
		for _, tag := range c.doc.Tags {
			if strings.Contains(strings.ToLower(tag), t) {
				score += int(idf * 20)
			}
		}
	}
	return score
}

// search 在指定 scope 及全局知识库中检索匹配的文本块，按相关度降序返回 topK。
// 关键词打分 + 语义向量混合（启用向量检索时）。供 AI 的 kb_search 工具使用。
func (km *knowledgeManager) search(scope, query string, topK int) []kbChunkResult {
	var queryVec []float32
	if km.embedder != nil {
		queryVec = km.embedder.EmbedOne(context.Background(), query)
	}
	return km.searchImpl(scope, query, topK, queryVec)
}

// searchImpl 检索实现。queryVec 为调用方预算好的查询向量：非 nil 时启用语义
// 混合打分，nil 时纯关键词。自动注入路径把每轮一次预算的向量透传进来，
// 避免用户消息被重复 embed。
func (km *knowledgeManager) searchImpl(scope, query string, topK int, queryVec []float32) []kbChunkResult {
	if topK <= 0 {
		topK = 5
	}
	terms := queryTerms(query)
	if len(terms) == 0 && len(queryVec) == 0 {
		return nil
	}

	// 收集候选块：会话库 + 全局库
	var cands []kbCandidate
	if scope != "" && scope != kbScopeGlobal {
		for _, doc := range km.list(scope) {
			for i, chunk := range chunkText(doc.Content) {
				cands = append(cands, kbCandidate{doc: doc, chunk: chunk, local: true, embIdx: i})
			}
		}
	}
	for _, doc := range km.list(kbScopeGlobal) {
		for i, chunk := range chunkText(doc.Content) {
			cands = append(cands, kbCandidate{doc: doc, chunk: chunk, embIdx: i})
		}
	}
	if len(cands) == 0 {
		return nil
	}

	// 局部 IDF
	df := make(map[string]int, len(terms))
	for _, t := range terms {
		for _, c := range cands {
			if termHits(c, t) {
				df[t]++
			}
		}
	}
	total := float64(len(cands))

	results := make([]kbChunkResult, 0, len(cands))
	for _, c := range cands {
		score := scoreChunk(c, terms, df, total)
		var sim float64
		if len(queryVec) > 0 && c.embIdx >= 0 && c.embIdx < len(c.doc.Emb) {
			if s := cosineSimilarity(c.doc.Emb[c.embIdx], queryVec); s > 0 {
				sim = s
				score += int(s * 40) // 语义相似度加分，与关键词分数同一量纲
			}
		}
		if score <= 0 {
			continue
		}
		if c.local {
			score += 5 // 会话内知识库命中加权，优先于全局
		}
		results = append(results, kbChunkResult{
			DocID: c.doc.ID,
			Title: c.doc.Title,
			Scope: c.doc.Scope,
			Chunk: c.chunk,
			Score: score,
			Sim:   sim,
		})
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > topK {
		results = results[:topK]
	}
	return results
}

// cosineSimilarity 计算两个向量（float32）的余弦相似度；维度不一致或零向量返回 0。
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// kbInjectMinSim 自动注入的纯语义命中准入门槛：关键词零分但余弦相似度
// 达到该值的块也会被注入（同义不同词的话题，如「饮品」命中「咖啡」）。
const kbInjectMinSim = 0.35

// autoInject 对用户消息做检索，命中相关片段时拼成一段上下文前缀返回
// （供注入到对话请求）。queryVec 为调用方预算好的用户消息向量：非 nil 时
// 走关键词+语义混合检索（纯语义命中按 kbInjectMinSim 准入），nil 时退回
// 纯关键词 + threshold 绝对分门槛（泛化词因 IDF 被压低，难以单独越过门槛）。
// 无命中或分数不足返回空串。
func (km *knowledgeManager) autoInject(scope, userMsg string, threshold int, queryVec []float32) string {
	if strings.TrimSpace(userMsg) == "" {
		return ""
	}
	if threshold <= 0 {
		threshold = 30
	}
	results := km.searchImpl(scope, userMsg, 3, queryVec)
	if len(results) == 0 {
		return ""
	}
	var sb strings.Builder
	wrote := false
	for _, r := range results {
		if r.Score < threshold && r.Sim < kbInjectMinSim {
			continue
		}
		if !wrote {
			sb.WriteString("【知识库】以下是知识库中与当前话题相关的资料，可据此回答（与话题无关可忽略）：\n")
			wrote = true
		}
		if r.Title != "" {
			sb.WriteString("《" + r.Title + "》")
			if r.Scope != kbScopeGlobal {
				sb.WriteString("（" + r.Scope + "）")
			}
			sb.WriteString("：")
		}
		sb.WriteString(tasklog.Truncate(r.Chunk, 400))
		sb.WriteString("\n")
	}
	if !wrote {
		return ""
	}
	return strings.TrimRight(sb.String(), "\n")
}
