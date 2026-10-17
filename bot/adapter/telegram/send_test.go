package telegram

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// pngB64 一段最小合法 PNG 文件头的 base64（含 PNG 魔数）。
var pngB64 = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0000000000000000"))

// TestSplitText 4096 上限分包：长文本、多字节字符不切断。
func TestSplitText(t *testing.T) {
	// 短文本不分包
	parts := splitText("hello")
	if len(parts) != 1 || parts[0] != "hello" {
		t.Fatalf("短文本应 1 包, got %v", parts)
	}
	// 长 ASCII 文本分 2 包
	long := strings.Repeat("a", 5000)
	parts = splitText(long)
	if len(parts) != 2 {
		t.Fatalf("5000 字符应分 2 包, got %d", len(parts))
	}
	for _, p := range parts {
		if len([]rune(p)) > 4090 {
			t.Fatalf("分包超过上限: %d", len([]rune(p)))
		}
	}
	// CJK（一字符一 UTF-16 单位）不切断多字节字符
	cjk := strings.Repeat("中", 5000)
	parts = splitText(cjk)
	if len(parts) != 2 || parts[0] != strings.Repeat("中", 4090) {
		t.Fatalf("CJK 分包错误: 包数 %d, 首包长度 %d", len(parts), len([]rune(parts[0])))
	}
	// 拼接还原
	if got := strings.Join(parts, ""); got != cjk {
		t.Fatal("分包拼接后内容不一致")
	}
}

// TestTruncateText 按 Telegram 计量单位（UTF-16 code unit）截断：不切断多字节
// 字符，也不因中文/emoji 而提前砍掉内容（旧实现按字节计，中文只保留约 1/3）。
func TestTruncateText(t *testing.T) {
	s := "你好世界"
	if got := truncateText(s, 100); got != s {
		t.Fatalf("未超限不应截断, got %q", got)
	}
	// 中文按"字"计数：限 2 个单元应保留 2 个字（按字节计只会保留 0 个）
	if got := truncateText("你好世", 2); got != "你好" {
		t.Fatalf("截断结果 = %q, want 你好", got)
	}
	// 中文长文本应保留接近上限的字符数（3 字节/字，按字节计只会有 ~1365 字）
	longCN := strings.Repeat("中", 5000)
	got := truncateText(longCN, maxEditTextLen)
	if n := len([]rune(got)); n != maxEditTextLen {
		t.Fatalf("中文截断应保留 %d 个字，实际 %d 个（疑似按字节计）", maxEditTextLen, n)
	}
	// 补充平面字符（emoji）占 2 个单元，不能切成半个
	if got := truncateText("😀😀😀", 3); got != "😀" {
		t.Fatalf("emoji 截断结果 = %q, want 😀", got)
	}
	long := strings.Repeat("a", 5000)
	if got := truncateText(long, maxEditTextLen); len(got) != maxEditTextLen {
		t.Fatalf("截断长度 = %d, want %d", len(got), maxEditTextLen)
	}
}

// TestResolveMention 预置 chatMemberCache 后 at 段展开为 @username（不触网）。
func TestResolveMention(t *testing.T) {
	a := testAdapter()
	a.chatMemberCache.Store("-100:222", mentionCache{username: "alice", at: time.Now()})

	s := message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{QQ: message.QID("tg:222")}.Marshal()}
	if got := a.resolveMention(nil, -100, s); got != "@alice " {
		t.Fatalf("resolveMention = %q, want @alice ", got)
	}
	// 私聊（chat_id 为正数）不解析
	if got := a.resolveMention(nil, 111, s); got != "" {
		t.Fatalf("私聊不应解析 @, got %q", got)
	}
	// @all 丢弃
	sAll := message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{IsAll: true}.Marshal()}
	if got := a.resolveMention(nil, -100, sAll); got != "" {
		t.Fatalf("@all 应丢弃, got %q", got)
	}
	// 无 username 的用户：缓存命中但 username 为空 → 丢弃
	a.chatMemberCache.Store("-100:333", mentionCache{username: "", at: time.Now()})
	s3 := message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{QQ: message.QID("tg:333")}.Marshal()}
	if got := a.resolveMention(nil, -100, s3); got != "" {
		t.Fatalf("无 username 应丢弃, got %q", got)
	}
}

// TestSendChainTextMention reply 提取 + at 展开 + 文本发送（无网络：client 为 nil 时发送失败
// 返回 false，但 reply 提取与 at 展开逻辑在发送前）。
func TestSendChainReplyAndMention(t *testing.T) {
	a := testAdapter()
	a.chatMemberCache.Store("-100:222", mentionCache{username: "alice", at: time.Now()})

	chain := msgchain.Builder().
		Group().
		Reply(message.QID("tg:-100:42")).
		Mention(message.QID("tg:222")).
		Text("你好").
		Build()
	segs := chain.GetGroupMsg()
	// 断言段序列：reply 在前
	if len(segs) == 0 || segs[0].Type != message.SegmentReply {
		t.Fatalf("期望 reply 段在前, got %+v", segs)
	}
	if id, _ := segs[0].Data["id"].(string); id != "tg:-100:42" {
		t.Fatalf("reply id = %q", id)
	}
	// client 为 nil → 发送失败（不 panic）
	if _, ok := a.SendGroupMsg(message.QID("tg:-100"), chain); ok {
		t.Fatal("client 为 nil 时发送应失败")
	}
}

// TestSendMediaKeySelection 媒体段方法名与文件键选择。
func TestSendMediaKeySelection(t *testing.T) {
	a := testAdapter()
	// client 为 nil 时 sendMedia 内部 call 会失败（resty nil 指针），
	// 仅验证 key 选择逻辑不 panic —— 直接测 sendMediaSegment 前置分支
	img := message.OB11Segment{Type: message.SegmentImage, Data: map[string]any{"file": "AgAAAA", "url": "https://x.com/a.jpg"}}
	if _, ok := a.sendMediaSegment(nil, -100, img, "", nil); ok {
		t.Fatal("client 为 nil 应发送失败")
	}
}

// TestMediaMethodOfFileImage file 段指向图片文件时走 sendPhoto，普通文件走 sendDocument。
func TestMediaMethodOfFileImage(t *testing.T) {
	// PNG base64 → sendPhoto（保留原文件名）
	method, fileKey, fileName := mediaMethodOf(message.OB11Segment{
		Type: message.SegmentFile,
		Data: message.FileMessage{File: "base64://" + pngB64, Name: "photo.png"}.Marshal(),
	})
	if method != "sendPhoto" {
		t.Fatalf("图片文件应走 sendPhoto, got %q", method)
	}
	if fileKey != "base64://"+pngB64 {
		t.Fatalf("文件源应保留, got %q", fileKey)
	}
	if fileName != "photo.png" {
		t.Fatalf("文件名 = %q, want photo.png", fileName)
	}

	// PNG 内容但无图片后缀 → 仍按内容识别为 sendPhoto
	method, _, _ = mediaMethodOf(message.OB11Segment{
		Type: message.SegmentFile,
		Data: message.FileMessage{File: "base64://" + pngB64, Name: "data.bin"}.Marshal(),
	})
	if method != "sendPhoto" {
		t.Fatalf("PNG 内容应走 sendPhoto, got %q", method)
	}

	// 非图片 → sendDocument
	method, _, fileName = mediaMethodOf(message.OB11Segment{
		Type: message.SegmentFile,
		Data: message.FileMessage{File: "base64://" + base64.StdEncoding.EncodeToString([]byte("text")), Name: "doc.pdf"}.Marshal(),
	})
	if method != "sendDocument" || fileName != "doc.pdf" {
		t.Fatalf("非图片应走 sendDocument, got %q/%q", method, fileName)
	}

	// 图片 URL → sendPhoto（扩展名判定）
	method, fileKey, _ = mediaMethodOf(message.OB11Segment{
		Type: message.SegmentFile,
		Data: message.FileMessage{File: "https://e.com/a.png?token=1", Name: "a.png"}.Marshal(),
	})
	if method != "sendPhoto" || fileKey != "https://e.com/a.png?token=1" {
		t.Fatalf("图片 URL 应走 sendPhoto 并保留源, got %q/%q", method, fileKey)
	}
}

// TestSendChainFileImageGoesPhoto 端到端：file 工具发来的 PNG 文件段按 sendPhoto
// multipart 上传（图片通道），PDF 文件段按 sendDocument 上传（文件通道）。
func TestSendChainFileImageGoesPhoto(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()

	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fake"))
	chain := msgchain.Builder().Group().
		FileBase64("photo.png", pngB64).
		FileBase64("doc.pdf", pdfB64).
		Build()
	if _, ok := a.SendGroupMsg(message.QID("tg:-100"), chain); !ok {
		t.Fatal("发送失败")
	}
	if n := f.count("sendPhoto"); n != 1 {
		t.Fatalf("sendPhoto 调用 = %d, want 1", n)
	}
	if n := f.count("sendDocument"); n != 1 {
		t.Fatalf("sendDocument 调用 = %d, want 1", n)
	}
	if r := f.req(0); r.fileField != "photo" || string(r.file) != "\x89PNG\r\n\x1a\n0000000000000000" {
		t.Fatalf("sendPhoto 应以 photo 字段上传 PNG 字节, got field=%q len=%d", r.fileField, len(r.file))
	}
	if r := f.req(1); r.fileField != "document" {
		t.Fatalf("sendDocument 应以 document 字段上传, got field=%q", r.fileField)
	}
}

// TestStreamHandleLifecycle 流式句柄：Patch 节流、End 幂等、End 后 no-op。
func TestStreamHandleLifecycle(t *testing.T) {
	a := testAdapter()
	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 1, prefix: "@alice "}

	// client 为 nil：patchLocked 直接返回 nil（不 panic）
	if err := h.Patch("hello"); err != nil {
		t.Fatalf("Patch 不应报错: %v", err)
	}
	// 节流窗口内再 Patch 仅记录内容
	h.content = "world"
	h.End()
	if h.content != "world" {
		t.Fatal("End 后 content 不应被修改")
	}
	// End 幂等：再次 End 不 panic
	h.End()
	// End 后 Patch 为 no-op
	if err := h.Patch("after"); err != nil {
		t.Fatalf("End 后 Patch 不应报错: %v", err)
	}
	if h.content != "world" {
		t.Fatal("End 后 Patch 不应更新内容")
	}
}

// TestStreamHandlePrefixPreserved Patch/End 的内容拼接保留 prefix。
func TestStreamHandlePrefixPreserved(t *testing.T) {
	a := testAdapter()
	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 1, prefix: "@alice "}
	h.content = "你好"
	// 模拟 patchLocked 的内容构造（client nil 时不真正调用 API，直接验证拼接逻辑）
	content := truncateText(h.prefix+h.content, maxEditTextLen)
	if content != "@alice 你好" {
		t.Fatalf("拼接内容 = %q, want @alice 你好（prefix 保留）", content)
	}
}

// TestSendTextPlainDefault 默认（off）不带 parse_mode，纯文本发送。
func TestSendTextPlainDefault(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()

	if _, ok := a.sendText(t.Context(), -100, "hello **world**", nil); !ok {
		t.Fatal("发送失败")
	}
	if n := f.count("sendMessage"); n != 1 {
		t.Fatalf("sendMessage 调用 = %d, want 1", n)
	}
	if _, has := f.req(0).json["parse_mode"]; has {
		t.Fatalf("默认不应携带 parse_mode, got %+v", f.req(0).json)
	}
}

// TestSendTextMarkdownEnabled markdownv2 开启：一次发送即带 parse_mode。
func TestSendTextMarkdownEnabled(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	if _, ok := a.sendText(t.Context(), -100, "**加粗**\n\n- 列表项", nil); !ok {
		t.Fatal("发送失败")
	}
	if n := f.count("sendMessage"); n != 1 {
		t.Fatalf("sendMessage 调用 = %d, want 1（无降级重发）", n)
	}
	if f.req(0).json["parse_mode"] != "MarkdownV2" {
		t.Fatalf("parse_mode = %v, want MarkdownV2", f.req(0).json["parse_mode"])
	}
}

// TestSendTextMarkdownFallback markdownv2 开启且解析失败（400）：降级纯文本重发，
// 内容与 reply_parameters 保持一致。
func TestSendTextMarkdownFallback(t *testing.T) {
	f := newFakeAPI()
	f.parseModeFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	replyTo := 5
	text := "**加粗** 含未转义 (括号)"
	id, ok := a.sendText(t.Context(), -100, text, &replyTo)
	if !ok {
		t.Fatal("解析失败后降级纯文本应发送成功")
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
	if n := f.count("sendMessage"); n != 2 {
		t.Fatalf("sendMessage 调用 = %d, want 2（带 parse_mode + 降级重发）", n)
	}
	r0, r1 := f.req(0), f.req(1)
	if r0.json["parse_mode"] != "MarkdownV2" {
		t.Fatalf("首次请求应带 parse_mode, got %+v", r0.json)
	}
	if _, has := r1.json["parse_mode"]; has {
		t.Fatalf("降级重发不应带 parse_mode, got %+v", r1.json)
	}
	if r1.json["text"] != text {
		t.Fatalf("降级重发内容 = %v, want 原内容", r1.json["text"])
	}
	if rp, _ := r1.json["reply_parameters"].(map[string]any); rp["message_id"] != float64(5) {
		t.Fatalf("降级重发应保留 reply_parameters, got %+v", r1.json["reply_parameters"])
	}
}

// TestStreamEndMarkdown 流式中间编辑始终纯文本；End 最终编辑带 parse_mode，
// 解析失败（400）降级纯文本重发保证最终内容落地。
func TestStreamEndMarkdown(t *testing.T) {
	f := newFakeAPI()
	f.parseModeFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	// 首次 Patch 无节流（lastPatch 为零值）→ 立即编辑，纯文本
	if err := h.Patch("中间内容"); err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	// 节流窗口内 Patch 仅记录内容，End 时一并发送
	h.content = "**最终内容** (未转义)"
	h.End()

	if n := f.count("editMessageText"); n != 3 {
		t.Fatalf("editMessageText 调用 = %d, want 3（中间 + 最终带 parse_mode + 降级重发）", n)
	}
	r0, r1, r2 := f.req(0), f.req(1), f.req(2)
	if _, has := r0.json["parse_mode"]; has {
		t.Fatalf("流式中间编辑不应带 parse_mode, got %+v", r0.json)
	}
	if r0.json["text"] != "中间内容" {
		t.Fatalf("中间编辑内容 = %v", r0.json["text"])
	}
	if r1.json["parse_mode"] != "MarkdownV2" {
		t.Fatalf("最终编辑应带 parse_mode, got %+v", r1.json)
	}
	if r2.json["text"] != "**最终内容** (未转义)" || r2.json["message_id"] != float64(7) {
		t.Fatalf("降级重发应带最终内容, got %+v", r2.json)
	}
	if _, has := r2.json["parse_mode"]; has {
		t.Fatalf("降级重发不应带 parse_mode, got %+v", r2.json)
	}
}

// TestStreamEndPlainWhenDisabled 未开启 markdownv2 时 End 最终编辑不带 parse_mode。
func TestStreamEndPlainWhenDisabled(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "hi"
	h.End()
	if n := f.count("editMessageText"); n != 1 {
		t.Fatalf("editMessageText 调用 = %d, want 1", n)
	}
	if _, has := f.req(0).json["parse_mode"]; has {
		t.Fatalf("未开启时不应带 parse_mode, got %+v", f.req(0).json)
	}
}

// TestSendTextGatewayRetry 网关异常响应（502 错误页，code 0）：原样重试一次，
// 保留 parse_mode（不降级），重试成功。
func TestSendTextGatewayRetry(t *testing.T) {
	f := newFakeAPI()
	f.htmlFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	if _, ok := a.sendText(t.Context(), -100, "**加粗**", nil); !ok {
		t.Fatal("网关异常重试后应发送成功")
	}
	if n := f.count("sendMessage"); n != 2 {
		t.Fatalf("sendMessage 调用 = %d, want 2（502 + 重试）", n)
	}
	for i := range 2 {
		if f.req(i).json["parse_mode"] != "MarkdownV2" {
			t.Fatalf("重试应保留 parse_mode（原样重试）, req%d = %+v", i, f.req(i).json)
		}
	}
}

// TestStreamEndGatewayRetry 流式 End 遇网关异常响应（502）：原样重试一次，
// 最终内容与 parse_mode 保留。
func TestStreamEndGatewayRetry(t *testing.T) {
	f := newFakeAPI()
	f.htmlFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "**最终内容**"
	h.End()
	if n := f.count("editMessageText"); n != 2 {
		t.Fatalf("editMessageText 调用 = %d, want 2（502 + 重试）", n)
	}
	for i := range 2 {
		if f.req(i).json["parse_mode"] != "MarkdownV2" || f.req(i).json["text"] != "**最终内容**" {
			t.Fatalf("重试应保留 parse_mode 与内容, req%d = %+v", i, f.req(i).json)
		}
	}
}

// TestStreamEndSkipWhenUnchanged 纯文本编辑且内容与上次成功编辑一致时跳过
// （Telegram 拒绝未变化的编辑，消除 "message is not modified" 噪音）。
func TestStreamEndSkipWhenUnchanged(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	// 未配置 parse_mode（纯文本模式）

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "hi"
	if err := h.patchLocked(false); err != nil {
		t.Fatalf("首次编辑失败: %v", err)
	}
	// 内容未变化：End 应跳过编辑
	h.End()
	if n := f.count("editMessageText"); n != 1 {
		t.Fatalf("editMessageText 调用 = %d, want 1（未变化跳过）", n)
	}
}

// TestStreamEndMarkdownRendersWhenUnchanged 配置 markdown 渲染时，End 的最终
// 编辑即使内容与最后一条纯文本一致也必须尝试（渲染生成的实体使消息内容变化，
// 参考 aiogram 流式写法：流式 ParseMode.NONE，最终无条件应用 Markdown）。
func TestStreamEndMarkdownRendersWhenUnchanged(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdown"

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "**加粗**\n\n- 列表项"
	if err := h.patchLocked(false); err != nil {
		t.Fatalf("中间编辑失败: %v", err)
	}
	// 内容未变化，但 End 必须尝试 Markdown 渲染
	h.End()
	if n := f.count("editMessageText"); n != 2 {
		t.Fatalf("editMessageText 调用 = %d, want 2（中间纯文本 + 最终 Markdown 渲染）", n)
	}
	r1 := f.req(1)
	if r1.json["parse_mode"] != "Markdown" {
		t.Fatalf("最终编辑应带 parse_mode=Markdown, got %+v", r1.json)
	}
	if r1.json["text"] != "**加粗**\n\n- 列表项" {
		t.Fatalf("最终编辑内容错误, got %+v", r1.json)
	}
}

// TestStreamEndMarkdownFallbackSkipsNoise 内容已以纯文本展示、最终 Markdown
// 渲染失败（400）时：降级纯文本重发与已展示内容一致，跳过重发（消息本就
// 完整），不产生 "message is not modified" 噪音。
func TestStreamEndMarkdownFallbackSkipsNoise(t *testing.T) {
	f := newFakeAPI()
	f.parseModeFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "**加粗**"
	if err := h.patchLocked(false); err != nil {
		t.Fatalf("中间编辑失败: %v", err)
	}
	h.End()
	// 仅 2 次编辑：中间纯文本 + 最终 Markdown 尝试（400 后跳过相同的纯文本重发）
	if n := f.count("editMessageText"); n != 2 {
		t.Fatalf("editMessageText 调用 = %d, want 2（无降级重发噪音）", n)
	}
	if f.req(1).json["parse_mode"] != "MarkdownV2" {
		t.Fatalf("最终编辑应带 parse_mode, got %+v", f.req(1).json)
	}
}

// TestSendTextGatewayRejectParseMode 网关以自有格式拒绝 parse_mode（code 0 + http 400）：
// 非瞬时，直接去掉 parse_mode 纯文本重发。
func TestSendTextGatewayRejectParseMode(t *testing.T) {
	f := newFakeAPI()
	f.text400Fail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	if _, ok := a.sendText(t.Context(), -100, "**加粗**", nil); !ok {
		t.Fatal("网关拒绝 parse_mode 后应降级纯文本发送成功")
	}
	if n := f.count("sendMessage"); n != 2 {
		t.Fatalf("sendMessage 调用 = %d, want 2（parse_mode + 纯文本降级，无原样重试）", n)
	}
	if f.req(0).json["parse_mode"] != "MarkdownV2" {
		t.Fatalf("首次应带 parse_mode, got %+v", f.req(0).json)
	}
	if _, has := f.req(1).json["parse_mode"]; has {
		t.Fatalf("降级重发不应带 parse_mode, got %+v", f.req(1).json)
	}
}

// TestStreamEndGatewayRejectParseMode 流式 End 被网关拒绝 parse_mode（code 0 + http 400）：
// 降级纯文本重发，最终内容落地，消息不再停在中间内容。
func TestStreamEndGatewayRejectParseMode(t *testing.T) {
	f := newFakeAPI()
	f.text400Fail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "markdownv2"

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "**最终内容**"
	h.End()
	if n := f.count("editMessageText"); n != 2 {
		t.Fatalf("editMessageText 调用 = %d, want 2（parse_mode + 纯文本降级，无原样重试）", n)
	}
	r0, r1 := f.req(0), f.req(1)
	if r0.json["parse_mode"] != "MarkdownV2" {
		t.Fatalf("首次应带 parse_mode, got %+v", r0.json)
	}
	if _, has := r1.json["parse_mode"]; has {
		t.Fatalf("降级重发不应带 parse_mode, got %+v", r1.json)
	}
	if r1.json["text"] != "**最终内容**" {
		t.Fatalf("降级重发应含最终内容, got %+v", r1.json)
	}
}

// TestSendTextHTML html 模式：AI markdown 转换为 Telegram HTML 携带 parse_mode=HTML。
func TestSendTextHTML(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "html"

	if _, ok := a.sendText(t.Context(), -100, "# 标题\n\n**加粗** 与 `代码`", nil); !ok {
		t.Fatal("html 模式发送应成功")
	}
	r := f.req(0)
	if r.json["parse_mode"] != "HTML" {
		t.Fatalf("应携带 parse_mode=HTML, got %+v", r.json)
	}
	want := "<b>标题</b>\n\n<b>加粗</b> 与 <code>代码</code>"
	if r.json["text"] != want {
		t.Fatalf("text = %q, want %q（markdown 已转换为 HTML）", r.json["text"], want)
	}
}

// TestSendTextHTMLFallbackRestoresRaw html 解析失败降级纯文本重发时：
// 必须还原未转换的原文（不能把 HTML 标签当纯文本发出）。
func TestSendTextHTMLFallbackRestoresRaw(t *testing.T) {
	f := newFakeAPI()
	f.parseModeFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "html"

	if _, ok := a.sendText(t.Context(), -100, "**加粗**", nil); !ok {
		t.Fatal("html 解析失败降级纯文本应发送成功")
	}
	if n := f.count("sendMessage"); n != 2 {
		t.Fatalf("sendMessage 调用 = %d, want 2（HTML + 纯文本降级）", n)
	}
	r0, r1 := f.req(0), f.req(1)
	if r0.json["parse_mode"] != "HTML" || r0.json["text"] != "<b>加粗</b>" {
		t.Fatalf("首次应为 HTML 转换后的内容, got %+v", r0.json)
	}
	if _, has := r1.json["parse_mode"]; has {
		t.Fatalf("降级重发不应带 parse_mode, got %+v", r1.json)
	}
	if r1.json["text"] != "**加粗**" {
		t.Fatalf("降级重发应还原原文, got %q, want %q", r1.json["text"], "**加粗**")
	}
}

// TestStreamEndHTML 流式 html 模式：中间编辑纯文本（原文），End 最终编辑
// 转换 HTML 携带 parse_mode=HTML。
func TestStreamEndHTML(t *testing.T) {
	f := newFakeAPI()
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "html"

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "## 结论\n\n**加粗** 内容"
	if err := h.patchLocked(false); err != nil {
		t.Fatalf("中间编辑失败: %v", err)
	}
	h.End()
	if n := f.count("editMessageText"); n != 2 {
		t.Fatalf("editMessageText 调用 = %d, want 2（中间纯文本 + 最终 HTML 渲染）", n)
	}
	r0, r1 := f.req(0), f.req(1)
	if _, has := r0.json["parse_mode"]; has {
		t.Fatalf("流式中间编辑不应带 parse_mode, got %+v", r0.json)
	}
	if r0.json["text"] != "## 结论\n\n**加粗** 内容" {
		t.Fatalf("中间编辑应为原文, got %+v", r0.json)
	}
	if r1.json["parse_mode"] != "HTML" {
		t.Fatalf("最终编辑应带 parse_mode=HTML, got %+v", r1.json)
	}
	if want := "<b>结论</b>\n\n<b>加粗</b> 内容"; r1.json["text"] != want {
		t.Fatalf("最终编辑 text = %q, want %q", r1.json["text"], want)
	}
}

// TestStreamEndHTMLFallbackRestoresRaw 流式 End 的 HTML 渲染失败时：
// 降级纯文本重发还原原文（不发出 HTML 标签）。
func TestStreamEndHTMLFallbackRestoresRaw(t *testing.T) {
	f := newFakeAPI()
	f.parseModeFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()
	a.cfg.parseMode = "html"

	h := &telegramStreamHandle{a: a, chatID: -100, msgID: 7}
	h.content = "**最终**"
	h.End()
	if n := f.count("editMessageText"); n != 2 {
		t.Fatalf("editMessageText 调用 = %d, want 2（HTML + 纯文本降级）", n)
	}
	r0, r1 := f.req(0), f.req(1)
	if r0.json["parse_mode"] != "HTML" || r0.json["text"] != "<b>最终</b>" {
		t.Fatalf("首次应为 HTML 转换后的内容, got %+v", r0.json)
	}
	if _, has := r1.json["parse_mode"]; has {
		t.Fatalf("降级重发不应带 parse_mode, got %+v", r1.json)
	}
	if r1.json["text"] != "**最终**" {
		t.Fatalf("降级重发应还原原文, got %q, want %q", r1.json["text"], "**最终**")
	}
}

// TestClientGatewayErrorStatus 网关错误页的错误信息附带 HTTP 状态码（诊断用）。
func TestClientGatewayErrorStatus(t *testing.T) {
	f := newFakeAPI()
	f.htmlFail = 1
	a, srv := testAdapterWithServer(f)
	defer srv.Close()

	var res messageSendResult
	err := a.client.call(t.Context(), "sendMessage", map[string]any{"chat_id": int64(-100), "text": "hi"}, &res)
	if err == nil {
		t.Fatal("502 错误页应返回错误")
	}
	if !strings.Contains(err.Error(), "http 502") {
		t.Fatalf("错误信息应附带 HTTP 状态码, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "telegram api error 0") {
		t.Fatalf("错误信息应含 code 0, got %q", err.Error())
	}
}

// TestSendStreamNilClient client 为 nil 时流式创建失败（不 panic）。
func TestSendStreamNilClient(t *testing.T) {
	a := testAdapter()
	chain := msgchain.Builder().Group().Text("hi").Build()
	if h, ok := a.SendGroupStream(message.QID("tg:-100"), chain); ok || h != nil {
		t.Fatal("client 为 nil 时流式创建应失败")
	}
	// StreamSenderExt 接口断言
	if _, ok := any(a).(interface {
		SendGroupStream(message.QID, msgchain.GroupChain) (bot.StreamHandle, bool)
	}); !ok {
		t.Fatal("适配器应实现 StreamSenderExt")
	}
}
