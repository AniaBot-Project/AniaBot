package weixin

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestImageDataURIUsesSniffedMIME 入站图片的 data URI 必须按字节魔数标注 MIME。
//
// 旧实现无条件写成 image/png：手机端微信图片绝大多数是 JPEG，标签与内容不符时
// 多模态模型/OCR 兜底会以 "unsupported image" 整体失败（飞书适配器已有同因教训
// 并做了字节嗅探）。
func TestImageDataURIUsesSniffedMIME(t *testing.T) {
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("jfif-body")...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("png-body")...)
	gif := append([]byte("GIF89a"), []byte("gif-body")...)
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("webp-body")...)
	unknown := []byte("not-an-image-at-all")

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", jpeg, "image/jpeg"},
		{"png", png, "image/png"},
		{"gif", gif, "image/gif"},
		{"webp", webp, "image/webp"},
		{"unknown 回退 png", unknown, "image/png"},
	}
	for _, c := range cases {
		uri := imageDataURI(c.data)
		prefix := "data:" + c.want + ";base64,"
		if !strings.HasPrefix(uri, prefix) {
			t.Errorf("%s: data URI 前缀应为 %q, got %q", c.name, prefix, uri[:min(len(uri), 40)])
		}
		// 负载必须与原字节一致
		payload := strings.TrimPrefix(uri, prefix)
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			t.Errorf("%s: base64 负载解码失败: %v", c.name, err)
			continue
		}
		if string(decoded) != string(c.data) {
			t.Errorf("%s: 负载与原始字节不一致", c.name)
		}
	}
}

// TestImageDataURIJPEGNotLabeledPNG 回归：JPEG 不得被标成 PNG（远程可解码性依赖此）。
func TestImageDataURIJPEGNotLabeledPNG(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43}
	if uri := imageDataURI(jpeg); strings.HasPrefix(uri, "data:image/png") {
		t.Fatalf("JPEG 被错误标注为 PNG: %q", uri[:min(len(uri), 40)])
	}
}
