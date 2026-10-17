package pluginaichat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/functool"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

func TestPlanManagerOnOff(t *testing.T) {
	m := newPlanManager()
	const key = "g:1"
	if m.IsOn(key) {
		t.Fatal("默认应为关闭")
	}
	m.Set(key, true)
	if !m.IsOn(key) {
		t.Fatal("Set(true) 后应为开启")
	}
	m.Set(key, false)
	if m.IsOn(key) {
		t.Fatal("Set(false) 后应为关闭")
	}
	// 会话间隔离
	m.Set("g:1", true)
	if m.IsOn("g:2") || m.IsOn("f:1") {
		t.Fatal("计划模式应按会话隔离")
	}
}

// TestPlanGateBlocking 计划模式是门禁第一腿：开启时副作用工具被阻断并给出
// 计划提示，只读工具与 todo_write（规划工作流一部分）放行；关闭后恢复。
func TestPlanGateBlocking(t *testing.T) {
	p := &AIChatPlugin{planManager: newPlanManager()}
	const key = "g:1"
	gate := p.buildPreToolGate(key, agenthook.AgentKindMain, message.FromUint64(1), func(string) {}, nil)
	call := func(name string) (bool, string) {
		return gate(context.Background(), llmtool.ToolCall{Name: name, Arguments: "{}"})
	}

	if blocked, _ := call("bash"); blocked {
		t.Fatal("未开启计划模式时不应阻断 bash")
	}

	p.planManager.Set(key, true)
	blocked, result := call("bash")
	if !blocked || result == "" {
		t.Fatalf("计划模式下 bash 应被阻断并携带提示, blocked=%v result=%q", blocked, result)
	}
	for _, name := range []string{"time", "webSearch", "memory_search", "config_get", "todo_write"} {
		if blocked, _ := call(name); blocked {
			t.Fatalf("计划模式下只读/中性工具 %s 不应被阻断", name)
		}
	}

	p.planManager.Set(key, false)
	if blocked, _ := call("bash"); blocked {
		t.Fatal("退出计划模式后 bash 应恢复放行")
	}
}

// TestPlanGateBlocksMCPToolPrefixes 计划模式必须覆盖 MCP 的发现/加载入口。
//
// MCP 工具集合在运行时变化（服务器可增删、工具可懒加载），具体工具名无法静态
// 枚举；`mcp_load` / `mcp_discover` 是「执行任意 MCP 工具」的入口，若放行则
// 先 load 再调用即可完全绕过计划模式这道门禁。
func TestPlanGateBlocksMCPToolPrefixes(t *testing.T) {
	p := &AIChatPlugin{planManager: newPlanManager()}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	const key = "g:1"

	gate := p.buildPreToolGate(key, agenthook.AgentKindMain, message.FromUint64(1), func(string) {}, nil)
	call := func(name string) (bool, string) {
		return gate(context.Background(), llmtool.ToolCall{Name: name, Arguments: "{}"})
	}

	if blocked, _ := call("mcp_load_demo"); blocked {
		t.Fatal("未开启计划模式时不应阻断 mcp_load")
	}

	p.planManager.Set(key, true)
	for _, name := range []string{"mcp_load_demo", "mcp_discover_demo", "mcp_add", "mcp_remove", "mcp_reconnect"} {
		if blocked, _ := call(name); !blocked {
			t.Fatalf("计划模式下 MCP 入口工具 %s 必须被阻断（否则可 load 后执行任意副作用）", name)
		}
	}
	// 只读的 MCP 状态查询仍放行
	if blocked, _ := call("mcp_list"); blocked {
		t.Fatal("计划模式下 mcp_list（只读）不应被阻断")
	}
}

// TestIsMCPToolNameCoversRegisteredMCPTools 已注册的 MCP 工具实例（eager 模式在
// 共享层、懒加载模式在会话层）都必须被识别为 MCP 工具，门禁才能按名阻断。
func TestIsMCPToolNameCoversRegisteredMCPTools(t *testing.T) {
	exec := llmtool.NewToolExecuter()
	exec.Register(functool.NewSendFileTool())
	if exec.IsMCPToolName("file") {
		t.Fatal("内置工具不应被判为 MCP 工具")
	}
	if exec.IsMCPToolName("not_exist") {
		t.Fatal("不存在的工具不应被判为 MCP 工具")
	}
	session := exec.NewSessionExecutor()
	if session.IsMCPToolName("not_exist") {
		t.Fatal("会话层不存在的工具不应被判为 MCP 工具")
	}
	if session.HasSession("not_exist") {
		t.Fatal("会话层未注册时 HasSession 应为 false")
	}
}

// TestNewConversationRequestedOnlyOwnText #新对话 只认消息自身的文本段：
// 引用消息正文与合并转发内容（FriendlyText 会展开）不得触发历史清空。
func TestNewConversationRequestedOnlyOwnText(t *testing.T) {
	textMsg := func(s string) message.Message {
		return message.Message{
			Message: []message.OB11Segment{
				{Type: message.SegmentText, Data: map[string]any{"text": s}},
			},
		}
	}
	// 引用一条"含 #新对话 的旧消息"：reply 段 + 自身普通文本
	quoted := message.Message{
		Message: []message.OB11Segment{
			{Type: message.SegmentReply, Data: map[string]any{"id": "qq:1"}},
			{Type: message.SegmentText, Data: map[string]any{"text": "你看这条 #新对话 是干嘛的"}},
		},
	}

	cases := []struct {
		name  string
		batch []message.Message
		want  bool
	}{
		{"自身文本含标记", []message.Message{textMsg("#新对话")}, true},
		{"自身文本含标记（混在句子里）", []message.Message{textMsg("帮我 #新对话 一下")}, true},
		{"普通消息", []message.Message{textMsg("你好")}, false},
		// reply 段本身不含 text，其正文由 FriendlyText 展开——不在本判定范围内
		{"仅引用段（正文在展开里）", []message.Message{
			{Message: []message.OB11Segment{{Type: message.SegmentReply, Data: map[string]any{"id": "qq:1"}}}},
		}, false},
		{"引用+自身普通文本", []message.Message{quoted}, true}, // 自身文本里确实有该标记
		{"批量中任一条命中", []message.Message{textMsg("你好"), textMsg("#新对话")}, true},
		{"批量全不命中", []message.Message{textMsg("你好"), textMsg("在吗")}, false},
		{"非文本段", []message.Message{
			{Message: []message.OB11Segment{{Type: message.SegmentImage, Data: map[string]any{"file": "x"}}}},
		}, false},
		{"空批次", nil, false},
	}
	for _, c := range cases {
		if got := newConversationRequested(c.batch); got != c.want {
			t.Errorf("%s: newConversationRequested = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestNewConversationIgnoresForwardContent 合并转发节点内的 #新对话 不应触发清空：
// FriendlyText 会展开转发内容，旧实现按渲染文本判定，转发一条含该字样的消息
// 就会误清空历史（并卸载动态 MCP 工具）。
func TestNewConversationIgnoresForwardContent(t *testing.T) {
	var inner []message.Message
	inner = append(inner, message.Message{
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": "#新对话"}},
		},
	})
	content, _ := json.Marshal(inner)

	forward := message.Message{
		Message: []message.OB11Segment{
			{Type: message.SegmentForward, Data: map[string]any{"content": string(content)}},
		},
	}
	if newConversationRequested([]message.Message{forward}) {
		t.Fatal("合并转发内容里的 #新对话 不应触发历史清空")
	}
}

// TestPlanBlockedToolsAreRealToolNames 阻断清单成员必须与真实注册的工具名一致，
// 防止工具改名后清单静默失效（清单里出现幽灵名字=计划模式漏拦）。
func TestPlanBlockedToolsAreRealToolNames(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.PersistentStorage = newPFake()

	names := map[string]bool{}
	collect := func(tools []llmtool.Tool) {
		for _, tool := range tools {
			names[tool.Name()] = true
		}
	}

	// functool 基础工具（仅需名字，nil 存储不影响 Name()）
	bashTool, err := functool.NewBashTool(functool.BashConfig{})
	if err != nil {
		t.Fatalf("NewBashTool: %v", err)
	}
	names[bashTool.Name()] = true
	names[functool.NewSendFileTool().Name()] = true
	names[functool.NewConfigSetTool(nil).Name()] = true
	names[functool.NewConfigGetTool(nil).Name()] = true
	names[functool.NewConfigFileSetTool(nil).Name()] = true
	names[functool.NewConfigFileGetTool(nil).Name()] = true

	// 文件读写工具组（与 Start 中按 file_tools.enable 注册的来源一致，此处只取名字）
	for _, tool := range functool.NewFileTools(functool.FileToolsConfig{Enable: true}) {
		names[tool.Name()] = true
	}

	// 电脑操作工具（与 Start 中按 computer_use.enable 注册的来源一致，
	// 仅在 Windows 宿主机可用，此处只取名字）
	for _, tool := range functool.NewComputerUseTools(functool.ComputerUseConfig{}) {
		names[tool.Name()] = true
	}

	// 会话级工具（与 getChat/registerScopedTools 的注册来源一致）
	collect(newMemoryTools(newTestMemoryManager(0), "g:1", ""))
	collect(newKnowledgeTools(newTestKnowledgeManager(0), "g:1", ""))
	collect(newClockTools(newClockManager(p, time.Second, 10), clockTargetGroup, "1"))
	collect(newSkillTools(newTestSkillPlugin(t)))
	collect(newMCPTools(newMCPToolPlugin(t)))
	collect(newSubagentTools(p, nil, message.FromUint64(1), true))
	collect(newTeamTools(p, nil, message.FromUint64(1), true))

	for name := range planBlockedTools {
		if !names[name] {
			t.Errorf("planBlockedTools 中的 %q 不是真实注册的工具名", name)
		}
	}
	// todo_write 是规划工作流的一部分，刻意不在阻断清单中
	if _, blocked := planBlockedTools["todo_write"]; blocked {
		t.Error("todo_write 不应被计划模式阻断")
	}
}
