// 自动更新：从配置的 git 源码目录拉取最新代码，拉依赖、构建前端、编译 Go，
// 以「改名交换」方式替换运行中的二进制（Windows 不允许覆盖运行中的 exe，
// 但允许重命名，故先编译为 AniaBot.update，再拷贝为 <exe>.new 做 rename 交换，
// 旧二进制保留为 <exe>.old 以便手动回滚），最后复用 sysrestart.Self 重启进程。
//
// 任一阶段失败都会中止更新，并记录错误分类（环境/仓库/依赖/前端/编译/系统），
// 前端轮询 GET /api/update/status 展示实时日志与失败原因。
package adminpanel

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/bot/component/sysrestart"
	"github.com/jeanhua/AniaBot/bot/marketplace"
	"github.com/jeanhua/AniaBot/bot/version"
)

// 更新流水线阶段
const (
	upPhaseIdle    = ""
	upPhaseEnv     = "env"     // 环境检查
	upPhaseFetch   = "fetch"   // 拉取代码
	upPhaseDeps    = "deps"    // 拉取 Go 依赖
	upPhaseWeb     = "web"     // 构建前端
	upPhaseBuild   = "build"   // 编译 Go
	upPhaseSwap    = "swap"    // 替换二进制
	upPhaseRestart = "restart" // 等待重启
	upPhaseDone    = "done"    // 完成（即将重启）
)

// updateState 更新任务的内存状态（同一时间只允许一个任务）。
type updateState struct {
	mu         sync.Mutex
	running    bool
	restarting bool
	phase      string
	logs       []string
	err        string
	errKind    string
	buf        string // logWriter 的半行缓冲
}

var upd = &updateState{}

const updateLogCap = 500 // 日志行数上限，超出丢弃最旧行

func (u *updateState) appendLog(line string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.logs = append(u.logs, line)
	if len(u.logs) > updateLogCap {
		u.logs = u.logs[len(u.logs)-updateLogCap:]
	}
}

func (u *updateState) setPhase(phase string) {
	u.mu.Lock()
	u.phase = phase
	u.mu.Unlock()
}

func (u *updateState) fail(kind string, err error) {
	u.mu.Lock()
	u.running = false
	u.errKind = kind
	u.err = err.Error()
	u.mu.Unlock()
}

// tryBegin 尝试占用更新任务；已有更新在运行，或已完成但仍在重启窗口内时返回 false。
func (u *updateState) tryBegin() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running || u.restarting {
		return false
	}
	u.running = true
	u.phase = upPhaseEnv
	u.logs = nil
	u.err = ""
	u.errKind = ""
	u.buf = ""
	return true
}

func (u *updateState) finish() {
	u.mu.Lock()
	u.running = false
	u.phase = upPhaseDone
	u.restarting = true
	u.mu.Unlock()
}

func (u *updateState) clearRestarting() {
	u.mu.Lock()
	u.restarting = false
	u.mu.Unlock()
}

func (u *updateState) snapshot() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	logs := make([]string, len(u.logs))
	copy(logs, u.logs)
	return map[string]any{
		"running":    u.running,
		"restarting": u.restarting,
		"phase":      u.phase,
		"logs":       logs,
		"error":      u.err,
		"errKind":    u.errKind,
	}
}

// logWriter 将命令输出按行切分追加到更新日志（io.Writer）。
type logWriter struct{ prefix string }

func (w logWriter) Write(p []byte) (int, error) {
	upd.mu.Lock()
	upd.buf += string(p)
	lines := strings.Split(upd.buf, "\n")
	upd.buf = lines[len(lines)-1]
	lines = lines[:len(lines)-1]
	upd.mu.Unlock()
	for _, l := range lines {
		upd.appendLog(w.prefix + strings.TrimRight(l, "\r"))
	}
	return len(p), nil
}

// isDevRun 检测是否为 go run 开发模式（可执行文件在临时编译目录中），
// 开发模式下禁用自动更新。
func isDevRun() bool {
	exe := sysrestart.Exe()
	if exe == "" {
		return false
	}
	tmp := os.TempDir()
	if rel, err := filepath.Rel(tmp, exe); err == nil && !strings.HasPrefix(rel, "..") {
		return true
	}
	return strings.Contains(exe, "go-build")
}

// cfgStr 从配置中心读取字符串配置键。
func (s *Server) cfgStr(key string) string {
	if s.opt.Config == nil {
		return ""
	}
	v, ok := s.opt.Config.Get(key)
	if !ok {
		return ""
	}
	str, _ := v.(string)
	return strings.TrimSpace(str)
}

// toolVersion 检测工具是否可用并返回版本输出首行，不可用返回空串。
func toolVersion(ctx context.Context, name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, name, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(first)
}

// handleUpdateInfo 返回更新页所需的运行模式、环境与版本信息。
func (s *Server) handleUpdateInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	mode := "binary"
	if isDevRun() {
		mode = "dev"
	}
	exe := sysrestart.Exe()
	srcDir := s.cfgStr("bot.update.source_dir")
	branch := s.cfgStr("bot.update.branch")
	if branch == "" {
		branch = "main"
	}
	gitURL := s.cfgStr("bot.update.git_url")

	info := map[string]any{
		"mode":       mode,
		"exe":        exe,
		"configured": srcDir != "",
		"sourceDir":  srcDir,
		"branch":     branch,
		"gitUrl":     gitURL,
		"env": map[string]string{
			"git":  toolVersion(ctx, "git", "--version"),
			"go":   toolVersion(ctx, "go", "version"),
			"node": toolVersion(ctx, "node", "--version"),
			"npm":  toolVersion(ctx, "npm", "--version"),
		},
	}

	// 已配置源码目录时，按目录状态分别处理：
	// 是 git 仓库 → 对比本地与远端 commit；空目录/不存在 → 标记待克隆，
	// 直接用 git ls-remote 检查远端；非空非仓库 → 报目录错误。
	if srcDir != "" {
		if isGitRepoDir(ctx, srcDir) {
			// 用完整哈希做比较：git rev-parse --short 的长度随仓库对象数增长
			// （可能是 8 位以上），与固定截断 7 位的远端哈希永不相等，
			// 会导致面板恒久误报"有新版本"。短哈希仅用于展示
			var localFull string
			if out, err := runGit(ctx, srcDir, "rev-parse", "HEAD"); err == nil {
				localFull = strings.TrimSpace(out)
				info["currentCommit"] = shortCommit(localFull)
			}
			if out, err := runGit(ctx, srcDir, "ls-remote", "origin", branch); err == nil {
				fields := strings.Fields(out)
				if len(fields) > 0 && len(fields[0]) >= 7 {
					remoteFull := fields[0]
					info["remoteCommit"] = shortCommit(remoteFull)
					info["updateAvailable"] = localFull != "" && localFull != remoteFull
				}
			} else {
				info["remoteError"] = "无法访问远端仓库（网络或认证问题）"
			}
		} else if empty, _ := dirIsEmpty(srcDir); empty {
			info["needClone"] = true
			if gitURL == "" {
				info["remoteError"] = "源码目录为空且未配置 git 地址（bot.update.git_url），无法克隆"
			} else if out, err := gitLsRemoteURL(ctx, gitURL, branch); err == nil {
				fields := strings.Fields(out)
				if len(fields) > 0 && len(fields[0]) >= 7 {
					info["remoteCommit"] = shortCommit(fields[0])
				}
			} else {
				info["remoteError"] = "无法访问远端仓库（网络或认证问题）"
			}
		} else {
			info["dirError"] = "源码目录已存在且非空，但不是 git 仓库（请更换目录或清空，空目录将自动克隆）"
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// isGitRepoDir 判断目录是否为 git 仓库。
// shortCommit 返回用于展示的 7 位短哈希（比较一律用完整哈希）。
func shortCommit(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

func isGitRepoDir(ctx context.Context, dir string) bool {
	_, err := runGit(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// dirIsEmpty 报告目录不存在或为空目录。
func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// gitLsRemoteURL 不依赖本地仓库，直接查询远端地址的分支引用。
func gitLsRemoteURL(ctx context.Context, url, branch string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", url, branch)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runGit 在指定目录执行 git 命令并返回输出（不经过更新日志）。
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// handleUpdateStatus 返回当前更新任务状态快照（前端轮询）。
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, upd.snapshot())
}

// handleUpdateStart 启动更新流水线（异步），立即返回。
func (s *Server) handleUpdateStart(w http.ResponseWriter, r *http.Request) {
	if isDevRun() {
		writeError(w, http.StatusBadRequest, "当前为 go run 开发模式运行，不支持自动更新")
		return
	}
	srcDir := s.cfgStr("bot.update.source_dir")
	if srcDir == "" {
		writeError(w, http.StatusBadRequest, "未配置源码目录（bot.update.source_dir），请先在「配置管理」中设置")
		return
	}
	branch := s.cfgStr("bot.update.branch")
	if branch == "" {
		branch = "main"
	}
	gitURL := s.cfgStr("bot.update.git_url")

	if !upd.tryBegin() {
		writeError(w, http.StatusConflict, "已有更新任务正在进行中或正在重启")
		return
	}

	oplog.Record(oplog.CategoryUpdate, "update_start", "面板启动自动更新（分支: "+branch+"），IP: "+clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go s.runUpdate(srcDir, gitURL, branch)
}

// stepCmd 执行流水线中的一条命令：记录命令行，输出实时写入更新日志。
func stepCmd(ctx context.Context, dir, name string, args ...string) error {
	upd.appendLog("$ " + name + " " + strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	w := logWriter{}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("`%s %s` 执行失败: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// runUpdate 更新流水线：环境检查 → 拉取代码 → 拉依赖 → 构建前端 → 编译 → 替换 → 重启。
// 任一阶段失败即中止并记录错误分类。
func (s *Server) runUpdate(srcDir, gitURL, branch string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	fail := func(kind string, err error) {
		upd.appendLog("✗ " + err.Error())
		upd.fail(kind, err)
		s.opt.Logger.Warn("自动更新失败", "kind", kind, "error", err)
	}

	// 1. 环境检查
	upd.setPhase(upPhaseEnv)
	upd.appendLog("== 检查运行环境 ==")
	for _, tool := range []struct {
		name string
		args []string
	}{
		{"git", []string{"--version"}},
		{"go", []string{"version"}},
		{"node", []string{"--version"}},
		{"npm", []string{"--version"}},
	} {
		ver := toolVersion(ctx, tool.name, tool.args...)
		if ver == "" {
			fail("环境缺失", fmt.Errorf("未找到 %s，请在部署机器上安装并加入 PATH", tool.name))
			return
		}
		upd.appendLog("  " + ver)
	}
	// 源码目录为空/不存在 → 自动克隆；非空但不是 git 仓库 → 报错中止
	if empty, _ := dirIsEmpty(srcDir); empty {
		if gitURL == "" {
			fail("环境缺失", fmt.Errorf("源码目录 %s 为空且未配置 git 地址（bot.update.git_url），无法克隆", srcDir))
			return
		}
		upd.appendLog("  源码目录为空，自动克隆仓库")
		parent := filepath.Dir(srcDir)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			fail("系统错误", fmt.Errorf("创建源码目录失败: %w", err))
			return
		}
		if err := stepCmd(ctx, parent, "git", "clone", "--branch", branch, gitURL, srcDir); err != nil {
			fail("仓库错误", fmt.Errorf("git clone 失败（检查 git 地址、网络与认证）: %w", err))
			return
		}
	} else if !isGitRepoDir(ctx, srcDir) {
		fail("环境缺失", fmt.Errorf("源码目录 %s 非空但不是 git 仓库，请更换目录或清空后重试（空目录将自动克隆）", srcDir))
		return
	}

	// 2. 拉取代码
	upd.setPhase(upPhaseFetch)
	upd.appendLog("== 拉取最新代码 ==")
	if gitURL != "" {
		if err := stepCmd(ctx, srcDir, "git", "remote", "set-url", "origin", gitURL); err != nil {
			fail("仓库错误", err)
			return
		}
	}
	if err := stepCmd(ctx, srcDir, "git", "fetch", "origin", branch); err != nil {
		fail("仓库错误", fmt.Errorf("拉取远端失败（检查网络、git 地址与认证）: %w", err))
		return
	}
	if err := stepCmd(ctx, srcDir, "git", "reset", "--hard", "origin/"+branch); err != nil {
		fail("仓库错误", err)
		return
	}

	// 2.5 重放已安装的市场插件（git reset 会清掉源码树中的 custom/plugins
	// 与生成的注册文件，这里从持久插件目录恢复，保证更新后插件不丢失）
	upd.setPhase(upPhaseDeps)
	upd.appendLog("== 重放已安装插件 ==")
	pluginDir := marketplace.DefaultPluginDir
	if err := marketplace.ApplyInstalled(ctx, srcDir, pluginDir, s.opt.Logger); err != nil {
		fail("插件错误", fmt.Errorf("重放已安装插件失败: %w", err))
		return
	}
	// 3. 拉取 Go 依赖（新代码可能新增了依赖）
	upd.setPhase(upPhaseDeps)
	upd.appendLog("== 拉取 Go 依赖 ==")
	if err := stepCmd(ctx, srcDir, "go", "mod", "tidy"); err != nil {
		fail("依赖错误", fmt.Errorf("go mod tidy 失败（检查网络代理或依赖源）: %w", err))
		return
	}

	// 4. 构建前端（go:embed 需要最新的 dist 产物）
	upd.setPhase(upPhaseWeb)
	upd.appendLog("== 构建前端 ==")
	webDir := filepath.Join(srcDir, "web")
	if err := stepCmd(ctx, webDir, "npm", "ci"); err != nil {
		fail("前端构建错误", fmt.Errorf("npm ci 失败: %w", err))
		return
	}
	if err := stepCmd(ctx, webDir, "npm", "run", "build"); err != nil {
		fail("前端构建错误", fmt.Errorf("npm run build 失败: %w", err))
		return
	}

	// 5. 编译 Go（输出名固定为 AniaBot.update，避免与运行中的二进制冲突）
	upd.setPhase(upPhaseBuild)
	upd.appendLog("== 编译 AniaBot ==")
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	builtPath := filepath.Join(srcDir, "build", "AniaBot.update"+ext)
	if err := os.MkdirAll(filepath.Dir(builtPath), 0o755); err != nil {
		fail("系统错误", fmt.Errorf("创建构建输出目录失败: %w", err))
		return
	}
	// ldflags 透传当前运行版本号：源码默认值是 dev，若不注入，自动更新后的
	// 二进制会丢失 CI 注入的版本标识（LLM 请求 UA 无法溯源）
	if err := stepCmd(ctx, srcDir, "go", "build", "-ldflags", version.Ldflags("-s -w"), "-o", builtPath, "./cmd/"); err != nil {
		fail("编译错误", fmt.Errorf("go build 失败: %w", err))
		return
	}
	if _, err := os.Stat(builtPath); err != nil {
		fail("编译错误", fmt.Errorf("编译产物不存在: %w", err))
		return
	}

	// 6. 替换二进制：拷贝为 <exe>.new → 重命名当前 exe 为 <exe>.old → 重命名 .new 为原名
	// 注意必须使用启动时缓存的 selfExe：Linux 下此时再取 os.Executable()
	// 会读到已被 rename 的旧二进制路径（/proc/self/exe 跟随 inode）。
	upd.setPhase(upPhaseSwap)
	upd.appendLog("== 替换二进制 ==")
	exe := sysrestart.Exe()
	if exe == "" {
		fail("系统错误", fmt.Errorf("无法获取当前可执行文件路径"))
		return
	}
	tmpNew := exe + ".new"
	backup := exe + ".old"
	if err := copyFile(builtPath, tmpNew); err != nil {
		fail("系统错误", fmt.Errorf("拷贝新二进制失败: %w", err))
		return
	}
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		_ = os.Remove(tmpNew)
		fail("系统错误", fmt.Errorf("备份当前二进制失败（文件可能被占用）: %w", err))
		return
	}
	if err := os.Rename(tmpNew, exe); err != nil {
		// 回滚：恢复原二进制
		_ = os.Rename(backup, exe)
		fail("系统错误", fmt.Errorf("替换二进制失败，已回滚: %w", err))
		return
	}
	upd.appendLog("  已替换，旧版本备份为 " + filepath.Base(backup))

	// 7. 完成，延迟后重启（先让前端拿到 done 状态）
	upd.finish()
	upd.appendLog("== 更新完成，正在重启 ==")
	s.opt.Logger.Info("自动更新完成，正在重启 AniaBot")
	go func() {
		time.Sleep(1500 * time.Millisecond)
		sysrestart.Self(s.opt.Logger)
		// Self 正常时会替换/退出当前进程；只有重启失败才会回到这里，
		// 此时清掉重启标记，允许手动重试下一次更新。
		upd.clearRestarting()
	}()
}

// copyFile 复制文件并保留可执行权限。
func copyFile(src, dst string) (err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}
