package telegram

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
)

type telegramConfig struct {
	token       string
	apiBase     string
	proxy       string
	pollTimeout int
	parseMode   string // ""/"off"=纯文本；"html"=AI markdown 转 Telegram HTML 渲染；"markdown"=旧版 Markdown；"markdownv2"=MarkdownV2（原生模式失败降级纯文本）
}

// telegramAdapter Telegram 平台适配器：Bot API 长轮询。
// 事件投递为 at-least-once（崩溃时 offset 未推进的更新会重推），
// 以 update_id 在适配器级去重，core 层再按 message_id 兜底（见 EventKeyer）。
type telegramAdapter struct {
	mu      sync.Mutex
	trigger adapter.TriggerWrapper
	cfg     telegramConfig

	client *telegramClient
	// self 机器人自身信息（getMe 结果：ID/Username），连接成功后填充
	self *User

	// chatMemberCache "chatID:userID" -> mentionCache：出站 @ 段解析 @username 用
	chatMemberCache sync.Map
	// dedup 长轮询 update_id 幂等去重（at-least-once 重推拦截）
	dedup *updateDedup
	// msgCache 入站/出站消息内存缓存（Bot API 无消息查询/历史端点，GetMsgDetail/历史用它兜底）
	msgCache *msgCache

	connState string
	lastErr   string

	logger *slog.Logger
}

// mentionCache getChatMember 结果缓存（@username 解析）。
type mentionCache struct {
	username string
	at       time.Time
}

func NewAdapter(cfg *viper.Viper) *telegramAdapter {
	return &telegramAdapter{
		dedup:    newUpdateDedup(eventDedupTTL),
		msgCache: newMsgCache(msgCachePerChat, msgCacheMaxChats),
		logger:   slog.Default().With("adapter", "telegram"),
	}
}

func (a *telegramAdapter) Name() string     { return "telegram" }
func (a *telegramAdapter) Platform() string { return Platform }

// MessageKey 实现 adapter.EventKeyer：按消息 ID 去重。
// Telegram 的 message_id 仅在同一会话内唯一，消息 ID 已编码为 "tg:<chat>:<msgid>"，
// 重推的 update 内容一致，键稳定。
func (a *telegramAdapter) MessageKey(msg message.Message) (string, bool) {
	raw := msg.MessageId.TrimPrefix(idPrefix)
	if raw == "" {
		return "", false
	}
	return "msg:" + raw, true
}

// NoticeKey 实现 adapter.EventKeyer：成员变动/表情回应无稳定 ID，
// 由适配器级 update_id 去重兜底（core 对通知不做组合兜底键）。
func (a *telegramAdapter) NoticeKey(noticeType string, notice any) (string, bool) {
	return "", false
}

// SelfID 实现 adapter.SelfIDProvider：返回机器人自身 ID（tg:<user_id>）。
// core 用它兜底填充 msg.SelfId，使自消息过滤与 @ 提及检测生效——
// 提及判定为 at 段 Data["qq"] 与 SelfId 的精确字符串比较，必须同为 tg: 前缀。
func (a *telegramAdapter) SelfID() message.QID {
	return a.selfID()
}

// telegramSegments Telegram 出站支持的通用段类型；face/json/music/forward
// 在 sendChain 的 default 分支退化（有 text 键并入文本），core 会对发送这类段告警。
// keyboard 段由 sendChain 提取为 reply_markup（实现 adapter.InteractiveExt）。
var telegramSegments = []string{
	message.SegmentText, message.SegmentMention, message.SegmentImage,
	message.SegmentReply, message.SegmentFile, message.SegmentRecord,
	message.SegmentVideo, message.SegmentKeyboard,
}

// SupportedSegments 实现 adapter.SegmentSupport。
func (a *telegramAdapter) SupportedSegments() []string { return telegramSegments }

func (a *telegramAdapter) SetTrigger(trigger adapter.TriggerWrapper) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trigger = trigger
}

func (a *telegramAdapter) triggerOf() adapter.TriggerWrapper {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trigger
}

func (a *telegramAdapter) setStatus(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connState = state
	a.lastErr = detail
}

// AdapterStatus 返回连接状态（connecting/connected/reconnecting）与详情。
func (a *telegramAdapter) AdapterStatus() (string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connState == "" {
		return "connecting", ""
	}
	return a.connState, a.lastErr
}

// Connected 长轮询是否已就绪（供面板状态探针使用）。
func (a *telegramAdapter) Connected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState == "connected"
}

// selfID 返回框架内表示机器人自身的统一 ID（tg:<user_id>），未连接成功则为空。
func (a *telegramAdapter) selfID() message.QID {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.self == nil || a.self.ID == 0 {
		return ""
	}
	return message.QID(idPrefix + strconv.FormatInt(a.self.ID, 10))
}

// selfUsername 返回机器人自身 username（@提及自识别用），未连接成功则为空。
func (a *telegramAdapter) selfUsername() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.self == nil {
		return ""
	}
	return a.self.Username
}

func (a *telegramAdapter) loadConfig(v *viper.Viper) telegramConfig {
	timeout := v.GetInt("bot.telegram.polling.timeout")
	if timeout <= 0 {
		timeout = 30
	}
	return telegramConfig{
		token:       v.GetString("bot.telegram.token"),
		apiBase:     v.GetString("bot.telegram.api_base"),
		proxy:       v.GetString("bot.telegram.proxy"),
		pollTimeout: timeout,
		parseMode:   v.GetString("bot.telegram.parse_mode"),
	}
}

// parseMode 返回 Bot API 的 parse_mode 参数值：off/空 = ""（纯文本）、
// html = "HTML"（AI markdown 先经 markdownToTelegramHTML 转换为 Telegram HTML，
// 任意输入都不会解析失败）、markdown = "Markdown"（旧版：仅需转义 _ * [ ]，
// 词中下划线不解析）、markdownv2 = "MarkdownV2"（新版：转义严格）。
func (a *telegramAdapter) parseMode() string {
	switch a.cfg.parseMode {
	case "html":
		return "HTML"
	case "markdown":
		return "Markdown"
	case "markdownv2":
		return "MarkdownV2"
	}
	return ""
}

// renderText 按渲染模式转换待发送文本：html 模式把 AI markdown 源码转换为
// Telegram HTML；其余模式原样返回（markdown/markdownv2 交给 Telegram 原生解析）。
func (a *telegramAdapter) renderText(raw string) string {
	if a.cfg.parseMode == "html" {
		return markdownToTelegramHTML(raw)
	}
	return raw
}

// Serve 启动 Telegram 适配器（阻塞）：getMe 校验 → 长轮询循环。
func (a *telegramAdapter) Serve(v *viper.Viper) {
	a.cfg = a.loadConfig(v)
	if a.cfg.token == "" {
		a.setStatus("reconnecting", "未配置 bot.telegram.token，请在 Web 面板配置后重启")
		a.logger.Warn("Telegram 适配器未配置 Bot Token，无法启动")
		return
	}
	if a.cfg.apiBase == "" {
		a.cfg.apiBase = "https://api.telegram.org"
	}
	// 客户端超时需大于长轮询等待时间（getUpdates timeout=30s 时请求挂起最多 ~30s）
	a.client = newTelegramClient(a.cfg.token, a.cfg.apiBase, a.cfg.proxy,
		time.Duration(a.cfg.pollTimeout+30)*time.Second)

	// getMe：校验 token 并获取机器人自身信息（ID/username），指数退避无限重试。
	// Serve 返回即适配器死亡（core 不再拉起），连接失败不可早退。
	a.setStatus("connecting", "正在校验 Bot Token")
	ctx := context.Background()
	for delay := time.Second; ; {
		me, err := a.getMe(ctx)
		if err == nil {
			a.mu.Lock()
			a.self = me
			a.mu.Unlock()
			break
		}
		a.setStatus("reconnecting", "getMe 失败: "+err.Error())
		a.logger.Warn("Telegram getMe 失败，重试中", "error", err)
		time.Sleep(delay)
		if delay < 30*time.Second {
			delay *= 2
		}
	}
	a.logger.Info("Telegram 长轮询启动", "botId", a.self.ID, "username", "@"+a.self.Username)
	a.setStatus("connected", "")
	a.pollLoop(ctx)
}

// getMe 获取机器人自身信息。
func (a *telegramAdapter) getMe(ctx context.Context) (*User, error) {
	var res userResult
	if err := a.client.call(ctx, "getMe", nil, &res); err != nil {
		return nil, err
	}
	return &res.User, nil
}

// pollLoop 长轮询主循环：
// getUpdates(offset, timeout) → 逐更新同步去重 claim 并推进 offset（无条件，
// 重推的 update 也要推进，否则死循环重推）→ 已 claim 的更新异步翻译分发。
// 先 claim 后处理的 at-least-once 语义：重复投递被去重键拦截（同飞书 onReceive
// 模式），崩溃窗口内已 claim 未处理的消息可能丢失（有界，可接受）。
func (a *telegramAdapter) pollLoop(ctx context.Context) {
	offset := 0
	backoff := time.Second
	for {
		updates, err := a.getUpdates(ctx, offset, a.cfg.pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.setStatus("reconnecting", err.Error())
			a.logger.Warn("Telegram getUpdates 失败", "error", err)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		a.setStatus("connected", "")
		// 仅 Debug 记录每批拉取量：轮询空转时也会命中，Info 级别会刷满日志
		a.logger.Debug("Telegram getUpdates 成功", "batch", len(updates), "offset", offset)
		offset = a.processUpdates(updates, offset)
	}
}

// processUpdates 处理一批更新：同步去重 claim 并推进 offset，返回新 offset。
// offset 无条件推进（重推的 update 也要推进，否则 Telegram 永远重推死循环）；
// 已 claim 的更新异步分发——翻译（含图片下载）与插件链不进轮询循环，
// 避免阻塞 getUpdates 导致 Telegram 侧队列堆积。
func (a *telegramAdapter) processUpdates(updates []Update, offset int) int {
	for _, u := range updates {
		claimed := a.dedup.Claim(u.UpdateID)
		if u.UpdateID+1 > offset {
			offset = u.UpdateID + 1
		}
		if !claimed {
			a.logger.Debug("跳过重复投递的长轮询更新", "updateId", u.UpdateID)
			continue
		}
		go a.handleUpdate(&u)
	}
	return offset
}

// allowedUpdates 显式订阅的更新类型：与 handleUpdate 的分支一一对应。
// 不传 allowed_updates 时投递类型取决于 Telegram 侧持久化的上一次设置
// （默认集不含 message_reaction，且无法保证含 callback_query），显式声明
// 保证按钮回调一定送达。
var allowedUpdates = []string{
	"message", "channel_post", "my_chat_member",
	"message_reaction", "callback_query",
}

// getUpdates 长轮询拉取更新。
func (a *telegramAdapter) getUpdates(ctx context.Context, offset, timeout int) ([]Update, error) {
	params := map[string]any{
		"timeout":         timeout,
		"allowed_updates": allowedUpdates,
	}
	if offset > 0 {
		params["offset"] = offset
	}
	var res []Update
	if err := a.client.call(ctx, "getUpdates", params, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// ---------- 事件入站分发 ----------

// handleUpdate 分发一个已去重的更新（在独立 goroutine 中调用）。
func (a *telegramAdapter) handleUpdate(u *Update) {
	a.logger.Debug("Telegram 收到更新", "update_id", u.UpdateID,
		"message", u.Message != nil, "channel_post", u.ChannelPost != nil,
		"my_chat_member", u.MyChatMember != nil, "message_reaction", u.MessageReaction != nil)
	if m := u.Message; m != nil {
		a.handleMessage(m)
		return
	}
	if m := u.ChannelPost; m != nil {
		a.handleMessage(m)
		return
	}
	if mc := u.MyChatMember; mc != nil {
		a.handleMyChatMember(mc)
		return
	}
	if r := u.MessageReaction; r != nil {
		a.handleReaction(r)
		return
	}
	if cq := u.CallbackQuery; cq != nil {
		a.handleCallbackQuery(cq)
		return
	}
	// 其余更新类型（edited_message/chat_member/inline_query/...）不处理
}

// handleMessage 分发一条消息：服务消息（成员变动）优先，其余翻译为通用消息。
func (a *telegramAdapter) handleMessage(m *Message) {
	// 过滤其他机器人的消息，防止 bot 循环（同飞书过滤 bot 发送者）
	if m.From != nil && m.From.IsBot {
		return
	}
	// 入站消息只以 Debug 记录且不含正文：用户聊天内容不应进持久化日志，
	// 高流量群里 Info 级别的逐条记录也会冲掉面板的环形缓冲
	a.logger.Debug("Telegram 收到消息", "chat_id", m.Chat.ID, "chat_type", m.Chat.Type,
		"from_id", fromIDOf(m.From), "text_runes", len([]rune(m.Text)))
	// 服务消息：成员加入/离开。Telegram 仅当 bot 为管理员或关闭隐私模式时
	// 才投递其他成员的变动（平台限制），bot 自己相关变动走 my_chat_member 更新。
	if len(m.NewChatMembers) > 0 {
		a.emitMemberChange(m, "group_increase")
		return
	}
	if m.LeftChatMember != nil {
		a.emitMemberChange(m, "group_decrease")
		return
	}
	msg := a.updateToMessage(m)
	if msg == nil {
		// 翻译为空说明该消息不被支持（纯媒体/服务消息等），保留 Warn 便于排查；
		// 但只记长度不记正文，避免用户聊天内容进入持久化日志
		a.logger.Warn("Telegram 消息翻译为空，丢弃", "chat_id", m.Chat.ID, "chat_type", m.Chat.Type,
			"text_runes", len([]rune(m.Text)))
		return
	}
	a.msgCache.Push(chatIDRaw(m.Chat.ID), *msg)
	trig := a.triggerOf()
	switch msg.MessageType {
	case "private":
		if trig.OnFriendMsg != nil {
			trig.OnFriendMsg(*msg)
		}
	default:
		if trig.OnGroupMsg != nil {
			trig.OnGroupMsg(*msg)
		}
	}
}

// emitMemberChange 群成员变动通知（五件套填充：Time/PostType/NoticeType/SelfId/SetPlatform）。
func (a *telegramAdapter) emitMemberChange(m *Message, noticeType string) {
	trig := a.triggerOf()
	groupID := message.QID(idPrefix + chatIDRaw(m.Chat.ID))
	operatorID := ""
	if m.From != nil {
		operatorID = idPrefix + strconv.FormatInt(m.From.ID, 10)
	}
	now := uint(time.Now().Unix())
	switch noticeType {
	case "group_increase":
		if trig.OnGroupIncrease == nil {
			return
		}
		for _, u := range m.NewChatMembers {
			n := message.GroupIncreaseNotice{
				SubType:    "invite",
				GroupId:    groupID,
				OperatorId: message.QID(operatorID),
				UserId:     message.QID(idPrefix + strconv.FormatInt(u.ID, 10)),
			}
			n.Time = now
			n.PostType = "notice"
			n.NoticeType = "group_increase"
			n.SelfId = a.selfID()
			n.SetPlatform(Platform)
			trig.OnGroupIncrease(n)
		}
	case "group_decrease":
		if trig.OnGroupDecrease == nil {
			return
		}
		u := m.LeftChatMember
		if u == nil {
			return
		}
		// 操作者非离开者本人 → 被踢（kick），否则主动退群（leave）
		subType := "kick"
		if m.From != nil && m.From.ID == u.ID {
			subType = "leave"
		}
		n := message.GroupDecreaseNotice{
			SubType:    subType,
			GroupId:    groupID,
			OperatorId: message.QID(operatorID),
			UserId:     message.QID(idPrefix + strconv.FormatInt(u.ID, 10)),
		}
		n.Time = now
		n.PostType = "notice"
		n.NoticeType = "group_decrease"
		n.SelfId = a.selfID()
		n.SetPlatform(Platform)
		trig.OnGroupDecrease(n)
	}
}

// handleMyChatMember bot 自身成员状态变化 → 平台特定事件。
func (a *telegramAdapter) handleMyChatMember(mc *ChatMemberUpdated) {
	eventType := ""
	switch mc.NewChatMember.Status {
	case "member", "administrator", "restricted":
		eventType = "telegram.bot_added"
	case "kicked", "left":
		eventType = "telegram.bot_removed"
	default:
		return
	}
	a.emitPlatformEvent(eventType, mc)
}

// handleReaction 表情回应 → 群消息表情回应通知。
// Telegram 仅投递对 bot 自己消息（或 bot 为管理员时全部消息）的回应，属平台限制。
func (a *telegramAdapter) handleReaction(r *MessageReactionUpdated) {
	trig := a.triggerOf()
	if trig.OnGroupMsgEmojiLike == nil || r.Chat.Type == "private" || r.User == nil {
		return
	}
	n := message.GroupMsgEmojiLikeNotice{
		GroupId:   message.QID(idPrefix + chatIDRaw(r.Chat.ID)),
		UserId:    message.QID(idPrefix + strconv.FormatInt(r.User.ID, 10)),
		MessageId: msgID(r.Chat.ID, r.MessageID),
	}
	for _, re := range r.NewReaction {
		emojiID := ""
		switch re.Type {
		case "emoji":
			emojiID = re.Emoji
		case "custom_emoji":
			emojiID = re.CustomEmojiID
		}
		if emojiID == "" {
			continue
		}
		n.Likes = append(n.Likes, struct {
			EmojiId string `json:"emoji_id"`
			Count   int    `json:"count"`
		}{EmojiId: emojiID, Count: 1})
	}
	if len(n.Likes) == 0 {
		return
	}
	n.Time = uint(time.Now().Unix())
	n.PostType = "notice"
	n.NoticeType = "group_msg_emoji_like"
	n.SelfId = a.selfID()
	n.SetPlatform(Platform)
	trig.OnGroupMsgEmojiLike(n)
}

func (a *telegramAdapter) emitPlatformEvent(eventType string, data any) {
	trig := a.triggerOf()
	if trig.OnPlatformEvent == nil {
		return
	}
	trig.OnPlatformEvent(message.PlatformEvent{
		Platform: Platform,
		Type:     eventType,
		Data:     data,
	})
}

// ---------- ID 编解码 ----------

// chatIDRaw chat_id 的字符串形式（群为负数，保持原样，仅作缓存键/前缀拼接）。
func chatIDRaw(chatID int64) string {
	return strconv.FormatInt(chatID, 10)
}

// fromIDOf 发送者 ID 的字符串形式（日志用）。
func fromIDOf(u *User) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

// msgID 构造框架内消息 ID："tg:<chat_id>:<message_id>"。
func msgID(chatID int64, messageID int) message.QID {
	return message.QID(idPrefix + chatIDRaw(chatID) + ":" + strconv.Itoa(messageID))
}

// parseMsgID 解析 "tg:<chat_id>:<message_id>"；非 tg: 前缀或格式非法返回 ok=false。
func parseMsgID(s string) (chatID int64, messageID int, ok bool) {
	if !strings.HasPrefix(s, idPrefix) {
		return 0, 0, false
	}
	raw := strings.TrimPrefix(s, idPrefix)
	sep := strings.LastIndex(raw, ":")
	if sep <= 0 || sep == len(raw)-1 {
		return 0, 0, false
	}
	c, err1 := strconv.ParseInt(raw[:sep], 10, 64)
	m, err2 := strconv.Atoi(raw[sep+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return c, m, true
}
