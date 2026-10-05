// Package adminpanel 提供 AniaBot 的 Web 控制面板。
//
// 面板由后端 API（纯 net/http，零额外依赖）与内嵌的 Vue SPA（go:embed dist）
// 组成，功能包括：配置管理（读取/修改配置中心，重启后生效）、运行状态总览、
// 插件列表、群/好友列表与 AI 定时任务管理（列表 / 启停）与执行日志、
// 操作日志（面板与 AI 工具的管理操作审计，见 component/oplog）。
//
// 认证：首次启动生成随机初始密码打印到控制台，SHA-256+salt 哈希存于持久化
// 存储的 __admin 命名空间；登录后签发内存会话（HttpOnly Cookie，24h 过期）。
// 登录接口带防爆破：按来源 IP 统计失败次数，超限锁定并固定延迟响应（见 loginguard.go）。
//
// 注意：使用独立的 http.ServeMux，绝不注册到 http.DefaultServeMux
// （NapCat HTTP 适配器占用了默认 mux 的 / 路由）。
package adminpanel

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/consollog"
	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/bot/component/querylog"
	"github.com/jeanhua/AniaBot/bot/component/sysrestart"
	"github.com/jeanhua/AniaBot/bot/component/tasklog"
	"github.com/jeanhua/AniaBot/bot/core/configstore"
	"github.com/jeanhua/AniaBot/bot/marketplace"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/pluginconfig"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/jeanhua/AniaBot/common/storage"
)

//go:embed dist
var distFS embed.FS

// BotInfo 面板需要的 Bot 运行信息（由 *core.AniaBot 实现，避免 import 环）。
// 群/好友列表属平台适配器专属能力，不在此接口，经 Options.Contacts 提供。
type BotInfo interface {
	GetPluginList() []plugininfo.PluginInfo
	GoroutineNum() int32
	StartTime() time.Time
}

// AdapterStatus 单个平台适配器的连接状态（面板状态总览展示）。
type AdapterStatus struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
}

// ContactSource 单个适配器的通讯录（群/好友列表）能力来源。
// 仅收集了实现 adapter.ContactsExt 的适配器；Telegram、QQ 官方等无枚举 API
// 的平台不会出现在列表中。
type ContactSource struct {
	Name     string              // 适配器名（如 napcat、feishu）
	Platform string              // 平台标识（如 qq、feishu）
	Contacts adapter.ContactsExt // 通讯录能力
}

// TaskLogSource 可选接口：插件实现后，面板「任务日志」页可按条件查询其定时任务
// 执行日志（当前由 AI 对话插件的 clock 功能实现）。
type TaskLogSource interface {
	TaskLogQuery(f tasklog.Filter) []tasklog.Entry
}

// ClockTaskSource 可选接口：插件实现后，面板可对其定时任务做增删改查与启停
// （当前由 AI 对话插件的 clock 功能实现）。
type ClockTaskSource interface {
	ClockTasks() []plugininfo.ClockTaskInfo
	CreateClockTask(t plugininfo.ClockTaskCreate) (string, error)
	UpdateClockTask(id string, f plugininfo.ClockTaskUpdate) error
	DeleteClockTask(id string) error
}

// MsgLogSource 可选接口：插件实现后，面板「消息日志」页可展示其记录的
// 群消息 / 好友消息 / 通知事件（当前由日志打印插件实现，内存环形缓冲）。
// beforeID>0 时仅返回 ID 小于它的更旧日志（滚动分页游标）。
type MsgLogSource interface {
	MsgLogPage(limit int, beforeID uint64) []msglog.Entry
}

// SkillSource 可选接口：插件实现后，面板可对其 AI skill 做列表 / 上传 / 删除
// （当前由 AI 对话插件实现）。上传/删除后插件负责热重载 skill 注册表。
type SkillSource interface {
	// SkillList 返回当前已加载的 skill 列表、skills 目录与白名单（空表示加载全部）
	SkillList() (skills []plugininfo.SkillInfo, dir string, whitelist []string)
	// SkillDelete 按名称删除 skill（同时从磁盘移除）并热重载
	SkillDelete(name string) error
	// SkillUpload 从 zip 压缩包内容安装 skill 并热重载，filename 为原始文件名
	SkillUpload(filename string, data []byte) error
}

// SkillDetailSource 可选接口：在 SkillSource 之外提供 SKILL 详情查看能力。
// 插件未实现时，面板详情入口会返回「功能未启用」，不影响列表 / 上传 / 删除。
type SkillDetailSource interface {
	SkillDetail(name string) (plugininfo.SkillDetail, error)
}

// MemorySource 可选接口：插件实现后，面板「记忆管理」页可对其 AI 长期记忆
// 做列表 / 新增 / 编辑 / 删除（当前由 AI 对话插件实现）。改动即时生效，无需重启。
type MemorySource interface {
	// MemoryScopes 返回已有记忆的会话 scope 列表及各自条数
	MemoryScopes() []plugininfo.MemoryScopeInfo
	// MemoryList 返回指定 scope 的全部记忆（新在前）
	MemoryList(scope string) ([]plugininfo.MemoryEntryInfo, error)
	// MemoryCreate 新增一条记忆，返回生成的 ID
	MemoryCreate(up plugininfo.MemoryEntryUpsert) (string, error)
	// MemoryUpdate 按 ID 更新一条记忆
	MemoryUpdate(up plugininfo.MemoryEntryUpsert) error
	// MemoryDelete 按 ID 删除一条记忆
	MemoryDelete(scope, id string) error
}

// QuotaSource 可选接口：插件实现后，面板「配额管理」页可查看每日 Token 用量
// 并清零（当前由 AI 对话插件实现）。改动即时生效，无需重启。
type QuotaSource interface {
	// QuotaSummary 返回当日配额汇总（全局 + 各会话明细）
	QuotaSummary() (plugininfo.QuotaSummaryInfo, error)
	// QuotaReset 清零配额计数：scope 为 "all" 清空当日全部，否则仅清除指定会话（g:/f: 前缀）
	QuotaReset(scope string) error
}

// TeamSource 可选接口：插件实现后，面板「Agent 团队」页可对其自定义团队
// 做列表 / 新增 / 编辑 / 删除（当前由 AI 对话插件实现）。改动即时生效，无需重启。
type TeamSource interface {
	// TeamRoles 返回预置团队成员角色列表（供面板选择器展示）
	TeamRoles() []plugininfo.TeamRoleInfo
	// TeamScopes 返回已有团队的会话 scope 列表及各自数量
	TeamScopes() []plugininfo.TeamScopeInfo
	// TeamList 返回指定 scope 的全部团队
	TeamList(scope string) ([]plugininfo.TeamInfo, error)
	// TeamCreate 新增一个团队
	TeamCreate(up plugininfo.TeamUpsert) error
	// TeamUpdate 替换一个团队的说明与成员
	TeamUpdate(up plugininfo.TeamUpsert) error
	// TeamDelete 删除一个团队
	TeamDelete(scope, name string) error
}

// QueryLogSource 可选接口：插件实现后，面板「Query 日志」页可展示每次 AI 回复
// 的完整执行记录（耗时、token 用量、工具调用详情）（当前由 AI 对话插件实现）。
type QueryLogSource interface {
	QueryLogRecent(f querylog.Filter) []querylog.Entry
}

// KnowledgeBaseSource 可选接口：插件实现后，面板「知识库」页可对其知识库文档
// 做列表 / 新增 / 编辑 / 删除 / URL 导入（当前由 AI 对话插件实现）。改动即时生效，无需重启。
type KnowledgeBaseSource interface {
	// KnowledgeScopes 返回已有知识库的作用域列表及各自文档条数
	KnowledgeScopes() []plugininfo.KnowledgeScopeInfo
	// KnowledgeList 返回指定 scope 的全部文档（新在前）
	KnowledgeList(scope string) ([]plugininfo.KnowledgeDocInfo, error)
	// KnowledgeCreate 新增一条文档，返回生成的 ID
	KnowledgeCreate(up plugininfo.KnowledgeDocUpsert) (string, error)
	// KnowledgeUpdate 按 ID 更新一条文档
	KnowledgeUpdate(up plugininfo.KnowledgeDocUpsert) error
	// KnowledgeDelete 按 ID 删除一条文档
	KnowledgeDelete(scope, id string) error
	// KnowledgeImportURL 抓取网页正文导入知识库，返回生成的 ID
	KnowledgeImportURL(scope, url string) (string, error)
}

// Options 面板依赖。
type Options struct {
	Listen          string                                             // 监听地址，如 127.0.0.1:7700
	Config          *configstore.Store                                 // 配置中心
	Persistent      storage.PersistentStorage                          // 根持久化存储（__admin 命名空间存密码哈希）
	Bot             BotInfo                                            // 运行信息来源
	Contacts        []ContactSource                                    // 各平台通讯录（群/好友列表）能力来源，可为空
	Adapter         func() string                                      // 适配器连接状态描述
	AdapterDetail   func() string                                      // 适配器状态详情（最近错误/重试次数，可为 nil）
	AdapterStatuses func() []AdapterStatus                             // 各平台适配器状态列表（可为 nil）
	TaskLogs        func(f tasklog.Filter) []tasklog.Entry             // AI 定时任务执行日志（可为 nil）
	Clocks          ClockTaskSource                                    // AI 定时任务列表与启停（可为 nil）
	MsgLogs         func(limit int, beforeID uint64) []msglog.Entry    // 消息日志（群/好友/通知，可为 nil）
	Skills          SkillSource                                        // AI skill 管理（可为 nil）
	Memories        MemorySource                                       // AI 长期记忆管理（可为 nil）
	Knowledge       KnowledgeBaseSource                                // AI 知识库管理（可为 nil）
	Teams           TeamSource                                         // Agent 团队管理（可为 nil）
	Quota           QuotaSource                                        // 每日 Token 配额管理（可为 nil）
	Marketplace     *marketplace.Service                               // 插件市场服务（可为 nil）
	QueryLogs       func(f querylog.Filter) []querylog.Entry           // AI Query 日志（可为 nil）
	ConsoleLogs     func(limit int, beforeID uint64) []consollog.Entry // 控制台日志（slog + log 输出，可为 nil）
	QRLogins        []QRLoginChannel                                   // 支持扫码登录的平台适配器（如微信；可为 nil）
	Logger          *slog.Logger
}

// Server 面板 HTTP 服务。
type Server struct {
	opt     Options
	auth    *authManager
	mux     *http.ServeMux
	started time.Time
	balance balanceCache
}

// NewServer 创建面板服务。Options.Listen 为空时默认 127.0.0.1:7700。
func NewServer(opt Options) *Server {
	if opt.Listen == "" {
		opt.Listen = "127.0.0.1:7700"
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	s := &Server{opt: opt, mux: http.NewServeMux(), started: time.Now()}
	s.auth = newAuthManager(opt.Persistent, opt.Logger)
	s.routes()
	startCPUSampler() // 后台持续采样 CPU，为负载图提供服务端缓存的历史曲线
	return s
}

// Run 启动 HTTP 服务（阻塞），通常以 goroutine 调用。
func (s *Server) Run() {
	s.opt.Logger.Info("Web 控制面板已启动", "listen", s.opt.Listen, "url", "http://"+s.opt.Listen)
	srv := &http.Server{
		Addr:              s.opt.Listen,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.opt.Logger.Error("Web 控制面板启动失败", "error", err)
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.handleMe)))
	s.mux.Handle("POST /api/setup/complete", s.requireAuth(http.HandlerFunc(s.handleSetupComplete)))
	s.mux.Handle("PUT /api/password", s.requireAuth(http.HandlerFunc(s.handleChangePassword)))
	s.mux.Handle("GET /api/config/schema", s.requireAuth(http.HandlerFunc(s.handleConfigSchema)))
	s.mux.Handle("GET /api/config", s.requireAuth(http.HandlerFunc(s.handleConfigGet)))
	s.mux.Handle("GET /api/config/export", s.requireAuth(http.HandlerFunc(s.handleConfigExport)))
	s.mux.Handle("PUT /api/config", s.requireAuth(http.HandlerFunc(s.handleConfigPut)))
	s.mux.Handle("GET /api/config/presets", s.requireAuth(http.HandlerFunc(s.handlePresetList)))
	s.mux.Handle("POST /api/config/presets", s.requireAuth(http.HandlerFunc(s.handlePresetSave)))
	s.mux.Handle("POST /api/config/presets/{name}/apply", s.requireAuth(http.HandlerFunc(s.handlePresetApply)))
	s.mux.Handle("DELETE /api/config/presets/{name}", s.requireAuth(http.HandlerFunc(s.handlePresetDelete)))
	s.mux.Handle("GET /api/files/{name}", s.requireAuth(http.HandlerFunc(s.handleFileGet)))
	s.mux.Handle("PUT /api/files/{name}", s.requireAuth(http.HandlerFunc(s.handleFilePut)))
	s.mux.Handle("GET /api/status", s.requireAuth(http.HandlerFunc(s.handleStatus)))
	s.mux.Handle("GET /api/host", s.requireAuth(http.HandlerFunc(s.handleHost)))
	s.mux.Handle("GET /api/plugins", s.requireAuth(http.HandlerFunc(s.handlePlugins)))
	s.mux.Handle("GET /api/contact/sources", s.requireAuth(http.HandlerFunc(s.handleContactSources)))
	s.mux.Handle("GET /api/contacts", s.requireAuth(http.HandlerFunc(s.handleContacts)))
	s.mux.Handle("GET /api/tasklogs", s.requireAuth(http.HandlerFunc(s.handleTaskLogs)))
	s.mux.Handle("GET /api/msglogs", s.requireAuth(http.HandlerFunc(s.handleMsgLogs)))
	s.mux.Handle("GET /api/querylogs", s.requireAuth(http.HandlerFunc(s.handleQueryLogs)))
	s.mux.Handle("GET /api/consolelogs", s.requireAuth(http.HandlerFunc(s.handleConsoleLogs)))
	s.mux.Handle("GET /api/oplogs", s.requireAuth(http.HandlerFunc(s.handleOpLogs)))
	s.mux.Handle("GET /api/tokenstats", s.requireAuth(http.HandlerFunc(s.handleTokenStats)))
	s.mux.Handle("GET /api/balance", s.requireAuth(http.HandlerFunc(s.handleBalance)))
	s.mux.Handle("GET /api/tokenstats/detail", s.requireAuth(http.HandlerFunc(s.handleTokenStatsDetail)))
	s.mux.Handle("GET /api/clocks", s.requireAuth(http.HandlerFunc(s.handleClockList)))
	s.mux.Handle("POST /api/clocks", s.requireAuth(http.HandlerFunc(s.handleClockCreate)))
	s.mux.Handle("PUT /api/clocks/{id}", s.requireAuth(http.HandlerFunc(s.handleClockUpdate)))
	s.mux.Handle("DELETE /api/clocks/{id}", s.requireAuth(http.HandlerFunc(s.handleClockDelete)))
	s.mux.Handle("GET /api/skills", s.requireAuth(http.HandlerFunc(s.handleSkillList)))
	s.mux.Handle("POST /api/skills", s.requireAuth(http.HandlerFunc(s.handleSkillUpload)))
	s.mux.Handle("GET /api/skills/{name}", s.requireAuth(http.HandlerFunc(s.handleSkillDetail)))
	s.mux.Handle("DELETE /api/skills/{name}", s.requireAuth(http.HandlerFunc(s.handleSkillDelete)))
	s.mux.Handle("GET /api/memory/scopes", s.requireAuth(http.HandlerFunc(s.handleMemoryScopes)))
	s.mux.Handle("GET /api/memory/list", s.requireAuth(http.HandlerFunc(s.handleMemoryList)))
	s.mux.Handle("POST /api/memory", s.requireAuth(http.HandlerFunc(s.handleMemoryCreate)))
	s.mux.Handle("PUT /api/memory", s.requireAuth(http.HandlerFunc(s.handleMemoryUpdate)))
	s.mux.Handle("DELETE /api/memory", s.requireAuth(http.HandlerFunc(s.handleMemoryDelete)))

	s.mux.Handle("GET /api/team/roles", s.requireAuth(http.HandlerFunc(s.handleTeamRoles)))
	s.mux.Handle("GET /api/team/scopes", s.requireAuth(http.HandlerFunc(s.handleTeamScopes)))
	s.mux.Handle("GET /api/team/list", s.requireAuth(http.HandlerFunc(s.handleTeamList)))
	s.mux.Handle("POST /api/team", s.requireAuth(http.HandlerFunc(s.handleTeamCreate)))
	s.mux.Handle("PUT /api/team", s.requireAuth(http.HandlerFunc(s.handleTeamUpdate)))
	s.mux.Handle("DELETE /api/team", s.requireAuth(http.HandlerFunc(s.handleTeamDelete)))
	s.mux.Handle("GET /api/quota", s.requireAuth(http.HandlerFunc(s.handleQuota)))
	s.mux.Handle("POST /api/quota/reset", s.requireAuth(http.HandlerFunc(s.handleQuotaReset)))
	s.mux.Handle("GET /api/knowledge/scopes", s.requireAuth(http.HandlerFunc(s.handleKnowledgeScopes)))
	s.mux.Handle("GET /api/knowledge/list", s.requireAuth(http.HandlerFunc(s.handleKnowledgeList)))
	s.mux.Handle("POST /api/knowledge", s.requireAuth(http.HandlerFunc(s.handleKnowledgeCreate)))
	s.mux.Handle("PUT /api/knowledge", s.requireAuth(http.HandlerFunc(s.handleKnowledgeUpdate)))
	s.mux.Handle("DELETE /api/knowledge", s.requireAuth(http.HandlerFunc(s.handleKnowledgeDelete)))
	s.mux.Handle("POST /api/knowledge/import-url", s.requireAuth(http.HandlerFunc(s.handleKnowledgeImportURL)))
	s.mux.Handle("GET /api/qrlogin/sources", s.requireAuth(http.HandlerFunc(s.handleQRLoginSources)))
	s.mux.Handle("POST /api/qrlogin/{platform}/start", s.requireAuth(http.HandlerFunc(s.handleQRLoginStart)))
	s.mux.Handle("GET /api/qrlogin/{platform}/status", s.requireAuth(http.HandlerFunc(s.handleQRLoginStatus)))
	s.mux.Handle("POST /api/qrlogin/{platform}/verify", s.requireAuth(http.HandlerFunc(s.handleQRLoginVerify)))
	s.mux.Handle("POST /api/restart", s.requireAuth(http.HandlerFunc(s.handleRestart)))
	s.mux.Handle("GET /api/update/info", s.requireAuth(http.HandlerFunc(s.handleUpdateInfo)))
	s.mux.Handle("POST /api/update/start", s.requireAuth(http.HandlerFunc(s.handleUpdateStart)))
	s.mux.Handle("GET /api/update/status", s.requireAuth(http.HandlerFunc(s.handleUpdateStatus)))
	s.mux.Handle("GET /api/marketplace/info", s.requireAuth(http.HandlerFunc(s.handleMarketplaceInfo)))
	s.mux.Handle("GET /api/marketplace/plugins", s.requireAuth(http.HandlerFunc(s.handleMarketplaceList)))
	s.mux.Handle("GET /api/marketplace/plugins/{id}", s.requireAuth(http.HandlerFunc(s.handleMarketplaceDetail)))
	s.mux.Handle("POST /api/marketplace/install", s.requireAuth(http.HandlerFunc(s.handleMarketplaceInstall)))
	s.mux.Handle("POST /api/marketplace/uninstall", s.requireAuth(http.HandlerFunc(s.handleMarketplaceUninstall)))
	s.mux.Handle("POST /api/marketplace/batch", s.requireAuth(http.HandlerFunc(s.handleMarketplaceBatch)))
	s.mux.Handle("POST /api/marketplace/rollback", s.requireAuth(http.HandlerFunc(s.handleMarketplaceRollback)))
	s.mux.Handle("GET /api/marketplace/status", s.requireAuth(http.HandlerFunc(s.handleMarketplaceStatus)))
	s.mux.Handle("POST /api/marketplace/oauth/start", s.requireAuth(http.HandlerFunc(s.handleMarketplaceOAuthStart)))
	s.mux.Handle("GET /api/marketplace/oauth/status", s.requireAuth(http.HandlerFunc(s.handleMarketplaceOAuthStatus)))
	s.mux.Handle("POST /api/marketplace/oauth/cancel", s.requireAuth(http.HandlerFunc(s.handleMarketplaceOAuthCancel)))
	s.mux.Handle("/", s.spaHandler())
}

// spaHandler 提供内嵌的前端静态资源，未命中路径回退到 index.html（SPA 路由）。
func (s *Server) spaHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			r.URL.Path = "/"
			path = "index.html"
		}
		// 带内容哈希的资源可长缓存；index.html 必须每次回源校验，否则前端更新后用户仍加载旧版本
		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=604800")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// requireAuth 校验会话 Cookie；会话被滑动续期时同步刷新 Cookie 过期时间。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		valid, renewed := s.auth.ValidSession(cookie.Value)
		if !valid {
			writeError(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		if renewed {
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    cookie.Value,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Expires:  time.Now().Add(sessionTTL),
			})
		}
		next.ServeHTTP(w, r)
	})
}

// ---- auth handlers ----

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	ip := clientIP(r)
	// 防爆破：来源处于锁定期时直接拒绝
	if locked, remain := s.auth.guard.locked(ip); locked {
		s.opt.Logger.Warn("面板登录被拒绝（来源锁定中）", "ip", ip, "retry_after", remain.Round(time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(int(remain.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("失败次数过多，请约 %d 分钟后再试", int(remain.Minutes())+1))
		return
	}
	if !s.auth.CheckPassword(req.Password) {
		lockedNow, lockDur := s.auth.guard.recordFail(ip)
		time.Sleep(loginFailDelay) // 固定延迟，拖慢在线爆破
		if lockedNow {
			s.opt.Logger.Warn("面板登录连续失败，来源已锁定", "ip", ip, "lock_duration", lockDur)
			w.Header().Set("Retry-After", strconv.Itoa(int(lockDur.Seconds())))
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("失败次数过多，已锁定 %d 分钟", int(lockDur.Minutes())))
			return
		}
		s.opt.Logger.Warn("面板登录失败：密码错误", "ip", ip)
		oplog.Record(oplog.CategoryAuth, "login_fail", "面板登录失败（密码错误），IP: "+ip)
		writeError(w, http.StatusUnauthorized, "密码错误")
		return
	}
	s.auth.guard.recordSuccess(ip)
	s.opt.Logger.Info("面板登录成功", "ip", ip)
	oplog.Record(oplog.CategoryAuth, "login", "面板登录成功，IP: "+ip)
	token := s.auth.NewSession()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.auth.DropSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"setup_required": s.opt.Config.SetupPending(),
	})
}

// handleSetupComplete 标记首次设置向导完成（或跳过）。
func (s *Server) handleSetupComplete(w http.ResponseWriter, _ *http.Request) {
	s.opt.Config.CompleteSetup()
	oplog.Record(oplog.CategorySystem, "setup_complete", "首次设置向导已完成")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(req.NewPassword) < 6 {
		writeError(w, http.StatusBadRequest, "新密码长度至少 6 位")
		return
	}
	if !s.auth.SetPassword(req.NewPassword) {
		writeError(w, http.StatusInternalServerError, "密码保存失败")
		return
	}
	// 修改密码后销毁所有会话（含当前），强制使用新密码重新登录
	s.auth.DropAllSessions()
	oplog.Record(oplog.CategoryAuth, "password_change", "面板密码已修改，IP: "+clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- config handlers ----

func (s *Server) handleConfigSchema(w http.ResponseWriter, _ *http.Request) {
	// 表单元信息来自配置注册表（框架 + 各插件 ConfigRegistrar 动态注册），
	// 新增/移除插件无需改动面板代码。
	writeJSON(w, http.StatusOK, pluginconfig.Fields())
}

func (s *Server) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	all := s.opt.Config.All()
	for _, f := range pluginconfig.Fields() {
		if !f.Sensitive {
			continue
		}
		if v, ok := all[f.Key]; ok {
			if str, ok2 := v.(string); ok2 && str != "" {
				all[f.Key] = maskPlaceholder
			}
		}
	}
	writeJSON(w, http.StatusOK, all)
}

// handleConfigExport 导出完整配置为 JSON 文件下载。
// 与 GET /api/config 不同，敏感字段（密钥/Token 等）不掩码、保留真实值，
// 便于备份与迁移；接口需要登录，响应标记 no-store 并携带下载文件名。
func (s *Server) handleConfigExport(w http.ResponseWriter, _ *http.Request) {
	data, err := json.MarshalIndent(s.opt.Config.All(), "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "导出配置失败")
		return
	}
	filename := fmt.Sprintf("aniabot-config-%s.json", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var updates map[string]any
	if err := readJSON(r, &updates); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误（需要 JSON 对象）")
		return
	}
	var deleted []string
	for k, v := range updates {
		// null 表示删除该键（可选参数清空=恢复未配置语义，不再向下游传）
		if v == nil {
			if s.opt.Config.Delete(k) {
				deleted = append(deleted, k)
			}
			delete(updates, k)
			continue
		}
		// 敏感字段传回掩码占位符表示未修改，跳过
		if str, ok := v.(string); ok && str == maskPlaceholder {
			delete(updates, k)
			continue
		}
		// 面板关键配置不允许置空 listen
		if k == "bot.admin_panel.listen" {
			if str, ok := v.(string); !ok || str == "" {
				delete(updates, k)
			}
		}
	}
	if len(updates) == 0 && len(deleted) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "need_restart": true})
		return
	}
	if len(updates) > 0 {
		if err := s.opt.Config.SetMany(updates); err != nil {
			writeError(w, http.StatusInternalServerError, "配置保存失败: "+err.Error())
			return
		}
	}
	keys := make([]string, 0, len(updates)+len(deleted))
	for k := range updates {
		keys = append(keys, k)
	}
	keys = append(keys, deleted...)
	sort.Strings(keys)
	detail := "面板更新配置（" + strconv.Itoa(len(keys)) + " 项）"
	if len(deleted) > 0 {
		detail += "，清空 " + strconv.Itoa(len(deleted)) + " 项"
	}
	detail += ": " + strings.Join(keys, ", ")
	s.opt.Logger.Info("配置已通过 Web 面板更新", "keys", len(keys), "deleted", len(deleted))
	oplog.Record(oplog.CategoryConfig, "config_update", detail)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "need_restart": true})
}

// ---- file handlers（MCP / Prompt 覆盖 / 钩子 / 自定义命令 JSON） ----

var fileKeys = map[string]string{
	"mcp":      configstore.KeyMCPJSON,
	"prompt":   configstore.KeyPromptJSON,
	"hooks":    configstore.KeyHooksJSON,
	"commands": configstore.KeyCommandsJSON,
}

func (s *Server) handleFileGet(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKeys[r.PathValue("name")]
	if !ok {
		writeError(w, http.StatusNotFound, "未知文件")
		return
	}
	v, _ := s.opt.Config.Get(key)
	str, _ := v.(string)
	writeJSON(w, http.StatusOK, map[string]string{"content": str})
}

func (s *Server) handleFilePut(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKeys[r.PathValue("name")]
	if !ok {
		writeError(w, http.StatusNotFound, "未知文件")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	// 非空内容必须是合法 JSON
	if strings.TrimSpace(req.Content) != "" && !json.Valid([]byte(req.Content)) {
		writeError(w, http.StatusBadRequest, "内容不是合法的 JSON")
		return
	}
	if err := s.opt.Config.Set(key, req.Content); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	// prompt/hooks/commands 由 AI 插件按 TTL 热重读，保存后数秒内生效无需重启；
	// mcp 仍需要重启 Bot 后生效
	needRestart := key != configstore.KeyPromptJSON && key != configstore.KeyHooksJSON && key != configstore.KeyCommandsJSON
	oplog.Record(oplog.CategoryConfig, "file_update", "面板修改扩展配置文件: "+r.PathValue("name"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "need_restart": needRestart})
}

// ---- status / list handlers ----

type pluginDTO struct {
	Name      string `json:"name"`
	HelpWords string `json:"help_words"`
	AdminOnly bool   `json:"admin_only"`
	Author    string `json:"author"`
	Version   string `json:"version"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	adapterStatus := "unknown"
	if s.opt.Adapter != nil {
		adapterStatus = s.opt.Adapter()
	}
	adapterDetail := ""
	if s.opt.AdapterDetail != nil {
		adapterDetail = s.opt.AdapterDetail()
	}
	adapters := []AdapterStatus{}
	if s.opt.AdapterStatuses != nil {
		adapters = s.opt.AdapterStatuses()
	}
	uptime := time.Since(s.opt.Bot.StartTime())
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime_sec":     int64(uptime.Seconds()),
		"started_at":     s.opt.Bot.StartTime().Format(time.RFC3339),
		"goroutines":     s.opt.Bot.GoroutineNum(),
		"adapter_status": adapterStatus,
		"adapter_detail": adapterDetail,
		"adapters":       adapters,
		"plugin_count":   len(s.opt.Bot.GetPluginList()),
	})
}

// handleHost 返回主机硬件配置与运行状态（CPU / 内存占用、系统信息等）。
func (s *Server) handleHost(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, collectHost())
}

func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	list := s.opt.Bot.GetPluginList()
	dtos := make([]pluginDTO, 0, len(list))
	for _, p := range list {
		dtos = append(dtos, pluginDTO{
			Name: p.Name, HelpWords: p.HelpWords,
			AdminOnly: p.AdminOnly, Author: p.Author, Version: p.Version,
		})
	}
	writeJSON(w, http.StatusOK, dtos)
}

// handleContactSources 返回支持通讯录（群/好友列表）的适配器列表。
// 只含实现了 adapter.ContactsExt 的已启用适配器；前端据此渲染平台标签页。
func (s *Server) handleContactSources(w http.ResponseWriter, _ *http.Request) {
	type sourceDTO struct {
		Adapter  string `json:"adapter"`
		Platform string `json:"platform"`
	}
	out := make([]sourceDTO, 0, len(s.opt.Contacts))
	for _, c := range s.opt.Contacts {
		out = append(out, sourceDTO{Adapter: c.Name, Platform: c.Platform})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleContacts 返回指定适配器的群列表或好友列表。
// 查询参数：adapter（适配器名，必填）、kind（groups/friends，默认 groups）。
func (s *Server) handleContacts(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("adapter")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "groups"
	}
	if kind != "groups" && kind != "friends" {
		writeError(w, http.StatusBadRequest, "kind 仅支持 groups / friends")
		return
	}
	var src *ContactSource
	for i := range s.opt.Contacts {
		if s.opt.Contacts[i].Name == name {
			src = &s.opt.Contacts[i]
			break
		}
	}
	if src == nil {
		writeError(w, http.StatusBadRequest, "适配器不支持通讯录或未启用")
		return
	}
	if kind == "friends" {
		friends, ok := src.Contacts.GetFriendList()
		if !ok || friends == nil {
			writeError(w, http.StatusBadGateway, "获取好友列表失败（适配器未连接？）")
			return
		}
		writeJSON(w, http.StatusOK, friends)
		return
	}
	groups, ok := src.Contacts.GetGroupList()
	if !ok || groups == nil {
		writeError(w, http.StatusBadGateway, "获取群列表失败（适配器未连接？）")
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

// handleTaskLogs 按条件分页查询定时任务执行日志（新在前）。
// 支持查询参数：target_type（group/friend）、target_id（群号/QQ）、task_id（任务 ID）、
// status（running/success/timeout/error/interrupted）、start / end（RFC3339 或 datetime-local 格式）、
// keyword（匹配任务标题）、limit（每页条数，默认 50，最大 200）、
// before（分页游标：仅返回比该日志 ID 更旧的记录）。
// 响应：{"items": [...], "has_more": bool}。
func (s *Server) handleTaskLogs(w http.ResponseWriter, r *http.Request) {
	if s.opt.TaskLogs == nil {
		writePagedLogs(w, nil, false)
		return
	}
	q := r.URL.Query()
	f := tasklog.Filter{
		TargetType: q.Get("target_type"),
		TargetID:   q.Get("target_id"),
		TaskID:     q.Get("task_id"),
		Status:     tasklog.Status(q.Get("status")),
		Keyword:    q.Get("keyword"),
	}
	if f.TargetType != "" && f.TargetType != "group" && f.TargetType != "friend" {
		writeError(w, http.StatusBadRequest, "target_type 仅支持 group / friend")
		return
	}
	switch f.Status {
	case "", tasklog.StatusRunning, tasklog.StatusSuccess, tasklog.StatusTimeout, tasklog.StatusError, tasklog.StatusInterrupted:
	default:
		writeError(w, http.StatusBadRequest, "status 仅支持 running / success / timeout / error / interrupted")
		return
	}
	if v := q.Get("start"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "start 时间格式错误（支持 RFC3339 或 2006-01-02T15:04）")
			return
		}
		f.Start = t
	}
	if v := q.Get("end"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "end 时间格式错误（支持 RFC3339 或 2006-01-02T15:04）")
			return
		}
		f.End = t
	}
	if v := q.Get("before"); v != "" {
		if _, err := strconv.ParseUint(v, 36, 64); err != nil {
			writeError(w, http.StatusBadRequest, "before 游标格式错误（应为日志 ID）")
			return
		}
		f.Before = v
	}
	limit := parsePageLimit(q.Get("limit"))
	// 多取一条判断是否还有下一页
	f.Limit = limit + 1
	items := s.opt.TaskLogs(f)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writePagedLogs(w, items, hasMore)
}

// handleMsgLogs 分页返回消息日志（群/好友/通知，新在前）。
// 支持查询参数：limit（每页条数，默认 50，最大 200）、
// before（分页游标：仅返回 ID 小于它的更旧日志）。
// 响应：{"items": [...], "has_more": bool}。
func (s *Server) handleMsgLogs(w http.ResponseWriter, r *http.Request) {
	if s.opt.MsgLogs == nil {
		writePagedLogs(w, nil, false)
		return
	}
	q := r.URL.Query()
	limit := parsePageLimit(q.Get("limit"))
	var before uint64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before 游标格式错误（应为数字日志 ID）")
			return
		}
		before = n
	}
	items := s.opt.MsgLogs(limit+1, before)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writePagedLogs(w, items, hasMore)
}

// handleQueryLogs 按条件分页查询 AI Query 日志（新在前）。
// 支持查询参数：chat_type（group/friend）、target_id（群号/QQ）、sender（触发人 QQ）、
// start / end（RFC3339 或 datetime-local 格式）、keyword（匹配用户输入）、
// limit（每页条数，默认 50，最大 200）、before（分页游标：仅返回比该日志 ID 更旧的记录）。
// 响应：{"items": [...], "has_more": bool}。
func (s *Server) handleQueryLogs(w http.ResponseWriter, r *http.Request) {
	if s.opt.QueryLogs == nil {
		writePagedLogs(w, nil, false)
		return
	}
	q := r.URL.Query()
	f := querylog.Filter{
		ChatType: q.Get("chat_type"),
		TargetID: q.Get("target_id"),
		Sender:   q.Get("sender"),
		Keyword:  q.Get("keyword"),
	}
	if f.ChatType != "" && f.ChatType != "group" && f.ChatType != "friend" {
		writeError(w, http.StatusBadRequest, "chat_type 仅支持 group / friend")
		return
	}
	if v := q.Get("start"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "start 时间格式错误（支持 RFC3339 或 2006-01-02T15:04）")
			return
		}
		f.Start = t
	}
	if v := q.Get("end"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "end 时间格式错误（支持 RFC3339 或 2006-01-02T15:04）")
			return
		}
		f.End = t
	}
	if v := q.Get("before"); v != "" {
		if _, err := strconv.ParseUint(v, 36, 64); err != nil {
			writeError(w, http.StatusBadRequest, "before 游标格式错误（应为日志 ID）")
			return
		}
		f.Before = v
	}
	limit := parsePageLimit(q.Get("limit"))
	f.Limit = limit + 1
	items := s.opt.QueryLogs(f)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writePagedLogs(w, items, hasMore)
}

// handleConsoleLogs 分页返回控制台日志（slog 结构化日志与标准库 log 输出，
// 新在前）。支持查询参数：limit（每页条数，默认 50，最大 200）、
// before（分页游标：仅返回 ID 小于它的更旧日志）。
// 响应：{"items": [...], "has_more": bool}。
func (s *Server) handleConsoleLogs(w http.ResponseWriter, r *http.Request) {
	if s.opt.ConsoleLogs == nil {
		writePagedLogs(w, nil, false)
		return
	}
	q := r.URL.Query()
	limit := parsePageLimit(q.Get("limit"))
	var before uint64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before 游标格式错误（应为数字日志 ID）")
			return
		}
		before = n
	}
	items := s.opt.ConsoleLogs(limit+1, before)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writePagedLogs(w, items, hasMore)
}

// handleOpLogs 按条件分页查询操作日志（面板与 AI 工具的管理操作审计，新在前）。
// 支持查询参数：category（操作分类，见 oplog.Category*）、start / end
// （RFC3339 或 datetime-local 格式）、keyword（匹配操作名 / 详情）、
// limit（每页条数，默认 50，最大 200）、before（分页游标：仅返回比该日志 ID 更旧的记录）。
// 响应：{"items": [...], "has_more": bool}。
func (s *Server) handleOpLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := oplog.Filter{
		Category: q.Get("category"),
		Keyword:  q.Get("keyword"),
	}
	if v := q.Get("start"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "start 时间格式错误（支持 RFC3339 或 2006-01-02T15:04）")
			return
		}
		f.Start = t
	}
	if v := q.Get("end"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "end 时间格式错误（支持 RFC3339 或 2006-01-02T15:04）")
			return
		}
		f.End = t
	}
	if v := q.Get("before"); v != "" {
		if _, err := strconv.ParseUint(v, 36, 64); err != nil {
			writeError(w, http.StatusBadRequest, "before 游标格式错误（应为日志 ID）")
			return
		}
		f.Before = v
	}
	limit := parsePageLimit(q.Get("limit"))
	f.Limit = limit + 1
	items := oplog.Query(f)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writePagedLogs(w, items, hasMore)
}

// parsePageLimit 解析分页大小：默认 50，最大 200；非法值取默认。
func parsePageLimit(v string) int {
	const (
		def = 50
		max = 200
	)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// writePagedLogs 输出分页日志响应；items 为 nil 时序列化为空数组而非 null。
func writePagedLogs(w http.ResponseWriter, items any, hasMore bool) {
	if items == nil {
		items = []any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"has_more": hasMore,
	})
}

// parseQueryTime 解析面板传来的时间：RFC3339，或 datetime-local 控件的
// "2006-01-02T15:04"（按本地时区解释）。
func parseQueryTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", v, time.Local)
}

// handleClockList 返回 AI 定时任务列表（功能未启用时返回空数组）。
func (s *Server) handleClockList(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Clocks == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	tasks := s.opt.Clocks.ClockTasks()
	if tasks == nil {
		tasks = []plugininfo.ClockTaskInfo{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

// handleClockCreate 新建定时任务。
func (s *Server) handleClockCreate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Clocks == nil {
		writeError(w, http.StatusNotFound, "定时任务功能未启用")
		return
	}
	var req plugininfo.ClockTaskCreate
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	id, err := s.opt.Clocks.CreateClockTask(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.opt.Logger.Info("定时任务已通过 Web 面板创建", "task", id, "title", req.Title)
	oplog.Record(oplog.CategoryClock, "clock_create", "面板创建定时任务 "+id+"（"+req.Title+"）")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleClockUpdate 编辑定时任务（仅更新请求体中提供的字段，含启用/停用）。
func (s *Server) handleClockUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Clocks == nil {
		writeError(w, http.StatusNotFound, "定时任务功能未启用")
		return
	}
	var req plugininfo.ClockTaskUpdate
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Cron == nil && req.Title == nil && req.Content == nil && req.Note == nil &&
		req.TimeoutSec == nil && req.Enabled == nil && req.TargetType == nil &&
		req.TargetID == nil && req.RunOnce == nil {
		writeError(w, http.StatusBadRequest, "没有需要更新的字段")
		return
	}
	id := r.PathValue("id")
	if err := s.opt.Clocks.UpdateClockTask(id, req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.opt.Logger.Info("定时任务已通过 Web 面板更新", "task", id)
	oplog.Record(oplog.CategoryClock, "clock_update", "面板更新定时任务 "+id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleClockDelete 删除定时任务。
func (s *Server) handleClockDelete(w http.ResponseWriter, r *http.Request) {
	if s.opt.Clocks == nil {
		writeError(w, http.StatusNotFound, "定时任务功能未启用")
		return
	}
	id := r.PathValue("id")
	if err := s.opt.Clocks.DeleteClockTask(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.opt.Logger.Info("定时任务已通过 Web 面板删除", "task", id)
	oplog.Record(oplog.CategoryClock, "clock_delete", "面板删除定时任务 "+id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- skill handlers（AI skill 管理） ----

// skillUploadMaxBytes 限制上传的 zip 压缩包大小
const skillUploadMaxBytes = 32 << 20 // 32 MiB

// handleSkillList 返回 skill 列表、skills 目录与白名单（功能未启用时返回空）。
func (s *Server) handleSkillList(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Skills == nil {
		writeJSON(w, http.StatusOK, map[string]any{"skills": []any{}, "dir": "", "whitelist": []string{}})
		return
	}
	skills, dir, whitelist := s.opt.Skills.SkillList()
	if skills == nil {
		skills = []plugininfo.SkillInfo{}
	}
	if whitelist == nil {
		whitelist = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"skills":    skills,
		"dir":       dir,
		"whitelist": whitelist,
	})
}

// handleSkillUpload 接收 multipart 表单中的 zip 压缩包（字段名 file），安装为 skill。
func (s *Server) handleSkillUpload(w http.ResponseWriter, r *http.Request) {
	if s.opt.Skills == nil {
		writeError(w, http.StatusNotFound, "skill 功能未启用")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, skillUploadMaxBytes)
	if err := r.ParseMultipartForm(skillUploadMaxBytes); err != nil {
		writeError(w, http.StatusBadRequest, "解析上传内容失败（文件过大？上限 32MB）")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少上传文件（字段名 file）")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取上传文件失败")
		return
	}
	if err := s.opt.Skills.SkillUpload(header.Filename, data); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.opt.Logger.Info("skill 已通过 Web 面板上传", "file", header.Filename)
	oplog.Record(oplog.CategorySkill, "skill_upload", "面板上传 skill: "+header.Filename)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSkillDetail 返回指定 skill 的 SKILL.md 完整内容与附属文件信息。
func (s *Server) handleSkillDetail(w http.ResponseWriter, r *http.Request) {
	if s.opt.Skills == nil {
		writeError(w, http.StatusNotFound, "skill 功能未启用")
		return
	}
	src, ok := s.opt.Skills.(SkillDetailSource)
	if !ok {
		writeError(w, http.StatusNotFound, "skill 详情功能未启用")
		return
	}
	name := r.PathValue("name")
	detail, err := src.SkillDetail(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if detail.Files == nil {
		detail.Files = []plugininfo.SkillFileInfo{}
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleSkillDelete 按名称删除 skill（同时从磁盘移除）。
func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	if s.opt.Skills == nil {
		writeError(w, http.StatusNotFound, "skill 功能未启用")
		return
	}
	name := r.PathValue("name")
	if err := s.opt.Skills.SkillDelete(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.opt.Logger.Info("skill 已通过 Web 面板删除", "skill", name)
	oplog.Record(oplog.CategorySkill, "skill_delete", "面板删除 skill: "+name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- memory handlers（AI 长期记忆管理） ----

// handleMemoryScopes 返回已有记忆的会话 scope 列表及条数（功能未启用时返回空数组）。
// handleQuota 返回当日配额汇总（全局 + 各会话明细）。
func (s *Server) handleQuota(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Quota == nil {
		writeError(w, http.StatusNotFound, "配额功能未启用")
		return
	}
	info, err := s.opt.Quota.QuotaSummary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.Sessions == nil {
		info.Sessions = []plugininfo.QuotaSessionInfo{}
	}
	writeJSON(w, http.StatusOK, info)
}

// handleQuotaReset 清零配额计数，body 形如 {"scope":"g:123"|"f:123"|"all"}。
func (s *Server) handleQuotaReset(w http.ResponseWriter, r *http.Request) {
	if s.opt.Quota == nil {
		writeError(w, http.StatusNotFound, "配额功能未启用")
		return
	}
	var req struct {
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if req.Scope == "" {
		writeError(w, http.StatusBadRequest, "scope 不能为空（g:会话ID / f:用户ID / all）")
		return
	}
	if err := s.opt.Quota.QuotaReset(req.Scope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryQuota, "quota_reset", "面板清零配额: "+req.Scope)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMemoryScopes(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Memories == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	scopes := s.opt.Memories.MemoryScopes()
	if scopes == nil {
		scopes = []plugininfo.MemoryScopeInfo{}
	}
	writeJSON(w, http.StatusOK, scopes)
}

// handleMemoryList 返回指定 scope（query 参数 scope）的全部记忆。
func (s *Server) handleMemoryList(w http.ResponseWriter, r *http.Request) {
	if s.opt.Memories == nil {
		writeError(w, http.StatusNotFound, "记忆功能未启用")
		return
	}
	entries, err := s.opt.Memories.MemoryList(r.URL.Query().Get("scope"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if entries == nil {
		entries = []plugininfo.MemoryEntryInfo{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleMemoryCreate 新增一条记忆。
func (s *Server) handleMemoryCreate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Memories == nil {
		writeError(w, http.StatusNotFound, "记忆功能未启用")
		return
	}
	var req plugininfo.MemoryEntryUpsert
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	id, err := s.opt.Memories.MemoryCreate(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryMemory, "memory_create", "面板新增记忆 "+req.Scope+"/"+id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleMemoryUpdate 按 ID 更新一条记忆。
func (s *Server) handleMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Memories == nil {
		writeError(w, http.StatusNotFound, "记忆功能未启用")
		return
	}
	var req plugininfo.MemoryEntryUpsert
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.opt.Memories.MemoryUpdate(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryMemory, "memory_update", "面板更新记忆 "+req.Scope+"/"+req.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMemoryDelete 按 ID 删除一条记忆（query 参数 scope 与 id）。
func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if s.opt.Memories == nil {
		writeError(w, http.StatusNotFound, "记忆功能未启用")
		return
	}
	q := r.URL.Query()
	if err := s.opt.Memories.MemoryDelete(q.Get("scope"), q.Get("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryMemory, "memory_delete", "面板删除记忆 "+q.Get("scope")+"/"+q.Get("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- team handlers（Agent 团队管理） ----

// handleTeamRoles 返回预置团队成员角色列表（功能未启用时返回空数组）。
func (s *Server) handleTeamRoles(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Teams == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	roles := s.opt.Teams.TeamRoles()
	if roles == nil {
		roles = []plugininfo.TeamRoleInfo{}
	}
	writeJSON(w, http.StatusOK, roles)
}

// handleTeamScopes 返回已有团队的会话 scope 列表及数量（功能未启用时返回空数组）。
func (s *Server) handleTeamScopes(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Teams == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	scopes := s.opt.Teams.TeamScopes()
	if scopes == nil {
		scopes = []plugininfo.TeamScopeInfo{}
	}
	writeJSON(w, http.StatusOK, scopes)
}

// handleTeamList 返回指定 scope（query 参数 scope）的全部团队。
func (s *Server) handleTeamList(w http.ResponseWriter, r *http.Request) {
	if s.opt.Teams == nil {
		writeError(w, http.StatusNotFound, "Agent 团队功能未启用")
		return
	}
	teams, err := s.opt.Teams.TeamList(r.URL.Query().Get("scope"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if teams == nil {
		teams = []plugininfo.TeamInfo{}
	}
	writeJSON(w, http.StatusOK, teams)
}

// handleTeamCreate 新增一个团队。
func (s *Server) handleTeamCreate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Teams == nil {
		writeError(w, http.StatusNotFound, "Agent 团队功能未启用")
		return
	}
	var req plugininfo.TeamUpsert
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.opt.Teams.TeamCreate(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryTeam, "team_create", "面板创建团队 "+req.Scope+"/"+req.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTeamUpdate 替换一个团队的说明与成员。
func (s *Server) handleTeamUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Teams == nil {
		writeError(w, http.StatusNotFound, "Agent 团队功能未启用")
		return
	}
	var req plugininfo.TeamUpsert
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.opt.Teams.TeamUpdate(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryTeam, "team_update", "面板更新团队 "+req.Scope+"/"+req.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTeamDelete 删除一个团队（query 参数 scope 与 name）。
func (s *Server) handleTeamDelete(w http.ResponseWriter, r *http.Request) {
	if s.opt.Teams == nil {
		writeError(w, http.StatusNotFound, "Agent 团队功能未启用")
		return
	}
	q := r.URL.Query()
	if err := s.opt.Teams.TeamDelete(q.Get("scope"), q.Get("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryTeam, "team_delete", "面板删除团队 "+q.Get("scope")+"/"+q.Get("name"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- knowledge handlers（AI 知识库管理） ----

// handleKnowledgeScopes 返回已有知识库的作用域列表及文档条数（功能未启用时返回空数组）。
func (s *Server) handleKnowledgeScopes(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Knowledge == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	scopes := s.opt.Knowledge.KnowledgeScopes()
	if scopes == nil {
		scopes = []plugininfo.KnowledgeScopeInfo{}
	}
	writeJSON(w, http.StatusOK, scopes)
}

// handleKnowledgeList 返回指定 scope（query 参数 scope）的全部文档。
func (s *Server) handleKnowledgeList(w http.ResponseWriter, r *http.Request) {
	if s.opt.Knowledge == nil {
		writeError(w, http.StatusNotFound, "知识库功能未启用")
		return
	}
	docs, err := s.opt.Knowledge.KnowledgeList(r.URL.Query().Get("scope"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if docs == nil {
		docs = []plugininfo.KnowledgeDocInfo{}
	}
	writeJSON(w, http.StatusOK, docs)
}

// handleKnowledgeCreate 新增一条知识库文档。
func (s *Server) handleKnowledgeCreate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Knowledge == nil {
		writeError(w, http.StatusNotFound, "知识库功能未启用")
		return
	}
	var req plugininfo.KnowledgeDocUpsert
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	id, err := s.opt.Knowledge.KnowledgeCreate(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryKnowledge, "knowledge_create", "面板新增知识库文档 "+req.Scope+"/"+id+"（"+req.Title+"）")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleKnowledgeUpdate 按 ID 更新一条知识库文档。
func (s *Server) handleKnowledgeUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Knowledge == nil {
		writeError(w, http.StatusNotFound, "知识库功能未启用")
		return
	}
	var req plugininfo.KnowledgeDocUpsert
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.opt.Knowledge.KnowledgeUpdate(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryKnowledge, "knowledge_update", "面板更新知识库文档 "+req.Scope+"/"+req.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleKnowledgeDelete 按 ID 删除一条知识库文档（query 参数 scope 与 id）。
func (s *Server) handleKnowledgeDelete(w http.ResponseWriter, r *http.Request) {
	if s.opt.Knowledge == nil {
		writeError(w, http.StatusNotFound, "知识库功能未启用")
		return
	}
	q := r.URL.Query()
	if err := s.opt.Knowledge.KnowledgeDelete(q.Get("scope"), q.Get("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryKnowledge, "knowledge_delete", "面板删除知识库文档 "+q.Get("scope")+"/"+q.Get("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleKnowledgeImportURL 抓取网页正文导入知识库。
func (s *Server) handleKnowledgeImportURL(w http.ResponseWriter, r *http.Request) {
	if s.opt.Knowledge == nil {
		writeError(w, http.StatusNotFound, "知识库功能未启用")
		return
	}
	var req plugininfo.KnowledgeImportURLRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	id, err := s.opt.Knowledge.KnowledgeImportURL(req.Scope, req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryKnowledge, "knowledge_import", "面板从 URL 导入知识库文档 "+req.Scope+"/"+id+": "+req.URL)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleRestart 自重启 Bot：先响应请求，再延迟以相同命令行参数重启进程。
// 配置修改随之生效；面板会话持久化在数据库中，重启后无需重新登录。
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	oplog.Record(oplog.CategorySystem, "restart", "面板请求重启 Bot，IP: "+clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		sysrestart.Self(s.opt.Logger)
	}()
}
