package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
	"github.com/jeanhua/AniaBot/common/workstate"
)

// teamScopeGlobal 全局团队作用域。与知识库的全局库一致：所有会话都可引用
// 全局团队中的成员（经 team_run 的解析链回退），但仅 Web 面板可管理。
const teamScopeGlobal = "global"

const (
	// teamMaxPerScope 单个会话 scope 可保存的团队数上限
	teamMaxPerScope = 20
	// teamMaxStoredMembers 单个已保存团队的成员数上限
	teamMaxStoredMembers = 10
	// teamMaxMembers 单次最多并行成员数，防止并发 LLM 请求风暴
	teamMaxMembers = 5
	// teamNameMaxRunes 团队名长度上限
	teamNameMaxRunes = 20
	// teamRoleMaxRunes 内联角色描述/成员角色描述长度上限
	teamRoleMaxRunes = 500
)

// teamNamePattern 团队名校验：仅中文/字母/数字/下划线/连字符，1-20 字符。
// 排除冒号等持久化 key 分隔符与空白。
var teamNamePattern = regexp.MustCompile(`^[\p{L}\p{N}_-]{1,20}$`)

// teamMember 一个团队成员定义（已保存团队中的一项）。
type teamMember struct {
	Name      string    `json:"name"`
	Role      string    `json:"role,omitempty"` // 角色描述；空表示按普通子代理执行
	CreatedAt time.Time `json:"created_at"`
}

// teamDefinition 一个已保存的团队（对应一个持久化 key）。
type teamDefinition struct {
	Name      string       `json:"name"`
	Desc      string       `json:"desc,omitempty"`
	Members   []teamMember `json:"members"`
	CreatedAt time.Time    `json:"created_at"`
}

// teamManager 持久团队管理器：按会话 scope（g:会话ID / f:用户ID）隔离存取。
//
// 每个团队是一个 JSON 对象整体读写（PersistentStorage 的 KV 语义），
// key = scope + ":" + 团队名，如 "g:12345:开发团队"；同 scope 内通过
// Keys(scope+":") 前缀列出。所有变更在 mu 保护下串行落盘；
// 存储错误内部记录日志，不拖垮主对话流程（与 memoryManager 风格一致）。
type teamManager struct {
	store  storage.PersistentStorage
	logger *slog.Logger

	mu sync.Mutex
}

func newTeamManager(store storage.PersistentStorage, logger *slog.Logger) *teamManager {
	return &teamManager{store: store.Clone("team:"), logger: logger}
}

// validateTeamName 校验团队名：TrimSpace 后匹配 teamNamePattern。
func validateTeamName(name string) error {
	name = strings.TrimSpace(name)
	if !teamNamePattern.MatchString(name) {
		return fmt.Errorf("团队名只能包含中文/字母/数字/下划线/连字符，长度 1-%d 字符", teamNameMaxRunes)
	}
	return nil
}

// list 列出指定 scope 的团队，按团队名排序；无记录或读取失败时返回 nil。
func (m *teamManager) list(scope string) []teamDefinition {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.store.Keys(context.Background(), scope+":")
	if err != nil {
		m.logger.Error("列出团队失败", "scope", scope, "error", err)
		return nil
	}
	slices.Sort(keys)
	defs := make([]teamDefinition, 0, len(keys))
	for _, k := range keys {
		if def, ok := m.getLocked(k); ok {
			defs = append(defs, def)
		}
	}
	return defs
}

func (m *teamManager) getLocked(key string) (teamDefinition, bool) {
	var def teamDefinition
	if ok := m.store.Get(context.Background(), key, &def); !ok {
		return teamDefinition{}, false
	}
	return def, true
}

// get 读取指定 scope 中的团队；不存在时返回 false。
func (m *teamManager) get(scope, name string) (teamDefinition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getLocked(scope + ":" + strings.TrimSpace(name))
}

// scopes 列出当前已有团队的全部会话 scope（g:会话ID / f:用户ID），排序后返回。
// 供 Web 面板的 Agent 团队管理页使用。
func (m *teamManager) scopes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.store.Keys(context.Background(), "")
	if err != nil {
		m.logger.Error("列出团队 scope 失败", "error", err)
		return nil
	}
	seen := make(map[string]bool, len(keys))
	scopes := make([]string, 0, len(keys))
	for _, k := range keys {
		// key = scope + ":" + 团队名（团队名不含冒号），取最后一个冒号前为 scope
		if idx := strings.LastIndex(k, ":"); idx > 0 {
			scope := k[:idx]
			if !seen[scope] {
				seen[scope] = true
				scopes = append(scopes, scope)
			}
		}
	}
	slices.Sort(scopes)
	return scopes
}

// create 创建团队；同名团队已存在时返回错误。
// 校验顺序：团队名 → 成员非空 → 成员名去重 → 成员名/角色截断 → 数量上限 → 落盘。
func (m *teamManager) create(scope, name, desc string, members []teamMember) (teamDefinition, error) {
	name = strings.TrimSpace(name)
	if err := validateTeamName(name); err != nil {
		return teamDefinition{}, err
	}
	members, err := validateTeamMembers(members)
	if err != nil {
		return teamDefinition{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := scope + ":" + name
	if _, ok := m.getLocked(key); ok {
		return teamDefinition{}, fmt.Errorf("团队「%s」已存在，可先 team_delete 再创建", name)
	}
	keys, err := m.store.Keys(context.Background(), scope+":")
	if err != nil {
		m.logger.Error("检查团队数量失败", "scope", scope, "error", err)
		return teamDefinition{}, errors.New("团队保存失败，请查看日志")
	}
	if len(keys) >= teamMaxPerScope {
		return teamDefinition{}, fmt.Errorf("当前会话已保存 %d 个团队（上限），请先 team_delete 删除不需要的团队", teamMaxPerScope)
	}

	def := teamDefinition{
		Name:      name,
		Desc:      strings.TrimSpace(desc),
		Members:   members,
		CreatedAt: time.Now(),
	}
	if ok := m.store.Set(context.Background(), key, def); !ok {
		m.logger.Error("保存团队失败", "scope", scope, "name", name)
		return teamDefinition{}, errors.New("团队保存失败，请查看日志")
	}
	return def, nil
}

// update 更新团队说明与成员；团队不存在时返回错误。创建时间保留不变。
func (m *teamManager) update(scope, name, desc string, members []teamMember) error {
	name = strings.TrimSpace(name)
	if err := validateTeamName(name); err != nil {
		return err
	}
	members, err := validateTeamMembers(members)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := scope + ":" + name
	def, ok := m.getLocked(key)
	if !ok {
		return fmt.Errorf("团队「%s」不存在，可先 team_create 创建", name)
	}
	def.Desc = strings.TrimSpace(desc)
	def.Members = members
	if ok := m.store.Set(context.Background(), key, def); !ok {
		m.logger.Error("更新团队后落盘失败", "scope", scope, "name", name)
		return errors.New("团队保存失败，请查看日志")
	}
	return nil
}

// delete 删除团队；不存在时返回 false。
func (m *teamManager) delete(scope, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := scope + ":" + strings.TrimSpace(name)
	if _, ok := m.getLocked(key); !ok {
		return false
	}
	if ok := m.store.Del(context.Background(), key); !ok {
		m.logger.Error("删除团队失败", "scope", scope, "name", name)
	}
	return true
}

// validateTeamMembers 校验团队成员列表：非空、成员名去重、成员名/角色清理与截断。
func validateTeamMembers(members []teamMember) ([]teamMember, error) {
	if len(members) == 0 {
		return nil, errors.New("团队成员不能为空（至少 1 名成员）")
	}
	if len(members) > teamMaxStoredMembers {
		return nil, fmt.Errorf("团队成员数量 %d 超过上限 %d", len(members), teamMaxStoredMembers)
	}
	seen := make(map[string]bool, len(members))
	clean := make([]teamMember, 0, len(members))
	for _, mem := range members {
		mem.Name = strings.TrimSpace(mem.Name)
		if mem.Name == "" {
			return nil, errors.New("成员 name 不能为空")
		}
		if seen[mem.Name] {
			return nil, fmt.Errorf("团队成员重复：%s", mem.Name)
		}
		seen[mem.Name] = true
		mem.Role = tasklog.Truncate(strings.TrimSpace(mem.Role), teamRoleMaxRunes)
		clean = append(clean, mem)
	}
	return clean, nil
}

// ---- 成员解析与并行执行 ----

// teamMemberSpec 解析后的成员执行规格。
type teamMemberSpec struct {
	label    string // 报告中的显示名（角色名/团队成员名/内联角色名）
	prompt   string // 成员系统提示词（空 = 降级为默认子代理提示词）
	task     string // 成员有效任务：总体任务 + 可选专属任务拼接（leader 分工）
	degraded string // 降级说明（空表示正常解析）
}

// teamMemberResult 单成员执行结果。成功时 result 含 runSubagent 的元信息前缀，
// 失败时 err 为分类后的错误；usage/duration 供 team_run 父记录汇总成员消耗与耗时，
// id 为该成员的可寻址 ID（报告、后台工作表与追问都靠它定位）。
type teamMemberResult struct {
	id       string
	label    string
	result   string
	err      error
	degraded string
	usage    aichat.TokenUsage
	duration time.Duration
}

// resolveTeamMemberSpec 成员解析降级链：
//  1. role 非空 → 内联角色（最高优先级），label 取 name；
//  2. name 命中预置角色（lookupTeamRole）→ 使用预置提示词；
//  3. name 命中当前 scope 已保存团队中的成员 → 使用该成员 Role（非空时）；
//  4. name 命中全局团队（teamScopeGlobal，所有会话共享，面板管理）中的成员 → 使用其 Role；
//  5. 以上皆未命中 → prompt 置空（执行时兜底为 defaultSubagentPrompt），
//     degraded 记为"未识别的角色「name」，已按普通子代理执行"。
//
// 角色描述统一截断到 teamRoleMaxRunes，防止超长描述撑大 system prompt。
func (p *AIChatPlugin) resolveTeamMemberSpec(mgr *teamManager, scope, name, role string) teamMemberSpec {
	name = strings.TrimSpace(name)
	spec := teamMemberSpec{label: name}

	role = tasklog.Truncate(strings.TrimSpace(role), teamRoleMaxRunes)
	if role != "" {
		// 内联角色描述优先级最高：直接作为成员系统提示词
		spec.prompt = "你是一名" + name + "。" + role + "\n\n" + teamRoleCommonNote
		return spec
	}

	if r, ok := lookupTeamRole(name); ok {
		spec.prompt = r.Prompt
		return spec
	}

	if mgr != nil {
		// 命中当前 scope 或全局团队中的成员（按团队成员名引用）：使用该成员的角色描述
		for _, s := range []string{scope, teamScopeGlobal} {
			for _, def := range mgr.list(s) {
				for _, mem := range def.Members {
					if strings.TrimSpace(mem.Name) == name && strings.TrimSpace(mem.Role) != "" {
						spec.prompt = "你是一名" + name + "。" + strings.TrimSpace(mem.Role) + "\n\n" + teamRoleCommonNote
						return spec
					}
				}
			}
		}
	}

	spec.degraded = fmt.Sprintf("未识别的角色「%s」，已按普通子代理执行", name)
	return spec
}

// teamEffectiveTask 组合成员的有效任务：顶层总体任务必填作为共享背景，
// 成员专属任务非空时拼接其后（leader 分工模式）；为空/全空白时回退为总体任务。
func teamEffectiveTask(base, memberTask string) string {
	memberTask = strings.TrimSpace(memberTask)
	if memberTask == "" {
		return base
	}
	return base + "\n\n" + memberTask
}

// runningTeamRun 一次正在执行的团队运行（#新对话 / 面板清空上下文据此取消）。
// 团队运行自异步化后与异步子代理一样在后台执行，完成前主回合已结束。
type runningTeamRun struct {
	cancel context.CancelFunc
}

// teamRunGroup 一个会话正在执行的团队运行集合。同一轮的多个工具调用可并行执行，
// AI 可能同时发起多个 team_run，因此按会话记录全部运行而不是单个取消函数。
type teamRunGroup struct {
	mu   sync.Mutex
	runs map[*runningTeamRun]struct{}
}

func (g *teamRunGroup) add(e *runningTeamRun) {
	g.mu.Lock()
	if g.runs == nil {
		g.runs = make(map[*runningTeamRun]struct{}, 1)
	}
	g.runs[e] = struct{}{}
	g.mu.Unlock()
}

// remove 移除一项并返回是否已空（取消路径 LoadAndDelete 摘除整组后为 false）。
func (g *teamRunGroup) remove(e *runningTeamRun) (empty bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.runs, e)
	return len(g.runs) == 0
}

// cancelAll 取消组内全部运行，返回取消数量。
func (g *teamRunGroup) cancelAll() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.runs)
	for e := range g.runs {
		e.cancel()
	}
	g.runs = nil
	return n
}

// registerTeamRun 把一次团队运行登记到会话后台工作表，返回派生上下文与注销函数。
// 上下文被 #新对话（cancelTeamRuns）或 /stop（父上下文取消）中止时，成员子代理
// 随之取消，runAsyncTeam 会尽快收敛并写入 cancelled 终态。
func (p *AIChatPlugin) registerTeamRun(ctx context.Context, id message.QID, isGroup bool) (context.Context, func()) {
	key := sessionKey(id, isGroup)
	teamCtx, cancel := context.WithCancel(ctx)
	entry := &runningTeamRun{cancel: cancel}
	v, _ := p.teamRuns.LoadOrStore(key, &teamRunGroup{})
	g, _ := v.(*teamRunGroup)
	g.add(entry)
	return teamCtx, func() {
		if g.remove(entry) {
			p.teamRuns.CompareAndDelete(key, g)
		}
		cancel()
	}
}

// cancelTeamRuns 取消会话当前全部正在执行的团队运行，返回取消数量。
func (p *AIChatPlugin) cancelTeamRuns(id message.QID, isGroup bool) int {
	v, ok := p.teamRuns.LoadAndDelete(sessionKey(id, isGroup))
	if !ok {
		return 0
	}
	g, ok := v.(*teamRunGroup)
	if !ok || g == nil {
		return 0
	}
	return g.cancelAll()
}

// teamMemberIDs 为一次团队运行分配可寻址的成员 ID：<runID>-m1 …（报告、后台工作表
// 与面板都用它引用单个成员）。成员 ID 在一次运行内稳定，完成后 TTL 内仍可追问。
func teamMemberIDs(runID string, count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-m%d", runID, i+1)
	}
	return ids
}

// launchAsyncTeam 异步启动一次团队运行：登记父/成员任务记录与会话后台工作表后
// 立即返回任务 ID 与成员 ID，成员在后台并行执行；全部结束后汇总报告回灌会话。
//
// 与同步版的区别：不再阻塞主回合；每个成员都是可寻址的后台条目（<runID>-mN），
// 运行中可 subagent_send 单独注入指令，完成后可在 TTL 内 subagent_followup。
func (p *AIChatPlugin) launchAsyncTeam(ctx context.Context, b bot.Bot, id message.QID, isGroup bool,
	task string, timeoutSec int, specs []teamMemberSpec, parentCbs llmtool.CallBackFuncs) (string, error) {
	// 预算预检：父回合剩余不足时直接失败，不启动任何成员
	if _, err := resolveSubagentTimeout(p.subagentTimeout(), timeoutSec, ctx); err != nil {
		return "", fmt.Errorf("无法启动 Agent 团队: %w", err)
	}
	runID := newTaskID()
	memberIDs := teamMemberIDs(runID, len(specs))

	// 后台运行脱离当前回合：只继承插件生命周期（卸载时取消），成员超时由
	// runSubagentWithOptions 内部管理（与异步子代理一致）
	base := context.Background()
	if p.runCtx != nil {
		base = p.runCtx
	}
	runCtx, unregister := p.registerTeamRun(base, id, isGroup)

	memberCtxs := make([]context.Context, len(specs))
	memberCancels := make([]context.CancelFunc, len(specs))
	memberSteers := make([]chan string, len(specs))
	for i := range specs {
		memberCtxs[i], memberCancels[i] = context.WithCancel(runCtx)
		memberSteers[i] = make(chan string, 8)
	}

	// 条目一次性登记：父运行 + 全部成员，避免中间态只看到一半成员
	entries := make([]asyncSubagentEntry, 0, len(specs)+1)
	entries = append(entries, asyncSubagentEntry{
		id: runID, kind: taskKindTeamRun, label: "团队", task: task, cancel: unregister,
	})
	for i, spec := range specs {
		entries = append(entries, asyncSubagentEntry{
			id: memberIDs[i], kind: taskKindTeamMember, label: spec.label, task: spec.task,
			prompt: spec.prompt, parentID: runID, steer: memberSteers[i], cancel: memberCancels[i],
		})
	}
	// 团队 ID 的 subagent_send 广播给全部成员（通道满的成员跳过，不阻塞其余成员）
	entries[0].broadcast = func(msg string) int {
		delivered := 0
		for _, ch := range memberSteers {
			select {
			case ch <- msg:
				delivered++
			default:
			}
		}
		return delivered
	}

	runTask := p.beginTeamRunTask(id, isGroup, task, runID)
	p.getAsyncGroup(id, isGroup).addEntries(entries)

	b.Go("team:"+runID, func() {
		defer unregister()
		// 登记为进行中的后台任务：重启前等待整队执行完成（受重启等待超时约束）
		endWork := workstate.Begin(workstate.KindTeam, "Agent 团队 "+truncateTaskTitle(task))
		defer endWork()
		// 异常退出兜底：正常完成的条目已转 finished，取消路径由 runAsyncTeam 移除
		defer p.retireTeamEntries(id, isGroup, runID, memberIDs)
		p.runAsyncTeam(runCtx, b, id, isGroup, runID, task, timeoutSec, specs, memberIDs,
			memberCtxs, memberSteers, runTask, parentCbs)
	})

	// 立即返回可寻址的任务/成员 ID：主 AI 可在后续回合直接引用
	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ Agent 团队已启动（任务ID: %s，成员 %d 人），正在后台执行，完成后结果会自动回到会话", runID, len(specs))
	sb.WriteString("\n成员 ID：")
	for i, spec := range specs {
		if i > 0 {
			sb.WriteString("、")
		}
		fmt.Fprintf(&sb, "%s（%s）", memberIDs[i], spec.label)
	}
	sb.WriteString("\n提示：执行期间不要等待或轮询；可用 subagent_list 查看进度、subagent_send 给成员补充指令（成员 ID 只发该成员，团队 ID 广播全队）、subagent_cancel 取消成员或整队；完成后可在保留窗口内用 subagent_followup 按成员 ID 继续追问。")
	return sb.String(), nil
}

// markTeamMemberEntryFinished 成员结束时立即更新会话后台工作表：完成/失败/
// 超时条目转 finished（TTL 内可追问），被单独取消的成员直接移除。
func (p *AIChatPlugin) markTeamMemberEntryFinished(id message.QID, isGroup bool, memberID string, r teamMemberResult) {
	if isContextCanceled(r.err) {
		p.removeAsyncSubagent(id, isGroup, memberID)
		return
	}
	p.finishAsyncSubagent(id, isGroup, memberID, r.result, r.err)
}

// runAsyncTeam 后台执行团队运行：成员并行，全部结束后写任务记录、把成员/团队
// 条目转为完成态并回灌汇总报告；父上下文取消时静默收尾（不注入结果）。
func (p *AIChatPlugin) runAsyncTeam(runCtx context.Context, b bot.Bot, id message.QID, isGroup bool,
	runID, task string, timeoutSec int, specs []teamMemberSpec, memberIDs []string,
	memberCtxs []context.Context, memberSteers []chan string, runTask *agentTask,
	parentCbs llmtool.CallBackFuncs) {
	start := time.Now()
	results := make([]teamMemberResult, len(specs))
	var wg sync.WaitGroup
	for i := range specs {
		i := i
		wg.Add(1)
		b.Go(fmt.Sprintf("team:%s:%d:%s", runID, i, specs[i].label), func() {
			defer wg.Done()
			// 成员执行 panic 时记录为失败成员，避免 b.Go 的 recover 让结果看起来成功
			defer func() {
				if r := recover(); r != nil {
					p.Logger.Error("团队成员执行异常", "member", specs[i].label, "member_id", memberIDs[i], "panic", fmt.Sprint(r))
					results[i] = teamMemberResult{id: memberIDs[i], label: specs[i].label, err: fmt.Errorf("团队成员执行异常: %v", r)}
				}
			}()
			// 全局子代理池：与异步子代理共用名额；拿不到名额时等取消，不阻塞其他成员
			if p.subagentSem != nil {
				select {
				case p.subagentSem <- struct{}{}:
					defer func() { <-p.subagentSem }()
				case <-memberCtxs[i].Done():
					results[i] = teamMemberResult{id: memberIDs[i], label: specs[i].label, err: memberCtxs[i].Err()}
					return
				}
			}
			results[i] = p.runTeamMember(memberCtxs[i], b, id, isGroup, timeoutSec, specs[i],
				parentCbs, runTask, memberIDs[i], memberSteers[i])
			// 成员先于整队完成时立刻转为完成态：早完成的成员就能被 followup，
			// 不再继续占用 steer 通道等整队结束
			p.markTeamMemberEntryFinished(id, isGroup, memberIDs[i], results[i])
		})
	}
	wg.Wait()

	duration := time.Since(start)
	report := buildTeamReport(results, duration)
	p.finishTeamRunTask(runTask, report, results, duration, runCtx.Err())

	if err := runCtx.Err(); err != nil {
		// /stop / #新对话 / 面板清空 / 插件卸载：整队取消，移除后台条目且不注入结果
		p.removeAsyncSubagent(id, isGroup, runID)
		for _, mid := range memberIDs {
			p.removeAsyncSubagent(id, isGroup, mid)
		}
		p.Logger.Info("Agent 团队运行已被取消", "task_id", runID, "id", id, "is_group", isGroup, "error", err.Error())
		return
	}

	// 成员完成态：正常情况下成员在各自 goroutine 结束时已转 finished（早完成
	// 即可追问）；这里补齐 panic 等异常路径遗漏的成员，并移除被单独取消的成员。
	for i, r := range results {
		if isContextCanceled(r.err) {
			p.removeAsyncSubagent(id, isGroup, memberIDs[i])
			continue
		}
		p.finishAsyncSubagent(id, isGroup, memberIDs[i], r.result, r.err)
	}
	// 父条目保留完整报告：团队 ID 的追问基于整队报告继续，成员 ID 则基于单成员结果
	status, _ := teamRunOutcome(results, nil)
	var runErr error
	if status != taskStatusCompleted {
		runErr = fmt.Errorf("团队全部成员执行失败")
	}
	p.finishAsyncSubagent(id, isGroup, runID, report, runErr)
	p.Logger.Info("Agent 团队执行完成", "task_id", runID, "id", id, "is_group", isGroup,
		"status", status, "duration", duration, "members", len(results))
	p.onTeamComplete(b, id, isGroup, runID, task, specs, memberIDs, results, report)
}

// retireTeamEntries 异常退出兜底：移除仍未结束的父/成员条目（已完成的保留）。
func (p *AIChatPlugin) retireTeamEntries(id message.QID, isGroup bool, runID string, memberIDs []string) {
	p.retireRunningAsyncSubagent(id, isGroup, runID)
	for _, mid := range memberIDs {
		p.retireRunningAsyncSubagent(id, isGroup, mid)
	}
}

// onTeamComplete 团队运行完成后的回调（在后台 goroutine 中执行）：构造包含任务 ID、
// 成员 ID 与汇总报告的合成消息入队，并触发主会话处理。报告按 ResultInlineRunes
// 治理（超长落盘 + handle），完成条目本身仍保留完整报告供追问。
func (p *AIChatPlugin) onTeamComplete(b bot.Bot, id message.QID, isGroup bool, runID, task string,
	specs []teamMemberSpec, memberIDs []string, results []teamMemberResult, report string) {
	status, _ := teamRunOutcome(results, nil)
	title := "【Agent 团队执行完成】"
	if status != taskStatusCompleted {
		title = "【Agent 团队执行结束】"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s状态=%s\n任务ID: %s\n任务: %s\n成员: ", title, status, runID, truncateTaskTitle(task))
	for i := range memberIDs {
		if i > 0 {
			sb.WriteString(" / ")
		}
		memberStatus, _ := taskRunStatus(results[i].err)
		fmt.Fprintf(&sb, "%s（%s·%s）", specs[i].label, memberIDs[i], taskStatusLabel(memberStatus))
	}
	sb.WriteString("\n\n")
	limited := p.toolResultLimiter.LimitAt(sessionKey(id, isGroup), "team_result", report, p.cfg.Subagent.ResultInlineRunes, false)
	sb.WriteString(limited)
	sb.WriteString("\n\n提示：可在保留窗口内用 subagent_followup(id=成员ID 或 团队任务ID, message=...) 基于结果继续追问。")

	syntheticMsg := message.Message{
		Sender: message.MessageSender{
			Nickname: "Agent 团队",
			UserId:   message.FromUint64(0),
		},
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": sb.String()}},
		},
	}
	p.sessionBots.Store(sessionKey(id, isGroup), b)
	if _, ok := p.enqueuePending(id, isGroup, syntheticMsg); !ok {
		p.Logger.Warn("Agent 团队结果入队失败（队列已满）", "id", id, "is_group", isGroup, "task_id", runID)
		return
	}
	p.Logger.Info("Agent 团队结果已入队", "id", id, "is_group", isGroup, "task_id", runID)
	b.Go("pending:"+sessionKey(id, isGroup), func() { p.kickPending(b, id, isGroup) })
}

// runTeamMember 单个成员的执行：带角色提示词的一次性子代理，执行参数与主模型
// 一致（模型/生成参数/工具轮数取主模型配置）。执行期间用 subagentTrace 收集工具
// 调用步骤，随成员子记录落库（面板可展开查看它具体做了什么）。
//
// memberID 为成员的可寻址 ID（<runID>-mN），steer 为运行中追加指令通道。
func (p *AIChatPlugin) runTeamMember(ctx context.Context, b bot.Bot, id message.QID, isGroup bool,
	timeoutSec int, spec teamMemberSpec, parentCbs llmtool.CallBackFuncs, runTask *agentTask,
	memberID string, steer <-chan string) teamMemberResult {
	logger := p.Logger.WithGroup("team")
	result := teamMemberResult{id: memberID, label: spec.label, degraded: spec.degraded}

	memberTask := p.beginTeamMemberTask(runTask, id, isGroup, spec, memberID)
	trace := newSubagentTrace()
	// 执行中增量回写工具步骤，面板「后台任务」页可在成员跑完前就看到进度
	trace.progress = p.trackProgress(memberTask)
	start := time.Now()
	resp, usage, err := p.runSubagentWithOptions(ctx, b, id, isGroup, spec.task, subagentRunOptions{
		prompt:     spec.prompt,
		timeoutSec: timeoutSec,
		steer:      steer,
		trace:      trace,
	}, parentCbs)
	result.usage = usage
	result.duration = time.Since(start)
	p.finishTeamMemberTask(memberTask, spec, resp, usage, err, result.duration, trace)
	// 成员消耗计入会话与全局配额（发起方已做前置检查，这里只累加不重复拒绝）
	p.quotaManager.Add(sessionKey(id, isGroup), usage)
	// 成员消耗并入会话统计：由下一次 finishQuery 取走累计值
	p.addExtraUsage(sessionKey(id, isGroup), usage)
	if err != nil {
		logger.Warn("团队成员执行失败", "member", spec.label, "member_id", memberID, "error", err.Error())
		result.err = err
		return result
	}
	result.result = resp
	return result
}

// buildTeamReport 汇总报告：按输入顺序输出各成员结果块。
// 头行统计成功/失败数；块头带成员可寻址 ID（subagent_followup 可精确定位到成员）；
// 失败块输出错误；降级成员在块内首行标注。
func buildTeamReport(results []teamMemberResult, total time.Duration) string {
	success, failed := 0, 0
	for _, r := range results {
		if r.err != nil {
			failed++
		} else {
			success++
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "【Agent 团队执行完成】耗时 %.1fs · 成员 %d 人（成功 %d · 失败 %d）\n",
		total.Seconds(), len(results), success, failed)
	for i, r := range results {
		memberID := ""
		if r.id != "" {
			memberID = "（ID: " + r.id + "）"
		}
		fmt.Fprintf(&sb, "\n===== 成员 %d/%d：%s%s =====\n", i+1, len(results), r.label, memberID)
		if r.degraded != "" {
			sb.WriteString("（" + r.degraded + "）\n")
		}
		switch {
		case r.err != nil:
			sb.WriteString("执行失败：" + r.err.Error() + "\n")
		case r.result == "":
			sb.WriteString("（成员没有返回内容）\n")
		default:
			sb.WriteString(r.result + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
