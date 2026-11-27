package plugininfo

import "errors"

// 面板「上下文管理」页使用的视图类型。
//
// 页面按会话（群聊 / 私聊）展示 AI 实际持有的完整上下文：系统提示词
// （静态提示词 + available_skills + 场景描述）、消息历史（含工具调用与结果）
// 与工具定义，并可删除单个会话、清空单个 / 全部会话上下文、导出选中会话。
//
// 放在 common/plugininfo 而不是 bot/adminpanel：插件只允许依赖 common/* 与
// bot/component/*，面板接口由 adminpanel 定义、通过类型别名引用这里的结构，
// 插件实现方法时无需 import bot/adminpanel（与 team/querylog 等数据源一致）。

// 上下文快照来源（ContextDetail.Source）。
const (
	// ContextSourceLive 会话驻留内存，快照取自实时消息窗口（最准确）。
	ContextSourceLive = "live"
	// ContextSourcePersisted 会话未驻留内存（进程重启或闲置回收），快照来自
	// 持久化历史；系统提示词按当前配置重建，工具定义不可读。
	ContextSourcePersisted = "persisted"
)

// 系统提示词分段标识（ContextSection.Key）。
const (
	ContextSectionPrompt = "prompt" // 静态提示词（含会话级 Prompt 覆盖）
	ContextSectionSkills = "skills" // available_skills 段
	ContextSectionScene  = "scene"  // 场景描述（主会话还含任务编排指引）
)

// ContextSession 会话上下文摘要（列表行）。时间戳统一 unix 秒，0 表示未知。
type ContextSession struct {
	Key          string `json:"key"`             // 会话键（g:/f: 前缀 + 平台 ID）
	Kind         string `json:"kind"`            // group / friend
	Target       string `json:"target"`          // 去掉 g:/f: 前缀的会话 ID
	MessageCount int    `json:"message_count"`   // 历史消息条数（驻留时取内存实时值）
	CreatedAt    int64  `json:"created_at"`      // 会话首次写入时间
	UpdatedAt    int64  `json:"updated_at"`      // 会话最近写入时间
	Resident     bool   `json:"resident"`        // 是否驻留在内存会话缓存
	Busy         bool   `json:"busy"`            // 是否有回合正在执行
	Stage        string `json:"stage,omitempty"` // 当前处理阶段（如「等待模型回复」）
	Pending      int    `json:"pending"`         // 排队等待处理的消息数
	Subagents    int    `json:"subagents"`       // 运行中的后台子代理数
	LastActive   int64  `json:"last_active"`     // 最近一次 AI 交互时间（0 表示未知）
	PlanMode     bool   `json:"plan_mode"`       // 是否处于计划模式
}

// ContextToolCall assistant 消息里的一次工具调用。
type ContextToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// ContextMessage 上下文里的一条消息（结构化展示工具调用与推理内容）。
type ContextMessage struct {
	Role       string            `json:"role"`                   // system / user / assistant / tool
	Text       string            `json:"text,omitempty"`         // 文本片段（图片降级为文字标记）
	Images     int               `json:"images,omitempty"`       // 图片片段数
	ToolCallID string            `json:"tool_call_id,omitempty"` // tool 结果消息的调用 ID
	ToolName   string            `json:"tool_name,omitempty"`    // tool 结果消息对应的工具名
	ToolCalls  []ContextToolCall `json:"tool_calls,omitempty"`   // assistant 消息的工具调用
	Reasoning  string            `json:"reasoning,omitempty"`    // 推理内容（reasoning_content）
	Chars      int               `json:"chars"`                  // 本条内容字符数
	EstTokens  int               `json:"est_tokens"`             // 字符数粗估的 token
}

// ContextSection 系统提示词的一段（按真正下发的顺序排列）。
type ContextSection struct {
	Key       string `json:"key"`   // prompt / skills / scene
	Title     string `json:"title"` // 展示标题
	Content   string `json:"content"`
	Chars     int    `json:"chars"`
	EstTokens int    `json:"est_tokens"`
}

// ContextTool 本次会话实际下发给模型的一个工具定义。
// 工具定义是上下文里容易被忽略的大块内容（几十个工具的描述 + 参数 schema），
// 面板与导出都完整保留，便于核对工具面是否膨胀。
type ContextTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  string `json:"parameters"` // 参数 schema 的 JSON 原文
	Chars       int    `json:"chars"`
	EstTokens   int    `json:"est_tokens"`
}

// ContextTodoItem 会话任务清单的一项（每轮以尾部提醒注入上下文）。
type ContextTodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // pending / in_progress / completed
	ActiveForm string `json:"active_form,omitempty"`
}

// ContextDetail 单个会话的完整上下文（详情与导出共用）。
type ContextDetail struct {
	ContextSession
	Source           string            `json:"source"`                  // live / persisted
	System           []ContextSection  `json:"system"`                  // 系统提示词分段
	Messages         []ContextMessage  `json:"messages"`                // 历史消息（按写入顺序）
	Tools            []ContextTool     `json:"tools"`                   // 工具定义（仅驻留会话可读）
	Todo             []ContextTodoItem `json:"todo,omitempty"`          // 任务清单
	SessionToken     string            `json:"session_token,omitempty"` // 会话实例 token（请求头占位符）
	SystemChars      int               `json:"system_chars"`
	SystemTokens     int               `json:"system_tokens"`
	HistoryChars     int               `json:"history_chars"`
	HistoryTokens    int               `json:"history_tokens"`
	ToolsChars       int               `json:"tools_chars"`
	ToolsTokens      int               `json:"tools_tokens"`
	TotalChars       int               `json:"total_chars"`
	TotalTokens      int               `json:"total_tokens"`       // 系统 + 历史 + 工具的粗估
	LastPromptTokens int               `json:"last_prompt_tokens"` // 最近一次请求的真实 prompt token
	MaxContextTokens int               `json:"max_context_tokens"` // 上下文上限（0 表示未配置）
	Hint             string            `json:"hint,omitempty"`     // 数据来源说明
}

// ErrContextBusy 会话正在执行请求：清空 / 删除前需要用户确认终止该请求，
// 面板据此返回 409 并弹出确认框（确认后带 terminate=true 重发）。
var ErrContextBusy = errors.New("会话正在执行请求，需确认终止后才能操作")

// ContextClearResult 清空 / 删除单个会话的结果。
type ContextClearResult struct {
	Key        string `json:"key"`
	OK         bool   `json:"ok"`
	Terminated bool   `json:"terminated"` // 是否终止了正在执行的请求
	Deleted    bool   `json:"deleted"`    // 是否删除了会话本身（清空为 false）
	Error      string `json:"error,omitempty"`
}

// ContextClearAllResult 清空全部会话上下文的结果。
type ContextClearAllResult struct {
	OK         bool                 `json:"ok"`
	Cleared    int                  `json:"cleared"`    // 成功清空的会话数
	Terminated int                  `json:"terminated"` // 被终止的正在执行的请求数
	Failed     []ContextClearResult `json:"failed,omitempty"`
}
