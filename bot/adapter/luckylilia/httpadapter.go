package luckylilia

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/spf13/viper"
)

type luckyliliaHttpAdapter struct {
	baseUrl    string
	token      *string
	httpClient *resty.Client
	trigger    adapter.TriggerWrapper
	// reqTimeout 普通请求等待超时；uploadTimeout 上传类请求（文件/视频/语音等）等待超时
	reqTimeout    time.Duration
	uploadTimeout time.Duration
}

// maxEventBodySize 上报事件体上限：容忍 base64 大图/语音等大事件，
// 同时防止无界读取把内存撑爆
const maxEventBodySize = 64 << 20 // 64MB

func (n *luckyliliaHttpAdapter) createContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// postAndCheck 调用 LLBot 的 HTTP API：Bearer 头鉴权（llms.txt Token 鉴权文档），
// 同时携带 access_token 查询参数兼容仅识别 OneBot v11 标准查询参数的协议端。
func (n *luckyliliaHttpAdapter) postAndCheck(url string, body any, result any) bool {
	// 上传类调用 LLBot 需先把内容上传到服务器再回包，耗时远高于普通接口，
	// 用单独的上传超时等待，避免慢上传被误判为发送失败
	upload := isUploadRequest(actionFromURL(url), body)
	timeout := n.reqTimeout
	if upload {
		timeout = n.uploadTimeout
	}
	ctx, cancel := n.createContext(timeout)
	defer cancel()

	req := n.httpClient.R().SetContext(ctx)
	if body != nil {
		req = req.SetBody(body)
	}
	if result != nil {
		req = req.SetResult(result)
	}
	if n.token != nil {
		req = req.SetHeader("Authorization", "Bearer "+*n.token)
		req = req.SetQueryParam("access_token", *n.token)
	}
	resp, err := req.Post(url)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			if upload {
				// 超时仅表示结果未知：LLBot 侧的上传并未取消，内容可能仍会送达，勿重发
				log.Printf("上传请求超时（结果未知：内容可能仍在发送、稍后送达，请勿重发）: %s", url)
			} else {
				log.Printf("HTTP请求超时: %s", url)
			}
			return false
		}
		log.Printf("HTTP请求失败: %v", err)
		return false
	}
	if resp.StatusCode() != http.StatusOK {
		log.Printf("HTTP响应异常: %d", resp.StatusCode())
		return false
	}
	return true
}

func checkResponseStatus[T any](resp *message.Response[T]) bool {
	return resp != nil && resp.Status == "ok"
}

func (n *luckyliliaHttpAdapter) Serve(v *viper.Viper) {
	// httpClient 已在构造函数中就绪（见 NewLuckyliliaHttpAdapter）
	n.baseUrl = strings.TrimRight(v.GetString("bot.luckylilia.http.target_url"), "/")
	if v.IsSet("bot.luckylilia.token") {
		token := v.GetString("bot.luckylilia.token")
		n.token = &token
	}
	port := v.GetInt("bot.luckylilia.http.listen_port")

	// 独立 ServeMux + Server：不占用 DefaultServeMux，避免与 napcat HTTP 模式
	// 同时启用时在 "/" 路由上的重复注册 panic 与端口冲突
	mux := http.NewServeMux()
	mux.HandleFunc("/", n.handler)
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}

	log.Println("已启用 luckylilia http adapter")
	if n.token == nil || *n.token == "" {
		// fail-closed：HTTP 模式下 Bot 是被动接收上报的一方，未配置有效 token
		// 时无法甄别事件来源，若放行则任何能访问该端口的主机都能伪造事件
		// （冒充管理员等），因此拒绝全部上报并在日志中提示配置方式
		log.Printf("警告: 未配置 bot.luckylilia.token，HTTP 上报接口将拒绝所有事件（请在面板配置 token 并同步到 LLBot 的 HTTP 客户端后重启）")
	}
	// 先绑定端口再打「已启动」日志：绑定失败（端口被占等）时不会误报服务已就绪
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		// 不 Fatal：保持面板可访问，用户可在面板修正端口后重启
		log.Printf("HTTP服务器启动失败（端口 %d 可能被占用），将无法接收LLBot事件: %v", port, err)
		return
	}
	log.Printf("本地HTTP服务器已启动 http://localhost:%d...（上报需携带 token）\n", port)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		// 不 Fatal：保持面板可访问，用户可在面板修正端口后重启
		log.Printf("HTTP服务器异常退出，将无法接收LLBot事件: %v", err)
	}
}

func (n *luckyliliaHttpAdapter) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	// HTTP 模式下 Bot 是被动接收方，必须校验上报来源：
	// LLBot 的 HTTP 客户端配置 token 后会在上报请求携带 Authorization: Bearer <token>
	// 未配置有效 token 时按未授权处理（fail-closed），防止伪造事件注入
	if n.token == nil || *n.token == "" || !n.checkInToken(r) {
		log.Printf("拒绝未授权的HTTP上报: %s", r.RemoteAddr)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEventBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			log.Printf("HTTP上报超过大小上限(%d字节), 来自 %s", maxEventBodySize, r.RemoteAddr)
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		log.Printf("无法读取HTTP请求内容: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.Printf("关闭HTTP请求体出错: %v", err.Error())
		}
	}()
	n.onMsg(body)
}

// checkInToken 校验 LLBot 上报请求的 token，兼容 Authorization: Bearer 头与 access_token 查询参数两种形式。
// token 是共享密钥：必须精确匹配（大小写不敏感比较会扩大可猜测面），
// 且用常量时间比较，避免逐字节比较的时序侧信道泄露前缀。
func (n *luckyliliaHttpAdapter) checkInToken(r *http.Request) bool {
	if auth := r.Header.Get("Authorization"); auth != "" {
		return subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+*n.token)) == 1
	}
	return subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("access_token")), []byte(*n.token)) == 1
}

func (n *luckyliliaHttpAdapter) onMsg(data []byte) {
	var callBack map[string]any
	if err := json.Unmarshal(data, &callBack); err != nil {
		return
	}
	postType, _ := callBack["post_type"].(string)
	switch postType {
	case "message", "message_sent":
		var msg message.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Println("解析HTTP消息失败:", err)
			return
		}
		message.NormalizeQQMessage(&msg)
		relilMessage(&msg)
		// 过滤规则与 WS 适配器保持一致：私聊仅投递好友消息（sub_type=friend，
		// 排除群临时会话等），且忽略 raw_message 为空的事件
		switch msg.MessageType {
		case "group":
			if n.trigger.OnGroupMsg != nil && msg.RawMessage != "" {
				n.trigger.OnGroupMsg(msg)
			}
		case "private":
			if n.trigger.OnFriendMsg != nil && msg.SubType == "friend" && msg.RawMessage != "" {
				n.trigger.OnFriendMsg(msg)
			}
		}
	case "notice":
		noticeType, _ := callBack["notice_type"].(string)
		handleNotice(n.trigger, noticeType, data)
	}
}

func (n *luckyliliaHttpAdapter) SetTrigger(trigger adapter.TriggerWrapper) {
	n.trigger = trigger
}

func (n *luckyliliaHttpAdapter) SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (msgId message.QID, success bool) {
	data := httpGroupPushData{
		GroupId: message.QID(rawLil(groupId)),
		Message: stripLilSegments(chain.GetGroupMsg()),
	}

	var resp message.Response[message.Message]
	if !n.postAndCheck(n.baseUrl+"/send_group_msg", data, &resp) || !checkResponseStatus(&resp) {
		return "", false
	}
	message.NormalizeQQMessage(&resp.Data)
	relilMessage(&resp.Data)
	return resp.Data.MessageId, true
}

func (n *luckyliliaHttpAdapter) SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (msgId message.QID, success bool) {
	data := httpFriendPushData{
		UserId:  message.QID(rawLil(userId)),
		Message: stripLilSegments(chain.GetFriendMsg()),
	}

	var resp message.Response[message.Message]
	if !n.postAndCheck(n.baseUrl+"/send_private_msg", data, &resp) || !checkResponseStatus(&resp) {
		return "", false
	}
	message.NormalizeQQMessage(&resp.Data)
	relilMessage(&resp.Data)
	return resp.Data.MessageId, true
}

func (n *luckyliliaHttpAdapter) SendGroupAIVoiceMsg(groupId message.QID, character, msg string) (msgId message.QID, success bool) {
	data := map[string]any{
		"group_id":  rawLil(groupId),
		"character": character,
		"text":      msg,
	}
	var resp message.Response[message.Message]
	if !n.postAndCheck(n.baseUrl+"/send_group_ai_record", data, &resp) || !checkResponseStatus(&resp) {
		return "", false
	}
	message.NormalizeQQMessage(&resp.Data)
	relilMessage(&resp.Data)
	return resp.Data.MessageId, true
}

func (n *luckyliliaHttpAdapter) SendPokeMsg(userId message.QID, groupId *message.QID) (success bool) {
	data := map[string]message.QID{}
	data["user_id"] = message.QID(rawLil(userId))
	if groupId != nil {
		data["group_id"] = message.QID(rawLil(*groupId))
	}
	var resp message.Response[json.RawMessage]
	if !n.postAndCheck(n.baseUrl+"/send_poke", data, &resp) {
		return false
	}
	return checkResponseStatus(&resp)
}

func (n *luckyliliaHttpAdapter) SendGroupForwardMsg(groupId message.QID, chain msgchain.GroupForwardChain) (msgId message.QID, success bool) {
	data := message.GroupForwardMessage{
		GroupId:               message.QID(rawLil(groupId)),
		ForwardMessageSegment: stripLilForward(chain.GetForwardMsg()),
	}
	var resp message.Response[message.Message]
	if !n.postAndCheck(n.baseUrl+"/send_forward_msg", data, &resp) || !checkResponseStatus(&resp) {
		return "", false
	}
	message.NormalizeQQMessage(&resp.Data)
	relilMessage(&resp.Data)
	return resp.Data.MessageId, true
}

func (n *luckyliliaHttpAdapter) SendFriendForwardMsg(userId message.QID, chain msgchain.FriendForwardChain) (msgId message.QID, success bool) {
	data := message.FriendForwardMessage{
		UserId:                message.QID(rawLil(userId)),
		ForwardMessageSegment: stripLilForward(chain.GetForwardMsg()),
	}
	var resp message.Response[message.Message]
	if !n.postAndCheck(n.baseUrl+"/send_forward_msg", data, &resp) || !checkResponseStatus(&resp) {
		return "", false
	}
	message.NormalizeQQMessage(&resp.Data)
	relilMessage(&resp.Data)
	return resp.Data.MessageId, true
}

func (n *luckyliliaHttpAdapter) GetMsgDetail(msgId message.QID) (*message.Message, bool) {
	data := map[string]message.QID{"message_id": message.QID(rawLil(msgId))}
	var resp message.Response[message.Message]
	if !n.postAndCheck(n.baseUrl+"/get_msg", data, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	message.NormalizeQQMessage(&resp.Data)
	relilMessage(&resp.Data)
	return &resp.Data, true
}

func (n *luckyliliaHttpAdapter) GetForwardMsg(msgId message.QID) (msgs *[]message.Message, success bool) {
	data := map[string]message.QID{"message_id": message.QID(rawLil(msgId))}
	var resp message.Response[httpForwardData]
	if !n.postAndCheck(n.baseUrl+"/get_forward_msg", data, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	for i := range resp.Data.Messages {
		message.NormalizeQQMessage(&resp.Data.Messages[i])
	}
	relilMessages(resp.Data.Messages)
	return &resp.Data.Messages, true
}

func (n *luckyliliaHttpAdapter) GetGroupUserInfo(groupId, userId message.QID) (*message.GroupUserInfo, bool) {
	data := map[string]any{
		"group_id": rawLil(groupId),
		"user_id":  rawLil(userId),
		"no_cache": true,
	}
	resp := message.Response[message.GroupUserInfo]{}
	if !n.postAndCheck(n.baseUrl+"/get_group_member_info", data, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	resp.Data.GroupID = toLil(resp.Data.GroupID)
	resp.Data.UserID = toLil(resp.Data.UserID)
	return &resp.Data, true
}

func (n *luckyliliaHttpAdapter) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	data := map[string]any{
		"group_id": rawLil(groupId),
		"no_cache": noCache,
	}
	resp := message.Response[[]message.GroupUserInfo]{}
	if !n.postAndCheck(n.baseUrl+"/get_group_member_list", data, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	for i := range resp.Data {
		resp.Data[i].GroupID = toLil(resp.Data[i].GroupID)
		resp.Data[i].UserID = toLil(resp.Data[i].UserID)
	}
	return &resp.Data, true
}

func (n *luckyliliaHttpAdapter) GetNCrkey() ([]message.NCrkey, bool) {
	resp := message.Response[[]message.NCrkey]{}
	if !n.postAndCheck(n.baseUrl+"/nc_get_rkey", nil, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	return resp.Data, true
}

func (n *luckyliliaHttpAdapter) GetFriendList() (*[]message.Friend, bool) {
	resp := message.Response[[]message.Friend]{}
	if !n.postAndCheck(n.baseUrl+"/get_friend_list", nil, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	for i := range resp.Data {
		resp.Data[i].UserID = toLil(resp.Data[i].UserID)
	}
	return &resp.Data, true
}

func (n *luckyliliaHttpAdapter) GetGroupDetail(groupId message.QID) (*message.GroupInfo, bool) {
	data := map[string]message.QID{"group_id": message.QID(rawLil(groupId))}
	resp := message.Response[message.GroupInfo]{}
	if !n.postAndCheck(n.baseUrl+"/get_group_detail_info", data, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	resp.Data.GroupID = toLil(resp.Data.GroupID)
	return &resp.Data, true
}

func (n *luckyliliaHttpAdapter) SetMsgEmojiLike(msgId message.QID, emojiId int, like bool) bool {
	data := message.EmojiLike{
		MessageID: message.QID(rawLil(msgId)),
		EmojiId:   emojiId,
		Set:       like,
	}
	resp := message.Response[json.RawMessage]{}
	if !n.postAndCheck(n.baseUrl+"/set_msg_emoji_like", data, &resp) {
		return false
	}
	return checkResponseStatus(&resp)
}

func (n *luckyliliaHttpAdapter) SendGroupSign(groupId message.QID) bool {
	data := map[string]message.QID{"group_id": message.QID(rawLil(groupId))}
	resp := message.Response[json.RawMessage]{}
	return n.postAndCheck(n.baseUrl+"/send_group_sign", data, &resp) && checkResponseStatus(&resp)
}

func (n *luckyliliaHttpAdapter) GetGroupMsgHistory(groupId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	data := map[string]any{
		"group_id":    rawLil(groupId),
		"count":       count,
		"message_seq": message_seq,
	}
	var resp message.Response[httpForwardData]
	if !n.postAndCheck(n.baseUrl+"/get_group_msg_history", data, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	for i := range resp.Data.Messages {
		message.NormalizeQQMessage(&resp.Data.Messages[i])
	}
	relilMessages(resp.Data.Messages)
	return &resp.Data.Messages, true
}

func (n *luckyliliaHttpAdapter) GetFriendMsgHistory(userId message.QID, count int, message_seq int) (*[]message.Message, bool) {
	data := map[string]any{
		"user_id":     rawLil(userId),
		"count":       count,
		"message_seq": message_seq,
	}
	var resp message.Response[httpForwardData]
	if !n.postAndCheck(n.baseUrl+"/get_friend_msg_history", data, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	for i := range resp.Data.Messages {
		message.NormalizeQQMessage(&resp.Data.Messages[i])
	}
	relilMessages(resp.Data.Messages)
	return &resp.Data.Messages, true
}

func (n *luckyliliaHttpAdapter) GetAIChatacter() (*[]message.AIChatacter, bool) {
	resp := message.Response[message.AIChatacterResp]{}
	if !n.postAndCheck(n.baseUrl+"/get_ai_chatacter", nil, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	return &resp.Data.Characters, true
}

func (n *luckyliliaHttpAdapter) GetPrivateFileURL(userId message.QID, fileId string) (string, bool) {
	data := map[string]any{
		"user_id": rawLil(userId),
		"file_id": fileId,
	}
	type privateFileData struct {
		URL string `json:"url"`
	}
	var resp message.Response[privateFileData]
	if !n.postAndCheck(n.baseUrl+"/get_private_file_url", data, &resp) || !checkResponseStatus(&resp) {
		return "", false
	}
	return resp.Data.URL, true
}

func (n *luckyliliaHttpAdapter) GetGroupList() (*[]message.GroupInfo, bool) {
	resp := message.Response[[]message.GroupInfo]{}
	if !n.postAndCheck(n.baseUrl+"/get_group_list", nil, &resp) || !checkResponseStatus(&resp) {
		return nil, false
	}
	for i := range resp.Data {
		resp.Data[i].GroupID = toLil(resp.Data[i].GroupID)
	}
	return &resp.Data, true
}

type httpFriendPushData struct {
	UserId  message.QID           `json:"user_id"`
	Message []message.OB11Segment `json:"message"`
}

type httpGroupPushData struct {
	GroupId message.QID           `json:"group_id"`
	Message []message.OB11Segment `json:"message"`
}

// httpForwardData 对应 OneBot v11 get_forward_msg / *_msg_history 响应的 data 字段
type httpForwardData struct {
	Messages []message.Message `json:"messages"`
}
