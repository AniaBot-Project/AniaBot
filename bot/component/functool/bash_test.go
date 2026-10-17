package functool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

func mustBash(t *testing.T, cfg BashConfig) *BashTool {
	t.Helper()
	tool, err := NewBashTool(cfg)
	if err != nil {
		t.Fatalf("NewBashTool: %v", err)
	}
	return tool
}

// TestBashCheckCommandThreeTier 三段式权限模型：黑名单→拒绝；白名单→放行；
// 都不命中→审批（含两份名单都为空的场景）。
func TestBashCheckCommandThreeTier(t *testing.T) {
	tool := mustBash(t, BashConfig{
		Whitelist: []string{`^echo `},
		Blacklist: []string{`rm -rf`, `shutdown`},
	})

	cases := []struct {
		cmd  string
		want CmdVerdict
	}{
		{"echo hello", CmdAllow},
		{"rm -rf /", CmdDeny},
		{"shutdown now", CmdDeny},
		{"ls -la", CmdAsk},       // 不在白名单 → 审批（旧语义为拒绝）
		{"cat file.txt", CmdAsk}, // 同上
	}
	for _, tc := range cases {
		got, err := tool.checkCommand(tc.cmd)
		if tc.want == CmdDeny {
			if got != CmdDeny || err == nil {
				t.Errorf("%q: 期望拒绝, got verdict=%v err=%v", tc.cmd, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: 非拒绝档不应返回 error, got %v", tc.cmd, err)
		}
		if got != tc.want {
			t.Errorf("%q: 期望 %v, got %v", tc.cmd, tc.want, got)
		}
	}

	// 黑名单优先于白名单
	tool2 := mustBash(t, BashConfig{Whitelist: []string{`.*`}, Blacklist: []string{`rm -rf`}})
	if v, _ := tool2.checkCommand("echo a && rm -rf /"); v != CmdDeny {
		t.Errorf("黑名单应优先于白名单, got %v", v)
	}

	// 无名单：全部走审批档（审批未启用时默认放行，只认黑名单）
	tool3 := mustBash(t, BashConfig{})
	if v, err := tool3.checkCommand("echo hi"); v != CmdAsk || err != nil {
		t.Errorf("无名单时应进入审批档, got verdict=%v err=%v", v, err)
	}
}

// TestBashAskWithoutApprovalChannel 审批档命令在无审批通道（RequestApproval=nil）
// 时默认放行（只认黑名单），命令正常执行。
func TestBashAskWithoutApprovalChannel(t *testing.T) {
	tool := mustBash(t, BashConfig{})
	out, err := tool.Execute(context.Background(), &BashParams{Command: "echo hi"}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("审批未启用时应默认放行, got out=%q err=%v", out, err)
	}
}

// TestBashAskApprovalFlow 审批档命令经 RequestApproval 批准才执行，拒绝则返回原因。
func TestBashAskApprovalFlow(t *testing.T) {
	tool := mustBash(t, BashConfig{})

	var gotTool, gotSummary string
	approve := llmtool.CallBackFuncs{RequestApproval: func(_ context.Context, toolName, summary string) (bool, string) {
		gotTool, gotSummary = toolName, summary
		return true, ""
	}}
	out, err := tool.Execute(context.Background(), &BashParams{Command: "echo bash-approval-ok"}, approve)
	if err != nil {
		t.Fatalf("批准后应执行成功: %v", err)
	}
	if !strings.Contains(out, "bash-approval-ok") {
		t.Fatalf("输出不符: %q", out)
	}
	if gotTool != "bash" || !strings.Contains(gotSummary, "echo bash-approval-ok") {
		t.Fatalf("审批参数不符: tool=%q summary=%q", gotTool, gotSummary)
	}

	deny := llmtool.CallBackFuncs{RequestApproval: func(context.Context, string, string) (bool, string) {
		return false, "用户拒绝了本次操作"
	}}
	if _, err := tool.Execute(context.Background(), &BashParams{Command: "echo never-runs"}, deny); err == nil || !strings.Contains(err.Error(), "用户拒绝了本次操作") {
		t.Fatalf("拒绝后应返回原因, got %v", err)
	}
}

// TestBashWhitelistSkipsApproval 白名单命令直接执行，不触发审批。
func TestBashWhitelistSkipsApproval(t *testing.T) {
	tool := mustBash(t, BashConfig{Whitelist: []string{`^echo `}})
	called := false
	cbs := llmtool.CallBackFuncs{RequestApproval: func(context.Context, string, string) (bool, string) {
		called = true
		return false, "不应被调用"
	}}
	out, err := tool.Execute(context.Background(), &BashParams{Command: "echo whitelist-ok"}, cbs)
	if err != nil || !strings.Contains(out, "whitelist-ok") {
		t.Fatalf("白名单命令应直接执行: out=%q err=%v", out, err)
	}
	if called {
		t.Fatal("白名单命令不应触发审批")
	}
}

// TestBashWorkingDir working_dir 配置：命令从该目录开始执行。
func TestBashWorkingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("working-dir-ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := mustBash(t, BashConfig{WorkingDir: dir, Whitelist: []string{`.*`}})
	cmd := "cat marker.txt"
	if runtime.GOOS == "windows" {
		cmd = "type marker.txt"
	}
	out, err := tool.Execute(context.Background(), &BashParams{Command: cmd}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(out, "working-dir-ok") {
		t.Fatalf("working_dir 未生效: out=%q err=%v", out, err)
	}
}

// TestBashPersistCwd persist_cwd：命令内 cd 延续到后续调用，且目录标记不漏进输出。
func TestBashPersistCwd(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("平台相关")
	}
	sub := filepath.Join(t.TempDir(), "persistcwd_marked")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := mustBash(t, BashConfig{PersistCwd: true, Whitelist: []string{`.*`}})

	var cdCmd, showCmd string
	if runtime.GOOS == "windows" {
		cdCmd = `cd /d "` + sub + `"`
		showCmd = "cd"
	} else {
		cdCmd = "cd '" + sub + "'"
		showCmd = "pwd"
	}

	out, err := tool.Execute(context.Background(), &BashParams{Command: cdCmd}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("cd 失败: %v out=%q", err, out)
	}
	if strings.Contains(out, "ANIABOT_CWD") {
		t.Fatalf("目录标记应从输出剥离: %q", out)
	}

	out, err = tool.Execute(context.Background(), &BashParams{Command: showCmd}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("pwd 失败: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "persistcwd_marked") {
		t.Fatalf("cwd 未持久化到下一次调用: %q", out)
	}

	// 退出码保持：命令失败时退出码语义不变（非零退出码作为结果而非工具错误返回）
	failCmd := "exit 3"
	if runtime.GOOS == "windows" {
		failCmd = "exit /b 3"
	}
	out, err = tool.Execute(context.Background(), &BashParams{Command: failCmd}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(out, "退出码 3") {
		t.Fatalf("包装脚本应保持退出码: out=%q err=%v", out, err)
	}
}

// TestBashMaxOutputTruncate max_output：超长输出按头尾保留，中段隐藏。
func TestBashMaxOutputTruncate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows cmd 生成超长输出较繁琐，truncateMiddle 已单测覆盖")
	}
	tool := mustBash(t, BashConfig{Whitelist: []string{`.*`}, MaxOutput: 100})
	cmd := "printf 'H%.0s' $(seq 120); echo; printf 'T%.0s' $(seq 120)"
	out, err := tool.Execute(context.Background(), &BashParams{Command: cmd}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "已截断") {
		t.Fatalf("超长输出应触发中段截断: %q", out)
	}
	r := []rune(out)
	if len(r) > 200 {
		t.Fatalf("截断后应明显变短: %d runes", len(r))
	}
}

// TestBashTimeoutSec 超时可配置：配置 1 秒超时，睡眠 3 秒的命令应被终止并提示。
func TestBashTimeoutSec(t *testing.T) {
	var cmd string
	switch runtime.GOOS {
	case "windows":
		cmd = "ping -n 4 127.0.0.1 >nul"
	case "darwin":
		cmd = "sleep 3"
	default:
		cmd = "sleep 3"
	}
	tool := mustBash(t, BashConfig{Whitelist: []string{`.*`}, TimeoutSec: 1})
	out, err := tool.Execute(context.Background(), &BashParams{Command: cmd}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("超时应作为结果返回而非错误: %v", err)
	}
	if !strings.Contains(out, "超时") {
		t.Fatalf("应提示超时: %q", out)
	}
}

// TestBashWaitDelayConfigured 命令必须设置 WaitDelay。
//
// ctx 到期时 CommandContext 只 Kill 直接子进程；`cmd /C <命令>`（或 `sh -c`）
// 派生的孙进程会继承 stdout/stderr 管道，管道写端未关闭时 Run() 会一直阻塞在
// 等待 copy goroutine 上——工具调用挂死，编排层的 wg.Wait() 让整轮请求无法
// 返回（5 分钟消息预算失效）。WaitDelay 是让 Run() 必然收敛的唯一保证，
// 这里直接锁定该配置，避免后续重构把兜底删掉（该挂死场景依赖具体进程树，
// 难以在单元测试里稳定复现）。
func TestBashWaitDelayConfigured(t *testing.T) {
	tool := mustBash(t, BashConfig{Whitelist: []string{`.*`}})
	if got := tool.commandWaitDelay(); got != bashWaitDelay {
		t.Fatalf("bash 命令必须设置 WaitDelay=%v, got %v", bashWaitDelay, got)
	}
	if bashWaitDelay <= 0 {
		t.Fatal("WaitDelay 必须为正，否则等同未设置")
	}
}

// TestBashEnv 自定义环境变量追加到继承环境：配置的变量生效，
// 进程内已有的变量不丢失（回归 161b30a：重构时 env 应用逻辑被整体删除）。
func TestBashEnv(t *testing.T) {
	t.Setenv("ANIABOT_ENV_CANARY", "inherited-ok")
	var printCmd string
	if runtime.GOOS == "windows" {
		printCmd = "echo %ANIABOT_ENV_CANARY% %ANIABOT_ENV_CUSTOM%"
	} else {
		printCmd = `echo "$ANIABOT_ENV_CANARY" "$ANIABOT_ENV_CUSTOM"`
	}
	tool := mustBash(t, BashConfig{
		Whitelist: []string{`.*`},
		Env:       []string{"ANIABOT_ENV_CUSTOM=configured-ok"},
	})
	out, err := tool.Execute(context.Background(), &BashParams{Command: printCmd}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "inherited-ok") || !strings.Contains(out, "configured-ok") {
		t.Fatalf("子进程环境应同时含继承值与配置值: %q", out)
	}
}

// TestTruncateMiddle 头尾保留式截断的比例与标记。
func TestTruncateMiddle(t *testing.T) {
	s := strings.Repeat("a", 120) + strings.Repeat("b", 120)
	got := truncateMiddle(s, 40)
	if !strings.Contains(got, "已截断") {
		t.Fatalf("应包含截断标记: %q", got)
	}
	if !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "bbbb") {
		t.Fatalf("应保留头尾: %q", got)
	}
	// 不超长原样返回
	if got := truncateMiddle("short", 40); got != "short" {
		t.Fatalf("不超长不应截断: %q", got)
	}
}
