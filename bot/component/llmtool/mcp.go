package llmtool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPConfig MCP服务器配置
type MCPConfig struct {
	Name        string            `json:"name"`        // MCP服务器名称
	Transport   string            `json:"transport"`   // 传输类型: stdio(默认), streamable, sse
	Command     string            `json:"command"`     // 启动命令 (stdio)
	Args        []string          `json:"args"`        // 命令参数 (stdio)
	Env         map[string]string `json:"env"`         // 环境变量 (stdio)
	Endpoint    string            `json:"endpoint"`    // HTTP 端点 URL (streamable/sse)
	Headers     map[string]string `json:"headers"`     // HTTP 请求头 (streamable/sse)
	Timeout     time.Duration     `json:"timeout"`     // 请求超时时间
	Description string            `json:"description"` // MCP服务器描述
	ToolFilter  ToolFilterFunc    `json:"-"`           // 工具过滤器（可选）
}

// ToolFilterFunc 工具过滤函数，返回 true 表示保留该工具
type ToolFilterFunc func(tool *mcp.Tool) bool

// MCPClient MCP客户端（基于官方SDK）
type MCPClient struct {
	config  *MCPConfig
	client  *mcp.Client
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

// NewMCPClient 创建新的MCP客户端
func NewMCPClient(config *MCPConfig) *MCPClient {
	if config.Timeout == 0 {
		config.Timeout = 120 * time.Second
	}

	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "ania-bot-mcp-client",
			Version: "1.0.0",
		},
		nil,
	)

	return &MCPClient{
		config: config,
		client: client,
	}
}

// Connect 连接到MCP服务器并获取工具列表
func (c *MCPClient) Connect(ctx context.Context) error {
	connectCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	var transport mcp.Transport

	switch strings.ToLower(c.config.Transport) {
	case "streamable", "streamable-http":
		log.Printf("[MCP:%s] 连接 Streamable HTTP: %s", c.config.Name, c.config.Endpoint)
		transport = &mcp.StreamableClientTransport{
			Endpoint:   c.config.Endpoint,
			HTTPClient: newStreamHTTPClient(c.config.Headers),
		}
	case "sse":
		log.Printf("[MCP:%s] 连接 SSE: %s", c.config.Name, c.config.Endpoint)
		transport = &mcp.SSEClientTransport{
			Endpoint:   c.config.Endpoint,
			HTTPClient: newStreamHTTPClient(c.config.Headers),
		}
	default:
		log.Printf("[MCP:%s] 启动进程: %s %v", c.config.Name, c.config.Command, c.config.Args)
		cmd := exec.Command(c.config.Command, c.config.Args...)
		if len(c.config.Env) > 0 {
			env := cmd.Environ()
			for k, v := range c.config.Env {
				env = append(env, fmt.Sprintf("%s=%s", k, v))
			}
			cmd.Env = env
		}
		transport = &mcp.CommandTransport{Command: cmd}
	}

	// 连接到服务器
	session, err := c.client.Connect(connectCtx, transport, nil)
	if err != nil {
		return fmt.Errorf("连接MCP服务器失败: %w", err)
	}
	c.session = session

	log.Printf("[MCP:%s] 连接成功，获取工具列表...", c.config.Name)

	// 获取工具列表
	toolsResp, err := session.ListTools(connectCtx, &mcp.ListToolsParams{})
	if err != nil {
		c.Close()
		return fmt.Errorf("获取工具列表失败: %w", err)
	}

	// 应用工具过滤器
	if c.config.ToolFilter != nil {
		filtered := make([]*mcp.Tool, 0)
		for _, tool := range toolsResp.Tools {
			if c.config.ToolFilter(tool) {
				filtered = append(filtered, tool)
			}
		}
		c.tools = filtered
		log.Printf("[MCP:%s] 过滤后获取到 %d 个工具（原始 %d 个）",
			c.config.Name, len(c.tools), len(toolsResp.Tools))
	} else {
		c.tools = toolsResp.Tools
		log.Printf("[MCP:%s] 获取到 %d 个工具", c.config.Name, len(c.tools))
	}

	return nil
}

// GetTools 获取MCP服务器提供的所有工具定义
func (c *MCPClient) GetTools() []*mcp.Tool {
	return c.tools
}

// CallTool 调用MCP工具
func (c *MCPClient) CallTool(ctx context.Context, toolName string, arguments json.RawMessage) (string, error) {
	if c.session == nil {
		return "", fmt.Errorf("MCP客户端未连接")
	}

	log.Printf("[MCP] 调用工具: %s/%s", c.config.Name, toolName)

	// 解析参数
	var args map[string]any
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", fmt.Errorf("解析参数失败: %w", err)
		}
	}

	// 调用工具
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("调用工具失败: %w", err)
	}

	if result.IsError {
		// 服务器在错误时通常也在 Content 中回传具体错误文本（多数实现 error 时
		// 同样返回 TextContent），保留它让模型能根据错误详情纠正参数
		if msg := extractMCPText(result.Content); msg != "" {
			return "", fmt.Errorf("工具执行错误: %s", truncateMCPResult(msg))
		}
		return "", fmt.Errorf("工具执行错误")
	}

	return truncateMCPResult(extractMCPText(result.Content)), nil
}

// extractMCPText 拼接 MCP 工具返回的全部 TextContent。
func extractMCPText(contents []mcp.Content) string {
	var textBuilder strings.Builder
	for _, content := range contents {
		if textContent, ok := content.(*mcp.TextContent); ok {
			textBuilder.WriteString(textContent.Text)
		}
	}
	return textBuilder.String()
}

// truncateMCPResult 按 rune 截断 MCP 工具结果：超大结果直接进入下一轮
// LLM 上下文并永久留在窗口历史中，会撑爆上下文并拖垮后续压缩。
func truncateMCPResult(s string) string {
	const maxRunes = 8000
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "\n...(MCP 工具结果已截断)"
	}
	return s
}

// Close 关闭MCP客户端
func (c *MCPClient) Close() error {
	if c.session != nil {
		return c.session.Close()
	}
	return nil
}

// MCPTool 包装MCP工具为本地Tool接口
type MCPTool struct {
	client     *MCPClient
	definition *mcp.Tool
}

// NewMCPTool 创建新的MCP工具包装器
func NewMCPTool(client *MCPClient, definition *mcp.Tool) *MCPTool {
	return &MCPTool{
		client:     client,
		definition: definition,
	}
}

// Name 返回工具名称
func (t *MCPTool) Name() string {
	return t.definition.Name
}

// Description 返回工具描述
func (t *MCPTool) Description() string {
	return t.definition.Description
}

// Params 返回工具参数定义
func (t *MCPTool) Params() any {
	return &struct{}{}
}

// GetInputSchema 返回 MCP 工具的输入 schema
func (t *MCPTool) GetInputSchema() map[string]any {
	if t.definition.InputSchema == nil {
		return nil
	}
	if schema, ok := t.definition.InputSchema.(map[string]any); ok {
		return schema
	}
	return nil
}

// Execute 执行MCP工具（标准接口）
func (t *MCPTool) Execute(ctx context.Context, params any, callbacks CallBackFuncs) (string, error) {
	args, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("序列化参数失败: %w", err)
	}
	return t.ExecuteWithArgs(ctx, args, callbacks)
}

// ExecuteWithArgs 使用原始 JSON 参数执行 MCP 工具
func (t *MCPTool) ExecuteWithArgs(ctx context.Context, args json.RawMessage, callbacks CallBackFuncs) (string, error) {
	result, err := t.client.CallTool(ctx, t.definition.Name, args)
	if err != nil {
		return "", fmt.Errorf("MCP工具调用失败: %w", err)
	}
	return result, nil
}

// IsMCPTool 检查工具是否是MCP工具
func (t *MCPTool) IsMCPTool() bool {
	return true
}

// GetMCPToolDefinition 获取MCP工具原始定义
func (t *MCPTool) GetMCPToolDefinition() *mcp.Tool {
	return t.definition
}

// MCPToolManager 管理 MCP 工具的延迟加载
type MCPToolManager struct {
	client          *MCPClient
	mu              sync.Mutex // 保护 toolCache 与 client/toolDefinitions（Reconnect 会整体替换）
	toolCache       map[string]*MCPTool
	toolDefinitions []*mcp.Tool
}

// NewMCPToolManager 创建 MCP 工具管理器
func NewMCPToolManager(client *MCPClient) *MCPToolManager {
	return &MCPToolManager{
		client:    client,
		toolCache: make(map[string]*MCPTool),
	}
}

// clientSnapshot 取当前客户端引用。Reconnect 会替换 m.client，读侧必须持锁取值
// （直接读 m.client 与 Reconnect 的写并发构成数据竞争）。
func (m *MCPToolManager) clientSnapshot() *MCPClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.client
}

// serverInfo 取服务器展示信息（名称、描述）。Reconnect 替换 m.client 时读侧同样
// 需要持锁，否则可能读到半更新的指针。
func (m *MCPToolManager) serverInfo() (name, description string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil || m.client.config == nil {
		return "", ""
	}
	return m.client.config.Name, m.client.config.Description
}

// toolDefinitionCount 当前已发现的工具数量（持锁读取，供 mcp_list 展示）。
func (m *MCPToolManager) toolDefinitionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.toolDefinitions)
}

// Initialize 初始化管理器，连接并获取工具列表
func (m *MCPToolManager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defs := client.GetTools()
	m.mu.Lock()
	m.toolDefinitions = defs
	m.mu.Unlock()
	log.Printf("[MCP:%s] 发现 %d 个工具（延迟加载模式）", client.config.Name, len(defs))
	return nil
}

// Name 返回该管理器对应的 MCP 服务器名称（持锁读取，与 Reconnect 的替换互斥）
func (m *MCPToolManager) Name() string {
	name, _ := m.serverInfo()
	return name
}

// Reconnect 重建客户端连接并刷新工具列表与工具缓存。
// 会话内此前通过 mcp_load 加载的该服务器工具仍绑定旧客户端，
// 重连后需重新 mcp_load 才能使用（旧句柄调用会以「未连接」错误失败）。
func (m *MCPToolManager) Reconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fresh := NewMCPClient(m.client.config)
	if err := fresh.Connect(ctx); err != nil {
		return fmt.Errorf("重连失败: %w", err)
	}
	m.client.Close()
	m.client = fresh
	m.toolCache = make(map[string]*MCPTool)
	m.toolDefinitions = fresh.GetTools()
	log.Printf("[MCP:%s] 重连完成，发现 %d 个工具", fresh.config.Name, len(m.toolDefinitions))
	return nil
}

// GetToolNames 获取所有工具名称和简短描述。
// 在锁内取快照后在锁外组装：读侧不加锁与 Reconnect 的替换并发构成数据竞争
// （slice header 三字被撕裂时 range 可越界 panic）。
func (m *MCPToolManager) GetToolNames() []map[string]string {
	m.mu.Lock()
	defs := m.toolDefinitions
	m.mu.Unlock()
	result := make([]map[string]string, 0, len(defs))
	for _, tool := range defs {
		result = append(result, map[string]string{
			"name":        tool.Name,
			"description": tool.Description,
		})
	}
	return result
}

// LoadTool 按需加载具体工具
func (m *MCPToolManager) LoadTool(toolName string) (*MCPTool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查缓存
	if tool, ok := m.toolCache[toolName]; ok {
		return tool, nil
	}

	// 查找工具定义
	for _, toolDef := range m.toolDefinitions {
		if toolDef.Name == toolName {
			tool := NewMCPTool(m.client, toolDef)
			m.toolCache[toolName] = tool
			return tool, nil
		}
	}

	return nil, fmt.Errorf("工具 '%s' 不存在", toolName)
}

// RegisterMCP 注册MCP服务器中的所有工具到执行器（传统方式，会导致上下文爆炸）
func (e *ToolExecuter) RegisterMCP(client *MCPClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 连接到MCP服务器
	if err := client.Connect(ctx); err != nil {
		return err
	}

	tools := client.GetTools()
	log.Printf("[MCP] 共发现 %d 个工具，开始注册...", len(tools))

	// 注册所有工具
	for _, toolDef := range tools {
		tool := NewMCPTool(client, toolDef)
		e.Register(tool)
	}

	log.Printf("[MCP] 所有工具注册完成")
	return nil
}

// RegisterMCPWithDiscovery 使用工具发现模式注册 MCP（推荐方式，避免上下文爆炸）
// DiscoveryTool 注册到共享层，LoaderTool 在 NewSessionExecutor 时自动注入到会话层
func (e *ToolExecuter) RegisterMCPWithDiscovery(client *MCPClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewMCPToolManager(client)
	if err := manager.Initialize(ctx); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	// 发现工具注册到共享层（只读，无副作用）
	discoveryTool := NewMCPDiscoveryTool(manager)
	e.registerLocked(discoveryTool)

	// 保存 manager，session 创建时自动注入对应的 LoaderTool
	e.mcpManagers = append(e.mcpManagers, manager)

	log.Printf("[MCP:%s] 工具发现模式注册完成", client.config.Name)
	return nil
}

// RegisterMCPWithConfig 使用配置直接注册MCP工具
func (e *ToolExecuter) RegisterMCPWithConfig(config *MCPConfig) error {
	client := NewMCPClient(config)
	return e.RegisterMCP(client)
}

// RegisterMCPWithConfigDiscovery 使用配置和工具发现模式注册 MCP（推荐）
func (e *ToolExecuter) RegisterMCPWithConfigDiscovery(config *MCPConfig) error {
	client := NewMCPClient(config)
	return e.RegisterMCPWithDiscovery(client)
}

// MCPDiscoveryTool 工具发现工具
type MCPDiscoveryTool struct {
	manager *MCPToolManager
}

func NewMCPDiscoveryTool(manager *MCPToolManager) *MCPDiscoveryTool {
	return &MCPDiscoveryTool{manager: manager}
}

func (t *MCPDiscoveryTool) Name() string {
	name, _ := t.manager.serverInfo()
	return fmt.Sprintf("mcp_discover_%s", name)
}

func (t *MCPDiscoveryTool) Description() string {
	_, description := t.manager.serverInfo()
	return fmt.Sprintf("发现 %s MCP 服务器提供的所有可用工具。返回工具名称和简短描述列表。使用此工具了解有哪些工具可用，然后使用 mcp_load 工具加载具体工具。", description)
}

func (t *MCPDiscoveryTool) Params() any {
	return &struct{}{}
}

func (t *MCPDiscoveryTool) Execute(ctx context.Context, params any, callbacks CallBackFuncs) (string, error) {
	log.Printf("[MCP:%s] 🔍 模型正在发现可用工具...", t.manager.Name())

	tools := t.manager.GetToolNames()
	data, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		log.Printf("[MCP:%s] ❌ 工具发现失败: %v", t.manager.Name(), err)
		return "", err
	}

	log.Printf("[MCP:%s] ✅ 工具发现完成，共 %d 个工具", t.manager.Name(), len(tools))
	return string(data), nil
}

// MCPLoaderTool 工具加载工具
type MCPLoaderTool struct {
	manager         *MCPToolManager
	sessionExecutor *SessionToolExecutor
}

type MCPLoaderParams struct {
	ToolName string `json:"tool_name" desc:"要加载的工具名称"`
}

func NewMCPLoaderTool(manager *MCPToolManager, sessionExecutor *SessionToolExecutor) *MCPLoaderTool {
	return &MCPLoaderTool{
		manager:         manager,
		sessionExecutor: sessionExecutor,
	}
}

func (t *MCPLoaderTool) Name() string {
	name, _ := t.manager.serverInfo()
	return fmt.Sprintf("mcp_load_%s", name)
}

func (t *MCPLoaderTool) Description() string {
	_, description := t.manager.serverInfo()
	return fmt.Sprintf("加载 %s MCP 服务器的指定工具。加载后该工具将可用于后续调用。参数: tool_name - 要加载的工具名称（从 mcp_discover 获取）", description)
}

func (t *MCPLoaderTool) Params() any {
	return &MCPLoaderParams{}
}

func (t *MCPLoaderTool) Execute(ctx context.Context, params any, callbacks CallBackFuncs) (string, error) {
	p := params.(*MCPLoaderParams)

	tool, err := t.manager.LoadTool(p.ToolName)
	if err != nil {
		return "", err
	}

	// 注册到会话级执行器，不影响其他用户的会话
	t.sessionExecutor.RegisterSession(tool)
	log.Printf("[MCP] 加载工具到会话: %s", tool.Name())

	// 只返回加载成功的简短确认，工具定义通过 tools 字段传递给模型
	return fmt.Sprintf("工具 '%s' 已加载成功，现在可以直接调用该工具。", tool.Name()), nil
}

// 常用工具过滤器

// FilterByPrefix 创建按名称前缀过滤的过滤器
func FilterByPrefix(prefix string) ToolFilterFunc {
	return func(tool *mcp.Tool) bool {
		return strings.HasPrefix(tool.Name, prefix)
	}
}

// FilterByNames 创建按名称列表过滤的过滤器
func FilterByNames(names ...string) ToolFilterFunc {
	nameSet := make(map[string]bool)
	for _, name := range names {
		nameSet[name] = true
	}
	return func(tool *mcp.Tool) bool {
		return nameSet[tool.Name]
	}
}

// FilterByKeywords 创建按描述关键词过滤的过滤器
func FilterByKeywords(keywords ...string) ToolFilterFunc {
	return func(tool *mcp.Tool) bool {
		desc := strings.ToLower(tool.Description)
		for _, keyword := range keywords {
			if strings.Contains(desc, strings.ToLower(keyword)) {
				return true
			}
		}
		return false
	}
}

// CombineFilters 组合多个过滤器（AND 逻辑）
func CombineFilters(filters ...ToolFilterFunc) ToolFilterFunc {
	return func(tool *mcp.Tool) bool {
		for _, filter := range filters {
			if !filter(tool) {
				return false
			}
		}
		return true
	}
}

// AnyFilter 组合多个过滤器（OR 逻辑）
func AnyFilter(filters ...ToolFilterFunc) ToolFilterFunc {
	return func(tool *mcp.Tool) bool {
		for _, filter := range filters {
			if filter(tool) {
				return true
			}
		}
		return false
	}
}

// headerTransport 为 HTTP 请求注入自定义 headers
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 克隆请求避免修改原始对象
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

// newStreamHTTPClient 构建用于流式传输（Streamable/SSE）的 HTTP 客户端。
// 不设置整体 http.Client.Timeout：Go 文档明确该超时覆盖整个请求生命周期
// （含读取响应体），会中断长连接 SSE 流的持续读取——对 Streamable 会引发
// 频繁重连、超过超时时长的流式响应直接失败。改为在 Transport 层设置
// DialContext 与 ResponseHeaderTimeout，既保护握手阶段不无限挂起，又不影响
// 长连接的持续读取。
func newStreamHTTPClient(headers map[string]string) *http.Client {
	transport := &http.Transport{}
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
	}
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 30 * time.Second
	rt := http.RoundTripper(transport)
	if len(headers) > 0 {
		rt = &headerTransport{base: transport, headers: headers}
	}
	return &http.Client{Transport: rt}
}
