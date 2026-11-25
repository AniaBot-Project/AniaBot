package plugininfo

// 面板「后台任务」页使用的只读视图类型。
//
// 放在 common/plugininfo 而不是 bot/adminpanel：插件只允许依赖 common/* 与
// bot/component/*，面板接口由 adminpanel 定义、通过类型别名引用这里的结构，
// 插件实现方法时无需 import bot/adminpanel（与 team/querylog 等面板数据源一致）。

// AgentTaskView 后台任务注册表的一行（对应 SQL 表 ania_agent_task，面板展示用）。
// 时间戳统一 unix 秒，0 表示未开始 / 未结束。Prompt / Result / Meta 可能较长，
// 由面板前端负责折叠与结构化展示。
type AgentTaskView struct {
	ID         string `json:"id"`
	SessionKey string `json:"session_key"`
	ParentID   string `json:"parent_id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Title      string `json:"title"`
	Prompt     string `json:"prompt"`
	Result     string `json:"result"`
	Error      string `json:"error"`
	StopReason string `json:"stop_reason"`
	Meta       string `json:"meta"`
	Tokens     int64  `json:"tokens"`
	CreatedAt  int64  `json:"created_at"`
	StartedAt  int64  `json:"started_at"`
	UpdatedAt  int64  `json:"updated_at"`
	FinishedAt int64  `json:"finished_at"`
}

// AgentGoalView 一个 active 会话目标（表 ania_agent_goal，跨回合自动续跑）。
type AgentGoalView struct {
	SessionKey     string `json:"session_key"`
	Objective      string `json:"objective"`
	Phase          string `json:"phase"`
	RoundsStarted  int    `json:"rounds_started"`
	MaxRounds      int    `json:"max_rounds"`
	BlockedCode    string `json:"blocked_code"`
	BlockedMessage string `json:"blocked_message"`
	UpdatedAt      int64  `json:"updated_at"`
}
