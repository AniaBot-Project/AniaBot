package adminpanel

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// 面板「上下文管理」页的数据来源（可选接口）。
//
// 每个群聊 / 私聊会话在 AI 引擎里都对应一份独立上下文：system prompt（静态
// 提示词 + available_skills + 场景描述）+ 消息历史（含工具调用与工具结果）+
// 工具定义。面板把这套内容原样展示出来，用于核对上下文结构、排查「模型看到
// 了什么」以及评估 token 占用；同时提供删除会话与清空上下文的入口——两者都
// 直接作用在运行中的会话对象与持久化历史上，立即生效。
//
// 视图类型定义在 common/plugininfo（插件只依赖 common），这里用别名引用，
// 插件实现接口时无需 import bot/adminpanel。

// ContextSource 可选接口：插件实现后，面板「上下文管理」页可查看 / 清空 / 删除
// 会话上下文（当前由 AI 对话插件实现）。
//
// 清空与删除都需要先终止该会话正在执行的请求才能保证结果一致（消息窗口由在跑
// 的回合占用）：调用方传 terminate=false 且会话正忙时，实现应返回
// plugininfo.ErrContextBusy，面板据此弹确认框，用户确认后带 terminate=true 重发。
type ContextSource interface {
	// ContextSessions 返回全部会话的上下文摘要（有持久化历史的会话 ∪ 内存驻留
	// 会话），按最近活动倒序。
	ContextSessions() []plugininfo.ContextSession
	// ContextDetail 返回单个会话的完整上下文；会话不存在时返回错误。
	// 会话正在执行请求时返回持久化快照（ContextDetail.Source = persisted），
	// 不阻塞面板请求。
	ContextDetail(key string) (plugininfo.ContextDetail, error)
	// ContextClear 清空指定会话的上下文（保留会话本身，下次发言即新对话）。
	// terminate 为 true 时先终止正在执行的请求；否则忙时返回 ErrContextBusy。
	ContextClear(key string, terminate bool) (plugininfo.ContextClearResult, error)
	// ContextDelete 删除整个会话（上下文 + 会话级运行态与目标）；语义同 ContextClear。
	ContextDelete(key string, terminate bool) (plugininfo.ContextClearResult, error)
	// ContextClearAll 清空全部会话的上下文，返回成功 / 终止 / 失败明细。
	// 正忙且 terminate=false 的会话计入失败，其余照常清空。
	ContextClearAll(terminate bool) plugininfo.ContextClearAllResult
	// ContextExport 返回选中会话（keys 为空表示全部会话）的完整上下文，导出用。
	// 单个会话读取失败时跳过，不影响其余会话。
	ContextExport(keys []string) []plugininfo.ContextDetail
}

// ContextSession / ContextDetail 等视图类型在面板侧的类型别名（见 common/plugininfo）。
type (
	ContextSession = plugininfo.ContextSession
	ContextDetail  = plugininfo.ContextDetail
)

const (
	// contextsMaxKeys 单次清空 / 导出最多处理的会话数（面板侧上限，防止误传巨大列表）。
	contextsMaxKeys = 500
	// contextsKeyMaxLen 会话键长度上限（与存储层 session_id 列宽一致）。
	contextsKeyMaxLen = 255
)

// registerContextRoutes 注册「上下文管理」页相关路由。
func (s *Server) registerContextRoutes() {
	s.mux.Handle("GET /api/contexts", s.requireAuth(http.HandlerFunc(s.handleContextList)))
	s.mux.Handle("GET /api/contexts/detail", s.requireAuth(http.HandlerFunc(s.handleContextDetail)))
	s.mux.Handle("GET /api/contexts/export", s.requireAuth(http.HandlerFunc(s.handleContextsExport)))
	s.mux.Handle("POST /api/contexts/clear", s.requireAuth(http.HandlerFunc(s.handleContextsClear)))
	s.mux.Handle("DELETE /api/contexts", s.requireAuth(http.HandlerFunc(s.handleContextDelete)))
}

// contextsUnavailable 上下文管理能力未就绪时统一返回 404（插件未启用或未实现接口）。
func (s *Server) contextsUnavailable(w http.ResponseWriter) bool {
	if s.opt.Contexts == nil {
		writeError(w, http.StatusNotFound, "上下文管理功能未启用（需启用 AI 对话插件）")
		return true
	}
	return false
}

// parseContextKeys 解析逗号分隔（也接受重复参数）的会话键列表，去重并做基本校验。
func parseContextKeys(r *http.Request, param string) ([]string, error) {
	raw := r.URL.Query()[param]
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	keys := make([]string, 0, len(raw))
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			key := strings.TrimSpace(part)
			if key == "" {
				continue
			}
			if err := validateContextKey(key); err != nil {
				return nil, err
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	if len(keys) > contextsMaxKeys {
		return nil, fmt.Errorf("单次最多处理 %d 个会话", contextsMaxKeys)
	}
	return keys, nil
}

// validateContextKey 校验会话键：g:<会话ID> / f:<用户ID>（ID 允许带平台前缀，
// 如 g:fs:oc_xxx）。
func validateContextKey(key string) error {
	if len(key) < 3 || (key[0] != 'g' && key[0] != 'f') || key[1] != ':' {
		return errors.New("会话键格式应为 g:会话ID 或 f:用户ID")
	}
	if len(key) > contextsKeyMaxLen {
		return errors.New("会话键过长")
	}
	return nil
}

// parseContextTerminate 解析 terminate 参数（"1" / "true" 为真）。
func parseContextTerminate(r *http.Request) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("terminate")))
	return v == "1" || v == "true" || v == "yes"
}

// handleContextList 返回全部会话的上下文摘要（无数据源时返回空数组）。
func (s *Server) handleContextList(w http.ResponseWriter, _ *http.Request) {
	items := []ContextSession{}
	if s.opt.Contexts != nil {
		if got := s.opt.Contexts.ContextSessions(); got != nil {
			items = got
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleContextDetail 返回单个会话的完整上下文。查询参数 key 为会话键
// （如 g:qq:123456 / f:tG:1234）。
func (s *Server) handleContextDetail(w http.ResponseWriter, r *http.Request) {
	if s.contextsUnavailable(w) {
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "缺少 key 参数")
		return
	}
	if err := validateContextKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := s.opt.Contexts.ContextDetail(key)
	if err != nil {
		if errors.Is(err, plugininfo.ErrContextBusy) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleContextsExport 导出选中会话（不带 keys 时导出全部会话）的完整上下文。
// 导出内容与详情页一致（系统提示词分段 / 消息 / 工具定义 / 估算 token），
// 方便离线对比不同会话的上下文结构或做后续优化。
func (s *Server) handleContextsExport(w http.ResponseWriter, r *http.Request) {
	if s.contextsUnavailable(w) {
		return
	}
	keys, err := parseContextKeys(r, "keys")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items := s.opt.Contexts.ContextExport(keys)
	if items == nil {
		items = []ContextDetail{}
	}
	scope := "全部会话"
	if len(keys) > 0 {
		scope = fmt.Sprintf("%d 个会话", len(keys))
	}
	s.opt.Logger.Info("上下文已通过 Web 面板导出", "scope", scope, "count", len(items))
	oplog.Record(oplog.CategorySystem, "context_export",
		fmt.Sprintf("面板导出上下文（%s，%d 份）", scope, len(items)))
	writeDownloadJSON(w, "aniabot-contexts-"+time.Now().Format("20060102-150405")+".json", map[string]any{
		"exported_at": time.Now().Format(time.RFC3339),
		"total":       len(items),
		"items":       items,
	})
}

// handleContextsClear 清空选中会话（all=true 时清空全部会话）的上下文，保留会话本身。
// 请求体：{"keys": ["g:123", ...], "all": false, "terminate": false}
// 有会话正在执行请求且 terminate=false 时，该会话计入 failed 并带 ErrContextBusy 文案，
// 面板据此弹确认框；用户确认后带 terminate=true 重发，实现会先终止请求再清空。
func (s *Server) handleContextsClear(w http.ResponseWriter, r *http.Request) {
	if s.contextsUnavailable(w) {
		return
	}
	var req struct {
		Keys      []string `json:"keys"`
		All       bool     `json:"all"`
		Terminate bool     `json:"terminate"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if !req.All && len(req.Keys) == 0 {
		writeError(w, http.StatusBadRequest, "请指定要清空的会话（keys）或传 all=true")
		return
	}
	res := plugininfo.ContextClearAllResult{OK: true}
	terminated := 0
	if req.All {
		res = s.opt.Contexts.ContextClearAll(req.Terminate)
		terminated = res.Terminated
		if res.OK {
			s.opt.Logger.Info("全部会话上下文已通过 Web 面板清空", "cleared", res.Cleared, "terminated", terminated)
			oplog.Record(oplog.CategorySystem, "context_clear_all",
				fmt.Sprintf("面板清空全部会话上下文（%d 个会话，终止 %d 个请求）", res.Cleared, terminated))
		}
	} else {
		// 先整体校验再执行，避免中途返回 400 时已经清掉了一部分会话
		keys := make([]string, 0, len(req.Keys))
		seen := make(map[string]struct{}, len(req.Keys))
		for _, raw := range req.Keys {
			key := strings.TrimSpace(raw)
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			if err := validateContextKey(key); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		if len(keys) == 0 {
			writeError(w, http.StatusBadRequest, "请指定要清空的会话（keys）或传 all=true")
			return
		}
		if len(keys) > contextsMaxKeys {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("单次最多处理 %d 个会话", contextsMaxKeys))
			return
		}
		for _, key := range keys {
			one, err := s.opt.Contexts.ContextClear(key, req.Terminate)
			if err != nil || !one.OK {
				fail := plugininfo.ContextClearResult{Key: key}
				if err != nil {
					fail.Error = err.Error()
				} else if one.Error != "" {
					fail.Error = one.Error
				}
				res.Failed = append(res.Failed, fail)
				continue
			}
			res.Cleared++
			if one.Terminated {
				terminated++
			}
		}
		res.Terminated = terminated
		res.OK = len(res.Failed) == 0
		if res.Cleared > 0 {
			s.opt.Logger.Info("会话上下文已通过 Web 面板清空", "cleared", res.Cleared, "terminated", terminated)
			oplog.Record(oplog.CategorySystem, "context_clear",
				fmt.Sprintf("面板清空会话上下文（%d 个会话，终止 %d 个请求）", res.Cleared, terminated))
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// handleContextDelete 删除单个会话（上下文 + 会话级运行态）。查询参数：
// key 为会话键，terminate=1 时先终止正在执行的请求（否则忙时返回 409）。
func (s *Server) handleContextDelete(w http.ResponseWriter, r *http.Request) {
	if s.contextsUnavailable(w) {
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "缺少 key 参数")
		return
	}
	if err := validateContextKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.opt.Contexts.ContextDelete(key, parseContextTerminate(r))
	if err != nil {
		if errors.Is(err, plugininfo.ErrContextBusy) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.opt.Logger.Info("会话已通过 Web 面板删除", "key", key, "terminated", res.Terminated)
	oplog.Record(oplog.CategorySystem, "context_delete", "面板删除会话 "+key)
	writeJSON(w, http.StatusOK, res)
}
