package pluginaichat

import (
	"context"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/aitool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 平台专属 AI 工具注入：事件来源适配器的 bot 外观实现 aitool.Provider 时
// （如 napcat/luckylilia 的 QQ 外观注入 AI 语音、戳一戳、群签到等工具），
// 会话创建时把其工具桥接注册进该会话的 SessionToolExecutor。
// 其他平台按同一契约扩展：在适配器包的外观包装器上实现 AITools 即可，
// 本插件无需改动；暂未接入的平台兜底注册通用历史消息工具（见下）。
// 工具最终序列化顺序由 ToolExecuter 按工具名排序统一兜底，
// 与注册顺序无关，保证 tools 字段逐请求稳定（上游 prompt 前缀缓存安全）。

// registerPlatformTools 向会话注册事件来源平台注入的工具。
// 外观未实现 Provider 的平台（暂未接入平台工具的，如飞书/Discord/QQ官方/微信）
// 兜底注册通用历史消息工具（读工具，建立在 bot.Bot 必备历史接口之上），
// 保持历史查看能力不因平台工具化回退。
// 重名防御：与内置共享工具同名、或本轮重复返回的工具一律跳过并记日志
// （会话层注册会静默覆盖同名工具，可能顶掉内置工具）。
// Logger 为 nil 时（部分测试场景）跳过日志。
func (p *AIChatPlugin) registerPlatformTools(sessionExecutor *llmtool.SessionToolExecutor, b bot.Bot, id message.QID, isGroup bool) {
	if b == nil {
		return
	}
	// 会话绑定信息：历史消息工具经 OnMessages 把拉取到的消息登记进本请求的
	// 图片注册表（registry 经请求上下文传递，见 processChatBatch / imageRegistryKey），
	// 使 load_images 能按哈希加载历史消息中的图片
	ctx := aitool.Context{
		Bot:     b,
		Target:  id,
		IsGroup: isGroup,
		OnMessages: func(rc context.Context, msgs []message.Message) {
			reg, ok := rc.Value(imageRegistryKey{}).(*imageRegistry)
			if !ok || reg == nil {
				return
			}
			registerMessageImages(reg, b, msgs...)
		},
	}
	provider, ok := b.(aitool.Provider)
	if !ok {
		if !p.toolExecutor.Has("get_msg_history") {
			sessionExecutor.RegisterSession(llmtool.AdaptProviderTool(
				aitool.NewMsgHistoryTool(ctx, "get_msg_history")))
		}
		return
	}
	skipLog := func(format string, args ...any) {
		if p.Logger != nil {
			p.Logger.Warn("平台工具注册已跳过: "+format, args...)
		}
	}
	seen := make(map[string]struct{})
	registered := 0
	for _, t := range provider.AITools(ctx) {
		if t == nil {
			continue
		}
		name := t.Name()
		if _, dup := seen[name]; dup {
			skipLog("平台工具 %s 重复返回", name)
			continue
		}
		seen[name] = struct{}{}
		// 判重必须覆盖共享层与会话层：clock_*/memory_*/kb_*/skill_*/mcp_*/todo_write/
		// subagent_*/team_* 都注册在会话层（RegisterSession 是裸赋值，会静默覆盖），
		// 只查共享层会让平台同名工具顶掉这些会话级工具（例如顶掉已绑定
		// g:<id> scope 的记忆工具）。
		if p.toolExecutor.Has(name) || sessionExecutor.HasSession(name) {
			skipLog("平台工具 %s 与内置/会话工具重名", name)
			continue
		}
		// 副作用声明（SideEffector）收集进插件级集合，供计划模式门禁按名阻断
		if se, isSide := t.(aitool.SideEffector); isSide && se.SideEffect() {
			p.platformSideEffects.Store(name, struct{}{})
		}
		sessionExecutor.RegisterSession(llmtool.AdaptProviderTool(t))
		registered++
	}
	if p.Logger != nil {
		p.Logger.Info("已注入平台专属工具", "target", id.String(), "is_group", isGroup, "count", registered)
	}
}

// platformToolBlocked 计划模式门禁的动态腿：平台注入的副作用工具
// （aitool.SideEffector 声明）与 planBlockedTools 静态清单同等阻断。
func (p *AIChatPlugin) platformToolBlocked(name string) bool {
	_, blocked := p.platformSideEffects.Load(name)
	return blocked
}
