package discord

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
)

type discordConfig struct {
	token        string
	proxy        string
	memberEvents bool
}

// discordAdapter Discord 平台适配器：discordgo Gateway WebSocket 收事件。
// 心跳/断线重连/会话 resume 由 discordgo 内部维护；core 层按 EventKeyer
// 对 resume 重放的消息与撤回通知去重。
type discordAdapter struct {
	mu      sync.Mutex
	trigger adapter.TriggerWrapper
	cfg     discordConfig

	session *discordgo.Session
	api     discordAPI

	// selfID/selfUsername 机器人自身信息（READY 填充）
	selfID       string
	selfUsername string

	// msgCache 入站/出站消息内存缓存（GetMsgDetail 快路径、撤回通知作者反查、历史兜底）
	msgCache *msgCache

	// auditSem 审计日志查询的并发闸门（见 resolveDeleter）：批量删除时避免
	// 同时发起大量 REST 查询撞速率限制。缓冲即并发上限。
	auditSem chan struct{}

	connState string
	lastErr   string

	logger *slog.Logger
}

func NewAdapter(cfg *viper.Viper) *discordAdapter {
	return &discordAdapter{
		msgCache: newMsgCache(msgCachePerChat, msgCacheMaxChats),
		auditSem: make(chan struct{}, discordAuditMaxConcurrent),
		logger:   slog.Default().With("adapter", "discord"),
	}
}

// discordAuditMaxConcurrent 审计日志查询的最大并发：批量删除时限制 REST 调用量。
const discordAuditMaxConcurrent = 4

func (a *discordAdapter) Name() string     { return "discord" }
func (a *discordAdapter) Platform() string { return Platform }

// MessageKey 实现 adapter.EventKeyer：按消息 ID 去重。
// 消息 ID 已编码为 "dc:<channel>:<msgid>"（snowflake 全局唯一，频道前缀仅为
// API 寻址），网关 resume 重放的内容一致，键稳定。
func (a *discordAdapter) MessageKey(msg message.Message) (string, bool) {
	raw := msg.MessageId.TrimPrefix(idPrefix)
	if raw == "" {
		return "", false
	}
	return "msg:" + raw, true
}

// NoticeKey 实现 adapter.EventKeyer：撤回通知按消息 ID 去重（resume 重放稳定）；
// 其余通知无稳定 ID，不去重。
func (a *discordAdapter) NoticeKey(noticeType string, notice any) (string, bool) {
	var msgID message.QID
	switch n := notice.(type) {
	case message.GroupRecallNotice:
		msgID = n.MessageId
	case message.FriendRecallNotice:
		msgID = n.MessageId
	default:
		return "", false
	}
	raw := msgID.TrimPrefix(idPrefix)
	if raw == "" {
		return "", false
	}
	return "recall:" + raw, true
}

// SelfID 实现 adapter.SelfIDProvider：返回机器人自身 ID（dc:<user_id>）。
// core 用它兜底填充 msg.SelfId，使自消息过滤与 @ 提及检测生效——
// 提及判定为 at 段 Data["qq"] 与 SelfId 的精确字符串比较，必须同为 dc: 前缀。
func (a *discordAdapter) SelfID() message.QID {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.selfID == "" {
		return ""
	}
	return message.QID(idPrefix + a.selfID)
}

// discordSegments Discord 出站支持的通用段类型；face/json/music/forward
// 在 sendChain 的 default 分支退化（有 text 键并入文本），core 会对发送这类段告警。
var discordSegments = []string{
	message.SegmentText, message.SegmentMention, message.SegmentImage,
	message.SegmentReply, message.SegmentFile, message.SegmentRecord,
	message.SegmentVideo,
}

// SupportedSegments 实现 adapter.SegmentSupport。
func (a *discordAdapter) SupportedSegments() []string { return discordSegments }

func (a *discordAdapter) SetTrigger(trigger adapter.TriggerWrapper) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trigger = trigger
}

func (a *discordAdapter) triggerOf() adapter.TriggerWrapper {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trigger
}

func (a *discordAdapter) setStatus(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connState = state
	a.lastErr = detail
}

// AdapterStatus 返回连接状态（connecting/connected/reconnecting）与详情。
func (a *discordAdapter) AdapterStatus() (string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connState == "" {
		return "connecting", ""
	}
	return a.connState, a.lastErr
}

// Connected 网关是否已就绪（供面板状态探针使用）。
func (a *discordAdapter) Connected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState == "connected"
}

// selfInfo 机器人自身信息日志串（未 READY 时为空）。
func (a *discordAdapter) selfInfo() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.selfID == "" {
		return ""
	}
	return a.selfUsername + "(" + a.selfID + ")"
}

func (a *discordAdapter) loadConfig(v *viper.Viper) discordConfig {
	return discordConfig{
		token:        strings.TrimSpace(v.GetString("bot.discord.token")),
		proxy:        v.GetString("bot.discord.proxy"),
		memberEvents: v.GetBool("bot.discord.member_events"),
	}
}

// Serve 启动 Discord 适配器（阻塞）：Open 网关连接，失败指数退避无限重试。
// Serve 返回即适配器死亡（core 不再拉起），连接失败不可早退；
// Open 成功后心跳/重连/resume 由 discordgo 内部维护，Serve 永驻。
func (a *discordAdapter) Serve(v *viper.Viper) {
	a.cfg = a.loadConfig(v)
	if a.cfg.token == "" {
		a.setStatus("reconnecting", "未配置 bot.discord.token，请在 Web 面板配置后重启")
		a.logger.Warn("Discord 适配器未配置 Bot Token，无法启动")
		return
	}
	a.setStatus("connecting", "正在连接 Discord 网关")
	for delay := time.Second; ; {
		s, err := a.newSession()
		if err == nil {
			a.session, a.api = s, s
			err = s.Open()
		}
		if err != nil {
			// Token 无效/特权意图未开启（close 4014）等错误在此循环呈现，
			// 修正配置需重启生效（与其他适配器一致）
			a.setStatus("reconnecting", err.Error())
			a.logger.Warn("Discord 网关连接失败，重试中", "error", err)
			time.Sleep(delay)
			if delay < 30*time.Second {
				delay *= 2
			}
			continue
		}
		a.logger.Info("Discord 网关已连接", "bot", a.selfInfo())
		select {} // Open 返回后网关由 discordgo 内部维护；Serve 永不返回
	}
}

// ---------- ID 编解码 ----------

// msgID 构造框架内消息 ID："dc:<channel_id>:<message_id>"。
func msgID(channelID, messageID string) message.QID {
	return message.QID(idPrefix + channelID + ":" + messageID)
}

// parseMsgID 解析 "dc:<channel_id>:<message_id>"；非 dc: 前缀或格式非法返回 ok=false。
func parseMsgID(s string) (channelID, messageID string, ok bool) {
	if !strings.HasPrefix(s, idPrefix) {
		return "", "", false
	}
	raw := strings.TrimPrefix(s, idPrefix)
	sep := strings.LastIndex(raw, ":")
	if sep <= 0 || sep == len(raw)-1 {
		return "", "", false
	}
	return raw[:sep], raw[sep+1:], true
}

// parseChannelID 解析 "dc:<channel_id>"；非 dc: 前缀（如 QQ 的 qq: 数字 ID）返回 ok=false，
// 避免把其他平台/默认平台的 ID 误当作本平台目标。
func parseChannelID(q message.QID) (string, bool) {
	s := q.String()
	if !strings.HasPrefix(s, idPrefix) {
		return "", false
	}
	raw := strings.TrimPrefix(s, idPrefix)
	if raw == "" || strings.Contains(raw, ":") {
		return "", false
	}
	return raw, true
}

// userQID 构造框架内用户 ID："dc:<user_id>"。
func userQID(userID string) message.QID {
	return message.QID(idPrefix + userID)
}
