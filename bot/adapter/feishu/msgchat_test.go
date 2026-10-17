package feishu

import (
	"fmt"
	"testing"
)

// TestMsgChatCacheBounded msgChats 反查缓存必须有界：它由每条入站消息填充，
// 旧实现用裸 sync.Map 且从不清理，长跑 Bot 内存随累计收信量线性增长。
func TestMsgChatCacheBounded(t *testing.T) {
	c := newMsgChatCache()
	// 写入远超容量的条目
	total := msgChatCacheCap * 3
	for i := 0; i < total; i++ {
		c.store(fmt.Sprintf("om_%d", i), fmt.Sprintf("oc_%d", i%7))
	}
	if got := c.len(); got > msgChatCacheCap {
		t.Fatalf("缓存条目 %d 超过上限 %d（无界增长）", got, msgChatCacheCap)
	}
	if got := c.len(); got != msgChatCacheCap {
		t.Fatalf("缓存应保持在上限 %d，实际 %d", msgChatCacheCap, got)
	}

	// 最近写入的条目仍可反查
	latest := fmt.Sprintf("om_%d", total-1)
	chatID, ok := c.load(latest)
	if !ok || chatID != fmt.Sprintf("oc_%d", (total-1)%7) {
		t.Fatalf("最近写入的条目应可反查, ok=%v chatID=%q", ok, chatID)
	}
	// 最旧的条目已被淘汰
	if _, ok := c.load("om_0"); ok {
		t.Fatal("超出容量的最旧条目应被淘汰")
	}
}

// TestMsgChatCacheOverwriteDoesNotGrow 同一条消息重复写入不应重复占位，
// 否则同一消息的多次投递会加速淘汰其它条目。
func TestMsgChatCacheOverwriteDoesNotGrow(t *testing.T) {
	c := newMsgChatCache()
	for i := 0; i < 10; i++ {
		c.store("om_dup", "oc_1")
	}
	if got := c.len(); got != 1 {
		t.Fatalf("重复写入同一消息应只占 1 条，实际 %d", got)
	}
	// 覆盖后取到最新值
	c.store("om_dup", "oc_2")
	if chatID, ok := c.load("om_dup"); !ok || chatID != "oc_2" {
		t.Fatalf("覆盖写入应更新映射, ok=%v chatID=%q", ok, chatID)
	}
	if got := c.len(); got != 1 {
		t.Fatalf("覆盖不应增加条目，实际 %d", got)
	}
}

// TestMsgChatCacheEvictionKeepsRecent 淘汰顺序为 FIFO：保留最近的 N 条。
func TestMsgChatCacheEvictionKeepsRecent(t *testing.T) {
	c := newMsgChatCache()
	// 先填满
	for i := 0; i < msgChatCacheCap; i++ {
		c.store(fmt.Sprintf("om_%d", i), "oc_x")
	}
	// 再写一条：最早的 om_0 应被淘汰，其余保留
	c.store("om_new", "oc_y")
	if _, ok := c.load("om_0"); ok {
		t.Fatal("写入新条目后最旧条目应被淘汰")
	}
	if _, ok := c.load("om_1"); !ok {
		t.Fatal("仅应淘汰最旧一条")
	}
	if _, ok := c.load("om_new"); !ok {
		t.Fatal("新写入条目应存在")
	}
}

// TestAdapterUsesBoundedMsgChats 适配器初始化时必须构造有界缓存（防止回退到裸 map）。
func TestAdapterUsesBoundedMsgChats(t *testing.T) {
	a := NewAdapter(nil)
	if a.msgChats == nil {
		t.Fatal("适配器必须初始化 msgChats 有界缓存")
	}
	a.msgChats.store("om_a", "oc_a")
	if chatID, ok := a.msgChats.load("om_a"); !ok || chatID != "oc_a" {
		t.Fatalf("适配器缓存读写不一致: ok=%v chatID=%q", ok, chatID)
	}
}
