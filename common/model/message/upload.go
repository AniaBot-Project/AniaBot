package message

// SegmentsContainMedia 判断消息段列表是否包含需上传媒体内容的段（文件/视频/语音）：
// 协议端发送这类消息时需先把内容上传到平台服务器再回包，耗时远高于普通消息。
func SegmentsContainMedia(segs []OB11Segment) bool {
	for _, seg := range segs {
		switch seg.Type {
		case SegmentFile, SegmentVideo, SegmentRecord:
			return true
		}
	}
	return false
}

// IncludesMediaUpload 判断出站请求参数是否包含媒体上传：支持 OneBot v11 发送接口
// 常见的参数形态（消息段列表、带 message 字段的参数映射、合并转发内嵌消息段），
// 供适配器区分普通调用与上传类调用（如选用更长的等待超时）。
func IncludesMediaUpload(params any) bool {
	switch p := params.(type) {
	case map[string]any:
		if segs, ok := p["message"].([]OB11Segment); ok {
			return SegmentsContainMedia(segs)
		}
	case []OB11Segment:
		return SegmentsContainMedia(p)
	case GroupForwardMessage:
		return forwardContainsMedia(p.ForwardMessageSegment)
	case FriendForwardMessage:
		return forwardContainsMedia(p.ForwardMessageSegment)
	}
	return false
}

func forwardContainsMedia(f ForwardMessageSegment) bool {
	for i := range f.Messages {
		if SegmentsContainMedia(f.Messages[i].Data.Content) {
			return true
		}
	}
	return false
}
