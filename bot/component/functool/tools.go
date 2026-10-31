package functool

import (
	"log"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/bot/component/llmtool/builtinskills"
)

// CreateDefaultTools 创建默认的工具执行器并注册所有内置工具。
// 注意：历史消息查看不再是内置工具——已平台工具化（aitool.NewMsgHistoryTool，
// 支持按群成员筛选），由 napcat/luckylilia/telegram 等适配器按平台支持情况注入。
// 联网搜索/浏览也不再内置：内置 web 技能提供 CLI（见 builtinskills），
// 由 AI 通过 skill_read + bash 按需调用。
func CreateDefaultTools(bashConfig BashConfig, fileConfig FileConfig, localImageConfig LocalImageConfig, fileToolsConfig FileToolsConfig) (*llmtool.ToolExecuter, error) {
	executer := llmtool.NewToolExecuter()
	executer.Register(NewTimeTool())
	if fileConfig.Enable {
		executer.Register(NewSendFileTool())
	}
	executer.Register(NewPrivateFileTool())
	executer.Register(NewLoadImagesTool())
	if localImageConfig.Enable {
		executer.Register(NewLoadLocalImageTool())
	}
	for _, tool := range NewFileTools(fileToolsConfig) {
		executer.Register(tool)
	}
	if bashConfig.Enable {
		bashTool, err := NewBashTool(bashConfig)
		if err != nil {
			return nil, err
		}
		executer.Register(bashTool)
	}
	return executer, nil
}

// CreateToolsWithMCP 创建工具执行器，注册内置工具和 MCP 工具。
// mcpLazyLoad 为 true 时走工具发现模式（mcp_discover/mcp_load 按需加载，
// 节省上下文）；为 false 时启动即全量注册所有 MCP 工具（工具列表恒定，
// 上游 prompt 缓存命中率更高，但工具较多时上下文开销大）。
func CreateToolsWithMCP(mcpConfigs []*llmtool.MCPConfig, bashConfig BashConfig, fileConfig FileConfig, localImageConfig LocalImageConfig, fileToolsConfig FileToolsConfig, mcpLazyLoad bool) (*llmtool.ToolExecuter, error) {
	executer, err := CreateDefaultTools(bashConfig, fileConfig, localImageConfig, fileToolsConfig)
	if err != nil {
		return nil, err
	}
	for _, config := range mcpConfigs {
		log.Printf("正在连接MCP服务器: %s (command=%s)", config.Name, config.Command)
		if mcpLazyLoad {
			err = executer.RegisterMCPWithConfigDiscovery(config)
		} else {
			err = executer.RegisterMCPWithConfig(config)
		}
		if err != nil {
			log.Printf("注册MCP服务器 %s 失败: %v", config.Name, err)
			continue
		}
		log.Printf("成功注册MCP服务器: %s", config.Name)
	}
	return executer, nil
}

// CreateToolsWithSkill 创建工具执行器，注册内置工具、MCP 工具和 Skill 工具。
// skillsDir 为空时跳过 skill 加载
// skills 非空时只加载指定名称的 skill（内置 skill 始终加载），为空时加载全部
// mcpLazyLoad 见 CreateToolsWithMCP
func CreateToolsWithSkill(mcpConfigs []*llmtool.MCPConfig, skillsDir string, bashConfig BashConfig, fileConfig FileConfig, localImageConfig LocalImageConfig, fileToolsConfig FileToolsConfig, skills []string, mcpLazyLoad bool) (*llmtool.ToolExecuter, *llmtool.SkillManager, error) {
	executer, err := CreateToolsWithMCP(mcpConfigs, bashConfig, fileConfig, localImageConfig, fileToolsConfig, mcpLazyLoad)
	if err != nil {
		return nil, nil, err
	}

	skillManager := llmtool.NewSkillManager()
	if skillsDir != "" {
		// 内置技能随二进制发布（物化到 <skillsDir>/.builtin，供 bash 执行其脚本），
		// 外置技能来自 skillsDir 下的普通目录；白名单与禁用只作用于外置技能。
		skillManager.EnableBuiltinSkills(builtinskills.FS, builtinskills.Root)
		if err := skillManager.LoadFromDirWithFilter(skillsDir, skills); err != nil {
			log.Printf("加载 skill 目录失败 [%s]: %v", skillsDir, err)
		} else {
			for _, m := range skillManager.List() {
				log.Printf("已加载 skill: %s (%s)", m.Name, m.Description)
			}
		}
	}
	skillManager.RegisterToExecuter(executer)

	return executer, skillManager, nil
}
