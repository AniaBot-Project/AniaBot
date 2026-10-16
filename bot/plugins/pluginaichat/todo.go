package pluginaichat

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// todoItem 一条任务清单项（同时是 todo_write 的参数元素，JSON 字段对齐主流 agent）。
type todoItem struct {
	Content    string `json:"content" desc:"任务内容，一句话描述"`
	Status     string `json:"status" desc:"任务状态：pending（待办）/ in_progress（进行中）/ completed（已完成）"`
	ActiveForm string `json:"activeForm,omitempty" desc:"进行中时展示的现在进行时描述（如「正在整理数据」），可留空"`
}

const (
	todoStatusPending    = "pending"
	todoStatusInProgress = "in_progress"
	todoStatusCompleted  = "completed"

	todoMaxItems        = 50
	todoMaxContentRunes = 200
)

// todoManager 任务清单（内存态，按会话隔离）：AI 通过 todo_write 以全量替换语义
// 维护清单；不持久化——清单是单次工作流的临时状态，会话淘汰/重启后自动清空。
// lastRemindHash 记录上次注入提醒的内容哈希，清单没变时不重复注入，避免每轮污染上下文。
type todoManager struct {
	mu             sync.Mutex
	lists          map[string][]todoItem // key = sessionKey
	lastRemindHash map[string]uint64
}

func newTodoManager() *todoManager {
	return &todoManager{
		lists:          make(map[string][]todoItem),
		lastRemindHash: make(map[string]uint64),
	}
}

// replace 全量替换指定会话的清单（先校验，通过才落库；空列表 = 清空）。
func (m *todoManager) replace(key string, items []todoItem) error {
	if len(items) > todoMaxItems {
		return fmt.Errorf("任务数量超限：最多 %d 项，收到 %d 项", todoMaxItems, len(items))
	}
	inProgress := 0
	for i := range items {
		items[i].Content = strings.TrimSpace(items[i].Content)
		if items[i].Content == "" {
			return fmt.Errorf("第 %d 项 content 不能为空", i+1)
		}
		if utf8.RuneCountInString(items[i].Content) > todoMaxContentRunes {
			return fmt.Errorf("第 %d 项 content 超过 %d 字上限", i+1, todoMaxContentRunes)
		}
		switch items[i].Status {
		case todoStatusPending, todoStatusCompleted:
		case todoStatusInProgress:
			inProgress++
		default:
			return fmt.Errorf("第 %d 项 status 非法：%q（可选 pending / in_progress / completed）", i+1, items[i].Status)
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("同一时间只能有 1 项 in_progress（收到 %d 项）：完成一项再开始下一项", inProgress)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(items) == 0 {
		delete(m.lists, key)
	} else {
		m.lists[key] = items
	}
	return nil
}

// clear 清空指定会话的清单与提醒哈希（会话被淘汰时调用）。
func (m *todoManager) clear(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.lists, key)
	delete(m.lastRemindHash, key)
}

// pendingReminder 生成未完成事项提醒（尾部注入用）；清单无未完成项时返回空串。
// 内容与上次注入相同时返回空串（哈希去重），避免每轮重复占用上下文。
func (m *todoManager) pendingReminder(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	items := m.lists[key]
	var sb strings.Builder
	pending := 0
	for _, it := range items {
		if it.Status == todoStatusCompleted {
			continue
		}
		pending++
		fmt.Fprintf(&sb, "%d. [%s] %s\n", pending, todoStatusLabel(it.Status), it.Content)
	}
	if pending == 0 {
		delete(m.lastRemindHash, key)
		return ""
	}
	text := sb.String()
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	hash := h.Sum64()
	if m.lastRemindHash[key] == hash {
		return ""
	}
	m.lastRemindHash[key] = hash
	return "<todo_reminder>当前任务清单还有 " + strconv.Itoa(pending) + " 项未完成：\n" + text +
		"每完成一项请立即用 todo_write 更新状态；全部完成后清空清单。</todo_reminder>"
}

// todoStatusLabel 状态的中文标签（结果文本与提醒共用）。
func todoStatusLabel(status string) string {
	switch status {
	case todoStatusInProgress:
		return "进行中"
	case todoStatusCompleted:
		return "已完成"
	default:
		return "待办"
	}
}

// ---- todo_write 工具 ----

type todoWriteParams struct {
	Todos []todoItem `json:"todos" desc:"完整的新任务清单（全量替换旧清单，不是追加）；传空数组清空清单"`
}

type todoWriteTool struct {
	llmtool.BaseTool[todoWriteParams]
	mgr *todoManager
	key string // sessionKey，清单按会话隔离
}

// newTodoWriteTool 创建绑定当前会话的 todo_write 工具。仅注册到主会话（getChat），
// 不进 registerScopedTools——子代理/定时任务的一次性会话不共享父会话清单。
func newTodoWriteTool(mgr *todoManager, key string) llmtool.Tool {
	return &todoWriteTool{
		BaseTool: llmtool.MakeBaseTool("todo_write",
			"创建或更新当前会话的任务清单，用于跟踪复杂多步任务的进度。接到需要多个步骤的任务时先建清单；开始一项前把它标为 in_progress（同一时间只能一项），完成后立刻标 completed。每次调用都传入完整的新清单（全量替换），传空数组清空。简单一两步能完成的事不要用", todoWriteParams{}),
		mgr: mgr,
		key: key,
	}
}

func (t *todoWriteTool) Execute(_ context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p := params.(*todoWriteParams)
	if err := t.mgr.replace(t.key, p.Todos); err != nil {
		return "", err
	}
	if len(p.Todos) == 0 {
		return "任务清单已清空", nil
	}

	counts := map[string]int{}
	for _, it := range p.Todos {
		counts[it.Status]++
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "任务清单已更新：共 %d 项（进行中 %d，待办 %d，已完成 %d）\n",
		len(p.Todos), counts[todoStatusInProgress], counts[todoStatusPending], counts[todoStatusCompleted])
	for i, it := range p.Todos {
		fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, todoStatusLabel(it.Status), it.Content)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
