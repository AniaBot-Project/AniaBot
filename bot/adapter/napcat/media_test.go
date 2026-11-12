package napcat

import (
	"testing"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestMediaRefreshAction 媒体地址续期按类型选择 NapCat 接口，并把 qq: 前缀
// 还原为协议端需要的原始数字 ID。
func TestMediaRefreshAction(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		fileID  string
		groupID string
		userID  string
		action  string
		check   func(t *testing.T, params map[string]any)
	}{
		{
			name: "图片", kind: message.SegmentImage, fileID: "fid-1", groupID: "qq:123", userID: "qq:456",
			action: "get_image",
			check: func(t *testing.T, p map[string]any) {
				t.Helper()
				if p["file_id"] != "fid-1" {
					t.Fatalf("file_id = %v", p["file_id"])
				}
			},
		},
		{
			name: "语音", kind: message.SegmentRecord, fileID: "fid-2",
			action: "get_record",
			check: func(t *testing.T, p map[string]any) {
				t.Helper()
				if p["file_id"] != "fid-2" || p["out_format"] != "mp3" {
					t.Fatalf("params = %+v", p)
				}
			},
		},
		{
			name: "视频", kind: message.SegmentVideo, fileID: "fid-3",
			action: "get_file",
			check: func(t *testing.T, p map[string]any) {
				t.Helper()
				if p["file_id"] != "fid-3" {
					t.Fatalf("params = %+v", p)
				}
			},
		},
		{
			name: "群文件", kind: message.SegmentFile, fileID: "fid-4", groupID: "qq:123",
			action: "get_group_file_url",
			check: func(t *testing.T, p map[string]any) {
				t.Helper()
				if p["group_id"] != "123" || p["file_id"] != "fid-4" {
					t.Fatalf("params = %+v", p)
				}
			},
		},
		{
			name: "私聊文件", kind: message.SegmentFile, fileID: "fid-5", userID: "qq:456",
			action: "get_private_file_url",
			check: func(t *testing.T, p map[string]any) {
				t.Helper()
				if p["user_id"] != "456" || p["file_id"] != "fid-5" {
					t.Fatalf("params = %+v", p)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			action, params := mediaRefreshAction(c.kind, c.fileID, c.groupID, c.userID)
			if action != c.action {
				t.Fatalf("action = %q, want %q", action, c.action)
			}
			c.check(t, params)
		})
	}

	// 没有文件 ID、或类型不支持时不发请求（续期失败由调用方回退）
	for _, c := range []struct{ kind, fileID string }{
		{message.SegmentImage, ""},
		{message.SegmentImage, "   "},
		{message.SegmentFile, ""},
		{message.SegmentText, "fid"},
	} {
		if action, _ := mediaRefreshAction(c.kind, c.fileID, "qq:1", "qq:2"); action != "" {
			t.Errorf("%s/%q 不应产生请求, got %q", c.kind, c.fileID, action)
		}
	}
}

// TestMediaHTTPURL 续期只接受 http(s) 地址，本地路径 / 空值按失败处理。
func TestMediaHTTPURL(t *testing.T) {
	for _, s := range []string{"https://cdn.example.com/a.png?rkey=1", " http://cdn.example.com/a.png "} {
		if _, ok := mediaHTTPURL(s); !ok {
			t.Errorf("%q 应被接受", s)
		}
	}
	for _, s := range []string{"", "file:///tmp/a.png", "base64://AAAA", "/local/a.png"} {
		if u, ok := mediaHTTPURL(s); ok {
			t.Errorf("%q 不应被接受, got %q", s, u)
		}
	}
}
