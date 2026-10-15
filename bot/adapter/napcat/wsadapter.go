package napcat

import (
	"encoding/json"
	"fmt"
	"log"
	neturl "net/url"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/spf13/viper"
)

type napcatWebSocketAdapter struct {
	wsConn      *websocket.Conn
	trigger     adapter.TriggerWrapper
	ackMng      *ackManager // ackMng.timeout 为普通请求超时
	mu          sync.Mutex
	msgCh       chan []byte
	workerWg    sync.WaitGroup
	workerCount int
	queueSize   int
	// uploadTimeout 上传类请求（文件/视频/语音等，见 isUploadRequest）的等待超时
	uploadTimeout time.Duration

	// 连接状态（供 Web 面板展示）
	connState string // connecting | connected | reconnecting
	lastErr   string
	failCount int
}

type ackManager struct {
	pendingAcks sync.Map
	timeout     time.Duration
}

type wsPushData[T any] struct {
	Action string `json:"action"`
	Params T      `json:"params"`
	Echo   string `json:"echo"`
}

// Connected 返回当前 WebSocket 连接是否就绪（false 表示断线重连中）。
func (n *napcatWebSocketAdapter) Connected() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.wsConn != nil
}

// setStatus 更新连接状态（供 Web 面板展示）。
func (n *napcatWebSocketAdapter) setStatus(state, errDetail string, failCount int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.connState = state
	n.lastErr = errDetail
	n.failCount = failCount
}

// AdapterStatus 返回连接状态（connecting/connected/reconnecting）与详情（最近错误或重试次数）。
func (n *napcatWebSocketAdapter) AdapterStatus() (state string, detail string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	state = n.connState
	if state == "" {
		state = "connecting"
	}
	if state == "reconnecting" && n.failCount > 0 {
		return state, fmt.Sprintf("第 %d 次重试: %s", n.failCount, n.lastErr)
	}
	return state, n.lastErr
}

// echoSeq 全局递增序号，保证并发请求在同一纳秒时间戳下 echo 仍唯一
var echoSeq atomic.Uint64

func request[T any](n *napcatWebSocketAdapter, action string, params any, prefix string) (*T, bool) {

	echo := fmt.Sprintf("%s:%d:%d", prefix, time.Now().UnixNano(), echoSeq.Add(1))

	req := wsPushData[any]{
		Action: action,
		Params: params,
		Echo:   echo,
	}
	b, err := json.Marshal(req)
	if err != nil {
		log.Printf("[%s] 序列化失败: %v", action, err)
		return nil, false
	}

	resCh := make(chan []byte, 1)
	n.ackMng.pendingAcks.Store(echo, resCh)
	defer n.ackMng.pendingAcks.Delete(echo)

	n.mu.Lock()
	if n.wsConn == nil {
		n.mu.Unlock()
		log.Printf("[%s] 连接未就绪（重连中），跳过发送", action)
		return nil, false
	}
	err = n.wsConn.WriteMessage(websocket.TextMessage, b)
	n.mu.Unlock()
	if err != nil {
		log.Printf("[%s] 发送失败: %v", action, err)
		return nil, false
	}

	// 上传类调用（文件/视频/语音等）NapCat 需先把内容上传到服务器再回包，耗时远高于
	// 普通接口，用单独的上传超时等待，避免慢上传被误判为发送失败
	upload := isUploadRequest(action, params)
	timeout := n.ackMng.timeout
	if upload {
		timeout = n.uploadTimeout
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case data := <-resCh:
		var resp message.Response[T]
		if err := json.Unmarshal(data, &resp); err != nil {
			log.Printf("[%s] 解析响应失败: %v", action, err)
			return nil, false
		}
		if resp.Status != "ok" {
			log.Printf("[%s] 消息发送失败\nparams: %v\necho: %s\nresp: %s", action, params, echo, string(data))
			return nil, false
		}
		return &resp.Data, true
	case <-timer.C:
		if upload {
			// 超时仅表示结果未知：NapCat 侧的上传并未取消，内容可能仍会送达，勿重发
			log.Printf("[%s] 上传超时（结果未知：内容可能仍在发送、稍后送达，请勿重发）, echo: %s", action, echo)
		} else {
			log.Printf("[%s] 请求超时, echo: %s", action, echo)
		}
		return nil, false
	}
}

func (n *napcatWebSocketAdapter) SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (message.QID, bool) {
	params := map[string]any{"group_id": rawQQ(groupId), "message": stripQQSegments(chain.GetGroupMsg())}
	res, ok := request[message.Message](n, "send_group_msg", params, "ack")
	if !ok || res == nil {
		return "", false
	}
	message.NormalizeQQMessage(res)
	return res.MessageId, true
}

func (n *napcatWebSocketAdapter) SendGroupAIVoiceMsg(groupId message.QID, character, msg string) (message.QID, bool) {
	params := message.AiVoiceMsg{GroupId: message.QID(rawQQ(groupId)), Character: character, Text: msg}
	res, ok := request[message.Message](n, "send_group_ai_record", params, "ack")
	if !ok || res == nil {
		return "", false
	}
	message.NormalizeQQMessage(res)
	return res.MessageId, true
}

func (n *napcatWebSocketAdapter) SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (message.QID, bool) {
	params := map[string]any{"user_id": rawQQ(userId), "message": stripQQSegments(chain.GetFriendMsg())}
	res, ok := request[message.Message](n, "send_private_msg", params, "ack")
	if !ok || res == nil {
		return "", false
	}
	message.NormalizeQQMessage(res)
	return res.MessageId, true
}

func (n *napcatWebSocketAdapter) SendGroupForwardMsg(groupId message.QID, chain msgchain.GroupForwardChain) (message.QID, bool) {
	params := message.GroupForwardMessage{GroupId: message.QID(rawQQ(groupId)), ForwardMessageSegment: stripQQForward(chain.GetForwardMsg())}
	res, ok := request[message.Message](n, "send_forward_msg", params, "ack")
	if !ok || res == nil {
		return "", false
	}
	message.NormalizeQQMessage(res)
	return res.MessageId, true
}

func (n *napcatWebSocketAdapter) SendFriendForwardMsg(userId message.QID, chain msgchain.FriendForwardChain) (message.QID, bool) {
	params := message.FriendForwardMessage{UserId: message.QID(rawQQ(userId)), ForwardMessageSegment: stripQQForward(chain.GetForwardMsg())}
	res, ok := request[message.Message](n, "send_forward_msg", params, "ack")
	if !ok || res == nil {
		return "", false
	}
	message.NormalizeQQMessage(res)
	return res.MessageId, true
}

func (n *napcatWebSocketAdapter) GetMsgDetail(msgId message.QID) (*message.Message, bool) {
	params := map[string]message.QID{"message_id": message.QID(rawQQ(msgId))}
	res, ok := request[message.Message](n, "get_msg", params, "dt")
	if ok && res != nil {
		message.NormalizeQQMessage(res)
	}
	return res, ok
}

func (n *napcatWebSocketAdapter) GetForwardMsg(msgId message.QID) (*[]message.Message, bool) {
	params := map[string]message.QID{"message_id": message.QID(rawQQ(msgId))}
	type fwData struct {
		Messages []message.Message `json:"messages"`
	}
	res, ok := request[fwData](n, "get_forward_msg", params, "fw")
	if !ok || res == nil {
		return nil, false
	}
	for i := range res.Messages {
		message.NormalizeQQMessage(&res.Messages[i])
	}
	return &res.Messages, true
}

func (n *napcatWebSocketAdapter) GetGroupUserInfo(groupId, userId message.QID) (*message.GroupUserInfo, bool) {
	params := map[string]any{"group_id": rawQQ(groupId), "user_id": rawQQ(userId), "no_cache": true}
	return request[message.GroupUserInfo](n, "get_group_member_info", params, "ugif")
}

func (n *napcatWebSocketAdapter) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	params := map[string]any{"group_id": rawQQ(groupId), "no_cache": noCache}
	return request[[]message.GroupUserInfo](n, "get_group_member_list", params, "mlist")
}

func (n *napcatWebSocketAdapter) GetNCrkey() ([]message.NCrkey, bool) {
	res, ok := request[[]message.NCrkey](n, "nc_get_rkey", struct{}{}, "ncrkey")
	if !ok || res == nil {
		return nil, false
	}
	return *res, true
}

func (n *napcatWebSocketAdapter) SendPokeMsg(userId message.QID, groupId *message.QID) (success bool) {
	params := map[string]message.QID{"user_id": message.QID(rawQQ(userId))}
	if groupId != nil {
		params["group_id"] = message.QID(rawQQ(*groupId))
	}
	_, ok := request[any](n, "send_poke", params, "ack")
	if !ok {
		return false
	}
	return true
}

func (n *napcatWebSocketAdapter) GetFriendList() (*[]message.Friend, bool) {
	res, ok := request[[]message.Friend](n, "get_friend_list", struct{}{}, "friend_list")
	if !ok || res == nil {
		return nil, false
	}
	return res, true
}

func (n *napcatWebSocketAdapter) GetGroupDetail(groupId message.QID) (*message.GroupInfo, bool) {
	params := map[string]message.QID{"group_id": message.QID(rawQQ(groupId))}
	return request[message.GroupInfo](n, "get_group_detail_info", params, "group_info")
}

func (n *napcatWebSocketAdapter) SetMsgEmojiLike(msgId message.QID, emojiId int, like bool) (success bool) {
	params := message.EmojiLike{MessageID: message.QID(rawQQ(msgId)), EmojiId: emojiId, Set: like}
	_, ok := request[any](n, "set_msg_emoji_like", params, "ack")
	if !ok {
		return false
	}
	return true
}

func (n *napcatWebSocketAdapter) SendGroupSign(groupId message.QID) (success bool) {
	params := map[string]message.QID{
		"group_id": message.QID(rawQQ(groupId)),
	}
	_, ok := request[any](n, "send_group_sign", params, "ack")
	if !ok {
		return false
	}
	return true
}

func (n *napcatWebSocketAdapter) GetGroupMsgHistory(groupId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	params := historyParams("group_id", groupId, count, message_seq)
	type fwData struct {
		Messages []message.Message `json:"messages"`
	}
	res, ok := request[fwData](n, "get_group_msg_history", params, "fw")
	if !ok || res == nil {
		return nil, false
	}
	for i := range res.Messages {
		message.NormalizeQQMessage(&res.Messages[i])
	}
	return &res.Messages, true
}

func (n *napcatWebSocketAdapter) GetFriendMsgHistory(userId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	params := historyParams("user_id", userId, count, message_seq)
	type fwData struct {
		Messages []message.Message `json:"messages"`
	}
	res, ok := request[fwData](n, "get_friend_msg_history", params, "fw")
	if !ok || res == nil {
		return nil, false
	}
	for i := range res.Messages {
		message.NormalizeQQMessage(&res.Messages[i])
	}
	return &res.Messages, true
}

func (n *napcatWebSocketAdapter) GetAIChatacter() (*[]message.AIChatacter, bool) {
	res, ok := request[message.AIChatacterResp](n, "get_ai_chatacter", struct{}{}, "ai_chatacter")
	if !ok || res == nil {
		return nil, false
	}
	return &res.Characters, true
}

func (n *napcatWebSocketAdapter) GetPrivateFileURL(userId message.QID, fileId string) (string, bool) {
	params := map[string]any{
		"user_id": rawQQ(userId),
		"file_id": fileId,
	}
	type privateFileData struct {
		URL string `json:"url"`
	}
	res, ok := request[privateFileData](n, "get_private_file_url", params, "pfu")
	if !ok || res == nil {
		return "", false
	}
	return res.URL, true
}

func (n *napcatWebSocketAdapter) GetGroupList() (*[]message.GroupInfo, bool) {
	res, ok := request[[]message.GroupInfo](n, "get_group_list", struct{}{}, "group_list")
	if !ok || res == nil {
		return nil, false
	}
	return res, true
}

func (n *napcatWebSocketAdapter) Serve(v *viper.Viper) {
	url := v.GetString("bot.adapter.ws.address")

	// worker pool configuration
	n.workerCount = v.GetInt("bot.adapter.ws.worker_count")
	if n.workerCount <= 0 {
		n.workerCount = runtime.NumCPU() * 2
	}
	n.queueSize = v.GetInt("bot.adapter.ws.worker_queue_size")
	if n.queueSize <= 0 {
		n.queueSize = 256
	}

	log.Println("已启用 napcat websocket adapter")
	n.setStatus("connecting", "", 0)

	// 连接失败不退出：无限重试（指数退避，封顶 30s），
	// 连接状态与最近错误通过 AdapterStatus 暴露给 Web 面板，
	// 用户可在面板修改连接配置后重启生效。
	const maxBackoff = 30 * time.Second
	failCount := 0
	for {
		var conn *websocket.Conn
		var err error
		if v.IsSet("bot.adapter.token") {
			token := v.GetString("bot.adapter.token")
			// token 需 URL 编码：含 + / = & 等特殊字符时直接拼接会破坏 query 解析，
			// 导致 NapCat 侧鉴权拿到的 token 与配置不一致（握手 401 且排查困难）
			conn, _, err = websocket.DefaultDialer.Dial(url+"?access_token="+neturl.QueryEscape(token), nil)
		} else {
			conn, _, err = websocket.DefaultDialer.Dial(url, nil)
		}
		if err != nil {
			failCount++
			n.setStatus("reconnecting", err.Error(), failCount)
			backoff := min(time.Second*time.Duration(1<<min(failCount-1, 5)), maxBackoff)
			log.Printf("连接失败（第 %d 次）: %v. %v 后重试...（可在 Web 面板修改连接配置后重启）", failCount, err, backoff)
			time.Sleep(backoff)
			continue
		}
		failCount = 0
		n.setStatus("connected", "", 0)
		log.Println("WebSocket 连接成功！")
		n.mu.Lock()
		n.wsConn = conn
		n.mu.Unlock()
		n.startWorkerPool(n.workerCount, n.queueSize)
		n.readLoop(conn)
		n.stopWorkerPool()
		n.mu.Lock()
		n.wsConn = nil
		n.mu.Unlock()
		n.setStatus("reconnecting", "连接已断开", 0)
		log.Println("连接断开，准备重连...")
	}
}

func (n *napcatWebSocketAdapter) readLoop(conn *websocket.Conn) {
	defer conn.Close()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Println("读取数据失败:", err)
			return
		}
		// ACK 必须在 readLoop 直接投递而不能进 worker 池：worker 可能全部
		// 阻塞在 request() 等待 ACK，若 ACK 也排队等空闲 worker 会互相饿死直到超时
		if n.deliverAck(msg) {
			continue
		}
		if n.msgCh != nil {
			select {
			case n.msgCh <- msg:
			default:
				log.Println("消息队列已满，丢弃消息")
			}
		} else {
			go n.onMsg(msg)
		}
	}
}

// deliverAck 识别带 echo 的响应帧并直接派发给等待中的 request()，
// 返回是否为响应帧（事件帧无 echo 字段，返回 false 交由 worker 池处理）。
func (n *napcatWebSocketAdapter) deliverAck(data []byte) bool {
	var frame struct {
		Echo string `json:"echo"`
	}
	if err := json.Unmarshal(data, &frame); err != nil || frame.Echo == "" {
		return false
	}
	if chIf, exists := n.ackMng.pendingAcks.Load(frame.Echo); exists {
		select {
		case chIf.(chan []byte) <- data:
		default:
		}
	}
	return true
}

func (n *napcatWebSocketAdapter) SetTrigger(trigger adapter.TriggerWrapper) {
	n.trigger = trigger
}

func (n *napcatWebSocketAdapter) startWorkerPool(count, qsize int) {
	if count <= 0 {
		count = 4
	}
	if qsize <= 0 {
		qsize = 256
	}
	n.msgCh = make(chan []byte, qsize)
	for i := 0; i < count; i++ {
		n.workerWg.Go(func() {
			for data := range n.msgCh {
				n.onMsg(data)
			}
		})
	}
	log.Printf("启动 napcat websocket worker pool: workers=%d queue=%d", count, qsize)
}

func (n *napcatWebSocketAdapter) stopWorkerPool() {
	if n.msgCh == nil {
		return
	}
	close(n.msgCh)
	n.workerWg.Wait()
	n.msgCh = nil
	log.Println("已停止 napcat websocket worker pool")
}

func (n *napcatWebSocketAdapter) onMsg(data []byte) {
	var callBack map[string]any
	if err := json.Unmarshal(data, &callBack); err != nil {
		return
	}

	postType, _ := callBack["post_type"].(string)
	switch postType {
	case "message", "message_sent":
		var msg message.Message
		if err := json.Unmarshal(data, &msg); err == nil {
			message.NormalizeQQMessage(&msg)
			if msg.MessageType == "group" && n.trigger.OnGroupMsg != nil {
				if msg.RawMessage != "" {
					n.trigger.OnGroupMsg(msg)
				}
			} else if msg.MessageType == "private" && msg.SubType == "friend" && n.trigger.OnFriendMsg != nil {
				if msg.RawMessage != "" {
					n.trigger.OnFriendMsg(msg)
				}
			}
		}
	case "notice":
		noticeType, _ := callBack["notice_type"].(string)
		wsSpreadNotice(n, noticeType, data)
	}
}

func wsSpreadNotice(n *napcatWebSocketAdapter, noticeType string, data []byte) {
	handleNotice(n.trigger, noticeType, data)
}
