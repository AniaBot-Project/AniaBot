package pluginaichat

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// newTestChatConsole 构造一个可用的试聊管理器（LLM 指向 httptest 假服务）。
// 试聊开启流式展示，因此假服务返回 SSE 分块响应（与真实上游一致）。
func newTestChatConsole(t *testing.T) (*AIChatPlugin, *chatConsoleManager) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 稍作延迟：让 send 之后的并发与轮询逻辑可复现
		time.Sleep(15 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range []string{"面板", "收到的", "回复"} {
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test\","+
				"\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", part)
		}
		fmt.Fprint(w, `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test","choices":[],`+
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	p := &AIChatPlugin{ready: true, msgEventTimeout: 10 * time.Second}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.PersistentStorage = newPFake()
	p.toolExecutor = llmtool.NewToolExecuter()
	p.cfg.BaseURL = srv.URL
	p.cfg.APIKey = "test-key"
	p.cfg.Model = "test-model"
	p.cfg.Prompt = "测试用系统提示词"
	m := newChatConsoleManager(p)
	p.chatConsole = m
	return p, m
}

func TestChatConsoleCreateValidation(t *testing.T) {
	_, m := newTestChatConsole(t)

	info, err := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	if err != nil {
		t.Fatalf("通用试聊创建失败: %v", err)
	}
	if info.ID == "" || info.TargetKind != plugininfo.ChatConsoleTargetTest || info.Title != "通用试聊" {
		t.Fatalf("会话信息不符: %+v", info)
	}

	info, err = m.create(plugininfo.ChatConsoleSessionCreate{
		TargetKind: plugininfo.ChatConsoleTargetGroup,
		TargetID:   "qq:123456",
		TargetName: "测试群",
	})
	if err != nil {
		t.Fatalf("群聊试聊创建失败: %v", err)
	}
	if info.Title != "测试群" || info.TargetID != "qq:123456" {
		t.Fatalf("群聊试聊信息不符: %+v", info)
	}

	if _, err := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: "bogus"}); err == nil {
		t.Fatal("非法目标类型应报错")
	}
	if _, err := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetFriend}); err == nil {
		t.Fatal("私聊试聊缺少目标 ID 应报错")
	}

	// 会话数上限
	for i := len(m.list()); i < chatConsoleMaxSessions; i++ {
		if _, err := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest}); err != nil {
			t.Fatalf("第 %d 个会话创建失败: %v", i+1, err)
		}
	}
	if _, err := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest}); err == nil {
		t.Fatal("会话数达上限后应拒绝创建")
	}
}

// TestChatConsoleSendEndToEnd 端到端：发消息 → 后台生成 → 轮询拿到回复、用量与状态。
func TestChatConsoleSendEndToEnd(t *testing.T) {
	_, m := newTestChatConsole(t)
	info, err := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := m.send(info.ID, "  你好  "); err != nil {
		t.Fatalf("send: %v", err)
	}
	// 生成期间不允许并发发送
	if err := m.send(info.ID, "再来一条"); err == nil {
		t.Fatal("生成中再次发送应报错")
	}

	msgs, busy := waitConsoleIdle(t, m, info.ID)
	if busy {
		t.Fatal("等待超时后仍在生成")
	}
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息（用户 + 助手），实际 %d 条: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != plugininfo.ChatConsoleRoleUser || msgs[0].Text != "你好" {
		t.Fatalf("用户消息不符: %+v", msgs[0])
	}
	asst := msgs[1]
	if asst.Role != plugininfo.ChatConsoleRoleAssistant || asst.Status != plugininfo.ChatConsoleStatusOK {
		t.Fatalf("助手消息不符: %+v", asst)
	}
	if asst.Text != "面板收到的回复" {
		t.Fatalf("回复文本 = %q", asst.Text)
	}
	if asst.Usage == nil || asst.Usage.TotalTokens != 18 || asst.Usage.Iterations < 1 {
		t.Fatalf("用量不符: %+v", asst.Usage)
	}
	if asst.DurationMs < 0 {
		t.Fatalf("耗时异常: %d", asst.DurationMs)
	}
	// 会话列表反映最近活动与条数
	list := m.list()
	if len(list) != 1 || list[0].Count != 2 || list[0].Busy {
		t.Fatalf("会话快照不符: %+v", list)
	}
}

// waitConsoleIdle 轮询直到会话空闲，返回当前全部消息。
func waitConsoleIdle(t *testing.T, m *chatConsoleManager, id string) ([]plugininfo.ChatConsoleMessage, bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msgs, busy, err := m.messages(id, 0)
		if err != nil {
			t.Fatalf("messages: %v", err)
		}
		if !busy {
			return msgs, false
		}
		time.Sleep(20 * time.Millisecond)
	}
	msgs, busy, _ := m.messages(id, 0)
	return msgs, busy
}

// TestChatConsoleMessagesCursor 增量轮询：after 为已拿到的最大 Seq 时只回最后一条，
// 保证流式文本与工具进度的后续更新能被前端看到。
func TestChatConsoleMessagesCursor(t *testing.T) {
	_, m := newTestChatConsole(t)
	info, _ := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	s, _ := m.session(info.ID)
	m.addNotice(s, "系统提示一")
	m.addNotice(s, "系统提示二")

	all, _, err := m.messages(info.ID, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("全量消息不符: %d, %v", len(all), err)
	}
	// after=1 只取更新的那一条
	rest, _, _ := m.messages(info.ID, 1)
	if len(rest) != 1 || rest[0].Text != "系统提示二" {
		t.Fatalf("增量消息不符: %+v", rest)
	}
	// after=最大 Seq：最后一条仍会返回（用于刷新同一消息内容）
	tail, _, _ := m.messages(info.ID, 2)
	if len(tail) != 1 || tail[0].Seq != 2 {
		t.Fatalf("最后一条应始终返回: %+v", tail)
	}
}

// TestChatConsoleTranscriptPersistAndInterrupt 转录跨重启恢复，运行中的消息归一化为中断。
func TestChatConsoleTranscriptPersistAndInterrupt(t *testing.T) {
	p, m := newTestChatConsole(t)
	info, _ := m.create(plugininfo.ChatConsoleSessionCreate{
		TargetKind: plugininfo.ChatConsoleTargetGroup,
		TargetID:   "qq:1",
		TargetName: "群",
	})
	s, _ := m.session(info.ID)
	s.mu.Lock()
	s.msgs = append(s.msgs,
		plugininfo.ChatConsoleMessage{Seq: 1, Role: plugininfo.ChatConsoleRoleUser, Text: "在吗", Status: plugininfo.ChatConsoleStatusOK},
		plugininfo.ChatConsoleMessage{Seq: 2, Role: plugininfo.ChatConsoleRoleAssistant, Status: plugininfo.ChatConsoleStatusRunning},
	)
	s.seq = 2
	s.mu.Unlock()
	m.persistSession(s)

	m2 := newChatConsoleManager(p)
	restored, ok := m2.session(info.ID)
	if !ok {
		t.Fatal("重启后未恢复会话")
	}
	msgs, busy, _ := m2.messages(info.ID, 0)
	if busy || len(msgs) != 2 {
		t.Fatalf("恢复结果不符: busy=%v msgs=%+v", busy, msgs)
	}
	if msgs[1].Status != plugininfo.ChatConsoleStatusInterrupted || msgs[1].Error == "" {
		t.Fatalf("运行中消息应归一化为中断: %+v", msgs[1])
	}
	if restored.info.TargetID != "qq:1" {
		t.Fatalf("会话目标丢失: %+v", restored.info)
	}
}

func TestChatConsoleTrimTranscript(t *testing.T) {
	_, m := newTestChatConsole(t)
	info, _ := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	s, _ := m.session(info.ID)
	for i := 0; i < chatConsoleMaxMessages+25; i++ {
		m.addNotice(s, fmt.Sprintf("提示 %d", i))
	}
	msgs, _, _ := m.messages(info.ID, 0)
	if len(msgs) != chatConsoleMaxMessages {
		t.Fatalf("转录应裁剪到 %d 条，实际 %d 条", chatConsoleMaxMessages, len(msgs))
	}
	if msgs[len(msgs)-1].Text != fmt.Sprintf("提示 %d", chatConsoleMaxMessages+24) {
		t.Fatalf("应保留最新消息: %+v", msgs[len(msgs)-1])
	}
}

func TestChatConsoleAttachment(t *testing.T) {
	_, m := newTestChatConsole(t)
	info, _ := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	s, _ := m.session(info.ID)
	s.mu.Lock()
	s.msgs = append(s.msgs, plugininfo.ChatConsoleMessage{Seq: 1, Role: plugininfo.ChatConsoleRoleAssistant, Status: plugininfo.ChatConsoleStatusRunning})
	s.seq = 1
	s.mu.Unlock()

	// 1x1 PNG
	pngB64 := base64.StdEncoding.EncodeToString([]byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
	})
	if err := m.appendAttachment(s, 1, "image", "", pngB64); err != nil {
		t.Fatalf("图片附件失败: %v", err)
	}
	if err := m.appendAttachment(s, 1, "file", "报告.txt", base64.StdEncoding.EncodeToString([]byte("hi"))); err != nil {
		t.Fatalf("文件附件失败: %v", err)
	}
	msgs, _, _ := m.messages(info.ID, 0)
	if len(msgs) != 1 || len(msgs[0].Attachments) != 2 {
		t.Fatalf("附件数量不符: %+v", msgs)
	}
	if !strings.HasPrefix(msgs[0].Attachments[0].URL, "data:image/png;base64,") {
		t.Fatalf("图片 data URI 不符: %s", msgs[0].Attachments[0].URL)
	}
	if !strings.HasPrefix(msgs[0].Attachments[1].URL, "data:text/plain") || msgs[0].Attachments[1].Name != "报告.txt" {
		t.Fatalf("文件 data URI 不符: %+v", msgs[0].Attachments[1])
	}

	if err := m.appendAttachment(s, 1, "image", "", "不是base64"); err == nil {
		t.Fatal("非法 base64 应报错")
	}
	// 超过上限：不报错，改为追加系统提示
	s.mu.Lock()
	s.msgs[0].Attachments = nil
	s.mu.Unlock()
	big := base64.StdEncoding.EncodeToString(make([]byte, chatConsoleMaxAttachmentBytes+1))
	if err := m.appendAttachment(s, 1, "image", "", big); err != nil {
		t.Fatalf("超大附件不应报错: %v", err)
	}
	msgs, _, _ = m.messages(info.ID, 0)
	if len(msgs) != 2 || !strings.Contains(msgs[1].Text, "未展示") {
		t.Fatalf("超大附件应追加提示: %+v", msgs)
	}
}

func TestChatConsoleDeleteAndClear(t *testing.T) {
	_, m := newTestChatConsole(t)
	info, _ := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	s, _ := m.session(info.ID)
	m.addNotice(s, "提示")

	if err := m.clear(info.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	msgs, _, _ := m.messages(info.ID, 0)
	if len(msgs) != 0 {
		t.Fatalf("清空后仍有消息: %+v", msgs)
	}
	if err := m.delete(info.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := m.session(info.ID); ok {
		t.Fatal("删除后会话仍存在")
	}
	if err := m.delete(info.ID); err == nil {
		t.Fatal("重复删除应报错")
	}
	if err := m.send(info.ID, "hi"); err == nil {
		t.Fatal("向不存在的会话发送应报错")
	}
}

func TestChatConsoleNotReadyAndEmpty(t *testing.T) {
	p, m := newTestChatConsole(t)
	p.ready = false
	info, _ := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	if err := m.send(info.ID, "hi"); err == nil {
		t.Fatal("未就绪时发送应报错")
	}
	p.ready = true
	if err := m.send(info.ID, "   "); err == nil {
		t.Fatal("空消息应报错")
	}
	if err := m.send(info.ID, strings.Repeat("长", chatConsoleMaxTextRunes+1)); err == nil {
		t.Fatal("超长消息应报错")
	}
}

// TestChatConsoleUserInputPrefix 试聊的用户消息与线上一致带发送者前缀。
func TestChatConsoleUserInputPrefix(t *testing.T) {
	_, m := newTestChatConsole(t)
	if got := m.userInput("你好"); got != "[nickname:面板管理员 id:0]: 你好" {
		t.Fatalf("用户消息前缀不符: %q", got)
	}
}

// TestChatConsoleStopWhileIdle 空闲时停止为空操作。
func TestChatConsoleStopWhileIdle(t *testing.T) {
	p, m := newTestChatConsole(t)
	info, _ := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	if err := p.ChatConsoleStop(info.ID); err != nil {
		t.Fatalf("空闲停止不应报错: %v", err)
	}
	if err := p.ChatConsoleStop("不存在"); err == nil {
		t.Fatal("不存在的会话停止应报错")
	}
}

// TestChatConsoleStopCancelsRun 生成中停止：请求被取消，消息状态标记为 stopped。
func TestChatConsoleStopCancelsRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 拖住响应，模拟长耗时生成（客户端取消后服务端连接可能仍被保持，
		// 因此用较短的兜底等待，避免 httptest.Server.Close 阻塞测试）
		select {
		case <-r.Context().Done():
		case <-time.After(800 * time.Millisecond):
		}
	}))
	t.Cleanup(srv.Close)

	p := &AIChatPlugin{ready: true, msgEventTimeout: 30 * time.Second}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.PersistentStorage = newPFake()
	p.toolExecutor = llmtool.NewToolExecuter()
	p.cfg.BaseURL = srv.URL
	p.cfg.APIKey = "test-key"
	p.cfg.Model = "test-model"
	p.cfg.Prompt = "测试用系统提示词"
	m := newChatConsoleManager(p)
	p.chatConsole = m

	info, err := m.create(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := m.send(info.ID, "这条请求会被停止"); err != nil {
		t.Fatalf("send: %v", err)
	}
	// 等生成真正开始（busy=true 且 assistant 消息已存在）
	time.Sleep(100 * time.Millisecond)
	if err := p.ChatConsoleStop(info.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var msgs []plugininfo.ChatConsoleMessage
	for time.Now().Before(deadline) {
		msgs, _, _ = m.messages(info.ID, 0)
		if len(msgs) == 2 && msgs[1].Status != plugininfo.ChatConsoleStatusRunning {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息，实际 %d 条", len(msgs))
	}
	if msgs[1].Status != plugininfo.ChatConsoleStatusStopped {
		t.Fatalf("停止后状态应为 stopped，实际 %+v", msgs[1])
	}
	if msgs[1].Error == "" {
		t.Fatalf("停止后应带原因说明: %+v", msgs[1])
	}
}

// TestChatConsolePanelNilManager 管理器未就绪（插件尚未 Start）时面板接口退化。
func TestChatConsolePanelNilManager(t *testing.T) {
	p := &AIChatPlugin{}
	if got := p.ChatConsoleSessions(); got != nil {
		t.Fatalf("未就绪时应返回空列表: %+v", got)
	}
	if _, err := p.ChatConsoleCreate(plugininfo.ChatConsoleSessionCreate{TargetKind: plugininfo.ChatConsoleTargetTest}); err == nil {
		t.Fatal("未就绪时创建应报错")
	}
	if _, _, err := p.ChatConsoleMessages("x", 0); err == nil {
		t.Fatal("未就绪时查询消息应报错")
	}
	if err := p.ChatConsoleClear("x"); err == nil {
		t.Fatal("未就绪时清空应报错")
	}
	if err := p.ChatConsoleDelete("x"); err == nil {
		t.Fatal("未就绪时删除应报错")
	}
	if err := p.ChatConsoleSend("x", "hi"); err == nil {
		t.Fatal("未就绪时发送应报错")
	}
	if err := p.ChatConsoleStop("x"); err == nil {
		t.Fatal("未就绪时停止应报错")
	}
}
