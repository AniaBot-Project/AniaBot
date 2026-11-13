package pluginaichat

import (
	"fmt"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// 面板「试聊室」接口实现（adminpanel.ChatConsoleSource）：把面板的 HTTP 请求
// 转交给 chatConsoleManager。管理器在未就绪（如插件未完成 Start）时为 nil，
// 各方法退化为空列表 / 明确错误，面板无需感知。

// chatConsoleNotReady 返回试聊不可用时的原因说明（插件尚未完成初始化）。
func (p *AIChatPlugin) chatConsoleNotReady() error {
	if !p.ready {
		return fmt.Errorf("AI 对话未就绪：请先在「配置管理」里填写模型 base_url / api_key / model，重启 Bot 后再试")
	}
	return fmt.Errorf("试聊功能未就绪（AI 对话插件未完成初始化）")
}

// ChatConsoleSessions 返回全部试聊会话（最近活动在前）。
func (p *AIChatPlugin) ChatConsoleSessions() []plugininfo.ChatConsoleSession {
	if p.chatConsole == nil {
		return nil
	}
	return p.chatConsole.list()
}

// ChatConsoleCreate 新建试聊会话。
func (p *AIChatPlugin) ChatConsoleCreate(req plugininfo.ChatConsoleSessionCreate) (plugininfo.ChatConsoleSession, error) {
	if p.chatConsole == nil {
		return plugininfo.ChatConsoleSession{}, p.chatConsoleNotReady()
	}
	return p.chatConsole.create(req)
}

// ChatConsoleDelete 删除试聊会话及其转录与 AI 上下文。
func (p *AIChatPlugin) ChatConsoleDelete(id string) error {
	if p.chatConsole == nil {
		return p.chatConsoleNotReady()
	}
	return p.chatConsole.delete(id)
}

// ChatConsoleMessages 返回 Seq 大于 afterSeq 的消息（最后一条总会重新返回，
// 供面板刷新流式文本），并告知该会话是否仍在生成。
func (p *AIChatPlugin) ChatConsoleMessages(id string, afterSeq int64) ([]plugininfo.ChatConsoleMessage, bool, error) {
	if p.chatConsole == nil {
		return nil, false, p.chatConsoleNotReady()
	}
	return p.chatConsole.messages(id, afterSeq)
}

// ChatConsoleClear 清空试聊会话的转录与 AI 上下文。
func (p *AIChatPlugin) ChatConsoleClear(id string) error {
	if p.chatConsole == nil {
		return p.chatConsoleNotReady()
	}
	return p.chatConsole.clear(id)
}

// ChatConsoleSend 异步发送一条用户消息。
func (p *AIChatPlugin) ChatConsoleSend(id, text string) error {
	if p.chatConsole == nil {
		return p.chatConsoleNotReady()
	}
	return p.chatConsole.send(id, text)
}

// ChatConsoleStop 中止该会话正在生成的回复。
func (p *AIChatPlugin) ChatConsoleStop(id string) error {
	if p.chatConsole == nil {
		return p.chatConsoleNotReady()
	}
	s, ok := p.chatConsole.session(id)
	if !ok {
		return fmt.Errorf("试聊会话不存在: %s", id)
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
