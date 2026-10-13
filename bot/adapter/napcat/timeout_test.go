package napcat

import (
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
)

// TestIsUploadRequest 上传类请求识别：文件/视频段与 AI 语音用上传超时，纯文本/查询不走。
func TestIsUploadRequest(t *testing.T) {
	fileSeg := message.OB11Segment{Type: message.SegmentFile, Data: map[string]any{"file": "base64://x"}}
	textSeg := message.OB11Segment{Type: message.SegmentText, Data: map[string]any{"text": "hi"}}

	if !isUploadRequest("send_group_msg", map[string]any{"message": []message.OB11Segment{textSeg, fileSeg}}) {
		t.Fatal("含文件段的消息发送应判定为上传类请求")
	}
	if isUploadRequest("send_group_msg", map[string]any{"message": []message.OB11Segment{textSeg}}) {
		t.Fatal("纯文本消息发送不应判定为上传类请求")
	}
	if !isUploadRequest("send_group_ai_record", message.AiVoiceMsg{}) {
		t.Fatal("AI 语音请求应判定为上传类请求")
	}
	// HTTP 模式的请求体形态
	if !isUploadRequest("send_private_msg", httpFriendPushData{Message: []message.OB11Segment{fileSeg}}) {
		t.Fatal("HTTP 私聊发送含文件段应判定为上传类请求")
	}
	if isUploadRequest("get_group_list", struct{}{}) {
		t.Fatal("查询类请求不应判定为上传类请求")
	}
	// HTTP 适配器传入完整 URL，动作名提取需兼容
	if got := actionFromURL("http://localhost:6680/send_group_msg"); got != "send_group_msg" {
		t.Fatalf("actionFromURL 提取动作名失败，got %q", got)
	}
	if !isUploadRequest(actionFromURL("http://localhost:6680/send_group_msg"), httpGroupPushData{Message: []message.OB11Segment{fileSeg}}) {
		t.Fatal("HTTP 完整 URL 含文件段应判定为上传类请求")
	}
}

// TestResolveTimeoutSec 超时配置读取：未配置/非法值回退默认，正常值生效，超大值限幅防溢出。
func TestResolveTimeoutSec(t *testing.T) {
	v := viper.New()
	if got := resolveTimeoutSec(v, "bot.adapter.request_timeout_sec", defaultRequestTimeout); got != defaultRequestTimeout {
		t.Fatalf("未配置应回退默认值，got %v", got)
	}
	v.Set("bot.adapter.request_timeout_sec", 30)
	if got := resolveTimeoutSec(v, "bot.adapter.request_timeout_sec", defaultRequestTimeout); got != 30*time.Second {
		t.Fatalf("正常配置应生效，got %v", got)
	}
	v.Set("bot.adapter.request_timeout_sec", -5)
	if got := resolveTimeoutSec(v, "bot.adapter.request_timeout_sec", defaultRequestTimeout); got != defaultRequestTimeout {
		t.Fatalf("非法配置应回退默认值，got %v", got)
	}
	v.Set("bot.adapter.request_timeout_sec", 1<<40)
	if got := resolveTimeoutSec(v, "bot.adapter.request_timeout_sec", defaultRequestTimeout); got != maxTimeoutSec*time.Second {
		t.Fatalf("超大配置应限幅，got %v", got)
	}
}
