package pluginaichat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

const (
	// subagentMaxTimeoutSec 子代理单次执行的超时上限（秒），防止 AI 传入离谱的 timeout_sec
	subagentMaxTimeoutSec = 1800
	// subagentParentReserve 为主请求预留的收尾时间。框架对单次消息处理有总预算
	// （bot.msg_event_timeout_sec，默认 5min，见 core.MsgEventTimeout），子代理超时
	// 必须早于该预算触发，超时才能作为
	// 工具结果优雅返回、让主 AI 用剩余时间完成最终回复；否则父 deadline 先触发，
	// 整个主请求会以「请求超时」中止
	subagentParentReserve = 30 * time.Second
)

// subagentTimeout 子代理/团队成员/定时任务的默认执行超时：跟随主请求预算——
// 框架级消息处理超时（bot.msg_event_timeout_sec，Start 时读入 p.msgEventTimeout）。
// 单次调用可用 timeout_sec 覆盖；实际执行还会按父请求剩余预算压缩
// （见 resolveSubagentTimeout）。
func (p *AIChatPlugin) subagentTimeout() time.Duration {
	if p.msgEventTimeout > 0 {
		return p.msgEventTimeout
	}
	// Start 未执行（如测试）时的兜底，与 core.MsgEventTimeout 的默认值一致
	return 5 * time.Minute
}

// defaultSubagentPrompt 子代理的系统提示词：子代理是主 AI 委派任务的一次性工作者，
// 无历史对话上下文，最终文本结果返回给主 AI 而非直接发给用户。
// 可用能力不在此枚举——具体工具以会话实际注册的工具列表为准（部分功能受配置门控）。
const defaultSubagentPrompt = `你是一个子代理，由主 AI 委派执行一个具体任务。

## 工作方式
- 你没有历史对话上下文，任务描述中包含了完成任务所需的全部信息
- 你可以使用提供的工具完成任务，具体以实际可用的工具列表为准
- 你的最终文本回复会作为执行结果返回给主 AI，请输出完整、可直接使用的结果

## 注意
- 只输出与任务相关的内容，不要寒暄
- 你执行过程中的中间消息不会发送给任何人`

// resolveSubagentTimeout 计算子代理单次执行的实际超时：
//  1. timeoutSec>0 时覆盖默认值（先限幅 int 再乘 time.Second，防溢出绕过上限）；
//  2. 父上下文带 deadline 时（框架消息处理总预算），再为主请求预留
//     subagentParentReserve 收尾时间压缩超时——保证子代理超时先触发，以工具结果
//     优雅返回；剩余预算不足时直接报错，不启动子代理。
func resolveSubagentTimeout(defaultTimeout time.Duration, timeoutSec int, parentCtx context.Context) (time.Duration, error) {
	timeout := defaultTimeout
	if timeoutSec > 0 {
		if timeoutSec > subagentMaxTimeoutSec {
			timeoutSec = subagentMaxTimeoutSec
		}
		timeout = time.Duration(timeoutSec) * time.Second
	}
	if deadline, ok := parentCtx.Deadline(); ok {
		budget := time.Until(deadline) - subagentParentReserve
		if budget <= 0 {
			return 0, fmt.Errorf("当前请求剩余时间不足，无法启动子代理")
		}
		if timeout > budget {
			timeout = budget
		}
	}
	return timeout, nil
}

// runSubagent 同步执行一次子代理任务（在主 AI 的工具调用内等待完成）。
//
// 参照 clock 的 executeTask 模式构建全新的一次性 ChatBot：nil historyStore（不持久化、
// 执行后丢弃）、独立的 SessionToolExecutor（动态 MCP 工具互不影响）。与主会话的区别：
// 不注册 subagent 工具自身（防止递归委派），中间轮文本丢弃不发给用户，图片加载状态
// 独立（不与主会话互踩，见 makeSubagentCallbacks）。
//
// ctx 为主会话的请求上下文：/stop 取消主请求时子代理一并取消；timeoutSec<=0 用默认超时。
// 返回的 TokenUsage 含子代理工具循环与其派生调用（备用图片识别）的全部消耗
// （失败/取消时返回已产生的部分用量）；统计与配额的归集由调用方负责：
// 会话路径计入会话级暂存与配额，定时任务路径并入任务总用量。
func (p *AIChatPlugin) runSubagent(ctx context.Context, b bot.Bot, id message.QID, isGroup bool, task string, timeoutSec int, parentCbs llmtool.CallBackFuncs) (string, aichat.TokenUsage, error) {
	return p.runSubagentWithOptions(ctx, b, id, isGroup, task, subagentRunOptions{timeoutSec: timeoutSec}, parentCbs)
}

// subagentRunOptions 子代理（含团队成员）单次执行的定制项：除提示词与单次
// 超时覆盖外，其余执行参数（模型、生成参数、轮数）一律跟随主模型。
type subagentRunOptions struct {
	prompt     string // 系统提示词，空用 defaultSubagentPrompt（团队成员传角色提示词）
	timeoutSec int    // 本次调用覆盖的秒数（先限幅再乘 time.Second），<=0 用默认超时
}

// runSubagentWithOptions 泛化的子代理执行：主体即原 runSubagent，差异仅在于
// 提示词（Agent 团队成员的并行执行复用它，传入角色提示词）。执行参数跟随主模型：
// 模型与生成参数、工具轮数取主模型配置，超时取框架消息处理预算（见
// subagentTimeout / resolveSubagentTimeout）。调用方不应预建带超时的 context，
// 否则会被二次压缩损失预算。
func (p *AIChatPlugin) runSubagentWithOptions(ctx context.Context, b bot.Bot, id message.QID, isGroup bool, task string, o subagentRunOptions, parentCbs llmtool.CallBackFuncs) (string, aichat.TokenUsage, error) {
	timeout, err := resolveSubagentTimeout(p.subagentTimeout(), o.timeoutSec, ctx)
	if err != nil {
		return "", aichat.TokenUsage{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 一次性会话：注册与主会话一致的会话级工具（clock/memory），但不注册 subagent 自身
	sessionExecutor := p.toolExecutor.NewSessionExecutor()
	p.registerScopedTools(sessionExecutor, id, isGroup)
	// 子代理继承父会话来源平台的专属工具（与主会话一致，见 platformtools.go）
	p.registerPlatformTools(sessionExecutor, b, id, isGroup)

	prompt := o.prompt
	if prompt == "" {
		prompt = defaultSubagentPrompt
	}
	// 场景描述经 WithScenePrompt 注入（组装时排在 available_skills 之后，见
	// MessageBuilder.buildSystemPrompt），与主会话/定时任务的 system 组装顺序一致
	scene := p.buildScenePrompt(b, id, isGroup)
	// 子代理模型跟随主模型（见 subagentLLMConfig）；团队成员同样经此路径
	// （team.go 复用本函数）
	saBaseURL, saAPIKey, saModel, saFormat := p.subagentLLMConfig()
	chat, err := aichat.NewChatBot(
		saBaseURL, saAPIKey, saModel,
		prompt, p.cfg.MaxContextTokens, sessionExecutor, nil,
		aichat.WithClientOptions(append(p.llmClientOptions(), aichat.WithAPIFormat(saFormat))...),
		aichat.WithScenePrompt(scene),
	)
	if err != nil {
		return "", aichat.TokenUsage{}, fmt.Errorf("创建子代理失败: %w", err)
	}
	if p.skillManager != nil {
		chat.SetSkillManager(p.skillManager)
	}
	chat.SetMaxIterations(p.mainMaxIterations())

	// 钩子与工具门禁：子代理同样走 PreToolUse/PostToolUse 等钩子与计划模式/审批门禁
	// （AgentKind=subagent 供钩子配置区分）；门禁审批路径仅管理员可批（requester=0），
	// 管理员审批提示私聊发给管理员（回退时发到当前会话）
	sKey := sessionKey(id, isGroup)
	if p.hookManager != nil {
		chat.SetHookRunner(p.hookManager, sKey, agenthook.AgentKindSubagent)
		// SubagentStop 钩子（仅通知）：同步/异步/团队/定时任务子代理都经本函数执行
		defer func() {
			p.hookManager.Run(context.Background(), agenthook.EventSubagentStop, agenthook.Payload{
				SessionKey: sKey,
				AgentKind:  agenthook.AgentKindSubagent,
				Prompt:     querylog.Truncate(task, 1000),
			})
		}()
	}

	logger := p.Logger.WithGroup("subagent")
	// 工具回调派生的 LLM 消耗（备用图片识别）累加到 extra，收尾时并入本次用量
	extra := &usageAcc{}
	cbs := p.makeSubagentCallbacks(runCtx, parentCbs, logger, extra.add)

	logger.Info("子代理开始执行", "id", id, "is_group", isGroup, "timeout", timeout, "task", task)
	start := time.Now()
	chatOpts := p.buildChatOptions()
	chatOpts.PreToolGate = p.buildPreToolGate(sKey, agenthook.AgentKindSubagent, message.FromUint64(0),
		func(text string) { p.sendPlainText(b, id, isGroup, text) },
		p.buildAdminPromptSender(b))
	resp, usage, err := chat.Chat(runCtx, "【子代理任务】\n"+task, cbs, chatOpts)
	duration := time.Since(start)
	usage = mergeTokenUsage(usage, extra.take())
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			logger.Warn("子代理执行超时", "id", id, "is_group", isGroup, "timeout", timeout)
			return "", usage, fmt.Errorf("子代理执行超时（%s），已中止", timeout)
		case ctx.Err() != nil:
			// 主请求被 /stop 取消或父上下文结束，原样返回让上层按取消处理
			return "", usage, ctx.Err()
		default:
			logger.Warn("子代理执行失败", "id", id, "is_group", isGroup, "error", err.Error())
			// 回填给主 AI 的失败原因用脱敏后的用户文案（SDK 错误原文含请求
			// URL 与原始响应 JSON，可能带密钥，不适合进入对话链路）
			return "", usage, fmt.Errorf("%s", aichat.UserErrorText(err))
		}
	}

	// 结果不截断：子代理产出多长就回填多长，避免长结论被腰斩
	logger.Info("子代理执行完成", "id", id, "is_group", isGroup,
		"duration", duration, "iterations", usage.Iterations, "tokens", usage.TotalTokens, "result_runes", utf8.RuneCountInString(resp))

	meta := fmt.Sprintf("【子代理执行完成】耗时 %.1fs · LLM 轮数 %d · token %d",
		duration.Seconds(), usage.Iterations, usage.TotalTokens)
	if resp == "" {
		return meta + "\n（子代理没有返回内容）", usage, nil
	}
	return meta + "\n" + resp, usage, nil
}

// makeSubagentCallbacks 构造子代理的工具回调。
//
// 发送类能力（SendImage/SendFile）与只读能力（GetPrivateFileURL）透传主会话
// 回调——子代理与主 AI 处于同一会话场景。历史消息查询不经回调：子代理会话
// 已注入平台历史消息工具（registerPlatformTools），与主会话同实现。
// SendText 替换为丢弃中间轮文本（记日志）：子代理的结论通过最终结果返回，
// 不直接打扰用户。
//
// 图片加载三回调（LoadImages/TakeLoadedImages/LoadLocalImage）不透传：它们闭包捕获的
// 是主请求级的 loadedImages/loadedHashes 状态（见 utils.go configureImageCallbacks），
// 透传会让主/子两个工具循环排干同一图片队列（主 AI 加载的图片被偷进子代理上下文，
// 或子代理毒化已加载标志导致主 AI 无法再加载用户图片）。参照 clock 的
// makeClockCallback 模式，为子代理建立独立的图片状态。
//
// usageSink 接收工具回调派生的 LLM 用量（备用图片识别），由调用方并入子代理总用量。
func (p *AIChatPlugin) makeSubagentCallbacks(ctx context.Context, parent llmtool.CallBackFuncs, logger *slog.Logger, usageSink func(aichat.TokenUsage)) llmtool.CallBackFuncs {
	var loadedImages []string
	cbs := llmtool.CallBackFuncs{
		SendText: func(s string) (string, error) {
			logger.Info("子代理丢弃中间轮文本（未发送）", "text", s)
			return "已记录，未发送", nil
		},
		SendImage:         parent.SendImage,
		SendFile:          parent.SendFile,
		GetPrivateFileURL: parent.GetPrivateFileURL,
		// 命令级人工审批透传父会话（同 SendImage 先例）：子代理内 bash 未列名命令
		// 的审批提示发到父会话，权限判断沿用父会话的 requester（定时任务触发的
		// 子代理其父回调 requester=0，天然仅管理员可批）
		RequestApproval: parent.RequestApproval,
		// 用户消息图片的加载状态属于主请求；子代理确需查看时在结果中说明，由主 AI 自行加载
		LoadImages: func(hashes []string) (string, error) {
			if len(hashes) > 0 {
				return "子代理无法直接加载图片（" + strings.Join(hashes, "、") + "）；如确需查看，请在最终结果中说明，由主 AI 自行调用 load_images 并传入对应图片哈希", nil
			}
			return "子代理无法直接加载图片；如确需查看，请在最终结果中说明，由主 AI 自行调用 load_images 并传入对应图片哈希", nil
		},
		TakeLoadedImages: func() []string {
			imgs := loadedImages
			loadedImages = nil
			return imgs
		},
	}
	// 本地图片读取使用子代理独立的图片队列（主模型多模态或配置了备用识别模型时可用）
	if p.cfg.Multimodal || p.ocrModel != nil {
		cbs.LoadLocalImage = func(path string) (string, error) {
			return p.loadLocalImageInto(ctx, path, &loadedImages, usageSink), nil
		}
	}
	return cbs
}
