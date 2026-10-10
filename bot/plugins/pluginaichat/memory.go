package pluginaichat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/storage"
)

// memoryEntry 一条长期记忆。
//
// 与会话内上下文（messageWindow）不同，长期记忆跨会话、跨重启保留，
// 由 AI 通过 memory_save / memory_search / memory_forget 工具自行管理。
// 记忆按会话 scope（g:会话ID / f:用户ID）隔离，群与群、群与私聊之间互不可见，
// 避免跨会话信息泄露。
type memoryEntry struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id,omitempty"` // 关联的用户 ID；空表示属于整个会话（群规、共同约定等）
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Emb 内容的语义向量（与知识库共用 embedding 服务）；计算失败或服务未
	// 启用时为 nil。旧数据无此字段，检索时跳过语义加分，兼容性良好。
	Emb []float32 `json:"emb,omitempty"`
}

// ErrMemoryFull 单会话记忆条数达到上限时返回，提示 AI 先清理或合并旧记忆。
var ErrMemoryFull = errors.New("记忆条数已达上限")

// MaxContentRunes 单条记忆内容的符文数上限，超出部分截断
// （记忆会整体注入上下文与检索结果，单条过长挤占 token）。
const MaxContentRunes = 2000

// memoryInjectMaxRunes 主动注入块的字符数上限：注入内容追加在消息尾部，
// 超限会白白占用上下文，从分数最低的条目开始截断。
const memoryInjectMaxRunes = 1500

// memoryStore 记忆存储后端：SQL 逐行存取（ania_memory 表）。
// 去重、上限、截断与语义向量计算等逻辑留在 memoryManager 层，后端只做存取。
type memoryStore interface {
	// list 读取指定 scope 的全部记忆（按创建时间升序）；无记录或失败时返回 nil。
	list(scope string) []memoryEntry
	// insert 追加一条记忆（调用方已完成去重与上限检查）。
	insert(scope string, e memoryEntry) bool
	// update 按 ID 覆盖一条记忆的可变字段；ID 不存在时返回 false。
	update(scope string, e memoryEntry) bool
	// remove 按 ID 删除一条记忆；ID 不存在时返回 false。
	remove(scope, id string) bool
	// scopes 列出已有记忆的全部 scope（排序后返回）。
	scopes() []string
}

// memoryManager 长期记忆管理器：按会话 scope 存取记忆条目。
//
// 每条记忆一行（ania_memory 表）。所有变更在 mu 保护下串行落盘；存储错误
// 内部记录日志，不拖垮主对话流程（与 HistoryStore 风格一致）。
type memoryManager struct {
	store      memoryStore
	logger     *slog.Logger
	maxEntries int // 单 scope 记忆条数上限，<=0 表示不限制
	// embedder 语义向量计算器：与知识库共享同一实例（复用 kb.embedding 配置）；
	// nil 时记忆检索保持纯关键词（与历史行为一致）。
	embedder *embedder

	mu sync.Mutex
}

// newMemoryManager 创建记忆管理器。持久化存储固定为 SQL 后端（sqlite/mysql），
// 记忆走 ania_memory 行级存储；探测或建表失败时返回 nil（调用方按 nil 判空，
// 记忆相关功能整体禁用），仅记录错误日志。
func newMemoryManager(store storage.PersistentStorage, logger *slog.Logger, maxEntries int, embedder *embedder) *memoryManager {
	db, dialect, ok := storage.SQLBackend(store)
	if !ok {
		logger.Error("持久化存储不支持 SQL，长期记忆功能禁用")
		return nil
	}
	if err := storage.EnsureTables(context.Background(), db, dialect, memoryTables...); err != nil {
		logger.Error("创建长期记忆表失败，长期记忆功能禁用", "error", err.Error())
		return nil
	}
	m := &memoryManager{
		store:      newSQLMemoryStore(db, logger),
		logger:     logger,
		maxEntries: maxEntries,
		embedder:   embedder,
	}
	m.startBackfill()
	return m
}

// backfillInterval 存量向量回填的逐条间隔：回填是后台任务，放慢节奏
// 避免触发 embedding 服务限流，也不与前台对话争抢配额。
const backfillInterval = 200 * time.Millisecond

// startBackfill 在 embedder 可用时启动后台 goroutine，为启用向量检索之前
// 写入、因而缺少语义向量的存量记忆补算 embedding。失败条目静默跳过，
// 下次重启再试；不阻塞插件启动。
func (m *memoryManager) startBackfill() {
	if m.embedder == nil {
		return
	}
	go m.backfillEmbeddings()
}

// backfillEmbeddings 遍历所有 scope，为缺向量的记忆逐条补算并落盘。
func (m *memoryManager) backfillEmbeddings() {
	filled := 0
	for _, scope := range m.scopes() {
		for _, e := range m.list(scope) {
			if len(e.Emb) > 0 {
				continue
			}
			vec := m.embedder.EmbedOne(context.Background(), e.Content)
			if len(vec) == 0 {
				continue // 计算失败静默跳过，下次重启再试
			}
			m.mu.Lock()
			e.Emb = vec
			if ok := m.store.update(scope, e); !ok {
				m.logger.Warn("回填记忆向量落盘失败", "scope", scope, "id", e.ID)
			} else {
				filled++
			}
			m.mu.Unlock()
			time.Sleep(backfillInterval)
		}
	}
	if filled > 0 {
		m.logger.Info("存量记忆向量回填完成", "filled", filled)
	}
}

// normalizeMemoryContent 规范化记忆内容用于去重比较。
func normalizeMemoryContent(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// memorySimilarThreshold 近似重复判定的相似度阈值（词元集合的 Dice 系数）。
// 取 0.55：同一件事换个说法（「小明喜欢喝咖啡」vs「小明喜欢熬夜喝咖啡」约 0.63）
// 能命中，仅在个别词上重合的不同事实（共享主语等，通常低于 0.3）不会误报。
const memorySimilarThreshold = 0.55

// memoryTermSet 用与检索同一套 CJK 分词（整词 + 相邻二元组）把文本转成词元集合。
func memoryTermSet(s string) map[string]struct{} {
	terms := queryTerms(s)
	if len(terms) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		set[t] = struct{}{}
	}
	return set
}

// diceSimilarity 两个词元集合的 Dice 系数（2|A∩B| / (|A|+|B|)），范围 [0,1]。
func diceSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(a)+len(b))
}

// findSimilar 返回与 content 高度相似（Dice 系数 >= memorySimilarThreshold）的
// 其它记忆，按相似度降序最多 limit 条；excludeID 用于排除条目自身。
// 与精确去重互补：识别换个说法的近似重复，供写入时提示 AI 合并。
func (m *memoryManager) findSimilar(scope, content, excludeID string, limit int) []memoryEntry {
	if limit <= 0 {
		limit = 1
	}
	base := memoryTermSet(content)
	if len(base) == 0 {
		return nil
	}

	type scored struct {
		e     memoryEntry
		score float64
	}
	var matched []scored
	for _, e := range m.list(scope) {
		if e.ID == excludeID {
			continue
		}
		if s := diceSimilarity(base, memoryTermSet(e.Content)); s >= memorySimilarThreshold {
			matched = append(matched, scored{e, s})
		}
	}
	for i := 1; i < len(matched); i++ {
		for j := i; j > 0 && matched[j].score > matched[j-1].score; j-- {
			matched[j], matched[j-1] = matched[j-1], matched[j]
		}
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]memoryEntry, len(matched))
	for i, sc := range matched {
		out[i] = sc.e
	}
	return out
}

// similarHint 生成写入后的近似重复提示：新条目与已有记忆高度相似时，建议合并
// 为一条（memory_update 更新保留的条目，再 memory_forget 删除多余的）。
// 无相似记忆时返回空串。
func (m *memoryManager) similarHint(scope, excludeID, content string) string {
	similar := m.findSimilar(scope, content, excludeID, 1)
	if len(similar) == 0 {
		return ""
	}
	e := similar[0]
	return fmt.Sprintf("提示：与已有记忆 [%s]「%s」高度相似。若是同一件事的不同说法，请合并为一条：用 memory_update 把合并后的完整内容更新到 [%s]，再 memory_forget 删除 [%s]；若确实是两件独立的事，忽略本提示。",
		e.ID, tasklog.Truncate(e.Content, 100), e.ID, excludeID)
}

// list 读取指定 scope 的全部记忆；无记录或读取失败时返回 nil。
func (m *memoryManager) list(scope string) []memoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked(scope)
}

func (m *memoryManager) listLocked(scope string) []memoryEntry {
	return m.store.list(scope)
}

// add 追加一条记忆，返回写入后的条目（含生成的 ID）。
// 内容与已有记忆重复（规范化后相同）时不重复写入，返回已有条目；
// 达到 maxEntries 上限时返回 ErrMemoryFull；超长内容按 MaxContentRunes 截断。
func (m *memoryManager) add(scope, userID, content string, tags []string) (memoryEntry, error) {
	entry, _, err := m.addEntry(scope, userID, content, tags)
	return entry, err
}

// addEntry 与 add 相同，额外返回是否为新写入的条目（false 表示命中精确去重，
// 返回的是已有条目）。AI 工具据此区分「已记住」与「已有相同内容，未重复保存」。
func (m *memoryManager) addEntry(scope, userID, content string, tags []string) (memoryEntry, bool, error) {
	content = tasklog.Truncate(strings.TrimSpace(content), MaxContentRunes)
	if content == "" {
		return memoryEntry{}, false, errors.New("记忆内容不能为空")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	entries := m.listLocked(scope)
	norm := normalizeMemoryContent(content)
	for _, e := range entries {
		if normalizeMemoryContent(e.Content) == norm {
			// 已存在相同记忆，不重复写入
			return e, false, nil
		}
	}
	if m.maxEntries > 0 && len(entries) >= m.maxEntries {
		return memoryEntry{}, false, fmt.Errorf("%w（%d 条），请先用 memory_update 合并或 memory_forget 删除旧记忆", ErrMemoryFull, m.maxEntries)
	}

	entry := memoryEntry{
		ID:        newMemoryID(),
		UserID:    strings.TrimSpace(userID),
		Content:   content,
		Tags:      tags,
		CreatedAt: time.Now().UTC(),
	}
	// 入库时计算语义向量（记忆写入频率极低，锁内调用可接受；失败静默降级为纯关键词）
	m.embedEntry(&entry)
	if ok := m.store.insert(scope, entry); !ok {
		m.logger.Error("保存记忆失败", "scope", scope)
		return memoryEntry{}, false, errors.New("记忆保存失败，请查看日志")
	}
	return entry, true, nil
}

// get 按 ID 读取指定 scope 中的一条记忆；ID 不存在时返回 false。
func (m *memoryManager) get(scope, id string) (memoryEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.listLocked(scope) {
		if e.ID == id {
			return e, true
		}
	}
	return memoryEntry{}, false
}

// embedEntry 计算单条记忆的语义向量；embedder 未启用（nil）或计算失败时
// 保持 nil，检索时自动跳过语义加分（纯关键词），不阻断记忆写入。
func (m *memoryManager) embedEntry(entry *memoryEntry) {
	if m.embedder == nil {
		return
	}
	if vec := m.embedder.EmbedOne(context.Background(), entry.Content); len(vec) > 0 {
		entry.Emb = vec
	}
}

// remove 按 ID 删除指定 scope 中的一条记忆；ID 不存在时返回 false。
func (m *memoryManager) remove(scope, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.remove(scope, id)
}

// scopes 列出当前已有记忆的全部会话 scope（g:会话ID / f:用户ID），排序后返回。
// 供 Web 面板的记忆管理页使用。
func (m *memoryManager) scopes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.scopes()
}

// update 按 ID 更新指定 scope 中一条记忆的内容、关联用户 ID 与标签；
// ID 不存在时返回错误。创建时间保留不变；超长内容按 MaxContentRunes 截断。
func (m *memoryManager) update(scope, id, userID, content string, tags []string) error {
	content = tasklog.Truncate(strings.TrimSpace(content), MaxContentRunes)
	if content == "" {
		return errors.New("记忆内容不能为空")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	entries := m.listLocked(scope)
	for _, e := range entries {
		if e.ID == id {
			e.UserID = strings.TrimSpace(userID)
			e.Content = content
			e.Tags = tags
			// 内容变更后语义向量需要重新计算
			m.embedEntry(&e)
			if ok := m.store.update(scope, e); !ok {
				m.logger.Error("更新记忆后落盘失败", "scope", scope, "id", id)
				return errors.New("记忆保存失败，请查看日志")
			}
			return nil
		}
	}
	return fmt.Errorf("记忆不存在: %s", id)
}

// autoInject 对用户消息做相关度检索，把相关记忆拼成一段「【长期记忆】…」
// 上下文块返回，供调用方注入到用户消息前（尾部注入：system 保持不变，
// 不影响上游前缀缓存；用户消息不落盘，注入内容不会污染持久化历史）。
//
// queryVec 为调用方预算好的用户消息向量：非 nil 时关键词+语义混合打分
// （同义不同词的记忆也能命中，如「饮品」命中「咖啡」），nil 时退回纯
// 关键词检索（与历史行为一致）。无命中返回空串。
func (m *memoryManager) autoInject(scope, userMsg string, max int, queryVec []float32) string {
	if strings.TrimSpace(userMsg) == "" {
		return ""
	}
	if max <= 0 {
		max = 3
	}
	entries := m.list(scope)
	if len(entries) == 0 {
		return ""
	}
	matched := filterMemoryByRelevance(entries, queryTerms(userMsg), queryVec)
	if len(matched) == 0 {
		return ""
	}
	if len(matched) > max {
		matched = matched[:max]
	}

	var sb strings.Builder
	sb.WriteString("【长期记忆】以下记忆可能与当前话题相关，可参考（与话题无关可忽略）；如发现与当前事实不符或已过时，用 memory_update 按方括号中的 ID 更正：\n")
	budget := memoryInjectMaxRunes
	for _, e := range matched {
		if budget <= 0 {
			break
		}
		line := tasklog.Truncate(formatMemoryLine(e), budget)
		sb.WriteString(line)
		sb.WriteString("\n")
		budget -= utf8.RuneCountInString(line) + 1
	}
	return strings.TrimRight(sb.String(), "\n")
}

// newMemoryID 生成短随机 ID（8 位十六进制）。
// 单 scope 条数有限（百级），随机碰撞概率可忽略；即便碰撞也仅表现为
// memory_forget 误删同 ID 的另一条，影响可控。
func newMemoryID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 加密随机数不可用时退化为时间戳，仍然可用
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}
