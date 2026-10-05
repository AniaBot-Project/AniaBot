// Package sysrestart 重启前回调的单元测试。
package sysrestart

import (
	"testing"
)

// withCleanHooks 在测试内重置重启前回调注册表，测试结束恢复原值。
func withCleanHooks(t *testing.T) {
	t.Helper()
	preRestartMu.Lock()
	saved := preRestartHooks
	preRestartHooks = nil
	preRestartMu.Unlock()
	t.Cleanup(func() {
		preRestartMu.Lock()
		preRestartHooks = saved
		preRestartMu.Unlock()
	})
}

// TestRunPreRestartHooksOrder 回调按注册顺序执行。
func TestRunPreRestartHooksOrder(t *testing.T) {
	withCleanHooks(t)

	var order []int
	OnPreRestart(func() { order = append(order, 1) })
	OnPreRestart(func() { order = append(order, 2) })
	runPreRestartHooks()

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("回调应按注册顺序执行: %v", order)
	}
}

// TestRunPreRestartHooksPanicIsolation 单个回调 panic 不影响其它回调。
func TestRunPreRestartHooksPanicIsolation(t *testing.T) {
	withCleanHooks(t)

	ran := false
	OnPreRestart(func() { panic("回调异常") })
	OnPreRestart(func() { ran = true })
	runPreRestartHooks()

	if !ran {
		t.Fatal("panic 回调之后的回调仍应执行")
	}
}

// TestOnPreRestartNil 注册 nil 回调被忽略，不 panic。
func TestOnPreRestartNil(t *testing.T) {
	withCleanHooks(t)

	OnPreRestart(nil)
	runPreRestartHooks()
}
