package pluginaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// 自定义斜杠命令：管理员把「/名 参数」映射为一段提示词模板（$args 为参数占位符），
// 存储在 files.commands_json（面板「扩展配置」页可直接编辑 JSON）。命中后消息被
// 改写为展开后的纯文本，走正常 AI 对话流程（排队/批处理/知识库/记忆注入不变）。

// customCommandNamePattern 命令名约束：字母开头，字母/数字/下划线/连字符，最长 32。
var customCommandNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,31}$`)

// builtinCommandNames 内置命令名：自定义命令不得撞名（否则内置命令被静默改写）。
var builtinCommandNames = map[string]struct{}{
	"clock": {}, "stop": {}, "plan": {}, "cmd": {},
	"help": {}, "reboot": {}, "exit": {}, "news": {},
	"explore": {}, "close": {}, "enable": {},
}

const (
	customCommandMaxTemplateRunes = 2000
	customCommandReloadTTL        = 5 * time.Second
)

// customCommandsFile files.commands_json 的 JSON schema。
type customCommandsFile struct {
	Commands map[string]string `json:"commands"`
}

// commandManager 自定义命令管理器：5s TTL 重读配置中心，raw 变化才重新解析
// （面板编辑秒级热生效；消息路径上的读取有界）。并发安全。
type commandManager struct {
	editor    plugin.ConfigEditor
	configKey string
	logger    *slog.Logger

	mu        sync.Mutex
	commands  map[string]string
	lastRaw   string
	lastCheck time.Time
}

func newCommandManager(editor plugin.ConfigEditor, configKey string, logger *slog.Logger) *commandManager {
	return &commandManager{editor: editor, configKey: configKey, logger: logger, commands: map[string]string{}}
}

// lookup 按名查找命令模板；每次都过 TTL 重读（面板热生效）。
func (m *commandManager) lookup(name string) (string, bool) {
	m.maybeReload()
	m.mu.Lock()
	defer m.mu.Unlock()
	tpl, ok := m.commands[strings.ToLower(name)]
	return tpl, ok
}

// list 返回排序后的命令名列表（/cmd list 用）。
func (m *commandManager) list() []string {
	m.maybeReload()
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.commands))
	for name := range m.commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *commandManager) maybeReload() {
	m.mu.Lock()
	if time.Since(m.lastCheck) < customCommandReloadTTL {
		m.mu.Unlock()
		return
	}
	m.lastCheck = time.Now()
	m.mu.Unlock()

	if m.editor == nil {
		return
	}
	v, ok := m.editor.Get(m.configKey)
	if !ok {
		return
	}
	raw, _ := v.(string)

	m.mu.Lock()
	defer m.mu.Unlock()
	if raw == m.lastRaw {
		return // 内容没变不重新解析
	}
	fresh, err := parseCustomCommands(raw)
	if err != nil {
		m.logger.Error("解析自定义命令配置失败，沿用旧配置", "error", err.Error())
		return
	}
	m.lastRaw = raw
	m.commands = fresh
}

// parseCustomCommands 解析并校验配置；任一条目非法则整体拒绝（避免面板写错一半生效）。
func parseCustomCommands(raw string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	var fileCfg customCommandsFile
	if err := json.Unmarshal([]byte(raw), &fileCfg); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	for name, tpl := range fileCfg.Commands {
		name = strings.ToLower(strings.TrimSpace(name))
		if err := validateCustomCommand(name, tpl); err != nil {
			return nil, fmt.Errorf("命令 %q: %w", name, err)
		}
		out[name] = tpl
	}
	return out, nil
}

// validateCustomCommand 校验单条命令（名称合法、不撞内置、模板非空且不超长）。
func validateCustomCommand(name, tpl string) error {
	if !customCommandNamePattern.MatchString(name) {
		return fmt.Errorf("名称非法（字母开头，字母/数字/_/-，最长 32 字符）")
	}
	if _, clash := builtinCommandNames[name]; clash {
		return fmt.Errorf("与内置命令 /%s 撞名", name)
	}
	if strings.TrimSpace(tpl) == "" {
		return fmt.Errorf("模板不能为空")
	}
	if utf8.RuneCountInString(tpl) > customCommandMaxTemplateRunes {
		return fmt.Errorf("模板超过 %d 字上限", customCommandMaxTemplateRunes)
	}
	return nil
}

// add 新增/覆盖一条命令（校验后整体写回配置中心，落库持久化）。
func (m *commandManager) add(name, tpl string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if err := validateCustomCommand(name, tpl); err != nil {
		return err
	}
	if m.editor == nil {
		return fmt.Errorf("配置中心不可用")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	fileCfg := customCommandsFile{Commands: map[string]string{}}
	if v, ok := m.editor.Get(m.configKey); ok {
		if raw, _ := v.(string); strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &fileCfg); err != nil {
				return fmt.Errorf("现有配置损坏，请先在面板修正: %w", err)
			}
		}
	}
	if fileCfg.Commands == nil {
		fileCfg.Commands = map[string]string{}
	}
	fileCfg.Commands[name] = tpl
	return m.writeLocked(&fileCfg)
}

// del 删除一条命令。
func (m *commandManager) del(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if m.editor == nil {
		return fmt.Errorf("配置中心不可用")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	fileCfg := customCommandsFile{Commands: map[string]string{}}
	if v, ok := m.editor.Get(m.configKey); ok {
		if raw, _ := v.(string); strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &fileCfg); err != nil {
				return fmt.Errorf("现有配置损坏，请先在面板修正: %w", err)
			}
		}
	}
	if _, ok := fileCfg.Commands[name]; !ok {
		return fmt.Errorf("命令 /%s 不存在", name)
	}
	delete(fileCfg.Commands, name)
	return m.writeLocked(&fileCfg)
}

// writeLocked 写回配置中心并立即更新内存快照（调用方须持 m.mu）。
func (m *commandManager) writeLocked(fileCfg *customCommandsFile) error {
	data, err := json.MarshalIndent(fileCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}
	if err := m.editor.Set(m.configKey, string(data)); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", m.configKey, err)
	}
	m.lastRaw = string(data)
	m.lastCheck = time.Now()
	m.commands = fileCfg.Commands
	return nil
}

// expandCustomCommand 展开模板：$args 替换为全部参数（空格连接）；
// 无占位符且有参数时把参数追加到末尾。
func expandCustomCommand(tpl string, args []string) string {
	joined := strings.Join(args, " ")
	if strings.Contains(tpl, "$args") {
		return strings.ReplaceAll(tpl, "$args", joined)
	}
	if joined != "" {
		return tpl + "\n" + joined
	}
	return tpl
}

// rewriteCustomCommand 命中自定义命令时把消息改写为展开后的单文本段
// （保留 Sender/GroupId/MessageId 等元信息），返回是否命中。
func (m *commandManager) rewriteCustomCommand(cmd command.Command, msg *message.Message) bool {
	if cmd.Name == "" {
		return false
	}
	tpl, ok := m.lookup(cmd.Name)
	if !ok {
		return false
	}
	text := expandCustomCommand(tpl, cmd.Args)
	msg.Message = []message.OB11Segment{{
		Type: "text",
		Data: map[string]any{"text": text},
	}}
	msg.RawMessage = text
	return true
}

// handleCmdCommand 处理 /cmd 命令：自定义斜杠命令的管理入口。
//
// 用法：
//
//	/cmd                      列出自定义命令
//	/cmd add <名> <模板>      新增/覆盖（仅管理员；模板中 $args 为参数占位符）
//	/cmd del <名>             删除（仅管理员）
func (p *AIChatPlugin) handleCmdCommand(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	isGroup := msg.GroupId != ""
	id := msg.Sender.UserId
	if isGroup {
		id = msg.GroupId
	}
	if p.commandManager == nil {
		p.sendPlainText(b, id, isGroup, "自定义命令功能未就绪")
		return false, nil
	}

	sub := ""
	if len(cmd.Args) > 0 {
		sub = strings.ToLower(cmd.Args[0])
	}

	switch sub {
	case "", "list":
		names := p.commandManager.list()
		if len(names) == 0 {
			p.sendPlainText(b, id, isGroup, "当前没有自定义命令。管理员可用 /cmd add <名> <模板> 添加，模板中 $args 为参数占位符")
			return false, nil
		}
		var sb strings.Builder
		sb.WriteString("自定义命令（共 " + fmt.Sprint(len(names)) + " 个）：\n")
		for _, name := range names {
			sb.WriteString("/" + name + "\n")
		}
		p.sendPlainText(b, id, isGroup, strings.TrimRight(sb.String(), "\n"))
	case "add":
		if !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
			p.sendPlainText(b, id, isGroup, "仅管理员可添加自定义命令")
			return false, nil
		}
		if len(cmd.Args) < 3 {
			p.sendPlainText(b, id, isGroup, "用法：/cmd add <名> <模板>（模板中 $args 为参数占位符；多行模板请在面板的扩展配置页编辑 files.commands_json）")
			return false, nil
		}
		name := cmd.Args[1]
		tpl := strings.TrimSpace(strings.Join(cmd.Args[2:], " "))
		if err := p.commandManager.add(name, tpl); err != nil {
			p.sendPlainText(b, id, isGroup, "添加失败："+err.Error())
			return false, nil
		}
		p.sendPlainText(b, id, isGroup, "已添加自定义命令 /"+strings.ToLower(name))
	case "del", "delete", "rm":
		if !p.SystemConfig.IsAdmin(msg.Sender.UserId) {
			p.sendPlainText(b, id, isGroup, "仅管理员可删除自定义命令")
			return false, nil
		}
		if len(cmd.Args) < 2 {
			p.sendPlainText(b, id, isGroup, "用法：/cmd del <名>")
			return false, nil
		}
		if err := p.commandManager.del(cmd.Args[1]); err != nil {
			p.sendPlainText(b, id, isGroup, "删除失败："+err.Error())
			return false, nil
		}
		p.sendPlainText(b, id, isGroup, "已删除自定义命令 /"+strings.ToLower(cmd.Args[1]))
	default:
		p.sendPlainText(b, id, isGroup, "用法：/cmd 列出，/cmd add <名> <模板> 添加，/cmd del <名> 删除（后两者仅管理员）")
	}
	return false, nil
}
