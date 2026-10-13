package luckylilia

import (
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
)

// 等待 LLBot 回包的默认超时：上传类调用（发送文件/视频/语音等）需先把内容上传到
// 平台服务器再回包，耗时远高于普通接口，分开配置（均可通过面板调整）。
const (
	defaultRequestTimeout = 10 * time.Second
	defaultUploadTimeout  = 120 * time.Second

	// maxTimeoutSec 超时配置上限（1 天）：用户误填超大值时防 time.Duration 溢出
	maxTimeoutSec = 24 * 60 * 60
)

// resolveTimeoutSec 读取超时配置（秒）：未配置或非法（<=0）时回退默认值，
// 超大值限幅到 maxTimeoutSec（直接乘 time.Second 会 int64 溢出为负值）。
func resolveTimeoutSec(v *viper.Viper, key string, def time.Duration) time.Duration {
	sec := v.GetInt(key)
	if sec <= 0 {
		return def
	}
	if sec > maxTimeoutSec {
		sec = maxTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// actionFromURL 从完整请求 URL 中提取 OneBot 动作名（最后一段路径），
// 使 HTTP 适配器与 WS 适配器共用同一套上传判定（baseUrl 在 Serve 前为空，
// 不能依赖它裁剪）。
func actionFromURL(url string) string {
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		return url[i+1:]
	}
	return url
}

// isUploadRequest 判断请求是否包含需上传媒体的内容（文件/视频/语音段，或 AI 语音
// 生成）：这类调用协议端需先上传内容再回包，应使用上传超时等待，避免慢上传被误判
// 为发送失败（超时仅表示结果未知，内容可能稍后才送达）。
func isUploadRequest(action string, params any) bool {
	switch action {
	case "send_group_ai_record":
		// AI 语音由协议端生成并上传，同属慢调用
		return true
	}
	switch p := params.(type) {
	case httpGroupPushData:
		return message.SegmentsContainMedia(p.Message)
	case httpFriendPushData:
		return message.SegmentsContainMedia(p.Message)
	}
	return message.IncludesMediaUpload(params)
}
