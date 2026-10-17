package llmtool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type ToolExecuter struct {
	// mu 保护 tools 与 mcpManagers：启动完成后 AI 仍可通过 mcp_add/mcp_remove
	// 等管理工具在运行时增删 MCP 服务器，而各会话的 Tools/Execute 在并发读，
	// 并发 map 读写是不可恢复的 fatal error
	mu          sync.RWMutex
	tools       map[string]Tool
	mcpManagers []*MCPToolManager
}

func NewToolExecuter() *ToolExecuter {
	return &ToolExecuter{
		tools:       make(map[string]Tool),
		mcpManagers: make([]*MCPToolManager, 0),
	}
}

// Register 注册工具；同名工具已存在时跳过并记录日志。
// 不能 panic：重名可能来自用户配置（如 files.mcp_json 中重复的服务器名），
// panic 会越过注册循环的容错逻辑，中断插件的整个初始化流程。
func (e *ToolExecuter) Register(tool Tool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.registerLocked(tool)
}

// registerLocked 是 Register 的无锁内部实现（调用方须已持有写锁）
func (e *ToolExecuter) registerLocked(tool Tool) {
	if _, ok := e.tools[tool.Name()]; ok {
		log.Printf("[ToolExecuter] 工具 '%s' 已注册，跳过重复注册", tool.Name())
		return
	}
	e.tools[tool.Name()] = tool
}

// Has 查询共享层是否已注册同名工具；供平台注入工具注册前的重名检查
// （会话层注册会静默覆盖同名工具，须提前拦下命名冲突）。
func (e *ToolExecuter) Has(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.tools[name]
	return ok
}

// IsMCPToolName 判断共享层中该名字是否为 MCP 工具（eager 模式直接注册在共享层；
// 懒加载模式的具体工具在会话层，由 SessionToolExecutor.IsMCPToolName 判定）。
// 供计划模式门禁阻断 MCP 工具——MCP 工具集合运行时可变，无法靠静态清单覆盖。
func (e *ToolExecuter) IsMCPToolName(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	tool, ok := e.tools[name]
	if !ok {
		return false
	}
	_, isMCP := tool.(*MCPTool)
	return isMCP
}

// IsMCPToolName 判断本会话已加载的工具是否为 MCP 工具。
func (s *SessionToolExecutor) IsMCPToolName(name string) bool {
	s.mu.RLock()
	tool, ok := s.sessionTools[name]
	s.mu.RUnlock()
	if ok {
		if _, isMCP := tool.(*MCPTool); isMCP {
			return true
		}
	}
	return s.shared.IsMCPToolName(name)
}

// HasSession 判断本会话层是否已注册该工具名（供平台工具注入前的判重，
// 共享层的 Has 查不到会话层注册的 clock_*/memory_*/kb_* 等工具）。
func (s *SessionToolExecutor) HasSession(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.sessionTools[name]
	return ok
}

func (e *ToolExecuter) Tools() []ToolDef {
	return e.toolsWithSession(nil)
}

// toolsWithSession 合并共享工具与会话工具的定义列表。
// 输出按工具名排序：Go map 遍历顺序随机，若直接序列化会导致每次请求的
// tools 字段排列不同，把上游 prompt 前缀缓存（如 DeepSeek context caching）
// 全部打失，必须保证完全确定的输出顺序。
// 同名工具共享层与会话层并存时只保留一份定义，与 resolveTool 一致由会话层
// 优先——否则 tools 字段出现两份同名 function 定义（部分提供方直接 400 拒绝），
// 且「下发的定义」与「实际执行的工具」不一致。
func (e *ToolExecuter) toolsWithSession(sessionTools map[string]Tool) []ToolDef {
	e.mu.RLock()
	defer e.mu.RUnlock()
	seen := make(map[string]struct{}, len(e.tools)+len(sessionTools))
	names := make([]string, 0, len(e.tools)+len(sessionTools))
	for name := range sessionTools {
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for name := range e.tools {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)

	tools := make([]ToolDef, 0, len(names))
	for _, name := range names {
		if tool, ok := sessionTools[name]; ok {
			tools = append(tools, structToOpenAITool(tool))
		} else if tool, ok := e.tools[name]; ok {
			tools = append(tools, structToOpenAITool(tool))
		}
	}
	return tools
}

func (e *ToolExecuter) resolveTool(name string, sessionTools map[string]Tool) (Tool, bool) {
	if sessionTools != nil {
		if t, ok := sessionTools[name]; ok {
			return t, true
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	t, ok := e.tools[name]
	return t, ok
}

func (e *ToolExecuter) Execute(ctx context.Context, call ToolCall, callbacks CallBackFuncs) (string, error) {
	return e.executeWithSession(ctx, call, callbacks, nil)
}

func (e *ToolExecuter) executeWithSession(ctx context.Context, call ToolCall, callbacks CallBackFuncs, sessionTools map[string]Tool) (string, error) {
	log.Printf("[ToolExecuter] 尝试执行工具: name=%s, available=%v", call.Name, e.getToolNames())
	tool, ok := e.resolveTool(call.Name, sessionTools)
	if !ok {
		return "", fmt.Errorf("tool '%s' not found. Available tools: %v",
			call.Name, e.getToolNames())
	}

	if mcpTool, ok := tool.(*MCPTool); ok {
		result, err := mcpTool.ExecuteWithArgs(ctx, []byte(call.Arguments), callbacks)
		if err != nil {
			return "", fmt.Errorf("MCP tool '%s' execution failed: %w\nArguments: %s",
				call.Name, err, call.Arguments)
		}
		return result, nil
	}

	params := reflect.New(reflect.TypeOf(tool.Params()).Elem()).Interface()
	if err := json.Unmarshal([]byte(call.Arguments), params); err != nil {
		// 回传该工具真实的 JSON schema（与请求下发的定义一致）帮助模型定位参数
		// 问题；原样回显参数可能长达数千字符（如被截断的 write_file 内容），
		// 只保留开头一段即可定位结构错误，避免浪费上下文
		schema, _ := json.Marshal(structToOpenAITool(tool).Function.Parameters)
		return "", fmt.Errorf("failed to parse arguments for tool '%s': %w\nArguments: %s\n参数 schema: %s",
			call.Name, err, truncateForError(call.Arguments), schema)
	}

	result, err := tool.Execute(ctx, params, callbacks)
	if err != nil {
		// 部分工具的错误把诊断信息放在 result 里（如读取文件的具体失败原因），
		// 直接返回 err 会把它们丢掉、模型只看到"执行失败"占位；合并回传
		if strings.TrimSpace(result) == "" {
			return "", fmt.Errorf("tool '%s' execution failed: %w", call.Name, err)
		}
		return "", fmt.Errorf("tool '%s' execution failed: %w\n工具输出：\n%s", call.Name, err, result)
	}
	return result, nil
}

// truncateForError 错误回填时截断超长参数回显（按字符，保留开头一段）。
func truncateForError(s string) string {
	const maxRunes = 500
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…(参数过长已截断)"
}

func (e *ToolExecuter) getToolNames() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.tools))
	for name := range e.tools {
		names = append(names, name)
	}
	// 排序保证输出确定：该列表会作为"工具未找到"错误文本回填给 LLM
	sort.Strings(names)
	return names
}

func (e *ToolExecuter) NewSessionExecutor() *SessionToolExecutor {
	session := &SessionToolExecutor{
		shared:       e,
		sessionTools: make(map[string]Tool),
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, manager := range e.mcpManagers {
		loaderTool := NewMCPLoaderTool(manager, session)
		session.sessionTools[loaderTool.Name()] = loaderTool
	}
	return session
}

type SessionToolExecutor struct {
	shared *ToolExecuter
	mu     sync.RWMutex // 保护 sessionTools：同一轮的多个工具调用并行执行，
	// mcp_load 等工具会并发 RegisterSession（写）而其他工具的 Execute/Tools 在读，
	// 并发 map 读写是不可恢复的 fatal error（见 aichat.ToolOrchestrator 并行调度）
	sessionTools map[string]Tool
}

// snapshotSessionTools 拷贝当前会话工具表：读写均在锁内完成快照，
// 后续遍历/解析在锁外进行，避免与并发 RegisterSession 产生 map 竞争。
func (s *SessionToolExecutor) snapshotSessionTools() map[string]Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := make(map[string]Tool, len(s.sessionTools))
	maps.Copy(snapshot, s.sessionTools)
	return snapshot
}

func (s *SessionToolExecutor) Tools() []ToolDef {
	return s.shared.toolsWithSession(s.snapshotSessionTools())
}

func (s *SessionToolExecutor) Execute(ctx context.Context, call ToolCall, callbacks CallBackFuncs) (string, error) {
	return s.shared.executeWithSession(ctx, call, callbacks, s.snapshotSessionTools())
}

func (s *SessionToolExecutor) RegisterSession(tool Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionTools[tool.Name()] = tool
}

func (s *SessionToolExecutor) ClearDynamicMCPTools() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := 0
	for name, tool := range s.sessionTools {
		if _, ok := tool.(*MCPTool); ok {
			delete(s.sessionTools, name)
			cleared++
		}
	}
	return cleared
}

// ─────────────────────────────────────────────
// 运行时 MCP 管理：供 AI 的 MCP 管理工具（mcp_add/mcp_remove/mcp_reconnect）
// 在启动完成后动态增删/重连 MCP 服务器
// ─────────────────────────────────────────────

// MCPServerInfo 是 MCP 服务器的运行时信息（供管理工具展示状态）
type MCPServerInfo struct {
	Name        string // 服务器名称
	Description string // 服务器描述
	ToolCount   int    // 发现的工具数量
	Lazy        bool   // true=懒加载（发现模式），false=全量注册
}

// AddMCP 运行时注册 MCP 服务器：lazy 为 true 走工具发现模式（mcp_discover/mcp_load
// 按需加载，新会话自动获得对应的 mcp_load 工具），false 全量注册所有工具。
// 连接失败返回错误且不留残余注册。ctx 为调用方上下文（如 AI 工具调用），
// 取消/超时会中止连接建立，且连接等待不会超过 30s。
func (e *ToolExecuter) AddMCP(ctx context.Context, config *MCPConfig, lazy bool) error {
	e.mu.RLock()
	dup := e.hasMCPLocked(config.Name)
	e.mu.RUnlock()
	if dup {
		return fmt.Errorf("MCP 服务器 '%s' 已注册", config.Name)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := NewMCPClient(config)
	if lazy {
		manager := NewMCPToolManager(client)
		if err := manager.Initialize(ctx); err != nil {
			return fmt.Errorf("连接 MCP 服务器 '%s' 失败: %w", config.Name, err)
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.registerLocked(NewMCPDiscoveryTool(manager))
		e.mcpManagers = append(e.mcpManagers, manager)
		log.Printf("[MCP:%s] 运行时注册完成（发现模式），共 %d 个工具", config.Name, len(manager.toolDefinitions))
		return nil
	}

	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("连接 MCP 服务器 '%s' 失败: %w", config.Name, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, toolDef := range client.GetTools() {
		e.registerLocked(NewMCPTool(client, toolDef))
	}
	log.Printf("[MCP:%s] 运行时注册完成（全量模式），共 %d 个工具", config.Name, len(client.GetTools()))
	return nil
}

// RemoveMCP 运行时移除 MCP 服务器：关闭连接，并移除其发现工具（懒加载模式）
// 或全部已注册工具（全量模式）。已创建会话中残留的 mcp_load/旧工具句柄
// 在调用时会以「未连接」错误优雅失败。
//
// Close 是一次可能阻塞的网络往返（默认超时可达 config.Timeout），必须在
// e.mu 释放后执行：e.mu 是所有会话共享的工具表锁，持锁关闭会让全部会话的
// Tools()/Execute() 一起卡住。
func (e *ToolExecuter) RemoveMCP(name string) error {
	var closing []*MCPClient

	e.mu.Lock()
	// 懒加载（发现模式）：移除 manager 与发现工具
	for i, manager := range e.mcpManagers {
		if manager.Name() == name {
			closing = append(closing, manager.clientSnapshot())
			delete(e.tools, NewMCPDiscoveryTool(manager).Name())
			e.mcpManagers = append(e.mcpManagers[:i], e.mcpManagers[i+1:]...)
			e.mu.Unlock()
			closeMCPClients(closing)
			log.Printf("[MCP:%s] 已运行时移除（发现模式）", name)
			return nil
		}
	}

	// 全量注册模式：移除属于该服务器的全部工具
	removed := 0
	for toolName, tool := range e.tools {
		if mcpTool, ok := tool.(*MCPTool); ok && mcpTool.client.config.Name == name {
			closing = append(closing, mcpTool.client)
			delete(e.tools, toolName)
			removed++
		}
	}
	e.mu.Unlock()

	if removed > 0 {
		closeMCPClients(closing)
		log.Printf("[MCP:%s] 已运行时移除（全量模式），清理 %d 个工具", name, removed)
		return nil
	}

	return fmt.Errorf("MCP 服务器 '%s' 未注册", name)
}

// closeMCPClients 关闭 MCP 客户端（去重）。调用方须已释放工具表锁。
func closeMCPClients(clients []*MCPClient) {
	seen := make(map[*MCPClient]struct{}, len(clients))
	for _, c := range clients {
		if c == nil {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		c.Close()
	}
}

// ReconnectMCP 重新连接指定 MCP 服务器并刷新其工具列表（两种模式均支持）。
// 会话内此前已加载的该服务器工具句柄随之失效，需要重新 mcp_load。
//
// 连接建立（Connect 可能阻塞长达 config.Timeout）在全局写锁之外完成：
// e.mu 保护所有会话共享的工具表，若持锁连接，一个挂死的服务器会卡住全部
// 会话的工具解析与列表下发（与 AddMCP 的「先连接后换锁」模式一致）。
func (e *ToolExecuter) ReconnectMCP(ctx context.Context, name string) error {
	// 懒加载（发现模式）：manager 原地重连，会话内的 mcp_load 工具引用同一
	// manager，重连后新加载的工具自动使用新连接（manager.mu 为该服务器私有，
	// 阻塞范围仅限此服务器的按需加载）
	e.mu.RLock()
	var manager *MCPToolManager
	for _, m := range e.mcpManagers {
		if m.Name() == name {
			manager = m
			break
		}
	}
	e.mu.RUnlock()
	if manager != nil {
		return manager.Reconnect(ctx)
	}

	// 全量注册模式：读锁下仅定位旧客户端，连接在锁外建立
	e.mu.RLock()
	var oldClient *MCPClient
	for _, tool := range e.tools {
		if mcpTool, ok := tool.(*MCPTool); ok && mcpTool.client.config.Name == name {
			oldClient = mcpTool.client
			break
		}
	}
	e.mu.RUnlock()
	if oldClient == nil {
		return fmt.Errorf("MCP 服务器 '%s' 未注册", name)
	}

	fresh := NewMCPClient(oldClient.config)
	if err := fresh.Connect(ctx); err != nil {
		return fmt.Errorf("重连 MCP 服务器 '%s' 失败: %w", name, err)
	}

	e.mu.Lock()
	// 双检：锁外连接期间该服务器可能已被移除（RemoveMCP 会关闭旧连接），
	// 此时不得把新工具重新挂回已删除的服务器
	present := false
	for _, t := range e.tools {
		if t2, ok := t.(*MCPTool); ok && t2.client == oldClient {
			present = true
			break
		}
	}
	if !present {
		e.mu.Unlock()
		fresh.Close()
		return fmt.Errorf("MCP 服务器 '%s' 已被移除", name)
	}
	for toolName, t := range e.tools {
		if t2, ok := t.(*MCPTool); ok && t2.client == oldClient {
			delete(e.tools, toolName)
		}
	}
	for _, toolDef := range fresh.GetTools() {
		// 经 registerLocked 而非直接赋值：重连同样必须遵守重名保护，否则一个
		// 暴露 bash/read_file 等同名工具的 MCP server 会在重连后静默顶掉所有
		// 会话共享的内置工具（启动路径与 mcp_add 都会被拦下）。
		e.registerLocked(NewMCPTool(fresh, toolDef))
	}
	e.mu.Unlock()

	// 旧连接关闭与保连接建立都可能在网络上阻塞，放在锁外执行（同 AddMCP 先例）
	oldClient.Close()
	log.Printf("[MCP:%s] 重连完成（全量模式），共 %d 个工具", name, len(fresh.GetTools()))
	return nil
}

// MCPServerInfos 返回当前已注册的全部 MCP 服务器信息（按名称排序，
// 输出会作为管理工具结果回填给 LLM，必须保证顺序确定）
func (e *ToolExecuter) MCPServerInfos() []MCPServerInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()

	infos := make([]MCPServerInfo, 0, len(e.mcpManagers))
	for _, manager := range e.mcpManagers {
		name, description := manager.serverInfo()
		infos = append(infos, MCPServerInfo{
			Name:        name,
			Description: description,
			ToolCount:   manager.toolDefinitionCount(),
			Lazy:        true,
		})
	}

	// 全量注册模式：按客户端聚合同一服务器的工具
	eagerClients := make(map[*MCPClient]int)
	for _, tool := range e.tools {
		if mcpTool, ok := tool.(*MCPTool); ok {
			eagerClients[mcpTool.client]++
		}
	}
	for client, count := range eagerClients {
		infos = append(infos, MCPServerInfo{
			Name:        client.config.Name,
			Description: client.config.Description,
			ToolCount:   count,
			Lazy:        false,
		})
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// hasMCPLocked 检查指定名称的 MCP 服务器是否已注册（调用方须已持有读锁或写锁）
func (e *ToolExecuter) hasMCPLocked(name string) bool {
	for _, manager := range e.mcpManagers {
		if manager.Name() == name {
			return true
		}
	}
	for _, tool := range e.tools {
		if mcpTool, ok := tool.(*MCPTool); ok && mcpTool.client.config.Name == name {
			return true
		}
	}
	return false
}
