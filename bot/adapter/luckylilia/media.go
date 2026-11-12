package luckylilia

import (
	"strings"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// mediaRefreshAction 构造媒体地址续期请求。
// LLBot 与 NapCat 同为 OneBot v11 QQ 协议端，媒体地址（rkey）过期后同样
// 按媒体类型调用对应接口重新签发。
func mediaRefreshAction(kind, fileId, groupId, userId string) (string, map[string]any) {
	fileId = strings.TrimSpace(fileId)
	if fileId == "" {
		return "", nil
	}
	switch kind {
	case message.SegmentImage:
		return "get_image", map[string]any{"file_id": fileId}
	case message.SegmentRecord:
		return "get_record", map[string]any{"file_id": fileId, "out_format": "mp3"}
	case message.SegmentVideo:
		return "get_file", map[string]any{"file_id": fileId}
	case message.SegmentFile:
		if groupId != "" {
			return "get_group_file_url", map[string]any{
				"group_id": rawLil(message.QID(groupId)),
				"file_id":  fileId,
			}
		}
		if userId != "" {
			return "get_private_file_url", map[string]any{
				"user_id": rawLil(message.QID(userId)),
				"file_id": fileId,
			}
		}
	}
	return "", nil
}

// mediaHTTPURL 只接受 http(s) 地址，协议端回本地路径 / base64 时按失败处理。
func mediaHTTPURL(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u, true
	}
	return "", false
}

// RefreshMediaURL 实现 adapter.MediaRefresherExt：媒体地址过期后向 LLBot
// 重新申请（图片 get_image、语音 get_record、视频 get_file、文件按群/私聊
// 分别走 get_group_file_url / get_private_file_url）。
func (n *luckyliliaWebSocketAdapter) RefreshMediaURL(kind, fileId, groupId, userId string) (string, bool) {
	action, params := mediaRefreshAction(kind, fileId, groupId, userId)
	if action == "" {
		return "", false
	}
	type mediaData struct {
		URL string `json:"url"`
	}
	res, ok := request[mediaData](n, action, params, "media")
	if !ok || res == nil {
		return "", false
	}
	return mediaHTTPURL(res.URL)
}

// RefreshMediaURL 实现 adapter.MediaRefresherExt（HTTP 模式，语义同 WebSocket）。
func (n *luckyliliaHttpAdapter) RefreshMediaURL(kind, fileId, groupId, userId string) (string, bool) {
	action, params := mediaRefreshAction(kind, fileId, groupId, userId)
	if action == "" {
		return "", false
	}
	type mediaData struct {
		URL string `json:"url"`
	}
	var resp message.Response[mediaData]
	if !n.postAndCheck(n.baseUrl+"/"+action, params, &resp) || !checkResponseStatus(&resp) {
		return "", false
	}
	return mediaHTTPURL(resp.Data.URL)
}
