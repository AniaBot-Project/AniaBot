package pluginaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 团队任务板：主 AI 用持久化任务板拆分任务、声明依赖与写作用域，
// 团队成员/子代理按任务认领并完成。
//
// 存储复用后台任务注册表 ania_agent_task（kind=team_task），不新建表：
// 任务标题存 Title，描述存 Prompt，任务板专有信息（负责人/依赖/写作用域/结果）
// 存 Meta JSON。团队任务按会话（g:/f:）隔离，只能认领本会话的任务。
//
// 并发：认领/完成在 p.teamBoardMu 内做「读-校验-写」，保证同一进程内同一任务
// 不会被并发认领；多实例部署时以数据库为准（最后写入者生效，不做分布式锁）。

// teamTaskMeta 团队任务板条目的 Meta JSON 约定（仓内唯一权威定义）。
type teamTaskMeta struct {
	Owner       string   `json:"owner,omitempty"`        // 认领者名字（团队成员名/角色）
	BlockedBy   []string `json:"blocked_by,omitempty"`   // 依赖的团队任务 ID：全部 completed 后才可认领
	WriteScopes []string `json:"write_scopes,omitempty"` // 写作用域（文件/目录前缀）：仅记录+展示，不做强制锁
	Result      string   `json:"result,omitempty"`       // 完成结果摘要（截断后入库）
}

const (
	// teamBoardMaxTasks 单个会话允许同时存在的「未完成」团队任务上限。
	// 已完成任务保留在任务板上作为历史（可通过 include_completed 查看），
	// 因此不计入上限，避免任务板做不了 20 个任务后永久锁死。
	teamBoardMaxTasks = 20

	// teamBoardScanLimit 内部整板扫描时读取的会话任务行数上限：远大于任务
	// 上限（会话里还有回合/子代理等其他 kind 的行），保证活跃的团队任务都能
	// 被扫到；列表工具按契约单独用 teamBoardListLimit。
	teamBoardScanLimit = 200

	// teamBoardListLimit team_task_list 每次读取的会话任务行数（契约值 50）。
	teamBoardListLimit = 50

	// teamTaskResultMaxRunes 完成结果写入 Meta 前的 rune 上限。
	teamTaskResultMaxRunes = 4000

	// teamBoardDisabledText taskStore 未启用时的统一提示。
	teamBoardDisabledText = "任务板未启用（缺少 SQL 存储）"
)

// teamBoardToolBase 团队任务板工具共享插件引用与当前会话信息（创建时绑定）。
type teamBoardToolBase struct {
	plugin  *AIChatPlugin
	id      message.QID
	isGroup bool
}

func (b teamBoardToolBase) sessionKey() string { return sessionKey(b.id, b.isGroup) }

// newTeamBoardTools 创建团队任务板工具（team_task_create/list/claim/complete）。
// 由主会话注册（Lead 在 llm.go 调用）；任务板未启用（taskStore=nil）时各工具
// 返回可读提示而不是注册失败。
func newTeamBoardTools(p *AIChatPlugin, id message.QID, isGroup bool) []llmtool.Tool {
	base := teamBoardToolBase{plugin: p, id: id, isGroup: isGroup}
	createDesc := "在团队任务板创建一个持久化任务，供团队成员/子代理认领执行。用它把多步骤工作拆成互相独立的任务：" +
		"title 写简短可辨识的标题，description 写自包含的目标/上下文/验收标准（认领者可能看不到主对话），" +
		"blocked_by 声明依赖的任务 ID（依赖全部完成后才可认领），write_scopes 声明允许改动的文件/目录前缀（仅记录并展示，提醒团队成员各改各的、尽量不重叠）。" +
		fmt.Sprintf("任务板按当前会话隔离，最多同时存在 %d 个未完成任务。", teamBoardMaxTasks)
	listDesc := "列出当前会话的团队任务板：ID、状态、负责人、标题、阻塞依赖。" +
		"默认只显示未完成任务（排队/进行中）；include_completed=true 时连同已完成任务一起列出。"
	claimDesc := "认领团队任务板上的一个任务（排队 → 进行中）。owner 填认领者名字（如团队成员名/角色）。" +
		"依赖任务未全部完成、任务已被别人认领、或任务不属于当前会话时返回可读原因；认领成功后任务由你负责。"
	completeDesc := "完成一个已认领的团队任务（进行中 → 已完成）。result 填结果摘要（会记录到任务板）。" +
		"返回因此解除阻塞、现在可以认领的后续任务 ID，便于编排。owner 可选：填写时校验与认领者一致。"
	return []llmtool.Tool{
		&teamTaskCreateTool{
			BaseTool:          llmtool.MakeBaseTool("team_task_create", createDesc, teamTaskCreateParams{}),
			teamBoardToolBase: base,
		},
		&teamTaskListTool{
			BaseTool:          llmtool.MakeBaseTool("team_task_list", listDesc, teamTaskListParams{}),
			teamBoardToolBase: base,
		},
		&teamTaskClaimTool{
			BaseTool:          llmtool.MakeBaseTool("team_task_claim", claimDesc, teamTaskClaimParams{}),
			teamBoardToolBase: base,
		},
		&teamTaskCompleteTool{
			BaseTool:          llmtool.MakeBaseTool("team_task_complete", completeDesc, teamTaskCompleteParams{}),
			teamBoardToolBase: base,
		},
	}
}

// ---- team_task_create ----

// teamTaskCreateParams team_task_create 参数。
type teamTaskCreateParams struct {
	Title       string   `json:"title" desc:"任务标题（简短可辨识，如「实现登录接口」）"`
	Description string   `json:"description,omitempty" desc:"任务描述：目标、上下文、验收标准；认领者可能看不到主对话，必须自包含"`
	BlockedBy   []string `json:"blocked_by,omitempty" desc:"依赖的任务 ID 列表：这些团队任务全部完成后本任务才可认领；必须是当前会话已存在的团队任务 ID"`
	WriteScopes []string `json:"write_scopes,omitempty" desc:"允许写入的文件/目录前缀（如 bot/plugins/x.go）：仅记录并在任务板展示以提醒避免冲突，不做强制锁"`
}

type teamTaskCreateTool struct {
	llmtool.BaseTool[teamTaskCreateParams]
	teamBoardToolBase
}

func (t *teamTaskCreateTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*teamTaskCreateParams)
	title := truncateTaskTitle(strings.TrimSpace(p.Title))
	if title == "" {
		return "", fmt.Errorf("title 不能为空")
	}
	if t.plugin.taskStore == nil {
		return teamBoardDisabledText, nil
	}
	key := t.sessionKey()
	blockedBy := teamTaskNormalizeList(p.BlockedBy)
	scopes := teamTaskNormalizeList(p.WriteScopes)
	task := &agentTask{
		ID:         newTaskID(),
		SessionKey: key,
		Kind:       taskKindTeamTask,
		Status:     taskStatusQueued,
		Title:      title,
		Prompt:     truncateTaskPrompt(strings.TrimSpace(p.Description)),
	}

	dbCtx, cancel := t.plugin.taskDB()
	defer cancel()

	// 依赖校验：必须存在、同会话、kind=team_task、不能自依赖
	if err := t.plugin.validateTeamTaskBlockers(dbCtx, key, blockedBy, task.ID); err != nil {
		return "", err
	}

	// 数量上限：只统计未完成的团队任务（已完成任务是历史记录，不计入上限）
	existing, err := t.plugin.taskStore.ListBySession(dbCtx, key, teamBoardScanLimit)
	if err != nil {
		return "", fmt.Errorf("读取任务板失败: %w", err)
	}
	active := 0
	for _, it := range existing {
		if it.Kind == taskKindTeamTask && it.Status != taskStatusCompleted {
			active++
		}
	}
	if active >= teamBoardMaxTasks {
		return "", fmt.Errorf("当前会话未完成的团队任务已达上限 %d 个，请先完成已有任务再创建", teamBoardMaxTasks)
	}

	task.Meta = encodeTeamTaskMeta(teamTaskMeta{BlockedBy: blockedBy, WriteScopes: scopes})
	if err := t.plugin.taskStore.Create(dbCtx, task); err != nil {
		return "", fmt.Errorf("创建团队任务失败: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "已创建团队任务 %s（%s）", task.ID, task.Title)
	if len(blockedBy) > 0 {
		fmt.Fprintf(&sb, "\n依赖任务：%s（全部完成前不可认领）", teamTaskJoin(blockedBy))
	}
	if len(scopes) > 0 {
		fmt.Fprintf(&sb, "\n写作用域：%s（认领者只应改动这些路径；不做强制锁，靠任务板约定避免冲突）", teamTaskJoin(scopes))
	}
	sb.WriteString("\n可用 team_task_list 查看任务板，团队成员用 team_task_claim 认领。")
	return sb.String(), nil
}

// ---- team_task_list ----

// teamTaskListParams team_task_list 参数。
type teamTaskListParams struct {
	IncludeCompleted bool `json:"include_completed,omitempty" desc:"是否包含已完成任务（默认 false：只看排队/进行中的任务）"`
}

type teamTaskListTool struct {
	llmtool.BaseTool[teamTaskListParams]
	teamBoardToolBase
}

func (t *teamTaskListTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*teamTaskListParams)
	if t.plugin.taskStore == nil {
		return teamBoardDisabledText, nil
	}
	dbCtx, cancel := t.plugin.taskDB()
	defer cancel()
	rows, err := t.plugin.taskStore.ListBySession(dbCtx, t.sessionKey(), teamBoardListLimit)
	if err != nil {
		return "", fmt.Errorf("读取任务板失败: %w", err)
	}
	board := make([]*agentTask, 0, len(rows))
	for _, it := range rows {
		if it.Kind != taskKindTeamTask {
			continue
		}
		if !p.IncludeCompleted && it.Status == taskStatusCompleted {
			continue
		}
		board = append(board, it)
	}
	if len(board) == 0 {
		return "任务板为空", nil
	}

	var sb strings.Builder
	if p.IncludeCompleted {
		fmt.Fprintf(&sb, "团队任务板（共 %d 个任务，含已完成）：\n", len(board))
	} else {
		fmt.Fprintf(&sb, "团队任务板（共 %d 个未完成任务）：\n", len(board))
	}
	sb.WriteString("ID | 状态 | 负责人 | 标题 | 阻塞依赖\n")
	for _, it := range board {
		meta := parseTeamTaskMeta(it.Meta)
		owner := meta.Owner
		if owner == "" {
			owner = "-"
		}
		blockers := teamTaskBlockersDisplay(dbCtx, t.plugin.taskStore, meta.BlockedBy)
		fmt.Fprintf(&sb, "%s | %s | %s | %s | %s\n",
			it.ID, taskStatusLabel(it.Status), owner, truncateTaskTitle(it.Title), blockers)
		if len(meta.WriteScopes) > 0 {
			fmt.Fprintf(&sb, "    写作用域：%s\n", teamTaskJoin(meta.WriteScopes))
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// teamTaskBlockersDisplay 渲染依赖列表（附依赖当前状态，便于判断是否可认领）。
// 读取单条依赖失败时只显示 ID，不影响任务板整体渲染。
func teamTaskBlockersDisplay(ctx context.Context, store *taskStore, blockedBy []string) string {
	if len(blockedBy) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(blockedBy))
	for _, raw := range blockedBy {
		depID := strings.TrimSpace(raw)
		if depID == "" {
			continue
		}
		label := depID
		dep, ok, err := store.Get(ctx, depID)
		switch {
		case err != nil:
			// 依赖状态读取失败：保留 ID 原样展示
		case !ok:
			label = depID + "[缺失]"
		default:
			label = depID + "[" + taskStatusLabel(dep.Status) + "]"
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "、")
}

// ---- team_task_claim ----

// teamTaskClaimParams team_task_claim 参数。
type teamTaskClaimParams struct {
	ID    string `json:"id" desc:"要认领的团队任务 ID（team_task_list 获取；也支持在当前会话内唯一匹配的 ID 前缀）"`
	Owner string `json:"owner" desc:"认领者名字（如团队成员名/角色），记录到任务板便于协作与校验"`
}

type teamTaskClaimTool struct {
	llmtool.BaseTool[teamTaskClaimParams]
	teamBoardToolBase
}

func (t *teamTaskClaimTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*teamTaskClaimParams)
	idOrPrefix := strings.TrimSpace(p.ID)
	if idOrPrefix == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	owner := strings.TrimSpace(p.Owner)
	if owner == "" {
		return "", fmt.Errorf("owner 不能为空（填写认领者名字，如团队成员名）")
	}
	if t.plugin.taskStore == nil {
		return teamBoardDisabledText, nil
	}
	key := t.sessionKey()

	t.plugin.teamBoardMu.Lock()
	defer t.plugin.teamBoardMu.Unlock()

	dbCtx, cancel := t.plugin.taskDB()
	defer cancel()

	task, meta, reason, err := t.plugin.resolveTeamBoardTask(dbCtx, key, idOrPrefix)
	if err != nil {
		return "", err
	}
	if reason != "" {
		return reason, nil
	}
	if task.SessionKey != key {
		return "只能认领本会话的团队任务", nil
	}
	if task.Kind != taskKindTeamTask {
		return fmt.Sprintf("任务 %s 不是团队任务（kind=%s），不能认领", task.ID, task.Kind), nil
	}
	switch task.Status {
	case taskStatusQueued:
		// 可认领
	case taskStatusRunning:
		return fmt.Sprintf("任务 %s 已被 %s 认领（进行中），不能重复认领", task.ID, teamTaskOwnerDisplay(meta.Owner)), nil
	default:
		return fmt.Sprintf("任务 %s 当前状态为「%s」，只有排队中（queued）的任务可以认领", task.ID, taskStatusLabel(task.Status)), nil
	}
	unmet, err := t.plugin.unmetTeamTaskBlockers(dbCtx, key, meta.BlockedBy)
	if err != nil {
		return "", err
	}
	if len(unmet) > 0 {
		return fmt.Sprintf("任务 %s 的依赖尚未全部完成：%s；请先完成依赖任务再认领", task.ID, strings.Join(unmet, "、")), nil
	}

	now := time.Now().Unix()
	task.Status = taskStatusRunning
	task.StartedAt = now
	meta.Owner = owner
	task.Meta = encodeTeamTaskMeta(meta)
	if err := t.plugin.taskStore.Update(dbCtx, task); err != nil {
		return "", fmt.Errorf("更新团队任务失败: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "任务 %s 认领成功（owner：%s）\n标题：%s", task.ID, owner, task.Title)
	if task.Prompt != "" {
		fmt.Fprintf(&sb, "\n描述：%s", task.Prompt)
	}
	if len(meta.WriteScopes) > 0 {
		fmt.Fprintf(&sb, "\n写作用域：%s（只改这些路径，与其他任务尽量不重叠）", teamTaskJoin(meta.WriteScopes))
	}
	if len(meta.BlockedBy) > 0 {
		fmt.Fprintf(&sb, "\n已满足的依赖：%s", teamTaskJoin(meta.BlockedBy))
	}
	sb.WriteString("\n完成后请调用 team_task_complete 记录结果，以解除后续任务的阻塞。")
	return sb.String(), nil
}

// teamTaskOwnerDisplay 负责人展示文案（空负责人给占位符）。
func teamTaskOwnerDisplay(owner string) string {
	if strings.TrimSpace(owner) == "" {
		return "未知成员"
	}
	return owner
}

// ---- team_task_complete ----

// teamTaskCompleteParams team_task_complete 参数。
type teamTaskCompleteParams struct {
	ID     string `json:"id" desc:"要完成的团队任务 ID（必须是已认领/进行中的任务）"`
	Result string `json:"result,omitempty" desc:"结果摘要：完成了什么、产出位置、验收结论（截断后记录到任务板）"`
	Owner  string `json:"owner,omitempty" desc:"认领者名字（可选）：填写时校验与认领者一致，防止他人代完成"`
}

type teamTaskCompleteTool struct {
	llmtool.BaseTool[teamTaskCompleteParams]
	teamBoardToolBase
}

func (t *teamTaskCompleteTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*teamTaskCompleteParams)
	idOrPrefix := strings.TrimSpace(p.ID)
	if idOrPrefix == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	owner := strings.TrimSpace(p.Owner)
	if t.plugin.taskStore == nil {
		return teamBoardDisabledText, nil
	}
	key := t.sessionKey()

	t.plugin.teamBoardMu.Lock()
	defer t.plugin.teamBoardMu.Unlock()

	dbCtx, cancel := t.plugin.taskDB()
	defer cancel()

	task, meta, reason, err := t.plugin.resolveTeamBoardTask(dbCtx, key, idOrPrefix)
	if err != nil {
		return "", err
	}
	if reason != "" {
		return reason, nil
	}
	if task.SessionKey != key {
		return "只能完成本会话的团队任务", nil
	}
	if task.Kind != taskKindTeamTask {
		return fmt.Sprintf("任务 %s 不是团队任务（kind=%s），不能按团队任务完成", task.ID, task.Kind), nil
	}
	if task.Status != taskStatusRunning {
		return fmt.Sprintf("任务 %s 当前状态为「%s」，只有已认领（进行中）的任务可以完成", task.ID, taskStatusLabel(task.Status)), nil
	}
	if owner != "" && meta.Owner != "" && owner != meta.Owner {
		return fmt.Sprintf("任务 %s 由 %s 认领，owner 不匹配（填写的是 %s），不能代完成", task.ID, meta.Owner, owner), nil
	}

	now := time.Now().Unix()
	task.Status = taskStatusCompleted
	task.StopReason = taskStopCompleted
	task.FinishedAt = now
	meta.Result = truncateTeamTaskResult(strings.TrimSpace(p.Result))
	task.Meta = encodeTeamTaskMeta(meta)
	if err := t.plugin.taskStore.Update(dbCtx, task); err != nil {
		return "", fmt.Errorf("更新团队任务失败: %w", err)
	}

	// 计算因此解除阻塞的后续任务；失败只影响提示信息，不影响已完成的事实
	ready, err := t.plugin.teamBoardUnblockedBy(dbCtx, key, task.ID)
	if err != nil {
		t.plugin.Logger.Warn("查询团队任务解除阻塞失败", "task", task.ID, "error", err.Error())
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "已完成团队任务 %s（%s）", task.ID, task.Title)
	if meta.Result != "" {
		fmt.Fprintf(&sb, "\n结果摘要已记录（%d 字）", len([]rune(meta.Result)))
	}
	if err != nil {
		sb.WriteString("\n（解除阻塞任务查询失败，可用 team_task_list 查看任务板）")
	} else if len(ready) > 0 {
		fmt.Fprintf(&sb, "\n因此解除阻塞、现在可认领的任务：%s", teamTaskJoin(ready))
	} else {
		sb.WriteString("\n没有其他任务因此解除阻塞。")
	}
	return sb.String(), nil
}

// ---- 内部辅助 ----

// parseTeamTaskMeta 解析任务板 Meta JSON；空串或解析失败按零值处理（不阻断
// 任务板读取：历史上可能存有旧格式/损坏的 Meta）。
func parseTeamTaskMeta(raw string) teamTaskMeta {
	var m teamTaskMeta
	if strings.TrimSpace(raw) == "" {
		return m
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return teamTaskMeta{}
	}
	return m
}

// encodeTeamTaskMeta 编码任务板 Meta JSON；该结构不可能编码失败，失败时返回空串。
func encodeTeamTaskMeta(m teamTaskMeta) string {
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// resolveTeamBoardTask 按完整 ID 或当前会话内唯一前缀解析团队任务。
// third 返回非空字符串表示「未找到 / 前缀不唯一」等可直接回给模型的原因，
// 此时不是系统错误（error 仅用于数据库故障）。
func (p *AIChatPlugin) resolveTeamBoardTask(ctx context.Context, key, idOrPrefix string) (*agentTask, teamTaskMeta, string, error) {
	task, ok, err := p.taskStore.Get(ctx, idOrPrefix)
	if err != nil {
		return nil, teamTaskMeta{}, "", fmt.Errorf("读取团队任务失败: %w", err)
	}
	if ok {
		return task, parseTeamTaskMeta(task.Meta), "", nil
	}
	rows, err := p.taskStore.ListBySession(ctx, key, teamBoardScanLimit)
	if err != nil {
		return nil, teamTaskMeta{}, "", fmt.Errorf("读取任务板失败: %w", err)
	}
	var matches []*agentTask
	for _, it := range rows {
		if it.Kind != taskKindTeamTask {
			continue
		}
		if strings.HasPrefix(it.ID, idOrPrefix) {
			matches = append(matches, it)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], parseTeamTaskMeta(matches[0].Meta), "", nil
	case 0:
		return nil, teamTaskMeta{}, fmt.Sprintf("未找到团队任务 %s（可用 team_task_list 查看任务板）", idOrPrefix), nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return nil, teamTaskMeta{}, fmt.Sprintf("ID 前缀 %s 匹配到多个团队任务，请使用完整 ID：%s", idOrPrefix, teamTaskJoin(ids)), nil
	}
}

// validateTeamTaskBlockers 校验 blocked_by：每个依赖必须已存在、属于同一会话、
// kind=team_task，且不能依赖自身（selfID 为新任务将要使用的 ID）。
func (p *AIChatPlugin) validateTeamTaskBlockers(ctx context.Context, key string, blockedBy []string, selfID string) error {
	for _, depID := range blockedBy {
		if selfID != "" && depID == selfID {
			return fmt.Errorf("任务不能依赖自身（blocked_by 含 %s）", depID)
		}
		dep, ok, err := p.taskStore.Get(ctx, depID)
		if err != nil {
			return fmt.Errorf("校验依赖任务 %s 失败: %w", depID, err)
		}
		if !ok {
			return fmt.Errorf("依赖任务 %s 不存在（blocked_by 必须引用已创建的团队任务 ID）", depID)
		}
		if dep.SessionKey != key {
			return fmt.Errorf("依赖任务 %s 不属于当前会话，不能跨会话依赖", depID)
		}
		if dep.Kind != taskKindTeamTask {
			return fmt.Errorf("依赖任务 %s 不是团队任务，不能作为 blocked_by 依赖", depID)
		}
	}
	return nil
}

// unmetTeamTaskBlockers 返回 blocked_by 中尚未完成的依赖（含可读状态标注）；
// 空切片表示全部依赖已满足。
func (p *AIChatPlugin) unmetTeamTaskBlockers(ctx context.Context, key string, blockedBy []string) ([]string, error) {
	var unmet []string
	for _, raw := range blockedBy {
		depID := strings.TrimSpace(raw)
		if depID == "" {
			continue
		}
		dep, ok, err := p.taskStore.Get(ctx, depID)
		if err != nil {
			return nil, fmt.Errorf("读取依赖任务 %s 失败: %w", depID, err)
		}
		switch {
		case !ok:
			unmet = append(unmet, depID+"（不存在）")
		case dep.SessionKey != key:
			unmet = append(unmet, depID+"（不属于本会话）")
		case dep.Kind != taskKindTeamTask:
			unmet = append(unmet, depID+"（不是团队任务）")
		case dep.Status != taskStatusCompleted:
			unmet = append(unmet, depID+"（"+taskStatusLabel(dep.Status)+"）")
		}
	}
	return unmet, nil
}

// teamBoardUnblockedBy 返回因 completedID 完成而「所有依赖都已满足」的排队
// 任务 ID（供完成工具提示后续可认领的任务，便于编排）。
func (p *AIChatPlugin) teamBoardUnblockedBy(ctx context.Context, key, completedID string) ([]string, error) {
	rows, err := p.taskStore.ListBySession(ctx, key, teamBoardScanLimit)
	if err != nil {
		return nil, fmt.Errorf("读取任务板失败: %w", err)
	}
	var ready []string
	for _, it := range rows {
		if it.Kind != taskKindTeamTask || it.ID == completedID || it.Status != taskStatusQueued {
			continue
		}
		meta := parseTeamTaskMeta(it.Meta)
		if len(meta.BlockedBy) == 0 {
			continue
		}
		dependsOnCompleted := false
		allCompleted := true
		for _, raw := range meta.BlockedBy {
			depID := strings.TrimSpace(raw)
			if depID == "" {
				continue
			}
			if depID == completedID {
				dependsOnCompleted = true
			}
			dep, ok, err := p.taskStore.Get(ctx, depID)
			if err != nil {
				return nil, fmt.Errorf("读取依赖任务 %s 失败: %w", depID, err)
			}
			if !ok || dep.Kind != taskKindTeamTask || dep.Status != taskStatusCompleted {
				allCompleted = false
			}
		}
		if dependsOnCompleted && allCompleted {
			ready = append(ready, it.ID)
		}
	}
	return ready, nil
}

// teamTaskNormalizeList 规整 ID/写作用域列表：去空白、丢弃空项、保序去重。
func teamTaskNormalizeList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// teamTaskJoin 用中文顿号连接列表；空列表返回占位符。
func teamTaskJoin(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, "、")
}

// truncateTeamTaskResult 任务板结果入库前的 rune 安全截断（上限 4000）。
func truncateTeamTaskResult(s string) string {
	r := []rune(s)
	if len(r) <= teamTaskResultMaxRunes {
		return s
	}
	return string(r[:teamTaskResultMaxRunes]) + "\n…（结果已截断）"
}
