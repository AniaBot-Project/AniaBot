package pluginaichat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/jeanhua/AniaBot/common/storage"
)

// 试聊室：把聊天搬到面板上，管理员直接在 Web 控制面板里和 AI 对话做测试。
//
// 与平台会话的区别：
//   - 回复只回显在面板中，不向任何平台发送消息（SendText / SendImage / SendFile
//     全部改为写入转录）；AI 用 ||| 指定的多条消息同样在面板中拆成多条展示，
//     与线上发送效果一致；
//   - 没有人工审批环节：危险工具直接放行并在转录里留一条系统提示（面板操作者
//     本身就是管理员，等价于「工具审批关闭」时的线上行为）；
//   - 照常写 Query 日志并计入 Token 统计，会话类型标记为 panel（面板可据此
//     筛选、统计里也能单列「试聊」），但**不计入每日配额**：试聊是调试行为，
//     不该占用线上会话的额度；
//   - 会话各自持有独立的 ChatBot（上下文跨轮、跨重启延续），转录额外持久化到
//     PersistentStorage，便于刷新页面或重启后继续看。
//
// 绑定真实群聊/私聊的试聊会继承该会话 scope 的系统提示词覆盖、长期记忆、知识库、
// 定时任务 / 主动互动等会话级工具，从而贴近线上表现；「通用试聊」不绑定 scope，
// 只保留基础工具，用于快速验证模型与提示词。
const (
	// chatConsolePersistSessions 会话索引在 PersistentStorage 中的键
	chatConsolePersistSessions = "sessions"
	// chatConsolePersistMessages 单会话转录的键前缀
	chatConsolePersistMessages = "msgs:"
	// chatConsoleHistoryPrefix 试聊会话在对话历史表里的 session_id 前缀，
	// 与真实会话的 g:/f: 命名空间隔离
	chatConsoleHistoryPrefix = "panelchat:"
	// chatConsoleMaxSessions 会话数量上限，防止面板无限建会话
	chatConsoleMaxSessions = 30
	// chatConsoleMaxMessages 单会话保留的转录条数（超出丢弃最旧的）
	chatConsoleMaxMessages = 200
	// chatConsoleMaxTextRunes 单条用户消息长度上限
	chatConsoleMaxTextRunes = 8000
	// chatConsoleMaxToolRecords 单轮保留的工具调用明细条数上限
	chatConsoleMaxToolRecords = 60
	// chatConsoleMaxToolFieldRunes 工具参数 / 结果在转录中的截断长度
	chatConsoleMaxToolFieldRunes = 6000
	// chatConsoleMaxAttachments 单条消息展示的附件数量上限
	chatConsoleMaxAttachments = 8
	// chatConsoleMaxAttachmentBytes 单个附件上限（超过只留文字说明，避免面板响应体过大）
	chatConsoleMaxAttachmentBytes = 8 << 20
	// chatConsoleMaxMessageRunes 单条 AI 回复的展示长度上限
	chatConsoleMaxMessageRunes = 40000
)

// chatConsoleManager 面板试聊会话管理器（由 AI 对话插件持有，面板经可选接口调用）。
type chatConsoleManager struct {
	plugin  *AIChatPlugin
	logger  *slog.Logger
	persist storage.PersistentStorage

	mu       sync.Mutex
	sessions map[string]*chatConsoleSession
	seq      uint64
}

// chatConsoleSession 一个试聊会话。
type chatConsoleSession struct {
	mu   sync.Mutex
	info plugininfo.ChatConsoleSession

	chat *aichat.ChatBot
	// prompt/scene 当前注入的系统提示词与场景描述（面板改 Prompt 覆盖后下一轮热更新）
	prompt string
	scene  string

	msgs []plugininfo.ChatConsoleMessage
	seq  int64

	busy    bool
	cancel  context.CancelFunc
	runSeq  int64 // 正在生成的 assistant 消息 Seq
	partial strings.Builder
	// deleted 会话已被删除：运行中的 goroutine 收尾时不再回写转录
	deleted bool
}

// chatConsolePersistIndex 会话索引的持久化结构。
type chatConsolePersistIndex struct {
	Sessions []plugininfo.ChatConsoleSession `json:"sessions"`
}

func newChatConsoleManager(p *AIChatPlugin) *chatConsoleManager {
	m := &chatConsoleManager{
		plugin:   p,
		logger:   p.Logger.WithGroup("chatconsole"),
		persist:  p.PersistentStorage,
		sessions: make(map[string]*chatConsoleSession),
	}
	m.load()
	return m
}

// chatConsoleHistoryKey 试聊会话在对话历史表中的 session_id。
func chatConsoleHistoryKey(id string) string {
	return chatConsoleHistoryPrefix + id
}

// load 启动时回放会话索引与各会话转录；正在生成中的消息归一化为「进程重启中断」。
func (m *chatConsoleManager) load() {
	if m.persist == nil {
		return
	}
	var idx chatConsolePersistIndex
	if !m.persist.Get(context.Background(), chatConsolePersistSessions, &idx) {
		return
	}
	for _, info := range idx.Sessions {
		if strings.TrimSpace(info.ID) == "" {
			continue
		}
		s := &chatConsoleSession{info: info}
		var msgs []plugininfo.ChatConsoleMessage
		if m.persist.Get(context.Background(), chatConsolePersistMessages+info.ID, &msgs) {
			for i := range msgs {
				if msgs[i].Status == plugininfo.ChatConsoleStatusRunning {
					msgs[i].Status = plugininfo.ChatConsoleStatusInterrupted
					msgs[i].Error = "进程重启，本次响应已中断"
				}
				if msgs[i].Seq > s.seq {
					s.seq = msgs[i].Seq
				}
			}
			s.msgs = msgs
		}
		s.info.Busy = false
		s.info.Count = len(s.msgs)
		m.sessions[info.ID] = s
	}
	if len(m.sessions) > 0 {
		m.logger.Info("已恢复面板试聊会话", "count", len(m.sessions))
	}
}

// persistIndex 落盘会话索引（调用方不持锁）。
func (m *chatConsoleManager) persistIndex() {
	if m.persist == nil {
		return
	}
	m.mu.Lock()
	list := make([]plugininfo.ChatConsoleSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, m.snapshotInfoLocked(s))
	}
	m.mu.Unlock()
	if !m.persist.Set(context.Background(), chatConsolePersistSessions, chatConsolePersistIndex{Sessions: list}) {
		m.logger.Error("保存面板试聊会话索引失败")
	}
}

// persistSession 落盘单个会话的转录（调用方不持锁）。附件（data URI）体积大且
// 属一次性展示内容，不落盘。
func (m *chatConsoleManager) persistSession(s *chatConsoleSession) {
	if m.persist == nil {
		return
	}
	s.mu.Lock()
	msgs := make([]plugininfo.ChatConsoleMessage, len(s.msgs))
	if s.deleted {
		s.mu.Unlock()
		return
	}
	for i := range s.msgs {
		msgs[i] = s.msgs[i]
		msgs[i].Attachments = nil
	}
	s.mu.Unlock()
	if !m.persist.Set(context.Background(), chatConsolePersistMessages+s.info.ID, msgs) {
		m.logger.Error("保存面板试聊转录失败", "session", s.info.ID)
	}
}

// snapshotInfoLocked 返回会话信息快照（要求 m.mu 已持有）。
func (m *chatConsoleManager) snapshotInfoLocked(s *chatConsoleSession) plugininfo.ChatConsoleSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.info
	info.Busy = s.busy
	info.Count = len(s.msgs)
	return info
}

// snapshotInfo 返回会话信息快照。
func (m *chatConsoleManager) snapshotInfo(s *chatConsoleSession) plugininfo.ChatConsoleSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotInfoLocked(s)
}

// session 按 ID 取会话。
func (m *chatConsoleManager) session(id string) (*chatConsoleSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// nextID 生成会话 ID（时间戳 base36 + 递增序号，短且可读）。
func (m *chatConsoleManager) nextID() string {
	n := atomic.AddUint64(&m.seq, 1)
	return strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatUint(n%4096, 36)
}

// list 返回全部会话（最近活动在前）。
func (m *chatConsoleManager) list() []plugininfo.ChatConsoleSession {
	m.mu.Lock()
	out := make([]plugininfo.ChatConsoleSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, m.snapshotInfoLocked(s))
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// create 新建试聊会话。
func (m *chatConsoleManager) create(req plugininfo.ChatConsoleSessionCreate) (plugininfo.ChatConsoleSession, error) {
	kind := strings.TrimSpace(req.TargetKind)
	if kind == "" {
		kind = plugininfo.ChatConsoleTargetTest
	}
	switch kind {
	case plugininfo.ChatConsoleTargetTest:
		req.TargetID = ""
		req.TargetName = "通用试聊"
	case plugininfo.ChatConsoleTargetGroup, plugininfo.ChatConsoleTargetFriend:
		req.TargetID = strings.TrimSpace(req.TargetID)
		if req.TargetID == "" {
			return plugininfo.ChatConsoleSession{}, fmt.Errorf("绑定群聊/私聊时必须提供目标 ID")
		}
	default:
		return plugininfo.ChatConsoleSession{}, fmt.Errorf("不支持的目标类型: %s", kind)
	}

	m.mu.Lock()
	if len(m.sessions) >= chatConsoleMaxSessions {
		m.mu.Unlock()
		return plugininfo.ChatConsoleSession{}, fmt.Errorf("试聊会话数量已达上限（%d 个），请先删除不用的会话", chatConsoleMaxSessions)
	}
	now := time.Now()
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.TargetName)
	}
	if title == "" {
		title = map[string]string{
			plugininfo.ChatConsoleTargetGroup:  "群聊试聊",
			plugininfo.ChatConsoleTargetFriend: "私聊试聊",
		}[kind]
	}
	if title == "" {
		title = "通用试聊"
	}
	s := &chatConsoleSession{info: plugininfo.ChatConsoleSession{
		ID:         m.nextID(),
		Title:      title,
		TargetKind: kind,
		TargetID:   req.TargetID,
		TargetName: strings.TrimSpace(req.TargetName),
		CreatedAt:  now,
		UpdatedAt:  now,
	}}
	m.sessions[s.info.ID] = s
	info := m.snapshotInfoLocked(s)
	m.mu.Unlock()

	m.persistSession(s)
	m.persistIndex()
	m.logger.Info("新建面板试聊会话", "session", info.ID, "kind", kind, "target", info.TargetID)
	return info, nil
}

// delete 删除会话（含转录、AI 上下文）。
func (m *chatConsoleManager) delete(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("试聊会话不存在: %s", id)
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.busy = false
	s.deleted = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.clearChatHistory(s)
	if m.persist != nil {
		m.persist.Del(context.Background(), chatConsolePersistMessages+id)
	}
	m.persistIndex()
	m.logger.Info("删除面板试聊会话", "session", id)
	return nil
}

// clearChatHistory 清空会话的 AI 上下文（ChatBot 内存窗口 + 持久化历史）。
func (m *chatConsoleManager) clearChatHistory(s *chatConsoleSession) {
	s.mu.Lock()
	chat := s.chat
	s.mu.Unlock()
	if chat == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	defer cancel()
	if err := chat.ClearHistory(ctx); err != nil {
		m.logger.Error("清空试聊上下文失败", "session", s.info.ID, "error", err.Error())
	}
	chat.ClearDynamicTools()
}

// clear 清空转录与 AI 上下文（保留会话）。
func (m *chatConsoleManager) clear(id string) error {
	s, ok := m.session(id)
	if !ok {
		return fmt.Errorf("试聊会话不存在: %s", id)
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return fmt.Errorf("会话正在生成回复，请先停止再清空")
	}
	s.msgs = nil
	s.seq = 0
	s.info.UpdatedAt = time.Now()
	s.mu.Unlock()
	m.clearChatHistory(s)
	m.persistSession(s)
	m.persistIndex()
	return nil
}

// messages 返回 Seq 大于 after 的消息。为了让面板能持续刷新「最后一条」的流式
// 文本与工具调用进度（这些更新发生在同一条消息上、Seq 不变），最后一条消息
// 总是随响应一起返回；前端按 Seq 覆盖即可。
func (m *chatConsoleManager) messages(id string, after int64) ([]plugininfo.ChatConsoleMessage, bool, error) {
	s, ok := m.session(id)
	if !ok {
		return nil, false, fmt.Errorf("试聊会话不存在: %s", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]plugininfo.ChatConsoleMessage, 0, len(s.msgs))
	for i := range s.msgs {
		if s.msgs[i].Seq > after {
			out = append(out, cloneConsoleMessage(s.msgs[i]))
		}
	}
	if n := len(s.msgs); n > 0 {
		last := s.msgs[n-1]
		if last.Seq <= after {
			out = append(out, cloneConsoleMessage(last))
		}
	}
	return out, s.busy, nil
}

// cloneConsoleMessage 深拷贝一条消息（切片字段与 HTTP 序列化不能与写路径共享底层数组）。
func cloneConsoleMessage(msg plugininfo.ChatConsoleMessage) plugininfo.ChatConsoleMessage {
	out := msg
	if len(msg.Attachments) > 0 {
		out.Attachments = append([]plugininfo.ChatConsoleAttachment(nil), msg.Attachments...)
	}
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = append([]plugininfo.ChatConsoleToolCall(nil), msg.ToolCalls...)
	}
	if msg.Usage != nil {
		usage := *msg.Usage
		out.Usage = &usage
	}
	return out
}

// send 追加用户消息并异步生成回复。
func (m *chatConsoleManager) send(id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("消息不能为空")
	}
	if utf8.RuneCountInString(text) > chatConsoleMaxTextRunes {
		return fmt.Errorf("消息过长（最多 %d 字）", chatConsoleMaxTextRunes)
	}
	p := m.plugin
	if !p.ready {
		return fmt.Errorf("AI 对话未就绪：请先在配置页完成模型 API 配置")
	}
	s, ok := m.session(id)
	if !ok {
		return fmt.Errorf("试聊会话不存在: %s", id)
	}

	now := time.Now()
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return fmt.Errorf("上一条消息还在生成中，请稍候或先停止")
	}
	s.msgs = append(s.msgs,
		plugininfo.ChatConsoleMessage{
			Seq:    s.nextSeqLocked(),
			Role:   plugininfo.ChatConsoleRoleUser,
			Text:   text,
			Status: plugininfo.ChatConsoleStatusOK,
			Time:   now,
		},
		plugininfo.ChatConsoleMessage{
			Seq:    s.nextSeqLocked(),
			Role:   plugininfo.ChatConsoleRoleAssistant,
			Status: plugininfo.ChatConsoleStatusRunning,
			Time:   now,
		},
	)
	asstSeq := s.msgs[len(s.msgs)-1].Seq
	s.busy = true
	s.runSeq = asstSeq
	s.partial.Reset()
	s.info.UpdatedAt = now
	s.trimLocked()

	timeout := p.msgEventTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.cancel = cancel
	s.mu.Unlock()

	m.persistSession(s)
	m.persistIndex()

	go m.run(ctx, cancel, s, m.userInput(text), text, asstSeq, timeout)
	return nil
}

// run 执行一轮对话并把结果写回转录。
func (m *chatConsoleManager) run(ctx context.Context, cancel context.CancelFunc, s *chatConsoleSession, input, raw string, asstSeq int64, timeout time.Duration) {
	p := m.plugin
	start := time.Now()
	// rec 是本轮 Query 日志记录；持久化不支持 SQL 等情况下为 nil（只回显转录）
	var rec *queryRecorder
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("内部错误: %v", r)
			m.finish(s, asstSeq, "", aichat.TokenUsage{}, err, timeout, start)
			p.finishConsoleQuery(rec, aichat.TokenUsage{}, "", "", err)
			m.logger.Error("面板试聊执行异常", "session", s.info.ID, "panic", r)
		}
		cancel()
		s.mu.Lock()
		s.busy = false
		s.cancel = nil
		s.runSeq = 0
		s.partial.Reset()
		s.mu.Unlock()
		m.persistSession(s)
		m.persistIndex()
	}()

	chat, err := m.ensureChat(s)
	if err != nil {
		m.finish(s, asstSeq, "", aichat.TokenUsage{}, err, timeout, start)
		return
	}
	opts := p.buildChatOptions()
	opts.PreToolGate = m.preToolGate(s)
	opts.PersistUserText = raw
	opts.OnStreamDelta = func(delta string) { m.onStreamDelta(s, asstSeq, delta) }
	opts.OnStreamRoundEnd = func() { m.onStreamRoundEnd(s, asstSeq) }

	// 工具调用同时进入转录（面板展示明细）与 Query 日志（落盘审计）：ChatBot 只
	// 保留一个观察者，这里合并挂钩。试聊照常写入 Query 日志并计入 Token 统计，
	// 会话类型标记为 panel，与线上群聊/私聊流量区分。
	note := "面板试聊会话「" + s.info.Title + "」"
	rec = p.beginConsoleQuery(chat, m.queryTarget(s), raw, func(info aichat.ToolCallInfo) {
		m.onToolCall(s, info)
	})

	resp, usage, err := chat.Chat(ctx, input, m.callbacks(ctx, s, asstSeq), opts)
	chat.SetToolObserver(nil)
	m.finish(s, asstSeq, resp, usage, err, timeout, start)
	p.finishConsoleQuery(rec, usage, resp, note, err)
}

// queryTarget 返回试聊 Query 日志里的目标标识：绑定真实会话时用其 ID（与线上
// 日志对齐，便于按同一个群/好友检索），通用试聊没有目标，用会话标题代替。
func (m *chatConsoleManager) queryTarget(s *chatConsoleSession) string {
	if id := strings.TrimSpace(s.info.TargetID); id != "" {
		return id
	}
	return s.info.Title
}

// ensureChat 返回会话的 ChatBot（首次调用时创建并回放历史）；系统提示词与场景
// 描述每轮同步，面板修改 Prompt 覆盖后无需重建会话即可生效。
func (m *chatConsoleManager) ensureChat(s *chatConsoleSession) (*aichat.ChatBot, error) {
	p := m.plugin
	prompt := m.systemPrompt(s)
	scene := m.scenePrompt(s)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chat != nil {
		if s.prompt != prompt {
			s.chat.SetSystemPrompt(prompt)
			s.prompt = prompt
		}
		if s.scene != scene {
			s.chat.SetScenePrompt(scene)
			s.scene = scene
		}
		return s.chat, nil
	}
	if p.toolExecutor == nil {
		return nil, fmt.Errorf("AI 工具执行器未就绪")
	}
	isGroup := s.info.TargetKind == plugininfo.ChatConsoleTargetGroup
	// 绑定真实会话时注册会话级工具（长期记忆 / 知识库 / 定时任务 / 主动互动等），
	// 让试聊表现贴近线上；通用试聊不绑定 scope，只用基础工具
	executor := p.toolExecutor.NewSessionExecutor()
	if s.info.TargetKind != plugininfo.ChatConsoleTargetTest {
		p.registerScopedTools(executor, parseQID(s.info.TargetID), isGroup)
	}
	// 上下文持久化到对话历史表（session_id 带 panelchat: 前缀，与真实会话隔离）；
	// SQL 后端不可用时仅内存窗口，重启即丢（与主对话的降级行为一致）
	var historyStore aichat.HistoryStore
	if p.historyDB != nil {
		historyStore = newSQLHistoryStore(p.historyDB, chatConsoleHistoryKey(s.info.ID), p.Logger)
	}
	chat, err := aichat.NewChatBot(
		p.cfg.BaseURL,
		p.cfg.APIKey,
		p.cfg.Model,
		prompt,
		p.cfg.MaxContextTokens,
		executor,
		historyStore,
		aichat.WithClientOptions(append(p.llmClientOptions(), aichat.WithAPIFormat(p.cfg.APIFormat))...),
		aichat.WithCompressorClient(p.buildCompressorClient()),
		aichat.WithScenePrompt(scene),
	)
	if err != nil {
		return nil, fmt.Errorf("创建对话失败: %w", err)
	}
	chat.SetMaxIterations(p.mainMaxIterations())
	if p.skillManager != nil {
		chat.SetSkillManager(p.skillManager)
	}
	if p.hookManager != nil {
		chat.SetHookRunner(p.hookManager, chatConsoleHistoryKey(s.info.ID), agenthook.AgentKindMain)
	}
	chat.SetToolObserver(func(info aichat.ToolCallInfo) { m.onToolCall(s, info) })

	loadCtx, cancelLoad := context.WithTimeout(context.Background(), historyLoadTimeout)
	chat.LoadHistory(loadCtx)
	cancelLoad()

	s.chat = chat
	s.prompt = prompt
	s.scene = scene
	m.logger.Info("创建面板试聊会话上下文", "session", s.info.ID, "kind", s.info.TargetKind, "target", s.info.TargetID)
	return chat, nil
}

// systemPrompt 取该试聊会话的系统提示词：绑定真实会话时继承其 Prompt 覆盖。
func (m *chatConsoleManager) systemPrompt(s *chatConsoleSession) string {
	p := m.plugin
	if s.info.TargetKind == plugininfo.ChatConsoleTargetTest {
		return p.getPromptForID("", false)
	}
	return p.getPromptForID(parseQID(s.info.TargetID), s.info.TargetKind == plugininfo.ChatConsoleTargetGroup)
}

// scenePrompt 生成场景描述：绑定真实会话时与线上一致（有平台外观时补充群名/人数），
// 通用试聊则说明这是面板内的对话测试。
func (m *chatConsoleManager) scenePrompt(s *chatConsoleSession) string {
	p := m.plugin
	if s.info.TargetKind == plugininfo.ChatConsoleTargetTest {
		return "\n\n【当前对话场景】\n你正在 Web 控制面板的「试聊室」中与管理员一对一对话，用于测试你的回复效果。" +
			"本会话不绑定任何群聊/私聊，没有平台专属工具（主动发消息、看群成员等）与历史消息工具，只能使用基础工具。" +
			"\n用户消息以 [nickname:昵称 id:用户ID] 开头，这里的 id 固定为 0（面板管理员）。"
	}
	qid := parseQID(s.info.TargetID)
	isGroup := s.info.TargetKind == plugininfo.ChatConsoleTargetGroup
	// 复用线上会话的平台外观补全群名/人数（只用读取能力，不发送任何消息）
	var b bot.Bot
	if v, ok := p.sessionBots.Load(sessionKey(qid, isGroup)); ok {
		if fb, ok := v.(bot.Bot); ok {
			b = fb
		}
	}
	return p.buildScenePrompt(b, qid, isGroup)
}

// userInput 构造发给模型的用户消息：与线上一致带发送者前缀，保持提示词约定不变。
func (m *chatConsoleManager) userInput(text string) string {
	return "[nickname:面板管理员 id:0]: " + text
}

// preToolGate 请求级工具门禁：面板试聊没有人工审批环节，只保留 PreToolUse 钩子
// （管理员配置的 shell 钩子仍能拦截危险工具），并跳过计划模式（面板无 /plan 入口）。
func (m *chatConsoleManager) preToolGate(s *chatConsoleSession) func(context.Context, llmtool.ToolCall) (bool, string) {
	p := m.plugin
	if p.hookManager == nil {
		return nil
	}
	key := chatConsoleHistoryKey(s.info.ID)
	return func(ctx context.Context, call llmtool.ToolCall) (bool, string) {
		res := p.hookManager.Run(ctx, agenthook.EventPreToolUse, agenthook.Payload{
			SessionKey: key,
			AgentKind:  agenthook.AgentKindMain,
			ToolName:   call.Name,
			ToolInput:  call.Arguments,
		})
		if !res.Block {
			return false, ""
		}
		reason := res.Reason
		if reason == "" {
			reason = "钩子要求阻断"
		}
		m.addNotice(s, "工具 "+call.Name+" 被钩子阻止："+reason)
		return true, "工具调用被钩子阻止: " + reason
	}
}

// callbacks 构造工具回调：所有发送动作改为写入面板转录，不产生任何平台副作用。
func (m *chatConsoleManager) callbacks(ctx context.Context, s *chatConsoleSession, asstSeq int64) llmtool.CallBackFuncs {
	var loaded []string
	cbs := llmtool.CallBackFuncs{
		SendText: func(text string) (string, error) {
			m.appendAssistantText(s, asstSeq, text)
			return "已展示在面板中", nil
		},
		SendImage: func(bs64content string) (string, error) {
			if err := m.appendAttachment(s, asstSeq, "image", "", bs64content); err != nil {
				return "", err
			}
			return "已展示在面板中", nil
		},
		SendFile: func(name, bs64content string) (string, error) {
			if err := m.appendAttachment(s, asstSeq, "file", name, bs64content); err != nil {
				return "", err
			}
			return "已展示在面板中", nil
		},
		GetPrivateFileURL: func(string) (string, error) {
			return "", fmt.Errorf("面板试聊无法获取私聊文件链接（未绑定平台消息通道）")
		},
		LoadImages: func([]string) (string, error) {
			return "当前为面板试聊，没有可加载的消息图片；如需看图请先用 local_image 读取本地图片", nil
		},
		TakeLoadedImages: func() []string {
			imgs := loaded
			loaded = nil
			return imgs
		},
		RequestApproval: func(_ context.Context, toolName, _ string) (bool, string) {
			m.addNotice(s, "已自动放行工具 "+toolName+" 的审批请求（试聊无人工审批环节）")
			return true, ""
		},
	}
	// 本地图片读取（多模态主模型 / 备用识别模型）与线上一致可用
	m.plugin.attachLocalImageCallback(&cbs, ctx, &loaded, nil)
	return cbs
}

// onStreamDelta 流式增量：累积到本轮缓冲并刷新转录里运行中消息的正文。
func (m *chatConsoleManager) onStreamDelta(s *chatConsoleSession, seq int64, delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial.WriteString(delta)
	text := streamDisplayText(aichat.RemoveThinkContent(s.partial.String()))
	if msg := s.msgLocked(seq); msg != nil {
		msg.Text = chatConsoleTruncate(text, chatConsoleMaxMessageRunes)
	}
}

// onStreamRoundEnd 工具轮结束：清空缓冲，下一轮文本从头覆盖同一条消息。
func (m *chatConsoleManager) onStreamRoundEnd(s *chatConsoleSession, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial.Reset()
	if msg := s.msgLocked(seq); msg != nil {
		msg.Text = ""
	}
}

// onToolCall 工具调用观察者：把明细追加到运行中的 assistant 消息。
func (m *chatConsoleManager) onToolCall(s *chatConsoleSession, info aichat.ToolCallInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := s.msgLocked(s.runSeq)
	if msg == nil {
		return
	}
	msg.ToolCallsTotal++
	if len(msg.ToolCalls) < chatConsoleMaxToolRecords {
		rec := plugininfo.ChatConsoleToolCall{
			Name:       info.Name,
			Arguments:  chatConsoleTruncate(info.Arguments, chatConsoleMaxToolFieldRunes),
			Result:     chatConsoleTruncate(info.Result, chatConsoleMaxToolFieldRunes),
			DurationMs: info.DurationMs,
		}
		if info.Err != nil {
			rec.Error = chatConsoleTruncate(info.Err.Error(), chatConsoleMaxToolFieldRunes)
		}
		msg.ToolCalls = append(msg.ToolCalls, rec)
	}
}

// appendAssistantText 把工具主动发送的文本并入 assistant 消息正文。
func (m *chatConsoleManager) appendAssistantText(s *chatConsoleSession, seq int64, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := s.msgLocked(seq)
	if msg == nil {
		return
	}
	if cur := strings.TrimSpace(msg.Text); cur != "" {
		msg.Text = chatConsoleTruncate(cur+"\n"+text, chatConsoleMaxMessageRunes)
	} else {
		msg.Text = chatConsoleTruncate(text, chatConsoleMaxMessageRunes)
	}
}

// appendAttachment 追加图片 / 文件附件（data URI）。
func (m *chatConsoleManager) appendAttachment(s *chatConsoleSession, seq int64, kind, name, bs64 string) error {
	bs64 = strings.TrimSpace(bs64)
	if bs64 == "" {
		return fmt.Errorf("内容为空")
	}
	raw, err := base64.StdEncoding.DecodeString(bs64)
	if err != nil && strings.HasPrefix(bs64, "data:") {
		data, derr := decodeDataURIPayload(bs64)
		if derr != nil {
			return fmt.Errorf("附件数据解码失败: %v", derr)
		}
		raw = data
	} else if err != nil {
		return fmt.Errorf("附件数据解码失败: %v", err)
	}
	if len(raw) > chatConsoleMaxAttachmentBytes {
		m.addNotice(s, fmt.Sprintf("AI 产出的%s超过 %.1fMB，面板未展示",
			map[string]string{"image": "图片", "file": "文件"}[kind], float64(chatConsoleMaxAttachmentBytes)/(1<<20)))
		return nil
	}
	var url string
	switch {
	case strings.HasPrefix(bs64, "data:"):
		url = bs64
	case kind == "image":
		imgMime, ok := sniffImageMIME(raw)
		if !ok {
			// 无法识别格式：仍交给浏览器按图片解码尝试
			imgMime = "image/png"
		}
		url = "data:" + imgMime + ";base64," + bs64
	default:
		fileMime := "application/octet-stream"
		if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
			if mt := mime.TypeByExtension(ext); mt != "" {
				fileMime = mt
			}
		}
		url = "data:" + fileMime + ";base64," + bs64
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	msg := s.msgLocked(seq)
	if msg == nil {
		return fmt.Errorf("内部错误：找不到对应的回复消息")
	}
	if len(msg.Attachments) >= chatConsoleMaxAttachments {
		return fmt.Errorf("单轮附件的数量已达上限（%d 个）", chatConsoleMaxAttachments)
	}
	msg.Attachments = append(msg.Attachments, plugininfo.ChatConsoleAttachment{
		Kind: kind,
		Name: name,
		URL:  url,
		Size: len(raw),
	})
	s.info.UpdatedAt = time.Now()
	return nil
}

// addNotice 追加一条系统提示消息（自动放行、钩子拦截等），居中展示。
func (m *chatConsoleManager) addNotice(s *chatConsoleSession, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, plugininfo.ChatConsoleMessage{
		Seq:    s.nextSeqLocked(),
		Role:   plugininfo.ChatConsoleRoleSystem,
		Text:   text,
		Status: plugininfo.ChatConsoleStatusOK,
		Time:   time.Now(),
	})
	s.info.UpdatedAt = time.Now()
	s.trimLocked()
}

// finish 写回一轮对话的最终状态、用量与错误。
func (m *chatConsoleManager) finish(s *chatConsoleSession, seq int64, resp string, usage aichat.TokenUsage, err error, timeout time.Duration, start time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := s.msgLocked(seq)
	if msg == nil {
		return
	}
	msg.DurationMs = time.Since(start).Milliseconds()
	msg.Usage = &plugininfo.ChatConsoleUsage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CachedTokens:     usage.CachedTokens,
		Iterations:       usage.Iterations,
	}
	s.info.UpdatedAt = time.Now()
	msg.Status = plugininfo.ChatConsoleStatusOK
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			msg.Status = plugininfo.ChatConsoleStatusStopped
			msg.Error = "已停止生成"
		case errors.Is(err, context.DeadlineExceeded):
			msg.Status = plugininfo.ChatConsoleStatusTimeout
			msg.Error = "响应超时（" + timeout.String() + "）"
		default:
			var blocked *aichat.PromptBlockedError
			if errors.As(err, &blocked) {
				msg.Status = plugininfo.ChatConsoleStatusStopped
				msg.Error = "请求被钩子阻断: " + blocked.Reason
			} else {
				msg.Status = plugininfo.ChatConsoleStatusError
				msg.Error = aichat.UserErrorText(err)
			}
			m.logger.Warn("面板试聊请求失败", "session", s.info.ID, "error", err.Error())
		}
		return
	}
	final := strings.TrimSpace(resp)
	if final == "" {
		// 纯工具回合（例如只发图片/文件）没有文字回复，给一条说明避免空白气泡
		if len(msg.Attachments) > 0 || msg.ToolCallsTotal > 0 {
			final = "（本轮没有文字回复）"
		} else {
			final = "（AI 没有返回内容）"
		}
	}
	// 与线上发送一致：AI 用 ||| 指定消息边界时拆成多条消息，面板同样拆成多个
	// 气泡展示（流式阶段已把 ||| 显示为换行，这里换成真正的多条消息）；附件、
	// 用量与耗时留在第一条上，后续片段各占一条 assistant 消息。
	segments := replySegments(final)
	msg.Text = chatConsoleTruncate(segments[0], chatConsoleMaxMessageRunes)
	for _, seg := range segments[1:] {
		s.msgs = append(s.msgs, plugininfo.ChatConsoleMessage{
			Seq:    s.nextSeqLocked(),
			Role:   plugininfo.ChatConsoleRoleAssistant,
			Text:   chatConsoleTruncate(seg, chatConsoleMaxMessageRunes),
			Status: plugininfo.ChatConsoleStatusOK,
			Time:   time.Now(),
		})
	}
	s.trimLocked()
}

// msgLocked 按 Seq 查找消息（要求 s.mu 已持有）。
func (s *chatConsoleSession) msgLocked(seq int64) *plugininfo.ChatConsoleMessage {
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if s.msgs[i].Seq == seq {
			return &s.msgs[i]
		}
	}
	return nil
}

// nextSeqLocked 分配下一个消息序号（要求 s.mu 已持有）。
func (s *chatConsoleSession) nextSeqLocked() int64 {
	s.seq++
	return s.seq
}

// trimLocked 只保留最近的 chatConsoleMaxMessages 条转录（要求 s.mu 已持有）。
func (s *chatConsoleSession) trimLocked() {
	if extra := len(s.msgs) - chatConsoleMaxMessages; extra > 0 {
		s.msgs = append([]plugininfo.ChatConsoleMessage(nil), s.msgs[extra:]...)
	}
}

// chatConsoleTruncate 按 rune 截断文本，保留可读的省略标记。
func chatConsoleTruncate(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "\n…（内容过长已截断）"
}
