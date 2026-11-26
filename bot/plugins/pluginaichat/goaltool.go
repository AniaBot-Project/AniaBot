package pluginaichat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 目标（Goal）工具与续跑驱动：让长任务跨回合自动推进。
//
// 工作方式：
//   - AI 用 goal_create 建立目标（objective + 轮数上限），goal_update 标记
//     complete/blocked/pause/resume；
//   - 每个回合正常结束后，运行器调用 maybeDriveGoal：若目标 active 且已武装
//     （本进程内由 goal_create 或用户 /goal resume 武装）、且没有排队消息与运行中
//     子代理，则 rounds_started+1 并把 <goal_round> 续跑指令入队，开启下一轮；
//   - 达到轮数上限自动 block；进程重启后 goalArmed 清空（需用户 /goal resume），
//     避免升级重启后自动烧 token。
//
// 并发约定：目标状态的「读-改-写」一律在 goalMu 内完成。命令（框架事件协程）、
// goal 工具（回合协程）与续跑驱动（后台协程）会并发操作同一会话的目标行，
// 不加锁时一次迟到的写会经 Upsert 的 INSERT 兜底复活已清除的目标。

// goalToolBase 目标工具共享插件引用与会话信息。
type goalToolBase struct {
	plugin  *AIChatPlugin
	id      message.QID
	isGroup bool
}

func (b goalToolBase) sessionKey() string { return sessionKey(b.id, b.isGroup) }

// newGoalTools 创建目标工具（仅主会话注册）。
func newGoalTools(p *AIChatPlugin, id message.QID, isGroup bool) []llmtool.Tool {
	base := goalToolBase{plugin: p, id: id, isGroup: isGroup}
	return []llmtool.Tool{
		&goalCreateTool{
			BaseTool:     llmtool.MakeBaseTool("goal_create", goalCreateDesc, goalCreateParams{}),
			goalToolBase: base,
		},
		&goalUpdateTool{
			BaseTool:     llmtool.MakeBaseTool("goal_update", goalUpdateDesc, goalUpdateParams{}),
			goalToolBase: base,
		},
		&goalStatusTool{
			BaseTool:     llmtool.MakeBaseTool("goal_status", "查看当前会话的长任务目标：目标内容、状态（active/paused/blocked/complete）、已续跑轮数与上限。", goalStatusParams{}),
			goalToolBase: base,
		},
	}
}

const goalCreateDesc = "为一个需要跨多轮持续推进的长任务建立持久化目标。适用场景：用户提出一个一轮做不完的长期目标（迁移、持续调研、多阶段改造），" +
	"且可以分多轮自动推进。建立后，每当本轮工作结束（且没有排队消息/运行中的子代理），系统会自动开启下一轮继续推进，直到你调用 goal_update action=complete、" +
	"达到轮数上限、或用户暂停。单轮可完成的任务不要建目标。目标存在期间，每轮结束前请判断：目标是否真正达成？未达成则保持 active 留给下一轮。"

const goalUpdateDesc = "更新当前会话目标的状态：complete=目标已达成（必须已有完成证据）；blocked=遇到无法自行解决的阻塞（必须说明原因，会停止自动续跑）；" +
	"pause=暂停自动续跑；resume=恢复已暂停/阻塞且还有轮数余额的目标；clear=清除目标记录。完成或阻塞后请给用户写一句收尾说明。"

// ---- goal_create ----

type goalCreateParams struct {
	Objective string `json:"objective" desc:"完整目标：要把什么做到什么程度、验收标准是什么（后续每轮自动续跑都会看到它）"`
	MaxRounds int    `json:"max_rounds,omitempty" desc:"最多自动续跑轮数（1~500），不填用插件配置 goal.max_rounds（默认 64）"`
}

type goalCreateTool struct {
	llmtool.BaseTool[goalCreateParams]
	goalToolBase
}

func (t *goalCreateTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*goalCreateParams)
	objective := strings.TrimSpace(p.Objective)
	if objective == "" {
		return "", fmt.Errorf("objective 不能为空")
	}
	if t.plugin.goalStore == nil {
		return "目标持久化未启用（缺少 SQL 存储），无法建立目标", nil
	}
	if !t.plugin.cfg.Goal.Enable {
		return "目标续跑功能已在配置中关闭（plugin.ai_chat_bot.goal.enable=false）", nil
	}
	key := t.sessionKey()
	// 目标的「查重 + 写入」必须在 goalMu 内完成：/goal clear 或 /goal resume 可能
	// 在框架事件协程并发执行，否则会出现重复建立目标或写入被清除打断的中间态。
	t.plugin.goalMu.Lock()
	defer t.plugin.goalMu.Unlock()
	if cur, ok, err := t.plugin.loadGoal(key); err != nil {
		return "", err
	} else if ok && cur.Phase != goalPhaseComplete {
		return fmt.Sprintf("当前会话已有进行中的目标（状态 %s），请先完成/清除它再建立新目标：%s", cur.Phase, cur.Objective), nil
	}
	maxRounds := p.MaxRounds
	if maxRounds <= 0 {
		maxRounds = t.plugin.cfg.Goal.MaxRounds
	}
	if maxRounds <= 0 {
		maxRounds = goalDefaultMaxRounds
	}
	if maxRounds > goalMaxRoundsLimit {
		maxRounds = goalMaxRoundsLimit
	}
	now := time.Now().Unix()
	g := &agentGoal{
		SessionKey: key,
		Objective:  objective,
		Phase:      goalPhaseActive,
		MaxRounds:  maxRounds,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := t.plugin.saveGoal(g); err != nil {
		return "", fmt.Errorf("保存目标失败: %w", err)
	}
	t.plugin.armGoal(key)
	return fmt.Sprintf("已建立长任务目标（最多自动续跑 %d 轮），本轮结束后会继续推进；完成后请调用 goal_update action=complete。", maxRounds), nil
}

// ---- goal_update ----

type goalUpdateParams struct {
	Action   string `json:"action" desc:"动作：complete（已完成）/ blocked（无法继续）/ pause（暂停）/ resume（恢复）/ clear（清除）"`
	Reason   string `json:"reason,omitempty" desc:"blocked 时必填：具体阻塞原因与已尝试的办法；complete 时建议填完成证据摘要"`
	Evidence string `json:"evidence,omitempty" desc:"complete 时的完成证据（检查结果/产出位置）；会写入目标记录"`
}

type goalUpdateTool struct {
	llmtool.BaseTool[goalUpdateParams]
	goalToolBase
}

func (t *goalUpdateTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	p := params.(*goalUpdateParams)
	action := strings.ToLower(strings.TrimSpace(p.Action))
	key := t.sessionKey()
	if action == "clear" {
		if t.plugin.goalStore == nil {
			return "目标持久化未启用", nil
		}
		// 与 /goal clear、续跑驱动串行：清除是「删除 + 解除武装」的原子动作。
		t.plugin.goalMu.Lock()
		defer t.plugin.goalMu.Unlock()
		if err := t.plugin.clearGoal(key); err != nil {
			return "", err
		}
		t.plugin.disarmGoal(key)
		return "已清除当前目标", nil
	}
	// 后续所有分支都是「读-改-写」：锁定后其他协程（/goal 命令、续跑驱动）不会
	// 在读到目标与写回之间清除/暂停它，避免用旧快照覆盖新状态。
	t.plugin.goalMu.Lock()
	defer t.plugin.goalMu.Unlock()
	g, ok, err := t.plugin.loadGoal(key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "当前会话没有目标，先用 goal_create 建立", nil
	}
	switch action {
	case "complete":
		g.Phase = goalPhaseComplete
		g.BlockedCode = ""
		g.BlockedMessage = ""
		if reason := strings.TrimSpace(p.Evidence); reason != "" {
			g.BlockedMessage = reason
		} else if reason := strings.TrimSpace(p.Reason); reason != "" {
			g.BlockedMessage = reason
		}
		t.plugin.disarmGoal(key)
	case "blocked":
		reason := strings.TrimSpace(p.Reason)
		if reason == "" {
			return "", fmt.Errorf("blocked 需要说明具体阻塞原因（reason）")
		}
		g.Phase = goalPhaseBlocked
		g.BlockedCode = "agent-blocked"
		g.BlockedMessage = truncateTaskTitle(reason)
		t.plugin.disarmGoal(key)
	case "pause":
		g.Phase = goalPhasePaused
		t.plugin.disarmGoal(key)
	case "resume":
		if g.Phase == goalPhaseComplete {
			return "目标已完成，不能 resume；如需新目标请 goal_create", nil
		}
		if g.RoundsStarted >= g.MaxRounds {
			return fmt.Sprintf("目标已用满 %d 轮，不能 resume；可调整目标或重新建立", g.MaxRounds), nil
		}
		g.Phase = goalPhaseActive
		g.BlockedCode = ""
		g.BlockedMessage = ""
		t.plugin.armGoal(key)
	default:
		return "", fmt.Errorf("未知 action %q（可选 complete/blocked/pause/resume/clear）", action)
	}
	if err := t.plugin.saveGoal(g); err != nil {
		return "", err
	}
	return fmt.Sprintf("目标状态已更新为 %s（已续跑 %d/%d 轮）", g.Phase, g.RoundsStarted, g.MaxRounds), nil
}

// ---- goal_status ----

type goalStatusParams struct{}

type goalStatusTool struct {
	llmtool.BaseTool[goalStatusParams]
	goalToolBase
}

func (t *goalStatusTool) Execute(ctx context.Context, params any, callbacks llmtool.CallBackFuncs) (string, error) {
	return t.plugin.renderGoalStatus(t.sessionKey()), nil
}

// renderGoalStatus 渲染目标状态文本。
func (p *AIChatPlugin) renderGoalStatus(key string) string {
	if p.goalStore == nil {
		return "目标持久化未启用（缺少 SQL 存储）"
	}
	g, ok, err := p.loadGoal(key)
	if err != nil {
		return "读取目标失败：" + err.Error()
	}
	if !ok {
		return "当前会话没有长任务目标。需要跨多轮推进时用 goal_create 建立。"
	}
	armed := p.goalIsArmed(key)
	var sb strings.Builder
	sb.WriteString("【长任务目标】\n")
	fmt.Fprintf(&sb, "目标：%s\n", truncateTaskPrompt(g.Objective))
	fmt.Fprintf(&sb, "状态：%s（已续跑 %d/%d 轮）\n", goalPhaseLabel(g.Phase), g.RoundsStarted, g.MaxRounds)
	if g.BlockedCode != "" {
		fmt.Fprintf(&sb, "阻塞原因：%s\n", g.BlockedMessage)
	}
	if g.Phase == goalPhaseActive && !armed {
		sb.WriteString("续跑：已暂停（进程重启后需发送 /goal resume 重新开启自动续跑）\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// goalPhaseLabel 目标状态的中文展示。
func goalPhaseLabel(phase string) string {
	switch phase {
	case goalPhaseActive:
		return "active（推进中）"
	case goalPhasePaused:
		return "paused（已暂停）"
	case goalPhaseBlocked:
		return "blocked（被阻塞）"
	case goalPhaseComplete:
		return "complete（已完成）"
	default:
		return phase
	}
}

// ---- /goal 命令 ----

// handleGoalCommand 处理 /goal [status|pause|resume|clear]：
// 用户侧控制目标自动续跑；群聊需 @ 机器人（与其它指令一致）。
func (p *AIChatPlugin) handleGoalCommand(b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	id, isGroup := messageTarget(msg)
	key := sessionKey(id, isGroup)
	action := ""
	if len(cmd.Args) > 0 {
		action = strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	}
	if p.goalStore == nil {
		p.sendPlainText(b, id, isGroup, "目标持久化未启用（缺少 SQL 存储）")
		return false, nil
	}
	if action == "" || action == "status" {
		p.sendPlainText(b, id, isGroup, p.renderGoalStatus(key))
		return false, nil
	}
	res := p.applyGoalCommand(key, action)
	p.sendPlainText(b, id, isGroup, res.text)
	if res.drive {
		// 立即尝试推进一轮（会话空闲时）；maybeDriveGoal 会在 goalMu 内重新
		// 校验目标状态，即使这里与 /goal clear 交错也不会复活已清除的目标。
		b.Go("goal-drive:"+key, func() { p.maybeDriveGoal(b, id, isGroup) })
	}
	return false, nil
}

// goalCommandResult /goal 状态迁移的结果：要回复用户的文本，以及迁移完成后
// 是否需要在会话空闲时立即推进一轮（resume）。
type goalCommandResult struct {
	text  string
	drive bool
}

// applyGoalCommand 在 goalMu 内完成 pause/resume/clear 的状态迁移并返回要发送
// 的文本，发送由调用方在锁外做（避免慢平台发送阻塞其他会话的目标操作）。
//
// 加锁的必要性：/goal 命令跑在框架事件协程，续跑驱动跑在后台协程。若清除与
// 续跑驱动的「读-改-写」交错，驱动那次基于旧快照的 saveGoal 会在行已被删除后
// 走 Upsert 的 INSERT 兜底，把目标重新插回去（/goal clear 后目标又出现，且
// 续跑指令已入队）。整段迁移串行化后，后拿到锁的一方一定能看到前一方写下的
// 最终状态：清除先行则驱动看到未武装/无行而放弃；驱动先行则它推进的那一轮
// 随后被清除整体撤销。
func (p *AIChatPlugin) applyGoalCommand(key, action string) goalCommandResult {
	p.goalMu.Lock()
	defer p.goalMu.Unlock()

	g, ok, err := p.loadGoal(key)
	if err != nil {
		return goalCommandResult{text: "读取目标失败：" + err.Error()}
	}
	if !ok {
		return goalCommandResult{text: "当前会话没有长任务目标"}
	}
	switch action {
	case "pause":
		g.Phase = goalPhasePaused
		if err := p.saveGoal(g); err != nil {
			return goalCommandResult{text: "暂停目标失败：" + err.Error()}
		}
		p.disarmGoal(key)
		return goalCommandResult{text: "目标已暂停自动续跑（发送 /goal resume 恢复）"}
	case "resume":
		if g.Phase == goalPhaseComplete {
			return goalCommandResult{text: "目标已完成，不能恢复；如需新目标请让 AI 用 goal_create 建立"}
		}
		if g.RoundsStarted >= g.MaxRounds {
			return goalCommandResult{text: fmt.Sprintf("目标已用满 %d 轮，无法恢复", g.MaxRounds)}
		}
		g.Phase = goalPhaseActive
		g.BlockedCode = ""
		g.BlockedMessage = ""
		if err := p.saveGoal(g); err != nil {
			return goalCommandResult{text: "恢复目标失败：" + err.Error()}
		}
		p.armGoal(key)
		return goalCommandResult{text: "目标已恢复自动续跑", drive: true}
	case "clear":
		if err := p.clearGoal(key); err != nil {
			return goalCommandResult{text: "清除目标失败：" + err.Error()}
		}
		p.disarmGoal(key)
		return goalCommandResult{text: "目标已清除"}
	}
	return goalCommandResult{text: "用法：/goal [status|pause|resume|clear]"}
}

// ---- 存储与武装状态 ----

// loadGoal 读取会话目标（goalStore 未启用时返回不存在）。
func (p *AIChatPlugin) loadGoal(key string) (*agentGoal, bool, error) {
	if p.goalStore == nil {
		return nil, false, nil
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	return p.goalStore.Get(ctx, key)
}

// saveGoal 写入会话目标。
func (p *AIChatPlugin) saveGoal(g *agentGoal) error {
	if p.goalStore == nil {
		return fmt.Errorf("目标存储未启用")
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	return p.goalStore.Upsert(ctx, g)
}

// clearGoal 删除会话目标。
func (p *AIChatPlugin) clearGoal(key string) error {
	if p.goalStore == nil {
		return nil
	}
	ctx, cancel := p.taskDB()
	defer cancel()
	return p.goalStore.Clear(ctx, key)
}

// armGoal / disarmGoal / goalArmed 进程内续跑武装状态（不持久化：重启后需用户
// /goal resume，避免升级重启后自动烧 token）。
func (p *AIChatPlugin) armGoal(key string)    { p.goalArmed.Store(key, struct{}{}) }
func (p *AIChatPlugin) disarmGoal(key string) { p.goalArmed.Delete(key) }
func (p *AIChatPlugin) goalIsArmed(key string) bool {
	_, ok := p.goalArmed.Load(key)
	return ok
}

// goalRoundMessage 构造自动续跑指令（作为一条合成消息入队，开启新一轮回合）。
func goalRoundMessage(g *agentGoal) string {
	return fmt.Sprintf("<goal_round>第 %d/%d 轮：继续在当前会话推进目标。\n目标：%s\n"+
		"要求：把当前会话、工具结果与文件系统当作权威，先核对现状再继续，不要重复已完成的工作；"+
		"本轮要做出具体进展并验证结果。如果目标已真正达成，调用 goal_update action=complete 并附完成证据；"+
		"如果还有剩余工作，保持目标为 active（不要调用 complete），系统会继续下一轮；"+
		"如果遇到无法自行解决的阻塞，调用 goal_update action=blocked 说明原因。不要向用户重复询问已经明确的信息。</goal_round>",
		g.RoundsStarted, g.MaxRounds, g.Objective)
}

// maybeDriveGoal 回合正常结束后的续跑驱动：满足条件时把下一轮 <goal_round>
// 入队并启动运行器。以下情况不续跑：功能关闭/未武装/有排队消息/有运行中的
// 子代理（等它们收敛）/目标非 active/轮数用尽。
func (p *AIChatPlugin) maybeDriveGoal(b bot.Bot, id message.QID, isGroup bool) {
	if p.goalStore == nil || !p.cfg.Goal.Enable {
		return
	}
	key := sessionKey(id, isGroup)
	// 未武装是绝大多数会话的常态：先做无锁快速判断，避免每个回合结束都去
	// 查子代理/排队队列。真正的判定在 advanceGoalRound 的锁内重新做。
	if !p.goalIsArmed(key) {
		return
	}
	if p.hasPending(key) || len(p.listRunningSubagents(id, isGroup)) > 0 {
		return
	}
	g, notice := p.advanceGoalRound(key)
	if notice != "" {
		p.sendPlainText(b, id, isGroup, notice)
		return
	}
	if g == nil {
		return
	}
	synthetic := message.Message{
		Sender:  message.MessageSender{Nickname: "目标续跑", UserId: message.FromUint64(0)},
		Message: []message.OB11Segment{{Type: message.SegmentText, Data: map[string]any{"text": goalRoundMessage(g)}}},
	}
	if _, ok := p.enqueuePending(id, isGroup, synthetic); !ok {
		p.Logger.Warn("目标续跑消息入队失败（队列已满）", "session", key)
		return
	}
	p.Logger.Info("目标续跑：开启下一轮", "session", key, "round", g.RoundsStarted, "max", g.MaxRounds)
	// 用新的 goroutine 接管，避免在运行器协程内递归加深调用栈
	b.Go("goal-drive:"+key, func() { p.runQueued(b, id, isGroup) })
}

// advanceGoalRound 在 goalMu 内完成「校验武装 → 读目标 → 判断可续跑性 → 推进
// 一轮或标记轮数上限」。返回 nil 表示本次不续跑；notice 非空表示需要通知用户
// （达到轮数上限自动阻塞，通知由调用方在锁外发送）。
//
// 这里必须与 /goal clear|pause、/stop 的暂停、goal 工具的状态迁移共用 goalMu：
// 它们都在别的协程里执行，若读与写之间被插入一次 DELETE，写回就会经 Upsert 的
// INSERT 兜底把目标复活。
func (p *AIChatPlugin) advanceGoalRound(key string) (*agentGoal, string) {
	p.goalMu.Lock()
	defer p.goalMu.Unlock()

	if !p.goalIsArmed(key) {
		return nil, ""
	}
	g, ok, err := p.loadGoal(key)
	if err != nil || !ok {
		return nil, ""
	}
	if g.Phase != goalPhaseActive {
		p.disarmGoal(key)
		return nil, ""
	}
	if g.RoundsStarted >= g.MaxRounds {
		g.Phase = goalPhaseBlocked
		g.BlockedCode = goalBlockRoundLimit
		g.BlockedMessage = fmt.Sprintf("已达到最大自动续跑轮数（%d）", g.MaxRounds)
		if err := p.saveGoal(g); err != nil {
			p.Logger.Warn("目标达到轮数上限后写库失败", "session", key, "error", err.Error())
		}
		p.disarmGoal(key)
		return nil, fmt.Sprintf("目标已达到最大自动续跑轮数（%d），自动续跑已停止。可发送 /goal resume 或 /goal clear，或让 AI 调整目标。", g.MaxRounds)
	}
	g.RoundsStarted++
	if err := p.saveGoal(g); err != nil {
		p.Logger.Warn("更新目标轮数失败，停止续跑", "session", key, "error", err.Error())
		p.disarmGoal(key)
		return nil, ""
	}
	return g, ""
}

// pauseGoalOnStop 用户 /stop 停止会话时暂停目标自动续跑（与 DSH 语义一致：
// 用户显式停止不应被自动续跑立刻顶回来）。
func (p *AIChatPlugin) pauseGoalOnStop(id message.QID, isGroup bool) {
	if p.goalStore == nil || !p.cfg.Goal.Enable {
		return
	}
	key := sessionKey(id, isGroup)
	if !p.goalIsArmed(key) {
		return
	}
	// 与 /goal 命令、续跑驱动共用 goalMu：显式停止的暂停不能被迟到的续跑写覆盖。
	p.goalMu.Lock()
	defer p.goalMu.Unlock()
	p.disarmGoal(key)
	g, ok, err := p.loadGoal(key)
	if err != nil || !ok || g.Phase != goalPhaseActive {
		return
	}
	g.Phase = goalPhasePaused
	if err := p.saveGoal(g); err != nil {
		p.Logger.Warn("暂停目标失败", "session", key, "error", err.Error())
		return
	}
	p.Logger.Info("用户停止会话，目标自动续跑已暂停", "session", key)
}
