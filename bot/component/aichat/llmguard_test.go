package aichat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 单次 LLM 请求保护：请求总超时 / 流空闲超时 ----

// hangingServer 启动一个永不主动返回响应的 mock server，返回释放函数。
//
// 注意：httptest.Server.Close 会等待仍在执行的 handler，而 handler 里的
// <-r.Context().Done() 在客户端超时断开后不一定被服务端及时感知（服务端只在
// 读写连接时才检测断连），因此这里用显式 release 通道：测试结束先释放 handler
// 再 Close，避免测试自身挂住。
func hangingServer() (*httptest.Server, func()) {
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	stop := func() {
		once.Do(func() { close(release) })
		srv.Close()
	}
	return srv, stop
}

// streamThenHangServer 启动一个先输出一个 SSE chunk 随后静默的 mock server
// （模拟上游半挂），返回释放函数（语义同 hangingServer）。
func streamThenHangServer() (*httptest.Server, func()) {
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunk(streamEvent(`[{"index":0,"delta":{"content":"第一段"},"finish_reason":null}]`)))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	stop := func() {
		once.Do(func() { close(release) })
		srv.Close()
	}
	return srv, stop
}

// TestRequestTimeoutGenerate Generate 在请求总超时内返回可识别的超时错误
// （errors.Is context.DeadlineExceeded），而不是一直挂在上游。
func TestRequestTimeoutGenerate(t *testing.T) {
	srv, stop := hangingServer()
	defer stop()

	c := newTestClient(srv.URL, WithRequestTimeout(100*time.Millisecond))
	start := time.Now()
	_, _, err := genReq(c)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望超时错误, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时错误应可被 errors.Is(context.DeadlineExceeded) 识别, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("错误文案应说明超时, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时未生效: 耗时 %v", elapsed)
	}
}

// TestRequestTimeoutAppliesToAllEntrypoints 请求超时对 Generate、
// GenerateStream、GenerateSingleWithUsage 三个出口同时生效。
func TestRequestTimeoutAppliesToAllEntrypoints(t *testing.T) {
	srv, stop := hangingServer()
	defer stop()

	msgs := []Message{TextMessage(RoleUser, "hello")}
	cases := []struct {
		name string
		call func(c *LLMClient) error
	}{
		{"Generate", func(c *LLMClient) error {
			_, _, err := c.Generate(context.Background(), msgs, ChatOptions{})
			return err
		}},
		{"GenerateStream", func(c *LLMClient) error {
			_, _, err := c.GenerateStream(context.Background(), msgs, ChatOptions{})
			return err
		}},
		{"GenerateSingleWithUsage", func(c *LLMClient) error {
			_, _, err := c.GenerateSingleWithUsage(context.Background(), msgs, ChatOptions{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(srv.URL, WithRequestTimeout(100*time.Millisecond))
			start := time.Now()
			err := tc.call(c)
			if err == nil {
				t.Fatal("期望超时错误, got nil")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("超时错误应可识别, got %v", err)
			}
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("超时未生效: 耗时 %v", elapsed)
			}
		})
	}
}

// TestRequestTimeoutPropagatedToFallback 备用模型客户端透传请求超时：主/备
// 两个上游都挂起时，总耗时仍受单次请求超时约束（防止备用客户端漏配后无限等待）。
func TestRequestTimeoutPropagatedToFallback(t *testing.T) {
	mainSrv, stopMain := hangingServer()
	defer stopMain()
	fbSrv, stopFb := hangingServer()
	defer stopFb()

	c := newTestClient(mainSrv.URL,
		WithRequestTimeout(100*time.Millisecond),
		WithFallback(fbSrv.URL, "fb-key", "fb-model", ""))
	start := time.Now()
	_, _, err := genReq(c)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望错误, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("备用模型超时错误应可识别, got %v", err)
	}
	if !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("应走备用模型后失败, got %v", err)
	}
	// 主 + 备各 100ms，留足余量后仍应远小于 3s（若备用客户端未透传超时，会一直挂起）
	if elapsed > 3*time.Second {
		t.Fatalf("备用客户端未透传请求超时: 耗时 %v", elapsed)
	}
}

// TestGenerateStreamIdleTimeout 流式响应收到首个 chunk 后静默：空闲超时内
// 中断本次请求并返回可识别的超时错误（按可重试错误处理的前提）。
func TestGenerateStreamIdleTimeout(t *testing.T) {
	srv, stop := streamThenHangServer()
	defer stop()

	c := newTestClient(srv.URL, WithStreamIdleTimeout(100*time.Millisecond))
	start := time.Now()
	_, _, err := c.GenerateStream(context.Background(),
		[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望流空闲超时错误, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("空闲超时错误应可识别, got %v", err)
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Fatalf("错误文案应说明流空闲超时, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("空闲超时未生效: 耗时 %v", elapsed)
	}
}

// TestGenerateStreamContinuousNotInterrupted 持续出字的流不应被空闲看门狗
// 中断：chunk 间隔小于 idle 时，即使总耗时接近/超过 idle 也正常完成。
func TestGenerateStreamContinuousNotInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, text := range []string{"你", "好", "！"} {
			fmt.Fprint(w, sseChunk(streamEvent(`[{"index":0,"delta":{"content":"`+text+`"},"finish_reason":null}]`)))
			if flusher != nil {
				flusher.Flush()
			}
			// 间隔远小于 idle 阈值（500ms），模拟模型持续出字
			time.Sleep(30 * time.Millisecond)
		}
		fmt.Fprint(w, sseChunk(usageEvent()))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	// idle 500ms：3 个 chunk 间隔 30ms，总耗时约 90ms，不应触发中断
	c := newTestClient(srv.URL, WithStreamIdleTimeout(500*time.Millisecond))
	resp, _, err := c.GenerateStream(context.Background(),
		[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{})
	if err != nil {
		t.Fatalf("持续出字的流不应被空闲看门狗中断: %v", err)
	}
	if resp.Content != "你好！" {
		t.Fatalf("内容累积不符: %q", resp.Content)
	}
}

// TestGenerateIdleTimeoutAnthropicInternalStream anthropic 的非流式 Generate
// 内部走流式通道聚合：流空闲超时同样生效（上游发一个事件后静默即中断）。
func TestGenerateIdleTimeoutAnthropicInternalStream(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, anthropicStreamEvent("message_start",
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":`+anthropicUsageJSON(5, 1, 0, 0)+`}}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		once.Do(func() { close(release) })
		srv.Close()
	}()

	client, err := NewLLMClient(srv.URL, "test-key", "claude-test",
		WithAPIFormat(APIFormatAnthropic),
		WithStreamIdleTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatalf("NewLLMClient 失败: %v", err)
	}
	start := time.Now()
	_, _, err = client.Generate(context.Background(),
		[]Message{TextMessage(RoleUser, "hello")}, ChatOptions{})
	if err == nil {
		t.Fatal("anthropic 内部流式生成也应受流空闲超时保护")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("空闲超时错误应可识别, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("空闲超时未生效: 耗时 %v", elapsed)
	}
}
