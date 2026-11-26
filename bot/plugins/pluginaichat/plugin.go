package pluginaichat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/computeruse"
	"github.com/jeanhua/AniaBot/bot/component/functool"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/bot/utils"
	"github.com/jeanhua/AniaBot/common/aniaerror"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/jeanhua/AniaBot/common/storage"
	"github.com/spf13/viper"
)

type AIChatPlugin struct {
	plugin.Meta
	// cfg 插件配置，由框架在 Start 前自动填充（见 ConfigSchema）
	cfg   aiChatConfig
	chats sync.Map

	// customHeaders 自定义 LLM 请求头（plugin.ai_chat_bot.headers 的解析结果，
	// Start 时解析一次）；nil 表示未配置。经 llmClientOptions 附加到全部 LLM
	// 客户端（主对话/子代理/定时任务/压缩器/备用识图/备用模型）
	customHeaders map[string]string
	// sessionTokens 会话键 → 会话实例 token（${session} 占位符取值）：每个会话
	// 唯一，新建会话（#新对话 / 自动清空历史 / 面板新对话）后换新，进程生命周期内
	// 稳定（会话对象被回收重建时沿用）。见 headers.go
	sessionTokens sync.Map

	// historyDB 对话历史的 SQL 后端连接（Start 时探测建表成功才赋值）；
	// nil 表示历史不持久化（仅内存窗口）
	historyDB *sql.DB

	lockStorage storage.Storage
	rateCh      chan struct{}

	// ready 插件是否完成完整初始化（AI 必需配置 base_url/api_key/model 齐备且
	// 各组件构造成功）。未就绪时消息事件直接跳过：未配置 API 不算错误，插件
	// 以降载模式运行（面板/配置页仍可用），避免空指针与反复刷错误日志
	ready bool

	activeContexts sync.Map

	// asyncSubagents 异步子代理管理，按会话（群/好友）隔离
	asyncSubagents sync.Map

	// pendingMsgs AI 响应期间到达的消息排队队列，按会话（群/好友）隔离
	pendingMsgs sync.Map

	// sessionBots 会话 → 最近使用的平台外观（事件来源 bot.Bot），供后台兜底
	// 清扫器在没有事件来源时构造发送回调；独立于 chats 存放，
	// 避免与事件线程并发写同一结构体字段
	sessionBots sync.Map

	// noMentionCount 未 @ 消息计数；noMentionMu 保护其读改写（sync.Map 的
	// Load+Store 组合非原子，并发消息会丢计数）。计数跨重启持久化（见
	// nomention.go），避免重启清零导致「30 条未@自动清空」永远无法触发
	noMentionCount sync.Map
	noMentionMu    sync.Mutex
	// noMentionStore 未@消息计数的持久化存储（nomention: 子命名空间），
	// 用于跨重启累计；为 nil 表示功能未启用或持久层不可用（退回纯内存计数）
	noMentionStore storage.PersistentStorage
	// noMentionLoaded 已从持久层恢复过未@计数的群（每个群只查一次 DB）
	noMentionLoaded sync.Map

	// promptManager 群聊/好友 Prompt 覆盖管理器（files.prompt_json，面板修改后
	// TTL 热重读，数秒内生效）；Start 时始终构造（配置中心不可用时仅内存快照）
	promptManager *promptOverrideManager

	// ocrModel 备用图片识别模型；为 nil 表示未启用或初始化失败
	ocrModel *aichat.ChatBot

	mcpConfigs   []*llmtool.MCPConfig
	toolExecutor *llmtool.ToolExecuter
	// toolResultLimiter 工具结果长度治理：超长成功结果落盘，回填预览+handle，
	// AI 用 read_tool_output 按需读取。Start 时构造，始终非 nil（未启用落盘时
	// 退化为头尾截断）。
	toolResultLimiter *toolResultLimiter

	skillManager *llmtool.SkillManager
	// skillsDir skill 所在目录（供面板管理接口使用）
	skillsDir string

	// clockManager AI 定时任务调度器；为 nil 表示功能未启用
	clockManager *clockManager

	// proactiveManager 主动互动调度器（意图队列）；为 nil 表示功能未启用
	proactiveManager *proactiveManager

	// memoryManager 长期记忆管理器；为 nil 表示功能未启用
	memoryManager *memoryManager

	// knowledgeManager 知识库管理器；为 nil 表示功能未启用
	knowledgeManager *knowledgeManager

	// embedder 知识库与记忆共享的语义向量计算器；为 nil 表示向量检索未启用。
	// 注入路径用它对每条用户消息预算一次查询向量（EmbedOneCached），
	// 复用于知识库与记忆的自动注入。
	embedder *embedder

	// teamManager Agent 团队管理器（功能固定启用）；为 nil 表示持久化存储不可用
	teamManager *teamManager

	// queryLogger Query 日志记录器（面板「Query 日志」页数据源）；为 nil 表示功能未启用
	queryLogger *querylog.Logger

	// recentTurns 本进程内最近完成的回合摘要（/tasks 展示用）。普通回合不再
	// 写入任务表（避免与 Query 日志重复），完整明细与持久记录以 Query 日志为准。
	recentTurns *recentTurnLog

	// extraUsage 会话级派生 LLM 用量暂存（异步子代理 / team_run 成员 / 备用图片
	// 识别等主请求循环之外的消耗），finishQuery 时并入该会话的 Query 日志；
	// 键为 sessionKey（见 usageacc.go）
	extraUsage sync.Map

	// quotaManager 每日 Token 配额管理器；为 nil 表示功能未启用
	quotaManager *quotaManager

	// hookManager AI 钩子管理器（shell 钩子 + 其他插件注册的 Go 钩子）；
	// Start 时始终构造，未启用（hooks.enable=false）时 Run 内部短路
	hookManager *agenthook.Manager
	// goHookHandlers core 收集的其他插件 Go 钩子（SetGoHookHandlers 注入，
	// 时序上在 Start 之后，防御性兜底：manager 未就绪时暂存）
	goHookHandlers []agenthook.Handler
	// sessionInject SessionStart 钩子产出的待注入上下文（按会话暂存，
	// 下一轮对话消费一次后删除）
	sessionInject sync.Map

	// planManager 计划模式状态（/plan on 开启后副作用工具被门禁阻断）；
	// Start 时始终构造（无开关：不主动开启即为纯内存空状态）
	planManager *planManager
	// commandManager 自定义斜杠命令（files.commands_json，面板可编辑）；
	// Start 时始终构造，命中后消息改写为展开模板走正常对话流程
	commandManager *commandManager
	// todoManager 任务清单（todo_write 工具，内存态按会话隔离）；
	// Start 时始终构造，cfg.Todo.Enable 控制是否注册工具与注入提醒
	todoManager *todoManager
	// approvalManager 工具审批管理器；为 nil 表示功能未启用
	approvalManager *approvalManager

	// platformSideEffects 平台注入工具中声明了副作用的工具名集合
	//（aitool.SideEffector，注册时收集）：计划模式门禁的动态腿，与
	// planBlockedTools 静态清单同等阻断；键为工具名
	platformSideEffects sync.Map

	// historyTools 各会话实际注册的历史消息工具名（平台前缀版本，如
	// qq_get_msg_history，见 registerPlatformTools）；「定时任务播报」提示
	// 据此给出该平台准确的工具名。键为 sessionKey
	historyTools sync.Map

	// msgEventTimeout 框架级消息处理超时（bot.msg_event_timeout_sec，Start 时
	// 从全量 viper 读取）：仅供历史遗留路径（面板试聊室等）兜底；AI 回合不再使用
	// 该预算，改由 turnSoftTimeout / turnHardTimeout 与本包看门狗管理（见 runner.go）。
	msgEventTimeout time.Duration

	// runCtx 插件生命周期上下文：回合运行器与后台任务继承它，OnUnload 时取消，
	// 确保进程重启/插件卸载能中止在跑的后台工作（b.Go 本身不接 ctx）。
	runCtx    context.Context
	runCancel context.CancelFunc

	// turnSoftTimeout 回合软截止：到时向模型注入一次"收尾或转后台"提醒，不取消；0=关闭
	turnSoftTimeout time.Duration
	// turnHardTimeout 回合硬截止：到时取消当前回合；0=不限制
	turnHardTimeout time.Duration

	// subagentSem 后台子代理全局并发信号量（容量来自 subagent.max_concurrent_global，
	// nil 表示不限制）；对话回合使用 rateCh，两者互不挤占
	subagentSem chan struct{}

	// taskStore 后台任务注册表（SQL 持久化）；historyDB 不可用时为 nil，
	// 任务只保留内存态（/tasks 仍可看当前运行中的子代理）
	taskStore *taskStore

	// goalStore 长任务目标存储（SQL 持久化）；nil 表示功能未启用或持久层不可用
	goalStore *goalStore
	// goalArmed 进程内「目标续跑已武装」状态（key=sessionKey）；不持久化，
	// 进程重启后清空，需用户 /goal resume 重新开启（避免升级重启后自动烧 token）
	goalArmed sync.Map

	// teamBoardMu 团队任务板的进程内互斥（claim/complete 的原子性；多实例部署
	// 时以 DB 为准，本进程内串行足够避免同一任务被并发认领）
	teamBoardMu sync.Mutex

	// chatConsole 面板「试聊室」会话管理器（Web 面板内直接对话测试）；
	// Start 末尾构造，未就绪时为 nil（面板接口退化为明确错误）
	chatConsole *chatConsoleManager
}

const (
	LockExpTime     = time.Minute * 10
	promptConfigKey = "files.prompt_json"
	// hooksConfigKey / commandsConfigKey 与 configstore.KeyHooksJSON/KeyCommandsJSON
	// 同值；按 promptConfigKey 先例在插件本地定义，避免 plugins → core 依赖
	hooksConfigKey    = "files.hooks_json"
	commandsConfigKey = "files.commands_json"

	// sessionMaxIdleMinutes 闲置回收时长上限（分钟）：一年。配置极大值会让
	// time.Duration 乘法溢出为负，导致闲置淘汰静默失效（见 Start）。
	sessionMaxIdleMinutes = 365 * 24 * 60
)

func NewAIChatPlugin() *AIChatPlugin {
	return &AIChatPlugin{
		Meta: plugin.Meta{
			Name:      "AI对话插件",
			HelpWords: "@我聊天哦，带上 #新对话 标签可以创建新对话，发送 /stop 可以停止 AI 响应（/stop 任务ID 取消指定后台任务），/tasks 查看后台任务，/goal 查看长任务目标，/status 查看当前响应状态",
			Order:     plugin.LevelPostHandle,
			ShowFor:   plugininfo.ShowForGroup | plugininfo.ShowForFriend,
			Author:    "jeanhua",
			Version:   "1.1.0",
		},
	}
}

func (p *AIChatPlugin) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	// 未完成初始化（如未配置 API KEY）时跳过全部处理，交由后续插件
	if !p.ready {
		return true, nil
	}
	// 主动互动：记录现场要覆盖全部群消息，所以放在 mention 门之前；
	// /proactive 指令本身放在门之后（群聊指令统一要求 @ 机器人）。
	if p.proactiveManager != nil {
		p.proactiveManager.noteMessage(msg, true)
	}
	// 工具审批回复必须最先拦截：审批等待期间会话锁被占用（回复走不到正常聊天
	// 流程），且回复通常不带 @（mention 门会把它挡掉）
	if p.approvalManager != nil {
		text, _ := utils.ExtraMessageStr(msg)
		if consumed, hint := p.approvalManager.tryHandleReply(msg.GroupId, true, msg.Sender.UserId, text); consumed {
			if hint != "" {
				p.sendPlainText(bot, msg.GroupId, true, hint)
			}
			return false, nil
		}
	}

	if !cmd.Mention {
		// 计数跨重启持久化；达到阈值后尝试清空该群对话历史（含持久化），
		// 下次 @ 即开新对话。阈值可在面板「AI 对话 · 会话驻留」配置，0 关闭
		threshold := p.cfg.Session.NoMentionClear
		if threshold > 0 && p.incrNoMention(msg.GroupId) >= threshold {
			// 会话未驻留（重启后尚未创建、被闲置回收淘汰）或 AI 正在响应拿不到锁时
			// 返回 false：保留计数，由下次 @ 或会话重建后的 applyNoMentionClear 补清
			if p.tryClearNoMentionChat(ctx, msg.GroupId) {
				p.resetNoMention(msg.GroupId)
			}
		}
		return true, nil
	}

	// 群聊指令统一要求 @ 机器人（与 /stop、/plan 及其他插件一致），
	// 不 @ 的 /proactive、/clock 会被上面的 mention 门当成普通消息拦下。
	if cmd.Name == "proactive" {
		return p.handleProactiveCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "clock" {
		return p.handleClockCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "tasks" {
		return p.handleTasksCommand(bot, msg)
	}

	if cmd.Name == "resume" {
		taskID := ""
		if len(cmd.Args) > 0 {
			taskID = strings.TrimSpace(cmd.Args[0])
		}
		return p.handleResumeCommand(bot, msg, taskID)
	}

	if cmd.Name == "goal" {
		return p.handleGoalCommand(bot, cmd, msg)
	}

	if cmd.Name == "stop" {
		if len(cmd.Args) > 0 {
			return p.handleStopByID(bot, msg, strings.TrimSpace(cmd.Args[0]))
		}
		isAdmin := p.SystemConfig.IsAdmin(msg.Sender.UserId)
		stopped, runningTasks, denied := p.stopSessionWork(msg.GroupId, true, msg.Sender.UserId, isAdmin)
		if denied != "" {
			builder := msgchain.Builder().Group()
			builder.Text(denied)
			bot.SendGroupMsg(msg.GroupId, builder.Build())
			return false, nil
		}
		builder := msgchain.Builder().Group()
		switch {
		case stopped && runningTasks > 0:
			builder.Text("已停止当前 AI 响应与后台任务")
		case stopped:
			builder.Text("用户停止AI响应")
		case runningTasks > 0:
			builder.Text("已停止后台任务")
		default:
			builder.Text("当前没有正在进行的 AI 请求")
		}
		if stopped || runningTasks > 0 {
			p.Logger.Info("用户停止 AI 工作", "group", msg.GroupId, "user", msg.Sender.UserId,
				"turn_stopped", stopped, "tasks_stopped", runningTasks)
		}
		bot.SendGroupMsg(msg.GroupId, builder.Build())
		return false, nil
	}

	if cmd.Name == "plan" {
		return p.handlePlanCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "status" {
		return p.handleStatusCommand(bot, msg)
	}

	if cmd.Name == "cmd" {
		return p.handleCmdCommand(ctx, bot, cmd, msg)
	}

	// 自定义斜杠命令：命中则把消息改写为展开后的纯文本，继续走正常对话流程
	if p.commandManager != nil {
		p.commandManager.rewriteCustomCommand(cmd, &msg)
	}

	// 回合改为后台执行：消息只入队并唤醒该会话的运行器，事件处理立即返回。
	// 长回合不再占用框架事件 goroutine，也不会被 bot.msg_event_timeout_sec
	// 截断；同一会话的串行性由会话锁 + 排队队列保证（见 runner.go）。
	p.submitTurn(bot, msg.GroupId, true, msg)
	return false, nil
}

// newConversationRequested 本批消息中是否有任何一条**自身的文本段**包含 #新对话。
//
// 只看原始消息段、不看 FriendlyText 渲染结果：后者会展开被引用消息与合并转发
// 内容，引用一条含该字样的旧消息就会误触发历史清空（连同动态 MCP 工具卸载）。
// 转发内容同样不算"用户当前输入"。
func newConversationRequested(batch []message.Message) bool {
	for i := range batch {
		for _, seg := range batch[i].Message {
			if seg.Type != message.SegmentText {
				continue
			}
			var txt message.TextMessage
			if !message.ParseText(seg, &txt) {
				continue
			}
			if strings.Contains(txt.Text, "#新对话") {
				return true
			}
		}
	}
	return false
}

// drainChatBatches 执行一批（及后续陆续入队的）消息，是回合运行器与后台接管
// 共用的执行循环。返回下一批待处理消息与是否继续（false 表示本回合被取消/中止，
// 调用方应终止整条处理链；**剩余排队消息保留**，等待下一次运行器接管）。
//
// 预算语义（P1）：
//   - 不再有"事件总预算"：回合只受 ctx 的硬截止（turnHardTimeout，默认 4 小时、
//     0=不限制）与 /stop/关停取消约束；
//   - 软截止（turnSoftTimeout）由 aichat 编排层在工具循环内注入一次收尾/交接
//     提醒，不在这里取消；
//   - 排队消息不再因预算耗尽被丢弃，避免长任务期间用户消息/子代理结果丢失。
func (p *AIChatPlugin) drainChatBatches(ctx context.Context, b bot.Bot, id message.QID, isGroup bool, chat *aichat.ChatBot, batch []message.Message) ([]message.Message, bool) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			p.reportTurnTimeout(b, id, isGroup)
		}
		return nil, false
	}
	if !p.processChatBatch(ctx, b, id, isGroup, chat, batch) {
		// 本批被取消：/stop、锁丢失、关停或硬截止到期。硬截止单独说明原因，
		// 其余情况由 processChatBatch 内部的错误呈现负责。
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			p.reportTurnTimeout(b, id, isGroup)
		}
		return nil, false
	}
	return p.drainPending(id, isGroup), true
}

// reportTurnTimeout 回合硬截止到时向用户说明原因；剩余排队消息保留，不丢弃。
func (p *AIChatPlugin) reportTurnTimeout(b bot.Bot, id message.QID, isGroup bool) {
	p.Logger.Warn("回合超过硬截止被中止", "id", id, "is_group", isGroup, "timeout", p.turnHardTimeout)
	text := "本轮处理已超过最长运行时间，已中止。已完成的进度会保留；发送 /stop 可停止后续任务。"
	if p.turnHardTimeout > 0 {
		text = "本轮处理已超过最长运行时间（" + formatStatusDuration(p.turnHardTimeout) + "），已中止。已完成的进度会保留；发送 /stop 可停止后续任务。"
	}
	p.sendPlainText(b, id, isGroup, text)
}

func (p *AIChatPlugin) OnFriendMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	// 未完成初始化（如未配置 API KEY）时跳过全部处理，交由后续插件
	if !p.ready {
		return true, nil
	}
	// 主动互动：先记录现场（与群聊一致，开关指令本身无 mention 门概念）
	if p.proactiveManager != nil {
		p.proactiveManager.noteMessage(msg, false)
	}
	// 工具审批回复必须最先拦截：审批等待期间会话锁被占用，回复走不到正常聊天流程
	if p.approvalManager != nil {
		text, _ := utils.ExtraMessageStr(msg)
		if consumed, hint := p.approvalManager.tryHandleReply(msg.Sender.UserId, false, msg.Sender.UserId, text); consumed {
			if hint != "" {
				p.sendPlainText(bot, msg.Sender.UserId, false, hint)
			}
			return false, nil
		}
	}

	if cmd.Name == "clock" {
		return p.handleClockCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "proactive" {
		return p.handleProactiveCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "tasks" {
		return p.handleTasksCommand(bot, msg)
	}

	if cmd.Name == "resume" {
		taskID := ""
		if len(cmd.Args) > 0 {
			taskID = strings.TrimSpace(cmd.Args[0])
		}
		return p.handleResumeCommand(bot, msg, taskID)
	}

	if cmd.Name == "goal" {
		return p.handleGoalCommand(bot, cmd, msg)
	}

	if cmd.Name == "stop" {
		if len(cmd.Args) > 0 {
			return p.handleStopByID(bot, msg, strings.TrimSpace(cmd.Args[0]))
		}
		isAdmin := p.SystemConfig.IsAdmin(msg.Sender.UserId)
		stopped, runningTasks, denied := p.stopSessionWork(msg.Sender.UserId, false, msg.Sender.UserId, isAdmin)
		if denied != "" {
			builder := msgchain.Builder().Friend()
			builder.Text(denied)
			bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
			return false, nil
		}
		builder := msgchain.Builder().Friend()
		switch {
		case stopped && runningTasks > 0:
			builder.Text("已停止当前 AI 响应与后台任务")
		case stopped:
			builder.Text("用户停止AI响应")
		case runningTasks > 0:
			builder.Text("已停止后台任务")
		default:
			builder.Text("当前没有正在进行的 AI 请求")
		}
		if stopped || runningTasks > 0 {
			p.Logger.Info("用户停止 AI 工作", "user", msg.Sender.UserId,
				"turn_stopped", stopped, "tasks_stopped", runningTasks)
		}
		bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		return false, nil
	}

	if cmd.Name == "plan" {
		return p.handlePlanCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "status" {
		return p.handleStatusCommand(bot, msg)
	}

	if cmd.Name == "cmd" {
		return p.handleCmdCommand(ctx, bot, cmd, msg)
	}

	// 自定义斜杠命令：命中则把消息改写为展开后的纯文本，继续走正常对话流程
	if p.commandManager != nil {
		p.commandManager.rewriteCustomCommand(cmd, &msg)
	}

	// 回合同群聊：消息入队后由后台运行器执行（见 runner.go / submitTurn）
	p.submitTurn(bot, msg.Sender.UserId, false, msg)
	return false, nil
}

// processChatBatch 处理一批消息：可能只有一条（直接触发），也可能包含 AI 响应期间
// 排队的多条消息。多条时合并为一轮请求，让 AI 一次性回应。返回 false 表示应终止
// 处理循环（请求被取消或出错，已通知用户并丢弃剩余排队消息）。
func (p *AIChatPlugin) processChatBatch(ctx context.Context, b bot.Bot, id message.QID, isGroup bool, chat *aichat.ChatBot, batch []message.Message) bool {
	lastMsg := batch[len(batch)-1]

	// 合并本批消息文本；多条时加引导说明，让 AI 知道这些是响应期间积攒的消息
	var extraText string
	if len(batch) == 1 {
		extraText = p.extraMsg(b, lastMsg)
	} else {
		var sb strings.Builder
		fmt.Fprintf(&sb, "【以下是 %d 条在你响应期间收到的消息，请逐一回应】\n", len(batch))
		for i := range batch {
			sb.WriteString(p.extraMsg(b, batch[i]))
			sb.WriteString("\n")
		}
		extraText = sb.String()
	}

	// #新对话 只认「本批消息自身的文本段」：extraText 经 FriendlyText 渲染，会把
	// 被引用消息正文（<reply>…</reply>）与合并转发节点内容一并展开——只要引用了
	// 任何含该字样的旧消息（例如机器人复述过的帮助文本）就会误清空历史，用户
	// 无法归因。这里改为扫描原始消息段的文本。
	if newConversationRequested(batch) {
		if err := chat.ClearHistory(ctx); err != nil {
			p.Logger.Error("无法清理AI聊天信息", "error", err)
			return false
		}
		// 新会话：换新会话 token，本次及之后的请求（含子代理/定时任务）的
		// ${session} 请求头随之改变
		p.renewSessionToken(sessionKey(id, isGroup))
		// 历史已清空，旧的落盘工具结果不再被引用，顺手删除（其余靠后台 TTL 清理）
		p.toolResultLimiter.Purge(sessionKey(id, isGroup))
		p.Logger.Info("清理AI对话信息成功")

		if cleared := chat.ClearDynamicTools(); cleared > 0 {
			p.Logger.Info("清理动态加载的 MCP 工具", "count", cleared)
		}
	}

	// 请求级图片哈希→URL 注册表：当前消息、历史消息记录、合并转发中的图片都会登记，
	// load_images 按哈希查找并只加载指定的图片。注册表随请求上下文传递，
	// 平台历史消息工具（aitool）执行时经它登记拉取到的历史消息图片
	imageReg := newImageRegistry()
	ctx = context.WithValue(ctx, imageRegistryKey{}, imageReg)
	// 主动发消息配额（send_message 工具）：随本次回合重置，防止长任务刷屏
	ctx = withSendMessageBudget(ctx, sendMessageMaxPerTurn)
	var msgFuncs llmtool.CallBackFuncs
	if isGroup {
		msgFuncs = MakeGroupCallback(b, id, lastMsg.Sender.UserId, batchSelfID(batch), p.Logger)
	} else {
		msgFuncs = MakeFriendCallback(b, id, p.Logger)
	}
	p.configureImageCallbacks(ctx, b, &msgFuncs, imageReg, func(u aichat.TokenUsage) {
		// 备用图片识别（OCR）消耗：并入会话统计（finishQuery 取走）与配额
		p.addExtraUsage(sessionKey(id, isGroup), u)
		p.quotaManager.Add(sessionKey(id, isGroup), u)
	}, batch...)
	// 工具结果长度治理回调（超长结果落盘/截断 + read_tool_output 读回）
	p.attachToolResultLimit(&msgFuncs, sessionKey(id, isGroup))

	// 命令级人工审批（bash 三段式中不在黑白名单的命令在工具内部调用）：与门禁
	// 审批腿共用 approvalManager；提示同样走纯发送闭包（与流式消息互不干扰）。
	// 仅在工具审批开关开启时注入：审批关闭时 manager 可能仅为配置修改工具构造，
	// bash 的未列名命令维持「审批未启用则默认放行」语义（只认黑名单）。
	if p.cfg.Approval.Enable && p.approvalManager != nil {
		requester := lastMsg.Sender.UserId
		sendPrompt := func(text string) { p.sendPlainText(b, id, isGroup, text) }
		msgFuncs.RequestApproval = func(ctx context.Context, toolName, summary string) (bool, string) {
			return p.approvalManager.request(ctx, sessionKey(id, isGroup), toolName, summary, requester, sendPrompt)
		}
	}

	// 每日配额检查：超限直接拒绝并丢弃排队消息（含子代理/定时任务的消耗，
	// 见各调用点 Add）。检查放在 beginQuery 之前，避免被拒请求留下无效的
	// Query 日志记录（running 状态悬挂）
	if reason, denied := p.quotaManager.Check(sessionKey(id, isGroup)); denied {
		p.sendPlainText(b, id, isGroup, reason)
		p.drainPending(id, isGroup)
		return false
	}

	// Query 日志：本次触发的原始用户输入与最终响应记录在同一条日志里。提前创建
	// 记录，使运行中插话在注入时能把文本追加进 Query 字段（面板/导出可审计）。
	recorder := p.beginQuery(chat, id, isGroup, batch, extraText)

	chatOpts := p.buildChatOptions()
	// 运行中插话：本次请求执行期间到达的新消息（含子代理完成等合成消息）在每次
	// LLM 请求前以 user 消息注入当前工具循环，不再等整轮结束后才排队处理；
	// 未及时注入的剩余消息仍留在队列里，由运行器作为下一批处理。注入的文本同时
	// 追加到本次 Query 日志的 Query 字段，便于面板审计「处理中用户又说了什么」。
	chatOpts.Steer = p.buildInterjectionSteer(b, id, isGroup, imageReg, func(texts []string) {
		p.appendInterjectionsToQuery(recorder, texts)
	})
	// 请求级工具门禁：计划模式 → PreToolUse 钩子 → 管理员审批 → 工具审批（见 gate.go）。
	// 审批提示走纯发送闭包，与进行中的流式消息互不干扰。
	chatOpts.PreToolGate = p.buildPreToolGate(sessionKey(id, isGroup), agenthook.AgentKindMain, lastMsg.Sender.UserId,
		func(text string) { p.sendPlainText(b, id, isGroup, text) },
		p.buildAdminPromptSender(b))

	// 处理阶段上报（/status 展示）：请求期间活跃请求记录最近一步在做什么。
	// 门禁在并行工具协程内调用，updateSessionStage 内部按活跃请求加锁写入。
	// 阶段文案用「工具调用 <名>」而非「执行工具」：门禁里可能正在等待人工审批
	// （最长数分钟），此时工具尚未真正执行，文案要涵盖这段等待。
	sKey := sessionKey(id, isGroup)
	p.updateSessionStage(sKey, stageWaitingModel)
	gate := chatOpts.PreToolGate
	chatOpts.PreToolGate = func(ctx context.Context, call llmtool.ToolCall) (bool, string) {
		p.updateSessionStage(sKey, "工具调用 "+call.Name)
		if gate == nil {
			return false, ""
		}
		return gate(ctx, call)
	}

	// 流式回复：平台支持「先发后改」（如飞书卡片/Telegram/Discord 消息 Patch）时逐字展示；
	// 平台不支持或流式创建失败时自动退化为一次性回复（下方原发送路径）。
	// 全程只维持一条消息：工具轮的中间文本先展示，随后被下一轮文本整体覆盖
	//（见 streamreply.go），最终留下的就是最后一轮的最终回复；群聊首条消息
	// 携带本批全部发言者的 @ 提及（与一次性路径的提及集一致）。
	// 回复拆分只作用于非流式发送：||| 分隔符在流式展示中转为换行（见
	// streamDisplayText）——流式是单条消息逐字更新，本身无法拆成多条，
	// 故流式生效时不做拆分。
	stream := p.newStreamReplyFor(b, id, isGroup, batch)
	if stream != nil {
		chatOpts.OnStreamDelta = func(delta string) {
			stream.OnDelta(delta)
		}
		chatOpts.OnStreamRoundEnd = stream.OnRoundEnd
		chatOpts.OnStreamRestart = stream.OnRestart
	}

	// 记忆/知识库检索须基于原始用户消息：下面的注入会改写 extraText
	userText := extraText

	// 自动注入的查询向量：启用向量检索时每轮对用户消息 embed 一次（带缓存
	// 与 10s 短超时），知识库与记忆注入复用同一向量；失败为 nil，两处自动
	// 退化为纯关键词检索。
	var queryVec []float32
	if p.embedder != nil && (p.cfg.Kb.AutoInject || p.cfg.Memory.AutoInject) {
		queryVec = p.embedder.EmbedOneCached(ctx, userText)
	}

	// 知识库自动注入：对用户消息做检索（有向量时语义+关键词混合，否则纯
	// 关键词），命中相关文档时把片段拼到用户消息前作为参考上下文。
	// 注入在 beginQuery 之后，query 日志保留原始用户消息。
	if p.knowledgeManager != nil && p.cfg.Kb.AutoInject {
		kbScope := "f:" + id.String()
		if isGroup {
			kbScope = "g:" + id.String()
		}
		if injected := p.knowledgeManager.autoInject(kbScope, extraText, 30, queryVec); injected != "" {
			extraText = injected + "\n\n" + extraText
		}
	}

	// 长期记忆自动注入：按原始用户消息检索相关记忆（有向量时语义+关键词
	// 混合，否则纯关键词），命中后拼到用户消息前（尾部注入：system 保持
	// 不变，不影响上游前缀缓存；用户消息落盘时以注入前的原文为准，注入
	// 内容不会污染持久化历史）。与知识库注入叠加时记忆块在最前、知识库块
	// 居中、用户消息最后。
	if p.memoryManager != nil && p.cfg.Memory.AutoInject {
		memScope := "f:" + id.String()
		if isGroup {
			memScope = "g:" + id.String()
		}
		if injected := p.memoryManager.autoInject(memScope, userText, p.cfg.Memory.InjectMax, queryVec); injected != "" {
			extraText = injected + "\n\n" + extraText
		}
	}

	// SessionStart 钩子上下文注入：会话（重）创建时钩子产出的上下文只消费一次
	// （尾部注入，同上的缓存与落盘约束）
	if v, ok := p.sessionInject.LoadAndDelete(sessionKey(id, isGroup)); ok {
		if injected, _ := v.(string); injected != "" {
			extraText = injected + "\n\n" + extraText
		}
	}

	// 任务清单提醒：有未完成项且内容有变化时注入（哈希去重，避免每轮重复污染）；
	// 尾部注入，同上的缓存与落盘约束
	if p.cfg.Todo.Enable && p.todoManager != nil {
		if reminder := p.todoManager.pendingReminder(sessionKey(id, isGroup)); reminder != "" {
			extraText = reminder + "\n\n" + extraText
		}
	}

	// 计划模式附言：每轮尾部注入（plan 可中途开关，buildScenePrompt 的一次性
	// 场景提示跟不上）；子代理/定时任务路径只有门禁拦截，不带附言。
	if p.planManager != nil && p.planManager.IsOn(sessionKey(id, isGroup)) {
		extraText = "【计划模式】当前处于计划模式：请只做分析与规划并输出实施计划，不要执行任何会产生实际副作用的操作（修改文件、运行命令、改配置、建任务等会被系统自动阻止）。用户确认计划后会退出计划模式再执行。\n\n" + extraText
	}

	// 定时任务播报提示：定时任务走一次性会话，推送的消息不在本会话历史里，
	// 也不随历史回放出现。用户在播报之后发言时补一条提示，引导 AI 先用历史
	// 消息工具确认播报内容，避免答非所问。消费一次（请求成功后才记账，失败
	// 轮次下次发言仍会提示）；尾部注入，落盘仍用注入前的用户原文。
	var deliveryHintSeq uint64
	if p.clockManager != nil {
		if hint, seq, ok := p.clockManager.deliveryHint(id, isGroup, p.historyToolNameFor(sessionKey(id, isGroup))); ok {
			extraText = hint + "\n\n" + extraText
			deliveryHintSeq = seq
		}
	}

	// 落盘用注入前的原始用户文本：记忆/知识库/待办/计划附言等注入只作用于当轮，
	// 不随历史回放（旧注入白占上下文，且可能引用已失效的记忆 ID 或过期资料）；
	// 用户消息本身正常落盘，跨轮/跨重启的对话上下文保持完整
	chatOpts.PersistUserText = userText

	resp, usage, err := chat.Chat(ctx, extraText, msgFuncs, chatOpts)
	// 流式回复收尾：Chat 返回后结束流式消息（幂等）
	if stream != nil {
		stream.End()
	}
	if err == nil {
		p.updateSessionStage(sKey, stageSendingReply)
	}
	// 待办提醒哈希仅在请求成功后提交：失败轮次不消耗提醒，下一轮仍会注入
	//（清单未变时恰恰是最需要提醒的时候）
	if err == nil && p.cfg.Todo.Enable && p.todoManager != nil {
		p.todoManager.commitReminder(sessionKey(id, isGroup))
	}
	// 定时任务播报提示同理：请求失败时提示并未生效（用户会再发一次），
	// 不记账，下一次发言重新注入
	if err == nil && deliveryHintSeq != 0 && p.clockManager != nil {
		p.clockManager.commitDeliveryHint(id, isGroup, deliveryHintSeq)
	}
	p.finishQuery(recorder, chat, usage, resp, err)
	// 主对话消耗计入会话与全局配额（子代理/团队/定时任务在其各自调用点累加）
	p.quotaManager.Add(sessionKey(id, isGroup), usage)
	if err != nil {
		// UserPromptSubmit 钩子阻断：不算请求失败——把原因告知用户后继续
		// 处理后续排队消息（不丢弃、不记错误日志）
		var blocked *aichat.PromptBlockedError
		if errors.As(err, &blocked) {
			p.sendPlainText(b, id, isGroup, blocked.Reason)
			return true
		}
		// 出错或取消时丢弃剩余排队消息，避免连续报错刷屏
		p.drainPending(id, isGroup)
		switch {
		case errors.Is(err, context.Canceled):
			p.sendPlainText(b, id, isGroup, "AI 响应已被停止")
		case errors.Is(err, context.DeadlineExceeded):
			p.sendPlainText(b, id, isGroup, "请求超时")
		default:
			p.Logger.Error("AI请求错误", "error", err.Error())
			p.sendPlainText(b, id, isGroup, aichat.UserErrorText(err))
		}
		return false
	}

	// Stop 钩子（仅通知）：一次完整响应成功结束
	if p.hookManager != nil {
		p.hookManager.Run(ctx, agenthook.EventStop, agenthook.Payload{
			SessionKey: sessionKey(id, isGroup),
			AgentKind:  agenthook.AgentKindMain,
			Prompt:     querylog.Truncate(resp, 1000),
		})
	}

	if len(strings.TrimSpace(resp)) == 0 {
		p.Logger.Info("AI请求没有返回什么东西")
		return true
	}

	p.Logger.Info("AI请求token消耗", "id", id, "is_group", isGroup, "batch", len(batch), "prompt_tokens", usage.PromptTokens, "completion_tokens", usage.CompletionTokens, "total_tokens", usage.TotalTokens)

	// 主动互动的现场标记：机器人刚说过话，短时间内不应再主动开口。只在确实
	// 发出回复后标记（流式已推送 / 合并转发 / 逐条发送），发送失败不算说过话。
	// 流式已发出回复时不再走一次性发送（内容已逐字展示在流式消息中）
	if stream != nil && stream.Streaming() {
		p.noteBotSpoke(id, isGroup)
		return true
	}

	// 最终回复片段（流式未生效时才会走到这里，如平台不支持流式或流式已退化）：
	// AI 用 ||| 指定了消息边界时拆成多条逐条发送，否则整段一条；片段过多
	//（> forwardSegThreshold）时平台支持合并转发（QQ）则改为一条合并转发，避免刷屏。
	segments := replySegments(resp)
	if isGroup {
		// 群聊中 @ 本批所有发言者（去重），让排队消息的每个人都收到回应；
		// 拆分发送时仅首条携带 @ 提及（合并转发为首节点），避免每条回复都提醒
		mentions := batchMentions(batch)
		if shouldMergeGroupReply(segments) && sendGroupForwardReply(b, id, segments, mentions, batchSelfID(batch), p.Logger) {
			p.noteBotSpoke(id, isGroup)
			return true
		}
		for i, seg := range segments {
			builder := msgchain.Builder().Group()
			if i == 0 {
				for _, uid := range mentions {
					builder.Mention(uid)
				}
				builder.Text(" " + seg)
			} else {
				builder.Text(seg)
			}
			if _, success := b.SendGroupMsg(id, builder.Build()); success {
				p.Logger.Info("发送文本", "group", id, "batch", len(batch), "segment", i+1, "text", seg)
			}
		}
	} else {
		for i, seg := range segments {
			builder := msgchain.Builder().Friend()
			builder.Text(seg)
			if _, success := b.SendFriendMsg(id, builder.Build()); success {
				p.Logger.Info("发送文本", "user", id, "batch", len(batch), "segment", i+1, "text", seg)
			}
		}
	}
	p.noteBotSpoke(id, isGroup)
	return true
}

// sendPlainText 发送纯文本提示信息（不 @ 任何人）
func (p *AIChatPlugin) sendPlainText(b bot.Bot, id message.QID, isGroup bool, text string) {
	if isGroup {
		builder := msgchain.Builder().Group()
		builder.Text(text)
		b.SendGroupMsg(id, builder.Build())
	} else {
		builder := msgchain.Builder().Friend()
		builder.Text(text)
		b.SendFriendMsg(id, builder.Build())
	}
}

// buildAdminPromptSender 构造把管理员审批提示私聊发给全部管理员的闭包（配置修改类
// 工具的管理员审批提示默认发到管理员私聊，任一管理员在私聊中回复「允许/拒绝」均可）。
// 未配置管理员时返回 nil；全部发送失败（如管理员均未加机器人好友）返回 false，
// 审批管理器会回退到在发起会话内提示。
func (p *AIChatPlugin) buildAdminPromptSender(b bot.Bot) func(text string) bool {
	admins := p.SystemConfig.Admins()
	if len(admins) == 0 {
		return nil
	}
	return func(text string) bool {
		sent := false
		for _, admin := range admins {
			builder := msgchain.Builder().Friend()
			builder.Text(text)
			if _, ok := b.SendFriendMsg(admin, builder.Build()); ok {
				sent = true
			}
		}
		return sent
	}
}

func (p *AIChatPlugin) buildChatOptions() aichat.ChatOptions {
	opts := p.thinkingOpts()
	opts.Watchdog = p.watchdogOptions(true)
	return opts
}

// buildSubagentChatOptions 子代理/团队成员的一次性会话参数：保留重复调用与
// 无进展保护，但不注入"收尾或转后台"软截止提醒（子代理没有 handoff 工具，
// 其生命周期由 subagent 超时与 /stop 管理）。
func (p *AIChatPlugin) buildSubagentChatOptions() aichat.ChatOptions {
	opts := p.thinkingOpts()
	opts.Watchdog = p.watchdogOptions(false)
	return opts
}

// watchdogOptions 构造 aichat 编排层的工具循环保护配置。
func (p *AIChatPlugin) watchdogOptions(withSoftDeadline bool) aichat.WatchdogOptions {
	wo := aichat.WatchdogOptions{
		ProgressReminderAfter: p.cfg.Turn.ProgressReminderAfter,
		RepeatWarnThresholds:  parseRepeatToolWarn(p.cfg.Turn.RepeatToolWarn),
		NoProgressLimit:       p.cfg.Turn.NoProgressLimit,
	}
	if withSoftDeadline && p.turnSoftTimeout > 0 {
		wo.SoftDeadline = time.Now().Add(p.turnSoftTimeout)
	}
	return wo
}

// parseRepeatToolWarn 解析 "3,5,8" 形式的重复调用提醒阈值；非法项跳过。
func parseRepeatToolWarn(raw string) []int {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n <= 0 {
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func (p *AIChatPlugin) buildOCRChatOptions() aichat.ChatOptions {
	opts := aichat.ChatOptions{}
	if p.cfg.OCR.MaxToken != nil {
		opts.MaxToken = p.cfg.OCR.MaxToken
	}
	if p.cfg.OCR.Temperature != nil {
		opts.Temperature = p.cfg.OCR.Temperature
	}
	if p.cfg.OCR.TopP != nil {
		opts.TopP = p.cfg.OCR.TopP
	}
	if p.cfg.OCR.TopK != nil {
		opts.TopK = p.cfg.OCR.TopK
	}
	return opts
}

func (p *AIChatPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	p.lockStorage = p.Storage.Clone("lock")
	p.lockStorage.Clear(ctx)

	// 插件配置已由框架自动填充到 p.cfg（见 ConfigSchema），这里只做校验与兜底
	rateLimit := p.cfg.RateLimit
	if rateLimit <= 0 {
		rateLimit = 4
	}
	p.rateCh = make(chan struct{}, rateLimit)

	// 回合预算：软截止只提醒、硬截止才取消；0 表示关闭/不限制（见 turnConfig）
	p.turnSoftTimeout = secondsDuration(p.cfg.Turn.SoftTimeoutSec)
	p.turnHardTimeout = secondsDuration(p.cfg.Turn.HardTimeoutSec)
	// 插件生命周期上下文：后台运行器与异步子代理继承，OnUnload 统一取消
	p.runCtx, p.runCancel = context.WithCancel(context.Background())
	// 后台子代理全局并发池（与对话回合的 rateCh 分开计算）
	if n := p.cfg.Subagent.MaxConcurrentGlobal; n > 0 {
		p.subagentSem = make(chan struct{}, n)
	}

	// AI 必需配置缺失不算初始化错误（首次启动设置向导期间是正常状态）：
	// 记警告并以降载模式加载（ready=false），消息事件直接跳过，不刷错误日志
	if p.cfg.BaseURL == "" || p.cfg.Model == "" || p.cfg.APIKey == "" {
		p.Logger.Warn("AI 对话未配置（缺少 base_url / model / api_key），AI 对话功能暂不可用；请在 Web 面板填写后重启 Bot")
		return nil
	}
	switch p.cfg.APIFormat {
	case "", aichat.APIFormatChatCompletions, aichat.APIFormatResponses, aichat.APIFormatAnthropic:
	default:
		return fmt.Errorf("%w: 未知 API 格式 %q（plugin.ai_chat_bot.api_format，可选 %s/%s/%s）",
			aniaerror.ParameterInitializeError, p.cfg.APIFormat,
			aichat.APIFormatChatCompletions, aichat.APIFormatResponses, aichat.APIFormatAnthropic)
	}
	if p.cfg.Prompt == "" {
		p.Logger.Warn("未配置 Prompt，将使用预设的默认提示词")
		p.cfg.Prompt = defaultPrompt
	}

	// 自定义请求头：附加到全部 LLM 请求；非法行跳过（原样下发会让 net/http
	// 拒绝请求，拖垮所有 LLM 调用），日志只记行号不记内容（可能含误填的凭据）
	customHeaders, invalidHeaderLines := parseHeaderLines(p.cfg.Headers)
	p.customHeaders = customHeaders
	if len(invalidHeaderLines) > 0 {
		p.Logger.Warn("自定义请求头存在非法行，已跳过（行号按配置文本框计数）",
			"lines", fmt.Sprint(invalidHeaderLines))
	}
	if len(p.customHeaders) > 0 {
		p.Logger.Info("已配置自定义 LLM 请求头", "headers", strings.Join(headerNames(p.customHeaders), ", "))
		// 未知占位符（如拼错 ${sessoin}）会原样下发，启动时提示一次
		if unknown := unknownHeaderPlaceholders(p.customHeaders); len(unknown) > 0 {
			p.Logger.Warn("自定义请求头包含未知占位符，将原样发送", "placeholders", strings.Join(unknown, ", "))
		}
	}

	// 加载群聊/好友独立 prompt 覆盖配置（框架级共享键，仍走 viper）
	p.loadPromptOverrides(cfg)

	// 框架级消息处理超时（bot.msg_event_timeout_sec）：后台触发的会话处理
	// （tryProcessPending）复用同一预算；与 core.msgEventTimeout 同样的兜底/限幅
	if sec := cfg.GetInt("bot.msg_event_timeout_sec"); sec > 0 {
		p.msgEventTimeout = time.Duration(min(sec, 86400)) * time.Second
	} else {
		p.msgEventTimeout = 5 * time.Minute
	}

	if p.cfg.Thinking.Mode == "" {
		p.cfg.Thinking.Mode = "auto"
	}
	if p.cfg.Thinking.Enable {
		p.Logger.Info("已启用深度思考模式", "mode", p.cfg.Thinking.Mode)
	}

	if p.cfg.Multimodal {
		p.Logger.Info("主对话模型已配置为支持多模态，将按需直接加载图片")
	}

	if p.cfg.OCR.Enable {
		p.Logger.Info("已启用备用图片识别 LLM")
		// OCR 客户端同样附加应用层重试（不配备用模型切换）
		ocrllm, err := aichat.NewChatBot(p.cfg.OCR.BaseURL, p.cfg.OCR.APIKey, p.cfg.OCR.Model, p.cfg.OCR.Prompt, 0, nil, nil,
			aichat.WithClientOptions(p.llmClientOptions()...))
		if err != nil {
			p.Logger.Error("无法初始化备用图片识别 LLM", "error", err.Error())
		} else {
			p.ocrModel = ocrllm
		}
	}

	if err := p.loadMCPConfigs(cfg); err != nil {
		p.Logger.Warn("加载 MCP 配置失败", "error", err.Error())
	}

	// AI 钩子：按 files.hooks_json 配置在会话事件上执行 shell 命令（面板「扩展配置」
	// 页编辑，秒级热生效）；同时接收其他插件注册的 Go 钩子（core 在全部插件
	// Start 后收集注入，见 SetGoHookHandlers）。管理器始终构造，未启用时 Run 短路。
	p.hookManager = agenthook.NewManager(p.ConfigEditor, hooksConfigKey, p.Logger.WithGroup("hooks"))
	p.hookManager.SetEnabled(p.cfg.Hooks.Enable)
	if len(p.goHookHandlers) > 0 {
		p.hookManager.SetGoHandlers(p.goHookHandlers)
		p.goHookHandlers = nil
	}
	if p.cfg.Hooks.Enable {
		if err := p.hookManager.Reload(); err != nil {
			p.Logger.Warn("加载钩子配置失败", "error", err.Error())
		}
		p.Logger.Info("已启用 AI 钩子功能", "timeout_sec", p.cfg.Hooks.TimeoutSec)
	} else {
		p.Logger.Info("AI 钩子功能未启用（plugin.ai_chat_bot.hooks.enable=false）")
	}

	// 计划模式：始终可用（无配置开关），/plan on 开启后副作用工具被门禁阻断
	p.planManager = newPlanManager()

	// 任务清单：始终构造（todo_write 注册与否由 cfg.Todo.Enable 在 getChat 决定）
	p.todoManager = newTodoManager()

	// 自定义斜杠命令：始终构造（无开关；配置中心不可用时管理操作报错、查询为空）
	p.commandManager = newCommandManager(p.ConfigEditor, commandsConfigKey, p.Logger)

	// 工具审批：默认关闭；启用后 approval.tools 列出的工具与 bash 未列名命令
	// 需请求发送者或管理员回复确认。配置修改类工具（config_set/config_file_set）
	// 恒需管理员审批（见 approval.go adminApprovalTools），故启用配置管理工具时
	// 也构造审批管理器（此时 tools 集合为空，仅管理员审批腿生效）。
	var approvalToolNames []string
	if p.cfg.Approval.Enable {
		approvalToolNames = strings.Split(p.cfg.Approval.Tools, ",")
	}
	if p.cfg.Approval.Enable || p.cfg.ConfigTool.Enable {
		p.approvalManager = newApprovalManager(approvalToolNames, p.cfg.Approval.TimeoutSec, p.SystemConfig.Admins(), p.Logger)
		if p.cfg.Approval.Enable {
			p.Logger.Info("已启用工具审批", "tools", p.approvalManager.tools, "timeout_sec", p.cfg.Approval.TimeoutSec)
		} else {
			p.Logger.Info("工具审批未启用，配置修改工具仍需要管理员审批")
		}
	} else {
		p.Logger.Info("工具审批未启用（plugin.ai_chat_bot.approval.enable=false）")
	}

	p.Logger.Info("初始化工具执行器...")
	skillsDir := p.cfg.SkillsDir
	if skillsDir == "" {
		skillsDir = "./skills"
	}
	p.skillsDir = skillsDir
	bashConfig := functool.BashConfig{
		Enable:     p.cfg.Bash.Enable,
		Shell:      p.cfg.Bash.Shell,
		Env:        p.cfg.Bash.Env,
		Whitelist:  p.cfg.Bash.Whitelist,
		Blacklist:  p.cfg.Bash.Blacklist,
		WorkingDir: p.cfg.Bash.WorkingDir,
		PersistCwd: p.cfg.Bash.PersistCwd,
		TimeoutSec: p.cfg.Bash.TimeoutSec,
	}
	if bashConfig.Enable {
		p.Logger.Info("已启用bash工具", "shell", bashConfig.Shell, "whitelist", bashConfig.Whitelist, "blacklist", bashConfig.Blacklist, "working_dir", bashConfig.WorkingDir, "persist_cwd", bashConfig.PersistCwd)
	}
	fileConfig := functool.FileConfig{Enable: p.cfg.File.Enable}
	if fileConfig.Enable {
		p.Logger.Info("已启用file工具（可读取宿主机本地文件并发送，请注意安全风险）")
	}
	localImageConfig := functool.LocalImageConfig{Enable: p.cfg.LocalImage.Enable}
	if localImageConfig.Enable {
		p.Logger.Info("已启用local_image工具（可读取宿主机本地图片供AI查看，请注意安全风险）")
	}
	fileToolsConfig := functool.FileToolsConfig{Enable: p.cfg.FileTools.Enable, Root: p.cfg.FileTools.Root}
	if fileToolsConfig.Enable {
		p.Logger.Info("已启用文件读写工具（read_file/write_file/edit_file/glob/grep，可读取与编辑宿主机文件，请注意安全风险）", "root", fileToolsConfig.Root)
	}
	var err error
	p.toolExecutor, p.skillManager, err = functool.CreateToolsWithSkill(
		p.mcpConfigs,
		skillsDir,
		bashConfig,
		fileConfig,
		localImageConfig,
		fileToolsConfig,
		p.cfg.Skills,
		p.cfg.MCP.LazyLoad,
	)
	if err != nil {
		return fmt.Errorf("%w: 创建工具执行器失败: %w", aniaerror.ParameterInitializeError, err)
	}
	// 应用技能禁用列表（内置技能不可禁用，管理器会忽略对应项）
	p.skillManager.SetDisabled(p.cfg.SkillsDisabled)
	if disabled := p.skillManager.DisabledNames(); len(disabled) > 0 {
		p.Logger.Info("已禁用部分 skill（描述不进入上下文）", "skills", strings.Join(disabled, ", "))
	}

	// 工具结果长度治理：所有工具结果回填前统一经过 LimitToolResult。超长成功
	// 结果落盘到会话临时文件，回填"预览 + handle"，AI 用 read_tool_output 读取；
	// 落盘不可用或关闭时退化为头尾截断（仍然防止上下文爆炸）。
	p.toolResultLimiter = newToolResultLimiter(p.cfg.ToolResult, p.Logger)
	if p.toolResultLimiter.SpillEnabled() {
		// 防御重名：MCP 服务器若恰好暴露 read_tool_output，内置读回工具无法注册，
		// 此时落盘结果将无人可读，明确告警（此时同名工具的超大结果仍会被治理）。
		if p.toolExecutor.Has("read_tool_output") {
			p.Logger.Warn("工具名 read_tool_output 已被其他工具占用，内置结果读回工具未注册，落盘结果可能无法读回")
		} else {
			p.toolExecutor.Register(functool.NewReadToolOutputTool())
		}
		p.toolResultLimiter.startJanitor()
		p.Logger.Info("已启用工具结果落盘",
			"max_inline_runes", p.toolResultLimiter.InlineRunes(),
			"preview_runes", p.toolResultLimiter.PreviewRunes(),
			"dir", p.toolResultLimiter.StoreDir())
	} else if p.cfg.ToolResult.Enable {
		p.Logger.Warn("工具结果落盘不可用，超长结果将直接截断")
	} else {
		p.Logger.Info("工具结果落盘未启用，超长结果按统一上限截断",
			"max_inline_runes", p.toolResultLimiter.InlineRunes())
	}

	// 配置管理工具（默认关闭，配置中心读写能力由 core 经 DI 注入）
	if p.cfg.ConfigTool.Enable {
		if p.ConfigEditor != nil {
			p.toolExecutor.Register(functool.NewConfigGetTool(p.ConfigEditor))
			p.toolExecutor.Register(functool.NewConfigSetTool(p.ConfigEditor))
			p.toolExecutor.Register(functool.NewConfigFileGetTool(p.ConfigEditor))
			p.toolExecutor.Register(functool.NewConfigFileSetTool(p.ConfigEditor))
			p.Logger.Info("已启用配置管理工具（AI 可查看/修改框架配置与扩展配置，敏感字段掩码，修改需管理员审批，重启后生效）")
		} else {
			p.Logger.Warn("配置管理工具不可用：配置中心未注入（持久化存储异常？）")
		}
	}

	// 电脑操作工具（默认关闭）：AI 可截图查看宿主机屏幕并控制鼠标键盘，
	// 仅 Windows 宿主机支持；截图回传复用 local_image 管线（多模态/OCR）
	if p.cfg.ComputerUse.Enable {
		if !computeruse.Available() {
			p.Logger.Warn("电脑操作工具不可用：当前平台不支持（仅支持 Windows 宿主机）")
		} else {
			for _, tool := range functool.NewComputerUseTools(functool.ComputerUseConfig{
				MaxWidth: p.cfg.ComputerUse.MaxWidth,
			}) {
				p.toolExecutor.Register(tool)
			}
			p.Logger.Info("已启用电脑操作工具（AI 可截图查看屏幕并控制宿主机鼠标键盘，请注意安全风险；可把 mouse_click 等加入审批工具列表实现逐次确认）")
		}
	}
	p.Logger.Info("工具执行器初始化完成")

	// AI 定时任务（clock）：AI / 用户动态管理的持久化定时任务，独立于框架 cron
	if p.cfg.Clock.Enable {
		defaultTimeoutSec := p.cfg.Clock.DefaultTimeoutSec
		if defaultTimeoutSec <= 0 {
			defaultTimeoutSec = 120
		}
		// 上限限幅：超大配置值会让 time.Duration 乘法溢出为负 duration
		if defaultTimeoutSec > clockMaxTimeoutSec {
			defaultTimeoutSec = clockMaxTimeoutSec
		}
		p.clockManager = newClockManager(p, time.Duration(defaultTimeoutSec)*time.Second)
		p.Logger.Info("已启用AI定时任务功能", "tasks", len(p.clockManager.List()), "default_timeout_sec", defaultTimeoutSec)
	} else {
		p.Logger.Info("AI定时任务功能未启用（plugin.ai_chat_bot.clock.enable=false）")
	}

	// 主动互动：意图队列 + tick 调度（默认关闭；需要 SQL 后端持久化意图）。
	// 建表失败时 newProactiveManager 记错误日志并返回 nil，功能降级不影响对话。
	if p.cfg.Proactive.Enable {
		p.proactiveManager = newProactiveManager(p)
	} else {
		p.Logger.Info("主动互动未启用（plugin.ai_chat_bot.proactive.enable=false）")
	}

	// 语义向量计算器：知识库与长期记忆共享（复用 kb.embedding 配置）
	embedder := p.buildKBEmbedder()
	p.embedder = embedder

	// AI 长期记忆：由 AI 通过 memory_save/search/forget 工具自行管理的跨会话记忆，
	// 按群聊/好友 scope 隔离，持久化到 PersistentStorage（memory: 命名空间）。
	// 嵌入向量开启时检索走关键词+语义混合打分，未开启则纯关键词
	if p.cfg.Memory.Enable {
		maxEntries := p.cfg.Memory.MaxEntries
		if maxEntries <= 0 {
			maxEntries = 200
		}
		p.memoryManager = newMemoryManager(p.PersistentStorage, p.Logger.WithGroup("memory"), maxEntries, embedder)
		p.Logger.Info("已启用AI长期记忆功能", "max_entries", maxEntries, "vector", embedder != nil)
	} else {
		p.Logger.Info("AI长期记忆功能未启用（plugin.ai_chat_bot.memory.enable=false）")
	}

	// 知识库：文档主动管理 + AI 检索（kb_search）与自动注入。作用域含全局库
	// 与按会话库，持久化固定为 SQL 行级存储（ania_kb_doc 表）；探测或建表
	// 失败时禁用该功能。构造时顺带把旧版 kb: 命名空间的整段 JSON 数据迁移进表。
	if p.cfg.Kb.Enable {
		maxDocs := p.cfg.Kb.MaxDocs
		if maxDocs <= 0 {
			maxDocs = 500
		}
		p.knowledgeManager = newKnowledgeManager(p.PersistentStorage, p.Logger.WithGroup("knowledge"), maxDocs, embedder)
		if p.knowledgeManager != nil {
			p.Logger.Info("已启用知识库功能", "max_docs", maxDocs, "vector", embedder != nil, "auto_inject", p.cfg.Kb.AutoInject)
		}
	} else {
		p.Logger.Info("知识库功能未启用（plugin.ai_chat_bot.kb.enable=false）")
	}

	// 每日 Token 配额：按每会话与全局两个维度限制 AI 消耗，计数持久化、
	// 重启不丢；启动时惰性清理 3 天前的过期日期键
	if p.cfg.Quota.Enable {
		p.quotaManager = newQuotaManager(p.PersistentStorage, p.Logger.WithGroup("quota"),
			p.cfg.Quota.DailyTokens, p.cfg.Quota.GlobalDailyTokens)
		p.quotaManager.pruneOld(3)
		p.Logger.Info("已启用每日配额限制",
			"daily_tokens", p.cfg.Quota.DailyTokens, "global_daily_tokens", p.cfg.Quota.GlobalDailyTokens)
	} else {
		p.Logger.Info("每日配额限制未启用（plugin.ai_chat_bot.quota.enable=false）")
	}

	// AI 子代理：主 AI 可通过 subagent_run 工具把复杂子任务委派给一次性子代理
	// （全新上下文 + 全部工具能力），执行结果返回给主 AI，避免污染主对话上下文；
	// 功能固定启用，模型与执行参数均跟随主模型配置/主请求预算，结果不截断
	p.Logger.Info("已启用子代理功能",
		"timeout_sec", int(p.subagentTimeout().Seconds()),
		"max_iterations", p.mainMaxIterations())

	// Agent 团队：主 AI 通过 team_run 组建多代理团队并行执行子任务。
	// 团队成员为带角色提示词的一次性子代理，复用子代理执行引擎；
	// 团队定义持久化到 PersistentStorage（team: 命名空间，按会话 scope 隔离）
	p.teamManager = newTeamManager(p.PersistentStorage, p.Logger.WithGroup("team"))
	p.Logger.Info("已启用Agent团队功能", "max_members", teamMaxMembers)

	// Query 日志：记录每次 AI 回复的完整执行过程（面板「Query 日志」页数据源）
	p.initQueryLogger()
	// 最近回合摘要：普通回合不再写入任务表，/tasks 的「最近回合」由内存保留
	p.recentTurns = newRecentTurnLog()

	// 对话历史行级化：按行存于 ania_chat_session/ania_chat_message
	// （增量追加只插入新行，避免整段 JSON 反复全量重写）；探测或建表失败时
	// historyDB 保持 nil，历史仅存于内存窗口（重启丢失），不影响对话。
	// 建表使用独立上下文：Start 的 1 分钟预算可能已被前置的 MCP 连接等消耗，
	// 继续用 startCtx 会让建表在建连慢时直接失败、历史持久化静默失效。
	if db, dialect, ok := storage.SQLBackend(p.PersistentStorage); ok {
		tableCtx, tableCancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := storage.EnsureTables(tableCtx, db, dialect, chatHistoryTables...)
		tableCancel()
		if err != nil {
			p.Logger.Error("创建对话历史表失败，历史将不持久化", "error", err.Error())
		} else {
			p.historyDB = db
			p.Logger.Info("对话历史使用行级存储", "dialect", dialect)
		}
	}

	// 未@自动清空计数持久化：跨重启累计（每 10 条落盘一次，达阈值强制落盘），
	// 避免重启清零导致「30 条未@自动清空」永远无法触发；持久层不可用时退回
	// 纯内存计数（行为等同旧版，重启清零）
	if p.cfg.Session.NoMentionClear > 0 && p.PersistentStorage != nil {
		p.noMentionStore = p.PersistentStorage.Clone(noMentionKeyPrefix)
	}

	// 后台任务注册表：会话/子代理/handoff 等后台工作的状态、进度与结果落库，
	// 供 /tasks 展示与重启后恢复。建表失败降级为不持久化（不阻断启动）。
	if p.historyDB != nil {
		taskTableCtx, taskTableCancel := context.WithTimeout(context.Background(), 15*time.Second)
		store := newTaskStore(p.historyDB, p.Logger.WithGroup("task"))
		err := store.EnsureTables(taskTableCtx)
		taskTableCancel()
		if err != nil {
			p.Logger.Error("创建后台任务表失败，任务将不做持久化", "error", err.Error())
		} else {
			p.taskStore = store
			if n, err := store.MarkInterrupted(context.Background()); err != nil {
				p.Logger.Warn("标记中断任务失败", "error", err.Error())
			} else if n > 0 {
				p.Logger.Info("已标记上次进程遗留的未完成任务为 interrupted", "count", n)
			}
		}
	}

	// 长任务目标（跨回合续跑）：需要 SQL 持久层；建表失败降级为功能不可用。
	// 注意：启动时不自动武装任何目标（goalArmed 为空）——进程重启后目标保持
	// active 但不会自动续跑，需用户 /goal resume。
	if p.historyDB != nil && p.cfg.Goal.Enable {
		goalTableCtx, goalTableCancel := context.WithTimeout(context.Background(), 15*time.Second)
		store := newGoalStore(p.historyDB, p.Logger.WithGroup("goal"))
		err := store.EnsureTables(goalTableCtx)
		goalTableCancel()
		if err != nil {
			p.Logger.Error("创建长任务目标表失败，目标续跑不可用", "error", err.Error())
		} else {
			p.goalStore = store
		}
	}

	// 会话内存回收：闲置淘汰 + LRU 容量上限，防止活跃会话增多导致内存线性增长。
	// 淘汰只丢弃内存对象，持久化历史保留，下次发言自动重建并回放。
	// 换算前限幅：先转 int64 再乘 time.Minute，超大配置值会溢出为负 duration，
	// 而 evictChats 里 `if maxIdle > 0` 为假 → 闲置淘汰静默失效（与 clock 超时
	// 的显式限幅保持一致）。
	maxIdleMinutes := min(max(p.cfg.Session.MaxIdleMinutes, 0), sessionMaxIdleMinutes)
	maxIdle := time.Duration(maxIdleMinutes) * time.Minute
	maxSessions := max(p.cfg.Session.MaxSessions, 0)
	if maxIdle > 0 || maxSessions > 0 {
		p.startChatJanitor(maxIdle, maxSessions)
		p.Logger.Info("已启用会话内存回收",
			"max_idle_minutes", p.cfg.Session.MaxIdleMinutes, "max_sessions", maxSessions)
	}

	// 排队消息兜底清扫器：唤醒集中在入队路径时，限流饱和或与锁释放交错
	// 都可能让消息错过所有消费者，由周期扫描接管（队列为空时零开销）
	p.startPendingSweeper()

	// 任务记录清理器：按 task.retention_days 删除过期已结束任务
	p.startTaskJanitor()

	// 面板「试聊室」：把聊天搬到 Web 控制面板上做对话测试——复用完整对话管线
	// （提示词/记忆/知识库/工具/上下文压缩），回复只回显面板、不向任何平台发送
	// 消息；会话转录持久化，AI 上下文跨重启延续
	p.chatConsole = newChatConsoleManager(p)
	p.Logger.Info("已启用面板试聊室", "sessions", len(p.chatConsole.list()))

	// 全部组件就绪：放行消息事件（早退路径保持 false，处理器据此跳过）
	p.ready = true
	return nil
}

// Awake Bot 启动完成后启动定时任务调度器。
func (p *AIChatPlugin) Awake(ctx context.Context, bot bot.Bot) error {
	if p.clockManager != nil {
		p.clockManager.Start(bot)
	}
	if p.proactiveManager != nil {
		p.proactiveManager.Start(bot)
	}
	return nil
}

// SetGoHookHandlers 实现 agenthook.HandlerRegistry：接收 core 收集的其他插件
// Go 钩子。时序上 core 在全部插件 Start 之后注入；防御性兜底：manager 未就绪
// 时暂存，Start 中转交。
func (p *AIChatPlugin) SetGoHookHandlers(handlers []agenthook.Handler) {
	if p.hookManager != nil {
		p.hookManager.SetGoHandlers(handlers)
		return
	}
	p.goHookHandlers = handlers
}

// loadPromptOverrides 初始化 Prompt 覆盖管理器：先用 Start 的 viper 快照填充
// 内存，之后由管理器按 TTL 重读配置中心（面板修改后热生效，无需重启）。
func (p *AIChatPlugin) loadPromptOverrides(cfg *viper.Viper) {
	p.promptManager = newPromptOverrideManager(p.ConfigEditor, promptConfigKey, p.Logger.WithGroup("prompt"))
	p.promptManager.loadRaw(cfg.GetString(promptConfigKey))
	groups, friends := p.promptManager.count()
	if groups+friends > 0 {
		p.Logger.Info("已加载 Prompt 覆盖配置", "groups", groups, "friends", friends)
	} else {
		p.Logger.Info("未配置 Prompt 覆盖", "key", promptConfigKey)
	}
}

func (p *AIChatPlugin) getPromptForID(id message.QID, isGroup bool) string {
	if p.promptManager != nil {
		if prompt, ok := p.promptManager.get(id, isGroup); ok {
			return prompt
		}
	}
	return p.cfg.Prompt
}

// OnUnload 插件卸载收尾：取消插件生命周期上下文，中止回合运行器与全部异步
// 子代理，避免进程重启/插件被卸载后仍有后台 goroutine 在跑（b.Go 本身不接 ctx）。
func (p *AIChatPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	if p.runCancel != nil {
		p.runCancel()
	}
	p.asyncSubagents.Range(func(_, v any) bool {
		if g, ok := v.(*asyncSubagentGroup); ok && g != nil {
			g.cancelAll()
		}
		return true
	})
	p.asyncSubagents.Clear()
	p.Logger.Info("AI 对话插件卸载：已取消后台运行器与子代理", "reason", string(reason))
	return nil
}
