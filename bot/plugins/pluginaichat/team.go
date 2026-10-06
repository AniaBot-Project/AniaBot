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

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
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
// 失败时 err 为分类后的错误。
type teamMemberResult struct {
	label    string
	result   string
	err      error
	degraded string
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

// runTeam 同步并行执行团队成员：N 个成员各起一个 b.Go goroutine（panic 自动恢复），
// 全部完成后组装汇总报告返回。成员之间互不可见、互不影响；单成员超时/失败不中断
// 整体执行（失败进报告）。
//
// 注意：成员 context 不在此预建超时——每个成员由 runSubagentWithOptions 内部按
// 父 ctx deadline 独立压缩预算（预留收尾时间），外层预建会双重压缩损失预算；
// 也正因为每成员超时都有界，wg.Wait() 必然收敛。
func (p *AIChatPlugin) runTeam(ctx context.Context, b bot.Bot, id message.QID, isGroup bool,
	timeoutSec int, specs []teamMemberSpec, parentCbs llmtool.CallBackFuncs) (string, error) {
	// 预算预检：剩余不足（父 deadline 30s 内）时直接失败，不启动任何成员
	if _, err := resolveSubagentTimeout(p.subagentTimeout(), timeoutSec, ctx); err != nil {
		return "", fmt.Errorf("无法启动 Agent 团队: %w", err)
	}

	start := time.Now()
	results := make([]teamMemberResult, len(specs))
	var wg sync.WaitGroup
	for i, spec := range specs {
		wg.Add(1)
		b.Go(fmt.Sprintf("team:%s:%d:%s", sessionKey(id, isGroup), i, spec.label), func() {
			defer wg.Done()
			results[i] = p.runTeamMember(ctx, b, id, isGroup, timeoutSec, spec, parentCbs)
		})
	}
	wg.Wait()

	return buildTeamReport(results, time.Since(start)), nil
}

// runTeamMember 单个成员的执行：带角色提示词的一次性子代理，执行参数与主模型
// 一致（模型/生成参数/工具轮数取主模型配置，超时取框架消息处理预算；见
// runSubagentWithOptions）。
func (p *AIChatPlugin) runTeamMember(ctx context.Context, b bot.Bot, id message.QID, isGroup bool,
	timeoutSec int, spec teamMemberSpec, parentCbs llmtool.CallBackFuncs) teamMemberResult {
	logger := p.Logger.WithGroup("team")
	result := teamMemberResult{label: spec.label, degraded: spec.degraded}

	resp, usage, err := p.runSubagentWithOptions(ctx, b, id, isGroup, spec.task, subagentRunOptions{
		prompt:     spec.prompt,
		timeoutSec: timeoutSec,
	}, parentCbs)
	// 成员消耗计入会话与全局配额（发起方已做前置检查，这里只累加不重复拒绝）
	p.quotaManager.Add(sessionKey(id, isGroup), usage)
	// 成员消耗并入会话统计：team_run 在主请求内同步等待完成，
	// 当次请求的 finishQuery 会取走该累计值
	p.addExtraUsage(sessionKey(id, isGroup), usage)
	if err != nil {
		logger.Warn("团队成员执行失败", "member", spec.label, "error", err.Error())
		result.err = err
		return result
	}
	result.result = resp
	return result
}

// buildTeamReport 汇总报告：按输入顺序输出各成员结果块。
// 头行统计成功/失败数；失败块输出错误；降级成员在块内首行标注。
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
		fmt.Fprintf(&sb, "\n===== 成员 %d/%d：%s =====\n", i+1, len(results), r.label)
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
