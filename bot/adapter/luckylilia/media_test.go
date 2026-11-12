package luckylilia

import (
	"testing"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestMediaRefreshAction 媒体地址续期按类型选择接口，并把 lil: 前缀还原为
// 协议端需要的原始数字 ID。
func TestMediaRefreshAction(t *testing.T) {
	action, params := mediaRefreshAction(message.SegmentImage, "fid-1", "lil:123", "lil:456")
	if action != "get_image" || params["file_id"] != "fid-1" {
		t.Fatalf("图片续期错误: action=%q params=%+v", action, params)
	}

	action, params = mediaRefreshAction(message.SegmentFile, "fid-2", "lil:123", "")
	if action != "get_group_file_url" || params["group_id"] != "123" {
		t.Fatalf("群文件续期错误: action=%q params=%+v", action, params)
	}

	action, params = mediaRefreshAction(message.SegmentFile, "fid-3", "", "lil:456")
	if action != "get_private_file_url" || params["user_id"] != "456" {
		t.Fatalf("私聊文件续期错误: action=%q params=%+v", action, params)
	}

	if action, _ := mediaRefreshAction(message.SegmentImage, "", "lil:1", ""); action != "" {
		t.Fatalf("缺少文件 ID 时不应产生请求, got %q", action)
	}
}

// TestMediaHTTPURL 续期只接受 http(s) 地址。
func TestMediaHTTPURL(t *testing.T) {
	if _, ok := mediaHTTPURL("https://cdn.example.com/a.png"); !ok {
		t.Fatal("http(s) 地址应被接受")
	}
	for _, s := range []string{"", "file:///tmp/a.png", "base64://AAAA"} {
		if _, ok := mediaHTTPURL(s); ok {
			t.Errorf("%q 不应被接受", s)
		}
	}
}
