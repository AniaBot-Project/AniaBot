package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/workstate"
)

const (
	// asyncSubagentJanitorInterval 会话后台工作表的清理周期：只清理超过
	// subagent.finished_ttl_min 的已完成条目，运行中条目不受影响。
	asyncSubagentJanitorInterval = time.Minute
	// followupQuestionMaxRunes 单次追问内容的长度上限：追问会拼进子代理的系统
	// 任务里，超长内容（如误贴整段报告）会把上下文撑爆，按 rune 截断。
	followupQuestionMaxRunes = 8000
)

// asyncSubagentEntry 一个会话后台工作条目：运行中的异步子代理 / 工作流 /
// 团队运行 / 团队成员，以及完成后在 TTL 内保留、可供 subagent_followup 追问的
// 已完成条目。
//
// 运行中条目靠 cancel + steer 管理；已完成条目 cancel/steer 置 nil，仅保留
// 原任务、结果、错误与父子关系，供追问时重建上下文。
type asyncSubagentEntry struct {
	id        string
	name      string // 可选展示名（subagent_run 的 name 参数）；空则只用 ID
	label     string // 展示名（团队成员名等，不参与寻址）；空则回退 name/ID
	kind      string // taskKindSubagent / Handoff / Workflow / TeamRun / TeamMember / Followup
	cancel    context.CancelFunc
	task      string
	startTime time.Time
	// steer 运行中追加指令通道（subagent_send）：orchestrator 在每次 LLM 请求前
	// 排空它，把内容作为 [追加指令] 用户消息注入子代理上下文。
	steer chan string
	// broadcast 团队运行条目的指令广播：把 subagent_send 的消息分发给全部成员
	// 的 steer 通道，返回实际送达数量（成员通道满时跳过该成员）。
	broadcast func(string) int
	// prompt 团队成员/自定义角色提示词：追问时沿用同一角色，避免退化成普通子代理。
	prompt string
	// parentID 团队成员的团队运行 ID，或追问条目的原任务 ID（写入任务表 ParentID）。
	parentID string
	// rootID 追问链的根任务 ID：追问次数上限按根任务统计（普通条目为空，等同自身）。
	rootID string

	// ---- 完成态（TTL 内保留） ----
	finished   bool
	finishedAt time.Time
	result     string // 完整结果文本（追问时按 ResultInlineRunes 治理后拼入任务）
	err        string // 失败/超时原因（成功为空）
}

// asyncEntryKindLabel 后台工作类型的中文展示（工具列表、/tasks 与通知共用）。
func asyncEntryKindLabel(kind string) string {
	switch kind {
	case taskKindSubagent:
		return "子代理"
	case taskKindHandoff:
		return "交接"
	case taskKindWorkflow:
		return "工作流"
	case taskKindTeamRun:
		return "团队运行"
	case taskKindTeamMember:
		return "团队成员"
	case taskKindFollowup:
		return "追问"
	default:
		return kind
	}
}

// display 条目的可读名称：label > name > ID。
func (e asyncSubagentEntry) display() string {
	if e.label != "" {
		return e.label
	}
	if e.name != "" {
		return e.name
	}
	return e.id
}

// asyncSubagentGroup 单个会话的后台工作集合（支持多子代理并发）。已完成条目在
// TTL 内保留，因此组不再等同于「运行中集合」；查询运行中状态一律用 running()。
type asyncSubagentGroup struct {
	mu      sync.Mutex
	entries []asyncSubagentEntry
	// followups 追问链根任务 ID → 已发起的追问次数（max_followups_per_task 用）。
	// 根条目被清理、且没有任何子条目引用该根时，计数一并删除。
	followups map[string]int
}

func (g *asyncSubagentGroup) add(id string, cancel context.CancelFunc, task string) {
	g.addEntry(asyncSubagentEntry{id: id, cancel: cancel, task: task})
}

// addEntry 加入一个条目（时间戳在此统一填充）。
func (g *asyncSubagentGroup) addEntry(e asyncSubagentEntry) {
	g.addEntries([]asyncSubagentEntry{e})
}

// addEntries 原子地加入一组条目：团队运行与全部成员一次性登记，避免面板/工具
// 在中间状态只看到一半成员。
func (g *asyncSubagentGroup) addEntries(entries []asyncSubagentEntry) {
	if len(entries) == 0 {
		return
	}
	now := time.Now()
	g.mu.Lock()
	for i := range entries {
		if entries[i].startTime.IsZero() {
			entries[i].startTime = now
		}
		if entries[i].kind == "" {
			entries[i].kind = taskKindSubagent
		}
		g.entries = append(g.entries, entries[i])
	}
	g.mu.Unlock()
}

// nameTaken 判断名字是否已被本会话运行中的后台工作占用（空名不占位）。
// 已完成条目不占位：任务结束后名字应可复用。
func (g *asyncSubagentGroup) nameTaken(name string) bool {
	if name == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		if !e.finished && e.name == name {
			return true
		}
	}
	return false
}

// find 按 ID 或名字查找运行中的条目（已完成条目不参与 send/cancel 路径）。
func (g *asyncSubagentGroup) find(idOrName string) (asyncSubagentEntry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.findLocked(idOrName)
}

// findLocked find 的实现体（调用方持锁）。团队成员的展示名（label）不保证
// 会话内唯一：仅当只有一个运行中条目匹配时才接受，避免把指令发给错误的成员。
func (g *asyncSubagentGroup) findLocked(idOrName string) (asyncSubagentEntry, bool) {
	for _, e := range g.entries {
		if e.finished {
			continue
		}
		if e.id == idOrName || (e.name != "" && e.name == idOrName) {
			return e, true
		}
	}
	var matched asyncSubagentEntry
	count := 0
	for _, e := range g.entries {
		if !e.finished && e.label != "" && e.label == idOrName {
			matched = e
			count++
		}
	}
	if count == 1 {
		return matched, true
	}
	return asyncSubagentEntry{}, false
}

// findAny 按 ID / 名字 / 展示名查找条目：ID 精确优先；同名时优先运行中条目，
// 否则取最近完成的条目。追问与会话后台工作表展示使用。
func (g *asyncSubagentGroup) findAny(idOrName string) (asyncSubagentEntry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		if e.id == idOrName {
			return e, true
		}
	}
	for _, e := range g.entries {
		if !e.finished && (e.name == idOrName || e.label == idOrName) {
			return e, true
		}
	}
	var found asyncSubagentEntry
	ok := false
	for _, e := range g.entries {
		if !e.finished || (e.name != idOrName && e.label != idOrName) {
			continue
		}
		if !ok || e.finishedAt.After(found.finishedAt) {
			found = e
			ok = true
		}
	}
	return found, ok
}

// send 向运行中的子代理追加一条指令；返回是否找到目标。查找与写入在同一临界区
// 内完成：成员恰好在这一刻完成时，要么送达（它还能排空），要么返回 false 让调用
// 方走 followup，不会误报成功。
func (g *asyncSubagentGroup) send(idOrName, msg string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.findLocked(idOrName)
	if !ok {
		return false
	}
	if e.broadcast != nil {
		return e.broadcast(msg) > 0
	}
	if e.steer == nil {
		return false
	}
	select {
	case e.steer <- msg:
		return true
	default:
		// 缓冲满：丢弃新指令而不是阻塞工具调用；调用方会得到"队列满"提示。
		return false
	}
}

// markFinished 把运行中的条目转为完成态并保留结果，返回是否找到条目。
// cancel/steer/broadcast 一并置空：完成条目不可再被取消或注入。
func (g *asyncSubagentGroup) markFinished(id, result, errText string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.entries {
		if g.entries[i].id != id {
			continue
		}
		e := &g.entries[i]
		if e.finished {
			return true
		}
		e.finished = true
		e.finishedAt = time.Now()
		e.result = result
		e.err = errText
		e.cancel = nil
		e.steer = nil
		e.broadcast = nil
		return true
	}
	return false
}

// removeIfEmpty 移除指定 ID 的条目，返回 (removed, empty)：removed 表示该 ID
// 确实在组内（移除与判空在同一临界区内完成，避免「判空后又加入新条目」的竞态；
// ID 不存在时不报告 empty，杜绝误删他人刚创建、尚未入条目的新组）。
func (g *asyncSubagentGroup) removeIfEmpty(id string) (removed, empty bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	filtered := make([]asyncSubagentEntry, 0, len(g.entries))
	for _, e := range g.entries {
		if e.id == id {
			removed = true
			continue
		}
		filtered = append(filtered, e)
	}
	g.entries = filtered
	g.pruneFollowupRootsLocked()
	return removed, len(g.entries) == 0
}

func (g *asyncSubagentGroup) cancelAll() {
	g.mu.Lock()
	for _, e := range g.entries {
		if e.cancel != nil {
			e.cancel()
		}
	}
	g.entries = nil
	g.followups = nil
	g.mu.Unlock()
}

func (g *asyncSubagentGroup) cancelOne(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range g.entries {
		if e.id == id && !e.finished {
			if e.cancel != nil {
				e.cancel()
			}
			return true
		}
	}
	return false
}

// snapshot 返回当前全部条目的快照（副本，含已完成条目）。
func (g *asyncSubagentGroup) snapshot() []asyncSubagentEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := make([]asyncSubagentEntry, len(g.entries))
	copy(cp, g.entries)
	return cp
}

// running 只返回运行中的条目（/status、并发上限、取消计数使用）。
func (g *asyncSubagentGroup) running() []asyncSubagentEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]asyncSubagentEntry, 0, len(g.entries))
	for _, e := range g.entries {
		if !e.finished {
			out = append(out, e)
		}
	}
	return out
}

// list 返回会话后台工作条目：includeFinished=false 只返回运行中；true 时附带
// TTL 内尚未过期的已完成条目（ttl<=0 表示不保留完成条目）。已完成条目按完成
// 时间倒序追加在运行中条目之后，便于模型优先看到可追问的最近结果。
func (g *asyncSubagentGroup) list(includeFinished bool, now time.Time, ttl time.Duration) []asyncSubagentEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	running := make([]asyncSubagentEntry, 0, len(g.entries))
	var finished []asyncSubagentEntry
	for _, e := range g.entries {
		if !e.finished {
			running = append(running, e)
			continue
		}
		if !includeFinished || ttl <= 0 || now.Sub(e.finishedAt) >= ttl {
			continue
		}
		finished = append(finished, e)
	}
	sortAsyncEntriesByFinished(finished)
	return append(running, finished...)
}

// sortAsyncEntriesByFinished 完成条目按完成时间倒序（最近完成的先展示）。
func sortAsyncEntriesByFinished(entries []asyncSubagentEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].finishedAt.After(entries[j].finishedAt)
	})
}

// pruneFinished 清理超过 TTL 的已完成条目，返回清理数量与组是否为空。
// ttl<=0 时清理全部已完成条目。
func (g *asyncSubagentGroup) pruneFinished(now time.Time, ttl time.Duration) (removed int, empty bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	filtered := make([]asyncSubagentEntry, 0, len(g.entries))
	for _, e := range g.entries {
		if e.finished && (ttl <= 0 || now.Sub(e.finishedAt) >= ttl) {
			removed++
			continue
		}
		filtered = append(filtered, e)
	}
	g.entries = filtered
	g.pruneFollowupRootsLocked()
	return removed, len(g.entries) == 0
}

// reserveFollowup 为一次追问占用配额：max<=0 表示不限制。根任务在清理后重新
// 计数会从 0 开始（条目本身也不在了，符合「过期后请重新委派」的语义）。
func (g *asyncSubagentGroup) reserveFollowup(rootID string, max int) bool {
	if rootID == "" {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if max > 0 && g.followups[rootID] >= max {
		return false
	}
	if g.followups == nil {
		g.followups = make(map[string]int, 1)
	}
	g.followups[rootID]++
	return true
}

// releaseFollowup 追问启动失败时归还配额，避免失败调用把次数吃掉。
func (g *asyncSubagentGroup) releaseFollowup(rootID string) {
	if rootID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.followups[rootID] > 0 {
		g.followups[rootID]--
	}
	if g.followups[rootID] == 0 {
		delete(g.followups, rootID)
	}
}

// followupCount 返回根任务已发起的追问次数（测试与提示文案用）。
func (g *asyncSubagentGroup) followupCount(rootID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.followups[rootID]
}

// pruneFollowupRootsLocked 清理不再被任何条目引用的追问根计数（调用方持锁）。
func (g *asyncSubagentGroup) pruneFollowupRootsLocked() {
	if len(g.followups) == 0 {
		return
	}
	roots := make(map[string]struct{}, len(g.entries))
	for _, e := range g.entries {
		root := e.rootID
		if root == "" {
			root = e.id
		}
		roots[root] = struct{}{}
	}
	for root := range g.followups {
		if _, ok := roots[root]; !ok {
			delete(g.followups, root)
		}
	}
}

// getAsyncGroup 获取或创建会话的后台工作组
func (p *AIChatPlugin) getAsyncGroup(id message.QID, isGroup bool) *asyncSubagentGroup {
	key := sessionKey(id, isGroup)
	v, _ := p.asyncSubagents.LoadOrStore(key, &asyncSubagentGroup{})
	return v.(*asyncSubagentGroup)
}

// cancelAsyncSubagents 取消指定会话全部运行中的后台工作并清理整个工作表（含
// 已完成条目）：开新对话 / 面板清空上下文都不允许旧任务的结果或追问窗口跨越。
func (p *AIChatPlugin) cancelAsyncSubagents(id message.QID, isGroup bool) {
	key := sessionKey(id, isGroup)
	if v, ok := p.asyncSubagents.LoadAndDelete(key); ok {
		v.(*asyncSubagentGroup).cancelAll()
	}
}

// lookupAsyncGroup 读取会话的后台工作组（不做创建）：查询/取消路径使用，
// 避免用 getAsyncGroup 重建空组造成状态表随会话累积泄漏。
func (p *AIChatPlugin) lookupAsyncGroup(id message.QID, isGroup bool) *asyncSubagentGroup {
	v, ok := p.asyncSubagents.Load(sessionKey(id, isGroup))
	if !ok {
		return nil
	}
	g, _ := v.(*asyncSubagentGroup)
	return g
}

// finishedSubagentTTL 完成条目的保留时长（subagent.finished_ttl_min）：0 表示
// 不保留完成条目（子代理结束即从工作表移除，行为与旧版一致）。
func (p *AIChatPlugin) finishedSubagentTTL() time.Duration {
	minutes := min(max(p.cfg.Subagent.FinishedTTLMin, 0), 365*24*60)
	return time.Duration(minutes) * time.Minute
}

// listRunningSubagents 返回当前会话运行中的后台工作（供并发上限、/status 与
// /tasks 使用；已完成条目不算运行中）。
func (p *AIChatPlugin) listRunningSubagents(id message.QID, isGroup bool) []asyncSubagentEntry {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return nil
	}
	return g.running()
}

// runningSubagentCount 统计会话中运行中的普通后台子代理（不含团队运行/成员）。
// 团队运行有独立的成员上限与全局池；若把成员计入 subagent.max_concurrent_per_session，
// 一个 5 人团队就会永久占满默认 4 个会话名额，导致后续 subagent_run 全部被拒。
func (p *AIChatPlugin) runningSubagentCount(id message.QID, isGroup bool) int {
	count := 0
	for _, e := range p.listRunningSubagents(id, isGroup) {
		if e.kind == taskKindTeamRun || e.kind == taskKindTeamMember {
			continue
		}
		count++
	}
	return count
}

// listSessionWork 返回会话后台工作快照：运行中条目始终返回，includeFinished 时
// 附带 TTL 内可追问的已完成条目（按完成时间倒序）。供 subagent_list 使用。
func (p *AIChatPlugin) listSessionWork(id message.QID, isGroup bool, includeFinished bool) []asyncSubagentEntry {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return nil
	}
	return g.list(includeFinished, time.Now(), p.finishedSubagentTTL())
}

// lookupSessionWorkEntry 按 ID/名字查找会话后台工作条目（含已完成）；已过 TTL
// 的完成条目视为不存在并顺手摘除，避免追问过期结果。
func (p *AIChatPlugin) lookupSessionWorkEntry(id message.QID, isGroup bool, idOrName string) (asyncSubagentEntry, bool) {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return asyncSubagentEntry{}, false
	}
	e, ok := g.findAny(idOrName)
	if !ok {
		return asyncSubagentEntry{}, false
	}
	if e.finished {
		ttl := p.finishedSubagentTTL()
		if ttl <= 0 || time.Since(e.finishedAt) >= ttl {
			p.removeAsyncSubagent(id, isGroup, e.id)
			return asyncSubagentEntry{}, false
		}
	}
	return e, true
}

// cancelSubagentByID 按 ID 或展示名取消单个运行中的后台工作（供 subagent_cancel
// 工具与 /stop <ID> 使用）。团队运行 ID 取消整队，成员 ID 只取消该成员。
func (p *AIChatPlugin) cancelSubagentByID(id message.QID, isGroup bool, idOrName string) bool {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return false
	}
	e, ok := g.find(idOrName)
	if !ok {
		return false
	}
	return g.cancelOne(e.id)
}

// sendSubagentMessage 向运行中的子代理追加一条指令（steering），返回是否送达。
// 指令由子代理的 aichat 编排层在下次 LLM 请求前注入为 user 消息。
func (p *AIChatPlugin) sendSubagentMessage(id message.QID, isGroup bool, idOrName, msg string) bool {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return false
	}
	return g.send(idOrName, msg)
}

// retireRunningAsyncSubagent 兜底清理：条目仍在运行态时移除（已完成的保留）。
// 后台 goroutine 异常退出（panic）时防止残留「永远运行中」的条目。
func (p *AIChatPlugin) retireRunningAsyncSubagent(id message.QID, isGroup bool, entryID string) {
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return
	}
	e, ok := g.findAny(entryID)
	if !ok || e.finished {
		return
	}
	p.removeAsyncSubagent(id, isGroup, entryID)
}

// removeAsyncSubagent 子代理结束后从会话组中移除；组已空（或已被 /stop 的
// cancelAsyncSubagents 摘除）时清理 sync.Map 条目，避免空组滞留。
func (p *AIChatPlugin) removeAsyncSubagent(id message.QID, isGroup bool, subagentID string) {
	key := sessionKey(id, isGroup)
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return
	}
	if removed, empty := g.removeIfEmpty(subagentID); removed && empty {
		p.asyncSubagents.CompareAndDelete(key, g)
	}
}

// finishAsyncSubagent 把一次正常结束（完成/失败/超时）的后台工作转为完成态并
// 在 subagent.finished_ttl_min 内保留；TTL 为 0 时按旧行为直接移除。
// 被 /stop / #新对话 取消的条目由调用方直接 removeAsyncSubagent，不作为追问目标。
func (p *AIChatPlugin) finishAsyncSubagent(id message.QID, isGroup bool, entryID, result string, runErr error) {
	if p.finishedSubagentTTL() <= 0 {
		p.removeAsyncSubagent(id, isGroup, entryID)
		return
	}
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	g.markFinished(entryID, result, errText)
}

// pruneFinishedSubagents 清理全部会话中超过 TTL 的完成条目（常驻 janitor 调用）。
func (p *AIChatPlugin) pruneFinishedSubagents(ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	now := time.Now()
	p.asyncSubagents.Range(func(key, value any) bool {
		g, ok := value.(*asyncSubagentGroup)
		if !ok || g == nil {
			return true
		}
		if _, empty := g.pruneFinished(now, ttl); empty {
			p.asyncSubagents.CompareAndDelete(key, g)
		}
		return true
	})
}

// startAsyncSubagentJanitor 启动完成条目清理器（与插件同生命周期，随 runCtx
// 退出；同 startChatJanitor 惯例）。
func (p *AIChatPlugin) startAsyncSubagentJanitor() {
	ttl := p.finishedSubagentTTL()
	if ttl <= 0 || p.runCtx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(asyncSubagentJanitorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.runCtx.Done():
				return
			case <-ticker.C:
				p.pruneFinishedSubagents(ttl)
			}
		}
	}()
}

// launchSubagentOptions 异步子代理启动参数。
type launchSubagentOptions struct {
	// Name 可选展示名：1-32 位小写字母/数字/连字符、以字母开头，会话内运行中唯一。
	Name string
	// Label 展示名（团队成员名等）；不参与寻址，仅用于列表/通知展示。
	Label string
	// Kind 任务类型：taskKindSubagent / taskKindHandoff / taskKindWorkflow /
	// taskKindFollowup / taskKindTeamMember。
	Kind string
	// Prompt 系统提示词覆盖（团队成员角色 / 追问沿用原角色）；空用默认子代理提示词。
	Prompt string
	// ParentID 父任务 ID：团队成员的团队运行 ID，或追问的原任务 ID。
	ParentID string
	// RootID 追问链根任务 ID（追问次数按根统计）；空表示自身为根。
	RootID string
	// TimeoutSec 单次超时覆盖（秒，0=默认）。
	TimeoutSec int
	// Callbacks 父会话工具回调（子代理显式发送消息/图片等能力沿用父会话）。
	Callbacks llmtool.CallBackFuncs
}

// validSubagentName 校验子代理展示名：1-32 位、小写字母开头，只含小写字母/数字/连字符。
func validSubagentName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && ((r >= '0' && r <= '9') || r == '-'):
		default:
			return false
		}
	}
	return true
}

// subagentWorkLabel 构造面板「重启等待」里的子代理描述：优先用名字，
// 否则用任务摘要。
func subagentWorkLabel(name, task string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return truncateTaskTitle(task)
}

// launchAsyncSubagent 在后台 goroutine 中启动子代理，立即返回占位文本。
//
// ctx 为主请求上下文：仅用于检查父回合剩余预算。
// 子代理的实际执行使用独立的 context.WithCancel(context.Background()) ——
// 无 deadline，让 runSubagent 内部的 resolveSubagentTimeout + WithTimeout 自行管理
// 超时；/stop 通过 asyncSubagentGroup 注册的 cancel 取消。
func (p *AIChatPlugin) launchAsyncSubagent(
	ctx context.Context,
	b bot.Bot,
	id message.QID,
	isGroup bool,
	task string,
	opts launchSubagentOptions,
) string {
	_, text, _ := p.startAsyncSubagent(ctx, b, id, isGroup, task, opts)
	return text
}

// startAsyncSubagent 启动异步子代理并返回 (任务 ID, 提示文本, 是否成功)。
// 失败时不占用并发/配额，调用方（如追问）可据此归还 followup 计数。
func (p *AIChatPlugin) startAsyncSubagent(
	ctx context.Context,
	b bot.Bot,
	id message.QID,
	isGroup bool,
	task string,
	opts launchSubagentOptions,
) (string, string, bool) {
	if !p.cfg.Subagent.Enable {
		return "", "子代理功能未启用（plugin.ai_chat_bot.subagent.enable=false）", false
	}
	if opts.Kind == "" {
		opts.Kind = taskKindSubagent
	}
	// 展示名校验与会话内唯一性
	if opts.Name != "" {
		if !validSubagentName(opts.Name) {
			return "", fmt.Sprintf("子代理名字 %q 不合法：需 1-32 位、小写字母开头，只含小写字母/数字/连字符", opts.Name), false
		}
		if p.getAsyncGroup(id, isGroup).nameTaken(opts.Name) {
			return "", fmt.Sprintf("名字 %q 已被当前会话运行中的子代理占用，请换一个名字或用 subagent_list 查看", opts.Name), false
		}
	}
	// 检查父请求剩余预算（父回合有硬截止；不足时 resolve 会缩短而非拒绝）
	if _, err := resolveSubagentTimeout(p.subagentTimeout(), opts.TimeoutSec, ctx); err != nil {
		return "", fmt.Sprintf("无法启动子代理: %v", err), false
	}
	// 每会话并发上限：超出直接拒绝（不静默排队，避免 AI 反复重试堆积）。
	// 团队运行不走这里：成员并发由 teamMaxMembers 与全局池控制。
	if maxPer := p.cfg.Subagent.MaxConcurrentPerSession; maxPer > 0 && p.runningSubagentCount(id, isGroup) >= maxPer {
		return "", fmt.Sprintf("当前会话后台子代理已达上限（%d 个），请等待现有任务完成，或用 subagent_list 查看、subagent_cancel 取消", maxPer), false
	}
	// 全局并发池：与对话回合的 rate_limit 分开，后台任务不挤占正常对话
	if p.subagentSem != nil {
		select {
		case p.subagentSem <- struct{}{}:
		default:
			return "", "后台子代理并发已满，请稍后再试（或等待现有任务完成）", false
		}
	}

	launchID := newTaskID()
	rootID := opts.RootID
	if rootID == "" {
		rootID = launchID
	}
	subTask := p.beginSubagentTask(launchID, id, isGroup, task, opts.Kind, opts.ParentID)
	// steering 通道：subagent_send 写入，子代理每次 LLM 请求前排空注入
	steer := make(chan string, 8)

	// 无 deadline 的 cancel context：runSubagent 内部自行通过 resolveSubagentTimeout
	// 创建带 timeout 的子 context，外部 cancel（/stop）不影响超时计算，且
	// asyncCtx.Err() == nil 让 runSubagent 能正确区分「子代理自身超时」和「外部取消」
	asyncCtx, asyncCancel := context.WithCancel(context.Background())
	// 关联插件生命周期：卸载/关停时取消在跑的后台子代理
	var stopShutdown func() bool
	if p.runCtx != nil {
		stopShutdown = context.AfterFunc(p.runCtx, asyncCancel)
	}

	p.getAsyncGroup(id, isGroup).addEntry(asyncSubagentEntry{
		id:       launchID,
		name:     opts.Name,
		label:    opts.Label,
		kind:     opts.Kind,
		task:     task,
		steer:    steer,
		prompt:   opts.Prompt,
		parentID: opts.ParentID,
		rootID:   rootID,
		cancel: func() {
			if stopShutdown != nil {
				stopShutdown()
			}
			asyncCancel()
		},
	})

	b.Go("subagent:"+sessionKey(id, isGroup)+":"+launchID, func() {
		if p.subagentSem != nil {
			defer func() { <-p.subagentSem }()
		}
		// 登记为进行中的后台任务：重启前等待子代理执行完成（受重启等待超时约束）
		endWork := workstate.Begin(workstate.KindSubagent, "子代理 "+subagentWorkLabel(opts.Name, task))
		defer endWork()
		// 异常退出兜底：onSubagentComplete 未及执行时移除运行中条目（正常结束的
		// 完成条目已转 finished，不会被这里误删）
		defer p.retireRunningAsyncSubagent(id, isGroup, launchID)
		// 执行步骤观测：子代理的工具调用随任务记录落库，面板「后台任务」页可展开查看。
		// 执行中按 3 秒节流增量回写（面板轮询即可看到进度），结束时再写终态全量。
		trace := newSubagentTrace()
		trace.progress = p.trackProgress(subTask)
		started := time.Now()
		result, usage, runErr := p.runSubagentWithOptions(asyncCtx, b, id, isGroup, task,
			subagentRunOptions{timeoutSec: opts.TimeoutSec, steer: steer, prompt: opts.Prompt, trace: trace}, opts.Callbacks)
		p.finishSubagentTaskMeta(subTask, result, usage, runErr, subagentTraceMeta(usage, time.Since(started), trace))
		// 消耗计入会话与全局配额（发起方已做前置检查，这里只累加不重复拒绝）
		p.quotaManager.Add(sessionKey(id, isGroup), usage)
		// 异步子代理完成时主请求通常已结束，其消耗暂存到会话级累计器，
		// 由该会话下一次 finishQuery 并入 Query 日志（/stop 取消的也照常计入）
		p.addExtraUsage(sessionKey(id, isGroup), usage)
		p.onSubagentComplete(b, id, isGroup, launchID, opts.Name, task, result, runErr)
	})

	display := launchID
	if opts.Name != "" {
		display = opts.Name + "（ID: " + launchID + "）"
	}
	return launchID, fmt.Sprintf("✅ 子代理已启动（%s），正在后台执行任务", display), true
}

// onSubagentComplete 子代理完成后的回调（在子代理 goroutine 中执行）。
//
// 1. 完成条目在 TTL 内保留（供 subagent_followup 追问），取消的条目直接移除
// 2. 格式化结果文本（第一行是机器可读的状态/任务 ID/名称，便于主 AI 判断）
// 3. 构造合成消息并入队 pending 队列
// 4. 若会话空闲则自动触发 AI 处理
func (p *AIChatPlugin) onSubagentComplete(b bot.Bot, id message.QID, isGroup bool, launchID, name, task, result string, err error) {
	var text string
	nameLine := ""
	if name != "" {
		nameLine = "\n名称: " + name
	}
	// 追问条目带父任务 ID：主 AI 收到结果时能知道它是基于哪条任务继续的
	parentLine := ""
	if e, ok := p.lookupSessionWorkEntry(id, isGroup, launchID); ok && e.parentID != "" {
		parentLine = "\n父任务ID: " + e.parentID
	}
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "超时"):
			// 超时同样保留完成条目：上一轮可能已有部分产出，TTL 内仍可追问
			p.finishAsyncSubagent(id, isGroup, launchID, result, err)
			text = fmt.Sprintf("【子代理执行结束】状态=timeout\n任务ID: %s%s%s\n任务: %s\n错误: %s", launchID, nameLine, parentLine, truncateTaskTitle(task), err.Error())
		case errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "canceled"):
			p.removeAsyncSubagent(id, isGroup, launchID)
			p.Logger.Info("异步子代理已被取消", "id", id, "is_group", isGroup, "launch_id", launchID, "task", task)
			return // 被 /stop 取消，不注入结果
		default:
			p.finishAsyncSubagent(id, isGroup, launchID, result, err)
			text = fmt.Sprintf("【子代理执行结束】状态=failed\n任务ID: %s%s%s\n任务: %s\n错误: %s", launchID, nameLine, parentLine, truncateTaskTitle(task), err.Error())
		}
	} else {
		// 完成条目保留完整结果：追问拼上下文时再按 ResultInlineRunes 治理
		p.finishAsyncSubagent(id, isGroup, launchID, result, nil)
		// 结果长度治理：超长结论落盘为会话私有文件，合成消息只带摘要 + handle，
		// 主 AI 用 read_tool_output 按需读取（避免超长子代理结果撑爆上下文；
		// 旧实现"结果不截断"会把数万字直接塞进 user 消息）
		limited := p.toolResultLimiter.LimitAt(sessionKey(id, isGroup), "subagent_result", result, p.cfg.Subagent.ResultInlineRunes, false)
		text = fmt.Sprintf("【子代理执行完成】状态=completed\n任务ID: %s%s%s\n任务: %s\n\n%s", launchID, nameLine, parentLine, truncateTaskTitle(task), limited)
	}

	// 构造合成消息
	syntheticMsg := message.Message{
		Sender: message.MessageSender{
			Nickname: "子代理",
			UserId:   message.FromUint64(0),
		},
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": text}},
		},
	}

	// 入队
	first, ok := p.enqueuePending(id, isGroup, syntheticMsg)
	if !ok {
		p.Logger.Warn("异步子代理结果入队失败（队列已满）", "id", id, "is_group", isGroup, "launch_id", launchID)
		return
	}

	if first {
		p.Logger.Info("异步子代理结果已入队（队列首条）", "id", id, "is_group", isGroup, "launch_id", launchID, "task", task)
	} else {
		p.Logger.Info("异步子代理结果已入队", "id", id, "is_group", isGroup, "launch_id", launchID, "task", task)
	}

	// 尝试触发处理：若会话空闲（锁可用）则立即处理排队消息
	b.Go("pending:"+sessionKey(id, isGroup), func() { p.kickPending(b, id, isGroup) })
}

// buildSubagentFollowupTask 拼装追问任务：原任务 + 上一轮结果 + 追加追问。
// 子代理没有历史上下文，追问必须让它在「已知上一轮结论」的前提下继续，而不是
// 整段重做（这是 subagent_followup 相对重新 subagent_run 的核心价值）。
func buildSubagentFollowupTask(task, previousResult, question string) string {
	var sb strings.Builder
	sb.WriteString("【子代理追问】\n")
	sb.WriteString("背景：你之前执行过下面的任务，已经产出了结果。请在此基础上回答追加追问，")
	sb.WriteString("不要整段重做已完成的工作。\n\n")
	sb.WriteString("原任务：\n")
	sb.WriteString(strings.TrimSpace(task))
	sb.WriteString("\n\n上一轮结果：\n")
	if strings.TrimSpace(previousResult) == "" {
		sb.WriteString("（上一轮没有返回文本结果）")
	} else {
		sb.WriteString(strings.TrimSpace(previousResult))
	}
	sb.WriteString("\n\n追加追问：\n")
	sb.WriteString(strings.TrimSpace(question))
	sb.WriteString("\n\n要求：\n")
	sb.WriteString("- 先利用上一轮已获得的信息回答追问；只有信息不足时才去取数/检索，且不要重复已完成的整段调研；\n")
	sb.WriteString("- 追问只针对新增问题，不要复述上一轮全文；如上一轮结论有误，明确说明修正点；\n")
	sb.WriteString("- 最终回复必须自包含（结论 + 关键证据），因为它会作为结果返回给主 AI。")
	return sb.String()
}

// followupTaskText 构造本次追问的完整任务文本：上一轮结果按 ResultInlineRunes
// 治理（超长落盘 + handle），追问内容按 rune 截断。
func (p *AIChatPlugin) followupTaskText(sessionKey string, target asyncSubagentEntry, question string) string {
	previous := p.toolResultLimiter.LimitAt(sessionKey, "subagent_followup", target.result, p.cfg.Subagent.ResultInlineRunes, false)
	return buildSubagentFollowupTask(target.task, previous, tasklog.Truncate(question, followupQuestionMaxRunes))
}

// followupSubagent 对已完成的后台工作发起追问：以原任务 + 原结果 + 新问题为
// 上下文启动一个新的异步子代理（团队成员沿用原角色提示词），立即返回新任务 ID。
func (p *AIChatPlugin) followupSubagent(
	ctx context.Context,
	b bot.Bot,
	id message.QID,
	isGroup bool,
	idOrName, question string,
	timeoutSec int,
	callbacks llmtool.CallBackFuncs,
) string {
	target, ok := p.lookupSessionWorkEntry(id, isGroup, idOrName)
	if !ok {
		return fmt.Sprintf("未找到可追问的任务 %s（可能已过期，默认只保留最近 %d 分钟；可用 subagent_list(include_finished=true) 查看，或重新 subagent_run）",
			idOrName, p.cfg.Subagent.FinishedTTLMin)
	}
	if !target.finished {
		return fmt.Sprintf("%s 仍在运行中，请用 subagent_send 追加指令（追问只用于已完成的任务）", target.display())
	}
	if strings.TrimSpace(target.result) == "" {
		reason := target.err
		if reason == "" {
			reason = "没有返回文本结果"
		}
		return fmt.Sprintf("%s 上次执行未产出可追问的结果（%s），请重新 subagent_run 委派", target.display(), reason)
	}

	rootID := target.rootID
	if rootID == "" {
		rootID = target.id
	}
	g := p.lookupAsyncGroup(id, isGroup)
	if g == nil {
		return fmt.Sprintf("未找到可追问的任务 %s（会话后台工作表已重置，请重新 subagent_run）", idOrName)
	}
	if !g.reserveFollowup(rootID, p.cfg.Subagent.MaxFollowupsPerTask) {
		return fmt.Sprintf("该任务已追问 %d 次（上限 plugin.ai_chat_bot.subagent.max_followups_per_task=%d），请改用新的 subagent_run 或 team_run 处理后续问题",
			g.followupCount(rootID), p.cfg.Subagent.MaxFollowupsPerTask)
	}
	// 与 #新对话 / 面板清空上下文的取消竞争：工作组已被摘除时不要再把新任务
	// 拉进新对话（正常路径下该检查与启动之间只有极短窗口）
	if p.lookupAsyncGroup(id, isGroup) != g {
		g.releaseFollowup(rootID)
		return fmt.Sprintf("会话上下文已重置，%s 的追问已取消；如仍需该问题，请重新 subagent_run 委派", target.display())
	}
	launchID, text, ok := p.startAsyncSubagent(ctx, b, id, isGroup,
		p.followupTaskText(sessionKey(id, isGroup), target, question),
		launchSubagentOptions{
			Kind:       taskKindFollowup,
			Label:      "追问：" + target.display(),
			Prompt:     target.prompt,
			ParentID:   target.id,
			RootID:     rootID,
			TimeoutSec: timeoutSec,
			Callbacks:  callbacks,
		})
	if !ok {
		g.releaseFollowup(rootID)
		return text
	}
	return fmt.Sprintf("✅ 已基于 %s 的上一轮结果发起追问（原任务已完成，已按追问重启；新任务 ID: %s），完成后结果会自动回到会话", target.display(), launchID)
}

// pendingKickAttempts / pendingKickInterval 兜底唤醒的即时重试节奏：入队与持锁者
// 「最后一次 drain」交错时消息会错过所有消费者；这种交错发生在锁释放的临界区
// 附近，短退避重试即可跨过。更持久的情况（如并发槽位被其他会话长期占满）由
// 常驻清扫器 sweepPendingSessions 兜底，不在这里长等。
const (
	pendingKickAttempts = 3
	pendingKickInterval = 100 * time.Millisecond
)

// pendingSweepInterval 常驻清扫器的扫描周期：只处理「有排队消息且能拿到
// 会话锁」的会话，队列为空时零开销。它是排队消息不永久滞留的最终保证——
// 唤醒逻辑集中在入队路径时，限流饱和、锁释放临界区交错等情况都可能让消息
// 错过所有消费者。
const pendingSweepInterval = 10 * time.Second

// kickPending 后台兜底唤醒：确认会话仍有排队消息时尝试接管消费队列。
// 正常路径下持锁者仍在响应，短暂重试后放弃，开销可忽略（错过的会在
// 清扫器周期内被接管）。
func (p *AIChatPlugin) kickPending(b bot.Bot, id message.QID, isGroup bool) {
	p.runQueued(b, id, isGroup)
}

// startPendingSweeper 启动常驻清扫器（与插件同生命周期，同 startChatJanitor 惯例）。
func (p *AIChatPlugin) startPendingSweeper() {
	go func() {
		ticker := time.NewTicker(pendingSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			p.sweepPendingSessions()
		}
	}()
}

// sweepPendingSessions 扫描所有会话的排队队列，尝试处理仍滞留的消息。
// 需要平台外观（chatEntry.bot）才能构造发送回调；拿不到外观的会话跳过，
// 其队列会在下次消息到达时由正常流程消费。
func (p *AIChatPlugin) sweepPendingSessions() {
	if !p.ready {
		return
	}
	p.pendingMsgs.Range(func(key, value any) bool {
		k, ok := key.(string)
		if !ok {
			return true
		}
		q, ok := value.(*pendingQueue)
		if !ok {
			return true
		}
		q.mu.Lock()
		pending := len(q.items) > 0
		q.mu.Unlock()
		if !pending {
			return true
		}
		id, isGroup, ok := parseSessionKey(k)
		if !ok {
			return true
		}
		v, ok := p.sessionBots.Load(k)
		if !ok {
			return true
		}
		b, ok := v.(bot.Bot)
		if !ok || b == nil {
			return true
		}
		// 每个会话独立协程接管：回合是长耗时操作，同步执行会让 10s 周期的
		// ticker 在接管期间不再触发，其余会话的滞留队列得不到兜底。
		// 会话锁保证同一会话不会被重复接管（拿不到锁的协程会立即返回）。
		pendingBot, pendingID, pendingGroup := b, id, isGroup
		b.Go("pending-sweep:"+k, func() {
			p.runQueued(pendingBot, pendingID, pendingGroup)
		})
		return true
	})
}

// parseSessionKey 解析 g:<id> / f:<id> 会话键（见 sessionKey）。
func parseSessionKey(key string) (message.QID, bool, bool) {
	if len(key) < 3 || key[1] != ':' {
		return "", false, false
	}
	switch key[0] {
	case 'g':
		return message.FromString(key[2:]), true, true
	case 'f':
		return message.FromString(key[2:]), false, true
	}
	return "", false, false
}
