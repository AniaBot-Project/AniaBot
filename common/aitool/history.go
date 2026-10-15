package aitool

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 历史消息查看工具：平台适配器把「查看会话历史消息」作为平台工具提供给 AI，
// 支持按群成员筛选发言。工具经 bot.Bot 的基础历史接口（GetGroupMsgHistory /
// GetFriendMsgHistory，所有适配器契约必备）拉取，实际可用性由适配器决定——
// 不支持的平台调用时返回失败文本；提供方按平台决定是否把本工具注入会话。
//
// 原 functool.get_msg_history（经 CallBackFuncs 回调实现）已删除，本工具是其
// 平台工具化的替代：格式化逻辑（message_seq 游标 + FriendlyText + 内嵌图片
// 哈希标注）收敛为此处的唯一实现，图片登记经 Context.OnMessages 交还宿主。

const (
	// historyDefaultCount 默认查看条数（与原 get_msg_history 语义一致）
	historyDefaultCount = 10
	// historyMaxCount 单次返回条数上限：防止把海量消息拼进工具结果撑大上下文
	historyMaxCount = 30
	// historyMaxFetched 筛选模式单次调用的最大回溯条数：限制耗时与上下文开销，
	// 未筛够时提示 AI 带游标续查
	historyMaxFetched = 400
)

// historyPageSize 筛选模式下每次回溯的页大小（var 供测试按需缩小以模拟分页）。
var historyPageSize = 50

// MsgHistoryParams 历史消息工具参数。
type MsgHistoryParams struct {
	Count      int    `json:"count,omitempty" desc:"希望查看的消息条数，默认 10，最大 30；筛选成员时表示希望命中的条数"`
	UserID     string `json:"user_id,omitempty" desc:"只看该成员的消息（填消息开头 [nickname:昵称 id:用户ID] 中的 id）。填写后从最近消息向前回溯筛选，耗时会随回溯范围增加"`
	MessageSeq int    `json:"message_seq,omitempty" desc:"翻页游标：填写某条消息的 message_seq 即从该消息之前的历史开始；不填默认从最新消息开始"`
}

// msgHistoryTool 历史消息查看工具（读工具，无副作用）。
type msgHistoryTool struct {
	ctx  Context
	name string
}

// NewMsgHistoryTool 构造历史消息查看工具。name 由提供方按平台前缀命名
// （如 qq_get_msg_history、tg_get_msg_history）。
func NewMsgHistoryTool(ctx Context, name string) Tool {
	return &msgHistoryTool{ctx: ctx, name: name}
}

func (t *msgHistoryTool) Name() string { return t.name }
func (t *msgHistoryTool) Description() string {
	return "查看当前会话的历史消息记录，消息按时间从旧到新排列；可向前翻页：把当前结果中最早一条" +
		"（最上面）的 message_seq 作为游标传入即可继续查看更早的消息。" +
		"支持按群成员筛选：user_id 填某成员 ID 时只返回其在最近消息中的发言。" +
		"消息中的图片以 [图片 <hash> url:<url>] 标识，可用 load_images 按哈希查看。"
}
func (t *msgHistoryTool) Params() any { return &MsgHistoryParams{} }

func (t *msgHistoryTool) Execute(ctx context.Context, params any) (string, error) {
	p, ok := params.(*MsgHistoryParams)
	if !ok {
		return "", fmt.Errorf("参数类型错误")
	}
	count := p.Count
	if count <= 0 {
		count = historyDefaultCount
	}
	if count > historyMaxCount {
		count = historyMaxCount
	}

	// 无筛选：单页拉取，行为与原 get_msg_history 一致
	if strings.TrimSpace(p.UserID) == "" {
		msgs, err := t.fetch(ctx, count, p.MessageSeq)
		if err != nil {
			return "", err
		}
		list := *msgs
		out := FormatMessages(sortByTime(list), t.ctx.Bot)
		// 显式给出本页最早一条的游标：AI 不必再从列表位置推断往哪传，
		// 也避免在返回顺序相反的协议端上取错端点。
		if oldest, ok := oldestInPage(list); ok {
			out = fmt.Sprintf("最近 %d 条消息（最早一条的 message_seq=%d，传回可继续向前查看更早消息）：\n%s",
				len(list), oldest, out)
		}
		return out, nil
	}

	// 按成员筛选：从游标（默认最新）处整页向前回溯，凑够 count 条命中即止
	userID, err := NormalizeID(p.UserID, t.ctx.Target)
	if err != nil {
		return "", err
	}
	var (
		matched   []message.Message
		seen      = map[int]struct{}{}
		fetched   int
		cursor    = p.MessageSeq
		exhausted bool
	)
	for len(matched) < count && fetched < historyMaxFetched {
		page, err := t.fetch(ctx, historyPageSize, cursor)
		if err != nil {
			return "", err
		}
		list := *page
		if len(list) == 0 {
			exhausted = true
			break
		}
		fetched += len(list)
		for _, m := range list {
			if m.Sender.UserId != userID {
				continue
			}
			// 相邻页可能以同一条消息为边界（反向翻页时锚点消息常同时出现在页首/页尾），
			// 按游标去重，避免同一条发言在结果里重复出现。
			if m.MessageSeq != 0 {
				if _, dup := seen[m.MessageSeq]; dup {
					continue
				}
				seen[m.MessageSeq] = struct{}{}
			}
			matched = append(matched, m)
		}
		// 续查游标必须取本页最旧一条：各协议端返回顺序不统一（QQ 协议端按时间
		// 升序、内存缓存类按时间降序），按数组位置取会在其中一种顺序下把游标取
		// 成最新一条，导致翻页原地踏步。
		next, ok := oldestInPage(list)
		if !ok || next == 0 || next == cursor || len(list) < historyPageSize {
			// 无游标可取、游标不前进（平台忽略游标或已到最早消息）或返回不足一页：无法继续回溯
			exhausted = true
			break
		}
		cursor = next
	}

	if len(matched) == 0 {
		msg := fmt.Sprintf("已回溯最近 %d 条消息，没有找到 %s 的发言。", fetched, userID)
		if !exhausted {
			msg += fmt.Sprintf("可带上 message_seq=%d 继续向前回溯，或稍后再试。", cursor)
		}
		return msg, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "已回溯最近 %d 条消息，其中 %d 条来自 %s：\n", fetched, len(matched), userID)
	sb.WriteString(FormatMessages(sortByTime(matched), t.ctx.Bot))
	if !exhausted && len(matched) < count {
		fmt.Fprintf(&sb, "\n（还有更早消息未回溯，可带上 message_seq=%d 继续向前筛选）", cursor)
	}
	return sb.String(), nil
}

// oldestInPage 返回本页最旧消息的 message_seq。
//
// 历史页的数组顺序随协议端变化（QQ 协议端按时间升序，内存缓存类按时间降序），因此
// 按时间取最旧，不能依赖数组端点。时间戳为秒级，一页可能整段落在同一秒：此时按页面
// 方向取该秒组的端点（升序取组内首条、降序取组内末条），选错会拿到同秒内较新的一条，
// 续查窗口以它为准虽不丢消息（调用方按游标去重）但会大幅缩小每次前进的距离。
// 整页时间完全相同时方向无从判断：按升序处理——填充 message_seq 的只有 NapCat 系
// 协议端，其返回按时间升序，正是该分支命中的场景。
// 全页缺失时间戳时回退旧实现的末位约定（假定最新在前）；最旧一条没有 sequence
// （返回 0）时 ok=false，调用方停止翻页。
func oldestInPage(msgs []message.Message) (int, bool) {
	if len(msgs) == 0 {
		return 0, false
	}
	timed := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Time != 0 {
			timed = append(timed, m)
		}
	}
	if len(timed) == 0 {
		if seq := msgs[len(msgs)-1].MessageSeq; seq != 0 {
			return seq, true
		}
		return 0, false
	}
	ascending := timed[0].Time <= timed[len(timed)-1].Time
	oldest := timed[0]
	for _, m := range timed[1:] {
		if m.Time < oldest.Time {
			oldest = m
		}
	}
	if !ascending {
		// 降序页：同秒组内更旧的排在更后面
		for i := len(timed) - 1; i >= 0; i-- {
			if timed[i].Time == oldest.Time {
				oldest = timed[i]
				break
			}
		}
	}
	if oldest.MessageSeq == 0 {
		return 0, false
	}
	return oldest.MessageSeq, true
}

// sortByTime 返回按时间升序（旧→新）排列的消息副本：历史页的数组顺序随协议端变化，
// 展示前统一排序，使「最早一条在最上面」成为稳定契约。时间缺失（0）的消息排在末尾，
// 比较关系因此是全序（不依赖排序算法的稳定性）。
func sortByTime(msgs []message.Message) []message.Message {
	out := append([]message.Message(nil), msgs...)
	sort.SliceStable(out, func(i, j int) bool {
		return sortKey(out[i].Time) < sortKey(out[j].Time)
	})
	return out
}

// sortKey 排序键：时间缺失（0）视为未知并排在末尾（取 uint 最大值）。
func sortKey(t uint) uint {
	if t == 0 {
		return ^uint(0)
	}
	return t
}

// fetch 按会话类型拉取一页历史消息，并在 OnMessages 回调非空时把消息交给宿主
// （登记图片供 load_images 按哈希加载；筛选未命中的消息同样登记，属于超集无害）。
func (t *msgHistoryTool) fetch(ctx context.Context, count, seq int) (*[]message.Message, error) {
	var (
		msgs *[]message.Message
		ok   bool
	)
	if t.ctx.IsGroup {
		msgs, ok = t.ctx.Bot.GetGroupMsgHistory(t.ctx.Target, count, seq)
	} else {
		msgs, ok = t.ctx.Bot.GetFriendMsgHistory(t.ctx.Target, count, seq)
	}
	if !ok || msgs == nil {
		return nil, fmt.Errorf("获取历史消息失败（当前平台可能不支持历史消息查询）")
	}
	if t.ctx.OnMessages != nil {
		t.ctx.OnMessages(ctx, *msgs)
	}
	return msgs, nil
}

// FormatMessages 把消息列表格式化为给 AI 阅读的历史文本：每条带 message_seq
// 游标（翻页用），正文经 FriendlyText 展开（引用/合并转发按能力展开），内嵌的
// 图片附件描述补充哈希标注（与 load_images 的加载标记一致）。
func FormatMessages(msgs []message.Message, b bot.Bot) string {
	opts := []message.MsgOptFunc{message.WithGetMsgFunc(b.GetMsgDetail)}
	if qb, ok := b.(bot.QQ); ok {
		opts = append(opts, message.WithGetForwardMsgFunc(qb.GetForwardMsg))
	}
	var sb strings.Builder
	for _, msg := range msgs {
		sb.WriteString(fmt.Sprintf("[message_seq:%d]\n", msg.MessageSeq))
		sb.WriteString(AnnotateEmbeddedImages(msg.FriendlyText(true, opts...)))
		sb.WriteString("\n")
	}
	return sb.String()
}

// ─────────────────────────────────────────────
// 文本内嵌图片附件：部分平台（如 QQ 官方聊天记录）把图片以文本描述形式内嵌在
// 消息里，适配器未拆段时在此兜底识别，保证展示带哈希标记、load_images 能加载。
// ─────────────────────────────────────────────

// embeddedImageRe 匹配文本内嵌的图片附件描述（与 qqofficial 适配器的拆段正则同源）。
var embeddedImageRe = regexp.MustCompile(`\[附件\d+\]\s*类型:([^\s]+)\s+(?:文件名:(\S+)\s+)?(?:尺寸:\d+x\d+\s+)?(?:大小:\S+\s+)?URL:(\S+)`)

// EmbeddedImage 文本内嵌的图片附件。
type EmbeddedImage struct {
	Filename string // 文件名（可为空）
	URL      string
}

// ScanEmbeddedImages 扫描文本内嵌的图片附件描述（仅图片类型）。
func ScanEmbeddedImages(text string) []EmbeddedImage {
	var out []EmbeddedImage
	for _, m := range embeddedImageRe.FindAllStringSubmatch(text, -1) {
		k := strings.ToLower(m[1])
		if k != "图片" && !strings.HasPrefix(k, "image") {
			continue
		}
		if m[3] == "" {
			continue
		}
		out = append(out, EmbeddedImage{Filename: m[2], URL: m[3]})
	}
	return out
}

// AnnotateEmbeddedImages 在文本内嵌的图片附件描述后补充 [图片 <hash> url:<url>]
// 标记，使 AI 能看到与 load_images 对应的图片哈希。
func AnnotateEmbeddedImages(text string) string {
	idx := embeddedImageRe.FindAllStringSubmatchIndex(text, -1)
	if len(idx) == 0 {
		return text
	}
	var sb strings.Builder
	last := 0
	for _, m := range idx {
		k := strings.ToLower(text[m[2]:m[3]])
		if k != "图片" && !strings.HasPrefix(k, "image") {
			continue
		}
		url := text[m[6]:m[7]]
		if url == "" {
			continue
		}
		sb.WriteString(text[last:m[1]])
		sb.WriteString(fmt.Sprintf(" [图片 %s url:%s]", message.ImageHash(url), url))
		last = m[1]
	}
	sb.WriteString(text[last:])
	return sb.String()
}
