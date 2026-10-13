package message

import "testing"

func TestIncludesMediaUpload(t *testing.T) {
	textSeg := OB11Segment{Type: SegmentText, Data: map[string]any{"text": "hi"}}
	imageSeg := OB11Segment{Type: SegmentImage, Data: map[string]any{"file": "base64://x"}}
	fileSeg := OB11Segment{Type: SegmentFile, Data: map[string]any{"file": "base64://x", "name": "a.zip"}}

	// 消息段列表（带 message 字段的参数映射）内的文件段
	if !IncludesMediaUpload(map[string]any{"group_id": "1", "message": []OB11Segment{textSeg, fileSeg}}) {
		t.Fatal("含 file 段的请求应判定为媒体上传")
	}
	// 仅图片/文本不判定为媒体上传（图片走独立的快速通道）
	if IncludesMediaUpload(map[string]any{"message": []OB11Segment{textSeg, imageSeg}}) {
		t.Fatal("仅图片/文本的请求不应判定为媒体上传")
	}
	// 视频/语音段
	if !IncludesMediaUpload([]OB11Segment{{Type: SegmentVideo}}) {
		t.Fatal("视频段应判定为媒体上传")
	}
	if !IncludesMediaUpload([]OB11Segment{{Type: SegmentRecord}}) {
		t.Fatal("语音段应判定为媒体上传")
	}
	// 合并转发内嵌消息段（群聊与私聊两种请求体）
	var node NodeMsg
	node.Type = "node"
	node.Data.Content = []OB11Segment{fileSeg}
	if !IncludesMediaUpload(GroupForwardMessage{ForwardMessageSegment: ForwardMessageSegment{Messages: []NodeMsg{node}}}) {
		t.Fatal("群合并转发内嵌 file 段应判定为媒体上传")
	}
	if !IncludesMediaUpload(FriendForwardMessage{ForwardMessageSegment: ForwardMessageSegment{Messages: []NodeMsg{node}}}) {
		t.Fatal("好友合并转发内嵌 file 段应判定为媒体上传")
	}
	// 其他参数形态（查询接口的空结构体等）不判定为媒体上传
	if IncludesMediaUpload(struct{}{}) || IncludesMediaUpload(nil) {
		t.Fatal("非消息参数不应判定为媒体上传")
	}
}
