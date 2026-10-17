package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeHistoryStore 用于测试的内存历史存储，模拟持久化的读写语义。
type fakeHistoryStore struct {
	saved    []Message
	cleared  bool
	replaced int // Replace 调用次数（压缩/截断断言用）
	// clearErr 非 nil 时 Clear 返回该错误（模拟 SQLite busy / MySQL 抖动）
	clearErr error
}

func (f *fakeHistoryStore) Load(_ context.Context) ([]Message, error) {
	if f.saved == nil {
		return nil, nil
	}
	// 返回副本，模拟从外部存储反序列化得到的新切片
	out := make([]Message, len(f.saved))
	copy(out, f.saved)
	return out, nil
}

func (f *fakeHistoryStore) Append(_ context.Context, messages []Message) error {
	f.saved = append(f.saved, messages...)
	return nil
}

func (f *fakeHistoryStore) Replace(_ context.Context, messages []Message) error {
	f.saved = make([]Message, len(messages))
	copy(f.saved, messages)
	f.replaced++
	return nil
}

func (f *fakeHistoryStore) Clear(_ context.Context) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.saved = nil
	f.cleared = true
	return nil
}

// TestMessageWindowClearPropagatesStoreError clear 必须把存储层错误透出：
// 调用方（#新对话 / 未@自动清空）需要知道"清空未真正生效"——否则内存窗口已空、
// 日志写"清理成功"，但 DB 行仍在，会话淘汰/进程重启后 LoadHistory 会把旧上下文
// 整段回放，用户看到"新对话没生效"。
func TestMessageWindowClearPropagatesStoreError(t *testing.T) {
	store := &fakeHistoryStore{clearErr: errors.New("db busy")}
	w := newMessageWindow(1000, nil, nil, store)
	w.append(TextMessage(RoleUser, "hi"))

	if err := w.clear(context.Background()); err == nil {
		t.Fatal("存储清空失败必须返回错误（否则上层误判为清理成功）")
	}
	// 内存窗口已清空（可接受），但错误必须可见
	if len(w.history()) != 0 {
		t.Fatalf("内存窗口应已清空, got %d 条", len(w.history()))
	}
}

// TestClearHistoryPropagatesError ChatBot.ClearHistory 不得吞掉存储错误。
func TestClearHistoryPropagatesError(t *testing.T) {
	store := &fakeHistoryStore{clearErr: errors.New("db busy")}
	bot, err := NewChatBot("http://127.0.0.1:1", "k", "m", "p", 1000, nil, store)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	if err := bot.ClearHistory(context.Background()); err == nil {
		t.Fatal("存储失败时 ClearHistory 应返回错误（旧实现恒返回 nil，上层错误分支是死代码）")
	}

	okStore := &fakeHistoryStore{}
	bot2, err := NewChatBot("http://127.0.0.1:1", "k", "m", "p", 1000, nil, okStore)
	if err != nil {
		t.Fatalf("NewChatBot: %v", err)
	}
	if err := bot2.ClearHistory(context.Background()); err != nil {
		t.Fatalf("正常清空不应报错: %v", err)
	}
	if !okStore.cleared {
		t.Fatal("正常清空应调用存储 Clear")
	}
}

func TestMessageWindowPersistAndLoad(t *testing.T) {
	store := &fakeHistoryStore{}
	w := newMessageWindow(1000, nil, nil, store)

	// append 后应自动落盘
	w.append(TextMessage(RoleUser, "你好"))
	w.append(Message{
		Role: RoleAssistant,
		Parts: []ContentPart{
			TextPart("这是图片"),
			ImageURLPart("https://example.com/a.png"),
		},
		ToolCalls: []llmtool.ToolCall{
			{ID: "call_1", Name: "get_time", Arguments: `{}`},
		},
		ReasoningContent: "推理过程",
	})

	if len(store.saved) != 2 {
		t.Fatalf("持久化消息数 = %d, want 2", len(store.saved))
	}
	// 落盘副本中的图片片段应降级为带哈希的文本标记（避免大 key 与写放大）
	wantMark := "[图片 " + message.ImageHash("https://example.com/a.png") + "]"
	if store.saved[1].Parts[1].Type != ContentPartText || store.saved[1].Parts[1].Text != wantMark {
		t.Fatalf("落盘历史中的图片应降级为文本标记: %+v", store.saved[1].Parts)
	}
	// 内存中的当前会话消息仍保留图片供本轮对话使用
	if w.history()[1].Parts[1].Type != ContentPartImageURL || w.history()[1].Parts[1].ImageURL != "https://example.com/a.png" {
		t.Fatalf("内存历史应保留原始图片: %+v", w.history()[1].Parts)
	}

	// 模拟重启：新建窗口并回放
	w2 := newMessageWindow(1000, nil, nil, store)
	w2.load(context.Background())
	if len(w2.history()) != 2 {
		t.Fatalf("回放后历史长度 = %d, want 2", len(w2.history()))
	}

	// 校验工具调用与推理内容均能正确还原
	got := w2.history()[1]
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "get_time" {
		t.Fatalf("工具调用未还原: %+v", got.ToolCalls)
	}
	if got.ToolCalls[0].ID != "call_1" || got.ToolCalls[0].Arguments != `{}` {
		t.Fatalf("工具调用字段未还原: %+v", got.ToolCalls[0])
	}
	if got.ReasoningContent != "推理过程" {
		t.Fatalf("推理内容未还原: %q", got.ReasoningContent)
	}
	// 回放后图片片段应为落盘时的带哈希文本标记
	if len(got.Parts) != 2 || got.Parts[1].Type != ContentPartText || got.Parts[1].Text != wantMark {
		t.Fatalf("回放后图片应为文本标记: %+v", got.Parts)
	}
}

// TestMessageWindowAppendIsIncremental 常规对话的落盘应为增量追加：
// 两次 append 后存储内容为两次之和，且第二次 append 不影响已存的首条消息
// （行级存储下 Append 只插入新行，不重写历史）。
func TestMessageWindowAppendIsIncremental(t *testing.T) {
	store := &fakeHistoryStore{}
	w := newMessageWindow(1000, nil, nil, store)

	w.append(TextMessage(RoleUser, "第一条"))
	w.append(TextMessage(RoleAssistant, "第二条"))

	if store.replaced != 0 {
		t.Fatalf("常规 append 不应触发 Replace, got %d", store.replaced)
	}
	if len(store.saved) != 2 {
		t.Fatalf("存储消息数 = %d, want 2", len(store.saved))
	}
	if store.saved[0].Parts[0].Text != "第一条" || store.saved[1].Parts[0].Text != "第二条" {
		t.Fatalf("增量追加内容不符: %+v", store.saved)
	}
}

func TestMessageWindowClearDeletesStore(t *testing.T) {
	store := &fakeHistoryStore{}
	w := newMessageWindow(1000, nil, nil, store)
	w.append(TextMessage(RoleUser, "hi"))

	if err := w.clear(context.Background()); err != nil {
		t.Fatalf("clear 失败: %v", err)
	}

	if !store.cleared {
		t.Fatal("clear 未触发存储删除")
	}
	if len(store.saved) != 0 {
		t.Fatalf("clear 后存储非空: %d", len(store.saved))
	}
	// 模拟重启回放，应为空
	w2 := newMessageWindow(1000, nil, nil, store)
	w2.load(context.Background())
	if len(w2.history()) != 0 {
		t.Fatalf("清除后回放非空: %d", len(w2.history()))
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	orig := []Message{
		TextMessage(RoleSystem, "系统提示"),
		TextMessage(RoleUser, "用户消息"),
		{
			Role: RoleAssistant,
			Parts: []ContentPart{
				TextPart("回复"),
				ImageURLPart("https://example.com/b.png"),
			},
			ToolCalls: []llmtool.ToolCall{
				{ID: "id1", Name: "web_search", Arguments: `{"q":"x"}`},
			},
			ReasoningContent: "思考",
		},
		{Role: RoleTool, ToolCallID: "id1", Parts: []ContentPart{TextPart("结果")}},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got []Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got) != len(orig) {
		t.Fatalf("消息数 = %d, want %d", len(got), len(orig))
	}
	// 抽查 assistant 消息的工具调用与图片片段
	asst := got[2]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Name != "web_search" {
		t.Fatalf("工具调用还原错误: %+v", asst.ToolCalls)
	}
	if asst.ReasoningContent != "思考" {
		t.Fatalf("推理内容还原错误: %q", asst.ReasoningContent)
	}
	if len(asst.Parts) != 2 || asst.Parts[1].Type != ContentPartImageURL {
		t.Fatalf("图片片段还原错误: %+v", asst.Parts)
	}
	// 抽查 tool 消息的 ToolCallID
	if got[3].ToolCallID != "id1" {
		t.Fatalf("ToolCallID 还原错误: %q", got[3].ToolCallID)
	}
}

func TestPersistDegradesDataURI(t *testing.T) {
	// data URI（base64 内联图片）体积可达 MB 级，落盘副本必须剔除，
	// 但内存中当前会话的消息应保留供本轮对话使用
	store := &fakeHistoryStore{}
	w := newMessageWindow(1000, nil, nil, store)

	w.append(Message{
		Role: RoleUser,
		Parts: []ContentPart{
			TextPart("看这张本地图"),
			ImageURLPart("data:image/png;base64,iVBORw0KGgo="),
		},
	})

	wantMark := "[图片 " + message.ImageHash("data:image/png;base64,iVBORw0KGgo=") + "]"
	if store.saved[0].Parts[1].Type != ContentPartText || store.saved[0].Parts[1].Text != wantMark {
		t.Fatalf("落盘副本的 data URI 应降级为文本标记: %+v", store.saved[0].Parts[1])
	}
	if w.history()[0].Parts[1].Type != ContentPartImageURL {
		t.Fatalf("内存消息应保留 data URI 图片: %+v", w.history()[0].Parts[1])
	}
	// 落盘降级不应修改原消息切片
	if w.history()[0].Parts[1].ImageURL != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("原消息被落盘降级修改: %+v", w.history()[0].Parts[1])
	}
}

func TestDegradeStripsInlineDataURIFromText(t *testing.T) {
	// 旧版本把适配器内联 data URI 完整写进 [图片 <hash> url:...] 标记并随之落盘，
	// MB 级 base64 会在回放与压缩重写时反复进入上下文；落盘与回放都应清洗
	uri := "data:image/png;base64,iVBORw0KGgo="
	mark := "[图片 " + message.ImageHash(uri) + " url:" + uri + "]"
	msg := TextMessage(RoleUser, "看图 "+mark)
	want := "看图 [图片 " + message.ImageHash(uri) + "]"

	got := degradeImagesForPersist([]Message{msg})[0]
	if got.Parts[0].Text != want {
		t.Fatalf("落盘文本应剔除内联 data URI:\n got %q\nwant %q", got.Parts[0].Text, want)
	}
	// 原消息不被修改
	if msg.Parts[0].Text != "看图 "+mark {
		t.Fatalf("原消息被修改: %q", msg.Parts[0].Text)
	}

	// 回放路径同样清洗（兼容旧版本已落盘的数据）
	store := &fakeHistoryStore{saved: []Message{msg}}
	w := newMessageWindow(1000, nil, nil, store)
	w.load(context.Background())
	if w.history()[0].Parts[0].Text != want {
		t.Fatalf("回放文本应剔除内联 data URI:\n got %q\nwant %q", w.history()[0].Parts[0].Text, want)
	}
}

// TestMaybeCompressRecordsUsage 压缩成功后其 token 用量被记录并可被取走
// （ChatBot.Chat 据此并入当次请求统计）；取走后清零，压缩失败不记录。
func TestMaybeCompressRecordsUsage(t *testing.T) {
	compressor := func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error) {
		return []Message{TextMessage(RoleUser, "[对话摘要]")},
			TokenUsage{PromptTokens: 500, CompletionTokens: 100, TotalTokens: 600}, nil
	}
	w := newMessageWindow(1000, &LLMClient{}, compressor, &fakeHistoryStore{})
	for i := range 4 {
		w.append(TextMessage(RoleUser, fmt.Sprintf("消息%d", i)))
	}
	w.RecordUsage(TokenUsage{LastPromptTokens: 900}) // 超阈值触发压缩

	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := w.takeCompressUsage()
	if u.PromptTokens != 500 || u.CompletionTokens != 100 || u.TotalTokens != 600 {
		t.Fatalf("压缩用量不符: %+v", u)
	}
	if u2 := w.takeCompressUsage(); u2.TotalTokens != 0 {
		t.Fatalf("取走后应清零, got %+v", u2)
	}
}

func TestMaybeCompressFailureDegradesToTruncation(t *testing.T) {
	// 压缩失败（网络抖动/限流）时不得阻断对话：降级丢弃最旧一半历史并返回 nil，
	// 保证本轮用户消息能正常处理与落盘
	store := &fakeHistoryStore{}
	failCompressor := func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error) {
		return nil, TokenUsage{}, fmt.Errorf("compress failed")
	}
	w := newMessageWindow(1000, &LLMClient{}, failCompressor, store)
	for i := range 8 {
		w.append(TextMessage(RoleUser, fmt.Sprintf("消息%d", i)))
	}
	w.RecordUsage(TokenUsage{LastPromptTokens: 900}) // 超阈值触发压缩

	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatalf("压缩失败应降级截断而不是返回错误: %v", err)
	}
	// 截断到最近一半（4 条），且保留最新消息
	if len(w.history()) != 4 {
		t.Fatalf("降级截断后历史长度 = %d, want 4", len(w.history()))
	}
	last := w.history()[len(w.history())-1]
	if last.Parts[0].Text != "消息7" {
		t.Fatalf("应保留最新消息，got %q", last.Parts[0].Text)
	}
	// 截断后应同步落盘（Replace 全量覆盖）
	if len(store.saved) != 4 {
		t.Fatalf("降级截断后落盘长度 = %d, want 4", len(store.saved))
	}
	if store.replaced != 1 {
		t.Fatalf("降级截断应走一次 Replace, got %d", store.replaced)
	}
}

// TestTruncateOldestHalfAlignsToolCallBoundary 回归：降级截断不得把保留区的
// 第一条留在孤立的 tool 结果消息上（其 assistant tool_calls 在被丢弃的一半里），
// 否则 OpenAI 兼容 API 拒绝（400）且此后每轮请求都失败。
func TestTruncateOldestHalfAlignsToolCallBoundary(t *testing.T) {
	store := &fakeHistoryStore{}
	failCompressor := func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error) {
		return nil, TokenUsage{}, fmt.Errorf("compress failed")
	}
	w := newMessageWindow(1000, &LLMClient{}, failCompressor, store)

	// 历史：user, user, assistant(tool_calls), tool 结果, assistant, user
	// 丢弃最旧一半（3 条）后切点恰落在 tool 结果消息上
	w.append(TextMessage(RoleUser, "消息0"))
	w.append(TextMessage(RoleUser, "消息1"))
	w.append(Message{
		Role:      RoleAssistant,
		ToolCalls: []llmtool.ToolCall{{ID: "c1", Name: "time", Arguments: "{}"}},
	})
	w.append(Message{Role: RoleTool, ToolCallID: "c1", Parts: []ContentPart{TextPart("工具结果")}})
	w.append(TextMessage(RoleAssistant, "好的"))
	w.append(TextMessage(RoleUser, "消息5"))
	w.RecordUsage(TokenUsage{LastPromptTokens: 900})

	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatalf("压缩失败应降级截断而不是返回错误: %v", err)
	}
	hist := w.history()
	if len(hist) == 0 {
		t.Fatal("截断后不应为空（切点后有合法消息）")
	}
	if hist[0].Role == RoleTool {
		t.Fatalf("保留区首条不得是孤立 tool 消息: %+v", hist[0])
	}
	// 孤立 tool 消息应被一并跳过：保留区 = [assistant(好的), user(消息5)]
	if len(hist) != 2 || hist[0].Role != RoleAssistant || hist[1].Role != RoleUser {
		t.Fatalf("切点未对齐 tool_call 边界: %+v", hist)
	}
}

// TestTruncateOldestHalfAllToolMessages 极端场景：保留区全是孤立 tool 消息时
// 全部跳过（清空历史），保证后续请求合法。
func TestTruncateOldestHalfAllToolMessages(t *testing.T) {
	store := &fakeHistoryStore{}
	failCompressor := func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error) {
		return nil, TokenUsage{}, fmt.Errorf("compress failed")
	}
	w := newMessageWindow(1000, &LLMClient{}, failCompressor, store)

	w.append(TextMessage(RoleUser, "消息0"))
	w.append(TextMessage(RoleUser, "消息1"))
	w.append(Message{Role: RoleTool, ToolCallID: "cA", Parts: []ContentPart{TextPart("工具结果A")}})
	w.append(Message{Role: RoleTool, ToolCallID: "cB", Parts: []ContentPart{TextPart("工具结果B")}})
	w.RecordUsage(TokenUsage{LastPromptTokens: 900})

	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(w.history()) != 0 {
		t.Fatalf("保留区全是孤立 tool 消息时应清空历史, got %+v", w.history())
	}
}

// TestMaybeCompressUsesCompressorClient 指定压缩器客户端时压缩走独立 client，
// 未指定时复用主对话 client。
func TestMaybeCompressUsesCompressorClient(t *testing.T) {
	mainClient := &LLMClient{model: "main"}
	compressorClient := &LLMClient{model: "compressor"}

	var got *LLMClient
	recordCompressor := func(ctx context.Context, client *LLMClient, oldMsgs []Message) ([]Message, TokenUsage, error) {
		got = client
		return []Message{TextMessage(RoleUser, "[对话摘要]")}, TokenUsage{}, nil
	}
	makeWindow := func(w *messageWindow) {
		for i := range 4 {
			w.append(TextMessage(RoleUser, fmt.Sprintf("消息%d", i)))
		}
		w.RecordUsage(TokenUsage{LastPromptTokens: 900}) // 超阈值触发压缩
	}

	// 指定了独立压缩器 client：压缩请求应发给它
	w := newMessageWindow(1000, mainClient, recordCompressor, &fakeHistoryStore{}, compressorClient)
	makeWindow(w)
	if err := w.MaybeCompress(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != compressorClient {
		t.Fatalf("压缩 client = %v, want compressorClient", got)
	}

	// 未指定：压缩复用主对话 client
	got = nil
	w2 := newMessageWindow(1000, mainClient, recordCompressor, &fakeHistoryStore{})
	makeWindow(w2)
	if err := w2.MaybeCompress(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != mainClient {
		t.Fatalf("压缩 client = %v, want mainClient", got)
	}
}

func TestNeedsCompressionFallbackWithoutUsage(t *testing.T) {
	// 上游未上报 usage（lastPromptTokens == 0）时，字符数粗估超阈值也应触发压缩
	w := newMessageWindow(100, nil, nil, nil)
	if w.needsCompression() {
		t.Fatal("空历史不应触发压缩")
	}
	// 阈值 = 100 * 0.8 = 80 token ≈ 160 字符
	w.append(TextMessage(RoleUser, strings.Repeat("啊", 200)))
	if !w.needsCompression() {
		t.Fatal("usage 缺失时字符数超阈值应触发压缩")
	}

	// 上报了真实 usage 时以 usage 为准：字符数超阈值但 usage 很低则不压缩
	w2 := newMessageWindow(100, nil, nil, nil)
	w2.append(TextMessage(RoleUser, strings.Repeat("啊", 200)))
	w2.RecordUsage(TokenUsage{LastPromptTokens: 10})
	if w2.needsCompression() {
		t.Fatal("已有真实 usage 时不应走字符兜底")
	}
}
