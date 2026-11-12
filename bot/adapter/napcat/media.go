package napcat

import (
	"strings"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// mediaRefreshAction 构造媒体地址续期请求。
// QQ 的图片 / 文件 / 语音地址带 rkey 等时效性鉴权参数（约 2 小时过期），
// 过期后按媒体类型调用对应接口重新签发（见 NapCat 文档「处理文件」）。
func mediaRefreshAction(kind, fileId, groupId, userId string) (string, map[string]any) {
	fileId = strings.TrimSpace(fileId)
	if fileId == "" {
		return "", nil
	}
	switch kind {
	case message.SegmentImage:
		return "get_image", map[string]any{"file_id": fileId}
	case message.SegmentRecord:
		// 音频交给协议端转成通用格式；返回地址可能为空（只给了本地路径），
		// 由调用方按失败处理，面板不会拿到不可访问的 file:// 地址。
		return "get_record", map[string]any{"file_id": fileId, "out_format": "mp3"}
	case message.SegmentVideo:
		return "get_file", map[string]any{"file_id": fileId}
	case message.SegmentFile:
		if groupId != "" {
			return "get_group_file_url", map[string]any{
				"group_id": rawQQ(message.QID(groupId)),
				"file_id":  fileId,
			}
		}
		if userId != "" {
			return "get_private_file_url", map[string]any{
				"user_id": rawQQ(message.QID(userId)),
				"file_id": fileId,
			}
		}
	}
	return "", nil
}

// mediaHTTPURL 只接受 http(s) 地址：协议端也可能回本地路径 / base64，
// 这些面板代理无法作为远程媒体访问，按续期失败处理。
func mediaHTTPURL(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u, true
	}
	return "", false
}

// RefreshMediaURL 实现 adapter.MediaRefresherExt：媒体地址过期后向 NapCat
// 重新申请（图片 get_image、语音 get_record、视频 get_file、文件按群/私聊
// 分别走 get_group_file_url / get_private_file_url）。
func (n *napcatWebSocketAdapter) RefreshMediaURL(kind, fileId, groupId, userId string) (string, bool) {
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
func (n *napcatHttpAdapter) RefreshMediaURL(kind, fileId, groupId, userId string) (string, bool) {
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
