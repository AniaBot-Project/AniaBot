package pluginaichat

// 主动互动的执行路径：把一条到点的意图变成一条真正发出去的消息。
//
// 与 AI 定时任务（clock）共用同一套「一次性会话」思路：每次执行为全新的
// ChatBot（会话历史只读回放、本次结果不落盘，执行后丢弃），带完整工具能力；
// 中间轮文本丢弃，只发最终回复。区别在于：
//   - 提示词不是「完成任务」，而是「决定要不要主动说、说什么」；
//   - 多一个 skip_output 出口：AI 判断此刻没什么可说的就保持安静；
//   - 发送成功后进入回应观察窗口（见 proactive.go 的 settleOutcomes）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/aitool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// proactiveResult 一次主动发言生成的结果。
type proactiveResult struct {
	reply      string
	skipped    bool
	skipReason string
}

// execute 执行一条意图：抢会话锁 → 配额检查 → 生成 → 发送 → 落盘状态。
func (m *proactiveManager) execute(it *ProactiveIntention, forced bool) {
	p := m.plugin
	if p == nil {
		m.closeIntention(it, proactiveStatusCancelled, "插件实例不存在")
		return
	}
	if !p.ready {
		// 初始化未完成（如未配置 API Key）：推迟而不是静默返回，否则该行会
		// 一直是 pending，每分钟被 tick 重新取出执行
		m.postpone(it, time.Now().Add(proactivePostponeStep), "AI 对话尚未就绪，稍后再试")
		return
	}
	id, isGroup, ok := parseScope(it.Scope)
	if !ok || id == "" {
		m.closeIntention(it, proactiveStatusCancelled, "会话标识非法")
		return
	}

	b := m.botRef()
	if b == nil {
		m.postpone(it, time.Now().Add(proactivePostponeStep), "机器人尚未就绪")
		return
	}

	// 会话锁：与主对话共用同一把锁，保证一条会话不会被两路 AI 同时驱动
	lock, fail := p.tryLockDetailed(id, isGroup)
	if lock == nil {
		reason := "会话正忙，稍后再试"
		if fail == lockRateLimited {
			reason = "全局并发已满，稍后再试"
		}
		m.postpone(it, time.Now().Add(proactivePostponeStep), reason)
		return
	}
	defer lock.release()

	// 每日 token 配额：与普通对话、定时任务共用同一套统计，超限就不花这个钱
	if reason, denied := p.quotaManager.Check(it.Scope); denied {
		m.closeIntention(it, proactiveStatusCancelled, "配额不足："+reason)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	// 锁确认丢失时取消本次生成（与主对话路径同一策略）：此时会话可能已被
	// 他人接管，宁可这次不发言，也不要在没有锁的情况下继续生成并写回历史
	lock.attachCancel(cancel)

	start := time.Now()
	res, usage, err := m.chat(ctx, it, id, isGroup, b)
	p.quotaManager.Add(it.Scope, usage)

	if err != nil {
		it.Attempts++
		if errors.Is(err, context.DeadlineExceeded) {
			m.closeIntention(it, proactiveStatusFailed, fmt.Sprintf("执行超时（%s）", m.timeout))
			return
		}
		if it.Attempts < proactiveMaxAttempts {
			m.postpone(it, time.Now().Add(10*time.Minute), "执行失败："+aichat.SafeErrorText(err))
			return
		}
		m.closeIntention(it, proactiveStatusFailed, "执行失败："+aichat.SafeErrorText(err))
		return
	}

	if res.skipped {
		reason := strings.TrimSpace(res.skipReason)
		if reason == "" {
			reason = "未说明原因"
		}
		m.closeIntention(it, proactiveStatusCancelled, "AI 判断此刻无需发言："+reason)
		return
	}

	text := strings.TrimSpace(res.reply)
	if text == "" {
		m.closeIntention(it, proactiveStatusCancelled, "AI 没有输出可发送的内容")
		return
	}

	ok, sentAny := m.send(b, id, isGroup, text)
	if !ok {
		it.Attempts++
		// 已经发出了一部分：重试会把前面的片段再发一遍，宁可不补也不重复
		if sentAny {
			m.closeIntention(it, proactiveStatusFailed, "部分内容已发出，为避免重复不再重试")
			return
		}
		if it.Attempts < proactiveMaxAttempts {
			m.postpone(it, time.Now().Add(proactivePostponeStep), "发送失败")
			return
		}
		m.closeIntention(it, proactiveStatusFailed, "发送失败")
		return
	}

	now := time.Now()
	it.Status = proactiveStatusSent
	it.SentAt = now
	it.Reply = truncateRunes(text, proactiveMaxReplyRunes)
	it.Reason = ""
	m.storeSent(it)
	// 把这句话写回会话历史：主动互动走的是独立的一次性会话，不写回的话
	// 主对话完全不知道自己刚说过什么（用户接话时上下文里只有用户那句）
	m.recordUtterance(id, isGroup, text)
	m.noteBotSpoke(id, isGroup)

	// 进入观察窗口：窗口内有人说话记为 replied，否则 tick 里结算为 ignored
	m.mu.Lock()
	m.awaiting[it.Scope] = struct{}{}
	m.mu.Unlock()

	m.logger.Info("主动发言已发送", "id", it.ID, "scope", it.Scope, "source", it.Source,
		"forced", forced, "chars", len([]rune(text)),
		"duration", time.Since(start).Round(time.Millisecond).String(),
		"tokens", usage.TotalTokens)
}

// recordUtterance 把主动发出的内容写回会话历史，让主对话（以及重启后的下一轮）
// 知道自己说过这句话。
//
// 主动互动的生成走「一次性会话」（历史只读回放、本次结果不落盘），发出的
// 内容不会像普通回复那样由 Chat 自动落盘；不补这一步的话，用户接一句
// 「你刚才说什么」，主对话的历史里只有用户消息、没有它自己那句，AI 就只能
// 反问你指什么。
//
// 会话驻留时追加到内存窗口（同时随窗口落盘）；未驻留时直接写持久化历史，
// 下次重建会话时会随历史一起回放。历史后端不可用（未启用 SQL）时自然降级。
//
// 调用点在持有会话锁期间执行（见 execute），写入先于锁释放完成：此时主对话
// 不可能正在响应（抢锁失败的主动互动只会顺延），排队消息则要等锁释放后才被
// 消费——因此它们一定排在这条主动发言之后，历史顺序与真实发言顺序一致。
func (m *proactiveManager) recordUtterance(id message.QID, isGroup bool, text string) {
	p := m.plugin
	text = strings.TrimSpace(text)
	if p == nil || text == "" {
		return
	}
	key := sessionKey(id, isGroup)
	if v, ok := p.chats.Load(key); ok {
		if e, ok2 := v.(*chatEntry); ok2 && e.chat != nil {
			e.chat.AppendHistory(aichat.TextMessage(aichat.RoleAssistant, text))
			return
		}
	}
	if p.historyDB == nil {
		return
	}
	store := newSQLHistoryStore(p.historyDB, key, p.Logger)
	if err := store.Append(context.Background(), []aichat.Message{
		aichat.TextMessage(aichat.RoleAssistant, text),
	}); err != nil {
		p.Logger.Warn("主动发言写回会话历史失败", "key", key, "error", err.Error())
	}
}

// readOnlyHistory 只读的会话历史：Load 透传，Append/Replace/Clear 一律丢弃。
//
// 主动互动的生成需要读会话历史（否则每次开口都像第一次进这个群），但这次
// 生成本身不能写进真实历史——触发提示词、中间工具轮、最终回复都只服务这一次
// 判断，真实历史里只应出现最终真的发出去了的那句话（见 recordUtterance）。
// 包装成只读后，Chat 的窗口照常回放与追加，压缩/截断也只作用于内存副本。
type readOnlyHistory struct {
	inner aichat.HistoryStore
}

func (r readOnlyHistory) Load(ctx context.Context) ([]aichat.Message, error) {
	return r.inner.Load(ctx)
}

func (readOnlyHistory) Append(context.Context, []aichat.Message) error  { return nil }
func (readOnlyHistory) Replace(context.Context, []aichat.Message) error { return nil }
func (readOnlyHistory) Clear(context.Context) error                     { return nil }

// chat 构建一次性会话并生成「这一次要说什么」（不发送）。
func (m *proactiveManager) chat(ctx context.Context, it *ProactiveIntention, id message.QID, isGroup bool, b bot.Bot) (proactiveResult, aichat.TokenUsage, error) {
	p := m.plugin

	prompt := p.getPromptForID(id, isGroup)
	scene := p.buildScenePrompt(b, id, isGroup)

	// 每次执行独立的会话工具集（动态 MCP 工具互不影响）
	sessionExecutor := p.toolExecutor.NewSessionExecutor()
	// 历史消息：主动开口前先看看群里/私聊里到底聊了什么
	sessionExecutor.RegisterSession(llmtool.AdaptProviderTool(
		aitool.NewMsgHistoryTool(aitool.Context{Bot: b, Target: id, IsGroup: isGroup}, "get_msg_history")))

	// 记忆与知识库检索：主动开口最需要「有什么可说的」
	sessionDesc := "私聊（对方 ID " + id.String() + "）"
	if isGroup {
		sessionDesc = "群聊（会话 ID " + id.String() + "）"
	}
	if p.memoryManager != nil {
		for _, tool := range newMemoryTools(p.memoryManager, it.Scope, sessionDesc) {
			sessionExecutor.RegisterSession(tool)
		}
	}
	if p.knowledgeManager != nil {
		for _, tool := range newKnowledgeTools(p.knowledgeManager, it.Scope, sessionDesc) {
			sessionExecutor.RegisterSession(tool)
		}
	}

	// skip_output：AI 判断此刻不该开口时的唯一出口（与定时任务共用状态与工具，
	// 但描述按主动互动场景给出）
	output := &clockOutputState{}
	if skipTool := newProactiveSkipOutputTool(output); !p.toolExecutor.Has(skipTool.Name()) {
		sessionExecutor.RegisterSession(skipTool)
	} else {
		p.Logger.Warn("skip_output 工具与内置工具重名，本次主动互动不注册", "intention", it.ID)
	}

	baseURL, apiKey, model, format := p.subagentLLMConfig()
	// 会话历史只读回放：要接着之前的话题说，就得先看到聊到哪儿了；但本次生成
	// 的触发提示词与回复只留在内存窗口（执行后丢弃），不污染真实历史。
	var history aichat.HistoryStore
	if p.historyDB != nil {
		history = readOnlyHistory{inner: newSQLHistoryStore(p.historyDB, sessionKey(id, isGroup), p.Logger)}
	}
	chat, err := aichat.NewChatBot(
		baseURL, apiKey, model,
		prompt, p.cfg.MaxContextTokens, sessionExecutor, history,
		aichat.WithClientOptions(append(p.llmClientOptions(), aichat.WithAPIFormat(format))...),
		aichat.WithScenePrompt(scene),
	)
	if err != nil {
		return proactiveResult{}, aichat.TokenUsage{}, fmt.Errorf("创建对话失败: %w", err)
	}
	// 回放历史（与 getChat 同口径的超时）：加载失败按空历史继续，不能因为
	// 历史读不出来就不敢开口
	if history != nil {
		loadCtx, cancelLoad := context.WithTimeout(context.Background(), historyLoadTimeout)
		chat.LoadHistory(loadCtx)
		cancelLoad()
	}
	chat.SetMaxIterations(p.mainMaxIterations())
	if p.skillManager != nil {
		chat.SetSkillManager(p.skillManager)
	}

	// 钩子与工具门禁：复用「后台一次性会话」身份（clock），
	// requester 为 0 时审批仅管理员可批
	if p.hookManager != nil {
		chat.SetHookRunner(p.hookManager, sessionKey(id, isGroup), agenthook.AgentKindClock)
	}
	chatOpts := p.buildChatOptions()
	chatOpts.PreToolGate = p.buildPreToolGate(sessionKey(id, isGroup), agenthook.AgentKindClock,
		message.FromUint64(0),
		func(text string) { p.sendPlainText(b, id, isGroup, text) },
		p.buildAdminPromptSender(b))

	var extra usageAcc
	resp, usage, err := chat.Chat(ctx, m.buildPrompt(it, id, isGroup), m.callbacks(ctx, b, id, isGroup, &extra), chatOpts)
	usage = mergeTokenUsage(usage, extra.take())
	if err != nil {
		return proactiveResult{}, usage, err
	}

	// Stop 钩子（仅通知）：一次主动发言尝试结束
	if p.hookManager != nil {
		p.hookManager.Run(ctx, agenthook.EventStop, agenthook.Payload{
			SessionKey: sessionKey(id, isGroup),
			AgentKind:  agenthook.AgentKindClock,
			Prompt:     truncateRunes(resp, 1000),
		})
	}

	skipped, skipReason := output.skipped()
	return proactiveResult{reply: resp, skipped: skipped, skipReason: skipReason}, usage, nil
}

// buildPrompt 构造「要不要主动说话」的触发内容。
//
// 提示词直接决定行为质量：明确要求短、有具体由头、不寒暄、重复不说、
// 没什么可说就 skip_output。冷场与 AI 自建意图的措辞略有不同。
func (m *proactiveManager) buildPrompt(it *ProactiveIntention, id message.QID, isGroup bool) string {
	now := time.Now()
	scene := "私聊"
	if isGroup {
		scene = "群聊"
	}

	var sb strings.Builder
	sb.WriteString("【主动互动】现在是 ")
	sb.WriteString(now.Format("2006-01-02 15:04"))
	sb.WriteString(" ")
	sb.WriteString(weekdayCN(now.Weekday()))
	sb.WriteString("，你在一个")
	sb.WriteString(scene)
	sb.WriteString("里，没有人在跟你说话。\n")

	switch it.Source {
	case proactiveSourceIdle:
		idle := "一段时间"
		if m.idle > 0 {
			idle = formatDurationCN(m.idle)
		}
		sb.WriteString("这里已经安静了 ")
		sb.WriteString(idle)
		sb.WriteString("，现在轮到你判断：要不要主动开口，以及说什么。\n")
	case proactiveSourceAI:
		sb.WriteString("这是你之前在对话里记下、现在到点了的一件想主动说的事：\n")
		sb.WriteString(strings.TrimSpace(it.Prompt))
		sb.WriteString("\n")
	case proactiveSourceManual:
		sb.WriteString("管理员手动触发了一次主动发言")
		if strings.TrimSpace(it.Prompt) != "" {
			sb.WriteString("，希望你说：")
			if strings.TrimSpace(it.Note) != "" {
				sb.WriteString("\n")
				sb.WriteString(strings.TrimSpace(it.Prompt))
				sb.WriteString("\n（备注：")
				sb.WriteString(strings.TrimSpace(it.Note))
				sb.WriteString("）\n")
			} else {
				sb.WriteString("\n")
				sb.WriteString(strings.TrimSpace(it.Prompt))
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString("。\n")
		}
	}
	if strings.TrimSpace(it.Note) != "" && it.Source != proactiveSourceManual {
		sb.WriteString("（备注：")
		sb.WriteString(strings.TrimSpace(it.Note))
		sb.WriteString("）\n")
	}

	if spoken := m.recentUtterances(it.Scope, proactiveRecentUtterances); spoken != "" {
		sb.WriteString("\n你最近主动说过的话（别重复，也不用装作没说过）：\n")
		sb.WriteString(spoken)
	}

	if ctxText := m.scopeContext(it.Scope); ctxText != "" {
		sb.WriteString("\n最近的消息：\n")
		sb.WriteString(ctxText)
	}

	sb.WriteString("\n要求：\n")
	sb.WriteString("- 像人一样说话：1~2 句，短，口语，不要用「根据记录」「温馨提示」这类汇报腔，不要分点罗列。\n")
	sb.WriteString("- 必须有具体由头：跟进之前聊过的事、问一句关心的话、提醒某件事、或分享刚查到的有用信息。不要泛泛寒暄，不要为了说话而说话。\n")
	sb.WriteString("- 不要重复你最近已经说过的话；群聊里不要 @ 所有人。\n")
	sb.WriteString("- 需要回忆以前的事可以先用 memory_search；需要资料可以用 kb_search；想看更早的消息可以用 get_msg_history。\n")
	sb.WriteString("- 如果此刻确实没什么可说的、或者这个话题已经过去了，调用 skip_output 保持安静——不说话也是正确的选择。\n")
	return sb.String()
}

// scopeContext 取该会话最近的现场记录（执行时现取，排队期间的新消息也算）。
func (m *proactiveManager) scopeContext(scope string) string {
	if snap := m.snapshot(scope); snap != nil {
		return strings.TrimSpace(snap.Context)
	}
	return ""
}

// callbacks 构造主动互动执行用的工具回调。
// 只有最终回复会外发：中间轮文本仅记日志，避免一次主动开口变成刷屏。
//
// 与定时任务（makeClockCallback）保持同一套能力：文件发送、私聊文件链接、本地
// 图片与命令级人工审批都要注入——缺 SendFile 时启用 file 工具会空函数调用
// panic，缺 RequestApproval 会让 bash 未列名命令绕过审批。
func (m *proactiveManager) callbacks(ctx context.Context, b bot.Bot, id message.QID, isGroup bool, extra *usageAcc) llmtool.CallBackFuncs {
	logger := m.logger
	p := m.plugin
	var loadedImages []string
	cbs := llmtool.CallBackFuncs{
		SendText: func(s string) (string, error) {
			logger.Info("主动互动丢弃中间轮文本（未主动发送）", "text", truncateRunes(s, 200))
			return "已记录，未发送", nil
		},
		SendImage: func(bs64 string) (string, error) {
			if !m.sendImage(b, id, isGroup, bs64) {
				return "", fmt.Errorf("发送失败")
			}
			return "发送成功", nil
		},
		SendFile: func(name, bs64 string) (string, error) {
			if !m.sendFile(b, id, isGroup, name, bs64) {
				return "", fmt.Errorf("发送失败（也可能是上传超时、内容稍后仍会送达，请勿重复发送）")
			}
			return "发送成功", nil
		},
		GetPrivateFileURL: func(fileId string) (string, error) {
			if isGroup {
				return "", fmt.Errorf("当前为群聊主动互动，无法获取私聊文件")
			}
			qb := botQQ(b)
			if qb == nil {
				return "", fmt.Errorf("当前平台不支持获取私聊文件URL")
			}
			url, ok := qb.GetPrivateFileURL(id, fileId)
			if !ok {
				return "", fmt.Errorf("获取私聊文件URL失败")
			}
			return url, nil
		},
		LoadImages: func(_ []string) (string, error) {
			return "当前为主动互动触发，没有可以直接加载的消息图片；可以用 get_msg_history 查看历史", nil
		},
		TakeLoadedImages: func() []string {
			imgs := loadedImages
			loadedImages = nil
			return imgs
		},
	}
	// 本地图片（主模型多模态或配置了备用识别模型时可用），与定时任务同一套实现
	if p != nil {
		p.attachLocalImageCallback(&cbs, ctx, &loadedImages, extra.add)
	}
	// 命令级人工审批（bash 三段式）：主动互动无人值守，requester=0 即仅管理员可批，
	// 提示发到本会话（与定时任务同口径）
	if p != nil && p.cfg.Approval.Enable && p.approvalManager != nil {
		cbs.RequestApproval = func(ctx context.Context, toolName, summary string) (bool, string) {
			return p.approvalManager.request(ctx, sessionKey(id, isGroup), toolName, summary,
				message.FromUint64(0), func(text string) { p.sendPlainText(b, id, isGroup, text) })
		}
	}
	return cbs
}

// send 发送主动发言内容。开启「按空行拆分回复」时拆成多条，与普通回复一致。
// 返回是否全部发送成功，以及是否已经发出至少一段——后者用于区分「完全没
// 发出去（可以重试）」与「部分已发出（重试会导致重复）」。
func (m *proactiveManager) send(b bot.Bot, id message.QID, isGroup bool, text string) (ok, sentAny bool) {
	if b == nil {
		return false, false
	}
	split := m.plugin != nil && m.plugin.cfg.Reply.SplitBlankLine
	for _, seg := range replySegments(text, split) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if !m.sendText(b, id, isGroup, seg) {
			return false, sentAny
		}
		sentAny = true
	}
	return true, sentAny
}

// sendText 发送一段纯文本，返回是否成功。
func (m *proactiveManager) sendText(b bot.Bot, id message.QID, isGroup bool, text string) bool {
	if b == nil {
		return false
	}
	if isGroup {
		builder := msgchain.Builder().Group()
		builder.Text(text)
		_, ok := b.SendGroupMsg(id, builder.Build())
		return ok
	}
	builder := msgchain.Builder().Friend()
	builder.Text(text)
	_, ok := b.SendFriendMsg(id, builder.Build())
	return ok
}

// sendImage 发送 base64 图片，返回是否成功。
func (m *proactiveManager) sendImage(b bot.Bot, id message.QID, isGroup bool, bs64 string) bool {
	if b == nil {
		return false
	}
	if isGroup {
		builder := msgchain.Builder().Group()
		builder.ImageBase64(bs64)
		_, ok := b.SendGroupMsg(id, builder.Build())
		return ok
	}
	builder := msgchain.Builder().Friend()
	builder.ImageBase64(bs64)
	_, ok := b.SendFriendMsg(id, builder.Build())
	return ok
}

// sendFile 发送 base64 文件，返回是否成功。
func (m *proactiveManager) sendFile(b bot.Bot, id message.QID, isGroup bool, name, bs64 string) bool {
	if b == nil {
		return false
	}
	if isGroup {
		builder := msgchain.Builder().Group()
		builder.FileBase64(name, bs64)
		_, ok := b.SendGroupMsg(id, builder.Build())
		return ok
	}
	builder := msgchain.Builder().Friend()
	builder.FileBase64(name, bs64)
	_, ok := b.SendFriendMsg(id, builder.Build())
	return ok
}

// weekdayCN 返回中文星期。
func weekdayCN(d time.Weekday) string {
	switch d {
	case time.Monday:
		return "周一"
	case time.Tuesday:
		return "周二"
	case time.Wednesday:
		return "周三"
	case time.Thursday:
		return "周四"
	case time.Friday:
		return "周五"
	case time.Saturday:
		return "周六"
	default:
		return "周日"
	}
}

// formatDurationCN 把时长转成「一个多小时 / 45 分钟」这类口语描述。
func formatDurationCN(d time.Duration) string {
	if d < time.Minute {
		return "一会儿"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	}
	hours := int(d.Hours())
	rest := int(d.Minutes()) - hours*60
	if rest == 0 {
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, rest)
}
