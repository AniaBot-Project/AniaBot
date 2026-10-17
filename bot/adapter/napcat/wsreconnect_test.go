package napcat

import (
	"testing"
	"time"
)

// TestWSReconnectBackoffIsMonotonicAndCapped 重连退避必须随失败次数增长并封顶。
//
// 旧实现只对「Dial 失败」退避：连接建立后立即断开（协议端重启、token 被拒后
// 踢线、反代空连接）走的是 failCount=0 的零退避路径，会形成零间隔重连风暴——
// CPU 空转、每秒数千行日志，同时把对端打满。
func TestWSReconnectBackoffIsMonotonicAndCapped(t *testing.T) {
	prev := time.Duration(0)
	for n := 1; n <= 12; n++ {
		got := wsReconnectBackoff(n)
		if got <= 0 {
			t.Fatalf("failCount=%d 的退避必须为正，got %v", n, got)
		}
		if got > 30*time.Second {
			t.Fatalf("failCount=%d 退避超出上限 30s：%v", n, got)
		}
		if n > 1 && got < prev {
			t.Fatalf("退避应随失败次数单调不减：failCount=%d 得 %v，前一次 %v", n, got, prev)
		}
		prev = got
	}
	// 退避必须真的在增长（而非恒定最小值）
	if wsReconnectBackoff(3) <= wsReconnectBackoff(1) {
		t.Fatalf("退避未指数增长：f1=%v f3=%v", wsReconnectBackoff(1), wsReconnectBackoff(3))
	}
	// 非法计数（0/负数）也要给出正退避，不能退化成 0
	if got := wsReconnectBackoff(0); got <= 0 {
		t.Fatalf("failCount=0 应兜底为最短退避，got %v", got)
	}
}

// TestWSNextFailCountAccumulatesShortLivedConnections 存活过短的连接累计失败次数
// （退避递增），长时间稳定后断开视为一次新故障。
func TestWSNextFailCountAccumulatesShortLivedConnections(t *testing.T) {
	// 立即断开：持续累计
	got := 0
	for i := 0; i < 4; i++ {
		got = wsNextFailCount(got, time.Second)
	}
	if got != 4 {
		t.Fatalf("连续短命连接应累计失败次数，got %d", got)
	}
	if wsReconnectBackoff(got) <= wsReconnectBackoff(1) {
		t.Fatal("累计后退避应明显增大")
	}

	// 长时间稳定后断开：回到一次新故障
	if got := wsNextFailCount(7, wsStableConnDuration+time.Second); got != 1 {
		t.Fatalf("稳定连接断开后应重置为 1，got %d", got)
	}
	// 恰好等于阈值：视为稳定（不退避加重）
	if got := wsNextFailCount(7, wsStableConnDuration); got != 1 {
		t.Fatalf("存活达到阈值应视为稳定，got %d", got)
	}
	// 略短于阈值：仍累计
	if got := wsNextFailCount(7, wsStableConnDuration-time.Millisecond); got != 8 {
		t.Fatalf("存活低于阈值应累计，got %d", got)
	}
}

// TestWSWriteTimeoutBounded 写超时必须为正且不长于请求等待超时，
// 否则对端假死时写操作会长时间占住写锁。
func TestWSWriteTimeoutBounded(t *testing.T) {
	n := &napcatWebSocketAdapter{ackMng: &ackManager{timeout: 10 * time.Second}}
	if got := n.writeTimeout(); got != 10*time.Second {
		t.Fatalf("写超时应取请求超时，got %v", got)
	}
	n2 := &napcatWebSocketAdapter{ackMng: &ackManager{timeout: time.Minute}}
	if got := n2.writeTimeout(); got <= 0 || got > 30*time.Second {
		t.Fatalf("请求超时较长时写超时应封顶，got %v", got)
	}
	// 未初始化 ackMng 也不能返回 0（0 表示无超时，即无限阻塞）
	n3 := &napcatWebSocketAdapter{}
	if got := n3.writeTimeout(); got <= 0 {
		t.Fatalf("ackMng 缺失时应给出正超时，got %v", got)
	}
}

// TestWSReadTimeoutPositive 读 deadline 必须为正：0 表示无超时，对端假死时
// ReadMessage 会永久阻塞且状态仍显示 connected。
func TestWSReadTimeoutPositive(t *testing.T) {
	if wsReadTimeout <= 0 {
		t.Fatal("读 deadline 必须为正")
	}
	if wsReadTimeout < 30*time.Second {
		t.Fatalf("读 deadline 过短会让空闲连接反复重连：%v", wsReadTimeout)
	}
}
