package functool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/oplog"
)

// 扩展配置（面板「扩展配置」页管理的 JSON 文件）对应的配置键。
// 与 configstore.Key* 常量、adminpanel fileKeys 同值；组件层不依赖 core 与面板，
// 按插件本地定义共享键的先例（pluginaichat 的 hooksConfigKey）在本包内声明。
var configFileKeys = map[string]string{
	"mcp":      "files.mcp_json",
	"prompt":   "files.prompt_json",
	"hooks":    "files.hooks_json",
	"commands": "files.commands_json",
}

// configFileDescs 各扩展配置文件的格式说明（供 AI 查看/编写内容）。
var configFileDescs = map[string]string{
	"mcp":      `MCP 服务器列表。格式: {"servers": [{"name": 名称, "transport": "stdio|streamable|sse", "command": 启动命令(stdio), "args": [参数], "env": {"K": "V"}, "endpoint": HTTP地址(streamable/sse), "headers": {"K": "V"}, "timeout": 秒, "description": 说明}]}。修改后重启生效`,
	"prompt":   `按群聊/好友覆盖 AI 系统提示词。格式: {"groups": {"群ID": "prompt"}, "friends": {"用户ID": "prompt"}}（统一带平台前缀，如 qq:123456 / fs:oc_xxx）。修改后立即生效`,
	"hooks":    `AI 钩子（会话事件上执行 shell 命令）。格式: {"hooks": {"事件名": [{"matcher": 工具名正则(可空), "command": shell命令, "timeout_sec": 秒(可空)}]}}。事件: SessionStart/UserPromptSubmit/PreToolUse/PostToolUse/Stop/SubagentStop/PreCompact；stdin 接收 JSON 载荷；退出码 0=通过(stdout 注入上下文)/2=阻断(stderr 为原因)/其他=仅记日志。保存后数秒内生效`,
	"commands": `自定义斜杠命令。格式: {"commands": {"命令名": "提示词模板"}}。模板中 $args 为用户参数占位符（无占位符时参数追加到末尾）；命令名字母开头、最长 32 字符，不得与内置命令撞名。保存后数秒内生效`,
}

// configFileList 全部扩展配置文件的说明（按文件名排序）。
func configFileList() string {
	names := make([]string, 0, len(configFileKeys))
	for n := range configFileKeys {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("可用扩展配置文件（config_file_get/config_file_set 的 name 取值）：\n")
	for _, n := range names {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", n, configFileDescs[n]))
	}
	return sb.String()
}

// ---- config_file_get：查看扩展配置 ----

type ConfigFileGetParams struct {
	Name string `json:"name,omitempty" desc:"扩展配置文件名：mcp/prompt/hooks/commands。留空则列出全部可用文件及格式说明"`
}

type ConfigFileGetTool struct {
	llmtool.BaseTool[ConfigFileGetParams]
	store ConfigStore
}

func NewConfigFileGetTool(store ConfigStore) *ConfigFileGetTool {
	return &ConfigFileGetTool{
		BaseTool: llmtool.MakeBaseTool("config_file_get", "查看扩展配置的原始 JSON 文本（面板「扩展配置」页管理的四个文件：MCP 服务器 mcp / Prompt 覆盖 prompt / AI 钩子 hooks / 自定义命令 commands）。留空 name 列出可用文件与格式说明", ConfigFileGetParams{}),
		store:    store,
	}
}

func (t *ConfigFileGetTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*ConfigFileGetParams)
	if !ok {
		return "", fmt.Errorf("config_file_get: 参数类型错误")
	}
	name := strings.ToLower(strings.TrimSpace(p.Name))
	log.Println("执行config_file_get... 参数:", name)
	if name == "" {
		return configFileList(), nil
	}
	key, ok := configFileKeys[name]
	if !ok {
		return "", fmt.Errorf("config_file_get: 未知扩展配置文件 %s（可用：mcp/prompt/hooks/commands，留空 name 查看说明）", name)
	}
	content := ""
	if v, ok := t.store.Get(key); ok {
		if s, ok := v.(string); ok {
			content = s
		}
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Sprintf("%s 当前为空。\n格式说明: %s", key, configFileDescs[name]), nil
	}
	return fmt.Sprintf("%s 当前内容：\n%s", key, content), nil
}

// ---- config_file_set：修改扩展配置 ----

type ConfigFileSetParams struct {
	Name    string `json:"name" desc:"扩展配置文件名：mcp/prompt/hooks/commands"`
	Content string `json:"content" desc:"新的完整 JSON 文本（与面板扩展配置页源码模式保存的格式一致）"`
}

type ConfigFileSetTool struct {
	llmtool.BaseTool[ConfigFileSetParams]
	store ConfigStore
}

func NewConfigFileSetTool(store ConfigStore) *ConfigFileSetTool {
	return &ConfigFileSetTool{
		BaseTool: llmtool.MakeBaseTool("config_file_set", "修改扩展配置（写入数据库，与面板「扩展配置」页保存等价）。只校验 JSON 语法，语义错误会导致对应功能加载失败——先用 config_file_get 查看当前内容与格式说明。该操作需要管理员审批：系统会直接私聊通知机器人管理员，管理员回复「允许」后才写入（超时未确认自动拒绝）。hooks/commands/prompt 保存后数秒内生效；mcp 重启后生效（重启需由管理员发送 /reboot 命令）", ConfigFileSetParams{}),
		store:    store,
	}
}

func (t *ConfigFileSetTool) Execute(ctx context.Context, params any, _ llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*ConfigFileSetParams)
	if !ok {
		return "", fmt.Errorf("config_file_set: 参数类型错误")
	}
	name := strings.ToLower(strings.TrimSpace(p.Name))
	key, ok := configFileKeys[name]
	if !ok {
		return "", fmt.Errorf("config_file_set: 未知扩展配置文件 %s（可用：mcp/prompt/hooks/commands）", name)
	}
	// 与面板保存一致：非空内容必须是合法 JSON
	if strings.TrimSpace(p.Content) != "" && !json.Valid([]byte(p.Content)) {
		return "", fmt.Errorf("config_file_set: 内容不是合法的 JSON（请检查引号/逗号/括号是否配对）")
	}
	log.Println("执行config_file_set... 文件:", name)

	if err := t.store.Set(key, p.Content); err != nil {
		return "", fmt.Errorf("config_file_set: 写入失败: %w", err)
	}
	oplog.Record(oplog.CategoryAI, "config_file_set", fmt.Sprintf("AI 修改扩展配置 %s（%d 字符）", key, len([]rune(p.Content))))

	// 与面板保存的提示保持一致：prompt/hooks/commands 由 AI 插件按 TTL 热重读，
	// 保存后数秒内生效；只有 mcp 需要重启（服务器在启动时连接）。
	effective := "保存后数秒内生效"
	if name == "mcp" {
		effective = "重启后生效，请提醒用户由管理员发送 /reboot 重启使配置生效"
	}
	return fmt.Sprintf("已更新扩展配置 %s，%s", key, effective), nil
}
