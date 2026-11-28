package workstate

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBeginEndCounts(t *testing.T) {
	if got := Snapshot().Count; got != 0 {
		t.Fatalf("初始应为空闲, got %d", got)
	}
	end1 := Begin(KindMessage, "群消息 qq:1")
	end2 := Begin(KindTurn, "AI 对话 g:qq:1")
	snap := Snapshot()
	if snap.Count != 2 || len(snap.Items) != 2 {
		t.Fatalf("登记后应有 2 个进行中任务: %+v", snap)
	}
	if snap.Items[0].Kind != KindMessage || snap.Items[1].Kind != KindTurn {
		t.Fatalf("快照顺序应为登记顺序: %+v", snap.Items)
	}
	end1()
	end1() // 幂等
	if got := Snapshot().Count; got != 1 {
		t.Fatalf("结束后应剩 1 个, got %d", got)
	}
	end2()
	if got := Snapshot().Count; got != 0 {
		t.Fatalf("全部结束后应为空闲, got %d", got)
	}
}

func TestWaitIdleImmediate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !WaitIdle(ctx) {
		t.Fatal("空闲时应立即返回 true")
	}
}

func TestWaitIdleBlocksUntilEnd(t *testing.T) {
	end := Begin(KindSubagent, "子代理 测试")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan bool, 1)
	go func() { done <- WaitIdle(ctx) }()

	select {
	case <-done:
		t.Fatal("有进行中任务时 WaitIdle 不应返回")
	case <-time.After(100 * time.Millisecond):
	}

	end()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("任务结束后 WaitIdle 应返回 true")
		}
	case <-time.After(time.Second):
		t.Fatal("任务结束后 WaitIdle 未返回")
	}
}

func TestWaitIdleContextDone(t *testing.T) {
	end := Begin(KindClock, "定时任务 测试")
	defer end()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if WaitIdle(ctx) {
		t.Fatal("ctx 超时时仍应返回 false")
	}
}

func TestConcurrentBeginEnd(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			end := Begin(KindMessage, "并发消息")
			_ = Snapshot()
			end()
		}()
	}
	wg.Wait()
	if got := Snapshot().Count; got != 0 {
		t.Fatalf("并发结束后应为空闲, got %d", got)
	}
}
