package sysrestart

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/workstate"
)

// testLogger 静默测试日志。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// withCleanRestartState 重置重启协调器的全局状态并在测试结束后恢复，
// 避免测试之间相互影响。
func withCleanRestartState(t *testing.T) {
	t.Helper()
	stateMu.Lock()
	pending = nil
	stateMu.Unlock()
	savedRestart := selfRestart
	savedTimeout := DefaultWaitTimeout()
	t.Cleanup(func() {
		// 先换成空实现，确保后台 goroutine 即使还在收尾也不会真正重启进程
		selfRestart = func(*slog.Logger) {}
		SetDefaultWaitTimeout(savedTimeout)
		// 等后台 goroutine 收尾，避免污染后续测试
		for i := 0; i < 300; i++ {
			if !RestartState().Pending {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		stateMu.Lock()
		pending = nil
		stateMu.Unlock()
		selfRestart = savedRestart
	})
}

// waitCalled 在超时时间内等待重启被触发。
func waitCalled(t *testing.T, ch <-chan struct{}, timeout time.Duration) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestGracefulRestartWaitsForWork 有进行中任务时不重启，任务结束后自动重启。
func TestGracefulRestartWaitsForWork(t *testing.T) {
	withCleanRestartState(t)
	SetDefaultWaitTimeout(5 * time.Second)
	called := make(chan struct{}, 1)
	selfRestart = func(*slog.Logger) { called <- struct{}{} }

	end := workstate.Begin(workstate.KindTurn, "AI 回合")
	st := RequestRestart(testLogger(), RestartOptions{Source: "panel", Reason: "测试重启"})
	if !st.Pending || st.Busy != 1 || st.Source != "panel" {
		t.Fatalf("状态错误: %+v", st)
	}
	if waitCalled(t, called, 300*time.Millisecond) {
		t.Fatal("任务进行中不应重启")
	}
	if got := RestartState(); !got.Pending || got.Busy != 1 {
		t.Fatalf("等待期间状态错误: %+v", got)
	}
	end()
	if !waitCalled(t, called, 3*time.Second) {
		t.Fatal("任务结束后应自动重启")
	}
}

// TestGracefulRestartForce 强制重启跳过等待。
func TestGracefulRestartForce(t *testing.T) {
	withCleanRestartState(t)
	SetDefaultWaitTimeout(time.Minute)
	called := make(chan struct{}, 1)
	selfRestart = func(*slog.Logger) { called <- struct{}{} }

	end := workstate.Begin(workstate.KindSubagent, "子代理")
	defer end()
	RequestRestart(testLogger(), RestartOptions{Source: "panel", Reason: "测试重启"})
	if waitCalled(t, called, 200*time.Millisecond) {
		t.Fatal("未强制时不应重启")
	}
	st := ForceRestart(testLogger())
	if !st.Forced {
		t.Fatalf("强制重启后状态应标记 forced: %+v", st)
	}
	if !waitCalled(t, called, 2*time.Second) {
		t.Fatal("强制重启应立即执行")
	}
}

// TestGracefulRestartTimeout 等待超时后自动强行重启。
func TestGracefulRestartTimeout(t *testing.T) {
	withCleanRestartState(t)
	SetDefaultWaitTimeout(150 * time.Millisecond)
	called := make(chan struct{}, 1)
	selfRestart = func(*slog.Logger) { called <- struct{}{} }

	end := workstate.Begin(workstate.KindClock, "定时任务")
	defer end()
	RequestRestart(testLogger(), RestartOptions{Source: "update", Reason: "自动更新完成"})
	if !waitCalled(t, called, 3*time.Second) {
		t.Fatal("等待超时后应自动重启")
	}
}

// TestRequestRestartDeduplicated 已有重启等待时重复请求不会启动第二个重启协程。
func TestRequestRestartDeduplicated(t *testing.T) {
	withCleanRestartState(t)
	SetDefaultWaitTimeout(time.Minute)
	var called atomic.Int32
	selfRestart = func(*slog.Logger) { called.Add(1) }

	end := workstate.Begin(workstate.KindTurn, "AI 回合")
	st1 := RequestRestart(testLogger(), RestartOptions{Source: "panel", Reason: "面板重启"})
	st2 := RequestRestart(testLogger(), RestartOptions{Source: "update", Reason: "更新完成"})
	if !st1.Pending || !st2.Pending {
		t.Fatal("两次请求都应返回等待中")
	}
	if st2.Reason != "面板重启" {
		t.Fatalf("重复请求应保留首次重启的原因: %+v", st2)
	}
	end()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && called.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if got := called.Load(); got != 1 {
		t.Fatalf("重复请求只应重启一次, got %d", got)
	}
}

// TestRestartFailedClearsPending 重启未能启动时清除等待状态并回调 OnFailed。
func TestRestartFailedClearsPending(t *testing.T) {
	withCleanRestartState(t)
	SetDefaultWaitTimeout(time.Second)
	selfRestart = func(*slog.Logger) {} // 模拟重启失败（Self 返回）
	failed := make(chan struct{}, 1)
	RequestRestart(testLogger(), RestartOptions{
		Source:   "panel",
		Reason:   "测试重启",
		OnFailed: func() { failed <- struct{}{} },
	})
	if !waitCalled(t, failed, 3*time.Second) {
		t.Fatal("重启失败应回调 OnFailed")
	}
	if st := RestartState(); st.Pending {
		t.Fatalf("重启失败后应清除等待状态: %+v", st)
	}
}

// TestRestartStateReportsBusyWithoutPending 没有重启请求时也返回进行中任务。
func TestRestartStateReportsBusyWithoutPending(t *testing.T) {
	withCleanRestartState(t)
	end := workstate.Begin(workstate.KindMessage, "群消息")
	st := RestartState()
	if st.Pending {
		t.Fatalf("不应有等待中的重启: %+v", st)
	}
	if st.Busy != 1 || len(st.Items) != 1 || st.Items[0].Kind != workstate.KindMessage {
		t.Fatalf("进行中任务信息错误: %+v", st)
	}
	if st.Boot == "" || st.PID <= 0 {
		t.Fatalf("进程标识缺失: %+v", st)
	}
	end()
	if st := RestartState(); st.Busy != 0 {
		t.Fatalf("任务结束后应无进行中任务: %+v", st)
	}
}
