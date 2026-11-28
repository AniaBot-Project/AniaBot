// 优雅重启协调器：重启前先等待进程内进行中的工作（消息插件链、AI 请求、
// 后台子代理等）完成，避免把用户正在等待的回复或工具调用直接截断。
//
// 面板、/reboot、自动更新与插件市场都通过 RequestRestart 发起重启：
//   - 默认等待全部进行中工作结束，最长 DefaultWaitTimeout（可由
//     bot.restart_wait_timeout_sec 配置，0 = 一直等待）；
//   - 等待期间可通过 ForceRestart 立即强制重启；
//   - 等待状态（等待原因、进行中任务明细、剩余时间）由 RestartState 暴露给
//     面板，前端据此显示「重启等待中」提示与「立即强制重启」按钮。
package sysrestart

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeanhua/AniaBot/common/workstate"
)

const (
	// defaultWaitTimeout 未配置时的默认等待上限。
	defaultWaitTimeout = 5 * time.Minute
	// restartPollInterval 等待进行中任务时的轮询间隔。
	restartPollInterval = 150 * time.Millisecond
	// restartGraceDelay 空闲确认时长：最后一次任务结束后再等一小会儿，
	// 避免刚判定空闲就有新消息进入导致重启打断新一轮处理。
	restartGraceDelay = 600 * time.Millisecond
)

// selfRestart 可替换以便测试（默认执行真正的进程替换）。
var selfRestart = Self

// processBoot 本进程实例标识：面板轮询时据此判断「进程是否已经重启」。
// Unix exec 重启后 PID 不变，所以不能用 PID 判断。
var processBoot = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

// waitTimeoutNanos 默认等待上限（纳秒）；0 表示一直等待。
var waitTimeoutNanos atomic.Int64

func init() {
	waitTimeoutNanos.Store(int64(defaultWaitTimeout))
}

// SetDefaultWaitTimeout 设置默认等待上限（0 = 一直等待；负数回退默认值）。
// 由 core 在启动时按 bot.restart_wait_timeout_sec 调用。
func SetDefaultWaitTimeout(d time.Duration) {
	if d < 0 {
		d = defaultWaitTimeout
	}
	waitTimeoutNanos.Store(int64(d))
}

// DefaultWaitTimeout 返回当前的默认等待上限（0 = 一直等待）。
func DefaultWaitTimeout() time.Duration {
	return time.Duration(waitTimeoutNanos.Load())
}

// RestartOptions 一次重启请求的参数。
type RestartOptions struct {
	// Source 发起来源：panel / command / update / marketplace。
	Source string
	// Reason 展示给面板的重启原因（简短中文）。
	Reason string
	// Force 不等待进行中的任务，立即重启。
	Force bool
	// OnFailed 重启未能启动（Self 返回）时回调，调用方可清除自身的重启窗口标记。
	OnFailed func()
}

// RestartStatus 重启协调器 + 进行中工作的状态快照（面板轮询用）。
type RestartStatus struct {
	// Boot 本进程实例标识；变化说明进程已重启。
	Boot string `json:"boot"`
	// PID 当前进程 ID。
	PID int `json:"pid"`
	// Pending 是否有一次重启正在等待/进行中。
	Pending bool `json:"pending"`
	// Source / Reason 本次重启的来源与原因。
	Source string `json:"source"`
	Reason string `json:"reason"`
	// StartedAt 重启请求时间（Unix 秒），未在等待时为 0。
	StartedAt int64 `json:"started_at"`
	// TimeoutSec 等待上限秒数（0 = 一直等待）。
	TimeoutSec int `json:"timeout_sec"`
	// RemainingSec 距离自动强制重启的剩余秒数（-1 = 不限时，0 = 已到时间）。
	RemainingSec int `json:"remaining_sec"`
	// Forced 是否已被请求强制重启。
	Forced bool `json:"forced"`
	// Busy 进行中的工作数量；Items 为明细。
	Busy  int              `json:"busy"`
	Items []workstate.Item `json:"items"`
}

// pendingRestart 一次正在等待的重启。
type pendingRestart struct {
	opts      RestartOptions
	startedAt time.Time
	timeout   time.Duration
	force     atomic.Bool
}

var (
	stateMu sync.Mutex
	pending *pendingRestart
)

// RequestRestart 发起一次优雅重启（异步）：登记等待状态并启动后台 goroutine，
// 等到没有进行中的工作（或超时/被强制）后调用 Self 重启进程。
//
// 已有重启在等待时不会重复启动，返回现有状态；此时若本次请求带 Force 则升级为
// 强制重启。
func RequestRestart(logger *slog.Logger, opts RestartOptions) RestartStatus {
	if logger == nil {
		logger = slog.Default()
	}
	stateMu.Lock()
	if pending != nil {
		if opts.Force {
			pending.force.Store(true)
		}
		st := statusLocked()
		stateMu.Unlock()
		logger.Info("已有重启在等待中，合并本次重启请求", "source", opts.Source, "reason", opts.Reason)
		return st
	}
	p := &pendingRestart{
		opts:      opts,
		startedAt: time.Now(),
		timeout:   time.Duration(waitTimeoutNanos.Load()),
	}
	if opts.Force {
		p.force.Store(true)
	}
	pending = p
	st := statusLocked()
	stateMu.Unlock()
	go runPendingRestart(logger, p)
	return st
}

// ForceRestart 强制立即重启：若已有重启在等待，跳过等待；否则直接发起一次
// 不等待的重启。
func ForceRestart(logger *slog.Logger) RestartStatus {
	if logger == nil {
		logger = slog.Default()
	}
	stateMu.Lock()
	if pending != nil {
		pending.force.Store(true)
		st := statusLocked()
		stateMu.Unlock()
		return st
	}
	stateMu.Unlock()
	return RequestRestart(logger, RestartOptions{
		Source: "panel",
		Reason: "面板强制重启",
		Force:  true,
	})
}

// RestartState 返回当前重启状态与进行中工作快照（不修改任何状态）。
func RestartState() RestartStatus {
	stateMu.Lock()
	defer stateMu.Unlock()
	return statusLocked()
}

// statusLocked 构造状态快照（要求 stateMu 已持有）。
func statusLocked() RestartStatus {
	snap := workstate.Snapshot()
	st := RestartStatus{
		Boot:         processBoot,
		PID:          os.Getpid(),
		Busy:         snap.Count,
		Items:        snap.Items,
		RemainingSec: -1,
	}
	if pending != nil {
		st.Pending = true
		st.Source = pending.opts.Source
		st.Reason = pending.opts.Reason
		st.StartedAt = pending.startedAt.Unix()
		st.TimeoutSec = int(pending.timeout / time.Second)
		st.Forced = pending.force.Load()
		if pending.timeout > 0 {
			remaining := pending.startedAt.Add(pending.timeout).Sub(time.Now())
			if remaining < 0 {
				remaining = 0
			}
			st.RemainingSec = int(remaining / time.Second)
		}
	}
	return st
}

// runPendingRestart 等待进行中的工作结束后重启进程。
// 正常路径不会返回（exec 替换进程 / Windows 启动新进程后 os.Exit）。
func runPendingRestart(logger *slog.Logger, p *pendingRestart) {
	busy := workstate.Snapshot().Count
	logger.Info("收到重启请求，等待进行中的任务完成",
		"source", p.opts.Source, "reason", p.opts.Reason, "busy", busy, "timeout", p.timeout)

	var deadline time.Time
	if p.timeout > 0 {
		deadline = p.startedAt.Add(p.timeout)
	}
	deadlineHit := false
	for {
		if p.force.Load() {
			logger.Info("重启请求被强制立即执行", "source", p.opts.Source)
			break
		}
		if workstate.Snapshot().Count == 0 {
			// 空闲确认期：给刚刚结束/刚开始的边界留一点余量
			time.Sleep(restartGraceDelay)
			if p.force.Load() || workstate.Snapshot().Count == 0 {
				break
			}
			continue
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			deadlineHit = true
			logger.Warn("等待进行中的任务超时，强制重启",
				"busy", workstate.Snapshot().Count, "timeout", p.timeout)
			break
		}
		time.Sleep(restartPollInterval)
	}

	logger.Info("准备重启 AniaBot", "busy", workstate.Snapshot().Count,
		"source", p.opts.Source, "reason", p.opts.Reason, "deadline_hit", deadlineHit)
	selfRestart(logger)

	// 只有重启未能启动才会回到这里：清除等待状态，允许调用方重试。
	stateMu.Lock()
	if pending == p {
		pending = nil
	}
	stateMu.Unlock()
	logger.Error("重启未能启动，已取消本次重启请求")
	if p.opts.OnFailed != nil {
		p.opts.OnFailed()
	}
}
