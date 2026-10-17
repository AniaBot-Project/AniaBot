package feishu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	"github.com/larksuite/oapi-sdk-go/v3/channel/safety"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/spf13/viper"
)

type feishuConfig struct {
	appID     string
	appSecret string
	mode      string
	// webhook 模式
	webhookListen     string
	webhookPath       string
	verificationToken string
	encryptKey        string
}

type feishuAdapter struct {
	mu      sync.Mutex
	trigger adapter.TriggerWrapper

	client *lark.Client // API 客户端（发消息、查消息等）
	http   *resty.Client

	// 机器人自身 open_id（懒加载：从收到的 @机器人 mentions 中捕获）
	selfOpenID string
	// chatID -> chat_type(group/p2p) 缓存，由收到的消息事件填充
	chatTypes sync.Map
	// openID -> 单聊 chat_id 缓存（p2p 历史消息用）
	p2pChats sync.Map
	// msgChats messageID -> chatID 缓存：表情回应等事件不含 chat_id，需由消息反查。
	// 有界（见 newMsgChatCache）：每条入站消息都写入，无界会随累计收信量线性增长。
	msgChats *msgChatCache
	// nameCache open_id -> 用户昵称缓存（通讯录查询结果，飞书消息事件本身不含发送者昵称）
	nameCache sync.Map

	connState string
	lastErr   string

	// dedup 事件幂等去重：飞书事件订阅为 at-least-once 投递，
	// 断线重连/ACK 丢失会重推同一事件，以消息 ID 为键去重避免重复响应。
	// 使用官方 SDK 的 channel/safety.DedupCache（LRU + TTL）。
	dedup *safety.DedupCache

	logger *slog.Logger
}

func NewAdapter(cfg *viper.Viper) *feishuAdapter {
	a := &feishuAdapter{
		http: resty.New(),
		// 去重窗口 1 小时：飞书重推可能因断线重连/ACK 丢失延迟数分钟才到达，
		// 同一 message_id 永远不会被合法地处理两次，窗口长只占 LRU 容量（4096 有界）
		dedup:    safety.NewDedupCache(4096, time.Hour),
		msgChats: newMsgChatCache(),
		logger:   slog.Default().With("adapter", "feishu"),
	}
	return a
}

func (a *feishuAdapter) Name() string     { return "feishu" }
func (a *feishuAdapter) Platform() string { return Platform }

// msgChatCacheCap messageID -> chatID 反查缓存的容量上限。表情回应事件不含
// chat_id，只能按消息 ID 反查；但每条入站消息都会写入，无界会随累计收信量
// 线性增长（百万级消息 ≈ 百 MB 级常驻）。表情回应总是发生在"刚发出的消息"上，
// 保留最近若干条足够覆盖，超出即淘汰最旧。
const msgChatCacheCap = 4096

// msgChatCache 有界的 messageID -> chatID 映射（FIFO 淘汰）。
type msgChatCache struct {
	mu    sync.Mutex
	byMsg map[string]string
	order []string
}

func newMsgChatCache() *msgChatCache {
	return &msgChatCache{byMsg: make(map[string]string, msgChatCacheCap)}
}

// store 记录消息所属会话；同一消息重复写入不重复入队。
func (c *msgChatCache) store(msgID, chatID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.byMsg[msgID]; exists {
		c.byMsg[msgID] = chatID
		return
	}
	for len(c.order) >= msgChatCacheCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.byMsg, oldest)
	}
	c.byMsg[msgID] = chatID
	c.order = append(c.order, msgID)
}

// load 反查消息所属会话。
func (c *msgChatCache) load(msgID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	chatID, ok := c.byMsg[msgID]
	return chatID, ok
}

// len 当前缓存条目数（测试断言有界性用）。
func (c *msgChatCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byMsg)
}

// MessageKey 实现 adapter.EventKeyer：按 message_id 去重（官方推荐，勿用 event_id——
// 每次投递 event_id 可能不同）。core 层以此为键做幂等去重；与适配器自身的早期
// 去重（省图片下载）互不冲突。
func (a *feishuAdapter) MessageKey(msg message.Message) (string, bool) {
	raw := msg.MessageId.TrimPrefix(idPrefix)
	if raw == "" {
		return "", false
	}
	return "msg:" + raw, true
}

// NoticeKey 实现 adapter.EventKeyer：撤回通知按 message_id 去重（与适配器早期
// 去重的键一致）；表情回应无可靠 core 键，维持适配器级去重（core 层返回 false）。
func (a *feishuAdapter) NoticeKey(noticeType string, notice any) (string, bool) {
	switch v := notice.(type) {
	case message.GroupRecallNotice:
		if raw := v.MessageId.TrimPrefix(idPrefix); raw != "" {
			return "recall:" + raw, true
		}
	case message.FriendRecallNotice:
		if raw := v.MessageId.TrimPrefix(idPrefix); raw != "" {
			return "recall:" + raw, true
		}
	}
	return "", false
}

// SelfID 实现 adapter.SelfIDProvider：返回机器人自身 open_id（带 fs: 前缀）。
func (a *feishuAdapter) SelfID() message.QID {
	return a.selfID()
}

// feishuSegments 飞书出站支持的通用段类型；其余段（face/video/json/music/forward）
// 在 segmentsToContent 的 default 分支被忽略，core 会对发送这类段告警。
var feishuSegments = []string{
	message.SegmentText, message.SegmentMention, message.SegmentImage,
	message.SegmentReply, message.SegmentFile, message.SegmentRecord,
}

// SupportedSegments 实现 adapter.SegmentSupport。
func (a *feishuAdapter) SupportedSegments() []string { return feishuSegments }

// SendGroupStream 实现 adapter.StreamSenderExt：以 interactive 卡片创建流式群聊消息。
func (a *feishuAdapter) SendGroupStream(groupId message.QID, chain msgchain.GroupChain) (bot.StreamHandle, bool) {
	return a.sendStream(context.Background(), groupId.TrimPrefix(idPrefix), "chat_id", chain.GetGroupMsg())
}

// SendFriendStream 实现 adapter.StreamSenderExt：优先复用缓存的 p2p chat_id，
// 否则按 open_id 直接创建流式私聊消息。
func (a *feishuAdapter) SendFriendStream(userId message.QID, chain msgchain.FriendChain) (bot.StreamHandle, bool) {
	openID := userId.TrimPrefix(idPrefix)
	if openID == "" {
		return nil, false
	}
	if chatID, ok := a.p2pChats.Load(openID); ok {
		if h, ok2 := a.sendStream(context.Background(), chatID.(string), "chat_id", chain.GetFriendMsg()); ok2 {
			return h, true
		}
	}
	return a.sendStream(context.Background(), openID, "open_id", chain.GetFriendMsg())
}

// sendStream 以 interactive 卡片创建流式消息：段 → markdown（text 拼接、at 转
// <at user_id> 标记、@all 转 <at user_id="all">）；图片/文件等无法入卡片的段忽略
// （core 已按 SupportedSegments 告警）；首条 reply 段作为回复目标（走 im.message.reply）。
// 提及（@）单独收集为 prefix：后续 Patch 替换整个卡片内容时重新带上，保证 @ 不消失。
func (a *feishuAdapter) sendStream(ctx context.Context, receiveID, receiveType string, segs []message.OB11Segment) (bot.StreamHandle, bool) {
	if a.client == nil {
		return nil, false
	}
	var textSb, mentionSb strings.Builder
	var replyTo string
	for _, s := range segs {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				textSb.WriteString(t)
			}
		case message.SegmentMention:
			if qq, ok := s.Data["qq"].(string); ok {
				if qq == "all" {
					mentionSb.WriteString(`<at user_id="all"></at>`)
				} else if openID := message.QID(qq).TrimPrefix(idPrefix); openID != "" {
					mentionSb.WriteString(`<at user_id="` + openID + `"></at>`)
				}
			}
		case message.SegmentReply:
			if replyTo == "" {
				if id, ok := s.Data["id"].(string); ok {
					replyTo = message.QID(id).TrimPrefix(idPrefix)
				}
			}
		}
	}
	prefix, text := mentionSb.String(), textSb.String()
	content := buildCardJSON(prefix + text)
	var msgID string
	var ok bool
	if replyTo != "" {
		msgID, ok = a.sendReply(ctx, replyTo, "interactive", content)
	} else {
		msgID, ok = a.sendCreate(ctx, receiveID, receiveType, "interactive", content)
	}
	if !ok || msgID == "" {
		return nil, false
	}
	return &feishuStreamHandle{a: a, msgID: msgID, prefix: prefix, content: text}, true
}

func (a *feishuAdapter) SetTrigger(trigger adapter.TriggerWrapper) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trigger = trigger
}

func (a *feishuAdapter) triggerOf() adapter.TriggerWrapper {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trigger
}

func (a *feishuAdapter) setStatus(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connState = state
	a.lastErr = detail
}

// AdapterStatus 返回连接状态（connecting/connected/reconnecting）与详情。
func (a *feishuAdapter) AdapterStatus() (string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connState == "" {
		return "connecting", ""
	}
	return a.connState, a.lastErr
}

// Connected 长连接是否已就绪（供面板状态探针使用）。
func (a *feishuAdapter) Connected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState == "connected"
}

// selfID 返回框架内表示机器人自身的统一 ID（fs:open_id），未捕获到则为空。
func (a *feishuAdapter) selfID() message.QID {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.selfOpenID == "" {
		return ""
	}
	return message.QID(idPrefix + a.selfOpenID)
}

// resolveName 查询用户昵称（带缓存）。飞书消息事件本身不含发送者昵称，
// 需经通讯录 contact/v3/user/get 查询（需 contact:user.base:readonly 权限）；
// 权限缺失/用户不可见/查询失败时静默返回空串（调用方降级为空昵称兜底显示）。
// 仅在异步分发 goroutine 内调用（2s 超时不阻塞 ACK），sender_type==bot 已在上游过滤。
func (a *feishuAdapter) resolveName(openID string) string {
	if openID == "" {
		return ""
	}
	if v, ok := a.nameCache.Load(openID); ok {
		return v.(string)
	}
	if a.client == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := a.client.Contact.V3.User.Get(ctx, larkcontact.NewGetUserReqBuilder().
		UserId(openID).
		UserIdType("open_id").
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil ||
		resp.Data.User == nil || resp.Data.User.Name == nil {
		return ""
	}
	if name := deref(resp.Data.User.Name); name != "" {
		a.nameCache.Store(openID, name)
		return name
	}
	return ""
}

func (a *feishuAdapter) setSelfOpenID(openID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.selfOpenID == "" && openID != "" {
		a.selfOpenID = openID
	}
}

func (a *feishuAdapter) loadConfig(v *viper.Viper) feishuConfig {
	return feishuConfig{
		appID:             v.GetString("bot.feishu.app_id"),
		appSecret:         v.GetString("bot.feishu.app_secret"),
		mode:              v.GetString("bot.feishu.mode"),
		webhookListen:     v.GetString("bot.feishu.webhook.listen"),
		webhookPath:       v.GetString("bot.feishu.webhook.path"),
		verificationToken: v.GetString("bot.feishu.webhook.verification_token"),
		encryptKey:        v.GetString("bot.feishu.webhook.encrypt_key"),
	}
}

// Serve 启动飞书适配器（阻塞）：ws 长连接或 webhook 二选一。
func (a *feishuAdapter) Serve(v *viper.Viper) {
	cfg := a.loadConfig(v)
	if cfg.appID == "" || cfg.appSecret == "" {
		a.setStatus("reconnecting", "未配置 bot.feishu.app_id / app_secret，请在 Web 面板配置后重启")
		a.logger.Warn("飞书适配器未配置 App ID/Secret，无法启动")
		return
	}
	a.client = lark.NewClient(cfg.appID, cfg.appSecret)

	if cfg.mode == "webhook" {
		a.serveWebhook(cfg)
		return
	}
	a.serveWS(cfg)
}

func (a *feishuAdapter) serveWS(cfg feishuConfig) {
	handler := a.buildDispatcher("", "")
	a.setStatus("connecting", "启动飞书 WebSocket 长连接")
	wsCli := larkws.NewClient(cfg.appID, cfg.appSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	wsCli.SetOnReady(func() { a.setStatus("connected", "") })
	wsCli.SetOnReconnecting(func() { a.setStatus("reconnecting", "重连中") })
	wsCli.SetOnReconnected(func() { a.setStatus("connected", "") })
	wsCli.SetOnDisconnected(func() { a.setStatus("reconnecting", "连接断开") })
	wsCli.SetOnError(func(err error) { a.setStatus("reconnecting", err.Error()) })
	a.logger.Info("已启用飞书 websocket adapter")
	// Start 在成功连接后内部 select{} 阻塞，仅在致命错误时返回
	if err := wsCli.Start(context.Background()); err != nil {
		a.setStatus("reconnecting", err.Error())
		a.logger.Error("飞书长连接退出", "error", err)
	}
}

func (a *feishuAdapter) serveWebhook(cfg feishuConfig) {
	handler := a.buildDispatcher(cfg.verificationToken, cfg.encryptKey)
	mux := http.NewServeMux()
	// 使用独立 mux：NapCat HTTP 适配器占用了 http.DefaultServeMux 的 / 路由
	mux.HandleFunc(cfg.webhookPath, httpserverext.NewEventHandlerFunc(handler, larkevent.WithLogLevel(larkcore.LogLevelInfo)))
	addr := cfg.webhookListen
	if addr == "" {
		addr = "127.0.0.1:7777"
	}
	a.setStatus("connected", "webhook 监听 "+addr+cfg.webhookPath)
	a.logger.Info("已启用飞书 webhook adapter", "listen", addr+cfg.webhookPath)
	if err := http.ListenAndServe(addr, mux); err != nil {
		a.setStatus("reconnecting", err.Error())
		a.logger.Error("飞书 webhook 服务退出", "error", err)
	}
}

// buildDispatcher 组装事件分发器：注册消息/通知/平台特定事件处理器。
func (a *feishuAdapter) buildDispatcher(verificationToken, encryptKey string) *dispatcher.EventDispatcher {
	d := dispatcher.NewEventDispatcher(verificationToken, encryptKey).
		OnP2MessageReceiveV1(a.onReceive).
		OnP2MessageRecalledV1(a.onRecall).
		OnP2MessageReactionCreatedV1(a.onReaction).
		OnP2ChatMemberUserAddedV1(a.onUserAdded).
		OnP2ChatMemberUserDeletedV1(a.onUserDeleted).
		OnP2ChatMemberUserWithdrawnV1(a.onUserWithdrawn).
		OnP2ChatMemberBotAddedV1(a.onBotAdded).
		OnP2ChatMemberBotDeletedV1(a.onBotDeleted).
		OnP2CardActionTrigger(a.onCardAction)
	return d
}

func (a *feishuAdapter) chatTypeOf(chatID string) string {
	if v, ok := a.chatTypes.Load(chatID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (a *feishuAdapter) chatTypeOfOrGroup(chatID string) string {
	t := a.chatTypeOf(chatID)
	if t == "" {
		return "group"
	}
	return t
}

// ---------- 事件入站 ----------

// onReceive 处理 im.message.receive_v1：翻译为通用消息段并分发。
func (a *feishuAdapter) onReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	ev := event.Event
	if ev == nil || ev.Message == nil || ev.Sender == nil {
		return nil
	}
	// 过滤机器人自身与其他机器人的消息，防止循环
	if ev.Sender.SenderType != nil && *ev.Sender.SenderType == "bot" {
		return nil
	}
	em := ev.Message

	// 幂等去重：飞书 at-least-once 投递，断线重连/ACK 丢失会重推同一事件，
	// 按 message_id 去重（官方推荐，勿用 event_id——每次投递 event_id 可能不同）
	if em.MessageId != nil && a.dedup.IsDuplicate("msg:"+*em.MessageId) {
		a.logger.Debug("跳过重复投递的消息事件", "messageId", *em.MessageId)
		return nil
	}

	chatType := ""
	if em.ChatType != nil {
		chatType = *em.ChatType
	}
	if em.ChatId != nil {
		a.chatTypes.Store(*em.ChatId, chatType)
	}
	if em.MessageId != nil && em.ChatId != nil {
		a.msgChats.store(*em.MessageId, *em.ChatId)
	}

	openID := ""
	if ev.Sender.SenderId != nil && ev.Sender.SenderId.OpenId != nil {
		openID = *ev.Sender.SenderId.OpenId
		if em.ChatId != nil && chatType == "p2p" {
			a.p2pChats.Store(openID, *em.ChatId)
		}
	}

	// 捕获机器人自身 open_id（@机器人 mentions 中 mentioned_type == bot）
	if em.Mentions != nil {
		for _, m := range em.Mentions {
			if m.MentionedType != nil && *m.MentionedType == "bot" && m.Id != nil && m.Id.OpenId != nil {
				a.setSelfOpenID(*m.Id.OpenId)
			}
		}
	}

	// 异步处理：翻译（含图片/文件资源下载，单次最多 20s）与插件链分发都放到
	// 独立 goroutine——飞书长连接要求尽快回 ACK（SDK 在处理完事件后才写 ACK 帧），
	// 任何耗时操作阻塞在这里都会导致 ACK 延迟/丢失，飞书随即按 at-least-once 重推
	// 同一事件造成重复响应。去重与缓存写入（无 I/O）已在上方同步完成，顺序语义不变。
	trig := a.triggerOf()
	go func() {
		ob := a.eventMessageToOB11(em)
		if len(ob) == 0 {
			return
		}
		// 对齐 OneBot v11 消息类型语义：p2p 私聊 = private + sub_type=friend
		msgType, subType := chatType, ""
		if chatType == "p2p" {
			msgType, subType = "private", "friend"
		}
		msg := message.Message{
			Time:        msTimeToUint(em.CreateTime),
			PostType:    "message",
			MessageType: msgType,
			SubType:     subType,
			MessageId:   message.QID(idPrefix + deref(em.MessageId)),
			GroupId:     message.QID(idPrefix + deref(em.ChatId)),
			UserId:      message.QID(idPrefix + openID),
			Message:     ob,
			RawMessage:  segmentsPlainText(ob),
			Sender:      message.MessageSender{UserId: message.QID(idPrefix + openID), Nickname: a.resolveName(openID)},
			SelfId:      a.selfID(),
			Platform:    Platform,
		}
		switch chatType {
		case "group":
			if trig.OnGroupMsg != nil {
				trig.OnGroupMsg(msg)
			}
		case "p2p":
			if trig.OnFriendMsg != nil {
				trig.OnFriendMsg(msg)
			}
		}
	}()
	return nil
}

// onRecall 处理消息撤回：按聊天类型映射为群/私聊撤回通知。
func (a *feishuAdapter) onRecall(ctx context.Context, event *larkim.P2MessageRecalledV1) error {
	ev := event.Event
	if ev == nil || ev.ChatId == nil {
		return nil
	}
	if ev.MessageId != nil && a.dedup.IsDuplicate("recall:"+*ev.MessageId) {
		return nil
	}
	trig := a.triggerOf()
	chatID := *ev.ChatId
	if a.chatTypeOf(chatID) == "p2p" {
		if trig.OnFriendRecall != nil {
			n := message.FriendRecallNotice{UserId: "", MessageId: message.QID(idPrefix + deref(ev.MessageId))}
			n.Time = uint(time.Now().Unix())
			n.PostType = "notice"
			n.NoticeType = "friend_recall"
			n.SelfId = a.selfID()
			n.SetPlatform(Platform)
			trig.OnFriendRecall(n)
		}
		return nil
	}
	if trig.OnGroupRecall != nil {
		n := message.GroupRecallNotice{GroupId: message.QID(idPrefix + chatID), MessageId: message.QID(idPrefix + deref(ev.MessageId))}
		n.Time = uint(time.Now().Unix())
		n.PostType = "notice"
		n.NoticeType = "group_recall"
		n.SelfId = a.selfID()
		n.SetPlatform(Platform)
		trig.OnGroupRecall(n)
	}
	return nil
}

// onReaction 处理表情回应：映射为群消息表情回应通知（仅群聊）。
// 事件本身不含 chat_id，需通过消息 ID 反查缓存。
func (a *feishuAdapter) onReaction(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
	ev := event.Event
	if ev == nil || ev.MessageId == nil || ev.ReactionType == nil || ev.ReactionType.EmojiType == nil {
		return nil
	}
	// 同一消息可被不同人/不同表情多次回应，去重键需带上操作者与表情与时间
	reactKey := "reaction:" + *ev.MessageId + ":" + deref(ev.ReactionType.EmojiType) + ":" +
		userIDOf(ev.UserId) + ":" + deref(ev.ActionTime)
	if a.dedup.IsDuplicate(reactKey) {
		return nil
	}
	chatID, ok := a.msgChats.load(*ev.MessageId)
	if !ok || a.chatTypeOfOrGroup(chatID) != "group" {
		return nil
	}
	trig := a.triggerOf()
	if trig.OnGroupMsgEmojiLike == nil {
		return nil
	}
	openID := ""
	if ev.UserId != nil && ev.UserId.OpenId != nil {
		openID = *ev.UserId.OpenId
	}
	n := message.GroupMsgEmojiLikeNotice{
		GroupId:   message.QID(idPrefix + chatID),
		UserId:    message.QID(idPrefix + openID),
		MessageId: message.QID(idPrefix + deref(ev.MessageId)),
		Likes: []struct {
			EmojiId string `json:"emoji_id"`
			Count   int    `json:"count"`
		}{{EmojiId: *ev.ReactionType.EmojiType, Count: 1}},
	}
	n.Time = uint(time.Now().Unix())
	n.PostType = "notice"
	n.NoticeType = "group_msg_emoji_like"
	n.SelfId = a.selfID()
	n.SetPlatform(Platform)
	trig.OnGroupMsgEmojiLike(n)
	return nil
}

func (a *feishuAdapter) onUserAdded(ctx context.Context, event *larkim.P2ChatMemberUserAddedV1) error {
	ev := event.Event
	if ev == nil || ev.ChatId == nil {
		return nil
	}
	return a.emitMemberChange(ev.ChatId, ev.OperatorId, ev.Users, "group_increase", "invite")
}

func (a *feishuAdapter) onUserDeleted(ctx context.Context, event *larkim.P2ChatMemberUserDeletedV1) error {
	ev := event.Event
	if ev == nil || ev.ChatId == nil {
		return nil
	}
	return a.emitMemberChange(ev.ChatId, ev.OperatorId, ev.Users, "group_decrease", "kick")
}

func (a *feishuAdapter) onUserWithdrawn(ctx context.Context, event *larkim.P2ChatMemberUserWithdrawnV1) error {
	ev := event.Event
	if ev == nil || ev.ChatId == nil {
		return nil
	}
	return a.emitMemberChange(ev.ChatId, ev.OperatorId, ev.Users, "group_decrease", "leave")
}

// emitMemberChange 群成员变动广播：按 noticeType 构造通知并逐成员触发。
func (a *feishuAdapter) emitMemberChange(chatID *string, operatorID *larkim.UserId, users []*larkim.ChatMemberUser, noticeType, subType string) error {
	trig := a.triggerOf()
	if trig.OnGroupIncrease == nil && trig.OnGroupDecrease == nil {
		return nil
	}
	switch noticeType {
	case "group_increase":
		if trig.OnGroupIncrease == nil {
			return nil
		}
		n := message.GroupIncreaseNotice{
			GroupId:    message.QID(idPrefix + deref(chatID)),
			OperatorId: message.QID(idPrefix + userIDOf(operatorID)),
			SubType:    subType,
		}
		n.Time = uint(time.Now().Unix())
		n.PostType = "notice"
		n.NoticeType = noticeType
		n.SelfId = a.selfID()
		n.SetPlatform(Platform)
		for _, u := range users {
			nn := n
			nn.UserId = message.QID(idPrefix + userIDOf(u.UserId))
			trig.OnGroupIncrease(nn)
		}
	case "group_decrease":
		if trig.OnGroupDecrease == nil {
			return nil
		}
		n := message.GroupDecreaseNotice{
			GroupId:    message.QID(idPrefix + deref(chatID)),
			OperatorId: message.QID(idPrefix + userIDOf(operatorID)),
			SubType:    subType,
		}
		n.Time = uint(time.Now().Unix())
		n.PostType = "notice"
		n.NoticeType = noticeType
		n.SelfId = a.selfID()
		n.SetPlatform(Platform)
		for _, u := range users {
			nn := n
			nn.UserId = message.QID(idPrefix + userIDOf(u.UserId))
			trig.OnGroupDecrease(nn)
		}
	}
	return nil
}

func (a *feishuAdapter) onBotAdded(ctx context.Context, event *larkim.P2ChatMemberBotAddedV1) error {
	a.emitPlatformEvent("feishu.bot_added", event)
	return nil
}

func (a *feishuAdapter) onBotDeleted(ctx context.Context, event *larkim.P2ChatMemberBotDeletedV1) error {
	a.emitPlatformEvent("feishu.bot_deleted", event)
	return nil
}

func (a *feishuAdapter) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	a.emitPlatformEvent("feishu.card_action", event)
	return nil, nil
}

func (a *feishuAdapter) emitPlatformEvent(eventType string, data any) {
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

// ---------- 消息翻译（入站） ----------

type feishuMention struct {
	key    string
	openID string
	isBot  bool
}

// eventMessageToOB11 将接收事件的消息翻译为通用消息段。
// 图片/文件下载为 data URI（不依赖临时链接），供 AI 插件直接加载。
func (a *feishuAdapter) eventMessageToOB11(em *larkim.EventMessage) []message.OB11Segment {
	if em == nil || em.MessageType == nil || em.Content == nil {
		return nil
	}
	msgID := deref(em.MessageId)
	var mentions []feishuMention
	for _, m := range em.Mentions {
		if m == nil || m.Key == nil || m.Id == nil || m.Id.OpenId == nil {
			continue
		}
		mentions = append(mentions, feishuMention{key: *m.Key, openID: *m.Id.OpenId, isBot: m.MentionedType != nil && *m.MentionedType == "bot"})
	}
	content := *em.Content
	var segs []message.OB11Segment
	switch *em.MessageType {
	case "text":
		segs = a.parseTextContent(content, mentions)
	case "post":
		segs = a.parsePostContent(content, mentions)
	case "image":
		segs = a.parseImageContent(content, msgID)
	case "file":
		segs = a.parseFileContent(content, msgID)
	case "audio":
		segs = a.parseAudioContent(content, msgID)
	case "merge_forward":
		segs = []message.OB11Segment{{Type: message.SegmentText, Data: message.TextMessage{Text: "[合并转发消息]"}.Marshal()}}
	default:
		segs = []message.OB11Segment{{Type: message.SegmentText, Data: message.TextMessage{Text: "[" + *em.MessageType + "]"}.Marshal()}}
	}
	// 图片段补齐 URL（下载为 data URI）
	for i := range segs {
		if segs[i].Type == message.SegmentImage {
			if key, _ := segs[i].Data["file"].(string); key != "" && !strings.HasPrefix(key, "fs:") {
				uri := a.downloadResource(msgID, key, "image")
				if uri != "" {
					segs[i].Data["url"] = uri
				}
			}
		}
	}
	return segs
}

// apiMessageToOB11 将消息 API（get/list）返回的消息翻译为通用消息段。
func (a *feishuAdapter) apiMessageToOB11(m *larkim.Message) []message.OB11Segment {
	if m == nil || m.MsgType == nil || m.Body == nil || m.Body.Content == nil {
		return nil
	}
	msgID := deref(m.MessageId)
	var mentions []feishuMention
	for _, mt := range m.Mentions {
		if mt == nil || mt.Id == nil {
			continue
		}
		openID := *mt.Id
		if mt.IdType != nil && *mt.IdType == "open_id" {
			openID = *mt.Id
		}
		mentions = append(mentions, feishuMention{key: deref(mt.Key), openID: openID})
	}
	content := *m.Body.Content
	var segs []message.OB11Segment
	switch *m.MsgType {
	case "text":
		segs = a.parseTextContent(content, mentions)
	case "post":
		segs = a.parsePostContent(content, mentions)
	case "image":
		segs = a.parseImageContent(content, msgID)
	case "file":
		segs = a.parseFileContent(content, msgID)
	case "audio":
		segs = a.parseAudioContent(content, msgID)
	default:
		segs = []message.OB11Segment{{Type: message.SegmentText, Data: message.TextMessage{Text: "[" + *m.MsgType + "]"}.Marshal()}}
	}
	for i := range segs {
		if segs[i].Type == message.SegmentImage {
			if key, _ := segs[i].Data["file"].(string); key != "" {
				uri := a.downloadResource(msgID, key, "image")
				if uri != "" {
					segs[i].Data["url"] = uri
				}
			}
		}
	}
	return segs
}

var atMarkupRe = regexp.MustCompile(`<at user_id="([^"]+)">[^<]*</at>`)

func (a *feishuAdapter) parseTextContent(content string, mentions []feishuMention) []message.OB11Segment {
	var raw struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}
	text := raw.Text
	segs := make([]message.OB11Segment, 0)
	// @全体成员占位符
	if strings.Contains(text, "@_all") {
		parts := strings.Split(text, "@_all")
		segs = appendTextSeg(segs, parts[0])
		segs = append(segs, message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{IsAll: true}.Marshal()})
		text = strings.Join(parts[1:], "")
	}
	// 按 mentions 的占位符拆分文本
	for _, m := range mentions {
		if m.key == "" || m.openID == "" {
			continue
		}
		idx := strings.Index(text, m.key)
		if idx < 0 {
			continue
		}
		segs = appendTextSeg(segs, text[:idx])
		segs = append(segs, message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{QQ: message.QID(idPrefix + m.openID)}.Marshal()})
		text = text[idx+len(m.key):]
	}
	// 兜底：消息 API 的文本可能使用 <at> 标记而非占位符
	// 按匹配位置顺序拆分，保证文本与 at 段顺序正确
	if strings.Contains(text, "<at ") {
		idxs := atMarkupRe.FindAllStringSubmatchIndex(text, -1)
		pos := 0
		for _, loc := range idxs {
			segs = appendTextSeg(segs, text[pos:loc[0]])
			segs = append(segs, message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{QQ: message.QID(idPrefix + text[loc[2]:loc[3]])}.Marshal()})
			pos = loc[1]
		}
		text = text[pos:]
	}
	segs = appendTextSeg(segs, text)
	return segs
}

type feishuPostElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	ImageKey string `json:"image_key"`
	FileKey  string `json:"file_key"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
}

// parsePostContent 解析富文本 post 消息，兼容两种 content 结构：
//   - 发送/消息 API 侧：{"zh_cn": {"title": ..., "content": [...]}}
//   - 接收事件侧：title/content/content_v2 在顶层（无 zh_cn 包装，
//     content_v2 为含 md 标签的 markdown 版本，content 为空时回退）
func (a *feishuAdapter) parsePostContent(content string, mentions []feishuMention) []message.OB11Segment {
	var raw struct {
		Title     string                `json:"title"`
		Content   [][]feishuPostElement `json:"content"`
		ContentV2 [][]feishuPostElement `json:"content_v2"`
		ZhCN      *struct {
			Title   string                `json:"title"`
			Content [][]feishuPostElement `json:"content"`
		} `json:"zh_cn"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}
	title := raw.Title
	lines := raw.Content
	switch {
	case raw.ZhCN != nil:
		// 发送/消息 API 侧结构：zh_cn 语言键包装
		title, lines = raw.ZhCN.Title, raw.ZhCN.Content
	case len(lines) == 0 && len(raw.ContentV2) > 0:
		// 接收事件侧 markdown 场景：content 为空时回退 content_v2
		lines = raw.ContentV2
	}
	segs := make([]message.OB11Segment, 0, len(lines)+1)
	if title != "" {
		segs = appendTextSeg(segs, title+"\n")
	}
	// 接收事件侧 at 元素的 user_id 是占位符（@_user_N），需经 mentions 反查 open_id；
	// 发送/消息 API 侧则是真实 open_id，可直接使用
	openIDByPlaceholder := map[string]string{}
	for _, m := range mentions {
		if m.key != "" {
			openIDByPlaceholder[m.key] = m.openID
		}
	}
	for _, line := range lines {
		for _, el := range line {
			switch el.Tag {
			case "text":
				segs = appendTextSeg(segs, el.Text)
			case "a":
				if el.Text != "" {
					segs = appendTextSeg(segs, fmt.Sprintf("%s(%s)", el.Text, el.Href))
				}
			case "at":
				if openID, ok := openIDByPlaceholder[el.UserID]; ok && openID != "" {
					segs = append(segs, message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{QQ: message.QID(idPrefix + openID)}.Marshal()})
				} else if el.UserID != "" {
					segs = append(segs, message.OB11Segment{Type: message.SegmentMention, Data: message.MentionMessage{QQ: message.QID(idPrefix + el.UserID)}.Marshal()})
				}
			case "img":
				if el.ImageKey != "" {
					segs = append(segs, message.OB11Segment{Type: message.SegmentImage, Data: message.ImageMessage{File: el.ImageKey}.Marshal()})
				}
			case "media":
				if el.ImageKey != "" {
					segs = append(segs, message.OB11Segment{Type: message.SegmentImage, Data: message.ImageMessage{File: el.ImageKey}.Marshal()})
				} else if el.FileKey != "" {
					segs = append(segs, message.OB11Segment{Type: message.SegmentFile, Data: message.FileMessage{File: el.FileKey, FileId: el.FileKey}.Marshal()})
				}
			case "emotion":
				segs = appendTextSeg(segs, "["+el.Text+"]")
			case "code_block":
				segs = appendTextSeg(segs, el.Text)
			case "hr":
				segs = appendTextSeg(segs, "\n")
			default:
				// 含 md 等标签：text 键即为内容，直接按文本处理
				segs = appendTextSeg(segs, el.Text)
			}
		}
		segs = appendTextSeg(segs, "\n")
	}
	return segs
}

func (a *feishuAdapter) parseImageContent(content, msgID string) []message.OB11Segment {
	var raw struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil || raw.ImageKey == "" {
		return nil
	}
	return []message.OB11Segment{{Type: message.SegmentImage, Data: message.ImageMessage{File: raw.ImageKey}.Marshal()}}
}

func (a *feishuAdapter) parseFileContent(content, msgID string) []message.OB11Segment {
	var raw struct {
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil || raw.FileKey == "" {
		return nil
	}
	return []message.OB11Segment{{Type: message.SegmentFile, Data: message.FileMessage{File: raw.FileKey, FileId: raw.FileKey, Name: raw.FileName}.Marshal()}}
}

func (a *feishuAdapter) parseAudioContent(content, msgID string) []message.OB11Segment {
	var raw struct {
		FileKey string `json:"file_key"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil || raw.FileKey == "" {
		return nil
	}
	return []message.OB11Segment{{Type: message.SegmentRecord, Data: message.RecordMessage{URL: raw.FileKey}.Marshal()}}
}

// downloadResource 通过 im.messageResource.get 下载消息资源并转为 data URI。
func (a *feishuAdapter) downloadResource(messageID, fileKey, resourceType string) string {
	if a.client == nil || messageID == "" || fileKey == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := a.client.Im.V1.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type(resourceType).
		Build())
	if err != nil || resp == nil || !resp.Success() {
		return ""
	}
	if resp.File == nil {
		return ""
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return ""
	}
	mime := "application/octet-stream"
	if resourceType == "image" {
		// 按实际字节识别 MIME（标签与内容不一致时模型端解码会报 unsupported image），
		// 识别失败再回退 image/png
		mime = "image/png"
		if m, ok := message.SniffImageMIME(data); ok {
			mime = m
		}
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func appendTextSeg(segs []message.OB11Segment, text string) []message.OB11Segment {
	if text == "" {
		return segs
	}
	if len(segs) > 0 && segs[len(segs)-1].Type == message.SegmentText {
		if prev, ok := segs[len(segs)-1].Data["text"].(string); ok {
			segs[len(segs)-1].Data["text"] = prev + text
			return segs
		}
	}
	return append(segs, message.OB11Segment{Type: message.SegmentText, Data: message.TextMessage{Text: text}.Marshal()})
}

// segmentsPlainText 消息段的纯文本（供 RawMessage 复读判等）。
func segmentsPlainText(segs []message.OB11Segment) string {
	var sb strings.Builder
	for _, s := range segs {
		if s.Type == message.SegmentText {
			if t, ok := s.Data["text"].(string); ok {
				sb.WriteString(t)
			}
		}
	}
	return sb.String()
}

func msTimeToUint(ms *string) uint {
	if ms == nil {
		return 0
	}
	val, err := strconv.ParseInt(*ms, 10, 64)
	if err != nil {
		return 0
	}
	return uint(val / 1000)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func userIDOf(u *larkim.UserId) string {
	if u == nil {
		return ""
	}
	if u.OpenId != nil {
		return *u.OpenId
	}
	if u.UnionId != nil {
		return *u.UnionId
	}
	if u.UserId != nil {
		return *u.UserId
	}
	return ""
}

// ---------- 发送 ----------

func (a *feishuAdapter) SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (message.QID, bool) {
	return a.sendChain(context.Background(), groupId.TrimPrefix(idPrefix), "chat_id", chain.GetGroupMsg())
}

func (a *feishuAdapter) SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (message.QID, bool) {
	openID := userId.TrimPrefix(idPrefix)
	if openID == "" {
		a.logger.Warn("飞书发送私聊消息失败：用户 ID 为空", "userId", userId)
		return "", false
	}
	// 优先用缓存的单聊 chat_id 发送（p2p 消息推荐走 chat_id，比 open_id 更可靠）；
	// open_id 失败时再回退到 chat_id（若有缓存）
	if chatID, ok := a.p2pChats.Load(openID); ok {
		if id, ok2 := a.sendChain(context.Background(), chatID.(string), "chat_id", chain.GetFriendMsg()); ok2 {
			return id, true
		}
	}
	if id, ok := a.sendChain(context.Background(), openID, "open_id", chain.GetFriendMsg()); ok {
		return id, true
	}
	// open_id 发送失败：用缓存的单聊 chat_id 再试一次（open_id 权限不足等场景）
	if chatID, ok := a.p2pChats.Load(openID); ok {
		return a.sendChain(context.Background(), chatID.(string), "chat_id", chain.GetFriendMsg())
	}
	return "", false
}

// sendChain 把通用消息段翻译为飞书消息并发送。
// 返回最后一条成功消息的框架内 ID（fs:om_xxx）。
func (a *feishuAdapter) sendChain(ctx context.Context, receiveID, receiveType string, segs []message.OB11Segment) (message.QID, bool) {
	if a.client == nil {
		return "", false
	}
	if receiveID == "" {
		a.logger.Warn("飞书发送消息失败：receive_id 为空", "receiveType", receiveType)
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 提取回复目标（首条 reply 段），其余段作为正文
	replyTo := ""
	body := make([]message.OB11Segment, 0, len(segs))
	for _, s := range segs {
		if s.Type == message.SegmentReply {
			if id, ok := s.Data["id"].(string); ok && replyTo == "" {
				replyTo = message.QID(id).TrimPrefix(idPrefix)
			}
			continue
		}
		body = append(body, s)
	}
	if len(body) == 0 {
		return "", false
	}

	// file 段指向图片文件时（file 工具等来源）转 image 段：随正文以 img 元素
	// 内联展示，而不是走文件上传（附件）
	for i, s := range body {
		if s.Type == message.SegmentFile {
			if imgData, ok := message.FileSegmentAsImage(s); ok {
				body[i] = message.OB11Segment{Type: message.SegmentImage, Data: imgData}
			}
		}
	}

	// 正文转飞书内容：文本/at 或富文本 post（含图片）
	msgType, content, imageKeys := a.segmentsToContent(ctx, body)
	if content == "" {
		return "", false
	}

	var msgID string
	var ok bool
	if replyTo != "" {
		msgID, ok = a.sendReply(ctx, replyTo, msgType, content)
	} else {
		msgID, ok = a.sendCreate(ctx, receiveID, receiveType, msgType, content)
	}
	if !ok {
		return "", false
	}

	// 图片已合入 post 的，无需单独发送；文件/语音等单独发送
	for _, s := range body {
		switch s.Type {
		case message.SegmentFile:
			a.sendFileSegment(ctx, receiveID, receiveType, s)
		case message.SegmentRecord:
			a.sendFileSegment(ctx, receiveID, receiveType, s)
		}
	}
	_ = imageKeys
	return message.QID(idPrefix + msgID), true
}

// segmentsToContent 将正文段翻译为（msgType, content, 已上传图片 key 列表）。
// 文本统一走 post + md 元素：Feishu 客户端原生渲染 markdown（标题/加粗/代码块/列表等），
// @提及拆为独立 at 元素前置（保证通知送达）；图片元素追加到正文末尾。
func (a *feishuAdapter) segmentsToContent(ctx context.Context, segs []message.OB11Segment) (string, string, []string) {
	var md strings.Builder
	var mentions []normalizeMention
	var imageKeys []string
	hasImage := false
	atAll := false

	for _, s := range segs {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				md.WriteString(t)
			}
		case message.SegmentMention:
			if qq, ok := s.Data["qq"].(string); ok {
				if qq == "all" {
					atAll = true
				} else {
					openID := message.QID(qq).TrimPrefix(idPrefix)
					mentions = append(mentions, normalizeMention{UserID: openID, OpenID: openID, Key: openID})
				}
			}
		case message.SegmentImage:
			hasImage = true
			if key, ok := a.uploadImage(ctx, s); ok {
				imageKeys = append(imageKeys, key)
			}
		case message.SegmentFile, message.SegmentRecord:
			// 文件单独发送，不进正文
		default:
			// 不支持的段类型（face/video/json/music/forward 等）：
			// 有 text 键时退化为文本，否则忽略（core 层已按 SupportedSegments 告警）
			if t, ok := s.Data["text"].(string); ok {
				md.WriteString(t)
			} else {
				a.logger.Debug("忽略飞书不支持的通用消息段", "segment", s.Type)
			}
		}
	}

	text := strings.TrimSpace(md.String())
	if text == "" && len(mentions) == 0 && !hasImage {
		return "", "", nil
	}

	// 纯图片（无文本/提及）：直接构造含 img 元素的 post
	if text == "" && len(mentions) == 0 {
		rows := make([][]map[string]any, 0, 1)
		if len(imageKeys) > 0 {
			row := make([]map[string]any, 0, len(imageKeys))
			for _, key := range imageKeys {
				row = append(row, map[string]any{"tag": "img", "image_key": key})
			}
			rows = append(rows, row)
		}
		b, err := json.Marshal(map[string]any{"zh_cn": map[string]any{"title": "", "content": rows}})
		if err != nil {
			return "", "", imageKeys
		}
		return larkim.MsgTypePost, string(b), imageKeys
	}

	// post 无 @all 元素，注入文本标记尽力而为（post 的 md 元素对原始 <at> 支持有限）
	if atAll {
		text = `<at user_id="all"></at> ` + text
	}

	content, err := normalize.SimpleMarkdownToPost("", text, mentionList(mentions))
	if err != nil {
		return "", "", imageKeys
	}
	// 有图片时把 img 元素追加到正文末尾（post 内容行）
	if len(imageKeys) > 0 {
		content = appendImagesToPost(content, imageKeys)
	}
	return larkim.MsgTypePost, content, imageKeys
}

// normalizeMention 与 channel/types.Mention 字段对齐（避免引入额外依赖暴露面的别名）。
type normalizeMention struct {
	Key    string
	UserID string
	OpenID string
	Name   string
	IsBot  bool
}

func mentionList(ms []normalizeMention) []types.Mention {
	out := make([]types.Mention, 0, len(ms))
	for _, m := range ms {
		out = append(out, types.Mention{Key: m.Key, UserID: m.UserID, OpenID: m.OpenID, Name: m.Name, IsBot: m.IsBot})
	}
	return out
}

// appendImagesToPost 把图片 key 追加进 post content 的最后一个段落（正文之后）。
func appendImagesToPost(content string, imageKeys []string) string {
	var post struct {
		ZhCN *struct {
			Title   string             `json:"title"`
			Content [][]map[string]any `json:"content"`
		} `json:"zh_cn"`
	}
	if err := json.Unmarshal([]byte(content), &post); err != nil || post.ZhCN == nil {
		return content
	}
	if len(post.ZhCN.Content) == 0 {
		post.ZhCN.Content = append(post.ZhCN.Content, []map[string]any{})
	}
	last := len(post.ZhCN.Content) - 1
	for _, key := range imageKeys {
		post.ZhCN.Content[last] = append(post.ZhCN.Content[last], map[string]any{"tag": "img", "image_key": key})
	}
	b, err := json.Marshal(post)
	if err != nil {
		return content
	}
	return string(b)
}

func (a *feishuAdapter) sendCreate(ctx context.Context, receiveID, receiveType, msgType, content string) (string, bool) {
	resp, err := a.client.Im.V1.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil || resp.Data.MessageId == nil {
		a.logSendFail("发送消息", err, resp, "receiveType", receiveType, "receiveId", receiveID)
		return "", false
	}
	return *resp.Data.MessageId, true
}

func (a *feishuAdapter) sendReply(ctx context.Context, replyTo, msgType, content string) (string, bool) {
	resp, err := a.client.Im.V1.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(replyTo).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content).
			Build()).
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil || resp.Data.MessageId == nil {
		a.logSendFail("回复消息", err, resp, "replyTo", replyTo)
		return "", false
	}
	return *resp.Data.MessageId, true
}

// logSendFail 记录飞书 API 调用失败详情。resp 非 nil 时提取业务错误码/信息/原始响应体，
// 便于定位权限不足、receive_id 无效、content 非法等具体原因。
func (a *feishuAdapter) logSendFail(op string, err error, resp any, kv ...any) {
	args := append([]any{"op", op}, kv...)
	if err != nil {
		args = append(args, "error", err)
		a.logger.Warn("飞书"+op+"失败", args...)
		return
	}
	if resp == nil {
		a.logger.Warn("飞书"+op+"失败（无响应）", args...)
		return
	}
	rv := reflect.ValueOf(resp)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			a.logger.Warn("飞书"+op+"失败（nil 响应）", args...)
			return
		}
		rv = rv.Elem()
	}
	code, msg, body := "", "", ""
	ce := rv.FieldByName("CodeError")
	if ce.IsValid() {
		if f := ce.FieldByName("Code"); f.IsValid() && f.Kind() == reflect.Int {
			code = strconv.Itoa(int(f.Int()))
		}
		if f := ce.FieldByName("Msg"); f.IsValid() && f.Kind() == reflect.String {
			msg = f.String()
		}
	}
	api := rv.FieldByName("ApiResp")
	if api.IsValid() && api.Kind() == reflect.Pointer && !api.IsNil() {
		if f := api.Elem().FieldByName("RawBody"); f.IsValid() && f.Kind() == reflect.Slice {
			body = string(f.Bytes())
		}
	}
	args = append(args, "code", code, "msg", msg, "body", body)
	a.logger.Warn("飞书"+op+"失败", args...)
}

// uploadImage 将通用 image 段上传为飞书 image_key。
func (a *feishuAdapter) uploadImage(ctx context.Context, seg message.OB11Segment) (string, bool) {
	if a.client == nil {
		return "", false
	}
	data, ok := a.resolveSegmentBytes(ctx, seg)
	if !ok {
		return "", false
	}
	resp, err := a.client.Im.V1.Image.Create(ctx, larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType(larkim.CreateImageImageTypeMessage).
			Image(bytes.NewReader(data)).
			Build()).
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil || resp.Data.ImageKey == nil {
		a.logger.Warn("飞书图片上传失败", "error", err)
		return "", false
	}
	return *resp.Data.ImageKey, true
}

func (a *feishuAdapter) sendFileSegment(ctx context.Context, receiveID, receiveType string, seg message.OB11Segment) {
	if a.client == nil {
		return
	}
	data, ok := a.resolveSegmentBytes(ctx, seg)
	if !ok {
		return
	}
	name, _ := seg.Data["name"].(string)
	if name == "" {
		name = "file"
	}
	fileType := a.fileTypeFor(name)
	resp, err := a.client.Im.V1.File.Create(ctx, larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType(fileType).
			FileName(name).
			File(bytes.NewReader(data)).
			Build()).
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil || resp.Data.FileKey == nil {
		a.logger.Warn("飞书文件上传失败", "error", err)
		return
	}
	content := `{"file_key":"` + *resp.Data.FileKey + `"}`
	a.sendCreate(ctx, receiveID, receiveType, larkim.MsgTypeFile, content)
}

func (a *feishuAdapter) fileTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".opus":
		return larkim.CreateFileFileTypeOpus
	case ".mp4":
		return larkim.CreateFileFileTypeMp4
	case ".pdf":
		return larkim.CreateFileFileTypePdf
	case ".doc", ".docx":
		return larkim.CreateFileFileTypeDoc
	case ".xls", ".xlsx":
		return larkim.CreateFileFileTypeXls
	case ".ppt", ".pptx":
		return larkim.CreateFileFileTypePpt
	default:
		return larkim.CreateFileFileTypeStream
	}
}

// resolveSegmentBytes 从 image/file 段的 data.file 解析出字节内容：
// 支持 base64://、file://、data: URI 与 http(s) URL。
func (a *feishuAdapter) resolveSegmentBytes(ctx context.Context, seg message.OB11Segment) ([]byte, bool) {
	fileStr, _ := seg.Data["file"].(string)
	if fileStr == "" {
		return nil, false
	}
	switch {
	case strings.HasPrefix(fileStr, "base64://"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(fileStr, "base64://"))
		return b, err == nil && len(b) > 0
	case strings.HasPrefix(fileStr, "file://"):
		b, err := os.ReadFile(strings.TrimPrefix(fileStr, "file://"))
		return b, err == nil && len(b) > 0
	case strings.HasPrefix(fileStr, "data:"):
		if _, after, ok := strings.Cut(fileStr, ","); ok {
			b, err := base64.StdEncoding.DecodeString(after)
			return b, err == nil && len(b) > 0
		}
	case strings.HasPrefix(fileStr, "http://") || strings.HasPrefix(fileStr, "https://"):
		if a.http != nil {
			resp, err := a.http.R().SetContext(ctx).Get(fileStr)
			if err == nil && resp.StatusCode() == http.StatusOK {
				return resp.Body(), len(resp.Body()) > 0
			}
		}
	}
	return nil, false
}

// ---------- 查询 ----------

func (a *feishuAdapter) GetMsgDetail(msgId message.QID) (*message.Message, bool) {
	if a.client == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := a.client.Im.V1.Message.Get(ctx, larkim.NewGetMessageReqBuilder().
		MessageId(msgId.TrimPrefix(idPrefix)).
		UserIdType("open_id").
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil || len(resp.Data.Items) == 0 {
		return nil, false
	}
	return a.apiMessageToMessage(resp.Data.Items[0]), true
}

func (a *feishuAdapter) apiMessageToMessage(m *larkim.Message) *message.Message {
	if m == nil {
		return nil
	}
	segs := a.apiMessageToOB11(m)
	msg := &message.Message{
		MessageId:  message.QID(idPrefix + deref(m.MessageId)),
		GroupId:    message.QID(idPrefix + deref(m.ChatId)),
		Message:    segs,
		RawMessage: segmentsPlainText(segs),
		SelfId:     a.selfID(),
		Platform:   Platform,
	}
	if m.Sender != nil {
		msg.Sender = message.MessageSender{
			UserId:   message.QID(idPrefix + deref(m.Sender.Id)),
			Nickname: deref(m.Sender.SenderName),
		}
		msg.UserId = msg.Sender.UserId
	}
	return msg
}

func (a *feishuAdapter) GetGroupDetail(groupId message.QID) (*message.GroupInfo, bool) {
	if a.client == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := a.client.Im.V1.Chat.Get(ctx, larkim.NewGetChatReqBuilder().
		ChatId(groupId.TrimPrefix(idPrefix)).
		UserIdType("open_id").
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil {
		return nil, false
	}
	d := resp.Data
	info := &message.GroupInfo{
		GroupID: groupId,
	}
	if d.Name != nil {
		info.GroupName = *d.Name
	}
	if d.UserCount != nil {
		info.MemberCount, _ = strconv.Atoi(*d.UserCount)
	}
	if d.UserCount != nil && d.BotCount != nil {
		uc, _ := strconv.Atoi(*d.UserCount)
		bc, _ := strconv.Atoi(*d.BotCount)
		info.MaxMemberCount = uc + bc
	}
	return info, true
}

func (a *feishuAdapter) GetGroupMsgHistory(groupId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	if a.client == nil {
		return nil, false
	}
	if count <= 0 {
		count = 20
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// message_seq 在飞书无对应语义（飞书无翻页游标号），忽略
	resp, err := a.client.Im.V1.Message.List(ctx, larkim.NewListMessageReqBuilder().
		ContainerIdType("chat").
		ContainerId(groupId.TrimPrefix(idPrefix)).
		PageSize(count).
		SortType("ByCreateTimeDesc").
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil {
		return nil, false
	}
	out := make([]message.Message, 0, len(resp.Data.Items))
	for _, m := range resp.Data.Items {
		mm := a.apiMessageToMessage(m)
		if mm == nil {
			continue
		}
		mm.GroupId = groupId
		out = append(out, *mm)
	}
	return &out, true
}

func (a *feishuAdapter) GetFriendMsgHistory(userId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	if a.client == nil {
		return nil, false
	}
	openID := userId.TrimPrefix(idPrefix)
	chatID, ok := a.p2pChats.Load(openID)
	if !ok {
		return nil, false
	}
	if count <= 0 {
		count = 20
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := a.client.Im.V1.Message.List(ctx, larkim.NewListMessageReqBuilder().
		ContainerIdType("chat").
		ContainerId(chatID.(string)).
		PageSize(count).
		SortType("ByCreateTimeDesc").
		Build())
	if err != nil || resp == nil || !resp.Success() || resp.Data == nil {
		return nil, false
	}
	out := make([]message.Message, 0, len(resp.Data.Items))
	for _, m := range resp.Data.Items {
		mm := a.apiMessageToMessage(m)
		if mm == nil {
			continue
		}
		mm.UserId = userId
		out = append(out, *mm)
	}
	return &out, true
}
