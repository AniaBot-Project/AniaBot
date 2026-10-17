package core

import (
	"context"
	"encoding/base64"
	"reflect"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
)

// 核心事件幂等去重：事件订阅为 at-least-once 投递的平台（如飞书断线重连/ACK 丢失会重推）
// 由 core 统一按去重键去重，避免每个适配器各自手写。适配器可经 adapter.EventKeyer
// 提供稳定键（优先）；未实现时消息走「平台 + MessageId」兜底，通知不做兜底。
//
// 飞书适配器自身的早期去重（在 SDK 事件处理器内、省图片下载）保留，
// 与 core 层去重互不冲突（键命名空间不同）。

const (
	coreDedupNamespace = "core_event_dedup"
	// eventDedupTTL 去重键保留时长：飞书断线重推延迟以分钟计，10 分钟足够；
	// 有界（内存 map / redis TTL 自动过期），不会无限增长。
	eventDedupTTL = 10 * time.Minute
)

// eventDedup 返回 core 专属去重存储（懒初始化）。
// storage 在 Run() 中初始化、适配器 Serve 之后事件才流入，此处时序安全。
func (ania *AniaBot) eventDedup() storage.Storage {
	ns := base64.StdEncoding.EncodeToString([]byte(coreDedupNamespace))
	return ania.storage.Clone(ns)
}

// tryClaimEvent 尝试占用去重键：首次占用成功返回 true，重复投递返回 false。
// 内存后端在共享 mutex 内原子 check-and-set；redis 后端为 SET NX EX，多实例共享。
//
// SetString 返回 false 有两种情况：键已存在（真重复）或存储故障（如 redis 断连）。
// 后者若直接丢弃会变成 fail-closed：redis 故障期间所有消息被静默吞掉。
// 这里用 GetString 复核：键存在→真重复，丢弃；键不存在→存储故障，fail-open 放行。
// at-least-once 语义下宁可重复处理一次，也不能静默丢消息。
func (ania *AniaBot) tryClaimEvent(key string) bool {
	dedup := ania.eventDedup()
	ctx := context.Background()
	if dedup.SetString(ctx, key, "1", storage.WithTTL(eventDedupTTL), storage.WithCheckExist()) {
		return true
	}
	if _, exists := dedup.GetString(ctx, key); exists {
		return false
	}
	ania.logger.Warn("事件去重存储故障，fail-open 放行（可能重复处理）", "key", key)
	return true
}

// messageDedupKey 计算消息去重键：优先适配器 EventKeyer，兜底「平台 + MessageId」。
// 无稳定 ID 时返回 false（放行）。
func (ania *AniaBot) messageDedupKey(e *adapterEntry, msg message.Message) (string, bool) {
	if ek, ok := e.adapter.(adapter.EventKeyer); ok {
		if key, ok := ek.MessageKey(msg); ok {
			return key, true
		}
	}
	// 兜底：OneBot message_id 全局唯一；用 entry 的平台（NapCat 消息的 msg.Platform 为空）
	if msg.MessageId != "" {
		return e.def.Platform + ":" + msg.MessageId.String(), true
	}
	return "", false
}

// noticeDedupKey 计算通知去重键：仅走适配器 EventKeyer.NoticeKey，
// 不做组合兜底（避免把同一秒内两次真实事件误判为重复投递）。
func (ania *AniaBot) noticeDedupKey(e *adapterEntry, notice any) (string, bool) {
	ek, ok := e.adapter.(adapter.EventKeyer)
	if !ok {
		return "", false
	}
	return ek.NoticeKey(noticeTypeOf(notice), notice)
}

// noticeTypeOf 从通知结构取 NoticeType 字段值（各 *Notice 都内嵌 BasicNotice）。
//
// 不能靠 `notice.(interface{ GetNoticeType() string })` 断言：GetNoticeType 定义在
// *BasicNotice 上（指针接收者），而 broadcastNotice 收到的是**值**（如
// message.GroupUploadNotice），值类型的方法集不含该方法，断言恒失败、结果恒为空串。
// 这里用反射读取内嵌结构体的字段，值/指针两种形态都能取到，且不引入额外依赖。
func noticeTypeOf(notice any) string {
	if notice == nil {
		return ""
	}
	v := reflect.ValueOf(notice)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	// 先看当前结构体自身，再看内嵌的 BasicNotice（各 Notice 都内嵌它）
	if f := v.FieldByName("NoticeType"); f.IsValid() && f.Kind() == reflect.String {
		return f.String()
	}
	basic := v.FieldByName("BasicNotice")
	if !basic.IsValid() {
		return ""
	}
	if basic.Kind() == reflect.Pointer {
		if basic.IsNil() {
			return ""
		}
		basic = basic.Elem()
	}
	if f := basic.FieldByName("NoticeType"); f.IsValid() && f.Kind() == reflect.String {
		return f.String()
	}
	return ""
}
