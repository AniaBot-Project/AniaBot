//go:build windows

package sysrestart

import (
	"log/slog"
	"os"
	"os/exec"
)

// Self 以相同参数启动新进程后退出当前进程
// （Windows 不支持 exec 语义，子进程继承控制台与标准流）。
// 使用启动时缓存的 selfExe，避免二进制被改名交换后取到旧路径。
func Self(logger *slog.Logger) {
	restartMu.Lock()
	defer restartMu.Unlock()
	if restartStarted.Load() {
		logger.Warn("忽略重复的重启请求，已有一个新进程正在启动")
		return
	}
	exe := selfExe
	if exe == "" {
		logger.Error("重启失败：无法获取可执行文件路径")
		return
	}
	wd, _ := os.Getwd()
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Dir = wd
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logger.Info("正在重启 AniaBot...")
	runPreRestartHooks() // 重启前清理（如插件卸载钩子）
	if err := cmd.Start(); err != nil {
		logger.Error("重启失败", "error", err)
		return
	}
	restartStarted.Store(true)
	os.Exit(0)
}
