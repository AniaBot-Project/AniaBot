package pluginaichat

import (
	"cmp"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// chatJanitorInterval 会话回收的轮询周期。会话条数有限（受 max_sessions 约束），
// 每分钟扫一遍的开销可忽略。
const chatJanitorInterval = time.Minute

// chatEntry 会话缓存条目：ChatBot 实例 + 淘汰所需的元数据。
//
// p.chats 中的会话只增不减会导致内存随活跃会话数线性增长；条目携带
// lastActive 后，janitor 可按闲置时长与容量上限淘汰。淘汰只丢弃内存对象
// （持久化历史保留在存储层），下次发言时 getChat 自动重建并回放历史，
// 代价为一次读取；已知副作用是会话内 mcp_load 动态加载的工具随之失效
// （行为等同进程重启）。
type chatEntry struct {
	chat    *aichat.ChatBot
	id      message.QID
	isGroup bool
	// prompt 创建/最近一次更新时应用的覆盖基础提示词（不含场景描述），
	// 用于检测面板修改 Prompt 覆盖后同步驻留会话（见 getChat）
	prompt string
	// lastActive 最近一次 AI 交互（收到指向 AI 的消息 / 创建会话）的 unix 秒
	lastActive atomic.Int64
}

func newChatEntry(chat *aichat.ChatBot, id message.QID, isGroup bool, prompt string) *chatEntry {
	e := &chatEntry{chat: chat, id: id, isGroup: isGroup, prompt: prompt}
	e.lastActive.Store(time.Now().Unix())
	return e
}

// touchChat 刷新会话活跃时间（闲置淘汰的依据）。群聊的未 @ 消息不刷新——
// 只统计指向 AI 的交互，否则繁忙群里的死会话永远不会被闲置淘汰。
func (p *AIChatPlugin) touchChat(key string) {
	if v, ok := p.chats.Load(key); ok {
		if e, ok2 := v.(*chatEntry); ok2 {
			e.lastActive.Store(time.Now().Unix())
		}
	}
}

// hasPending 判断会话是否有排队消息（只读探测，不创建队列）。
func (p *AIChatPlugin) hasPending(key string) bool {
	v, ok := p.pendingMsgs.Load(key)
	if !ok {
		return false
	}
	q := v.(*pendingQueue)
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) > 0
}

// startChatJanitor 启动会话回收 goroutine。插件无 Stop 钩子，goroutine 与
// 进程同生命周期（与 clockManager 自带 cron 的惯例一致）。
func (p *AIChatPlugin) startChatJanitor(maxIdle time.Duration, maxSessions int) {
	go func() {
		ticker := time.NewTicker(chatJanitorInterval)
		defer ticker.Stop()
		for range ticker.C {
			p.evictChats(maxIdle, maxSessions)
		}
	}()
}

// evictChats 执行一轮淘汰：先按闲置时长过滤，再按 LRU 淘汰超出容量的
// 最久未活跃会话。maxIdle/maxSessions <=0 时分别禁用对应策略。
func (p *AIChatPlugin) evictChats(maxIdle time.Duration, maxSessions int) {
	type kv struct {
		key string
		e   *chatEntry
	}
	var all []kv
	p.chats.Range(func(key, value any) bool {
		k, ok1 := key.(string)
		e, ok2 := value.(*chatEntry)
		if ok1 && ok2 {
			all = append(all, kv{k, e})
		}
		return true
	})
	if len(all) == 0 {
		return
	}

	// 闲置淘汰
	if maxIdle > 0 {
		idleSec := int64(maxIdle.Seconds())
		now := time.Now().Unix()
		for _, it := range all {
			if now-it.e.lastActive.Load() > idleSec {
				p.evictOneChat(it.key, it.e)
			}
		}
	}

	// 容量溢出：淘汰最久未活跃的若干条（刚被闲置淘汰的条目 CompareAndDelete 幂等跳过）
	if maxSessions > 0 && len(all) > maxSessions {
		slices.SortFunc(all, func(a, b kv) int {
			return cmp.Compare(a.e.lastActive.Load(), b.e.lastActive.Load())
		})
		for _, it := range all[:len(all)-maxSessions] {
			p.evictOneChat(it.key, it.e)
		}
	}
}

// evictOneChat 淘汰单个会话条目，仅删除内存对象，不动持久化历史。
//
// 响应中的会话持有会话锁，tryLock 必然失败——直接跳过；拿到锁的短暂窗口内
// 到达的新消息只能进排队队列或刷新活跃时间，故拿锁后复检二者均变化才删除。
// tryLock 会瞬时占用一个并发槽位，可忽略。
func (p *AIChatPlugin) evictOneChat(key string, e *chatEntry) {
	lastSeen := e.lastActive.Load()
	lock := p.tryLock(e.id, e.isGroup)
	if lock == nil {
		return
	}
	defer lock.release()
	if e.lastActive.Load() != lastSeen || p.hasPending(key) {
		return
	}
	if p.chats.CompareAndDelete(key, e) {
		// 会话级内存状态一并复位：计划模式与任务清单都声明为「会话淘汰后自动
		// 清空」（见 planManager/todoManager 注释），且淘汰意味着该会话已无排队
		// 消息与在跑请求，复位不会影响进行中的工作流；平台外观引用同时释放。
		// 淘汰后该会话若再收到消息，getChat 重建并重新登记外观。
		if p.planManager != nil {
			p.planManager.Set(key, false)
		}
		if p.todoManager != nil {
			p.todoManager.clear(key)
		}
		p.sessionBots.Delete(key)
		// 旁挂状态一并回收（队列为空才删；未@计数标记复位后由下次加载从持久层恢复）
		p.reclaimSessionQueue(key)
		p.Logger.Info("淘汰闲置会话", "key", key)
	}
}
