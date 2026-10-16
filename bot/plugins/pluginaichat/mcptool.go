package pluginaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/oplog"
)

// MCP 管理工具：让 AI 无需后台面板即可自行添加/删除/重连/查看 MCP 服务器。
// 由 plugin.ai_chat_bot.mcp_tool.enable 门控（默认关闭）。添加/删除会写入
// files.mcp_json 持久化（经 DI 注入的 ConfigEditor），同时在运行时热注册/注销，
// 立即生效、无需重启；重连只影响运行时连接，不改配置。

// mcpServerNamePattern 服务器名称校验：名称会拼进发现/加载工具名
// （mcp_discover_<name> / mcp_load_<name>），必须满足 LLM 工具名规范
var mcpServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// mcpToolBase 为 MCP 管理工具共享插件引用。
type mcpToolBase struct {
	plugin *AIChatPlugin
}

// newMCPTools 创建 MCP 管理工具（mcp_list / mcp_add / mcp_remove / mcp_reconnect），
// 注册到会话执行器中（主会话与子代理一致），仅在配置开启时被 registerScopedTools 调用。
func newMCPTools(p *AIChatPlugin) []llmtool.Tool {
	base := mcpToolBase{plugin: p}
	return []llmtool.Tool{
		&mcpListTool{
			BaseTool:    llmtool.MakeBaseTool("mcp_list", "列出当前配置的所有 MCP 服务器及其运行状态（连接情况、工具数量、加载模式）。当用户提到 MCP 服务器、或你需要确认某个 MCP 服务器是否可用时调用", mcpListParams{}),
			mcpToolBase: base,
		},
		&mcpAddTool{
			BaseTool:    llmtool.MakeBaseTool("mcp_add", "添加新的 MCP 服务器：立即连接并注册其工具（即时生效），同时写入配置持久化（重启后保留）。支持 stdio 本地命令（command+args）与 streamable/sse HTTP 端点（endpoint）两种模式。添加后懒加载模式下用 mcp_discover_<名称> 查看其工具、mcp_load_<名称> 加载使用。仅在用户明确要求添加 MCP 服务器时使用", mcpAddParams{}),
			mcpToolBase: base,
		},
		&mcpRemoveTool{
			BaseTool:    llmtool.MakeBaseTool("mcp_remove", "删除指定的 MCP 服务器：断开连接并注销其全部工具（即时生效），同时从持久化配置中移除。名称用 mcp_list 查看。仅在用户明确要求删除 MCP 服务器时使用", mcpRemoveParams{}),
			mcpToolBase: base,
		},
		&mcpReconnectTool{
			BaseTool:    llmtool.MakeBaseTool("mcp_reconnect", "重新连接指定的 MCP 服务器并刷新其工具列表。当某个 MCP 服务重启过、工具列表有更新、或调用其工具持续报连接错误时使用。重连后会话内此前加载的该服务器工具失效，需重新 mcp_load", mcpReconnectParams{}),
			mcpToolBase: base,
		},
	}
}

// ---- mcp_list ----

type mcpListParams struct{}

type mcpListTool struct {
	llmtool.BaseTool[mcpListParams]
	mcpToolBase
}

func (t *mcpListTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	fileCfg, err := t.plugin.readMCPFileConfig()
	if err != nil {
		return "", fmt.Errorf("读取 MCP 配置失败: %w", err)
	}
	runtimeInfos := make(map[string]llmtool.MCPServerInfo)
	for _, info := range t.plugin.toolExecutor.MCPServerInfos() {
		runtimeInfos[info.Name] = info
	}

	if len(fileCfg.Servers) == 0 && len(runtimeInfos) == 0 {
		return "当前没有配置任何 MCP 服务器。可用 mcp_add 添加（stdio 本地命令或 streamable/sse HTTP 端点）。", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "MCP 服务器列表（配置 %d 个）：\n", len(fileCfg.Servers))
	for _, entry := range fileCfg.Servers {
		fmt.Fprintf(&sb, "- %s", entry.Name)
		if entry.Description != "" {
			fmt.Fprintf(&sb, "：%s", entry.Description)
		}
		sb.WriteString("（")
		if strings.ToLower(entry.Transport) == "streamable" || strings.ToLower(entry.Transport) == "streamable-http" || strings.ToLower(entry.Transport) == "sse" {
			fmt.Fprintf(&sb, "%s %s", entry.Transport, entry.Endpoint)
		} else {
			fmt.Fprintf(&sb, "stdio %s %s", entry.Command, strings.Join(entry.Args, " "))
		}
		if info, ok := runtimeInfos[entry.Name]; ok {
			mode := "懒加载"
			if !info.Lazy {
				mode = "全量注册"
			}
			fmt.Fprintf(&sb, "；已连接，%d 个工具，%s", info.ToolCount, mode)
		} else {
			sb.WriteString("；未连接（启动时连接失败或已被移除，可用 mcp_reconnect 重试）")
		}
		sb.WriteString("）\n")
	}
	return sb.String(), nil
}

// ---- mcp_add ----

type mcpAddParams struct {
	Name        string   `json:"name" desc:"服务器名称（唯一标识，仅限字母/数字/下划线/连字符，如 github、filesystem）"`
	Transport   string   `json:"transport,omitempty" desc:"传输类型：stdio（默认，本地命令）、streamable（HTTP）、sse"`
	Command     string   `json:"command,omitempty" desc:"stdio 模式的启动命令，如 npx、uvx、python"`
	Args        []string `json:"args,omitempty" desc:"stdio 模式的命令参数，每个元素一个参数，如 [\"-y\", \"@modelcontextprotocol/server-filesystem\", \"/tmp\"]"`
	Env         []string `json:"env,omitempty" desc:"stdio 模式附加环境变量，KEY=VALUE 格式，如 [\"API_KEY=xxx\"]"`
	Endpoint    string   `json:"endpoint,omitempty" desc:"streamable/sse 模式的 HTTP 端点 URL"`
	Headers     []string `json:"headers,omitempty" desc:"HTTP 请求头，KEY=VALUE 格式，如 [\"Authorization=Bearer xxx\"]"`
	TimeoutSec  int      `json:"timeout_sec,omitempty" desc:"连接超时秒数（默认 120）"`
	Description string   `json:"description,omitempty" desc:"服务器功能描述，帮助你与后续对话理解何时使用它"`
}

type mcpAddTool struct {
	llmtool.BaseTool[mcpAddParams]
	mcpToolBase
}

func (t *mcpAddTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p := params.(*mcpAddParams)
	entry, mcpConfig, err := p.toServerEntry()
	if err != nil {
		return "", err
	}
	if t.plugin.ConfigEditor == nil {
		return "", fmt.Errorf("配置中心不可用（持久化存储异常？），无法持久化 MCP 配置")
	}

	// 冲突检查：配置或运行时中已存在同名服务器
	fileCfg, err := t.plugin.readMCPFileConfig()
	if err != nil {
		return "", fmt.Errorf("读取 MCP 配置失败: %w", err)
	}
	for _, existing := range fileCfg.Servers {
		if existing.Name == entry.Name {
			return "", fmt.Errorf("MCP 服务器 '%s' 已存在于配置中（如需更新请先 mcp_remove 再添加）", entry.Name)
		}
	}

	// 先连接注册（失败则不落配置），再持久化（失败则回滚运行时注册，保持一致）
	if err := t.plugin.toolExecutor.AddMCP(ctx, mcpConfig, t.plugin.cfg.MCP.LazyLoad); err != nil {
		return "", err
	}
	fileCfg.Servers = append(fileCfg.Servers, entry)
	if err := t.plugin.writeMCPFileConfig(fileCfg); err != nil {
		_ = t.plugin.toolExecutor.RemoveMCP(entry.Name)
		return "", fmt.Errorf("MCP 服务器已连接，但写入持久化配置失败（已回滚运行时注册）: %w", err)
	}

	oplog.Record(oplog.CategoryAI, "mcp_add", fmt.Sprintf("AI 添加 MCP 服务器 %s", entry.Name))
	t.plugin.Logger.Info("AI 添加 MCP 服务器", "name", entry.Name, "transport", entry.Transport)
	return fmt.Sprintf("MCP 服务器「%s」已添加并连接成功，配置已持久化。可用 mcp_discover_%s 查看其工具，mcp_load_%s 加载需要的工具。", entry.Name, entry.Name, entry.Name), nil
}

// toServerEntry 校验参数并转换为持久化条目与运行时配置
func (p *mcpAddParams) toServerEntry() (*mcpServerEntry, *llmtool.MCPConfig, error) {
	name := strings.TrimSpace(p.Name)
	if !mcpServerNamePattern.MatchString(name) {
		return nil, nil, fmt.Errorf("名称 '%s' 不合法：仅限 1-64 位字母/数字/下划线/连字符", p.Name)
	}
	transport := strings.ToLower(strings.TrimSpace(p.Transport))
	if transport == "" {
		transport = "stdio"
	}

	entry := &mcpServerEntry{
		Name:        name,
		Transport:   transport,
		Description: strings.TrimSpace(p.Description),
	}
	mcpConfig := &llmtool.MCPConfig{
		Name:        name,
		Transport:   transport,
		Description: entry.Description,
	}

	switch transport {
	case "streamable", "streamable-http", "sse":
		endpoint := strings.TrimSpace(p.Endpoint)
		if endpoint == "" {
			return nil, nil, fmt.Errorf("%s 模式必须提供 endpoint（HTTP 端点 URL）", transport)
		}
		entry.Endpoint = endpoint
		mcpConfig.Endpoint = endpoint
		if headers := parseKeyValueList(p.Headers); len(headers) > 0 {
			entry.Headers = headers
			mcpConfig.Headers = headers
		}
	case "stdio":
		command := strings.TrimSpace(p.Command)
		if command == "" {
			return nil, nil, fmt.Errorf("stdio 模式必须提供 command（启动命令）")
		}
		entry.Command = command
		entry.Args = p.Args
		mcpConfig.Command = command
		mcpConfig.Args = p.Args
		if env := parseKeyValueList(p.Env); len(env) > 0 {
			entry.Env = env
			mcpConfig.Env = env
		}
	default:
		return nil, nil, fmt.Errorf("未知传输类型 '%s'（可选 stdio / streamable / sse）", p.Transport)
	}

	if p.TimeoutSec > 0 {
		entry.TimeoutSecs = p.TimeoutSec
		mcpConfig.Timeout = time.Duration(p.TimeoutSec) * time.Second
	}
	return entry, mcpConfig, nil
}

// parseKeyValueList 解析 ["KEY=VALUE", ...] 列表为 map（忽略无等号的非法项）
func parseKeyValueList(list []string) map[string]string {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]string, len(list))
	for _, item := range list {
		k, v, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		out[strings.TrimSpace(k)] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---- mcp_remove ----

type mcpRemoveParams struct {
	Name string `json:"name" desc:"要删除的 MCP 服务器名称（mcp_list 可查看）"`
}

type mcpRemoveTool struct {
	llmtool.BaseTool[mcpRemoveParams]
	mcpToolBase
}

func (t *mcpRemoveTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p := params.(*mcpRemoveParams)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return "", fmt.Errorf("name 不能为空")
	}
	if t.plugin.ConfigEditor == nil {
		return "", fmt.Errorf("配置中心不可用（持久化存储异常？），无法持久化 MCP 配置")
	}

	fileCfg, err := t.plugin.readMCPFileConfig()
	if err != nil {
		return "", fmt.Errorf("读取 MCP 配置失败: %w", err)
	}
	kept := fileCfg.Servers[:0]
	found := false
	for _, entry := range fileCfg.Servers {
		if entry.Name == name {
			found = true
			continue
		}
		kept = append(kept, entry)
	}
	if !found {
		return "", fmt.Errorf("MCP 服务器 '%s' 不存在（mcp_list 可查看已配置的服务器）", name)
	}
	fileCfg.Servers = kept

	// 先持久化（配置是唯一事实来源），再注销运行时；运行时未注册
	// （如启动时连接失败）时忽略注销错误，保证配置层面删除总是可用
	if err := t.plugin.writeMCPFileConfig(fileCfg); err != nil {
		return "", fmt.Errorf("写入持久化配置失败: %w", err)
	}
	if err := t.plugin.toolExecutor.RemoveMCP(name); err != nil {
		t.plugin.Logger.Warn("运行时移除 MCP 服务器失败（配置已删除，重启后完全生效）", "name", name, "error", err.Error())
	}

	oplog.Record(oplog.CategoryAI, "mcp_remove", fmt.Sprintf("AI 删除 MCP 服务器 %s", name))
	t.plugin.Logger.Info("AI 删除 MCP 服务器", "name", name)
	return fmt.Sprintf("MCP 服务器「%s」已删除：连接已断开、工具已注销、配置已移除。", name), nil
}

// ---- mcp_reconnect ----

type mcpReconnectParams struct {
	Name string `json:"name" desc:"要重连的 MCP 服务器名称（mcp_list 可查看）"`
}

type mcpReconnectTool struct {
	llmtool.BaseTool[mcpReconnectParams]
	mcpToolBase
}

func (t *mcpReconnectTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p := params.(*mcpReconnectParams)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return "", fmt.Errorf("name 不能为空")
	}
	reconnectCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := t.plugin.reconnectMCPServer(reconnectCtx, name); err != nil {
		return "", err
	}
	oplog.Record(oplog.CategoryAI, "mcp_reconnect", fmt.Sprintf("AI 重连 MCP 服务器 %s", name))
	t.plugin.Logger.Info("AI 重连 MCP 服务器", "name", name)
	return fmt.Sprintf("MCP 服务器「%s」已重连并刷新工具列表。本会话内此前加载的该服务器工具已失效，请用 mcp_load_%s 重新加载。", name, name), nil
}

// reconnectMCPServer 重连指定 MCP 服务器；若服务器因启动时连接失败从未注册，
// 则从持久化配置读取其定义并重新注册（懒加载/全量模式跟随当前配置）。
func (p *AIChatPlugin) reconnectMCPServer(ctx context.Context, name string) error {
	err := p.toolExecutor.ReconnectMCP(ctx, name)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "未注册") {
		return err
	}
	// 运行时未注册：尝试从配置恢复（启动时连接失败的服务器）
	fileCfg, cfgErr := p.readMCPFileConfig()
	if cfgErr != nil {
		return err
	}
	for _, entry := range fileCfg.Servers {
		if entry.Name != name {
			continue
		}
		mcpConfig, convErr := mcpEntryToConfig(entry)
		if convErr != nil {
			return fmt.Errorf("MCP 服务器 '%s' 配置无效: %w", name, convErr)
		}
		return p.toolExecutor.AddMCP(ctx, mcpConfig, p.cfg.MCP.LazyLoad)
	}
	return err
}

// ---- files.mcp_json 持久化读写 ----

// readMCPFileConfig 从配置中心读取 MCP 服务器配置；配置中心不可用或为空时返回空配置。
func (p *AIChatPlugin) readMCPFileConfig() (*mcpFileConfig, error) {
	if p.ConfigEditor == nil {
		return &mcpFileConfig{}, nil
	}
	v, ok := p.ConfigEditor.Get(mcpConfigKey)
	if !ok {
		return &mcpFileConfig{}, nil
	}
	raw, ok := v.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return &mcpFileConfig{}, nil
	}
	var fileCfg mcpFileConfig
	if err := json.Unmarshal([]byte(raw), &fileCfg); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", mcpConfigKey, err)
	}
	return &fileCfg, nil
}

// writeMCPFileConfig 将 MCP 服务器配置写回配置中心（JSON 文本落库，重启后保留）。
func (p *AIChatPlugin) writeMCPFileConfig(fileCfg *mcpFileConfig) error {
	if p.ConfigEditor == nil {
		return fmt.Errorf("配置中心不可用")
	}
	data, err := json.MarshalIndent(fileCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 MCP 配置失败: %w", err)
	}
	if err := p.ConfigEditor.Set(mcpConfigKey, string(data)); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", mcpConfigKey, err)
	}
	return nil
}
