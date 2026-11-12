package pluginlog

import (
	"log/slog"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/core"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeMediaBot 媒体续期 / 消息回捞的测试桩：嵌入 bot.Bot 接口以满足公共外观，
// 只覆写测试用到的方法（其余方法被调用即 panic，测试不会触发）。
type fakeMediaBot struct {
	bot.Bot
	refreshCalls int
	refreshURL   string
	lastKind     string
	lastFileID   string
	lastGroupID  string
	lastUserID   string
	msg          *message.Message
}

func (f *fakeMediaBot) RefreshMediaURL(kind, fileId, groupId, userId string) (string, bool) {
	f.refreshCalls++
	f.lastKind, f.lastFileID, f.lastGroupID, f.lastUserID = kind, fileId, groupId, userId
	return f.refreshURL, f.refreshURL != ""
}

func (f *fakeMediaBot) GetMsgDetail(msgId message.QID) (*message.Message, bool) {
	if f.msg == nil {
		return nil, false
	}
	return f.msg, true
}

// newMediaTestPlugin 构造带记录器与 bot 的插件实例：写入一条含图片 / 文件的群消息日志。
func newMediaTestPlugin(t *testing.T, b bot.Bot) (*LogPlugin, uint64) {
	t.Helper()
	p := NewPlugin()
	p.Logger = slog.Default()
	p.recorder = msglog.New(core.NewAniaMemoryStorage(slog.Default()), 0)
	p.bot = b
	p.recorder.Add(msglog.Entry{
		Type:      msglog.TypeGroup,
		GroupId:   "qq:123",
		UserId:    "qq:456",
		MessageId: "qq:789",
		Text:      "[图片]",
		Media: []msglog.Media{
			{Kind: msglog.MediaImage, URL: "https://cdn.example.com/old.png?rkey=old", FileId: "fid-img"},
			{Kind: msglog.MediaFile, URL: "https://cdn.example.com/old.zip?rkey=old", FileId: "fid-file", Name: "a.zip"},
		},
	})
	return p, 1
}

// TestMsgLogMediaURLRefreshByFileID 优先按文件 ID 向平台续期，参数要带上
// 媒体类型与群 / 用户 ID（群文件、私聊文件续期需要）。
func TestMsgLogMediaURLRefreshByFileID(t *testing.T) {
	fb := &fakeMediaBot{refreshURL: "https://cdn.example.com/new.png?rkey=new"}
	p, id := newMediaTestPlugin(t, fb)

	got, ok := p.MsgLogMediaURL(id, 0)
	if !ok || got != "https://cdn.example.com/new.png?rkey=new" {
		t.Fatalf("MsgLogMediaURL = %q, %v", got, ok)
	}
	if fb.refreshCalls != 1 || fb.lastKind != message.SegmentImage || fb.lastFileID != "fid-img" ||
		fb.lastGroupID != "qq:123" || fb.lastUserID != "qq:456" {
		t.Fatalf("续期参数错误: calls=%d kind=%q file=%q group=%q user=%q",
			fb.refreshCalls, fb.lastKind, fb.lastFileID, fb.lastGroupID, fb.lastUserID)
	}

	// 同一媒体重复请求走短缓存，不再打扰协议端
	if _, ok := p.MsgLogMediaURL(id, 0); !ok || fb.refreshCalls != 1 {
		t.Fatalf("重复请求应命中缓存, calls=%d", fb.refreshCalls)
	}

	// 文件类媒体按 SegmentFile 续期
	if _, ok := p.MsgLogMediaURL(id, 1); !ok || fb.lastKind != message.SegmentFile {
		t.Fatalf("文件续期类型错误: kind=%q ok=%v", fb.lastKind, ok)
	}
}

// TestMsgLogMediaURLFallbackToMessage 平台没有直接续期能力（或文件 ID 续期失败）时，
// 退化为重新拉取原消息，并按文件 ID 匹配新地址。
func TestMsgLogMediaURLFallbackToMessage(t *testing.T) {
	fb := &fakeMediaBot{msg: &message.Message{Message: []message.OB11Segment{
		{Type: message.SegmentText, Data: map[string]any{"text": "图"}},
		{Type: message.SegmentImage, Data: map[string]any{
			"url":     "https://cdn.example.com/fresh.png?rkey=fresh",
			"file_id": "fid-img",
		}},
		{Type: message.SegmentFile, Data: map[string]any{
			"url": "https://cdn.example.com/fresh.zip?rkey=fresh", "file_id": "fid-file", "name": "a.zip",
		}},
	}}}
	p, id := newMediaTestPlugin(t, fb)

	got, ok := p.MsgLogMediaURL(id, 0)
	if !ok || got != "https://cdn.example.com/fresh.png?rkey=fresh" {
		t.Fatalf("回捞消息续期失败: %q, %v", got, ok)
	}
	if fb.refreshCalls != 1 {
		t.Fatalf("应先把文件 ID 交给平台续期, calls=%d", fb.refreshCalls)
	}
}

// TestMsgLogMediaURLSameURLFallsThrough 协议端回了同一条过期地址时不算续期成功，
// 继续尝试重新拉取消息（消息重新编码时仍可能拿到新签发的地址）。
func TestMsgLogMediaURLSameURLFallsThrough(t *testing.T) {
	fb := &fakeMediaBot{
		refreshURL: "https://cdn.example.com/old.png?rkey=old", // 与日志里相同
		msg: &message.Message{Message: []message.OB11Segment{
			{Type: message.SegmentImage, Data: map[string]any{
				"url":     "https://cdn.example.com/fresh.png?rkey=fresh",
				"file_id": "fid-img",
			}},
			{Type: message.SegmentFile, Data: map[string]any{
				"url": "https://cdn.example.com/fresh.zip", "file_id": "fid-file",
			}},
		}},
	}
	p, id := newMediaTestPlugin(t, fb)

	got, ok := p.MsgLogMediaURL(id, 0)
	if !ok || got != "https://cdn.example.com/fresh.png?rkey=fresh" {
		t.Fatalf("同地址时应回退到消息回捞, got %q, %v", got, ok)
	}
}

// TestMsgLogMediaURLInvalidRef 越界下标 / 不存在的日志 / 未注入 bot 都返回 false。
func TestMsgLogMediaURLInvalidRef(t *testing.T) {
	fb := &fakeMediaBot{refreshURL: "https://cdn.example.com/new.png"}
	p, id := newMediaTestPlugin(t, fb)

	if _, ok := p.MsgLogMediaURL(id, 2); ok {
		t.Fatal("越界下标不应成功")
	}
	if _, ok := p.MsgLogMediaURL(id+1, 0); ok {
		t.Fatal("不存在的日志不应成功")
	}
	p.bot = nil
	if _, ok := p.MsgLogMediaURL(id, 0); ok {
		t.Fatal("未注入 bot 时不应成功")
	}
}
