package functool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

const (
	// bashDefaultTimeoutSec 默认命令超时（秒）：编码任务的编译/测试动辄数十秒，
	// 太短会频繁中断；仍受请求级超时（bot.msg_event_timeout_sec）兜底约束
	bashDefaultTimeoutSec = 300
	// bashDefaultMaxOutput 默认输出保留字符数：编译错误列表等长输出按头尾保留
	bashDefaultMaxOutput = 30000
	// bashCwdMarker persist_cwd 模式下包装脚本回传当前目录的标记前缀
	bashCwdMarker = "__ANIABOT_CWD__"
	// bashCwdFail 标记值：起始目录不存在（cd 失败）时回传，不更新 cwd
	bashCwdFail = "FAIL"
	// bashWaitDelay 命令结束后等待输出管道关闭的上限：ctx 到期只杀直接子进程，
	// 派生出的孙进程若继承 stdout/stderr 管道，Run() 会一直阻塞在等待 copy
	// goroutine 上（工具调用挂死 → 整轮请求无法返回）。设上限后超时即放弃管道，
	// Run() 必然收敛（返回 exec.ErrWaitDelay，按成功处理，输出已收集完整）。
	bashWaitDelay = 5 * time.Second
)

// applyCommandWaitDelay 为待执行的命令设置 WaitDelay 兜底。集中在一处设置便于
// 测试锁定（该挂死场景依赖具体进程树，单元测试难以稳定复现，因此直接断言配置）。
func applyCommandWaitDelay(cmd *exec.Cmd) {
	cmd.WaitDelay = bashWaitDelay
}

// commandWaitDelay 返回当前生效的 WaitDelay（供测试断言兜底未被移除）。
func (t *BashTool) commandWaitDelay() time.Duration { return bashWaitDelay }

// BashConfig bash工具配置
type BashConfig struct {
	Enable    bool     `json:"enable" mapstructure:"enable"`
	Shell     string   `json:"shell" mapstructure:"shell"`         // shell 路径，留空使用系统默认（Linux/macOS 为 sh，Windows 为 cmd）
	Env       []string `json:"env" mapstructure:"env"`             // 环境变量，格式 KEY=VALUE
	Whitelist []string `json:"whitelist" mapstructure:"whitelist"` // 非空时只允许匹配这些正则的命令
	Blacklist []string `json:"blacklist" mapstructure:"blacklist"` // 匹配这些正则的命令被禁止
	// WorkingDir 工作目录：非空时命令从该目录开始执行；persist_cwd 关闭时每次命令
	// 都回到该目录（命令内 cd 不影响下次调用）
	WorkingDir string `json:"working_dir" mapstructure:"working_dir"`
	// PersistCwd 跨调用持久化工作目录：开启后每次命令从上次调用结束时的目录开始，
	// 命令内的 cd 会延续到后续调用（编码任务在同一项目目录内连续操作免写全路径）。
	// 实现为包装脚本回传最终目录（尽力而为：包装失败或目录消失时回落 WorkingDir）
	PersistCwd bool `json:"persist_cwd" mapstructure:"persist_cwd"`
	// TimeoutSec 单条命令超时（秒），<=0 时取默认值（bashDefaultTimeoutSec）
	TimeoutSec int `json:"timeout_sec" mapstructure:"timeout_sec"`
	// MaxOutput 输出最多保留的字符数（按头尾保留、隐藏中段），<=0 时取默认值
	MaxOutput int `json:"max_output" mapstructure:"max_output"`
}

type BashParams struct {
	Command string `json:"command" desc:"要执行的 shell 命令"`
}

type BashTool struct {
	llmtool.BaseTool[BashParams]
	shell     string
	shellArg  string // 命令行包装参数：sh/bash 为 -c，cmd 为 /C
	env       []string
	whitelist []*regexp.Regexp
	blacklist []*regexp.Regexp

	workingDir string // 固定工作目录（persist_cwd 关闭时的起始目录）
	persistCwd bool   // 是否跨调用持久化工作目录
	timeout    time.Duration
	maxOutput  int

	// cwd persist_cwd 模式下记录的当前目录；同轮并行工具调用可能同时执行
	// bash（如「编译并测试」拆两个调用），读写都需持锁
	cwdMu sync.Mutex
	cwd   string
}

// ResolveShell 解析实际使用的 shell 及其包装参数。
// 未配置时使用系统默认 shell，命令由该 shell 解释，
// AI 可在命令中显式调用 bash/ash/python 等其他解释器。
// 导出供 agenthook 等需要在宿主机执行管理员配置命令的组件复用。
func ResolveShell(configured string) (shell, shellArg string) {
	if configured == "" {
		if runtime.GOOS == "windows" {
			return "cmd", "/C"
		}
		return "sh", "-c"
	}
	base := strings.ToLower(filepath.Base(configured))
	if base == "cmd" || base == "cmd.exe" {
		return configured, "/C"
	}
	return configured, "-c"
}

// CmdVerdict 命令校验结论（三段式权限模型）。
type CmdVerdict int

const (
	// CmdAllow 命中白名单（或无任何名单），直接放行
	CmdAllow CmdVerdict = iota
	// CmdDeny 命中黑名单，直接拒绝
	CmdDeny
	// CmdAsk 既不在黑名单也不在白名单：需人工审批（经 CallBackFuncs.RequestApproval）；
	// 审批未启用（RequestApproval 为 nil）时默认放行
	CmdAsk
)

func NewBashTool(config BashConfig) (*BashTool, error) {
	shell, shellArg := ResolveShell(config.Shell)

	compile := func(patterns []string) ([]*regexp.Regexp, error) {
		regs := make([]*regexp.Regexp, 0, len(patterns))
		for _, p := range patterns {
			r, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("编译正则 %q 失败: %w", p, err)
			}
			regs = append(regs, r)
		}
		return regs, nil
	}

	whitelist, err := compile(config.Whitelist)
	if err != nil {
		return nil, err
	}
	blacklist, err := compile(config.Blacklist)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(config.TimeoutSec) * time.Second
	if config.TimeoutSec <= 0 {
		timeout = bashDefaultTimeoutSec * time.Second
	}
	maxOutput := config.MaxOutput
	if maxOutput <= 0 {
		maxOutput = bashDefaultMaxOutput
	}

	cwdHint := ""
	if config.PersistCwd {
		cwdHint = "工作目录跨调用持久化：命令内 cd 会延续到后续调用"
	} else if config.WorkingDir != "" {
		cwdHint = fmt.Sprintf("命令固定从 %s 开始执行", config.WorkingDir)
	}

	desc := fmt.Sprintf("在宿主机上执行 shell 命令（由 %s 解释执行），超时 %s，输出超过 %d 字符时保留头尾截断。%s权限分三档：命中黑名单直接拒绝；命中白名单直接放行；两者都不命中时会向用户发起审批，等用户回复「允许」后才执行（审批未启用则默认放行）。注意：不要假设环境存在 bash，运行 .sh 脚本优先用 `sh 脚本路径`；需要 python3 等其他解释器时先用 `command -v` 确认其存在",
		shell, timeout, maxOutput, cwdHint)
	return &BashTool{
		BaseTool:   llmtool.MakeBaseTool("bash", desc, BashParams{}),
		shell:      shell,
		shellArg:   shellArg,
		env:        config.Env,
		whitelist:  whitelist,
		blacklist:  blacklist,
		workingDir: config.WorkingDir,
		persistCwd: config.PersistCwd,
		timeout:    timeout,
		maxOutput:  maxOutput,
	}, nil
}

// checkCommand 三段式校验：黑名单优先（命中即拒绝）；白名单命中即放行；
// 两者都不命中返回 CmdAsk 交由人工审批（审批未启用时默认放行）。
func (t *BashTool) checkCommand(cmd string) (CmdVerdict, error) {
	for _, blocked := range t.blacklist {
		if blocked.MatchString(cmd) {
			return CmdDeny, fmt.Errorf("bash: 命令被规则 %q 禁止", blocked.String())
		}
	}

	for _, w := range t.whitelist {
		if w.MatchString(cmd) {
			return CmdAllow, nil
		}
	}

	return CmdAsk, nil
}

// summarizeCommand 审批提示中的命令摘要：压缩空白并按 rune 截断，避免超长命令刷屏。
func summarizeCommand(cmd string) string {
	const maxRunes = 300
	compact := strings.Join(strings.Fields(cmd), " ")
	if r := []rune(compact); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return compact
}

func (t *BashTool) Execute(ctx context.Context, params any, cbs llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*BashParams)
	if !ok {
		return "", fmt.Errorf("bash: 参数类型错误")
	}
	if p.Command == "" {
		return "", fmt.Errorf("bash: 命令不能为空")
	}

	log.Println("执行bash... 参数: ", p.Command)

	verdict, err := t.checkCommand(p.Command)
	if err != nil {
		return "", err
	}
	if verdict == CmdAsk && cbs.RequestApproval != nil {
		allowed, reason := cbs.RequestApproval(ctx, "bash", "执行命令："+summarizeCommand(p.Command))
		if !allowed {
			return "", fmt.Errorf("bash: 命令未获批准：%s", reason)
		}
	}
	// CmdAsk 且审批未启用（RequestApproval 为 nil）：默认放行，只认黑名单

	// 基于调用方 ctx 派生超时：/stop 取消请求时命令随之终止，
	// 不会因忽略 ctx 而让长命令继续占满会话锁与并发槽
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	var (
		cmd     *exec.Cmd
		cleanup func()
	)
	if t.persistCwd {
		// 持久化工作目录：从上次结束目录（缺省回落固定工作目录）出发，
		// 包装脚本回传命令结束时的目录并保持退出码不变
		t.cwdMu.Lock()
		start := t.cwd
		t.cwdMu.Unlock()
		if start == "" {
			start = t.workingDir
		}
		cmd, cleanup, err = t.buildPersistCwdCmd(ctx, start, p.Command)
		if err != nil {
			return "", fmt.Errorf("bash: 构造命令失败: %w", err)
		}
		defer cleanup()
	} else {
		cmd = exec.CommandContext(ctx, t.shell, t.shellArg, p.Command)
		if t.workingDir != "" {
			cmd.Dir = t.workingDir
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// WaitDelay：ctx 到期/被取消时，CommandContext 只会 Kill 直接子进程；而
	// `cmd /C <命令>`（或 `sh -c`）派生的**孙进程**会继承 stdout/stderr 管道，
	// 管道写端未全部关闭时 Run() 仍会阻塞在等待 copy goroutine 上——即工具调用
	// 挂死，并因编排层 wg.Wait() 让整轮请求无法返回（5 分钟消息预算失效）。
	// 由 applyCommandWaitDelay 统一设置，保证 Run() 一定收敛。
	applyCommandWaitDelay(cmd)

	// 追加而非替换：直接赋值 cmd.Env 会丢弃进程继承的 PATH/HOME 等变量，
	// 配置任意一个自定义变量就会破坏依赖继承环境的命令查找；同名变量以配置值为准
	if len(t.env) > 0 {
		cmd.Env = append(cmd.Environ(), t.env...)
	}

	err = cmd.Run()

	result := stdout.String()
	if stderr.Len() > 0 {
		if result != "" {
			result += "\n"
		}
		result += "stderr: " + stderr.String()
	}

	if t.persistCwd {
		result = t.consumeCwdMarker(result)
	}

	if r := []rune(result); len(r) > t.maxOutput {
		result = truncateMiddle(string(r), t.maxOutput)
	}

	// ErrWaitDelay：命令本体已结束/已被杀，只是孙进程仍占着输出管道被强制放弃。
	// 输出已收集完整，按成功返回，避免把「输出已拿到」误报为执行失败。
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}

	if ctx.Err() == context.DeadlineExceeded {
		return result + fmt.Sprintf("\n命令执行超时(%s)", t.timeout), nil
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// 非零退出码不是工具层错误：返回 error 会导致 stdout/stderr 被执行器
			// 丢弃、模型只看到"退出码 N"。把输出与退出码一并作为结果返回
			if result != "" {
				result += "\n"
			}
			if exitErr.ExitCode() == 127 {
				// 127 只表示"命令未找到"，具体是哪个命令缺失应以 stderr 为准（如 "sh: curl: not found"），
				// 不要臆断为某个特定解释器缺失
				result += "(命令退出码 127：命令未找到。请根据 stderr 确认缺失的命令或解释器，可用 `command -v <命令> || echo missing` 验证后重试)"
			} else {
				result += fmt.Sprintf("(命令退出码 %d)", exitErr.ExitCode())
			}
			return result, nil
		}
		return result, fmt.Errorf("bash: 执行失败: %w", err)
	}
	return result, nil
}

// buildPersistCwdCmd 构造带目录回传的命令：包装脚本从 start 目录出发执行原命令，
// 之后打印标记行回传最终目录，并以原命令的退出码退出。
//
// sh 系：多行脚本直接经 -c 传入。
// cmd 系：多行脚本经参数传递时逐行执行与 %VAR% 展开不可靠，写入临时 .cmd 文件
// 执行（/Q 关闭命令回显，避免包装脚本内容污染输出）；cleanup 删除临时文件。
func (t *BashTool) buildPersistCwdCmd(ctx context.Context, start, command string) (*exec.Cmd, func(), error) {
	if t.shellArg != "/C" {
		cmd := exec.CommandContext(ctx, t.shell, t.shellArg, t.wrapShCwdScript(start, command))
		return cmd, func() {}, nil
	}

	lines := []string{
		`cd /d "` + start + `" || exit /b 1`,
		command,
		`echo ` + bashCwdMarker + `%CD%`,
		`exit %ERRORLEVEL%`,
	}
	if start == "" {
		lines[0] = `rem 无起始目录，从当前目录开始`
	}
	f, err := os.CreateTemp("", "aniabot-bash-*.cmd")
	if err != nil {
		return nil, nil, err
	}
	if _, err := f.WriteString(strings.Join(lines, "\r\n") + "\r\n"); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, err
	}
	f.Close()
	cmd := exec.CommandContext(ctx, t.shell, "/Q", "/C", f.Name())
	return cmd, func() { os.Remove(f.Name()) }, nil
}

// wrapShCwdScript 构造 POSIX sh 系的目录回传包装脚本。
func (t *BashTool) wrapShCwdScript(start, command string) string {
	script := ""
	if start != "" {
		script += "cd '" + strings.ReplaceAll(start, "'", `'\''`) + "' || { echo " + bashCwdMarker + bashCwdFail + "; exit 1; }\n"
	}
	script += command + "\n"
	script += "__ania_ec=$?\n"
	script += `printf '%s\n' "` + bashCwdMarker + `$PWD"` + "\n"
	script += "exit $__ania_ec"
	return script
}

// consumeCwdMarker 从输出中剥离最后一行目录标记并更新持久化 cwd。
// 找不到标记（命令被信号杀死/包装未执行）或目录已不存在时不更新。
func (t *BashTool) consumeCwdMarker(result string) string {
	idx := strings.LastIndex(result, bashCwdMarker)
	if idx < 0 {
		return result
	}
	lineEnd := strings.IndexAny(result[idx:], "\r\n")
	markerLine := result[idx:]
	if lineEnd >= 0 {
		markerLine = result[idx : idx+lineEnd]
	}
	dir := strings.TrimPrefix(strings.TrimSpace(markerLine), bashCwdMarker)
	rest := strings.TrimRight(result[:idx]+result[idx+len(markerLine):], "\n")
	if dir == "" || dir == bashCwdFail {
		return rest
	}
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		t.cwdMu.Lock()
		t.cwd = dir
		t.cwdMu.Unlock()
	}
	return rest
}

// truncateMiddle 头尾保留式截断：多保留头部（错误通常在前面最先出现），
// 中段以省略标记替代并注明被隐藏的字符数。
func truncateMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	if tail <= 0 {
		return string(r[:head]) + fmt.Sprintf("\n…（尾部 %d 字符已截断）", len(r)-head)
	}
	hidden := len(r) - head - tail
	return string(r[:head]) + fmt.Sprintf("\n…（中间 %d 字符已截断）…\n", hidden) + string(r[len(r)-tail:])
}
