package pluginaichat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

	skillManager *llmtool.SkillManager
	// skillsDir skill 所在目录（供面板管理接口使用）
	skillsDir string

	// clockManager AI 定时任务调度器；为 nil 表示功能未启用
	clockManager *clockManager

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

	// msgEventTimeout 框架级消息处理超时（bot.msg_event_timeout_sec，Start 时
	// 从全量 viper 读取）：tryProcessPending 等后台触发的会话处理复用同一预算
	msgEventTimeout time.Duration
}

const (
	LockExpTime     = time.Minute * 10
	promptConfigKey = "files.prompt_json"
	// hooksConfigKey / commandsConfigKey 与 configstore.KeyHooksJSON/KeyCommandsJSON
	// 同值；按 promptConfigKey 先例在插件本地定义，避免 plugins → core 依赖
	hooksConfigKey    = "files.hooks_json"
	commandsConfigKey = "files.commands_json"
)

func NewAIChatPlugin() *AIChatPlugin {
	return &AIChatPlugin{
		Meta: plugin.Meta{
			Name:      "AI对话插件",
			HelpWords: "@我聊天哦，带上 #新对话 标签可以创建新对话，发送 /stop 可以停止 AI 响应",
			Order:     plugin.LevelPostHandle,
			ShowFor:   plugininfo.ShowForGroup | plugininfo.ShowForFriend,
			Author:    "jeanhua",
			Version:   "1.0.0",
		},
	}
}

func (p *AIChatPlugin) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	// 未完成初始化（如未配置 API KEY）时跳过全部处理，交由后续插件
	if !p.ready {
		return true, nil
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

	if cmd.Name == "clock" {
		return p.handleClockCommand(ctx, bot, cmd, msg)
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

	if cmd.Name == "stop" {
		// 停止当前请求、取消异步子代理，同时丢弃排队消息
		p.cancelAsyncSubagents(msg.GroupId, true)
		p.drainPending(msg.GroupId, true)
		if p.stopRequest(msg.GroupId, true) {
			builder := msgchain.Builder().Group()
			builder.Text("用户停止AI响应")
			bot.SendGroupMsg(msg.GroupId, builder.Build())
			p.Logger.Info("用户停止 AI 响应", "group", msg.GroupId, "user", msg.Sender.UserId)
		} else {
			builder := msgchain.Builder().Group()
			builder.Text("当前没有正在进行的 AI 请求")
			bot.SendGroupMsg(msg.GroupId, builder.Build())
		}
		return false, nil
	}

	if cmd.Name == "plan" {
		return p.handlePlanCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "cmd" {
		return p.handleCmdCommand(ctx, bot, cmd, msg)
	}

	// 自定义斜杠命令：命中则把消息改写为展开后的纯文本，继续走正常对话流程
	if p.commandManager != nil {
		p.commandManager.rewriteCustomCommand(cmd, &msg)
	}

	lock := p.tryLock(msg.GroupId, true)
	if lock == nil {
		// 当前正在响应：消息进入排队队列，响应结束后自动合并处理
		first, ok := p.enqueuePending(msg.GroupId, true, msg)
		if !ok {
			builder := msgchain.Builder().Group()
			builder.Text("排队消息太多啦，稍后再试试吧~")
			bot.SendGroupMsg(msg.GroupId, builder.Build())
		} else if first {
			builder := msgchain.Builder().Group()
			builder.Text("正在回复上一条消息，你的消息已排队，稍后回复你~")
			bot.SendGroupMsg(msg.GroupId, builder.Build())
		}
		p.touchChat(sessionKey(msg.GroupId, true))
		return true, nil
	}
	p.touchChat(sessionKey(msg.GroupId, true))
	defer lock.release()
	defer p.clearActiveContext(msg.GroupId, true)

	chat, err := p.getChat(bot, msg.GroupId, true, p.getPromptForID(msg.GroupId, true))
	if err != nil {
		builder := msgchain.Builder().Group()
		builder.Text("无法创建对话：" + aichat.SafeErrorText(err))
		bot.SendGroupMsg(msg.GroupId, builder.Build())
		return true, nil
	}

	// 未@消息计数达到阈值时补清：会话重启/闲置淘汰后重建（历史从持久层回放），
	// 若阈值是在会话未驻留期间达到的，这里清空让本轮对话从新上下文开始；
	// 无论是否清空，@ 消息都会重置连续未@计数
	p.applyNoMentionClear(ctx, msg.GroupId, chat)

	chatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.setActiveContext(msg.GroupId, true, cancel)

	// 先取出可能遗留的排队消息（上一响应结束瞬间到达、未来得及处理的），与本次消息合并；
	// 之后每轮响应结束继续排空队列，直到没有新消息为止
	batch := append(p.drainPending(msg.GroupId, true), msg)
	for len(batch) > 0 {
		if !p.processChatBatch(chatCtx, bot, msg.GroupId, true, chat, batch) {
			return false, nil
		}
		batch = p.drainPending(msg.GroupId, true)
	}
	return true, nil
}

func (p *AIChatPlugin) OnFriendMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	// 未完成初始化（如未配置 API KEY）时跳过全部处理，交由后续插件
	if !p.ready {
		return true, nil
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

	if cmd.Name == "stop" {
		// 停止当前请求、取消异步子代理，同时丢弃排队消息
		p.cancelAsyncSubagents(msg.Sender.UserId, false)
		p.drainPending(msg.Sender.UserId, false)
		if p.stopRequest(msg.Sender.UserId, false) {
			builder := msgchain.Builder().Friend()
			builder.Text("用户停止AI响应")
			bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
			p.Logger.Info("用户停止 AI 响应", "user", msg.Sender.UserId)
		} else {
			builder := msgchain.Builder().Friend()
			builder.Text("当前没有正在进行的 AI 请求")
			bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		}
		return false, nil
	}

	if cmd.Name == "plan" {
		return p.handlePlanCommand(ctx, bot, cmd, msg)
	}

	if cmd.Name == "cmd" {
		return p.handleCmdCommand(ctx, bot, cmd, msg)
	}

	// 自定义斜杠命令：命中则把消息改写为展开后的纯文本，继续走正常对话流程
	if p.commandManager != nil {
		p.commandManager.rewriteCustomCommand(cmd, &msg)
	}

	lock := p.tryLock(msg.Sender.UserId, false)
	if lock == nil {
		// 当前正在响应：消息进入排队队列，响应结束后自动合并处理
		first, ok := p.enqueuePending(msg.Sender.UserId, false, msg)
		if !ok {
			builder := msgchain.Builder().Friend()
			builder.Text("排队消息太多啦，稍后再试试吧~")
			bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		} else if first {
			builder := msgchain.Builder().Friend()
			builder.Text("正在回复上一条消息，你的消息已排队，稍后回复你~")
			bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		}
		p.touchChat(sessionKey(msg.Sender.UserId, false))
		return true, nil
	}
	p.touchChat(sessionKey(msg.Sender.UserId, false))
	defer lock.release()
	defer p.clearActiveContext(msg.Sender.UserId, false)

	chat, err := p.getChat(bot, msg.Sender.UserId, false, p.getPromptForID(msg.Sender.UserId, false))
	if err != nil {
		builder := msgchain.Builder().Friend()
		builder.Text("无法创建对话：" + aichat.SafeErrorText(err))
		bot.SendFriendMsg(msg.Sender.UserId, builder.Build())
		return true, nil
	}

	chatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.setActiveContext(msg.Sender.UserId, false, cancel)

	// 先取出可能遗留的排队消息（上一响应结束瞬间到达、未来得及处理的），与本次消息合并；
	// 之后每轮响应结束继续排空队列，直到没有新消息为止
	batch := append(p.drainPending(msg.Sender.UserId, false), msg)
	for len(batch) > 0 {
		if !p.processChatBatch(chatCtx, bot, msg.Sender.UserId, false, chat, batch) {
			return false, nil
		}
		batch = p.drainPending(msg.Sender.UserId, false)
	}
	return true, nil
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

	if strings.Contains(extraText, "#新对话") {
		if err := chat.ClearHistory(ctx); err != nil {
			p.Logger.Error("无法清理AI聊天信息", "error", err)
			return false
		}
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
	var msgFuncs llmtool.CallBackFuncs
	if isGroup {
		msgFuncs = MakeGroupCallback(b, id, lastMsg.Sender.UserId, p.Logger)
	} else {
		msgFuncs = MakeFriendCallback(b, id, p.Logger)
	}
	p.configureImageCallbacks(ctx, b, &msgFuncs, imageReg, func(u aichat.TokenUsage) {
		// 备用图片识别（OCR）消耗：并入会话统计（finishQuery 取走）与配额
		p.addExtraUsage(sessionKey(id, isGroup), u)
		p.quotaManager.Add(sessionKey(id, isGroup), u)
	}, batch...)

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

	chatOpts := p.buildChatOptions()
	// 请求级工具门禁：计划模式 → PreToolUse 钩子 → 管理员审批 → 工具审批（见 gate.go）。
	// 审批提示走纯发送闭包，与进行中的流式消息互不干扰。
	chatOpts.PreToolGate = p.buildPreToolGate(sessionKey(id, isGroup), agenthook.AgentKindMain, lastMsg.Sender.UserId,
		func(text string) { p.sendPlainText(b, id, isGroup, text) },
		p.buildAdminPromptSender(b))

	// 流式回复：平台支持「先发后改」（如飞书卡片/Telegram/Discord 消息 Patch）时逐字展示；
	// 平台不支持或流式创建失败时自动退化为一次性回复（下方原发送路径）。
	// 群聊首条流式消息携带本批全部发言者的 @ 提及（与一次性路径的提及集一致）；
	// 工具轮边界后续轮次的新消息不再重复 @，避免一次回复多次提醒用户。
	var streamHandle bot.StreamHandle
	var streamBuf strings.Builder
	streaming := false
	streamUnsupported := false
	mentionPending := isGroup
	if p.cfg.Stream.Enable {
		if ss, ok := b.(bot.StreamSender); ok {
			chatOpts.OnStreamDelta = func(delta string) {
				if delta == "" || streamUnsupported {
					return
				}
				streamBuf.WriteString(delta)
				if streamHandle != nil {
					streamHandle.Patch(aichat.RemoveThinkContent(streamBuf.String()))
					return
				}
				// 首个增量：创建流式消息（初始内容即当前已积累文本）
				if isGroup {
					builder := msgchain.Builder().Group()
					text := aichat.RemoveThinkContent(streamBuf.String())
					if mentionPending {
						mentionPending = false
						seen := make(map[message.QID]struct{}, len(batch))
						for i := range batch {
							uid := batch[i].Sender.UserId
							if uid == message.FromUint64(0) {
								continue // 跳过子代理结果等合成消息，避免 @ 到无效用户
							}
							if _, ok := seen[uid]; ok {
								continue
							}
							seen[uid] = struct{}{}
							builder.Mention(uid)
						}
						text = " " + text
					}
					builder.Text(text)
					streamHandle, streaming = ss.SendGroupStream(id, builder.Build())
				} else {
					builder := msgchain.Builder().Friend()
					builder.Text(aichat.RemoveThinkContent(streamBuf.String()))
					streamHandle, streaming = ss.SendFriendStream(id, builder.Build())
				}
				if !streaming {
					// 平台不支持流式：忽略后续增量，走一次性兜底
					streamUnsupported = true
					streamHandle = nil
				}
			}
			// 工具调用轮结束：结束当前流式消息；下一轮首个增量创建新消息
			chatOpts.OnStreamRoundEnd = func() {
				if streamHandle != nil {
					streamHandle.End()
					streamHandle = nil
				}
				// 清空已发出的累积缓冲：新消息只携带新一轮文本，
				// 否则会以「历史各轮全文 + 新文本」开头，重复发送中间内容
				streamBuf.Reset()
			}
			// 流式中途失败后的重试/切备用：清空已展示缓冲，让重试从头生成的
			// 内容整体覆盖旧输出（Patch 为覆盖语义，不会拼接重复文本）
			chatOpts.OnStreamRestart = func() {
				streamBuf.Reset()
			}
		}
	}

	recorder := p.beginQuery(chat, id, isGroup, batch, extraText)

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
	// 不变，不影响上游前缀缓存；用户消息不落盘，注入内容不会污染持久化
	// 历史）。与知识库注入叠加时记忆块在最前、知识库块居中、用户消息最后。
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

	resp, usage, err := chat.Chat(ctx, extraText, msgFuncs, chatOpts)
	// 流式回复收尾：Chat 返回后结束流式消息（幂等；工具轮边界已由 OnStreamRoundEnd 处理）
	if streamHandle != nil {
		streamHandle.End()
		streamHandle = nil
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

	// 流式已发出回复时不再走一次性发送（内容已逐字展示在流式消息中）
	if streaming {
		return true
	}

	if isGroup {
		// 群聊中 @ 本批所有发言者（去重），让排队消息的每个人都收到回应
		builder := msgchain.Builder().Group()
		seen := make(map[message.QID]struct{}, len(batch))
		for i := range batch {
			uid := batch[i].Sender.UserId
			if uid == message.FromUint64(0) {
				continue // 跳过子代理结果等合成消息，避免 @ 到无效用户
			}
			if _, ok := seen[uid]; ok {
				continue
			}
			seen[uid] = struct{}{}
			builder.Mention(uid)
		}
		builder.Text(" " + resp)
		if _, success := b.SendGroupMsg(id, builder.Build()); success {
			p.Logger.Info("发送文本", "group", id, "batch", len(batch), "text", resp)
		}
	} else {
		builder := msgchain.Builder().Friend()
		builder.Text(resp)
		if _, success := b.SendFriendMsg(id, builder.Build()); success {
			p.Logger.Info("发送文本", "user", id, "batch", len(batch), "text", resp)
		}
	}
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

// buildAdminPromptSender 构造把管理员审批提示私聊发给管理员的闭包（配置修改类
// 工具的管理员审批提示默认发到管理员私聊，管理员在私聊中回复「允许/拒绝」）。
// 管理员 ID 未设置时返回 nil；发送失败（如管理员未加机器人好友）返回 false，
// 审批管理器会回退到在发起会话内提示。
func (p *AIChatPlugin) buildAdminPromptSender(b bot.Bot) func(text string) bool {
	admin := p.SystemConfig.AdminId
	if admin == message.FromUint64(0) {
		return nil
	}
	return func(text string) bool {
		builder := msgchain.Builder().Friend()
		builder.Text(text)
		_, ok := b.SendFriendMsg(admin, builder.Build())
		return ok
	}
}

func (p *AIChatPlugin) buildChatOptions() aichat.ChatOptions {
	opts := p.thinkingOpts()
	if p.cfg.MaxToken != nil {
		opts.MaxToken = p.cfg.MaxToken
	}
	if p.cfg.Temperature != nil {
		opts.Temperature = p.cfg.Temperature
	}
	if p.cfg.TopP != nil {
		opts.TopP = p.cfg.TopP
	}
	if p.cfg.TopK != nil {
		opts.TopK = p.cfg.TopK
	}
	return opts
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
		rateLimit = 2
	}
	p.rateCh = make(chan struct{}, rateLimit)

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

	if p.cfg.Search.Token == "" {
		p.Logger.Warn("Jina AI Token 未设置，将无法使用网页浏览和搜索功能")
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
		p.approvalManager = newApprovalManager(approvalToolNames, p.cfg.Approval.TimeoutSec, p.SystemConfig.AdminId, p.Logger)
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
		MaxOutput:  p.cfg.Bash.MaxOutput,
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
		p.cfg.Search.Token,
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
		maxLog := p.cfg.Clock.MaxLogEntries
		if maxLog <= 0 {
			maxLog = 20000
		}
		p.clockManager = newClockManager(p, time.Duration(defaultTimeoutSec)*time.Second, maxLog)
		p.Logger.Info("已启用AI定时任务功能", "tasks", len(p.clockManager.List()), "default_timeout_sec", defaultTimeoutSec)
	} else {
		p.Logger.Info("AI定时任务功能未启用（plugin.ai_chat_bot.clock.enable=false）")
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

	// 对话历史行级化：按行存于 ania_chat_session/ania_chat_message
	// （增量追加只插入新行，避免整段 JSON 反复全量重写）；探测或建表失败时
	// historyDB 保持 nil，历史仅存于内存窗口（重启丢失），不影响对话
	if db, dialect, ok := storage.SQLBackend(p.PersistentStorage); ok {
		if err := storage.EnsureTables(ctx, db, dialect, chatHistoryTables...); err != nil {
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

	// 会话内存回收：闲置淘汰 + LRU 容量上限，防止活跃会话增多导致内存线性增长。
	// 淘汰只丢弃内存对象，持久化历史保留，下次发言自动重建并回放
	maxIdle := time.Duration(max(p.cfg.Session.MaxIdleMinutes, 0)) * time.Minute
	maxSessions := max(p.cfg.Session.MaxSessions, 0)
	if maxIdle > 0 || maxSessions > 0 {
		p.startChatJanitor(maxIdle, maxSessions)
		p.Logger.Info("已启用会话内存回收",
			"max_idle_minutes", p.cfg.Session.MaxIdleMinutes, "max_sessions", maxSessions)
	}

	// 全部组件就绪：放行消息事件（早退路径保持 false，处理器据此跳过）
	p.ready = true
	return nil
}

// Awake Bot 启动完成后启动定时任务调度器。
func (p *AIChatPlugin) Awake(ctx context.Context, bot bot.Bot) error {
	if p.clockManager != nil {
		p.clockManager.Start(bot)
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
