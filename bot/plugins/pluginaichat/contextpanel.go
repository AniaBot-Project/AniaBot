package pluginaichat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// 面板「上下文管理」页数据源（实现 adminpanel.ContextSource）。
//
// 每个群聊 / 私聊会话对应一份独立上下文：system prompt（静态提示词 +
// available_skills + 场景描述）+ 消息历史（含工具调用与工具结果）+ 工具定义。
// 面板把这套内容原样展示，用于核对「模型实际看到了什么」与评估 token 占用，
// 并提供删除会话 / 清空上下文的入口。
//
// 数据来源分两层：
//   - 内存（p.chats）：驻留会话的实时消息窗口、系统提示词与工具定义，最准确；
//   - 持久化（ania_chat_session / ania_chat_message）：会话索引与历史。
//
// 会话列表取两者的并集（有历史但已被淘汰的会话仍可查看）；详情优先取内存快照，
// 会话正在执行请求（消息窗口被本轮占用）时退回持久化快照，不阻塞面板请求。
//
// 清空 / 删除是写操作，必须与回合运行器互斥：同一会话的 ChatBot 由会话锁保证
// 只被一个执行者驱动。流程是「按需终止在跑请求 → 轮询会话锁 → 拿到锁后清空」，
// 避免「内存已清、DB 未清」或与在途回合互相覆盖。

const (
	// contextClearLockWait 清空 / 删除前等待会话锁释放的上限：终止请求后回合
	// 通常在一个 LLM 请求的错误返回内结束，留出足够余量又不至于让面板请求久挂。
	contextClearLockWait = 15 * time.Second
	// contextClearLockPoll 会话锁轮询间隔。
	contextClearLockPoll = 150 * time.Millisecond
	// contextDBTimeout 单次面板读 / 写数据库的超时（避免 DB 卡住面板请求）。
	contextDBTimeout = 10 * time.Second
	// contextSessionListLimit 面板列表最多返回的会话数（按最近活跃倒序取前 N 个）。
	contextSessionListLimit = 2000
)

// contextSessionMeta 会话在持久化层的元信息。
type contextSessionMeta struct {
	msgCount  int
	createdAt int64
	updatedAt int64
}

// ContextSessions 返回全部会话的上下文摘要（持久化会话 ∪ 内存驻留会话），
// 实现 adminpanel.ContextSource。按最近活跃倒序排列，正在执行的会话排在最前。
func (p *AIChatPlugin) ContextSessions() []plugininfo.ContextSession {
	metas := p.contextSessionMetaAll()
	keys := make([]string, 0, len(metas))
	for key := range metas {
		keys = append(keys, key)
	}
	p.chats.Range(func(k, _ any) bool {
		key, ok := k.(string)
		if !ok {
			return true
		}
		if _, exists := metas[key]; !exists {
			keys = append(keys, key)
		}
		return true
	})

	out := make([]plugininfo.ContextSession, 0, len(keys))
	for _, key := range keys {
		out = append(out, p.contextSessionInfo(key, metas[key]))
	}
	// 正在执行 > 最近活跃 > 最近写入 > 键名，保证顺序稳定
	sort.Slice(out, func(i, j int) bool {
		if out[i].Busy != out[j].Busy {
			return out[i].Busy
		}
		if out[i].LastActive != out[j].LastActive {
			return out[i].LastActive > out[j].LastActive
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > contextSessionListLimit {
		out = out[:contextSessionListLimit]
	}
	return out
}

// contextSessionInfo 组装单个会话的摘要。meta 为持久化层元信息（不存在时为零值）。
func (p *AIChatPlugin) contextSessionInfo(key string, meta contextSessionMeta) plugininfo.ContextSession {
	info := plugininfo.ContextSession{
		Key:          key,
		Kind:         "friend",
		MessageCount: meta.msgCount,
		CreatedAt:    meta.createdAt,
		UpdatedAt:    meta.updatedAt,
	}
	id, isGroup, ok := parseSessionKey(key)
	if !ok {
		// 非 g:/f: 会话键不在此页展示（如试聊室的 panelchat: 历史由试聊室页管理）
		info.Target = key
		return info
	}
	if isGroup {
		info.Kind = "group"
	}
	info.Target = id.String()

	if v, ok := p.chats.Load(key); ok {
		if e, ok2 := v.(*chatEntry); ok2 && e.chat != nil {
			info.Resident = true
			info.LastActive = e.lastActive.Load()
			// 驻留时以内存窗口为准：持久化的 msg_count 只在每轮结束时推进
			if n, ok3 := e.chat.HistoryCount(); ok3 {
				info.MessageCount = n
			}
		}
	}
	busy, stage := p.contextRuntimeStage(key)
	info.Busy = busy
	info.Stage = stage
	info.Pending = p.pendingCount(key)
	info.Subagents = len(p.listRunningSubagents(id, isGroup))
	if p.planManager != nil {
		info.PlanMode = p.planManager.IsOn(key)
	}
	return info
}

// contextRuntimeStage 返回会话是否正在执行请求与当前处理阶段。
// 只认 activeContexts（回合已登记的运行态）：会话锁可能在「建会话 / 回放历史」
// 阶段被短暂持有却还没登记阶段，那属于瞬时窗口，面板不必据此报「执行中」。
func (p *AIChatPlugin) contextRuntimeStage(key string) (bool, string) {
	v, ok := p.activeContexts.Load(key)
	if !ok {
		return false, ""
	}
	ar, ok := v.(*activeRequest)
	if !ok || ar == nil {
		return false, ""
	}
	stage, _ := ar.stageSnapshot()
	return true, stage
}

// contextSessionMetaAll 读取全部会话的持久化元信息（键含 panelchat: 试聊历史，
// 由调用方按会话键过滤）。SQL 不可用或查询失败时返回空表（面板按空列表降级）。
func (p *AIChatPlugin) contextSessionMetaAll() map[string]contextSessionMeta {
	metas := make(map[string]contextSessionMeta)
	if p.historyDB == nil {
		return metas
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextDBTimeout)
	defer cancel()
	rows, err := p.historyDB.QueryContext(ctx,
		`SELECT session_id, msg_count, created_at, updated_at FROM ania_chat_session `+
			`ORDER BY updated_at DESC, session_id ASC LIMIT ?`, contextSessionListLimit)
	if err != nil {
		p.contextLogger().Warn("面板读取会话索引失败", "error", err.Error())
		return metas
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key              string
			msgCount         int
			created, updated string
		)
		if err := rows.Scan(&key, &msgCount, &created, &updated); err != nil {
			p.contextLogger().Warn("面板解析会话索引失败", "error", err.Error())
			return metas
		}
		if _, _, ok := parseSessionKey(key); !ok {
			continue // 试聊室历史（panelchat:）不在此页展示
		}
		metas[key] = contextSessionMeta{
			msgCount:  msgCount,
			createdAt: parseSQLTimestamp(created),
			updatedAt: parseSQLTimestamp(updated),
		}
	}
	if err := rows.Err(); err != nil {
		p.contextLogger().Warn("面板遍历会话索引失败", "error", err.Error())
	}
	return metas
}

// contextSessionMeta 读取单个会话的持久化元信息；不存在时 ok=false。
func (p *AIChatPlugin) contextSessionMeta(key string) (contextSessionMeta, bool) {
	if p.historyDB == nil {
		return contextSessionMeta{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextDBTimeout)
	defer cancel()
	var (
		msgCount         int
		created, updated string
	)
	err := p.historyDB.QueryRowContext(ctx,
		`SELECT msg_count, created_at, updated_at FROM ania_chat_session WHERE session_id = ?`, key).
		Scan(&msgCount, &created, &updated)
	if err != nil {
		if err != sql.ErrNoRows {
			p.contextLogger().Warn("面板读取会话元信息失败", "key", key, "error", err.Error())
		}
		return contextSessionMeta{}, false
	}
	return contextSessionMeta{
		msgCount:  msgCount,
		createdAt: parseSQLTimestamp(created),
		updatedAt: parseSQLTimestamp(updated),
	}, true
}

// parseSQLTimestamp 解析 SQL 后端返回的时间文本（SQLite 的 TEXT 与 MySQL 的
// TIMESTAMP 扫描结果都是 "2006-01-02 15:04:05"）。解析失败返回 0，面板按「未知」
// 展示，不影响其余字段。
func parseSQLTimestamp(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	layouts := []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// ContextDetail 返回单个会话的完整上下文，实现 adminpanel.ContextSource。
//
// 驻留且空闲的会话取实时快照（system prompt 分段 + 消息窗口 + 工具定义）；
// 正在执行请求或已被淘汰时退回持久化历史，系统提示词按当前配置重建。
func (p *AIChatPlugin) ContextDetail(key string) (plugininfo.ContextDetail, error) {
	id, isGroup, ok := parseSessionKey(key)
	if !ok {
		return plugininfo.ContextDetail{}, fmt.Errorf("无效的会话键：%s", key)
	}
	meta, hasMeta := p.contextSessionMeta(key)
	entry := p.contextChatEntry(key)
	if entry == nil && !hasMeta {
		return plugininfo.ContextDetail{}, fmt.Errorf("会话不存在或已无上下文：%s", key)
	}

	detail := plugininfo.ContextDetail{
		ContextSession: p.contextSessionInfo(key, meta),
		SessionToken:   p.sessionTokenPeek(key),
	}

	snap, live := p.contextSnapshot(entry)
	if live {
		detail.Source = plugininfo.ContextSourceLive
		detail.System = contextSystemSections(snap.System)
		detail.Messages = contextMessageViews(snap.Messages)
		detail.Tools = contextToolViews(snap.Tools)
		detail.LastPromptTokens = snap.LastPromptTokens
		detail.MaxContextTokens = snap.MaxContextTokens
		detail.Hint = "实时上下文：系统提示词、消息窗口与工具定义取自驻留内存的会话对象。"
	} else {
		detail.Source = plugininfo.ContextSourcePersisted
		prompt, skills, scene := p.contextRebuiltSystem(id, isGroup)
		detail.System = contextSystemSections(aichat.SystemContextSnapshot{
			Prompt: prompt,
			Skills: skills,
			Scene:  scene,
		})
		detail.Messages = contextMessageViews(p.contextPersistedMessages(key))
		detail.MaxContextTokens = p.cfg.MaxContextTokens
		if entry != nil {
			detail.Hint = "会话正在执行请求，本轮消息窗口不可读：这里展示持久化快照，本轮新增内容与工具定义会在回合结束后可见；系统提示词按当前配置重建。"
		} else {
			detail.Hint = "会话未驻留内存（进程重启或闲置回收）：历史来自持久化存储，系统提示词按当前配置重建；工具定义需会话驻留后可见（任意一条消息即可重建会话）。"
		}
	}
	if p.todoManager != nil {
		detail.Todo = contextTodoViews(p.todoManager.snapshot(key))
	}
	contextSumStats(&detail)
	return detail, nil
}

// contextSnapshot 取会话的实时上下文快照；entry 为 nil 或快照不可用时 live=false。
func (p *AIChatPlugin) contextSnapshot(entry *chatEntry) (aichat.ContextSnapshot, bool) {
	if entry == nil || entry.chat == nil {
		return aichat.ContextSnapshot{}, false
	}
	snap, ok := entry.chat.ContextSnapshot()
	return snap, ok
}

// contextChatEntry 读取会话缓存条目（不存在返回 nil）。
func (p *AIChatPlugin) contextChatEntry(key string) *chatEntry {
	v, ok := p.chats.Load(key)
	if !ok {
		return nil
	}
	e, ok := v.(*chatEntry)
	if !ok {
		return nil
	}
	return e
}

// sessionTokenPeek 只读探测会话实例 token（不存在时不生成，避免查看上下文
// 这类只读操作改变请求头取值）。
func (p *AIChatPlugin) sessionTokenPeek(key string) string {
	if v, ok := p.sessionTokens.Load(key); ok {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return ""
}

// contextRebuiltSystem 按当前配置重建会话的 system prompt 分段（会话未驻留或
// 正在执行时使用）。场景描述里的群名需要平台适配器，拿不到时降级为仅会话 ID。
func (p *AIChatPlugin) contextRebuiltSystem(id message.QID, isGroup bool) (prompt, skills, scene string) {
	prompt = p.getPromptForID(id, isGroup)
	if p.skillManager != nil {
		skills = p.skillManager.BuildAvailableSkillsPrompt()
	}
	v, _ := p.sessionBots.Load(sessionKey(id, isGroup))
	facade, _ := v.(bot.Bot)
	scene = p.buildScenePrompt(facade, id, isGroup) + orchestrationGuide
	return prompt, skills, scene
}

// contextPersistedMessages 从持久化存储回放会话历史；不可用时返回空切片。
func (p *AIChatPlugin) contextPersistedMessages(key string) []aichat.Message {
	if p.historyDB == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextDBTimeout)
	defer cancel()
	store := newSQLHistoryStore(p.historyDB, key, p.contextLogger())
	msgs, err := store.Load(ctx)
	if err != nil {
		p.contextLogger().Warn("面板读取会话历史失败", "key", key, "error", err.Error())
		return nil
	}
	return msgs
}

// contextSystemSections 把 system prompt 的三段原始内容转换为面板分段视图。
func contextSystemSections(sections aichat.SystemContextSnapshot) []plugininfo.ContextSection {
	items := []struct {
		key   string
		title string
		text  string
	}{
		{plugininfo.ContextSectionPrompt, "静态提示词（含会话 Prompt 覆盖）", sections.Prompt},
		{plugininfo.ContextSectionSkills, "可用技能（available_skills）", sections.Skills},
		{plugininfo.ContextSectionScene, "场景描述与编排指引", sections.Scene},
	}
	out := make([]plugininfo.ContextSection, 0, len(items))
	for _, it := range items {
		if strings.TrimSpace(it.text) == "" {
			continue
		}
		chars, tokens := contextTextStats(it.text)
		out = append(out, plugininfo.ContextSection{
			Key:       it.key,
			Title:     it.title,
			Content:   it.text,
			Chars:     chars,
			EstTokens: tokens,
		})
	}
	return out
}

// contextMessageViews 把对话消息转换为面板视图，并补齐 tool 结果消息对应的工具名
// （tool 消息本身只带 tool_call_id，名字来自配对的 assistant 消息）。
func contextMessageViews(msgs []aichat.Message) []plugininfo.ContextMessage {
	out := make([]plugininfo.ContextMessage, 0, len(msgs))
	if len(msgs) == 0 {
		return out
	}
	names := make(map[string]string, len(msgs))
	for i := range msgs {
		for _, tc := range msgs[i].ToolCalls {
			if tc.ID != "" && tc.Name != "" {
				names[tc.ID] = tc.Name
			}
		}
	}
	for i := range msgs {
		m := msgs[i]
		view := plugininfo.ContextMessage{Role: string(m.Role)}
		var sb strings.Builder
		chars := 0
		for _, part := range m.Parts {
			if part.Type == aichat.ContentPartImageURL {
				view.Images++
				continue
			}
			sb.WriteString(part.Text)
			chars += utf8.RuneCountInString(part.Text)
		}
		view.Text = sb.String()
		if m.ReasoningContent != "" {
			view.Reasoning = m.ReasoningContent
			chars += utf8.RuneCountInString(m.ReasoningContent)
		}
		// Anthropic 思考块（含签名）同样占用上下文体积
		chars += len(m.ThinkingBlocks)
		if len(m.ToolCalls) > 0 {
			view.ToolCalls = make([]plugininfo.ContextToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				view.ToolCalls = append(view.ToolCalls, plugininfo.ContextToolCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				})
				chars += utf8.RuneCountInString(tc.Name) + utf8.RuneCountInString(tc.Arguments)
			}
		}
		if m.Role == aichat.RoleTool {
			view.ToolCallID = m.ToolCallID
			view.ToolName = names[m.ToolCallID]
		}
		view.Chars = chars
		// 与消息窗口的兜底估算口径一致：约 2 字符折 1 token，图片按固定值计
		view.EstTokens = chars/2 + view.Images*contextImageTokens
		out = append(out, view)
	}
	return out
}

// contextImageTokens 每张图片的固定 token 估算（与 messageWindow.estimateTokens 一致）。
const contextImageTokens = 1000

// contextToolViews 把实际下发的工具定义转换为面板视图（描述与参数 schema 原样保留）。
func contextToolViews(defs []llmtool.ToolDef) []plugininfo.ContextTool {
	out := make([]plugininfo.ContextTool, 0, len(defs))
	for _, def := range defs {
		params := ""
		if def.Function.Parameters != nil {
			if data, err := json.Marshal(def.Function.Parameters); err == nil {
				params = string(data)
			}
		}
		chars := utf8.RuneCountInString(def.Function.Name) +
			utf8.RuneCountInString(def.Function.Description) + len(params)
		out = append(out, plugininfo.ContextTool{
			Name:        def.Function.Name,
			Description: def.Function.Description,
			Parameters:  params,
			Chars:       chars,
			EstTokens:   chars / 2,
		})
	}
	return out
}

// contextTodoViews 把会话任务清单转换为面板视图。
func contextTodoViews(items []todoItem) []plugininfo.ContextTodoItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]plugininfo.ContextTodoItem, 0, len(items))
	for _, it := range items {
		out = append(out, plugininfo.ContextTodoItem{
			Content:    it.Content,
			Status:     it.Status,
			ActiveForm: it.ActiveForm,
		})
	}
	return out
}

// contextSumStats 汇总各段字符 / token 估算，写入详情。
func contextSumStats(d *plugininfo.ContextDetail) {
	for _, s := range d.System {
		d.SystemChars += s.Chars
		d.SystemTokens += s.EstTokens
	}
	for _, m := range d.Messages {
		d.HistoryChars += m.Chars
		d.HistoryTokens += m.EstTokens
	}
	for _, t := range d.Tools {
		d.ToolsChars += t.Chars
		d.ToolsTokens += t.EstTokens
	}
	d.TotalChars = d.SystemChars + d.HistoryChars + d.ToolsChars
	d.TotalTokens = d.SystemTokens + d.HistoryTokens + d.ToolsTokens
}

// contextTextStats 返回文本的字符数与 token 粗估（约 2 字符折 1 token）。
func contextTextStats(text string) (chars, tokens int) {
	chars = utf8.RuneCountInString(text)
	return chars, chars / 2
}

// ContextExport 返回选中会话（keys 为空表示全部会话）的完整上下文，实现
// adminpanel.ContextSource。单个会话读取失败时跳过，不影响其余会话。
func (p *AIChatPlugin) ContextExport(keys []string) []plugininfo.ContextDetail {
	exportKeys := keys
	if len(exportKeys) == 0 {
		for _, s := range p.ContextSessions() {
			exportKeys = append(exportKeys, s.Key)
		}
	}
	out := make([]plugininfo.ContextDetail, 0, len(exportKeys))
	for _, key := range exportKeys {
		detail, err := p.ContextDetail(strings.TrimSpace(key))
		if err != nil {
			p.contextLogger().Warn("导出会话上下文失败，已跳过", "key", key, "error", err.Error())
			continue
		}
		out = append(out, detail)
	}
	return out
}

// ContextClear 清空指定会话的上下文（保留会话本身，下次发言即新对话）。
func (p *AIChatPlugin) ContextClear(key string, terminate bool) (plugininfo.ContextClearResult, error) {
	return p.contextPurge(key, terminate, false)
}

// ContextDelete 删除整个会话（上下文 + 会话级运行态与目标）。
func (p *AIChatPlugin) ContextDelete(key string, terminate bool) (plugininfo.ContextClearResult, error) {
	return p.contextPurge(key, terminate, true)
}

// ContextClearAll 清空全部会话的上下文。正忙且 terminate=false 的会话计入失败，
// 其余照常清空（面板会先弹确认框，用户确认后带 terminate=true 重发）。
func (p *AIChatPlugin) ContextClearAll(terminate bool) plugininfo.ContextClearAllResult {
	sessions := p.ContextSessions()
	res := plugininfo.ContextClearAllResult{OK: true, Failed: []plugininfo.ContextClearResult{}}
	for _, s := range sessions {
		one, err := p.contextPurge(s.Key, terminate, false)
		switch {
		case err != nil:
			res.Failed = append(res.Failed, plugininfo.ContextClearResult{Key: s.Key, Error: err.Error()})
		case !one.OK:
			res.Failed = append(res.Failed, one)
		default:
			res.Cleared++
			if one.Terminated {
				res.Terminated++
			}
		}
	}
	res.OK = len(res.Failed) == 0
	return res
}

// contextPurge 清空（removeSession=false）或删除（removeSession=true）会话上下文。
//
// 步骤：校验会话键 → 忙于执行且未授权终止时返回 ErrContextBusy → 终止在跑请求与
// 后台工作 → 轮询直到拿到会话锁 → 清空内存窗口 / 持久化历史 → 复位会话级状态
// （动态 MCP 工具、落盘工具结果、任务清单、会话 token 等）。
func (p *AIChatPlugin) contextPurge(key string, terminate, removeSession bool) (plugininfo.ContextClearResult, error) {
	res := plugininfo.ContextClearResult{Key: key, Deleted: removeSession}
	id, isGroup, ok := parseSessionKey(key)
	if !ok {
		return res, fmt.Errorf("无效的会话键：%s", key)
	}
	entry := p.contextChatEntry(key)
	_, hasMeta := p.contextSessionMeta(key)
	if entry == nil && !hasMeta {
		return res, fmt.Errorf("会话不存在或已无上下文：%s", key)
	}

	busy, _ := p.contextRuntimeStage(key)
	if busy && !terminate {
		return res, plugininfo.ErrContextBusy
	}
	if busy {
		if stopped, _ := p.stopRequest(id, isGroup, message.FromUint64(0), true); stopped {
			res.Terminated = true
		}
	}
	// 与 #新对话 一致：无论回合是否在跑，先停掉该会话的后台工作与排队消息，
	// 否则旧子代理 / 团队运行的结果会回灌到清空后的上下文里。
	subagents, teams := p.stopSessionBackground(id, isGroup)
	if subagents > 0 || teams > 0 {
		p.contextLogger().Info("清空上下文时已停止会话后台工作", "key", key,
			"subagents", subagents, "team_runs", teams)
	}
	p.drainPending(id, isGroup)

	lock, ok := p.waitContextLock(id, isGroup)
	if !ok {
		return res, fmt.Errorf("会话仍在执行请求，请稍后重试")
	}
	if lock != nil {
		defer lock.release()
	}

	ctx, cancel := context.WithTimeout(context.Background(), contextDBTimeout)
	defer cancel()

	// 1. 清空内存窗口（同时删除持久化历史）；未驻留时直接删持久化历史
	if entry != nil && entry.chat != nil {
		if err := entry.chat.ClearHistory(ctx); err != nil {
			return res, fmt.Errorf("清空会话历史失败: %w", err)
		}
		if cleared := entry.chat.ClearDynamicTools(); cleared > 0 {
			p.contextLogger().Info("清空上下文：已卸载动态加载的 MCP 工具", "key", key, "count", cleared)
		}
	}
	if p.historyDB != nil {
		store := newSQLHistoryStore(p.historyDB, key, p.contextLogger())
		if err := store.Clear(ctx); err != nil {
			return res, fmt.Errorf("清除持久化历史失败: %w", err)
		}
	}

	// 2. 复位会话级内存状态：淘汰会话对象（下次发言按空历史重建），清掉只作用于
	// 旧上下文的旁挂状态（任务清单提醒、待注入钩子上下文、历史工具名、落盘工具
	// 结果、未@计数、排队队列、最近回合摘要）
	if entry != nil && p.chats.CompareAndDelete(key, entry) {
		p.contextLogger().Info("清空上下文：会话对象已从内存移除", "key", key)
	}
	if p.todoManager != nil {
		p.todoManager.clear(key)
	}
	if p.recentTurns != nil {
		p.recentTurns.remove(key)
	}
	p.historyTools.Delete(key)
	p.sessionInject.Delete(key)
	p.toolResultLimiter.Purge(key)
	p.reclaimSessionQueue(key)
	// 新会话：换新会话实例 token（${session} 请求头占位符随之改变）
	p.renewSessionToken(key)

	// 3. 删除会话时一并清理会话级持久状态：长任务目标、未@计数（群聊自动清空
	// 计数）、计划模式。清空语义保留目标（已暂停，可用 /goal resume 恢复）与
	// 计划模式，避免面板操作改动与上下文无关的用户设置。
	if removeSession {
		if p.planManager != nil {
			p.planManager.Set(key, false)
		}
		if p.goalStore != nil {
			if err := p.goalStore.Clear(ctx, key); err != nil {
				p.contextLogger().Warn("删除会话时清除长任务目标失败", "key", key, "error", err.Error())
			}
		}
		p.goalArmed.Delete(key)
		if isGroup {
			p.resetNoMention(id)
		}
		p.sessionBots.Delete(key)
	}

	res.OK = true
	return res, nil
}

// waitContextLock 轮询等待会话锁（最多 contextClearLockWait）。成功返回已持有的
// 锁句柄，调用方负责 release；超时返回 nil。
//
// 用 tryLockOnly 而不是 tryLock：后者会一并占用对话并发槽位，其他会话正跑满
// rate_limit 时面板清空会白白失败；管理操作只需要与回合互斥，不需要限流名额。
func (p *AIChatPlugin) waitContextLock(id message.QID, isGroup bool) (*sessionLock, bool) {
	if p.lockStorage == nil {
		// 降载模式（无会话锁存储）：没有可互斥的对象，直接执行清空
		return nil, true
	}
	deadline := time.Now().Add(contextClearLockWait)
	for {
		if lock := p.tryLockOnly(id, isGroup); lock != nil {
			return lock, true
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		time.Sleep(contextClearLockPoll)
	}
}

// contextLogger 返回插件日志器；未注入时退回默认日志（部分测试直接构造插件）。
func (p *AIChatPlugin) contextLogger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}
