package aitool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeHistoryBot 测试桩：实现历史消息工具所需的基础接口（GetGroupMsgHistory /
// GetFriendMsgHistory），按预设页返回并记录调用参数。
type fakeHistoryBot struct {
	bot.Bot
	pages    [][]message.Message // 每页按调用次序返回（模拟最新在前的分页）
	calls    []int               // 每次调用的 (count, seq) 记录，下标与 pages 对应
	lastSeqs []int
	group    bool // true=群接口，false=好友接口
	fail     bool // true=模拟平台不支持（接口返回 false）
}

func (f *fakeHistoryBot) GetGroupMsgHistory(groupId message.QID, count int, seq int) (*[]message.Message, bool) {
	if !f.group || f.fail {
		return nil, false
	}
	return f.page(count, seq)
}

func (f *fakeHistoryBot) GetFriendMsgHistory(userId message.QID, count int, seq int) (*[]message.Message, bool) {
	if f.group || f.fail {
		return nil, false
	}
	return f.page(count, seq)
}

func (f *fakeHistoryBot) page(count int, seq int) (*[]message.Message, bool) {
	idx := len(f.calls)
	f.calls = append(f.calls, count)
	f.lastSeqs = append(f.lastSeqs, seq)
	if idx >= len(f.pages) {
		return &[]message.Message{}, true
	}
	list := append([]message.Message(nil), f.pages[idx]...)
	return &list, true
}

func historyCtx(b bot.Bot, sink MessageSink) Context {
	return Context{Bot: b, Target: message.QID("qq:888"), IsGroup: true, OnMessages: sink}
}

// mkMsg 构造带发送者与游标的消息。
func mkMsg(seq int, sender string) message.Message {
	return message.Message{MessageSeq: seq, Sender: message.MessageSender{UserId: message.QID(sender)}}
}

// mkTimedMsg 构造带时间戳的消息（seq 与时间是同向递增的，便于按时间断言）。
func mkTimedMsg(seq, ts int, sender string) message.Message {
	return message.Message{MessageSeq: seq, Time: uint(ts), Sender: message.MessageSender{UserId: message.QID(sender)}}
}

func callHistory(t *testing.T, tool Tool, args string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), paramsOf(t, tool, args))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	return out
}

// paramsOf 模拟框架的参数解析路径：JSON 实参 → Params() 同型结构体指针。
func paramsOf(t *testing.T, tool Tool, args string) any {
	t.Helper()
	p := tool.Params()
	if args == "" {
		return p
	}
	if err := json.Unmarshal([]byte(args), p); err != nil {
		t.Fatalf("参数解析失败: %v", err)
	}
	return p
}

func TestHistoryNoFilter(t *testing.T) {
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		{mkMsg(105, "qq:1"), mkMsg(104, "qq:2"), mkMsg(103, "qq:1")},
	}}
	tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

	out := callHistory(t, tool, `{"count":3}`)
	if !strings.Contains(out, "[message_seq:105]") || !strings.Contains(out, "[message_seq:103]") {
		t.Errorf("输出缺少消息游标:\n%s", out)
	}
	if fake.lastSeqs[0] != 0 {
		t.Errorf("首次调用应从最新开始（seq=0）, got %d", fake.lastSeqs[0])
	}
}

func TestHistoryFilterByUser(t *testing.T) {
	// 两页数据：第一页只有 1 条命中，第二页 2 条；凑够 3 条即止
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		{mkMsg(60, "qq:1"), mkMsg(59, "qq:2"), mkMsg(58, "qq:2"), mkMsg(57, "qq:2")},
		{mkMsg(56, "qq:1"), mkMsg(55, "qq:1"), mkMsg(54, "qq:2"), mkMsg(53, "qq:2"), mkMsg(52, "qq:2")},
	}}
	setTestPageSize(t, 4)
	tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

	out := callHistory(t, tool, `{"count":3,"user_id":"1"}`)
	if !strings.Contains(out, "已回溯最近 9 条消息") {
		t.Errorf("应注明回溯总量:\n%s", out)
	}
	if !strings.Contains(out, "3 条来自 qq:1") {
		t.Errorf("应注明命中数:\n%s", out)
	}
	if strings.Count(out, "[message_seq:") != 3 {
		t.Errorf("应只输出命中的 3 条:\n%s", out)
	}
	if !strings.Contains(out, "[message_seq:56]") || !strings.Contains(out, "[message_seq:60]") {
		t.Errorf("命中消息缺失:\n%s", out)
	}
	// 第二次调用游标应为第一页最后一条的 seq（57）
	if fake.lastSeqs[1] != 57 {
		t.Errorf("翻页游标应为 57, got %d", fake.lastSeqs[1])
	}
	// 凑够即止：不应有"继续回溯"提示
	if strings.Contains(out, "继续向前筛选") {
		t.Errorf("已凑够命中数时不应提示继续翻页:\n%s", out)
	}
}

func TestHistoryFilterNotFound(t *testing.T) {
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		{mkMsg(5, "qq:2"), mkMsg(4, "qq:2")},
	}}
	tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

	out := callHistory(t, tool, `{"user_id":"qq:999"}`)
	if !strings.Contains(out, "没有找到 qq:999 的发言") {
		t.Errorf("未命中应如实提示:\n%s", out)
	}
	if !strings.Contains(out, "已回溯最近 2 条") {
		t.Errorf("应注明回溯总量:\n%s", out)
	}
}

// TestHistoryFilterCrossPrefix 筛选 ID 容忍 qq:/lil:/纯数字写法，统一按会话前缀匹配。
func TestHistoryFilterCrossPrefix(t *testing.T) {
	pages := [][]message.Message{{mkMsg(5, "qq:2"), mkMsg(4, "qq:1")}}
	for _, input := range []string{`{"user_id":"lil:2"}`, `{"user_id":"qq:2"}`, `{"user_id":"2"}`} {
		// 每次调用用全新桩：仅验证 ID 归一化，与分页状态无关
		fake := &fakeHistoryBot{group: true, pages: pages}
		tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")
		out := callHistory(t, tool, input)
		if !strings.Contains(out, "1 条来自 qq:2") {
			t.Errorf("输入 %s 应命中 qq:2:\n%s", input, out)
		}
	}
}

// TestHistoryPaginationStopConditions 游标不前进 / 返回不足一页时应停止翻页，
// 防止对不支持分页的平台（内存缓存兜底）无限回溯。
func TestHistoryPaginationStopConditions(t *testing.T) {
	// 平台忽略游标：第二页返回与第一页相同的内容（游标不前进 → 停止）
	stub := &fakeHistoryBot{group: true, pages: [][]message.Message{
		{mkMsg(3, "qq:2"), mkMsg(2, "qq:2"), mkMsg(1, "qq:1")},
		{mkMsg(3, "qq:2"), mkMsg(2, "qq:2"), mkMsg(1, "qq:1")},
	}}
	setTestPageSize(t, 3)
	tool := NewMsgHistoryTool(historyCtx(stub, nil), "qq_get_msg_history")
	out := callHistory(t, tool, `{"user_id":"2"}`)
	if !strings.Contains(out, "已回溯最近 6 条") {
		t.Errorf("游标不前进时应停止翻页并汇总已回溯量:\n%s", out)
	}
	if len(stub.calls) != 2 {
		t.Errorf("游标不前进时应立即停止翻页, calls=%d", len(stub.calls))
	}
	if strings.Contains(out, "继续向前筛选") {
		t.Errorf("已到最早消息时不应提示继续翻页:\n%s", out)
	}

	// 末页不足 page size：视为已到最早消息，不再翻页
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		{mkMsg(2, "qq:2"), mkMsg(1, "qq:1")},
	}}
	setTestPageSize(t, 50)
	tool2 := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")
	out2 := callHistory(t, tool2, `{"user_id":"1"}`)
	if !strings.Contains(out2, "[message_seq:1]") {
		t.Errorf("末页命中应返回:\n%s", out2)
	}
	if len(fake.calls) != 1 {
		t.Errorf("不足一页时应停止翻页, calls=%d", len(fake.calls))
	}
}

// setTestPageSize 临时缩小筛选分页大小（页大小常量对真实平台为 50，
// 测试用小页模拟多轮回溯），测试结束后恢复。
func setTestPageSize(t *testing.T, n int) {
	t.Helper()
	old := historyPageSize
	historyPageSize = n
	t.Cleanup(func() { historyPageSize = old })
}

// TestHistoryBackwardCursorByTimeNotPosition 续查游标必须按时间取最旧一条，不能按
// 数组位置：QQ 协议端修复后按时间升序返回（最旧在最前），此时取末位会拿到最新一条，
// 翻页原地踏步（NapCat issue #441 的现象）；内存缓存类适配器则是最新在前。
// 两种返回顺序都必须选中同一条最旧消息（seq=57）。
func TestHistoryBackwardCursorByTimeNotPosition(t *testing.T) {
	// 复用同一批消息的两种排列：升序（最旧在前）与倒序（最新在前）
	ascending := []message.Message{
		mkTimedMsg(57, 5700, "qq:2"), mkTimedMsg(58, 5800, "qq:2"),
		mkTimedMsg(59, 5900, "qq:2"), mkTimedMsg(60, 6000, "qq:1"),
	}
	descending := []message.Message{
		mkTimedMsg(60, 6000, "qq:1"), mkTimedMsg(59, 5900, "qq:2"),
		mkTimedMsg(58, 5800, "qq:2"), mkTimedMsg(57, 5700, "qq:2"),
	}
	for name, firstPage := range map[string][]message.Message{"升序返回": ascending, "倒序返回": descending} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
				firstPage,
				// 第二页只有一条命中，且不足一页 → 翻页到此结束
				{mkTimedMsg(56, 5600, "qq:1"), mkTimedMsg(55, 5500, "qq:2")},
			}}
			setTestPageSize(t, 4)
			tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

			// 首条命中只凑够 1 条（count=2），必然继续发起第二次请求
			out := callHistory(t, tool, `{"count":2,"user_id":"qq:1"}`)
			if len(fake.lastSeqs) < 2 {
				t.Fatalf("应继续向前翻页, lastSeqs=%v", fake.lastSeqs)
			}
			if fake.lastSeqs[1] != 57 {
				t.Errorf("续查游标应为最旧的 57（而非数组端点的 57/60）, got %d", fake.lastSeqs[1])
			}
			if !strings.Contains(out, "[message_seq:60]") {
				t.Errorf("首条命中应为最新的 60:\n%s", out)
			}
		})
	}
}

// TestHistoryFilterCursorStopsWhenNotAdvancing 平台忽略游标（或已到最早消息）时，
// 最旧一条与传入游标相同 → 停止翻页，不得无限循环。
func TestHistoryFilterCursorStopsWhenNotAdvancing(t *testing.T) {
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		// 平台忽略游标：两次都返回同一批
		{mkTimedMsg(60, 6000, "qq:2"), mkTimedMsg(59, 5900, "qq:2"), mkTimedMsg(58, 5800, "qq:2"), mkTimedMsg(57, 5700, "qq:2")},
		{mkTimedMsg(60, 6000, "qq:2"), mkTimedMsg(59, 5900, "qq:2"), mkTimedMsg(58, 5800, "qq:2"), mkTimedMsg(57, 5700, "qq:2")},
	}}
	setTestPageSize(t, 4)
	tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

	out := callHistory(t, tool, `{"user_id":"qq:9"}`)
	if len(fake.calls) > 2 {
		t.Errorf("游标不前进时应尽快停止, calls=%d", len(fake.calls))
	}
	if !strings.Contains(out, "没有找到") {
		t.Errorf("未命中应如实提示:\n%s", out)
	}
}

// TestHistoryFilterCursorWithinSameSecond QQ 时间戳是秒级，页内可能多条落在同一秒：
// 报文顺序即真实先后（升序页里同秒组由旧到新），必须选中该秒组内最旧的一条。
// 若只按 Time 取最小值而忽略同秒内的先后，会拿到同秒里较新的一条，续查窗口随之
// 缩水。该场景只出现在填充 message_seq 的 QQ 协议端（返回时间升序）。
func TestHistoryFilterCursorWithinSameSecond(t *testing.T) {
	// 首条 5000 与末条 4000 之间夹着同秒组，整页升序
	page := []message.Message{
		mkTimedMsg(57, 5000, "qq:2"), // 同秒组内最旧
		mkTimedMsg(58, 5000, "qq:2"),
		mkTimedMsg(59, 5000, "qq:2"),
	}
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		page,
		{mkTimedMsg(56, 4000, "qq:1")}, // 第二页命中且不足一页 → 结束
	}}
	setTestPageSize(t, 3) // 首页须满一页才会继续回溯
	tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

	callHistory(t, tool, `{"count":1,"user_id":"qq:1"}`)
	if len(fake.lastSeqs) < 2 {
		t.Fatalf("应继续向前翻页, lastSeqs=%v", fake.lastSeqs)
	}
	if fake.lastSeqs[1] != 57 {
		t.Errorf("同秒页的续查游标应为同秒组内最旧的 57, got %d", fake.lastSeqs[1])
	}
}

// TestHistoryNoFilterSortsAscendingAndHintsCursor 无筛选单页：输出按时间升序
// （最早在最上面），并在开头给出本页最早一条的游标，AI 无需从位置推断翻页入口。
func TestHistoryNoFilterSortsAscendingAndHintsCursor(t *testing.T) {
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		{mkTimedMsg(105, 1050, "qq:1"), mkTimedMsg(104, 1040, "qq:2"), mkTimedMsg(103, 1030, "qq:1")},
	}}
	tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

	out := callHistory(t, tool, `{"count":3}`)
	if !strings.Contains(out, "最早一条的 message_seq=103") {
		t.Errorf("应给出续查游标提示:\n%s", out)
	}
	// 升序：103 在 105 之前
	if strings.Index(out, "[message_seq:103]") > strings.Index(out, "[message_seq:105]") {
		t.Errorf("输出应按时间升序（旧的在前）:\n%s", out)
	}
}

// TestHistoryFilterDedupAcrossPages 反向翻页时锚点消息常同时出现在相邻两页，
// 命中的重复条目应去重，避免同一条发言重复占满结果。
func TestHistoryFilterDedupAcrossPages(t *testing.T) {
	// 两页在 seq=57 处重叠，且它来自被筛选成员
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{
		{mkTimedMsg(60, 6000, "qq:2"), mkTimedMsg(59, 5900, "qq:2"), mkTimedMsg(58, 5800, "qq:2"), mkTimedMsg(57, 5700, "qq:1")},
		{mkTimedMsg(57, 5700, "qq:1"), mkTimedMsg(56, 5600, "qq:1"), mkTimedMsg(55, 5500, "qq:2"), mkTimedMsg(54, 5400, "qq:2")},
	}}
	setTestPageSize(t, 4)
	tool := NewMsgHistoryTool(historyCtx(fake, nil), "qq_get_msg_history")

	out := callHistory(t, tool, `{"count":2,"user_id":"1"}`)
	if got := strings.Count(out, "[message_seq:57]"); got != 1 {
		t.Errorf("跨页重叠的命中消息应去重（出现 %d 次）:\n%s", got, out)
	}
	if !strings.Contains(out, "2 条来自 qq:1") {
		t.Errorf("应命中 2 条（57、56）:\n%s", out)
	}
}

// TestHistoryImageSink 拉取到的消息应回调 OnMessages（宿主借此登记图片）。
func TestHistoryImageSink(t *testing.T) {
	msgs := []message.Message{mkMsg(5, "qq:1")}
	var sunk [][]message.Message
	fake := &fakeHistoryBot{group: true, pages: [][]message.Message{msgs}}
	ctx := historyCtx(fake, func(_ context.Context, m []message.Message) {
		sunk = append(sunk, m)
	})
	tool := NewMsgHistoryTool(ctx, "qq_get_msg_history")

	callHistory(t, tool, "")
	if len(sunk) != 1 || len(sunk[0]) != 1 {
		t.Fatalf("OnMessages 应收到拉取的消息, got %+v", sunk)
	}
}

// TestHistoryPrivateAndUnsupported 私聊会话走好友接口；平台不支持时如实报错。
func TestHistoryPrivateAndUnsupported(t *testing.T) {
	fake := &fakeHistoryBot{pages: [][]message.Message{{mkMsg(5, "qq:9")}}}
	ctx := Context{Bot: fake, Target: message.QID("qq:123"), IsGroup: false}
	tool := NewMsgHistoryTool(ctx, "qq_get_msg_history")
	out := callHistory(t, tool, "")
	if !strings.Contains(out, "[message_seq:5]") {
		t.Errorf("私聊应走好友接口并返回消息:\n%s", out)
	}

	unsupported := &fakeHistoryBot{group: true, fail: true} // 群会话 + 群接口失败
	tool2 := NewMsgHistoryTool(Context{Bot: unsupported, Target: "qq:1", IsGroup: true}, "qq_get_msg_history")
	if _, err := tool2.Execute(context.Background(), paramsOf(t, tool2, "")); err == nil {
		t.Fatal("平台不支持历史查询时应报错")
	}
}
