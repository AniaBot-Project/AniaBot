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

// maxForwardMediaPerMessage 合并转发记录内部图片单独计数：转发内容再多也不
// 挤占顶层图片 / 文件的额度（两批媒体各自有序，面板分别消费）。
const maxForwardMediaPerMessage = 12

// extractMedia 从消息段中提取图片 / 文件 / 语音 / 视频，供面板在聊天气泡内联
// 展示图片、提供文件下载。只保留 http(s) 地址：data:/base64: 内联负载体积
// 可达 MB 级，塞进每条日志会让缓存存储迅速膨胀（详见 msglog.Media 注释）。
//
// 合并转发记录内部（含多层嵌套）的图片另取一批追加在后面并标记 InForward：
// 面板按出现顺序把它们填回对应转发节点里的 [图片] 占位，让聊天记录里的图片
// 也能直接看到。内部的文件 / 语音 / 视频不取，节点里保留文字占位。
func extractMedia(segs []message.OB11Segment) []msglog.Media {
	var out []msglog.Media
	for _, seg := range segs {
		if len(out) >= maxMediaPerMessage {
			break
		}
		switch seg.Type {
		case message.SegmentImage:
			if m, ok := imageMediaOf(seg); ok {
				out = append(out, m)
			}
		case message.SegmentFile:
			// 部分平台把图片作为 file 段发来（进站时未归一化为 image），
			// 能判定为图片且拿到 http(s) 地址时按图片内联展示。
			if m, ok := imageMediaOf(seg); ok {
				out = append(out, m)
				continue
			}
			if m, ok := fileMedia(seg); ok {
				out = append(out, m)
			}
		case message.SegmentRecord:
			if u := firstHTTPURL(seg.Data, "url", "file"); u != "" {
				out = append(out, msglog.Media{Kind: msglog.MediaRecord, URL: u, Name: "语音", FileId: segmentFileID(seg)})
			}
		case message.SegmentVideo:
			if u := firstHTTPURL(seg.Data, "url", "file"); u != "" {
				out = append(out, msglog.Media{Kind: msglog.MediaVideo, URL: u, Name: "视频", FileId: segmentFileID(seg)})
			}
		}
	}
	left := maxForwardMediaPerMessage
	appendForwardImages(&out, segs, &left)
	return out
}

// appendForwardImages 从顶层消息段里找出合并转发段，按文档顺序收集其内部
// （含多层嵌套）的图片，与 FriendlyText 写出 [图片] 占位的顺序一致，面板
// 据此逐个匹配。只收图片（含以 file 段发来的图片），其他类型在转发节点里
// 保留文字占位；整条日志最多 left 张，每张都标记 InForward。
func appendForwardImages(out *[]msglog.Media, segs []message.OB11Segment, left *int) {
	for _, seg := range segs {
		if *left <= 0 {
			return
		}
		if seg.Type != message.SegmentForward {
			continue
		}
		collectForwardMedia(out, seg, left)
	}
}

// collectForwardMedia 收集一条转发记录内联内容里的图片：逐条消息、逐个段按
// 文档顺序处理，转发段继续递归（内层 id 无处可查，只认内联 content）。
func collectForwardMedia(out *[]msglog.Media, seg message.OB11Segment, left *int) {
	content, ok := message.ParseForwardContent(seg)
	if !ok {
		return
	}
	for _, nested := range content {
		if *left <= 0 {
			return
		}
		for _, inner := range nested.Message {
			if *left <= 0 {
				return
			}
			switch inner.Type {
			case message.SegmentImage, message.SegmentFile:
				if m, ok := imageMediaOf(inner); ok {
					m.InForward = true
					*out = append(*out, m)
					*left--
				}
			case message.SegmentForward:
				collectForwardMedia(out, inner, left)
			}
		}
	}
}

// imageMediaOf 把一个「图片」段解析成媒体：image 段直接取，file 段里能判定为
// 图片的同样按图片处理（部分平台/协议端这样下发图片）。
func imageMediaOf(seg message.OB11Segment) (msglog.Media, bool) {
	data := seg.Data
	if seg.Type == message.SegmentFile {
		img, ok := message.FileSegmentAsImage(seg)
		if !ok {
			return msglog.Media{}, false
		}
		data = img
	} else if seg.Type != message.SegmentImage {
		return msglog.Media{}, false
	}
	u := firstHTTPURL(data, "url", "file")
	if u == "" {
		return msglog.Media{}, false
	}
	return msglog.Media{Kind: msglog.MediaImage, URL: u, FileId: segmentFileID(seg)}, true
}

// segmentFileID 读取段数据里的平台文件 ID：QQ 的图片 / 语音 / 视频地址过期后
// 需要用它向协议端重新申请地址（get_image / get_record / get_file）。
func segmentFileID(seg message.OB11Segment) string {
	s, _ := seg.Data["file_id"].(string)
	return strings.TrimSpace(s)
}

// segmentKindOf 把消息日志的媒体类型映射为消息段类型（媒体地址续期接口按段
// 类型区分平台接口）。
func segmentKindOf(mediaKind string) string {
	switch mediaKind {
	case msglog.MediaImage:
		return message.SegmentImage
	case msglog.MediaRecord:
		return message.SegmentRecord
	case msglog.MediaVideo:
		return message.SegmentVideo
	case msglog.MediaFile:
		return message.SegmentFile
	}
	return ""
}

// matchMediaURL 从重新拉取到的同一条消息里找出与旧媒体对应的地址：优先按
// 文件 ID + 类型精确匹配（重新拉取后媒体顺序 / 数量可能变化），拿不到文件 ID
// 时退回「同类型第 n 个」的位置匹配。
func matchMediaURL(fresh []msglog.Media, old []msglog.Media, index int) string {
	if index < 0 || index >= len(old) {
		return ""
	}
	want := old[index]
	if want.FileId != "" {
		for _, m := range fresh {
			if m.Kind == want.Kind && m.FileId == want.FileId && isHTTPURL(m.URL) {
				return m.URL
			}
		}
		return ""
	}
	if len(fresh) != len(old) {
		return ""
	}
	if m := fresh[index]; m.Kind == want.Kind && isHTTPURL(m.URL) {
		return m.URL
	}
	return ""
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
