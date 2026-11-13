package adminpanel

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/oplog"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// ChatConsoleSource 可选接口：插件实现后，面板「试聊室」页可在面板内直接与 AI
// 对话做测试（当前由 AI 对话插件实现）。
//
// 试聊复用真实对话管线（同一套系统提示词、记忆、知识库、工具与上下文压缩），
// 但回复只回显在面板中、不向任何平台发送消息。会话各自维护转录与 AI 上下文，
// 发送为异步执行：Send 立即返回，面板按 Seq 轮询 Messages 获取流式产出与最终结果。
type ChatConsoleSource interface {
	// ChatConsoleSessions 返回全部试聊会话（最近活动在前）。
	ChatConsoleSessions() []plugininfo.ChatConsoleSession
	// ChatConsoleCreate 新建试聊会话（目标为通用试聊或某个群聊/私聊 scope）。
	ChatConsoleCreate(req plugininfo.ChatConsoleSessionCreate) (plugininfo.ChatConsoleSession, error)
	// ChatConsoleDelete 删除试聊会话及其转录与 AI 上下文。
	ChatConsoleDelete(id string) error
	// ChatConsoleMessages 返回 Seq 大于 afterSeq 的消息（afterSeq=0 取全部），
	// 并告知该会话是否仍在生成。最后一条消息总会被重新返回，供面板刷新流式文本。
	ChatConsoleMessages(id string, afterSeq int64) ([]plugininfo.ChatConsoleMessage, bool, error)
	// ChatConsoleClear 清空转录与 AI 上下文（保留会话本身）。
	ChatConsoleClear(id string) error
	// ChatConsoleSend 异步发送一条用户消息；上一条仍在生成时返回错误。
	ChatConsoleSend(id, text string) error
	// ChatConsoleStop 中止该会话正在生成的回复（未在生成时为空操作）。
	ChatConsoleStop(id string) error
}

// chatConsoleSendMaxBytes 试聊发送请求体上限（单条消息 32K 字节足够）。
const chatConsoleSendMaxBytes = 64 << 10

// chatConsoleTargetsTimeout 汇总各平台通讯录的等待上限：适配器未连接时枚举
// 会阻塞，超时后返回已拿到的部分，避免「新建会话」弹窗一直转圈。
const chatConsoleTargetsTimeout = 8 * time.Second

// registerChatConsoleRoutes 注册试聊室相关路由。
func (s *Server) registerChatConsoleRoutes() {
	s.mux.Handle("GET /api/chatconsole/targets", s.requireAuth(http.HandlerFunc(s.handleChatConsoleTargets)))
	s.mux.Handle("GET /api/chatconsole/sessions", s.requireAuth(http.HandlerFunc(s.handleChatConsoleSessions)))
	s.mux.Handle("POST /api/chatconsole/sessions", s.requireAuth(http.HandlerFunc(s.handleChatConsoleCreate)))
	s.mux.Handle("DELETE /api/chatconsole/sessions/{id}", s.requireAuth(http.HandlerFunc(s.handleChatConsoleDelete)))
	s.mux.Handle("POST /api/chatconsole/sessions/{id}/clear", s.requireAuth(http.HandlerFunc(s.handleChatConsoleClear)))
	s.mux.Handle("GET /api/chatconsole/sessions/{id}/messages", s.requireAuth(http.HandlerFunc(s.handleChatConsoleMessages)))
	s.mux.Handle("POST /api/chatconsole/sessions/{id}/send", s.requireAuth(http.HandlerFunc(s.handleChatConsoleSend)))
	s.mux.Handle("POST /api/chatconsole/sessions/{id}/stop", s.requireAuth(http.HandlerFunc(s.handleChatConsoleStop)))
}

// chatConsoleUnavailable 试聊能力未就绪时统一返回 404（插件未启用或未实现接口）。
func (s *Server) chatConsoleUnavailable(w http.ResponseWriter) bool {
	if s.opt.ChatConsole == nil {
		writeError(w, http.StatusNotFound, "试聊功能未启用（需启用 AI 对话插件）")
		return true
	}
	return false
}

// handleChatConsoleTargets 返回试聊可选目标：通用试聊 + 各平台通讯录里的群/好友。
// 拉取任一平台失败只跳过该平台（群/好友列表因此可为空，仍可用通用试聊）。
func (s *Server) handleChatConsoleTargets(w http.ResponseWriter, r *http.Request) {
	targets := []plugininfo.ChatConsoleTarget{{
		Kind: plugininfo.ChatConsoleTargetTest,
		Name: "通用试聊",
	}}

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		collected []plugininfo.ChatConsoleTarget
	)
	for i := range s.opt.Contacts {
		c := s.opt.Contacts[i]
		wg.Add(1)
		go func(c ContactSource) {
			defer wg.Done()
			var local []plugininfo.ChatConsoleTarget
			if groups, ok := c.Contacts.GetGroupList(); ok && groups != nil {
				for _, g := range *groups {
					id := g.GroupID.String()
					name := strings.TrimSpace(g.GroupName)
					if name == "" {
						name = "群 " + id
					}
					local = append(local, plugininfo.ChatConsoleTarget{
						Kind:     plugininfo.ChatConsoleTargetGroup,
						ID:       id,
						Name:     name,
						Platform: c.Platform,
					})
				}
			}
			if friends, ok := c.Contacts.GetFriendList(); ok && friends != nil {
				for _, f := range *friends {
					id := f.UserID.String()
					name := strings.TrimSpace(f.Remark)
					if name == "" {
						name = strings.TrimSpace(f.Nickname)
					}
					if name == "" {
						name = "用户 " + id
					}
					local = append(local, plugininfo.ChatConsoleTarget{
						Kind:     plugininfo.ChatConsoleTargetFriend,
						ID:       id,
						Name:     name,
						Platform: c.Platform,
					})
				}
			}
			if len(local) == 0 {
				return
			}
			mu.Lock()
			collected = append(collected, local...)
			mu.Unlock()
		}(c)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(chatConsoleTargetsTimeout):
		s.opt.Logger.Warn("试聊目标列表超时，返回已收集部分", "collected", len(collected))
	case <-r.Context().Done():
		return
	}
	// 群在前、私聊在后，各自按名称排序，便于选择器阅读
	sort.SliceStable(collected, func(i, j int) bool {
		if collected[i].Kind != collected[j].Kind {
			return collected[i].Kind == plugininfo.ChatConsoleTargetGroup
		}
		return collected[i].Name < collected[j].Name
	})
	targets = append(targets, collected...)
	writeJSON(w, http.StatusOK, map[string]any{"items": targets})
}

// handleChatConsoleSessions 返回试聊会话列表。
func (s *Server) handleChatConsoleSessions(w http.ResponseWriter, _ *http.Request) {
	if s.chatConsoleUnavailable(w) {
		return
	}
	items := s.opt.ChatConsole.ChatConsoleSessions()
	if items == nil {
		items = []plugininfo.ChatConsoleSession{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleChatConsoleCreate 新建试聊会话。请求体：{title?, target_kind, target_id?, target_name?}。
func (s *Server) handleChatConsoleCreate(w http.ResponseWriter, r *http.Request) {
	if s.chatConsoleUnavailable(w) {
		return
	}
	var req plugininfo.ChatConsoleSessionCreate
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	req.TargetKind = strings.TrimSpace(req.TargetKind)
	req.TargetID = strings.TrimSpace(req.TargetID)
	req.TargetName = strings.TrimSpace(req.TargetName)
	req.Title = strings.TrimSpace(req.Title)
	switch req.TargetKind {
	case plugininfo.ChatConsoleTargetTest:
		// 通用试聊不绑定会话 scope：忽略客户端可能带来的目标信息
		req.TargetID = ""
		req.TargetName = "通用试聊"
	case plugininfo.ChatConsoleTargetGroup, plugininfo.ChatConsoleTargetFriend:
		if req.TargetID == "" {
			writeError(w, http.StatusBadRequest, "绑定群聊/私聊时必须提供 target_id")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "target_kind 仅支持 test / group / friend")
		return
	}
	if utf8.RuneCountInString(req.Title) > 64 {
		writeError(w, http.StatusBadRequest, "标题过长（最多 64 字）")
		return
	}
	sess, err := s.opt.ChatConsole.ChatConsoleCreate(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oplog.Record(oplog.CategorySystem, "chat_console_create",
		"面板新建试聊会话 "+sess.ID+"（"+sess.TargetKind+" "+sess.TargetID+"）")
	writeJSON(w, http.StatusOK, sess)
}

// handleChatConsoleDelete 删除试聊会话（连同转录与 AI 上下文）。
func (s *Server) handleChatConsoleDelete(w http.ResponseWriter, r *http.Request) {
	if s.chatConsoleUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.opt.ChatConsole.ChatConsoleDelete(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	oplog.Record(oplog.CategorySystem, "chat_console_delete", "面板删除试聊会话 "+id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChatConsoleClear 清空试聊会话的转录与 AI 上下文。
func (s *Server) handleChatConsoleClear(w http.ResponseWriter, r *http.Request) {
	if s.chatConsoleUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.opt.ChatConsole.ChatConsoleClear(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	oplog.Record(oplog.CategorySystem, "chat_console_clear", "面板清空试聊会话 "+id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChatConsoleMessages 返回会话消息：after 为上一次拿到的最大 Seq
// （缺省 0 表示取全部），响应含 busy 表示是否仍在生成。
func (s *Server) handleChatConsoleMessages(w http.ResponseWriter, r *http.Request) {
	if s.chatConsoleUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "after 仅支持非负整数")
			return
		}
		after = n
	}
	items, busy, err := s.opt.ChatConsole.ChatConsoleMessages(id, after)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if items == nil {
		items = []plugininfo.ChatConsoleMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "busy": busy})
}

// handleChatConsoleSend 发送一条用户消息（异步执行，回复通过 messages 轮询获取）。
func (s *Server) handleChatConsoleSend(w http.ResponseWriter, r *http.Request) {
	if s.chatConsoleUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, chatConsoleSendMaxBytes)
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	if err := s.opt.ChatConsole.ChatConsoleSend(id, text); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChatConsoleStop 中止会话正在生成的回复。
func (s *Server) handleChatConsoleStop(w http.ResponseWriter, r *http.Request) {
	if s.chatConsoleUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.opt.ChatConsole.ChatConsoleStop(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
