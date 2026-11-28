// Package workstate 登记进程内正在处理的工作单元（消息插件链、AI 请求、后台任务等）。
//
// 供进程自重启（面板重启、/reboot、自动更新、插件市场）在替换进程前判断
//
//	「是否还有请求正在处理」：有则等待其完成，避免把用户正在等的回复/工具调用
//	直接截断。面板通过 sysrestart 状态接口展示这些进行中的任务并支持强制重启。
//
// 只做计数与快照，不阻塞工作本身：Begin 返回的结束函数必须调用（通常 defer），
// 重复调用安全。所有方法并发安全。
package workstate

import (
	"context"
	"sort"
	"sync"
	"time"
)

// 工作单元类别。面板可据此分类展示（中文说明由调用方写在 Label 里）。
const (
	KindMessage   = "message"   // 平台消息在框架插件链中的处理
	KindNotice    = "notice"    // 平台通知事件广播
	KindPlatform  = "platform"  // 平台特定事件
	KindTurn      = "turn"      // AI 对话回合
	KindConsole   = "console"   // 面板试聊请求
	KindClock     = "clock"     // AI 定时任务一次执行
	KindProactive = "proactive" // 主动互动一次生成
	KindSubagent  = "subagent"  // 后台子代理/交接/追问
	KindTeam      = "team"      // Agent 团队运行
	KindWorkflow  = "workflow"  // 工作流运行
)

// Item 一个正在处理的工作单元。
type Item struct {
	ID    uint64    `json:"id"`
	Kind  string    `json:"kind"`
	Label string    `json:"label"`
	Since time.Time `json:"since"`
}

// State 当前进行中工作的快照。
type State struct {
	Count int    `json:"count"`
	Items []Item `json:"items"`
}

// maxSnapshotItems 快照最多返回的明细条数（Count 仍是准确值）。
// 面板只需要看个大概，避免异常情况下把响应体撑大。
const maxSnapshotItems = 100

var (
	mu     sync.Mutex
	nextID uint64
	active = make(map[uint64]Item)
	// idleCh 在 active 为空时处于已关闭状态；从空变为非空时新建一个 channel，
	// WaitIdle 在 channel 关闭（即重新空闲）时被唤醒。
	idleCh   = closedChan()
	idleOpen = true // idleCh 是否处于「已关闭 = 空闲」状态
)

// closedChan 返回一个已关闭的空 channel。
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// Begin 登记一个进行中的工作单元，返回结束时调用的函数（幂等，建议 defer）。
// kind 取本包的 Kind* 常量；label 是给面板展示的简短中文描述。
func Begin(kind, label string) func() {
	mu.Lock()
	nextID++
	id := nextID
	active[id] = Item{ID: id, Kind: kind, Label: label, Since: time.Now()}
	if idleOpen {
		idleCh = make(chan struct{})
		idleOpen = false
	}
	mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { end(id) })
	}
}

// end 移除一个工作单元；移除后若已无进行中工作则关闭 idleCh 唤醒等待者。
func end(id uint64) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := active[id]; !ok {
		return
	}
	delete(active, id)
	if len(active) == 0 && !idleOpen {
		close(idleCh)
		idleOpen = true
	}
}

// Snapshot 返回当前进行中工作的快照（按开始时间排序，明细最多 maxSnapshotItems 条）。
func Snapshot() State {
	mu.Lock()
	snap := State{Count: len(active)}
	if snap.Count > 0 {
		items := make([]Item, 0, len(active))
		for _, it := range active {
			items = append(items, it)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Since.Equal(items[j].Since) {
				return items[i].ID < items[j].ID
			}
			return items[i].Since.Before(items[j].Since)
		})
		if len(items) > maxSnapshotItems {
			items = items[:maxSnapshotItems]
		}
		snap.Items = items
	}
	mu.Unlock()
	return snap
}

// WaitIdle 阻塞直到当前没有进行中的工作，或 ctx 结束。
// 返回 true 表示已空闲；false 表示 ctx 先结束（仍有工作在进行）。
func WaitIdle(ctx context.Context) bool {
	for {
		mu.Lock()
		if len(active) == 0 {
			mu.Unlock()
			return true
		}
		ch := idleCh
		mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-ch:
		}
	}
}
