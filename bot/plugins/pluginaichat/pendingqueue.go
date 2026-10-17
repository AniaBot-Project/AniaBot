package pluginaichat

import (
	"sync"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// maxPendingMessages 每个会话的排队消息上限，防止 AI 响应期间消息无限堆积
const maxPendingMessages = 20

// pendingQueue 单会话的排队消息队列，存放 AI 响应期间到达的消息
type pendingQueue struct {
	mu    sync.Mutex
	items []message.Message
}

// sessionKey 群聊与好友的 QID 数值空间可能重叠，加 g:/f: 前缀区分会话。
// 所有会话级状态（排队队列、ChatBot 实例、活跃上下文、并发锁、持久化历史）
// 统一以该键索引，避免同号群聊与私聊互串。
func sessionKey(id message.QID, isGroup bool) string {
	if isGroup {
		return "g:" + id.String()
	}
	return "f:" + id.String()
}

func (p *AIChatPlugin) getPendingQueue(id message.QID, isGroup bool) *pendingQueue {
	q, _ := p.pendingMsgs.LoadOrStore(sessionKey(id, isGroup), &pendingQueue{})
	return q.(*pendingQueue)
}

// enqueuePending 将消息加入排队队列。
// first 表示该消息是队列中的第一条（调用方可据此给用户一次性提示），
// ok 为 false 表示队列已满，消息被丢弃。
func (p *AIChatPlugin) enqueuePending(id message.QID, isGroup bool, msg message.Message) (first, ok bool) {
	q := p.getPendingQueue(id, isGroup)
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= maxPendingMessages {
		return false, false
	}
	first = len(q.items) == 0
	q.items = append(q.items, msg)
	return first, true
}

// drainPending 取出并清空当前所有排队消息
func (p *AIChatPlugin) drainPending(id message.QID, isGroup bool) []message.Message {
	q := p.getPendingQueue(id, isGroup)
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}

// isEmpty 当前队列是否为空（加锁读取，供淘汰路径判断）。
func (q *pendingQueue) isEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) == 0
}

// reclaimSessionQueue 在会话淘汰时回收旁挂状态（队列、待注入上下文、未@计数标记）。
// 队列非空时不删（可能有待消费消息，交由正常流程处理）；这些 sync.Map 若不回收
// 会随历史会话数无界增长，且清扫器每 10s 都要 Range 全表扫描空条目。
func (p *AIChatPlugin) reclaimSessionQueue(key string) {
	if v, ok := p.pendingMsgs.Load(key); ok {
		if q, isQueue := v.(*pendingQueue); isQueue && q.isEmpty() {
			p.pendingMsgs.CompareAndDelete(key, q)
		}
	}
	p.sessionInject.Delete(key)
	// 未@计数标记一并复位：淘汰后重新加载会从持久层恢复，语义不变
	p.noMentionLoaded.Delete(key)
}
