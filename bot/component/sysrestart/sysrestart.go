// Package sysrestart 提供进程自重启能力。
//
// 供 Web 控制面板（重启按钮 / 自动更新）、系统插件的 /reboot 命令与
// 插件市场流水线共用：以相同命令行参数重启当前进程，使配置修改生效。
package sysrestart

import (
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// selfExe 进程启动时捕获的可执行文件路径。
//
// 自动更新的「改名交换」会把运行中的二进制 rename 为 <exe>.old，此后在
// Linux 上再调 os.Executable() 读到的 /proc/self/exe 跟随 inode，会指向
// 旧二进制（<exe>.old），导致 swap 换错文件、exec 重启回旧版本。
// 因此必须在任何 rename 发生之前（包初始化时）把路径固定下来，
// 更新替换与重启统一使用这个启动时的路径。
var selfExe = captureSelfExe()

var (
	restartMu      sync.Mutex
	restartStarted atomic.Bool
)

// 重启前回调（如 core 注册的插件卸载钩子清扫）。
var (
	preRestartMu    sync.Mutex
	preRestartHooks []func()
)

// OnPreRestart 注册进程重启前执行的回调，在替换/启动新进程之前按注册
// 顺序同步调用。回调需自行保证快速返回与并发安全，单个回调 panic 不影响
// 其余回调与重启流程。
func OnPreRestart(fn func()) {
	if fn == nil {
		return
	}
	preRestartMu.Lock()
	defer preRestartMu.Unlock()
	preRestartHooks = append(preRestartHooks, fn)
}

// runPreRestartHooks 依次执行已注册的重启前回调。
func runPreRestartHooks() {
	preRestartMu.Lock()
	hooks := append([]func(){}, preRestartHooks...)
	preRestartMu.Unlock()
	for _, fn := range hooks {
		func() {
			defer func() {
				if err := recover(); err != nil {
					slog.Default().Error("重启前回调执行异常", "error", err)
				}
			}()
			fn()
		}()
	}
}

// captureSelfExe 返回当前可执行文件路径，失败时返回空串。
func captureSelfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// Exe 返回启动时缓存的可执行文件路径（见 selfExe 的注释）。
func Exe() string {
	return selfExe
}
