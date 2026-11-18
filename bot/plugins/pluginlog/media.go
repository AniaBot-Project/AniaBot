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

// forwardFetchFunc 按消息 ID 拉取合并转发内容（QQ 平台能力）。收消息事件里的
// forward 段通常只带 id、没有内联内容，节点里的图片必须靠它拿到；非 QQ 平台与
// 发送路径（事件载荷自带内联内容）传 nil 即可。
type forwardFetchFunc func(msgId message.QID) ([]message.Message, bool)

// extractMedia 从消息段中提取图片 / 文件 / 语音 / 视频，供面板在聊天气泡内联
// 展示图片、提供文件下载。只保留 http(s) 地址：data:/base64: 内联负载体积
// 可达 MB 级，塞进每条日志会让缓存存储迅速膨胀（详见 msglog.Media 注释）。
//
// 只认转发段内联携带的 content：发送路径的载荷本身就带内容，不需要再向平台
// 请求；收消息路径用 extractMediaWithFetch 按 id 展开。
func extractMedia(segs []message.OB11Segment) []msglog.Media {
	return extractMediaWithFetch(segs, nil)
}

// extractMediaWithFetch 同 extractMedia，额外支持按 id 展开只带 id 的合并转发段。
//
// 合并转发记录内部（含多层嵌套）的图片另取一批追加在后面并标记 InForward：
// 面板按出现顺序把它们填回对应转发节点里的 [图片] 占位，让聊天记录里的图片
// 也能直接看到。内部的文件 / 语音 / 视频不取，节点里保留文字占位。
func extractMediaWithFetch(segs []message.OB11Segment, fetch forwardFetchFunc) []msglog.Media {
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
	appendForwardImages(&out, segs, &left, fetch)
	return out
}

// appendForwardImages 从顶层消息段里找出合并转发段，按文档顺序收集其内部
// （含多层嵌套）的图片，与 FriendlyText 写出 [图片] 占位的顺序一致，面板
// 据此逐个匹配。只收图片（含以 file 段发来的图片），其他类型在转发节点里
// 保留文字占位；整条日志最多 left 张，每张都标记 InForward。
//
// fetch 用于展开只带 id 的转发段（收消息事件里就是这样）：调用方传入的通常
// 是「文本渲染时按 id 拉过的内容」的只读缓存，因此不会重复请求协议端。
func appendForwardImages(out *[]msglog.Media, segs []message.OB11Segment, left *int, fetch forwardFetchFunc) {
	for _, seg := range segs {
		if *left <= 0 {
			return
		}
		if seg.Type != message.SegmentForward {
			continue
		}
		collectForwardMedia(out, seg, left, fetch)
	}
}

// collectForwardMedia 收集一条转发记录里的图片：逐条消息、逐个段按文档顺序
// 处理，转发段继续递归（内层 id 无处可查，通常只认内联 content）。
func collectForwardMedia(out *[]msglog.Media, seg message.OB11Segment, left *int, fetch forwardFetchFunc) {
	content, ok := forwardContentOf(seg, fetch)
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
				collectForwardMedia(out, inner, left, fetch)
			}
		}
	}
}

// forwardContentOf 取转发段的内容：优先内联 content，没有时再按 id 拉取
// （收消息事件里的 forward 段通常只带 id，NapCat / Luckylilia 都是如此）。
func forwardContentOf(seg message.OB11Segment, fetch forwardFetchFunc) ([]message.Message, bool) {
	if content, ok := message.ParseForwardContent(seg); ok {
		return content, true
	}
	if fetch == nil {
		return nil, false
	}
	var fwd message.ForwardMessage
	if !message.ParseForward(seg, &fwd) {
		return nil, false
	}
	return fetch(fwd.Id)
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
//
// file 源可能是 base64:// 内联负载（Bot 用 file 工具发送时就是这样）：不能拿它
// 当文件名，否则 MB 级 base64 会随日志条目进入缓存与面板响应。此时退回「文件」，
// 并按 base64 长度估算真实文件大小（不实际解码）。
func fileMedia(seg message.OB11Segment) (msglog.Media, bool) {
	var f message.FileMessage
	if !message.ParseFile(seg, &f) {
		return msglog.Media{}, false
	}
	inline := strings.HasPrefix(f.File, "base64://") || strings.HasPrefix(f.File, "data:")
	name := f.Name
	if name == "" && !inline {
		name = f.File
	}
	if name != "" {
		name = strings.SplitN(name, "?", 2)[0]
		name = filepath.Base(strings.TrimRight(name, "/"))
	}
	if name == "" || name == "." {
		name = "文件"
	}
	size := segmentSize(seg)
	if size == 0 && inline {
		size = inlinePayloadSize(f.File)
	}
	m := msglog.Media{
		Kind:   msglog.MediaFile,
		Name:   name,
		Size:   size,
		FileId: f.FileId,
	}
	if isHTTPURL(f.URL) {
		m.URL = f.URL
	} else if isHTTPURL(f.File) {
		m.URL = f.File
	}
	return m, true
}

// inlinePayloadSize 估算内联负载的原始字节数（base64:// 或 data:*;base64,）：
// 只做长度换算，不解码内容，避免为了显示文件大小而复制 MB 级数据。
// 非 base64 的 data: 负载（百分比编码等）无法估算时返回 0。
func inlinePayloadSize(src string) int64 {
	var b64 string
	switch {
	case strings.HasPrefix(src, "base64://"):
		b64 = strings.TrimPrefix(src, "base64://")
	case strings.HasPrefix(src, "data:"):
		if i := strings.Index(src, ";base64,"); i >= 0 {
			b64 = src[i+len(";base64,"):]
		}
	}
	if b64 == "" {
		return 0
	}
	size := int64(len(b64)/4) * 3
	switch {
	case strings.HasSuffix(b64, "=="):
		size -= 2
	case strings.HasSuffix(b64, "="):
		size--
	}
	if size < 0 {
		return 0
	}
	return size
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
