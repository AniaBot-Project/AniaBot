package pluginaichat

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---- 执行参数跟随主模型 ----

// TestSubagentTimeoutFollowsMain 子代理默认超时跟随框架消息处理预算
// （bot.msg_event_timeout_sec，Start 时读入 p.msgEventTimeout）；未配置时
// 与 core.MsgEventTimeout 兜底一致（5 分钟）。
func TestSubagentTimeoutFollowsMain(t *testing.T) {
	p := &AIChatPlugin{}
	if got := p.subagentTimeout(); got != 5*time.Minute {
		t.Fatalf("未配置时兜底超时 = %v, want 5m", got)
	}
	p.msgEventTimeout = 10 * time.Minute
	if got := p.subagentTimeout(); got != 10*time.Minute {
		t.Fatalf("超时应跟随框架消息处理预算, got %v", got)
	}
}

// TestSubagentMaxIterationsFollowsMain 子代理工具轮数跟随主模型的最大工具调用轮数。
func TestSubagentMaxIterationsFollowsMain(t *testing.T) {
	p := &AIChatPlugin{}
	if got := p.mainMaxIterations(); got != 20 {
		t.Fatalf("未配置时兜底轮数 = %d, want 20", got)
	}
	p.cfg.MaxIterations = 55
	if got := p.mainMaxIterations(); got != 55 {
		t.Fatalf("轮数应跟随主模型配置, got %d", got)
	}
}

// ---- resolveSubagentTimeout ----

func TestResolveSubagentTimeout(t *testing.T) {
	def := 5 * time.Minute

	t.Run("无父deadline时用传入的默认超时", func(t *testing.T) {
		got, err := resolveSubagentTimeout(def, 0, context.Background())
		if err != nil || got != def {
			t.Fatalf("got %v, err %v; want %v", got, err, def)
		}
	})

	t.Run("单次调用覆盖默认超时", func(t *testing.T) {
		got, err := resolveSubagentTimeout(def, 60, context.Background())
		if err != nil || got != 60*time.Second {
			t.Fatalf("got %v, err %v; want 60s", got, err)
		}
	})

	t.Run("超大timeout_sec先限幅再乘防止溢出", func(t *testing.T) {
		got, err := resolveSubagentTimeout(def, 10000000000, context.Background())
		if err != nil || got != subagentMaxTimeoutSec*time.Second {
			t.Fatalf("got %v, err %v; want %v", got, err, subagentMaxTimeoutSec*time.Second)
		}
	})

	t.Run("父deadline预算不足时压缩超时", func(t *testing.T) {
		// 父请求还剩 60s：预留 30s 收尾后预算 30s，默认 300s 被压缩到 30s
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		got, err := resolveSubagentTimeout(def, 0, ctx)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got <= 0 || got > 30*time.Second {
			t.Fatalf("超时应被压缩到 (0, 30s], got %v", got)
		}
	})

	t.Run("父deadline预算耗尽时直接报错", func(t *testing.T) {
		// 父请求只剩 10s（不足 30s 预留），不应启动子代理
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := resolveSubagentTimeout(def, 0, ctx); err == nil {
			t.Fatal("预算耗尽时应返回错误")
		}
	})
}

// ---- makeSubagentCallbacks ----

func TestMakeSubagentCallbacks(t *testing.T) {
	p := &AIChatPlugin{}
	parentSendTextCalled := false
	parentLoadImagesCalled := false
	parent := llmtool.CallBackFuncs{
		SendText: func(s string) (string, error) {
			parentSendTextCalled = true
			return "发送成功", nil
		},
		SendImage: func(bs64 string) (string, error) {
			return "图片已发送:" + bs64, nil
		},
		LoadImages: func(_ []string) (string, error) {
			parentLoadImagesCalled = true
			return "已加载 1 张图片", nil
		},
		TakeLoadedImages: func() []string {
			return []string{"parent-image"}
		},
	}
	cbs := p.makeSubagentCallbacks(context.Background(), parent, testLogger(), nil)

	// 中间轮文本被丢弃，不透传给主会话（避免打扰用户）
	res, err := cbs.SendText("中间过程文本")
	if err != nil {
		t.Fatalf("SendText err = %v", err)
	}
	if parentSendTextCalled {
		t.Fatal("子代理中间轮文本不应调用主会话的 SendText")
	}
	if !strings.Contains(res, "未发送") {
		t.Fatalf("SendText 应返回丢弃提示, got %q", res)
	}

	// 图片发送等能力透传主会话回调
	res, err = cbs.SendImage("base64data")
	if err != nil {
		t.Fatalf("SendImage err = %v", err)
	}
	if res != "图片已发送:base64data" {
		t.Fatalf("SendImage 应透传主会话回调, got %q", res)
	}

	// LoadImages 不透传：子代理有独立图片状态，不与主会话互踩
	res, err = cbs.LoadImages([]string{"a1b2c3d4"})
	if err != nil {
		t.Fatalf("LoadImages err = %v", err)
	}
	if parentLoadImagesCalled {
		t.Fatal("子代理 LoadImages 不应调用主会话回调（共享图片状态会互踩）")
	}
	if !strings.Contains(res, "子代理无法直接加载") {
		t.Fatalf("LoadImages 应返回独立提示, got %q", res)
	}

	// TakeLoadedImages 排空子代理自己的队列，不动主会话的
	if imgs := cbs.TakeLoadedImages(); len(imgs) != 0 {
		t.Fatalf("子代理图片队列初始应为空, got %v", imgs)
	}
}

// TestSubagentCallbacksLocalImageIsolation 多模态开启时，子代理经 LoadLocalImage
// 加载的本地图片进入子代理自己的队列（由 TakeLoadedImages 排空），与主会话隔离。
func TestSubagentCallbacksLocalImageIsolation(t *testing.T) {
	p := &AIChatPlugin{}
	p.cfg.Multimodal = true

	imgPath := filepath.Join(t.TempDir(), "test.png")
	// 真实 PNG 字节：加载路径按内容魔数识别格式，假字节会被判定为无法识别
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgPath, pngBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	parentTakeCalled := false
	parent := llmtool.CallBackFuncs{
		TakeLoadedImages: func() []string {
			parentTakeCalled = true
			return nil
		},
	}
	cbs := p.makeSubagentCallbacks(context.Background(), parent, testLogger(), nil)
	if cbs.LoadLocalImage == nil {
		t.Fatal("多模态开启时子代理应支持 LoadLocalImage")
	}

	res, err := cbs.LoadLocalImage(imgPath)
	if err != nil {
		t.Fatalf("LoadLocalImage err = %v", err)
	}
	if !strings.Contains(res, "已加载本地图片") {
		t.Fatalf("LoadLocalImage 提示异常, got %q", res)
	}
	imgs := cbs.TakeLoadedImages()
	if len(imgs) != 1 || !strings.HasPrefix(imgs[0], "data:image/png;base64,") {
		t.Fatalf("子代理图片队列应有 1 张 data URI 图片, got %v", imgs)
	}
	if parentTakeCalled {
		t.Fatal("子代理 TakeLoadedImages 不应触碰主会话图片队列")
	}
	// 排空后再次取应为空
	if imgs := cbs.TakeLoadedImages(); len(imgs) != 0 {
		t.Fatalf("排空后应返回空, got %v", imgs)
	}
}

// TestSubagentCallbacksLocalImageDisabled 未启用多模态且无 OCR 模型时，
// 子代理不支持读取本地图片（LoadLocalImage 为 nil，与 clock 回调语义一致）。
func TestSubagentCallbacksLocalImageDisabled(t *testing.T) {
	p := &AIChatPlugin{}
	cbs := p.makeSubagentCallbacks(context.Background(), llmtool.CallBackFuncs{}, testLogger(), nil)
	if cbs.LoadLocalImage != nil {
		t.Fatal("未启用多模态且无 OCR 模型时 LoadLocalImage 应为 nil")
	}
}

// ---- newSubagentTools ----

func TestNewSubagentTools(t *testing.T) {
	p := &AIChatPlugin{}

	groupTools := newSubagentTools(p, nil, message.FromUint64(12345), true)
	if len(groupTools) != 3 {
		t.Fatalf("应有 3 个子代理工具（run/list/cancel）, got %d", len(groupTools))
	}
	tool := groupTools[0]
	if tool.Name() != "subagent_run" {
		t.Fatalf("工具名 = %q, want subagent_run", tool.Name())
	}
	if !strings.Contains(tool.Description(), "群聊（会话 ID qq:12345）") {
		t.Fatalf("群聊会话描述应包含会话 ID, got %q", tool.Description())
	}
	if !strings.Contains(tool.Description(), "300 秒") {
		t.Fatalf("描述应包含默认超时, got %q", tool.Description())
	}
	// 超时跟随框架消息处理预算：配置后描述同步变化
	p.msgEventTimeout = 10 * time.Minute
	if desc := newSubagentTools(p, nil, message.FromUint64(12345), true)[0].Description(); !strings.Contains(desc, "600 秒") {
		t.Fatalf("超时应跟随框架消息处理预算（600 秒）, got %q", desc)
	}

	friendTools := newSubagentTools(p, nil, message.FromUint64(67890), false)
	if !strings.Contains(friendTools[0].Description(), "私聊（对方 ID qq:67890）") {
		t.Fatalf("私聊会话描述应包含对方 ID, got %q", friendTools[0].Description())
	}
}

// ---- subagent_run 参数校验 ----

func TestSubagentRunToolEmptyTask(t *testing.T) {
	p := &AIChatPlugin{}
	tool := newSubagentTools(p, nil, message.FromUint64(1), true)[0]

	if _, err := tool.Execute(context.Background(), &subagentRunParams{Task: "   "}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("空 task 应返回错误")
	}
}

// ---- 子代理模型配置跟随主模型 ----

func TestSubagentLLMConfigFollowsMain(t *testing.T) {
	p := &AIChatPlugin{}
	p.cfg.BaseURL = "https://main.example.com"
	p.cfg.APIKey = "main-key"
	p.cfg.Model = "main-model"
	p.cfg.APIFormat = "anthropic"

	base, key, model, format := p.subagentLLMConfig()
	if base != "https://main.example.com" || key != "main-key" || model != "main-model" || format != "anthropic" {
		t.Fatalf("子代理模型配置应跟随主模型, got %q/%q/%q/%q", base, key, model, format)
	}
}

func TestCompressorLLMConfigFallback(t *testing.T) {
	p := &AIChatPlugin{}
	p.cfg.BaseURL = "https://main.example.com"
	p.cfg.APIKey = "main-key"
	p.cfg.Model = "main-model"

	base, key, model, _ := p.compressorLLMConfig()
	if base != "https://main.example.com" || key != "main-key" || model != "main-model" {
		t.Fatalf("全空应回退主模型, got %q/%q/%q", base, key, model)
	}

	p.cfg.Compressor.Model = "summary-model"
	if _, _, model, _ := p.compressorLLMConfig(); model != "summary-model" {
		t.Fatalf("model = %q, want summary-model", model)
	}

	// 格式回退与独立覆盖
	p.cfg.APIFormat = "anthropic"
	if _, _, _, format := p.compressorLLMConfig(); format != "anthropic" {
		t.Fatalf("format 未填应回退主格式, got %q", format)
	}
	p.cfg.Compressor.APIFormat = "chat_completions"
	if _, _, _, format := p.compressorLLMConfig(); format != "chat_completions" {
		t.Fatalf("独立 format 未生效, got %q", format)
	}
}

func TestBuildCompressorClientNilWhenEmpty(t *testing.T) {
	p := &AIChatPlugin{}
	p.cfg.BaseURL = "https://main.example.com"
	p.cfg.APIKey = "main-key"
	p.cfg.Model = "main-model"
	// 三字段全空：返回 nil（压缩复用主对话客户端）
	if c := p.buildCompressorClient(); c != nil {
		t.Fatalf("三字段全空时应返回 nil, got %v", c)
	}
	// 配置了模型则构造独立客户端
	p.cfg.Compressor.Model = "summary-model"
	if c := p.buildCompressorClient(); c == nil {
		t.Fatal("配置了模型应返回独立客户端")
	}
}
