// Package marketplace 实现插件市场：从 GitHub 插件仓库拉取插件列表/详情，
// 在本地源码树中安装/卸载/升级第三方插件（custom/plugins），重新编译并重启 Bot。
//
// 安全模型：安装插件 = 在 Bot 所在机器上编译并执行插件代码（与 Bot 同进程）。
// 功能默认关闭（bot.marketplace.enable=false），面板安装前会再次提示风险。
// 插件来源是独立仓库（默认 AniaBot-Project/AniaBot-Plugins），由维护者人工审查后合并。
package marketplace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/sysrestart"
	"github.com/jeanhua/AniaBot/common/pluginmeta"
)

// Config 市场依赖的配置中心能力（configstore.Store 满足）。
type Config interface {
	Get(key string) (any, bool)
	Set(key string, val any) error
}

// PluginUnloader 卸载插件运行实例的清理能力（core.AniaBot 实现）。
// 卸载流水线在二进制替换成功、重启之前调用，让插件有机会清理自身数据；
// 返回 called=false 表示插件未实现卸载钩子（无需清理）。
type PluginUnloader interface {
	UnloadPlugin(ctx context.Context, id string) (called bool, err error)
}

// Option 可选配置项。
type Option func(*Service)

// WithUnloader 注入插件卸载清理能力（插件市场由 core 创建时传入）。
func WithUnloader(u PluginUnloader) Option {
	return func(s *Service) { s.unloader = u }
}

// Service 插件市场服务。
type Service struct {
	cfg      Config
	logger   *slog.Logger
	state    *taskState
	oauth    *oauthFlow
	unloader PluginUnloader
	mu       sync.Mutex
	gh       *githubClient // 按最新配置懒重建
	man      *manifestStore
}

// New 创建插件市场服务。
func New(cfg Config, logger *slog.Logger, opts ...Option) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		cfg:    cfg,
		logger: logger,
		state:  newTaskState(),
		oauth:  newOAuthFlow(),
		man:    nil, // 首次使用时按 pluginDir 创建
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ---------- 配置读取 ----------

func (s *Service) cfgStr(key string) string {
	if s.cfg == nil {
		return ""
	}
	v, ok := s.cfg.Get(key)
	if !ok {
		return ""
	}
	str, _ := v.(string)
	return strings.TrimSpace(str)
}

func (s *Service) cfgBool(key string) bool {
	v, ok := s.cfg.Get(key)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Enabled 插件市场功能是否开启。
func (s *Service) Enabled() bool { return s.cfgBool("bot.marketplace.enable") }

func (s *Service) repo() string {
	if r := s.cfgStr("bot.marketplace.repo"); r != "" {
		return r
	}
	return defaultRepo
}

func (s *Service) branch() string {
	if b := s.cfgStr("bot.marketplace.branch"); b != "" {
		return b
	}
	return defaultBranch
}

func (s *Service) token() string { return s.cfgStr("bot.marketplace.token") }

// sourceDir 编译用源码目录：优先市场专属配置，回退到自动更新目录。
func (s *Service) sourceDir() string {
	if d := s.cfgStr("bot.marketplace.source_dir"); d != "" {
		return d
	}
	return s.cfgStr("bot.update.source_dir")
}

func (s *Service) pluginDir() string {
	if d := s.cfgStr("bot.marketplace.plugin_dir"); d != "" {
		return d
	}
	return "./data/plugins"
}

func (s *Service) cacheDir() string {
	if d := s.cfgStr("bot.marketplace.cache_dir"); d != "" {
		return d
	}
	return "./data/marketplace"
}

func (s *Service) manifest() *manifestStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.man == nil {
		s.man = newManifestStore(s.pluginDir())
	}
	return s.man
}

func (s *Service) client() *githubClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gh == nil || s.gh.owner != parseOwner(s.repo()) || s.gh.repo != parseName(s.repo()) || s.gh.token != s.token() {
		s.gh = newGitHubClient(s.repo(), s.token())
	}
	return s.gh
}

func parseOwner(repo string) string {
	o, _ := parseRepo(repo)
	return o
}

func parseName(repo string) string {
	_, n := parseRepo(repo)
	return n
}

// ---------- 开发模式检测 ----------

// isDevRun 检测是否为 go run 开发模式（可执行文件在临时编译目录中）。
func isDevRun() bool {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return false
	}
	tmp := os.TempDir()
	if rel, err := filepath.Rel(tmp, exe); err == nil && !strings.HasPrefix(rel, "..") {
		return true
	}
	return strings.Contains(exe, "go-build")
}

// toolDirs 常见工具安装目录兜底：进程 PATH 未包含时依次探测，命中后补进进程 PATH。
var toolDirs = map[string][]string{
	"go": {
		"/usr/local/go/bin",
		"/usr/lib/go/bin",
		"/opt/go/bin",
		"C:\\Go\\bin",
		filepath.Join(os.Getenv("ProgramFiles"), "Go", "bin"),
		filepath.Join(os.Getenv("LocalAppData"), "Programs", "Go", "bin"),
	},
	"git": {
		"/usr/bin",
		"/usr/local/bin",
		"C:\\Program Files\\Git\\cmd",
		filepath.Join(os.Getenv("LocalAppData"), "Programs", "Git", "cmd"),
	},
}

// ensureTool 确认工具可用：优先按 PATH 查找；未命中时探测常见安装目录并把目录
// 补进进程 PATH（后续 stepCmd 的 exec 也能找到），仍失败则返回带 PATH 的错误。
func ensureTool(ctx context.Context, name string, args ...string) error {
	if _, err := exec.LookPath(name); err == nil {
		if _, verr := toolVersion(ctx, name, args...); verr != nil {
			return fmt.Errorf("%s 已安装但执行失败: %w", name, verr)
		}
		return nil
	}
	for _, dir := range toolDirs[name] {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if _, verr := toolVersion(ctx, name, args...); verr != nil {
			return fmt.Errorf("%s 位于 %s 但执行失败: %w", name, candidate, verr)
		}
		return nil
	}
	return fmt.Errorf("未找到 %s（进程 PATH=%q，且常见安装目录 %v 均未命中）", name, os.Getenv("PATH"), toolDirs[name])
}

// toolVersion 执行工具版本命令，返回首行版本；失败返回错误。
func toolVersion(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(first), nil
}

// ---------- 对外能力 ----------

// Info 返回面板「插件市场」页所需的环境与配置信息。
func (s *Service) Info() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env := map[string]string{}
	// 注意：go 的子命令是 `go version`（无 `--version`），与安装/自动更新路径保持一致，
	// 否则 `go --version` 会以错误退出，导致面板误显示「Go 未安装」。
	for _, t := range []struct {
		name string
		args []string
	}{
		{"git", []string{"--version"}},
		{"go", []string{"version"}},
	} {
		v, _ := toolVersion(ctx, t.name, t.args...)
		env[t.name] = v
	}
	srcDir := s.sourceDir()
	configured := srcDir != "" && s.repo() != ""
	rate := -1
	rollbackAvailable := false
	if exe := sysrestart.Exe(); exe != "" {
		if _, err := os.Stat(exe + ".old"); err == nil {
			rollbackAvailable = true
		}
	}
	tokenSet := s.token() != ""
	tokenValid := false
	oauthUser := s.oauthUser()
	if tokenSet {
		// 实际调用一次 /user 校验 Token 是否仍有效，避免显示过期的「已登录」；
		// 网络异常无法确认时保守按有效显示，避免误报「登录已失效」
		if c := s.client(); c != nil {
			user, ok, invalid := c.verifyToken(ctx)
			switch {
			case ok:
				tokenValid = true
				if user != "" && user != oauthUser {
					oauthUser = user
					if s.cfg != nil {
						_ = s.cfg.Set("bot.marketplace.oauth_user", user)
					}
				}
			case invalid:
				tokenValid = false
			default:
				tokenValid = true
			}
			rate = c.rateRemaining
		}
	}
	return map[string]any{
		"enabled":            s.Enabled(),
		"mode":               map[bool]string{true: "dev", false: "binary"}[isDevRun()],
		"configured":         configured,
		"repo":               s.repo(),
		"branch":             s.branch(),
		"token_set":          tokenSet,
		"token_valid":        tokenValid,
		"rate_remaining":     rate,
		"index_synced_at":    s.indexSyncedAt(),
		"rollback_available": rollbackAvailable,
		"source_dir":         srcDir,
		"plugin_dir":         s.pluginDir(),
		"cache_dir":          s.cacheDir(),
		"env":                env,
		"installed":          len(s.manifest().all()),
		"oauth_configured":   s.oauthConfigured(),
		"oauth_user":         oauthUser,
	}
}

// indexSyncedAt 返回市场索引本地缓存的最后同步时间（Unix 秒，0 表示尚未拉取过）。
func (s *Service) indexSyncedAt() int64 {
	if st, err := os.Stat(s.indexCachePath()); err == nil {
		return st.ModTime().Unix()
	}
	return 0
}

// SaveToken 保存/清除 GitHub Token（登录功能），立即生效。
func (s *Service) SaveToken(token string) error {
	if s.cfg == nil {
		return fmt.Errorf("配置中心不可用")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return s.cfg.Set("bot.marketplace.token", "")
	}
	return s.cfg.Set("bot.marketplace.token", token)
}

// ---------- 列表与详情 ----------

// PluginDTO 市场插件条目（叠加本地安装状态）。
type PluginDTO struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Author           string   `json:"author"`
	Version          string   `json:"version"`
	Platforms        []string `json:"platforms"`
	Tags             []string `json:"tags"`
	MinFramework     string   `json:"min_framework"`
	Installed        bool     `json:"installed"`
	InstalledVersion string   `json:"installed_version,omitempty"`
	InstalledCommit  string   `json:"installed_commit,omitempty"`
	UpdateAvailable  bool     `json:"update_available"`
}

// List 返回市场插件列表。refresh=true 时强制重新拉取索引；否则优先用本地缓存。
func (s *Service) List(ctx context.Context, refresh bool) ([]PluginDTO, error) {
	idx, err := s.loadIndex(ctx, refresh)
	if err != nil {
		return nil, err
	}
	installed := s.manifest().all()
	byID := map[string]InstalledPlugin{}
	for _, p := range installed {
		byID[p.ID] = p
	}
	out := make([]PluginDTO, 0, len(idx.Plugins))
	for _, m := range idx.Plugins {
		dto := PluginDTO{
			ID: m.ID, Name: m.Name, Description: m.Description,
			Author: m.Author, Version: m.Version,
			Platforms: m.Platforms, Tags: m.Tags, MinFramework: m.MinFramework,
		}
		if ip, ok := byID[m.ID]; ok {
			dto.Installed = true
			dto.InstalledVersion = ip.Version
			dto.InstalledCommit = ip.Commit
			dto.UpdateAvailable = ip.Version != m.Version
		}
		out = append(out, dto)
	}
	return out, nil
}

// DetailDTO 插件详情（元信息 + README + 安装状态）。
type DetailDTO struct {
	Manifest         pluginmeta.Manifest `json:"manifest"`
	Readme           string              `json:"readme"`
	ReadmeError      string              `json:"readme_error,omitempty"`
	Installed        bool                `json:"installed"`
	InstalledVersion string              `json:"installed_version,omitempty"`
	InstalledCommit  string              `json:"installed_commit,omitempty"`
}

// Detail 返回单个插件详情。
func (s *Service) Detail(ctx context.Context, id string) (*DetailDTO, error) {
	idx, err := s.loadIndex(ctx, false)
	if err != nil {
		return nil, err
	}
	var m *pluginmeta.Manifest
	for i := range idx.Plugins {
		if idx.Plugins[i].ID == id {
			m = &idx.Plugins[i]
			break
		}
	}
	if m == nil {
		return nil, fmt.Errorf("插件 %s 不存在", id)
	}
	dto := &DetailDTO{Manifest: *m}
	if ip, ok := s.manifest().find(id); ok {
		dto.Installed = true
		dto.InstalledVersion = ip.Version
		dto.InstalledCommit = ip.Commit
	}
	// README 有 10 分钟短期缓存：反复点开同一插件详情直接命中，不再每次请求 GitHub
	if content, ok := s.loadCachedReadme(id); ok {
		dto.Readme = content
		return dto, nil
	}
	readme, err := s.client().fetchReadme(ctx, id, m.ReadmeName(), s.branch())
	if err != nil {
		// README 拉取失败不阻塞详情：把真实原因带回面板，避免误显示「未提供 README」
		s.logger.Warn("读取插件 README 失败", "id", id, "error", err)
		dto.ReadmeError = err.Error()
		return dto, nil
	}
	dto.Readme = readme
	s.saveCachedReadme(id, readme)
	return dto, nil
}

// loadIndex 读取索引：refresh 或缓存不存在时走 GitHub API 并落盘缓存。
func (s *Service) loadIndex(ctx context.Context, refresh bool) (*pluginmeta.Index, error) {
	cachePath := s.indexCachePath()
	if !refresh {
		if data, err := os.ReadFile(cachePath); err == nil {
			var idx pluginmeta.Index
			if jsonUnmarshal(data, &idx) == nil {
				return &idx, nil
			}
		}
	}
	idx, err := s.client().fetchIndex(ctx, s.branch())
	if err != nil {
		// 拉取失败时回退到缓存（如有），避免限流导致市场完全不可用
		if data, rerr := os.ReadFile(cachePath); rerr == nil {
			var cached pluginmeta.Index
			if jsonUnmarshal(data, &cached) == nil {
				return &cached, nil
			}
		}
		return nil, err
	}
	if err := os.MkdirAll(s.cacheDir(), 0o755); err == nil {
		_ = os.WriteFile(cachePath, mustJSON(idx), 0o644)
		s.clearReadmeCache() // 索引已更新，旧 README 缓存作废，下次详情重新拉取
	}
	return idx, nil
}

// Status 返回当前任务状态快照（面板轮询）。
func (s *Service) Status() map[string]any { return s.state.snapshot() }
