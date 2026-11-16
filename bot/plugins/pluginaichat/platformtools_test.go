package pluginaichat

import (
	"context"

	"testing"

	"github.com/jeanhua/AniaBot/bot/component/agenthook"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/aitool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeProviderFacade 实现 aitool.Provider 的 bot 外观测试桩：返回 QQ 工具集
// （aitool.Provider 是外观包装器实现的可选接口，插件按此断言注入）。
type fakeProviderFacade struct {
	bot.Bot
	tools []aitool.Tool
}

func (f *fakeProviderFacade) AITools(ctx aitool.Context) []aitool.Tool { return f.tools }

// TestRegisterPlatformTools 注入主路径：Provider 外观的工具注册进会话执行器，
// 事件来源平台未实现 Provider 时兜底注册通用历史消息工具（nil 外观跳过）。
func TestRegisterPlatformTools(t *testing.T) {
	p := &AIChatPlugin{toolExecutor: llmtool.NewToolExecuter()}
	session := p.toolExecutor.NewSessionExecutor()

	facade := &fakeProviderFacade{tools: aitool.QQTools(
		aitool.Context{Target: message.QID("qq:888"), IsGroup: true}, nil)}
	p.registerPlatformTools(session, facade, message.FromUint64(888), true)

	names := sessionToolNames(session)
	for _, want := range []string{"qq_get_group_list", "qq_send_poke", "qq_send_ai_voice"} {
		if !names[want] {
			t.Errorf("平台工具 %s 未注入会话", want)
		}
	}
	if names["time"] {
		t.Error("不应注入平台清单之外的工具")
	}
	// 历史消息工具名按会话登记：播报提示据此给出平台前缀名而不是通用名
	if got := p.historyToolNameFor(sessionKey(message.FromUint64(888), true)); got != "qq_get_msg_history" {
		t.Errorf("应登记平台历史消息工具名, got %q", got)
	}
	if got := p.historyToolNameFor(sessionKey(message.FromUint64(999), true)); got != "get_msg_history" {
		t.Errorf("未登记的会话应回退通用工具名, got %q", got)
	}

	// nil 外观：不注册任何工具
	session2 := p.toolExecutor.NewSessionExecutor()
	p.registerPlatformTools(session2, nil, message.FromUint64(888), true)
	if len(sessionToolNames(session2)) != 0 {
		t.Error("nil 外观不应注册工具")
	}

	// 非 Provider 外观（暂未接入平台工具的平台）：兜底注册通用历史消息工具，
	// 保证历史查看能力不因平台工具化回退（建立在 bot.Bot 基础历史接口之上）
	session3 := p.toolExecutor.NewSessionExecutor()
	p.registerPlatformTools(session3, struct{ bot.Bot }{}, message.FromUint64(888), true)
	names3 := sessionToolNames(session3)
	if !names3["get_msg_history"] {
		t.Error("非 Provider 平台应兜底注册 get_msg_history")
	}
	if len(names3) != 1 {
		t.Errorf("非 Provider 平台只应兜底注册历史消息工具, got %v", names3)
	}
	if got := p.historyToolNameFor(sessionKey(message.FromUint64(888), true)); got != "get_msg_history" {
		t.Errorf("非 Provider 平台应登记兜底工具名, got %q", got)
	}
}

// TestRegisterPlatformToolsNameConflict 平台工具与内置共享工具同名时跳过注册，
// 防止会话层静默覆盖把内置工具顶掉。
func TestRegisterPlatformToolsNameConflict(t *testing.T) {
	p := &AIChatPlugin{toolExecutor: llmtool.NewToolExecuter()}
	p.toolExecutor.Register(llmtool.AdaptProviderTool(&namedTool{name: "time", desc: "builtin-time"}))
	session := p.toolExecutor.NewSessionExecutor()

	facade := &fakeProviderFacade{tools: []aitool.Tool{
		&namedTool{name: "time", desc: "platform-time"},
		&namedTool{name: "qq_send_poke", desc: "poke", sideEffect: true},
	}}
	p.registerPlatformTools(session, facade, message.FromUint64(888), true)

	// 合并列表中 time 只有一份，且仍是内置版本（平台版被跳过）
	var timeDesc string
	count := 0
	for _, def := range session.Tools() {
		if def.Function.Name == "time" {
			count++
			timeDesc = def.Function.Description
		}
	}
	if count != 1 {
		t.Fatalf("time 定义应只出现一次, got %d", count)
	}
	if timeDesc != "builtin-time" {
		t.Errorf("内置工具被平台工具覆盖: %s", timeDesc)
	}
	if !sessionToolNames(session)["qq_send_poke"] {
		t.Error("正常平台工具应注册")
	}
	// 副作用声明仍只对成功注册的工具收集
	if _, ok := p.platformSideEffects.Load("time"); ok {
		t.Error("被跳过的重名工具不应进入副作用集合")
	}
}

// TestPlanGateBlocksPlatformSideEffects 计划模式门禁的动态腿：平台注入的
// 副作用工具（SideEffector 声明）被阻断，只读工具放行；退出计划模式后恢复。
func TestPlanGateBlocksPlatformSideEffects(t *testing.T) {
	p := &AIChatPlugin{toolExecutor: llmtool.NewToolExecuter(), planManager: newPlanManager()}
	session := p.toolExecutor.NewSessionExecutor()
	facade := &fakeProviderFacade{tools: aitool.QQTools(
		aitool.Context{Target: message.QID("qq:888"), IsGroup: true}, nil)}
	p.registerPlatformTools(session, facade, message.FromUint64(888), true)

	const key = "g:888"
	gate := p.buildPreToolGate(key, agenthook.AgentKindMain, message.FromUint64(1), func(string) {}, nil)
	call := func(name string) bool {
		blocked, _ := gate(context.Background(), llmtool.ToolCall{Name: name, Arguments: "{}"})
		return blocked
	}

	// 未开启计划模式：全部放行
	if call("qq_send_poke") {
		t.Fatal("未开启计划模式时不应阻断")
	}

	p.planManager.Set(key, true)
	if !call("qq_send_poke") || !call("qq_send_ai_voice") || !call("qq_send_group_sign") {
		t.Error("计划模式下平台副作用工具应被阻断")
	}
	if call("qq_get_group_list") || call("qq_get_group_user_info") {
		t.Error("计划模式下平台只读工具不应被阻断")
	}

	p.planManager.Set(key, false)
	if call("qq_send_poke") {
		t.Error("退出计划模式后应恢复放行")
	}
}

// TestRegisterPlatformToolsStableToolsSerialization 注入后同一会话多轮请求的
// tools 列表稳定（缓存安全）；两个独立会话注入同一平台工具时列表一致。
func TestRegisterPlatformToolsStableToolsSerialization(t *testing.T) {
	p := &AIChatPlugin{toolExecutor: llmtool.NewToolExecuter()}

	inject := func() []llmtool.ToolDef {
		session := p.toolExecutor.NewSessionExecutor()
		facade := &fakeProviderFacade{tools: aitool.QQTools(
			aitool.Context{Target: message.QID("qq:888"), IsGroup: true}, nil)}
		p.registerPlatformTools(session, facade, message.FromUint64(888), true)
		return session.Tools()
	}

	first := inject()
	second := inject()
	if len(first) != len(second) {
		t.Fatalf("两次注入工具数不同: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Function.Name != second[i].Function.Name {
			t.Errorf("工具顺序不稳定: 第 %d 个 %s vs %s", i, first[i].Function.Name, second[i].Function.Name)
		}
	}
}

// sessionToolNames 收集会话执行器当前注册的工具名。
func sessionToolNames(s *llmtool.SessionToolExecutor) map[string]bool {
	names := map[string]bool{}
	for _, def := range s.Tools() {
		names[def.Function.Name] = true
	}
	return names
}

// namedTool 最小工具桩（名字/描述可控，用于重名冲突测试）。
type namedTool struct {
	name       string
	desc       string
	sideEffect bool
}

func (t *namedTool) Name() string        { return t.name }
func (t *namedTool) Description() string { return t.desc }
func (t *namedTool) Params() any         { return &struct{}{} }
func (t *namedTool) Execute(ctx context.Context, params any) (string, error) {
	return "ok", nil
}
func (t *namedTool) SideEffect() bool { return t.sideEffect }
