package core

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/workstate"
)

// onInteraction 内联按钮点击回调路由：回调数据约定为「插件名:载荷」
// （插件发送 keyboard 段时经 Meta.CallbackData 打包），按首个 ':' 前的
// 插件名找到目标插件，实现 plugin.InteractionHandler 时回调之并把载荷
// 填入 ev.Data。无论路由成败（回调无主/插件已卸载/会话过期）都在
// 插件返回后统一应答平台，消除客户端等待转圈；AnswerText 由插件填写。
func (ania *AniaBot) onInteraction(e *adapterEntry, ev message.InteractionEvent) {
	defer func() {
		if err := recover(); err != nil {
			Logger().Error("按钮点击事件触发错误: ", err)
		}
	}()
	// 登记为进行中的工作：重启前等待按钮回调在插件中处理完
	endWork := workstate.Begin(workstate.KindPlatform, "按钮点击事件")
	defer endWork()
	if pluginName, payload, ok := strings.Cut(ev.Data, ":"); ok {
		ev.Data = payload
		for _, p := range ania.plugins {
			if p.GetMeta().Name != pluginName {
				continue
			}
			if h, ok := p.(plugin.InteractionHandler); ok {
				safeExecute("按钮点击事件", p, func(p plugin.Plugin) {
					ictx, cancel := context.WithTimeout(ania.ctx, NoticeEventTimeout)
					err := h.OnInteraction(ictx, e.evBot, &ev)
					logError(err, p, "按钮点击事件")
					cancel()
				})
			}
			break
		}
	}
	ania.answerInteraction(e, &ev)
}

// answerInteraction 应答一次按钮点击（callbackId 为空或平台无应答能力时跳过）。
func (ania *AniaBot) answerInteraction(e *adapterEntry, ev *message.InteractionEvent) {
	if ev.CallbackId == "" {
		return
	}
	if aa, ok := e.adapter.(adapter.InteractionAnswerer); ok {
		aa.AnswerInteraction(ev.CallbackId, ev.AnswerText)
	}
}

// stripUnsupportedKeyboard 平台不支持内联按钮（未实现 adapter.InteractiveExt
// 或声明不支持）时剥离链中的 keyboard 段，避免未知段类型透传给适配器导致
// 发送失败；剥离计数节流告警，提示插件应先断言 bot.Interactive 探测。
func (ania *AniaBot) stripUnsupportedKeyboard(a adapter.Adapter, segs []message.OB11Segment) []message.OB11Segment {
	has := false
	for _, s := range segs {
		if s.Type == message.SegmentKeyboard {
			has = true
			break
		}
	}
	if !has {
		return segs
	}
	if iv, ok := a.(adapter.InteractiveExt); ok && iv.SupportsKeyboard() {
		return segs
	}
	key := a.Platform() + "|kbd_strip"
	v, _ := segWarn.LoadOrStore(key, &atomic.Int64{})
	n := v.(*atomic.Int64).Add(1)
	if n == 1 || n%100 == 0 {
		Logger().Warn("平台不支持内联按钮，出站 keyboard 段已剥离（插件可断言 bot.Interactive 探测后降级）",
			"platform", a.Platform(), "times", n)
	}
	return message.StripKeyboardSegments(segs)
}
