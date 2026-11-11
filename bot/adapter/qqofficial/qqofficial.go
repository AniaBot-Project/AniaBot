package qqofficial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/gorilla/websocket"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
)

type qqOfficialConfig struct {
	appID     string
	appSecret string
	sandbox   bool
	apiBase   string
	markdown  bool // 文本回复以 msg_type=2 Markdown 消息发送
	autoQuote bool // 回复时自动引用触发消息（message_reference 引用气泡）
}

// qqOfficialAdapter QQ 官方机器人平台适配器：WebSocket 网关收事件 + REST OpenAPI 发消息。
// 事件投递为 at-least-once（「相同 msg_id 可能重复推送」），由 core 按
// EventKeyer.MessageKey（消息 ID）去重。
type qqOfficialAdapter struct {
	mu      sync.Mutex
	trigger adapter.TriggerWrapper
	cfg     qqOfficialConfig

	tokens *tokenManager
	client *qqClient

	// self 机器人自身信息（READY 事件填充：ID/Username）
	selfID       string
	selfUsername string

	// session WebSocket 会话状态（断线重连 resume 用）
	sessionID string
	lastSeq   int

	// msgCache 入站/出站消息内存缓存（官方无消息查询/历史端点，GetMsgDetail/历史用它兜底）
	msgCache *msgCache
	// replyTokens 会话被动回复凭证（群 openid/用户 openid -> 最近事件的 msg_id 与回复序号）
	replyTokens sync.Map

	connState string
	lastErr   string

	logger *slog.Logger
}

func NewAdapter(cfg *viper.Viper) *qqOfficialAdapter {
	return &qqOfficialAdapter{
		msgCache: newMsgCache(msgCachePerChat, msgCacheMaxChats),
		logger:   slog.Default().With("adapter", "qqofficial"),
	}
}

func (a *qqOfficialAdapter) Name() string     { return "qqofficial" }
func (a *qqOfficialAdapter) Platform() string { return Platform }

// MessageKey 实现 adapter.EventKeyer：按消息 ID 去重。
// 官方明确「相同 msg_id 可能重复推送」，消息 ID 稳定且全局唯一，适合做去重键。
func (a *qqOfficialAdapter) MessageKey(msg message.Message) (string, bool) {
	raw := msg.MessageId.TrimPrefix(idPrefix)
	if raw == "" {
		return "", false
	}
	return "msg:" + raw, true
}

// NoticeKey 实现 adapter.EventKeyer：好友添加等事件无稳定 ID，不去重。
func (a *qqOfficialAdapter) NoticeKey(noticeType string, notice any) (string, bool) {
	return "", false
}

// SelfID 实现 adapter.SelfIDProvider：返回机器人自身 ID（qo:<READY user.id>）。
// 群聊入站消息会注入 qq=SelfId 的 at 段（使 aichat 的 @ 触发生效，
// 官方群事件本就是 @机器人才推送），二者必须同为 qo: 前缀。
func (a *qqOfficialAdapter) SelfID() message.QID {
	return a.selfQID()
}

// qqOfficialSegments QQ 官方出站支持的通用段类型。
// at 段无法渲染（官方群消息不支持 @ 成员，见 send.go 注释）但仍声明，
// 由 sendChain 静默退化；face/json/music/forward 在 default 分支退化。
var qqOfficialSegments = []string{
	message.SegmentText, message.SegmentMention, message.SegmentImage,
	message.SegmentReply, message.SegmentFile, message.SegmentRecord,
	message.SegmentVideo,
}

// SupportedSegments 实现 adapter.SegmentSupport。
func (a *qqOfficialAdapter) SupportedSegments() []string { return qqOfficialSegments }

func (a *qqOfficialAdapter) SetTrigger(trigger adapter.TriggerWrapper) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trigger = trigger
}

func (a *qqOfficialAdapter) triggerOf() adapter.TriggerWrapper {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trigger
}

func (a *qqOfficialAdapter) setStatus(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connState = state
	a.lastErr = detail
}

// AdapterStatus 返回连接状态（connecting/connected/reconnecting）与详情（面板状态探针）。
func (a *qqOfficialAdapter) AdapterStatus() (string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connState == "" {
		return "connecting", ""
	}
	return a.connState, a.lastErr
}

// Connected 网关是否已就绪（面板状态探针兜底）。
func (a *qqOfficialAdapter) Connected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState == "connected"
}

// selfQID 框架内表示机器人自身的统一 ID（qo:<READY user.id>），未就绪为空。
func (a *qqOfficialAdapter) selfQID() message.QID {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.selfID == "" {
		return ""
	}
	return message.QID(idPrefix + a.selfID)
}

// selfInfo 机器人自身原始 ID 与用户名（READY user.id / username，无前缀），未就绪为空。
// 全量模式过滤自身消息与 mentions 自身提及识别使用。
func (a *qqOfficialAdapter) selfInfo() (id, username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selfID, a.selfUsername
}

func (a *qqOfficialAdapter) loadConfig(v *viper.Viper) qqOfficialConfig {
	apiBase := strings.TrimSpace(v.GetString("bot.qqofficial.api_base"))
	if apiBase == "" {
		apiBase = "https://api.sgroup.qq.com"
	}
	if v.GetBool("bot.qqofficial.sandbox") {
		apiBase = "https://sandbox.api.sgroup.qq.com"
	}
	return qqOfficialConfig{
		appID:     strings.TrimSpace(v.GetString("bot.qqofficial.app_id")),
		appSecret: strings.TrimSpace(v.GetString("bot.qqofficial.app_secret")),
		sandbox:   v.GetBool("bot.qqofficial.sandbox"),
		apiBase:   apiBase,
		markdown:  v.GetBool("bot.qqofficial.markdown"),
		autoQuote: v.GetBool("bot.qqofficial.auto_quote"),
	}
}

// Serve 启动 QQ 官方适配器（阻塞）：token 校验 → 网关重连循环。
// Serve 返回即适配器死亡（core 不再拉起），连接失败不可早退。
func (a *qqOfficialAdapter) Serve(v *viper.Viper) {
	a.cfg = a.loadConfig(v)
	if a.cfg.appID == "" || a.cfg.appSecret == "" {
		a.setStatus("reconnecting", "未配置 bot.qqofficial.app_id / app_secret，请在 Web 面板配置后重启")
		a.logger.Warn("QQ 官方适配器未配置 AppID/AppSecret，无法启动")
		return
	}
	tokenHTTP := resty.New().SetTimeout(15 * time.Second)
	a.tokens = newTokenManager(a.cfg.appID, a.cfg.appSecret, tokenHTTP)
	a.client = newQQClient(a.cfg.apiBase, a.tokens)

	// 预先换取一次 token：AppID/AppSecret 错误能在启动期暴露（不影响后续自动刷新重试）。
	a.setStatus("connecting", "正在换取 access_token")
	ctx := context.Background()
	if _, err := a.tokens.Token(ctx); err != nil {
		a.setStatus("reconnecting", "获取 access_token 失败: "+err.Error())
		a.logger.Warn("QQ 官方获取 access_token 失败，进入重连循环", "error", err)
	} else {
		a.logger.Info("QQ 官方 access_token 获取成功", "appId", a.cfg.appID, "apiBase", a.cfg.apiBase)
	}

	a.serveLoop(ctx)
}

// ---------- WebSocket 网关 ----------

// 连接终止原因分类：决定下一轮重连走 resume 还是 identify。
var (
	// errResume 连接断开但会话可恢复（服务端 op 7 重连通知 / op 9 且 d=true / 读写出错）。
	errResume = errors.New("qqofficial ws: 连接断开，尝试 resume 重连")
	// errInvalidSession 会话失效（op 9 且 d=false），需清空 session 重新 identify。
	errInvalidSession = errors.New("qqofficial ws: 会话失效，重新 identify")
)

// serveLoop 网关重连主循环：connectOnce 返回后按会话状态选择 resume/identify，
// 指数退避（1s→30s 封顶），成功进入过 READY/RESUMED 状态后重置退避。
func (a *qqOfficialAdapter) serveLoop(ctx context.Context) {
	backoff := time.Second
	for {
		wasReady, err := a.connectOnce(ctx)
		if wasReady {
			backoff = time.Second
		}
		if errors.Is(err, errInvalidSession) {
			a.mu.Lock()
			a.sessionID, a.lastSeq = "", 0
			a.mu.Unlock()
			a.logger.Warn("QQ 官方网关会话失效，将重新 identify")
		}
		a.setStatus("reconnecting", err.Error())
		a.logger.Warn("QQ 官方网关连接断开，重连中", "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// connectOnce 建立一次网关连接并阻塞到连接断开。
// 返回 wasReady=true 表示本次连接成功完成过鉴权（收到 READY/RESUMED）。
// 会话有效（sessionID + lastSeq）时走 OpCode 6 resume，否则 OpCode 2 identify。
func (a *qqOfficialAdapter) connectOnce(ctx context.Context) (bool, error) {
	a.setStatus("connecting", "正在获取网关地址")
	wssURL, err := a.gatewayURL(ctx)
	if err != nil {
		return false, fmt.Errorf("获取网关地址失败: %w", err)
	}
	a.setStatus("connecting", "正在连接网关")
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wssURL, nil)
	if err != nil {
		return false, fmt.Errorf("连接网关失败: %w", err)
	}
	defer conn.Close()

	// 首条消息必须是 OpCode 10 Hello（携带心跳周期）
	var hello wsPayload
	if err := conn.ReadJSON(&hello); err != nil {
		return false, fmt.Errorf("读取 Hello 失败: %w", err)
	}
	if hello.Op != opHello {
		return false, fmt.Errorf("网关首条消息不是 Hello (op=%d)", hello.Op)
	}
	var hd helloData
	if err := json.Unmarshal(hello.D, &hd); err != nil || hd.HeartbeatInterval <= 0 {
		return false, fmt.Errorf("Hello 心跳周期非法: %w", err)
	}

	c := &wsSession{
		conn:     conn,
		interval: time.Duration(hd.HeartbeatInterval) * time.Millisecond,
		lastAck:  time.Now(),
		done:     make(chan struct{}),
		adapter:  a,
	}
	defer close(c.done)
	go c.heartbeatLoop()

	// 鉴权：有会话则 resume（服务端补发 seq 之后的事件），否则 identify
	a.mu.Lock()
	sessionID, lastSeq := a.sessionID, a.lastSeq
	a.mu.Unlock()
	token, err := a.tokens.Token(ctx)
	if err != nil {
		return false, fmt.Errorf("获取 access_token 失败: %w", err)
	}
	resuming := sessionID != "" && lastSeq > 0
	if resuming {
		if err := c.writeJSON(wsPayload{Op: opResume, D: mustMarshal(resumeData{
			Token: "QQBot " + token, SessionID: sessionID, Seq: lastSeq,
		})}); err != nil {
			return false, fmt.Errorf("发送 Resume 失败: %w", err)
		}
	} else {
		if err := c.writeJSON(wsPayload{Op: opIdentify, D: mustMarshal(identifyData{
			Token:   "QQBot " + token,
			Intents: intentGroupAndC2C,
			Shard:   [2]int{0, 1},
			Properties: map[string]any{
				"$os": runtime.GOOS, "$browser": "AniaBot", "$device": "AniaBot",
			},
		})}); err != nil {
			return false, fmt.Errorf("发送 Identify 失败: %w", err)
		}
	}

	return c.readLoop()
}

// gatewayURL 获取通用 WSS 接入点（GET /gateway）。
func (a *qqOfficialAdapter) gatewayURL(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var gr gatewayResponse
	if err := a.client.getJSON(ctx, "/gateway", &gr); err != nil {
		return "", err
	}
	if gr.URL == "" {
		return "", errors.New("响应缺少 url 字段")
	}
	return gr.URL, nil
}

// wsSession 一次网关连接的会话状态：串行化写、心跳与僵尸检测。
type wsSession struct {
	conn     *websocket.Conn
	writeMu  sync.Mutex
	interval time.Duration
	ackMu    sync.Mutex
	lastAck  time.Time
	done     chan struct{} // 连接关闭信号（由 connectOnce defer 关闭）
	adapter  *qqOfficialAdapter
}

// writeJSON 串行化写入一条网关消息（gorilla/websocket 不允许并发写）。
func (c *wsSession) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

// heartbeatLoop 按周期发送 OpCode 1 心跳（d 为已收到的最大序列号，首次为 null）。
// 僵尸检测：超过两个心跳周期未收到 OpCode 11 ACK 时主动断开连接触发重连。
func (c *wsSession) heartbeatLoop() {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}
		c.ackMu.Lock()
		stale := time.Since(c.lastAck) > 2*c.interval
		c.ackMu.Unlock()
		if stale {
			c.adapter.logger.Warn("QQ 官方网关心跳超时（僵尸连接），主动断开重连")
			c.conn.Close()
			return
		}
		c.adapter.mu.Lock()
		seq := c.adapter.lastSeq
		c.adapter.mu.Unlock()
		var d any
		if seq > 0 {
			d = seq
		}
		if err := c.writeJSON(wsPayload{Op: opHeartbeat, D: mustMarshal(d)}); err != nil {
			c.conn.Close()
			return
		}
	}
}

// readLoop 网关读循环：分发事件、维护序列号、处理服务端重连/失效指令。
// 事件处理投递到独立 goroutine（翻译含 REST I/O，不阻塞读循环保证心跳及时）。
func (c *wsSession) readLoop() (bool, error) {
	a := c.adapter
	ready := false
	for {
		var p wsPayload
		if err := c.conn.ReadJSON(&p); err != nil {
			return ready, fmt.Errorf("%w: %v", errResume, err)
		}
		switch p.Op {
		case opDispatch:
			if p.S > 0 {
				a.mu.Lock()
				a.lastSeq = p.S
				a.mu.Unlock()
			}
			switch p.T {
			case "READY":
				var rd readyData
				if err := json.Unmarshal(p.D, &rd); err != nil {
					a.logger.Warn("解析 READY 事件失败", "error", err)
					continue
				}
				a.mu.Lock()
				a.sessionID = rd.SessionID
				a.selfID = rd.User.ID
				a.selfUsername = rd.User.Username
				a.mu.Unlock()
				ready = true
				a.setStatus("connected", "")
				a.logger.Info("QQ 官方网关鉴权成功", "botId", rd.User.ID, "username", rd.User.Username, "sessionId", rd.SessionID)
			case "RESUMED":
				ready = true
				a.setStatus("connected", "")
				a.logger.Info("QQ 官方网关会话恢复成功")
			default:
				go a.handleEvent(p.T, p.D)
			}
		case opHeartbeat:
			// 服务端心跳（少见）：立即回 ACK 语义的心跳帧
			a.mu.Lock()
			seq := a.lastSeq
			a.mu.Unlock()
			var d any
			if seq > 0 {
				d = seq
			}
			_ = c.writeJSON(wsPayload{Op: opHeartbeat, D: mustMarshal(d)})
		case opHeartbeatACK:
			c.ackMu.Lock()
			c.lastAck = time.Now()
			c.ackMu.Unlock()
		case opReconnect:
			return ready, errResume
		case opInvalidSess:
			// d=true 表示可以 resume 重试，false 表示必须重新 identify
			canResume := false
			if len(p.D) > 0 {
				_ = json.Unmarshal(p.D, &canResume)
			}
			if canResume {
				return ready, errResume
			}
			return ready, errInvalidSession
		}
	}
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
