package pluginaichat

import "sync"

// planManager 计划模式状态（内存态，按会话隔离）：/plan on 开启后副作用工具
// 被门禁阻断，AI 只输出分析与实施计划；/plan off 退出（即用户批准执行）。
// 不持久化——计划模式是会话内的临时工作流状态，会话淘汰/重启后自动复位。
type planManager struct {
	mu sync.RWMutex
	on map[string]bool // key = sessionKey
}

func newPlanManager() *planManager {
	return &planManager{on: make(map[string]bool)}
}

func (m *planManager) IsOn(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.on[key]
}

func (m *planManager) Set(key string, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		m.on[key] = true
	} else {
		delete(m.on, key)
	}
}

// planBlockedTools 计划模式下被阻止的副作用工具（对照工具注册处逐一核对）。
// todo_write 刻意放行：任务清单是规划工作流的一部分，无副作用；
// screenshot/active_window/list_windows 是只读观察，不阻断（计划模式下
// AI 可以看屏幕做分析）；其余只读/中性工具（time/webSearch/webExplore/
// 历史消息（qq_get_msg_history 等）/load_images/local_image/config_get/
// memory_search/kb_search/skill_*/mcp_list/clock_list/clock_get/clock_log/
// subagent_list/subagent_cancel/team_list）不受影响。
var planBlockedTools = map[string]struct{}{
	"bash": {}, "file": {}, "config_set": {}, "config_file_set": {},
	"write_file": {}, "edit_file": {}, // 文件写入/编辑有副作用；read_file/glob/grep 只读放行
	"mouse_click": {}, "mousemove": {}, "mouse_scroll": {},
	"keyboard_type": {}, "keyboard_press": {},
	"memory_save": {}, "memory_update": {}, "memory_forget": {}, "kb_add": {},
	"clock_create": {}, "clock_update": {}, "clock_delete": {},
	"skill_install": {}, "skill_remove": {},
	"mcp_add": {}, "mcp_remove": {}, "mcp_reconnect": {},
	"subagent_run": {}, "team_run": {}, "team_create": {}, "team_delete": {},
}
