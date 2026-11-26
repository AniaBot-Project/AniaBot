package pluginaichat

import (
	"sync"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// maxPendingMessages 每个会话的排队消息上限，防止 AI 响应期间消息无限堆积
const maxPendingMessages = 20

// pendingQueue 单会话的待处理消息队列，存放 AI 响应期间到达的消息。
// 与旧行为（整轮结束后才消费）不同，运行中的回合会在每次 LLM 请求前经
// ChatOptions.Steer 排空并作为插话注入（见 interject.go）；排空后剩余的消息
// 由运行器在本批结束后作为下一批处理。两条消费路径互斥，同一消息只会被其中
// 一条消费（不会重复处理）；/stop、取消与错误路径维持旧行为清空队列。
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

// messageTarget 返回一条消息所属的会话目标与是否群聊：群聊取 GroupId，
// 私聊取发送者。群聊与否一律以 message_type 为准（见 message.Message.IsGroup）：
// 微信、飞书、Telegram 等平台的私聊消息同样带 GroupId，按「GroupId 非空」推断
// 会把私聊当群聊，回复走 SendGroupMsg，面板「消息日志」随之把同一会话拆成
// 群聊 + 私聊两条，会话级状态（计划模式、主动互动、定时任务）也会记错键。
func messageTarget(msg message.Message) (message.QID, bool) {
	if msg.IsGroup() {
		return msg.GroupId, true
	}
	return msg.Sender.UserId, false
}

func (p *AIChatPlugin) getPendingQueue(id message.QID, isGroup bool) *pendingQueue {
	q, _ := p.pendingMsgs.LoadOrStore(sessionKey(id, isGroup), &pendingQueue{})
	return q.(*pendingQueue)
}

// enqueuePending 将消息加入排队队列（静默排队，调用方不据此向用户发提示）。
// first 表示该消息是队列中的第一条（调用方仅用于日志区分），
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

// prependPending 把一批消息放回队列头部（保持传入顺序），用于把当前不能消费的
// 消息暂缓到下一批：运行中插话遇到 #新对话 时，清空历史是批次级语义（在途请求
// 仍带着旧历史，不能中途清），必须把 #新对话 及其之后的消息留到下一批开始前处理。
// 容量按软上限处理（暂缓消息来自刚 drain 的同一批，允许与并发入队短暂叠加）。
func (p *AIChatPlugin) prependPending(id message.QID, isGroup bool, msgs []message.Message) {
	if len(msgs) == 0 {
		return
	}
	q := p.getPendingQueue(id, isGroup)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(append([]message.Message(nil), msgs...), q.items...)
}

// pendingCount 只读统计会话排队消息数（不创建队列，供 /status 展示）。
func (p *AIChatPlugin) pendingCount(key string) int {
	v, ok := p.pendingMsgs.Load(key)
	if !ok {
		return 0
	}
	q, ok := v.(*pendingQueue)
	if !ok {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
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
