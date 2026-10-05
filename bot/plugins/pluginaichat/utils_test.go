package pluginaichat

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/aitool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// testPNGDataURI / testGIFDataURI 供本地加载测试使用（data URI 无需网络下载）。
const (
	testPNGDataURI = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	testGIFDataURI = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"
)

// fakeMsgSource 测试用消息来源：实现 GetMsgDetail 与 GetForwardMsg 两个最小能力。
type fakeMsgSource struct {
	details     map[message.QID]*message.Message
	forwards    map[message.QID]*[]message.Message
	detailCalls int // GetMsgDetail 调用次数（用于断言不应发起无谓请求）
}

func (f *fakeMsgSource) GetMsgDetail(id message.QID) (*message.Message, bool) {
	f.detailCalls++
	d, ok := f.details[id]
	return d, ok
}

func (f *fakeMsgSource) GetForwardMsg(id message.QID) (*[]message.Message, bool) {
	d, ok := f.forwards[id]
	return d, ok
}

func imageSegment(url string) message.OB11Segment {
	return message.OB11Segment{Type: message.SegmentImage, Data: map[string]any{"url": url, "file": "img.png"}}
}

func TestImageRegistryResolve(t *testing.T) {
	reg := newImageRegistry()
	const urlA = testPNGDataURI
	const urlB = testGIFDataURI
	reg.register(message.ImageHash(urlA), urlA)
	reg.register(message.ImageHash(urlA), "https://example.com/dup.png") // 先到先得
	reg.register(message.ImageHash(urlB), urlB)

	found, missing := reg.resolve([]string{message.ImageHash(urlA), "deadbeef", message.ImageHash(urlB)})
	if len(found) != 2 {
		t.Fatalf("resolve found = %d, want 2", len(found))
	}
	if found[0].URL != urlA {
		t.Fatalf("首个引用 URL = %q, want %q（重复登记不应覆盖）", found[0].URL, urlA)
	}
	if len(missing) != 1 || missing[0] != "deadbeef" {
		t.Fatalf("missing = %v, want [deadbeef]", missing)
	}
}

func TestRegisterMessageImagesRecursive(t *testing.T) {
	const urlMain = "https://example.com/main.png"
	const urlReply = "https://example.com/reply.png"
	const urlForward = "https://example.com/forward.png"

	replyMsg := &message.Message{
		MessageId: message.FromString("reply-1"),
		Message:   []message.OB11Segment{imageSegment(urlReply)},
	}
	forwardMsg := &message.Message{
		MessageId: message.FromString("fwd-1"),
		Message:   []message.OB11Segment{imageSegment(urlForward)},
	}
	forwards := []message.Message{*forwardMsg}

	src := &fakeMsgSource{
		details: map[message.QID]*message.Message{
			message.FromString("r1"): replyMsg,
		},
		forwards: map[message.QID]*[]message.Message{
			message.FromString("f1"): &forwards,
		},
	}

	main := message.Message{
		MessageId: message.FromString("main-1"),
		Message: []message.OB11Segment{
			imageSegment(urlMain),
			{Type: message.SegmentReply, Data: map[string]any{"id": message.FromString("r1").String()}},
			{Type: message.SegmentForward, Data: map[string]any{"id": message.FromString("f1").String()}},
		},
	}

	reg := newImageRegistry()
	registerMessageImages(reg, src, main)

	found, missing := reg.resolve([]string{
		message.ImageHash(urlMain),
		message.ImageHash(urlReply),
		message.ImageHash(urlForward),
	})
	if len(found) != 3 {
		t.Fatalf("resolve found = %d, want 3 (missing=%v)", len(found), missing)
	}
}

func TestConfigureImageCallbacksLoadByHash(t *testing.T) {
	p := &AIChatPlugin{}
	p.cfg.Multimodal = true

	const urlA = testPNGDataURI
	const urlB = testGIFDataURI
	reg := newImageRegistry()

	// 当前消息中的图片由 configureImageCallbacks 自动登记
	curMsg := message.Message{Message: []message.OB11Segment{imageSegment(urlA), imageSegment(urlB)}}

	var cbs llmtool.CallBackFuncs
	p.configureImageCallbacks(context.Background(), nil, &cbs, reg, nil, curMsg)
	if cbs.LoadImages == nil {
		t.Fatal("LoadImages 回调未挂载")
	}

	// 只加载指定的哈希，不把全部图片塞进上下文
	res, err := cbs.LoadImages([]string{message.ImageHash(urlA)})
	if err != nil {
		t.Fatalf("LoadImages err = %v", err)
	}
	if !strings.Contains(res, "已加载 1 张图片") {
		t.Fatalf("LoadImages 提示异常, got %q", res)
	}
	if imgs := cbs.TakeLoadedImages(); len(imgs) != 1 || imgs[0] != urlA {
		t.Fatalf("待加载队列应为 [%s], got %v", urlA, imgs)
	}

	// 已加载的哈希重复调用不再加载
	res, err = cbs.LoadImages([]string{message.ImageHash(urlA), message.ImageHash(urlB)})
	if err != nil {
		t.Fatalf("LoadImages err = %v", err)
	}
	if !strings.Contains(res, "已加载 1 张图片") {
		t.Fatalf("第二次应只加载未加载过的图片, got %q", res)
	}
	if imgs := cbs.TakeLoadedImages(); len(imgs) != 1 || imgs[0] != urlB {
		t.Fatalf("待加载队列应为 [%s], got %v", urlB, imgs)
	}

	// 未登记的哈希给出引导
	res, err = cbs.LoadImages([]string{"deadbeef"})
	if err != nil {
		t.Fatalf("LoadImages err = %v", err)
	}
	if !strings.Contains(res, "没有找到可加载的图片") {
		t.Fatalf("未找到哈希时应提示, got %q", res)
	}

	// 空哈希返回引导，不加载任何图片
	res, err = cbs.LoadImages(nil)
	if err != nil {
		t.Fatalf("LoadImages err = %v", err)
	}
	if !strings.Contains(res, "hashes 参数") {
		t.Fatalf("空哈希应提示传入 hashes, got %q", res)
	}
}

// TestRegisterMessageImagesEmbeddedText 平台文本内嵌的图片附件描述（如 QQ 官方
// 聊天记录）应登记哈希与文件名别名：load_images 传哈希或文件名都能加载，
// 且别名命中后返回规范哈希（与 [图片 <hash>] 标记一致）。
func TestRegisterMessageImagesEmbeddedText(t *testing.T) {
	const urlA = "https://multimedia.nt.qq.com.cn/download?appid=1&fileid=a&rkey=x&spec=0"
	const urlB = "https://multimedia.nt.qq.com.cn/download?appid=1&fileid=b&rkey=y&spec=0"
	text := "[群聊的聊天记录]\n" +
		"=== 消息 1 ===\n" +
		"[发送者] Ice-Nick\n" +
		"[附件1] 类型:图片 文件名:photoA.jpg 尺寸:630x1142 大小:101.9KB URL:" + urlA + "\n" +
		"=== 消息 2 ===\n" +
		"[附件1] 类型:视频 文件名:mv.mp4 尺寸:1920x1080 URL:" + urlB + "\n"
	msg := message.Message{
		Message: []message.OB11Segment{{Type: message.SegmentText, Data: map[string]any{"text": text}}},
	}

	reg := newImageRegistry()
	registerMessageImages(reg, nil, msg)

	// 按规范哈希加载
	found, missing := reg.resolve([]string{message.ImageHash(urlA)})
	if len(found) != 1 || found[0].URL != urlA || len(missing) != 0 {
		t.Fatalf("按哈希解析失败: found=%+v missing=%v", found, missing)
	}
	// AI 拿文件名当哈希也能加载（回归：QQ 官方聊天记录此前只给文本，AI 乱用文件名）
	found, missing = reg.resolve([]string{"photoA.jpg"})
	if len(found) != 1 || found[0].URL != urlA || found[0].Hash != message.ImageHash(urlA) {
		t.Fatalf("文件名别名解析失败: found=%+v missing=%v", found, missing)
	}
	// 非图片附件（视频）不登记为图片
	found, missing = reg.resolve([]string{message.ImageHash(urlB)})
	if len(found) != 0 || len(missing) != 1 {
		t.Fatalf("视频不应登记为图片: found=%+v missing=%v", found, missing)
	}
}

// TestAnnotateEmbeddedImages 文本内嵌图片附件描述应补充 [图片 <hash> url:<url>] 标记，
// 使 AI 看到与 load_images 对应的哈希而不是文件名。
func TestAnnotateEmbeddedImages(t *testing.T) {
	const url = "https://multimedia.nt.qq.com.cn/download?appid=1&fileid=a&rkey=x&spec=0"
	text := "[群聊的聊天记录]\n[发送者] A\n[附件1] 类型:图片 文件名:p.jpg 尺寸:1x1 大小:1KB URL:" + url + "\n[消息内容] hi"
	out := aitool.AnnotateEmbeddedImages(text)
	if !strings.Contains(out, "[图片 "+message.ImageHash(url)+" url:"+url+"]") {
		t.Fatalf("应补充 [图片 <hash> url:<url>] 标记, got %q", out)
	}
	if !strings.Contains(out, "文件名:p.jpg") {
		t.Fatalf("附件描述原文应保留（标记追加在描述后）, got %q", out)
	}
}

// TestRegisterMessageImagesInlineForward NapCat 嵌套转发以内联 content 形式存在
// （内层 id 无法再拉取），其中的图片应直接登记，供 load_images 按哈希加载。
func TestRegisterMessageImagesInlineForward(t *testing.T) {
	const urlNested = "https://example.com/nested-forward.png"
	inner := message.Message{
		MessageId: message.FromString("inner-view-1"),
		Message:   []message.OB11Segment{imageSegment(urlNested)},
	}
	main := message.Message{
		MessageId: message.FromString("main-1"),
		Message: []message.OB11Segment{{
			Type: message.SegmentForward,
			Data: map[string]any{"id": "view-only-fwd", "content": []message.Message{inner}},
		}},
	}

	reg := newImageRegistry()
	// 不提供任何可拉取的消息源（nil），内联 content 里的图片仍应登记
	registerMessageImages(reg, nil, main)

	found, missing := reg.resolve([]string{message.ImageHash(urlNested)})
	if len(found) != 1 || found[0].URL != urlNested || len(missing) != 0 {
		t.Fatalf("内联转发图片应被登记: found=%+v missing=%v", found, missing)
	}
}

// TestRegisterMessageImagesSkipsReplyInsideForward 合并转发记录内的 reply 目标多为
// 聊天记录里的历史消息，NapCat 无法再通过 get_msg 拉取。登记图片时不应解析转发
// 内的 reply，避免处理转发消息时刷出一堆「消息不存在或已被撤回」的请求失败日志。
func TestRegisterMessageImagesSkipsReplyInsideForward(t *testing.T) {
	const urlInner = "https://example.com/inner.png"
	const urlReply = "https://example.com/reply-target.png"
	replyTarget := &message.Message{
		MessageId: message.FromString("reply-1"),
		Message:   []message.OB11Segment{imageSegment(urlReply)},
	}
	inner := message.Message{
		MessageId: message.FromString("inner-1"),
		Message: []message.OB11Segment{
			imageSegment(urlInner),
			{Type: message.SegmentReply, Data: map[string]any{"id": message.FromString("r1").String()}},
		},
	}
	src := &fakeMsgSource{
		details: map[message.QID]*message.Message{
			message.FromString("r1"): replyTarget,
		},
	}
	main := message.Message{
		MessageId: message.FromString("main-1"),
		Message: []message.OB11Segment{{
			Type: message.SegmentForward,
			Data: map[string]any{"id": "view-only-fwd", "content": []message.Message{inner}},
		}},
	}

	reg := newImageRegistry()
	registerMessageImages(reg, src, main)
	if src.detailCalls != 0 {
		t.Fatalf("转发记录内的 reply 不应触发 GetMsgDetail, calls=%d", src.detailCalls)
	}
	// 转发记录自身的图片仍应登记
	found, missing := reg.resolve([]string{message.ImageHash(urlInner)})
	if len(found) != 1 || found[0].URL != urlInner || len(missing) != 0 {
		t.Fatalf("转发记录内图片应被登记: found=%+v missing=%v", found, missing)
	}
	// reply 目标未被拉取，其图片不应登记
	_, missing = reg.resolve([]string{message.ImageHash(urlReply)})
	if len(missing) != 1 {
		t.Fatalf("reply 目标图片不应被登记, missing=%v", missing)
	}
}

// TestParseHeaderLines 自定义请求头解析：冒号/等号分隔、值内可含另一种分隔符、
// 前后空白裁剪、重复名后者覆盖；非法行（缺分隔符、空名、名字含非法字符、
// 值含换行/控制字符）被跳过并按 1 基行号报出（日志只记行号，不泄露误填的凭据）。
func TestParseHeaderLines(t *testing.T) {
	lines := []string{
		"X-Token: abc",
		"X-Route=proxy-a",
		"  X-Space :  trimmed  ",
		"Authorization: Bearer a=b",
		"X-Dup: one",
		"X-Dup: two",
		"no-separator",
		": empty-name",
		"Bad Name: v",
		"X-Bad-Value: line\nbreak",
		"",
	}
	headers, invalid := parseHeaderLines(lines)

	want := map[string]string{
		"X-Token":       "abc",
		"X-Route":       "proxy-a",
		"X-Space":       "trimmed",
		"Authorization": "Bearer a=b",
		"X-Dup":         "two",
	}
	if len(headers) != len(want) {
		t.Fatalf("headers = %v, want %v", headers, want)
	}
	for k, v := range want {
		if headers[k] != v {
			t.Errorf("headers[%s] = %q, want %q", k, headers[k], v)
		}
	}
	wantInvalid := []int{7, 8, 9, 10}
	if !slices.Equal(invalid, wantInvalid) {
		t.Fatalf("invalid = %v, want %v", invalid, wantInvalid)
	}
}

// TestParseHeaderLinesEmpty 空配置返回 nil（llmClientOptions 据此跳过 WithHeaders）。
func TestParseHeaderLinesEmpty(t *testing.T) {
	for _, lines := range [][]string{nil, {""}, {"  ", "\t"}} {
		headers, invalid := parseHeaderLines(lines)
		if headers != nil || len(invalid) != 0 {
			t.Fatalf("lines=%q headers=%v invalid=%v，应返回 nil", lines, headers, invalid)
		}
	}
}

// TestParseHeaderLinesCanonical 名字规范化：大小写不同的同名头合并为一条，
// 后出现的值生效（map 遍历顺序随机，不规范化会让生效值不确定）。
func TestParseHeaderLinesCanonical(t *testing.T) {
	headers, invalid := parseHeaderLines([]string{"x-token: one", "X-Token: two"})
	if len(invalid) != 0 {
		t.Fatalf("意外非法行: %v", invalid)
	}
	if len(headers) != 1 || headers["X-Token"] != "two" {
		t.Fatalf("headers = %v, want 单条 X-Token=two", headers)
	}
}
