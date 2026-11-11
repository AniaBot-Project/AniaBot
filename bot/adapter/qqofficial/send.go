package qqofficial

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// 被动回复限制（官方文档「消息收发概述」）。
const (
	groupReplyTTL   = 5 * time.Minute  // 群聊被动回复有效期
	groupReplyLimit = 5                // 群聊每条消息可回复次数
	c2cReplyTTL     = 60 * time.Minute // 单聊被动回复有效期
	c2cReplyLimit   = 4                // 单聊每条消息可回复次数
	// textRunesLimit 单条文本消息长度上限（官方未明示，超限报 40054007；
	// 取保守值按 rune 分包，保留 rune 完整性）
	textRunesLimit = 1800
)

// replyToken 会话被动回复凭证：最近一次入站事件的 msg_id 与已用回复序号。
// 相同 msg_id+msg_seq 重复发送会失败，多次回复需递增 msg_seq。
type replyToken struct {
	mu    sync.Mutex
	msgID string
	seq   int
	at    time.Time
}

// storeReplyToken 入站事件携带的 msg_id 记录为该会话的被动回复凭证。
func (a *qqOfficialAdapter) storeReplyToken(conversation, msgID string) {
	if conversation == "" || msgID == "" {
		return
	}
	a.replyTokens.Store(conversation, &replyToken{msgID: msgID, at: time.Now()})
}

// nextReplySeq 取该会话下一次被动回复的 (msg_id, msg_seq)；
// 凭证过期（群 5 分钟/单聊 60 分钟）或次数耗尽（群 5 次/单聊 4 次）返回 ok=false，
// 调用方降级为主动消息（无 msg_id，受主动频控与配额限制）。
func (a *qqOfficialAdapter) nextReplySeq(conversation string, isGroup bool) (string, int, bool) {
	v, ok := a.replyTokens.Load(conversation)
	if !ok {
		return "", 0, false
	}
	tok := v.(*replyToken)
	tok.mu.Lock()
	defer tok.mu.Unlock()
	ttl, limit := c2cReplyTTL, c2cReplyLimit
	if isGroup {
		ttl, limit = groupReplyTTL, groupReplyLimit
	}
	if tok.msgID == "" || time.Since(tok.at) > ttl || tok.seq >= limit {
		return "", 0, false
	}
	tok.seq++
	return tok.msgID, tok.seq, true
}

// peekReplyMsgID 窥探该会话当前被动回复凭证的 msg_id（不消费序号）；
// 凭证过期或次数耗尽返回空串。用于隐式 message_reference（见 sendChain）。
func (a *qqOfficialAdapter) peekReplyMsgID(conversation string, isGroup bool) string {
	v, ok := a.replyTokens.Load(conversation)
	if !ok {
		return ""
	}
	tok := v.(*replyToken)
	tok.mu.Lock()
	defer tok.mu.Unlock()
	ttl, limit := c2cReplyTTL, c2cReplyLimit
	if isGroup {
		ttl, limit = groupReplyTTL, groupReplyLimit
	}
	if tok.msgID == "" || time.Since(tok.at) > ttl || tok.seq >= limit {
		return ""
	}
	return tok.msgID
}

// SendGroupMsg 发送群消息（POST /v2/groups/{group_openid}/messages）。
func (a *qqOfficialAdapter) SendGroupMsg(groupId message.QID, chain msgchain.GroupChain) (message.QID, bool) {
	openid, ok := parseOpenID(groupId)
	if !ok || a.client == nil {
		return "", false
	}
	return a.sendChain(context.Background(), openid, true, chain.GetGroupMsg())
}

// SendFriendMsg 发送单聊消息（POST /v2/users/{user_openid}/messages）。
func (a *qqOfficialAdapter) SendFriendMsg(userId message.QID, chain msgchain.FriendChain) (message.QID, bool) {
	openid, ok := parseOpenID(userId)
	if !ok || a.client == nil {
		return "", false
	}
	return a.sendChain(context.Background(), openid, false, chain.GetFriendMsg())
}

// parseOpenID 解析 "qo:<openid>"；非 qo: 前缀（如 QQ 的 qq: 数字 ID）返回 ok=false，
// 避免把其他平台/默认平台的 ID 误当作本平台目标。
func parseOpenID(q message.QID) (string, bool) {
	s := q.String()
	if !strings.HasPrefix(s, idPrefix) {
		return "", false
	}
	raw := strings.TrimPrefix(s, idPrefix)
	if raw == "" {
		return "", false
	}
	return raw, true
}

// sendChain 把通用消息段翻译为 QQ 官方消息序列并发送。
// 文本按段顺序累积，媒体穿插发送；首条消息携带 message_reference（引用气泡）。
// 引用目标：显式 reply 段优先；否则在开启 bot.qqofficial.auto_quote 时引用当前
// 被动回复凭证的消息（触发消息）。自动引用默认关闭——官方群消息不能 @ 成员，
// 仅当需要「回复某条」的视觉语义时才建议开启。
// 返回最后成功消息的框架内 ID（qo:<message_id>）。
func (a *qqOfficialAdapter) sendChain(ctx context.Context, openid string, isGroup bool, segs []message.OB11Segment) (message.QID, bool) {
	if a.client == nil {
		return "", false
	}
	// 提取回复目标（首条 reply 段，非 qo: 前缀忽略），其余段作为正文
	reference := ""
	body := make([]message.OB11Segment, 0, len(segs))
	for _, s := range segs {
		if s.Type == message.SegmentReply && reference == "" {
			if id, ok := s.Data["id"].(string); ok {
				if raw := strings.TrimPrefix(id, idPrefix); raw != id && raw != "" {
					reference = raw
					continue
				}
			}
		}
		body = append(body, s)
	}
	if len(body) == 0 {
		return "", false
	}
	// 隐式引用：开启 auto_quote 且无显式 reply 段时引用被动回复凭证的消息
	// （触发消息）；默认关闭，直接发送不带引用气泡。
	if reference == "" && a.cfg.autoQuote {
		reference = a.peekReplyMsgID(openid, isGroup)
	}

	var text strings.Builder
	var lastID string
	sentAny := false
	// 发送累积的文本（首条携带引用，之后清空）
	flushText := func() {
		if text.Len() == 0 {
			return
		}
		t := text.String()
		text.Reset()
		if id, ok := a.sendText(ctx, openid, isGroup, t, reference); ok {
			lastID = id
			sentAny = true
			reference = ""
		}
	}

	for _, s := range body {
		switch s.Type {
		case message.SegmentText:
			if t, ok := s.Data["text"].(string); ok {
				text.WriteString(t)
			}
		case message.SegmentMention:
			// 官方群/单聊消息不支持 @ 成员（无对应 API），回复语义已由
			// message_reference 表达，at 段静默退化（避免输出无意义文本）
			a.logger.Debug("忽略 QQ 官方不支持的 at 段（消息不支持 @ 成员）")
		case message.SegmentImage, message.SegmentFile, message.SegmentRecord, message.SegmentVideo:
			flushText()
			if id, ok := a.sendMedia(ctx, openid, isGroup, s, reference); ok {
				lastID = id
				sentAny = true
				reference = ""
			}
		default:
			// 不支持的段（face/json/music/forward）：有 text 键时退化为文本
			if t, ok := s.Data["text"].(string); ok {
				text.WriteString(t)
			} else {
				a.logger.Debug("忽略 QQ 官方不支持的通用消息段", "segment", s.Type)
			}
		}
	}
	flushText()
	if !sentAny {
		return "", false
	}
	a.cacheSent(openid, isGroup, lastID, body)
	return message.QID(idPrefix + lastID), true
}

// msgIDExpiredCodes 被动回复凭证失效类错误码：出现即降级为主动消息重试一次
// （去掉 msg_id/msg_seq；主动消息受频控与配额限制，可能因未认证被拒，如实上报）。
var msgIDExpiredCodes = map[int]bool{
	304026:   true, // 非 At 当前用户的消息不允许回复
	304027:   true, // MSG_EXPIRE 回复的消息过期
	304103:   true, // 消息ID已过期，不能回复
	40034005: true, // 回复消息msg_id已过期
	40034024: true, // 请求参数msg_id无效或越权
	40034128: true, // 被动回复时间或次数超限
}

// markdownRejectCodes Markdown 消息被拒绝类错误码：出现即降级纯文本重试。
var markdownRejectCodes = map[int]bool{
	22006:    true, // 消息类型与内容不匹配
	304036:   true, // 无Markdown模板权限
	40034124: true, // markdown消息参数错误
	40034127: true, // 无markdown模板权限
	50055:    true, // 无效的 markdown content
	50056:    true, // 不允许发送 markdown content
	50057:    true, // markdown 参数只支持原生语法或者模版二选一
}

// postMessage 发送一条消息并统一处理两类降级：
//  1. 被动回复凭证失效（msg_id 过期/次数耗尽）→ 去掉 msg_id/msg_seq 主动消息重试一次；
//  2. Markdown 被拒 → 降级纯文本重试一次（content 置为 markdown 原文）。
//
// 返回平台消息 ID。
func (a *qqOfficialAdapter) postMessage(ctx context.Context, openid string, isGroup bool, req sendMessageRequest) (string, error) {
	var res sendMessageResponse
	path := scopePath(isGroup) + "/" + openid + "/messages"
	err := a.client.post(ctx, path, req, &res)
	if err == nil {
		return res.ID, nil
	}
	code, ok := errCode(err)
	if !ok {
		return "", err
	}
	// 降级 1：msg_id 失效 → 主动消息重试
	if req.MsgID != "" && msgIDExpiredCodes[code] {
		a.logger.Debug("被动回复凭证失效，降级主动消息重试", "code", code, "openid", openid)
		req.MsgID, req.MsgSeq = "", 0
		err = a.client.post(ctx, path, req, &res)
		if err == nil {
			return res.ID, nil
		}
		code, ok = errCode(err)
		if !ok {
			return "", err
		}
	}
	// 降级 2：Markdown 被拒 → 纯文本重试
	if req.MsgType == 2 && req.Markdown != nil && markdownRejectCodes[code] {
		a.logger.Debug("Markdown 消息被拒，降级纯文本重试", "code", code, "openid", openid)
		req.MsgType = 0
		req.Content = req.Markdown.Content
		req.Markdown = nil
		if err = a.client.post(ctx, path, req, &res); err == nil {
			return res.ID, nil
		}
	}
	return "", err
}

// withReply 为请求装配被动回复凭证（msg_id + 递增 msg_seq）。
func (a *qqOfficialAdapter) withReply(req *sendMessageRequest, openid string, isGroup bool) {
	if msgID, seq, ok := a.nextReplySeq(openid, isGroup); ok {
		req.MsgID = msgID
		req.MsgSeq = seq
	}
}

// sendText 发送文本消息：默认 msg_type=0 纯文本；配置 bot.qqofficial.markdown
// 后以 msg_type=2 Markdown 发送（AI 输出本就是 markdown，标题/加粗/列表富文本渲染，
// 被拒自动降级纯文本）。超过长度上限按 rune 分包。reference 非空时首包携带引用回复。
func (a *qqOfficialAdapter) sendText(ctx context.Context, openid string, isGroup bool, text string, reference string) (string, bool) {
	parts := splitText(text, textRunesLimit)
	if len(parts) == 0 {
		return "", false
	}
	last := ""
	for i, part := range parts {
		req := sendMessageRequest{MsgType: 0, Content: part}
		if a.cfg.markdown {
			req.MsgType = 2
			req.Markdown = &markdownPayload{Content: part}
			req.Content = ""
		}
		if reference != "" && i == 0 {
			req.MessageReference = &messageReference{MessageID: reference}
		}
		a.withReply(&req, openid, isGroup)
		id, err := a.postMessage(ctx, openid, isGroup, req)
		if err != nil {
			a.logger.Warn("QQ 官方发送文本消息失败", "openid", openid, "isGroup", isGroup, "error", err)
			return last, last != "" // 部分成功也上报最后成功消息
		}
		last = id
	}
	return last, true
}

// fileTypeOf 通用段 → 官方媒体类型（1=图片 2=视频 3=语音 4=文件）。
func fileTypeOf(segType string) int {
	switch segType {
	case message.SegmentImage:
		return 1
	case message.SegmentVideo:
		return 2
	case message.SegmentRecord:
		return 3
	default:
		return 4
	}
}

// sendMedia 发送一个媒体段：先上传换 file_info（URL 直传或分片上传），
// 再以 msg_type=7 富媒体消息发送（reference 非空时携带引用回复）。
// 文本说明不支持（官方富媒体消息无 caption 概念，相邻文本已由 sendChain 单独成条发送）。
func (a *qqOfficialAdapter) sendMedia(ctx context.Context, openid string, isGroup bool, s message.OB11Segment, reference string) (string, bool) {
	// file 段指向图片文件时（file 工具等来源）按图片类型（file_type=1）上传，
	// 聊天内联展示，而不是文件类型（file_type=4）
	if s.Type == message.SegmentFile {
		if imgData, ok := message.FileSegmentAsImage(s); ok {
			s = message.OB11Segment{Type: message.SegmentImage, Data: imgData}
		}
	}
	src := ""
	if u, ok := s.Data["url"].(string); ok && u != "" {
		src = u
	} else if f, ok := s.Data["file"].(string); ok {
		src = f
	}
	if src == "" {
		return "", false
	}
	fileName, _ := s.Data["name"].(string)
	fileInfo, err := a.uploadMedia(ctx, openid, isGroup, fileTypeOf(s.Type), src, fileName)
	if err != nil {
		a.logger.Warn("QQ 官方媒体上传失败", "segment", s.Type, "openid", openid, "error", err)
		return "", false
	}
	req := sendMessageRequest{MsgType: 7, Media: &mediaPayload{FileInfo: fileInfo}}
	if reference != "" {
		req.MessageReference = &messageReference{MessageID: reference}
	}
	a.withReply(&req, openid, isGroup)
	id, err := a.postMessage(ctx, openid, isGroup, req)
	if err != nil {
		a.logger.Warn("QQ 官方发送媒体消息失败", "segment", s.Type, "openid", openid, "error", err)
		return "", false
	}
	return id, true
}

// scopePath 群聊/单聊 API 路径前缀。
func scopePath(isGroup bool) string {
	if isGroup {
		return "/v2/groups"
	}
	return "/v2/users"
}

// splitText 按 rune 上限分包（保留 rune 完整性）。
func splitText(text string, limit int) []string {
	if text == "" {
		return nil
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var parts []string
	runes := []rune(text)
	for start := 0; start < len(runes); start += limit {
		end := min(start+limit, len(runes))
		parts = append(parts, string(runes[start:end]))
	}
	return parts
}

// cacheSent 记录出站消息到内存缓存（GetMsgDetail/历史兜底）。
// 出站消息先剔除内联 base64/data 负载再入缓存，避免大图常驻内存。
func (a *qqOfficialAdapter) cacheSent(openid string, isGroup bool, msgID string, segs []message.OB11Segment) {
	msgType := "group"
	if !isGroup {
		msgType = "private"
	}
	msg := message.Message{
		Time:        uint(time.Now().Unix()),
		PostType:    "message",
		MessageType: msgType,
		MessageId:   message.QID(idPrefix + msgID),
		Message:     message.StripInlinePayloadSegments(segs),
		RawMessage:  segmentsPlainText(segs),
		SelfId:      a.selfQID(),
		Platform:    Platform,
	}
	if isGroup {
		msg.GroupId = message.QID(idPrefix + openid)
	} else {
		msg.UserId = message.QID(idPrefix + openid)
	}
	a.msgCache.Push(openid, msg)
}
