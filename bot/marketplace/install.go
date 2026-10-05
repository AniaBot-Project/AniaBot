package marketplace

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/bot/component/sysrestart"
	"github.com/jeanhua/AniaBot/bot/version"
	"github.com/jeanhua/AniaBot/common/pluginmeta"
)

// 流水线阶段
const (
	phIdle     = ""
	phEnv      = "env"      // 环境检查
	phFetch    = "fetch"    // 下载插件源码
	phVerify   = "verify"   // 校验元信息
	phCopy     = "copy"     // 写入插件目录
	phGenerate = "generate" // 生成注册代码
	phDeps     = "deps"     // 拉取依赖
	phBuild    = "build"    // 编译
	phSwap     = "swap"     // 替换二进制
	phRestart  = "restart"  // 等待重启
	phDone     = "done"     // 完成（即将重启）
)

const marketLogCap = 500 // 日志行数上限

// taskState 市场任务的内存状态（同一时间只允许一个任务）。
type taskState struct {
	mu         sync.Mutex
	running    bool
	restarting bool
	action     string // install / uninstall / rollback
	pluginID   string
	phase      string
	logs       []string
	err        string
	errKind    string
	buf        string // logWriter 的半行缓冲
}

func newTaskState() *taskState { return &taskState{} }

func (t *taskState) appendLog(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs = append(t.logs, line)
	if len(t.logs) > marketLogCap {
		t.logs = t.logs[len(t.logs)-marketLogCap:]
	}
}

func (t *taskState) setPhase(phase string) {
	t.mu.Lock()
	t.phase = phase
	t.mu.Unlock()
}

func (t *taskState) setTask(action, id string) {
	t.mu.Lock()
	t.action = action
	t.pluginID = id
	t.mu.Unlock()
}

func (t *taskState) fail(kind string, err error) {
	t.mu.Lock()
	t.running = false
	t.errKind = kind
	t.err = err.Error()
	t.mu.Unlock()
}

// tryBegin 尝试占用任务；已有任务运行或处于重启窗口时返回 false。
func (t *taskState) tryBegin() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running || t.restarting {
		return false
	}
	t.running = true
	t.phase = phEnv
	t.logs = nil
	t.err = ""
	t.errKind = ""
	t.buf = ""
	return true
}

func (t *taskState) finish() {
	t.mu.Lock()
	t.running = false
	t.phase = phDone
	t.restarting = true
	t.mu.Unlock()
}

func (t *taskState) clearRestarting() {
	t.mu.Lock()
	t.restarting = false
	t.mu.Unlock()
}

func (t *taskState) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	logs := make([]string, len(t.logs))
	copy(logs, t.logs)
	return map[string]any{
		"running":    t.running,
		"restarting": t.restarting,
		"action":     t.action,
		"plugin_id":  t.pluginID,
		"phase":      t.phase,
		"logs":       logs,
		"error":      t.err,
		"errKind":    t.errKind,
	}
}

// logWriter 将命令输出按行切分追加到任务日志。
type logWriter struct{ t *taskState }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.mu.Lock()
	w.t.buf += string(p)
	lines := strings.Split(w.t.buf, "\n")
	w.t.buf = lines[len(lines)-1]
	lines = lines[:len(lines)-1]
	w.t.mu.Unlock()
	for _, l := range lines {
		w.t.appendLog(strings.TrimRight(l, "\r"))
	}
	return len(p), nil
}

// stepCmd 执行流水线命令，输出实时写入任务日志。
func (s *Service) stepCmd(ctx context.Context, dir, name string, args ...string) error {
	s.state.appendLog("$ " + name + " " + strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	w := logWriter{t: s.state}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("`%s %s` 执行失败: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// preflight 环境检查：开关、运行模式、工具链、源码目录。
// 返回错误信息；ok=false 表示不可用。
func (s *Service) preflight(ctx context.Context) (string, bool) {
	if !s.Enabled() {
		return "插件市场未开启，请先在「配置管理」中设置 bot.marketplace.enable=true", false
	}
	if isDevRun() {
		return "当前为 go run 开发模式运行，插件市场不可用，请以编译后的二进制部署", false
	}
	// 与自动更新页一致的检测：git --version / go version；PATH 找不到时自动探测
	// 常见安装目录（/usr/local/go/bin、C:\Go\bin 等）并补进进程 PATH。
	for _, tool := range []struct {
		name string
		args []string
	}{
		{"git", []string{"--version"}},
		{"go", []string{"version"}},
	} {
		if err := ensureTool(ctx, tool.name, tool.args...); err != nil {
			s.logger.Warn("插件市场环境检查失败", "tool", tool.name, "path", os.Getenv("PATH"), "error", err)
			return err.Error(), false
		}
	}
	srcDir := s.sourceDir()
	if srcDir == "" {
		return "未配置源码目录（bot.marketplace.source_dir 或 bot.update.source_dir），请先在「配置管理」中设置", false
	}
	if _, err := os.Stat(filepath.Join(srcDir, "go.mod")); err != nil {
		return fmt.Sprintf("源码目录 %s 不是 AniaBot 仓库（缺少 go.mod），请先完成一次自动更新", srcDir), false
	}
	if _, err := os.Stat(filepath.Join(srcDir, "bot", "adminpanel", "dist", "index.html")); err != nil {
		return "源码目录缺少前端产物（bot/adminpanel/dist），请先完成一次自动更新", false
	}
	if _, err := os.Stat(filepath.Join(srcDir, "tools", "plugingen", "main.go")); err != nil {
		return "当前源码版本过旧，缺少 tools/plugingen，请先完成一次自动更新", false
	}
	return "", true
}

// ---------- 批量安装 / 卸载 ----------

// 操作类型
const (
	opInstall   = "install"
	opUninstall = "uninstall"
)

// maxBatchSize 单次批量操作允许的插件数量上限。
const maxBatchSize = 50

// operation 批量任务中的单个操作。
type operation struct {
	kind   string // opInstall / opUninstall
	id     string
	commit string // 仅 install：指定 commit（空 = 分支最新）
}

// Batch 批量安装/升级/卸载插件（可混合，异步）。
// 所有操作共用一次生成注册代码、拉取依赖、编译与重启，比逐个操作快得多。
func (s *Service) Batch(installIDs, uninstallIDs []string) error {
	if !s.Enabled() {
		return fmt.Errorf("插件市场未开启")
	}
	ops, err := s.buildOps(installIDs, uninstallIDs)
	if err != nil {
		return err
	}
	return s.start(ops)
}

// Install 开始安装/升级单个插件（异步）。
func (s *Service) Install(id, commit string) error {
	if !s.Enabled() {
		return fmt.Errorf("插件市场未开启")
	}
	ops, err := s.buildOps([]string{id}, nil)
	if err != nil {
		return err
	}
	ops[0].commit = commit
	return s.start(ops)
}

// Uninstall 开始卸载单个插件（异步）。
func (s *Service) Uninstall(id string) error {
	if !s.Enabled() {
		return fmt.Errorf("插件市场未开启")
	}
	ops, err := s.buildOps(nil, []string{id})
	if err != nil {
		return err
	}
	return s.start(ops)
}

// buildOps 校验并归一化批量请求：去空白、去重、数量上限、安装/卸载冲突，
// 以及卸载目标必须已安装。
func (s *Service) buildOps(installIDs, uninstallIDs []string) ([]operation, error) {
	installs, err := normalizeIDs(installIDs)
	if err != nil {
		return nil, err
	}
	uninstalls, err := normalizeIDs(uninstallIDs)
	if err != nil {
		return nil, err
	}
	if len(installs)+len(uninstalls) == 0 {
		return nil, fmt.Errorf("未选择任何插件")
	}
	if len(installs)+len(uninstalls) > maxBatchSize {
		return nil, fmt.Errorf("一次最多操作 %d 个插件", maxBatchSize)
	}
	installSet := make(map[string]struct{}, len(installs))
	for _, id := range installs {
		installSet[id] = struct{}{}
	}
	for _, id := range uninstalls {
		if _, dup := installSet[id]; dup {
			return nil, fmt.Errorf("插件 %s 同时出现在安装与卸载列表中", id)
		}
	}
	// 卸载目标必须是市场安装过的插件（安装/升级不校验市场索引，保持原行为）
	for _, id := range uninstalls {
		if _, ok := s.manifest().find(id); !ok {
			return nil, fmt.Errorf("插件 %s 未安装", id)
		}
	}
	ops := make([]operation, 0, len(installs)+len(uninstalls))
	for _, id := range installs {
		ops = append(ops, operation{kind: opInstall, id: id})
	}
	for _, id := range uninstalls {
		ops = append(ops, operation{kind: opUninstall, id: id})
	}
	return ops, nil
}

// normalizeIDs 去空白、去重并保持原顺序，同时校验 ID 合法性（防路径穿越）。
func normalizeIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if err := pluginmeta.ValidateID(id); err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// start 占用任务槽并异步执行批量操作。
func (s *Service) start(ops []operation) error {
	if !s.state.tryBegin() {
		return fmt.Errorf("已有插件任务正在进行中或正在重启")
	}
	ids := make([]string, 0, len(ops))
	installs, uninstalls := 0, 0
	for _, op := range ops {
		ids = append(ids, op.id)
		if op.kind == opInstall {
			installs++
		} else {
			uninstalls++
		}
	}
	action := "batch"
	if len(ops) == 1 {
		action = ops[0].kind
	}
	// 状态里的插件标识：过长时截断，避免面板一次塞入几十个 ID
	label := strings.Join(ids, ", ")
	if len(ids) > 6 {
		label = strings.Join(ids[:6], ", ") + fmt.Sprintf(" 等 %d 个", len(ids))
	}
	s.state.setTask(action, label)

	detail := fmt.Sprintf("批量操作 %d 个插件（安装/升级 %d、卸载 %d）: %s", len(ids), installs, uninstalls, label)
	if len(ops) == 1 && ops[0].kind == opInstall {
		detail = "安装插件 " + ops[0].id
	}
	if len(ops) == 1 && ops[0].kind == opUninstall {
		detail = "卸载插件 " + ops[0].id
	}
	oplog.Record(oplog.CategoryPlugin, "marketplace_"+action, detail)
	go s.runBatch(ops)
	return nil
}

// runBatch 执行批量安装/升级/卸载流水线：先下载校验并写入全部插件，
// 再共用一次生成注册代码、拉依赖、编译、替换二进制与重启。
func (s *Service) runBatch(ops []operation) {
	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout(len(ops)))
	defer cancel()

	var installs, uninstalls []operation
	for _, op := range ops {
		if op.kind == opInstall {
			installs = append(installs, op)
		} else {
			uninstalls = append(uninstalls, op)
		}
	}

	// 已下载校验、待写入的插件
	type staged struct {
		op       operation
		manifest *pluginmeta.Manifest
		commit   string
		dir      string
	}
	// 已写盘的安装记录（失败时按此回滚；backupDir 仅在 hadOld 时有效）
	type written struct {
		id           string
		persistDir   string
		srcPluginDir string
		backupDir    string
		hadOld       bool
	}
	// 已移除源码树的卸载记录（持久副本保留至编译成功，失败可还原）
	type removed struct {
		id           string
		persistDir   string
		srcPluginDir string
	}
	var stagedList []staged
	var writtenList []written
	var removedList []removed
	cleaned := false

	// rollback 失败时把持久目录/源码树恢复到操作前状态：
	// 升级回旧版本，新安装则删除，卸载从持久副本还原源码树，并重新生成注册代码。
	rollback := func() {
		if cleaned || (len(writtenList) == 0 && len(removedList) == 0) {
			return
		}
		cleaned = true
		for i := len(writtenList) - 1; i >= 0; i-- {
			r := writtenList[i]
			_ = os.RemoveAll(r.srcPluginDir)
			if r.hadOld {
				_ = os.RemoveAll(r.persistDir)
				if err := os.Rename(r.backupDir, r.persistDir); err != nil {
					s.logger.Warn("插件安装失败后恢复旧版本失败", "plugin", r.id, "error", err)
					continue
				}
				_ = replaceDir(r.persistDir, r.srcPluginDir)
			} else {
				_ = os.RemoveAll(r.persistDir)
			}
		}
		for i := len(removedList) - 1; i >= 0; i-- {
			r := removedList[i]
			_ = os.RemoveAll(r.srcPluginDir)
			if _, err := os.Stat(r.persistDir); err == nil {
				_ = replaceDir(r.persistDir, r.srcPluginDir)
			}
		}
		// 尽力把注册代码恢复为与目录一致的状态
		_ = s.stepCmd(ctx, s.sourceDir(), "go", "run", "./tools/plugingen")
	}

	fail := func(kind string, err error) {
		s.state.appendLog("✗ " + err.Error())
		rollback()
		s.state.fail(kind, err)
		s.logger.Warn("插件操作失败", "kind", kind, "error", err)
	}

	// 1. 环境检查
	s.state.setPhase(phEnv)
	s.state.appendLog("== 检查运行环境 ==")
	if msg, ok := s.preflight(ctx); !ok {
		fail("环境", fmt.Errorf("%s", msg))
		return
	}
	s.state.appendLog("  环境正常")
	if len(ops) > 1 {
		s.state.appendLog(fmt.Sprintf("== 批量操作：安装/升级 %d 个、卸载 %d 个 ==", len(installs), len(uninstalls)))
	}

	// 2. 统一下载全部待安装插件的源码（全部下载成功后才开始校验与写盘）
	if len(installs) > 0 {
		s.state.setPhase(phFetch)
		s.state.appendLog("== 下载插件源码 ==")
	}
	latest, latestFetched := "", false
	downloaded := make([]staged, 0, len(installs))
	for i, op := range installs {
		label := op.id
		if len(installs) > 1 {
			label = fmt.Sprintf("%s（%d/%d）", op.id, i+1, len(installs))
		}
		commit := op.commit
		if commit == "" {
			// 同一分支的多个插件复用一次 latestCommit 查询
			if !latestFetched {
				c, err := s.client().latestCommit(ctx, s.branch())
				if err != nil {
					fail("仓库", err)
					return
				}
				latest = c
				latestFetched = true
			}
			commit = latest
		}
		s.state.appendLog("  " + label + " @ " + commit)
		staging := filepath.Join(s.cacheDir(), "extract", fmt.Sprintf("%s-%d", op.id, time.Now().UnixNano()))
		if err := os.RemoveAll(staging); err != nil {
			fail("系统", err)
			return
		}
		if err := os.MkdirAll(staging, 0o755); err != nil {
			fail("系统", err)
			return
		}
		if err := s.client().downloadPlugin(ctx, commit, op.id, staging); err != nil {
			fail("仓库", err)
			return
		}
		downloaded = append(downloaded, staged{op: op, commit: commit, dir: staging})
	}

	// 3. 统一校验全部下载的插件元信息
	if len(downloaded) > 0 {
		s.state.setPhase(phVerify)
		s.state.appendLog("== 校验插件元信息 ==")
	}
	for _, st := range downloaded {
		m, err := pluginmeta.LoadManifest(filepath.Join(st.dir, "plugin.json"))
		if err != nil {
			fail("校验", err)
			return
		}
		if m.ID != st.op.id {
			fail("校验", fmt.Errorf("插件目录与 plugin.json 的 id 不一致: %q != %q", m.ID, st.op.id))
			return
		}
		s.state.appendLog(fmt.Sprintf("  %s v%s by %s", m.Name, m.Version, m.Author))
		st.manifest = m
		stagedList = append(stagedList, st)
	}

	// 4. 写入插件目录（持久副本 + 源码树副本；已存在的先备份旧版本供失败回滚）
	if len(stagedList) > 0 {
		s.state.setPhase(phCopy)
		s.state.appendLog("== 写入插件目录 ==")
	}
	for _, st := range stagedList {
		persistDir := filepath.Join(s.pluginDir(), st.op.id)
		backupDir := persistDir + ".old"
		hadOld := false
		if _, err := os.Stat(persistDir); err == nil {
			_ = os.RemoveAll(backupDir)
			if err := os.Rename(persistDir, backupDir); err != nil {
				fail("系统", fmt.Errorf("备份旧插件 %s 失败: %w", st.op.id, err))
				return
			}
			hadOld = true
		}
		rec := written{
			id: st.op.id, persistDir: persistDir, backupDir: backupDir, hadOld: hadOld,
			srcPluginDir: filepath.Join(s.sourceDir(), pluginmeta.PluginRoot, st.op.id),
		}
		writtenList = append(writtenList, rec)
		if err := replaceDir(st.dir, rec.persistDir); err != nil {
			fail("系统", fmt.Errorf("写入插件 %s 的持久目录失败: %w", st.op.id, err))
			return
		}
		if err := replaceDir(st.dir, rec.srcPluginDir); err != nil {
			fail("系统", fmt.Errorf("写入插件 %s 的源码树失败: %w", st.op.id, err))
			return
		}
		verb := "安装"
		if hadOld {
			verb = "升级"
		}
		s.state.appendLog(fmt.Sprintf("  %s %s v%s", verb, st.op.id, st.manifest.Version))
	}

	// 5. 只先移除待卸载插件的源码树副本（持久副本保留到编译成功后再删，失败可恢复）
	if len(uninstalls) > 0 {
		s.state.setPhase(phCopy)
		s.state.appendLog("== 移除插件源码 ==")
	}
	for _, op := range uninstalls {
		rec := removed{
			id:           op.id,
			persistDir:   filepath.Join(s.pluginDir(), op.id),
			srcPluginDir: filepath.Join(s.sourceDir(), pluginmeta.PluginRoot, op.id),
		}
		removedList = append(removedList, rec)
		if err := os.RemoveAll(rec.srcPluginDir); err != nil {
			fail("系统", fmt.Errorf("移除插件 %s 源码失败: %w", op.id, err))
			return
		}
		s.state.appendLog("  " + op.id)
	}

	// 6. 生成注册代码
	s.state.setPhase(phGenerate)
	s.state.appendLog("== 生成插件注册代码 ==")
	if err := s.stepCmd(ctx, s.sourceDir(), "go", "run", "./tools/plugingen"); err != nil {
		fail("生成", err)
		return
	}

	// 7. 拉取依赖
	s.state.setPhase(phDeps)
	s.state.appendLog("== 拉取 Go 依赖 ==")
	if err := s.stepCmd(ctx, s.sourceDir(), "go", "mod", "tidy"); err != nil {
		fail("依赖", fmt.Errorf("go mod tidy 失败: %w", err))
		return
	}

	// 8. 编译
	s.state.setPhase(phBuild)
	s.state.appendLog("== 编译 AniaBot ==")
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	builtPath := filepath.Join(s.sourceDir(), "build", "AniaBot.update"+ext)
	if err := os.MkdirAll(filepath.Dir(builtPath), 0o755); err != nil {
		fail("系统", err)
		return
	}
	// ldflags 透传当前运行版本号，重新编译不丢失 CI 注入的版本标识（见 version 包）
	if err := s.stepCmd(ctx, s.sourceDir(), "go", "build", "-ldflags", version.Ldflags("-s -w"), "-o", builtPath, "./cmd/"); err != nil {
		fail("编译", fmt.Errorf("go build 失败（插件与当前框架 API 不兼容时通常在此报错）: %w", err))
		return
	}

	// 9. 替换二进制
	s.state.setPhase(phSwap)
	s.state.appendLog("== 替换二进制 ==")
	if err := s.swapBinary(builtPath); err != nil {
		fail("系统", err)
		return
	}

	// 10. 记录安装清单、清理旧版本备份；卸载方先执行清理钩子再删除持久副本
	for _, st := range stagedList {
		if err := s.manifest().set(InstalledPlugin{
			ID: st.manifest.ID, Name: st.manifest.Name, Version: st.manifest.Version,
			Commit: st.commit, InstalledAt: nowStamp(),
		}); err != nil {
			s.logger.Warn("写入插件安装清单失败", "plugin", st.op.id, "error", err)
		}
		_ = os.RemoveAll(filepath.Join(s.pluginDir(), st.op.id) + ".old")
	}
	for _, op := range uninstalls {
		// 编译与二进制替换均已成功，重启前调用被卸载插件的清理钩子
		// （OnUnload，reason=uninstall），让运行中的实例在退出前清理自身数据；
		// 钩子失败只记日志并继续卸载（与编译失败不同，此处不再有可恢复的路径）。
		s.fireUnloadHook(ctx, op.id)
		if err := os.RemoveAll(filepath.Join(s.pluginDir(), op.id)); err != nil {
			s.logger.Warn("删除插件持久副本失败", "plugin", op.id, "error", err)
		}
		if err := s.manifest().remove(op.id); err != nil {
			s.logger.Warn("更新插件安装清单失败", "plugin", op.id, "error", err)
		}
	}
	s.finishAndRestart()
}

// batchTimeout 批量任务超时：基础 15 分钟，每个额外操作 +3 分钟，上限 30 分钟。
func batchTimeout(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := 15*time.Minute + time.Duration(n-1)*3*time.Minute
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
}

// fireUnloadHook 调用被卸载插件的卸载钩子（core 注入的 PluginUnloader）。
// 未注入或插件未实现钩子时跳过；钩子失败只记日志，不阻断卸载流程。
func (s *Service) fireUnloadHook(ctx context.Context, id string) {
	if s.unloader == nil {
		return
	}
	called, err := s.unloader.UnloadPlugin(ctx, id)
	if err != nil {
		s.state.appendLog("! 插件卸载钩子执行失败（继续卸载）: " + err.Error())
		s.logger.Warn("插件卸载钩子执行失败", "plugin", id, "error", err)
		return
	}
	if called {
		s.state.appendLog("== 已执行插件卸载钩子（数据清理） ==")
	}
}

// ---------- 回滚 ----------

// Rollback 开始回滚（异步）：恢复上次替换前的二进制与插件清单。
func (s *Service) Rollback() error {
	if !s.Enabled() {
		return fmt.Errorf("插件市场未开启")
	}
	if !s.state.tryBegin() {
		return fmt.Errorf("已有插件任务正在进行中或正在重启")
	}
	s.state.setTask("rollback", "")
	oplog.Record(oplog.CategoryPlugin, "marketplace_rollback", "面板回滚插件安装")
	go s.runRollback()
	return nil
}

func (s *Service) runRollback() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	fail := func(kind string, err error) {
		s.state.appendLog("✗ " + err.Error())
		s.state.fail(kind, err)
		s.logger.Warn("插件回滚失败", "kind", kind, "error", err)
	}

	s.state.setPhase(phEnv)
	s.state.appendLog("== 检查运行环境 ==")
	if msg, ok := s.preflight(ctx); !ok {
		fail("环境", fmt.Errorf("%s", msg))
		return
	}

	s.state.setPhase(phSwap)
	s.state.appendLog("== 恢复旧二进制 ==")
	exe := sysrestart.Exe()
	if exe == "" {
		fail("系统", fmt.Errorf("无法获取当前可执行文件路径"))
		return
	}
	backup := exe + ".old"
	if _, err := os.Stat(backup); err != nil {
		fail("系统", fmt.Errorf("没有可回滚的旧二进制（%s 不存在）", backup))
		return
	}
	if err := s.swapBinary(backup); err != nil {
		fail("系统", err)
		return
	}
	if err := s.manifest().restoreBackup(); err != nil {
		s.logger.Warn("恢复插件安装清单失败", "error", err)
	}
	s.finishAndRestart()
}

// ---------- 通用 ----------

// swapBinary 把 builtPath 交换为当前运行二进制（沿用自动更新的改名交换，
// 兼容 Windows 不允许覆盖运行中 exe 的限制），失败时回滚。
func (s *Service) swapBinary(builtPath string) error {
	exe := sysrestart.Exe()
	if exe == "" {
		return fmt.Errorf("无法获取当前可执行文件路径")
	}
	tmpNew := exe + ".new"
	backup := exe + ".old"
	if err := copyFile(builtPath, tmpNew); err != nil {
		return fmt.Errorf("拷贝新二进制失败: %w", err)
	}
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		_ = os.Remove(tmpNew)
		return fmt.Errorf("备份当前二进制失败（文件可能被占用）: %w", err)
	}
	if err := os.Rename(tmpNew, exe); err != nil {
		_ = os.Rename(backup, exe) // 回滚
		return fmt.Errorf("替换二进制失败，已回滚: %w", err)
	}
	s.state.appendLog("  已替换，旧版本备份为 " + filepath.Base(backup))
	return nil
}

// finishAndRestart 标记任务完成并延迟重启。
func (s *Service) finishAndRestart() {
	s.state.finish()
	s.state.appendLog("== 操作完成，正在重启 ==")
	s.logger.Info("插件市场操作完成，正在重启 AniaBot")
	go func() {
		time.Sleep(1500 * time.Millisecond)
		sysrestart.Self(s.logger)
		// Self 正常时会替换/退出当前进程；只有重启失败才会回到这里
		s.state.clearRestarting()
	}()
}

// replaceDir 用 src 整体替换 dst（先删后拷，目录内容必须完全一致）。
func replaceDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyDir(src, dst)
}

// copyDir 递归复制目录，拒绝符号链接（防止插件通过软链逃逸目录）。
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("插件目录不允许包含符号链接: %s", rel)
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return copyFile(path, out)
	})
}

// copyFile 复制文件。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
