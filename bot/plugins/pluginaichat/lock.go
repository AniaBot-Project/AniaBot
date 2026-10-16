package pluginaichat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
)

// lockRenewInterval 会话锁续期心跳间隔：取 TTL 的 1/3，一次续期失败仍有
// 两次机会在过期前补救。
const lockRenewInterval = LockExpTime / 3

// renewMissTolerance 续期连续未确认成功的容忍次数：键缺失与存储瞬时故障
// 在存储接口上无法区分，连续两次失败（间隔 2×lockRenewInterval）才判定
// 锁已丢失，避免一次抖动误杀正在进行的响应。
const renewMissTolerance = 2

// lockFail 会话锁获取失败原因。区分「会话正忙」与「并发槽位已满」：
// 前者消息应进入排队队列（锁持有者的 drain 循环会消费），后者没有
// 任何消费者（会话本身空闲，缺的是全局并发配额），排队只会沉淀——
// 调用方应直接拒绝并提示用户重试。
type lockFail int

const (
	lockOK lockFail = iota
	lockBusy
	lockRateLimited
)

// sessionLock 一次成功获取的会话锁句柄：
//   - 后台心跳周期性把 TTL 续回 LockExpTime。AI 响应耗时可配置到远超
//     LockExpTime（工具审批、子代理、多轮工具调用），若锁中途过期，后续
//     消息会拿到锁并与当前响应并发驱动同一个 ChatBot（messageWindow 等状态
//     非并发安全，会产生数据竞争与历史错乱）；
//   - 锁值携带随机令牌，release 时仅在令牌仍匹配时删除——即使锁意外过期被
//     他人接管，先到响应的 release 也不会误删新持有者的锁；
//   - 心跳确认锁丢失（令牌被换或键消失）时取消附着其上的请求（attachCancel）：
//     宁可让在跑请求以「已停止」收尾，也不让它与新持锁者并发驱动同一会话。
type sessionLock struct {
	p           *AIChatPlugin
	key         string
	token       string
	stopCh      chan struct{}
	stopped     chan struct{}
	releaseOnce sync.Once

	// cancelMu 保护 cancel：请求侧在每批处理开始前 attach，心跳线程并发读取
	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// tryLockDetailed 尝试获取会话锁：成功返回锁句柄（后台自动续期），失败返回原因。
// 同时占用一个并发槽位（rateCh），与并发限制共用语义。
func (p *AIChatPlugin) tryLockDetailed(id message.QID, isGroup bool) (*sessionLock, lockFail) {
	select {
	case p.rateCh <- struct{}{}:
	default:
		return nil, lockRateLimited
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	token := newLockToken()
	locked := p.lockStorage.SetString(ctx, sessionKey(id, isGroup), token, storage.WithCheckExist(), storage.WithTTL(LockExpTime))
	if !locked {
		select {
		case <-p.rateCh:
		default:
		}
		return nil, lockBusy
	}
	l := &sessionLock{
		p:       p,
		key:     sessionKey(id, isGroup),
		token:   token,
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go l.renewLoop()
	return l, lockOK
}

// tryLock 兼容包装：不关心失败原因（会话正忙 / 并发槽位已满）的调用方使用。
func (p *AIChatPlugin) tryLock(id message.QID, isGroup bool) *sessionLock {
	lock, _ := p.tryLockDetailed(id, isGroup)
	return lock
}

// release 释放锁（幂等，可安全 defer）：先停心跳并等其退出（否则删除后
// 心跳可能把 TTL 续回去，锁「复活」阻塞后续消息），再在令牌仍匹配时删除，
// 最后归还并发槽位。
//
// 删键先于归还槽位：反序会产生「槽位已空、锁键仍在」的窗口，窗口内到达的
// 消息抢到槽位却拿不到锁，被当作「会话正忙」排队，而此刻已无持锁者消费
// 队列；正序下同样存在「锁键已删、槽位未还」的窗口，但那种情况的失败原因是
// 并发槽位已满（直接拒绝、用户可重试，且有 kickPending 兜底唤醒），
// 不会静默沉淀消息。GetString 与 Del 之间的检查窗口（存储接口未暴露原子
// compare-and-delete）相比无条件删除已消除「误删他人锁」的实际风险。
func (l *sessionLock) release() {
	l.releaseOnce.Do(func() {
		close(l.stopCh)
		<-l.stopped
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		if cur, ok := l.p.lockStorage.GetString(ctx, l.key); ok && cur == l.token {
			l.p.lockStorage.Del(ctx, l.key)
		}
		select {
		case <-l.p.rateCh:
		default:
		}
	})
}

// attachCancel 附着当前请求的取消函数（锁确认丢失时由心跳调用）。
func (l *sessionLock) attachCancel(cancel context.CancelFunc) {
	l.cancelMu.Lock()
	l.cancel = cancel
	l.cancelMu.Unlock()
}

// renewResult 一次续期尝试的结果
type renewResult int

const (
	renewOK   renewResult = iota // 令牌匹配且 TTL 已续回
	renewMiss                    // 键缺失或存储报错：无法区分，计入容忍次数
	renewLost                    // 键存在但令牌已换：锁被他人接管，确定丢失
)

// renewLoop 心跳续期：锁持有期间周期性把 TTL 重置回 LockExpTime。
func (l *sessionLock) renewLoop() {
	defer close(l.stopped)
	ticker := time.NewTicker(lockRenewInterval)
	defer ticker.Stop()
	misses := 0
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			switch l.renewOnce() {
			case renewOK:
				misses = 0
			case renewLost:
				l.lostCancel()
				return
			default: // renewMiss
				misses++
				if misses >= renewMissTolerance {
					l.lostCancel()
					return
				}
			}
		}
	}
}

// renewOnce 执行一次续期尝试。
func (l *sessionLock) renewOnce() renewResult {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	cur, ok := l.p.lockStorage.GetString(ctx, l.key)
	if !ok {
		return renewMiss
	}
	if cur != l.token {
		return renewLost
	}
	if !l.p.lockStorage.Expire(ctx, l.key, LockExpTime) {
		return renewMiss
	}
	return renewOK
}

// lostCancel 锁确认丢失：记错误日志并取消附着的请求。
func (l *sessionLock) lostCancel() {
	l.p.Logger.Error("会话锁续期失败，判定锁已丢失，取消当前请求", "key", l.key)
	l.cancelMu.Lock()
	cancel := l.cancel
	l.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// newLockToken 生成锁持有者随机令牌
func newLockToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极罕见，退化用纳秒时间戳（配合 CheckExist 仍可工作）
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}
