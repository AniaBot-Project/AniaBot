// 插件市场面板 API：市场列表/详情/Token 登录/安装/卸载/回滚/状态轮询。
// 后端逻辑在 bot/marketplace，这里只做 HTTP 适配与鉴权。
package adminpanel

import (
	"encoding/json"
	"net/http"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
)

// handleMarketplaceInfo 返回市场环境与配置信息。
func (s *Server) handleMarketplaceInfo(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Marketplace.Info())
}

// handleMarketplaceList 返回市场插件列表（叠加本地安装状态）。
// 查询参数 refresh=1 时强制重新拉取索引。
func (s *Server) handleMarketplaceList(w http.ResponseWriter, r *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	list, err := s.opt.Marketplace.List(r.Context(), refresh)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": list})
}

// handleMarketplaceDetail 返回插件详情（元信息 + README）。
func (s *Server) handleMarketplaceDetail(w http.ResponseWriter, r *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	dto, err := s.opt.Marketplace.Detail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleMarketplaceInstall 开始安装/升级插件。
func (s *Server) handleMarketplaceInstall(w http.ResponseWriter, r *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	var req struct {
		ID     string `json:"id"`
		Commit string `json:"commit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "缺少插件 id")
		return
	}
	if err := s.opt.Marketplace.Install(req.ID, req.Commit); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMarketplaceUninstall 开始卸载插件。
func (s *Server) handleMarketplaceUninstall(w http.ResponseWriter, r *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "缺少插件 id")
		return
	}
	if err := s.opt.Marketplace.Uninstall(req.ID); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMarketplaceBatch 开始批量安装/升级/卸载插件（一次编译、一次重启）。
func (s *Server) handleMarketplaceBatch(w http.ResponseWriter, r *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	var req struct {
		Install   []string `json:"install"`
		Uninstall []string `json:"uninstall"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (len(req.Install) == 0 && len(req.Uninstall) == 0) {
		writeError(w, http.StatusBadRequest, "缺少待操作的插件列表")
		return
	}
	if err := s.opt.Marketplace.Batch(req.Install, req.Uninstall); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMarketplaceRollback 回滚上次插件安装（恢复旧二进制）。
func (s *Server) handleMarketplaceRollback(w http.ResponseWriter, r *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	if err := s.opt.Marketplace.Rollback(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMarketplaceStatus 返回当前市场任务状态（前端轮询）。
func (s *Server) handleMarketplaceStatus(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Marketplace.Status())
}

// handleMarketplaceOAuthStart 开始 GitHub 设备授权流（在线登录）。
func (s *Server) handleMarketplaceOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	out, err := s.opt.Marketplace.StartOAuth(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategoryPlugin, "marketplace_oauth_start", "面板发起 GitHub 在线登录（IP: "+clientIP(r)+"）")
	writeJSON(w, http.StatusOK, out)
}

// handleMarketplaceOAuthStatus 返回设备授权流程状态（前端轮询）。
func (s *Server) handleMarketplaceOAuthStatus(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Marketplace.OAuthStatus())
}

// handleMarketplaceOAuthCancel 取消进行中的设备授权流程。
func (s *Server) handleMarketplaceOAuthCancel(w http.ResponseWriter, _ *http.Request) {
	if s.opt.Marketplace == nil {
		writeError(w, http.StatusBadRequest, "插件市场服务不可用")
		return
	}
	s.opt.Marketplace.CancelOAuth()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
