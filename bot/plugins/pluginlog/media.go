package pluginlog

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// maxMediaPerMessage 单条消息最多保留的媒体数量，避免异常消息把日志撑爆。
const maxMediaPerMessage = 12

// extractMedia 从消息段中提取图片 / 文件 / 语音 / 视频，供面板在聊天气泡内联
// 展示图片、提供文件下载。只保留 http(s) 地址：data:/base64: 内联负载体积
// 可达 MB 级，塞进每条日志会让缓存存储迅速膨胀（详见 msglog.Media 注释）。
func extractMedia(segs []message.OB11Segment) []msglog.Media {
	var out []msglog.Media
	for _, seg := range segs {
		if len(out) >= maxMediaPerMessage {
			break
		}
		switch seg.Type {
		case message.SegmentImage:
			if u := firstHTTPURL(seg.Data, "url", "file"); u != "" {
				out = append(out, msglog.Media{Kind: msglog.MediaImage, URL: u})
			}
		case message.SegmentFile:
			// 部分平台把图片作为 file 段发来（进站时未归一化为 image），
			// 能判定为图片且拿到 http(s) 地址时按图片内联展示。
			if img, ok := message.FileSegmentAsImage(seg); ok {
				if u := firstHTTPURL(img, "url", "file"); u != "" {
					out = append(out, msglog.Media{Kind: msglog.MediaImage, URL: u})
					continue
				}
			}
			if m, ok := fileMedia(seg); ok {
				out = append(out, m)
			}
		case message.SegmentRecord:
			if u := firstHTTPURL(seg.Data, "url", "file"); u != "" {
				out = append(out, msglog.Media{Kind: msglog.MediaRecord, URL: u, Name: "语音"})
			}
		case message.SegmentVideo:
			if u := firstHTTPURL(seg.Data, "url", "file"); u != "" {
				out = append(out, msglog.Media{Kind: msglog.MediaVideo, URL: u, Name: "视频"})
			}
		}
	}
	return out
}

// fileMedia 解析 file 段：文件名优先 name，其次 file 源；大小读 file_size（
// OneBot 协议字段，数值或字符串均见得到）。
func fileMedia(seg message.OB11Segment) (msglog.Media, bool) {
	var f message.FileMessage
	if !message.ParseFile(seg, &f) {
		return msglog.Media{}, false
	}
	name := f.Name
	if name == "" {
		name = f.File
	}
	if name != "" {
		name = strings.SplitN(name, "?", 2)[0]
		name = filepath.Base(strings.TrimRight(name, "/"))
	}
	if name == "" || name == "." {
		name = "文件"
	}
	m := msglog.Media{
		Kind:   msglog.MediaFile,
		Name:   name,
		Size:   segmentSize(seg),
		FileId: f.FileId,
	}
	if isHTTPURL(f.URL) {
		m.URL = f.URL
	} else if isHTTPURL(f.File) {
		m.URL = f.File
	}
	return m, true
}

// segmentSize 读取段数据里的文件大小（OneBot 的 file_size，部分适配器写 size）。
func segmentSize(seg message.OB11Segment) int64 {
	for _, key := range []string{"file_size", "size"} {
		switch v := seg.Data[key].(type) {
		case float64:
			if v > 0 {
				return int64(v)
			}
		case int:
			if v > 0 {
				return int64(v)
			}
		case int64:
			if v > 0 {
				return v
			}
		case json.Number:
			if n, err := v.Int64(); err == nil && n > 0 {
				return n
			}
		case string:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// firstHTTPURL 返回段数据中第一个 http(s) 地址（按给定键顺序）。
func firstHTTPURL(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := data[key].(string); ok && isHTTPURL(s) {
			return s
		}
	}
	return ""
}

// isHTTPURL 判断字符串是否为可直接交给浏览器 / 代理访问的 http(s) 地址。
// 过长的地址（异常数据）直接拒绝，避免把垃圾写进日志。
func isHTTPURL(s string) bool {
	if s == "" || len(s) > 4096 {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
