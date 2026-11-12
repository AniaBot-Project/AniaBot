// Package msglog 提供消息日志记录能力。
//
// 与 tasklog（持久化执行日志）不同，msglog 面向高频的收消息 / 通知事件，
// 底层使用缓存存储（storage.Storage）：缓存驱动为 memory 时仅保留在进程内，
// 重启后清空（与旧版内存环形缓冲行为一致）；驱动为 redis 时日志随 Redis
// 保存，重启后仍可回看。供 Web 控制面板的「消息日志」页实时展示群消息、
// 好友消息与通知事件。
//
// 存储结构刻意拆分为多个键，避免「一个大 JSON 塞一个 key」的读写放大：
//   - msglog:seq     自增 ID 计数器（十进制字符串）
//   - msglog:entries 列表，每个元素是一条独立日志（LPush 新条目在前，
//     LTrim 按容量上限滚动淘汰最旧条目）
//
// 会话索引（面板聊天式视图的侧栏与按会话分页）不额外占存储：Recorder 在首次
// 查询会话时扫描一遍 entries 建立「会话 → 日志 ID」内存索引，之后由 Add 增量
// 维护；索引只保存 uint64，20 万条日志的内存开销也在 MB 级。
//
// 命名空间由调用方在注入存储时隔离，本组件不再 Clone。
package msglog

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// Type 日志类型
type Type string

const (
	TypeGroup  Type = "group"  // 群消息
	TypeFriend Type = "friend" // 好友（私聊）消息
	TypeNotice Type = "notice" // 通知事件（群成员变动、撤回、戳一戳等）
)

// 会话类型（面板侧栏分组用）
const (
	SessionGroup  = "group"  // 群聊会话
	SessionFriend = "friend" // 私聊会话
	SessionSystem = "system" // 系统通知（既不属群也不属好友的事件）
)

// 媒体类型（面板内联展示 / 下载用）
const (
	MediaImage  = "image"
	MediaFile   = "file"
	MediaRecord = "record"
	MediaVideo  = "video"
)

const (
	seqKey     = "msglog:seq"     // 自增 ID 计数器
	entriesKey = "msglog:entries" // 日志列表（新在前，每条日志一个元素）
	// defaultMax 默认保留条数；面板「消息日志」前端按同一上限裁剪列表，两边保持一致。
	defaultMax = 5000

	systemSessionKey = "n:system" // 系统通知会话键
	// lastTextMaxRunes 会话预览文本的截断长度：侧栏只展示一行，无需传输全文。
	lastTextMaxRunes = 100
	// minPageBatch 分页扫描的最小批量：日志被删除后成为墓碑，仍需占一个列表
	// 槽位，逐条读取会让「翻一页」变成多次存储往返。
	minPageBatch = 32
)

// Media 消息中的媒体 / 文件，供面板在聊天气泡内联展示图片、提供文件下载。
// 只保留可被浏览器直接访问的 http(s) 地址：data:/base64: 内联负载体积可达
// MB 级，复制进每条日志会让缓存存储（尤其 Redis）迅速膨胀，因此落盘时丢弃，
// 由 Text 里的占位符提示「此处有媒体」。
//
// URL 往往带有时效性鉴权参数（QQ 的 rkey 约 2 小时过期），过期后面板会经
// adminpanel 的媒体代理按 Entry.MessageId / 本结构体的 FileId 重新向平台
// 申请地址（见 pluginlog.MsgLogMediaURL），因此这两个字段需要尽量保留。
type Media struct {
	Kind   string `json:"kind"`              // image / file / record / video
	URL    string `json:"url,omitempty"`     // 远程地址（可能为空，表示暂无下载地址）
	Name   string `json:"name,omitempty"`    // 文件名 / 语音视频标题
	Size   int64  `json:"size,omitempty"`    // 文件大小（字节）
	FileId string `json:"file_id,omitempty"` // 平台文件 ID（媒体地址过期时用于续期）
}

// Entry 一条消息日志
type Entry struct {
	ID        uint64    `json:"id"`
	Time      time.Time `json:"time"`
	Type      Type      `json:"type"`
	GroupId   string    `json:"group_id,omitempty"`   // 会话 ID（群消息 / 群通知）
	UserId    string    `json:"user_id,omitempty"`    // 相关用户 ID
	Nickname  string    `json:"nickname,omitempty"`   // 发送者昵称 / 群名片
	Title     string    `json:"title,omitempty"`      // 通知标题（仅 notice 类型），如「群成员增加」
	Text      string    `json:"text"`                 // 消息内容 / 通知描述
	Media     []Media   `json:"media,omitempty"`      // 消息内的图片 / 文件（按出现顺序）
	MessageId string    `json:"message_id,omitempty"` // 平台消息 ID（媒体地址过期时用于重新拉取消息）

	// Deleted 已删除标记：面板删除单条日志时不移除列表元素（分页游标按
	// 「偏移 = seq - ID」换算，移除会让之后所有日志整体错位），而是把该位置
	// 覆盖成墓碑条目，所有读取接口统一过滤。墓碑随容量淘汰自然清理。
	Deleted bool `json:"deleted,omitempty"`
}

// SessionKey 返回日志所属的会话键，面板用它把消息归类到群聊 / 私聊 / 系统通知：
//   - 群消息与群通知：g:<群 ID>（通知与聊天记录合并展示，更像聊天软件）
//   - 好友消息，以及带用户 ID 但不带群 ID 的通知：f:<用户 ID>
//   - 其余通知：n:system
func (e Entry) SessionKey() string {
	switch e.Type {
	case TypeGroup:
		if e.GroupId != "" {
			return "g:" + e.GroupId
		}
	case TypeFriend:
		if e.UserId != "" {
			return "f:" + e.UserId
		}
	case TypeNotice:
		if e.GroupId != "" {
			return "g:" + e.GroupId
		}
		if e.UserId != "" {
			return "f:" + e.UserId
		}
	}
	return systemSessionKey
}

// Session 面板侧栏的一个会话摘要。
type Session struct {
	Key       string    `json:"key"`                  // 会话键（g:/f:/n:system）
	Kind      string    `json:"kind"`                 // group / friend / system
	GroupId   string    `json:"group_id,omitempty"`   // 群 ID（群聊）
	UserId    string    `json:"user_id,omitempty"`    // 用户 ID（私聊 / 通知）
	Name      string    `json:"name,omitempty"`       // 最近发言人昵称（群名由面板按通讯录补全）
	Title     string    `json:"title,omitempty"`      // 最近一条通知标题
	LastText  string    `json:"last_text"`            // 最近一条消息预览（已截断）
	LastTime  time.Time `json:"last_time"`            // 最近一条消息时间
	LastID    uint64    `json:"last_id"`              // 最近一条消息 ID（排序 / 未读标记）
	LastKind  Type      `json:"last_type"`            // 最近一条日志类型
	LastMedia string    `json:"last_media,omitempty"` // 最近一条消息含有的媒体类型
	Count     int       `json:"count"`                // 当前保留的日志条数
}

// sessionState 单个会话的内存索引。
type sessionState struct {
	kind      string
	groupId   string
	userId    string
	name      string
	title     string
	lastText  string
	lastKind  Type
	lastID    uint64
	lastTime  time.Time
	lastMedia string
	ids       []uint64 // 日志 ID，升序（旧 → 新）
}

// Recorder 消息日志记录器。
//
// 内部用互斥串行化 Add，保证 ID 单调递增；读取（Recent / Page）每次直接从存储
// 加载最新数据，不持有消息副本，保证与存储状态一致（redis 驱动下重启不丢）。
// 会话索引是唯一的例外：它只缓存 ID 与预览字段，不缓存消息正文。
type Recorder struct {
	store storage.Storage
	max   int64
	mu    sync.Mutex
	seq   uint64

	sessions     map[string]*sessionState // 会话索引（首次查询时重建）
	sessionsRead bool
}

// New 创建记录器。store 应为已隔离好命名空间的缓存存储；maxEntries<=0 时取默认值。
func New(store storage.Storage, maxEntries int) *Recorder {
	if maxEntries <= 0 {
		maxEntries = defaultMax
	}
	r := &Recorder{
		store:    store,
		max:      int64(maxEntries),
		sessions: make(map[string]*sessionState),
	}
	r.seq = r.loadSeq()
	return r
}

// loadSeq 从存储恢复自增 ID 计数器（redis 驱动下重启后续接之前的 ID）。
func (r *Recorder) loadSeq() uint64 {
	s, ok := r.store.GetString(context.Background(), seqKey)
	if !ok {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Add 追加一条日志。Time 为零值时取当前时间。
func (r *Recorder) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	e.ID = r.seq
	if e.Time.IsZero() {
		e.Time = time.Now()
	}

	ctx := context.Background()
	r.store.LPush(ctx, entriesKey, e)
	r.store.LTrim(ctx, entriesKey, 0, r.max-1)
	r.store.SetString(ctx, seqKey, strconv.FormatUint(r.seq, 10))

	// 索引在 Add 期间可能尚未重建（首次查询还没发生）：此时先增量写入，
	// 真正重建时会整体按存储重扫，因此不会重复计数。
	r.indexLocked(e)
}

// indexLocked 把一条日志并入会话索引；调用方需持有 r.mu。
// 已删除的墓碑条目不进索引（重建索引时会整体跳过它们）。
func (r *Recorder) indexLocked(e Entry) {
	if e.Deleted {
		return
	}
	key := e.SessionKey()
	st := r.sessions[key]
	if st == nil {
		st = &sessionState{kind: sessionKind(key)}
		// 会话 ID 由键决定：群会话只记群 ID，私聊会话只记用户 ID
		if st.kind == SessionGroup {
			st.groupId = e.GroupId
		} else if st.kind == SessionFriend {
			st.userId = e.UserId
		}
		r.sessions[key] = st
	}
	st.lastID = e.ID
	st.lastTime = e.Time
	st.lastKind = e.Type
	st.lastText = truncateRunes(e.Text, lastTextMaxRunes)
	st.lastMedia = ""
	if len(e.Media) > 0 {
		st.lastMedia = e.Media[0].Kind
	}
	// 通知没有昵称：保留最近一次真人发言的昵称，供群名未知时兜底展示
	if e.Nickname != "" {
		st.name = e.Nickname
	}
	if e.Type == TypeNotice {
		st.title = e.Title
	}
	st.ids = append(st.ids, e.ID)
	r.trimSessionLocked(st)
}

// trimSessionLocked 丢弃已被全局容量淘汰的会话 ID（保持 id 升序）。
// 仅当待丢弃条目超过一定量时才收缩切片，避免每条消息都搬移内存。
func (r *Recorder) trimSessionLocked(st *sessionState) {
	if len(st.ids) == 0 {
		return
	}
	minID := uint64(1)
	if r.seq > uint64(r.max) {
		minID = r.seq - uint64(r.max) + 1
	}
	if st.ids[0] >= minID {
		return
	}
	cut := sort.Search(len(st.ids), func(i int) bool { return st.ids[i] >= minID })
	if cut > 1024 {
		st.ids = append([]uint64(nil), st.ids[cut:]...)
		return
	}
	st.ids = st.ids[cut:]
}

// ensureSessionsLocked 在首次查询会话时从存储重建索引；调用方需持有 r.mu。
// 重建会整体重置索引，因此 Add 期间先写入的增量也会被存储中的同一条日志覆盖，
// 不会重复。
func (r *Recorder) ensureSessionsLocked() {
	if r.sessionsRead {
		return
	}
	r.sessionsRead = true
	r.sessions = make(map[string]*sessionState)

	items, ok := r.store.LRange(context.Background(), entriesKey, 0, r.max-1)
	if !ok {
		return
	}
	entries := decodeEntries(items)
	// 存储列表为新在前：倒序处理即可得到 ID 升序的索引。
	for i := len(entries) - 1; i >= 0; i-- {
		r.indexLocked(entries[i])
	}
}

// Sessions 返回全部会话摘要（按最近一条日志的 ID 倒序）。
func (r *Recorder) Sessions() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureSessionsLocked()

	out := make([]Session, 0, len(r.sessions))
	for key, st := range r.sessions {
		if len(st.ids) == 0 {
			continue
		}
		out = append(out, Session{
			Key:       key,
			Kind:      st.kind,
			GroupId:   st.groupId,
			UserId:    st.userId,
			Name:      st.name,
			Title:     st.title,
			LastText:  st.lastText,
			LastTime:  st.lastTime,
			LastID:    st.lastID,
			LastKind:  st.lastKind,
			LastMedia: st.lastMedia,
			Count:     len(st.ids),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastID == out[j].LastID {
			return out[i].Key < out[j].Key
		}
		return out[i].LastID > out[j].LastID
	})
	return out
}

// Recent 返回最近 limit 条日志（新在前）。limit<=0 时返回全部。
// 已删除的日志不返回，缺口由更旧的日志补齐。
func (r *Recorder) Recent(limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > int(r.max) {
		limit = int(r.max)
	}
	return r.pageLocked(0, limit)
}

// Delete 删除一条日志，返回是否实际删除（ID 不存在或已被删除时返回 false）。
//
// 删除只影响面板的留存日志，不会撤回平台上的原消息。实现上用墓碑覆盖原条目
// 而不是移除元素：分页游标按「偏移 = seq - ID」换算，移除会让之后所有日志的
// 偏移整体错位；墓碑仍占列表槽位，读取时统一过滤，并随容量淘汰自然清理。
func (r *Recorder) Delete(id uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == 0 || id > r.seq {
		return false
	}
	ctx := context.Background()
	offset := int64(r.seq - id)
	// 先读回该位置校验 ID：偏移对不上（存储被外部改写）时宁可不删，也不能误删别条
	item, ok := r.store.LIndex(ctx, entriesKey, offset)
	if !ok {
		return false
	}
	entries := decodeEntries([]any{item})
	if len(entries) == 0 || entries[0].ID != id || entries[0].Deleted {
		return false
	}
	if !r.store.LSet(ctx, entriesKey, offset, tombstoneEntry(id)) {
		return false
	}
	r.invalidateSessionsLocked()
	return true
}

// DeleteSession 删除一个会话（g:/f:/n:system）的全部日志，返回删除条数。
//
// 与 Delete 一样用墓碑覆盖，保持 ID ↔ 列表偏移的映射不变；会话被清空后从
// 面板侧栏消失，之后该会话收到的新消息仍会正常记录。
func (r *Recorder) DeleteSession(session string) int {
	if session == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureSessionsLocked()
	st := r.sessions[session]
	if st == nil || len(st.ids) == 0 {
		return 0
	}
	// 索引里的 ID 都落在容量窗口内，偏移可直接由 seq 换算；成百上千条逐条读回
	// 校验会产生同样数量的存储往返，这里只在越界时跳过（LSet 自己也会拒绝）。
	ctx := context.Background()
	ids := append([]uint64(nil), st.ids...)
	removed := 0
	for _, id := range ids {
		if id == 0 || id > r.seq {
			continue
		}
		if r.store.LSet(ctx, entriesKey, int64(r.seq-id), tombstoneEntry(id)) {
			removed++
		}
	}
	if removed == 0 {
		return 0
	}
	r.invalidateSessionsLocked()
	return removed
}

// invalidateSessionsLocked 标记会话索引待重建：条数 / 最新摘要都可能因删除
// 而变化，下次查询时按存储重扫（墓碑被跳过）。删除是低频的人工操作，
// 重建代价可以接受。
func (r *Recorder) invalidateSessionsLocked() {
	r.sessionsRead = false
}

// tombstoneEntry 已删除日志的占位条目：只保留 ID，其余字段留空。
func tombstoneEntry(id uint64) Entry {
	return Entry{ID: id, Deleted: true}
}

// Get 按日志 ID 取回单条日志，供面板按需重新解析媒体地址（媒体 URL 过期时
// 需要拿到原消息 ID / 文件 ID）。日志已被容量淘汰、已被删除或 ID 不存在时
// 返回 false。
func (r *Recorder) Get(id uint64) (Entry, bool) {
	if id == 0 {
		return Entry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id > r.seq {
		return Entry{}, false
	}
	offset := int64(r.seq - id)
	items, ok := r.store.LRange(context.Background(), entriesKey, offset, offset)
	if !ok || len(items) == 0 {
		return Entry{}, false
	}
	entries := decodeEntries(items)
	if len(entries) == 0 || entries[0].ID != id || entries[0].Deleted {
		// 列表偏移与 seq 不一致（存储被裁剪 / 外部写入）时不返回错误数据
		return Entry{}, false
	}
	return entries[0], true
}

// Page 返回一页日志（新在前），供面板滚动分页使用：beforeID>0 时仅返回
// ID 小于它的更旧日志，否则从最新开始。limit<=0 时取容量上限。
// 已删除的日志不返回，缺口由更旧的日志补齐。
//
// 列表按新在前排列且 ID 连续自增，索引 i 处的条目 ID 为 seq-i，
// 因此可直接把游标换算为列表偏移，无需全量扫描。
func (r *Recorder) Page(limit int, beforeID uint64) []Entry {
	// 读取期间持锁：Add 会改变列表偏移，否则并发写入会让游标错位。
	r.mu.Lock()
	defer r.mu.Unlock()

	if limit <= 0 || limit > int(r.max) {
		limit = int(r.max)
	}
	var start int64
	if beforeID > 0 && beforeID <= r.seq {
		start = int64(r.seq - beforeID + 1)
	}
	return r.pageLocked(start, limit)
}

// pageLocked 从列表偏移 start 起按「新 → 旧」扫描，跳过已删除的墓碑条目，
// 收集最多 limit 条有效日志；调用方需持有 r.mu（偏移由 seq 换算）。
func (r *Recorder) pageLocked(start int64, limit int) []Entry {
	if start < 0 {
		start = 0
	}
	out := make([]Entry, 0, limit)
	ctx := context.Background()
	offset := start
	for len(out) < limit {
		batch := int64(limit - len(out))
		if batch < minPageBatch {
			batch = minPageBatch
		}
		items, ok := r.store.LRange(ctx, entriesKey, offset, offset+batch-1)
		if !ok || len(items) == 0 {
			break
		}
		for _, e := range decodeEntries(items) {
			if e.Deleted {
				continue
			}
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
		if int64(len(items)) < batch {
			break // 已读到列表末尾
		}
		offset += int64(len(items))
	}
	return out
}

// PageSession 返回指定会话的一页日志（新在前）：
//   - beforeID>0：仅返回 ID 小于它的更旧日志（向上滚动加载）；
//   - afterID>0：仅返回 ID 大于它的新日志（轮询增量，通常只有几条）。
//
// limit<=0 时取容量上限；会话不存在时返回 nil。
func (r *Recorder) PageSession(session string, limit int, beforeID, afterID uint64) []Entry {
	if limit <= 0 || limit > int(r.max) {
		limit = int(r.max)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureSessionsLocked()
	seq := r.seq
	st := r.sessions[session]
	var ids []uint64
	if st != nil && len(st.ids) > 0 {
		for i := len(st.ids) - 1; i >= 0 && len(ids) < limit; i-- {
			id := st.ids[i]
			if afterID > 0 {
				if id <= afterID {
					break
				}
				ids = append(ids, id)
				continue
			}
			if beforeID > 0 && id >= beforeID {
				continue
			}
			ids = append(ids, id)
		}
	}

	// 取日志时保持持锁：偏移量由 seq 换算，Add 并发写入会让偏移整体错位。
	return r.fetchByIDs(seq, ids)
}

// fetchByIDs 按 ID 倒序（新在前）取回日志：把连续的 ID 合并成一次 LRange，
// 避免每条日志一次存储往返。调用方已持有索引快照与 seq。
func (r *Recorder) fetchByIDs(seq uint64, ids []uint64) []Entry {
	if len(ids) == 0 {
		return nil
	}
	out := make([]Entry, 0, len(ids))
	ctx := context.Background()
	for i := 0; i < len(ids); {
		id := ids[i]
		if id > seq {
			i++
			continue
		}
		start := seq - id
		j := i + 1
		for j < len(ids) && ids[j] <= seq && ids[j-1]-ids[j] == 1 {
			j++
		}
		end := seq - ids[j-1]
		items, ok := r.store.LRange(ctx, entriesKey, int64(start), int64(end))
		if ok {
			// 索引理论上不含墓碑，这里仍按存储实际内容过滤一次（索引重建前
			// 删掉的条目可能还留在快照里）
			for _, e := range decodeEntries(items) {
				if e.Deleted {
					continue
				}
				out = append(out, e)
			}
		}
		i = j
	}
	return out
}

// sessionKind 由会话键前缀推断会话类型。
func sessionKind(key string) string {
	switch {
	case len(key) > 2 && key[:2] == "g:":
		return SessionGroup
	case len(key) > 2 && key[:2] == "f:":
		return SessionFriend
	default:
		return SessionSystem
	}
}

// truncateRunes 按字符截断文本（不破坏多字节字符），超长时补省略号。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// decodeEntries 把存储层按 any 解码的列表元素（map）经 JSON 往返还原为 Entry。
func decodeEntries(items []any) []Entry {
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}
