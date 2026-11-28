package core

import (
	"context"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/workstate"
)

// broadcastNotice 向声明支持该平台的插件广播一条通知事件（不可中断）。
func broadcastNotice[T any](ania *AniaBot, e *adapterEntry, tag string, notice T,
	fn func(p plugin.Plugin, ctx context.Context, b bot.Bot, n T) error) {
	// 事件幂等去重：仅当适配器提供稳定键时去重（不做组合兜底，防误伤）
	if key, ok := ania.noticeDedupKey(e, notice); ok && !ania.tryClaimEvent(key) {
		return
	}
	// 登记为进行中的工作：重启前等待通知事件在插件链中处理完
	endWork := workstate.Begin(workstate.KindNotice, tag)
	defer endWork()
	ctx := context.Background()
	for _, p := range ania.plugins {
		if !ania.supportsPlatform(p, e.def.Platform) {
			continue
		}
		safeExecute(tag, p, func(p plugin.Plugin) {
			noticeCtx, cancel := context.WithTimeout(ctx, NoticeEventTimeout)
			err := fn(p, noticeCtx, e.evBot, notice)
			logError(err, p, tag)
			cancel()
		})
	}
}

func (ania *AniaBot) onGroupUploadEvent(e *adapterEntry, notice message.GroupUploadNotice) {
	broadcastNotice(ania, e, "群文件上传事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupUploadNotice) error {
		return p.OnGroupUpload(ctx, b, n)
	})
}

func (ania *AniaBot) onGroupAdminEvent(e *adapterEntry, notice message.GroupAdminNotice) {
	broadcastNotice(ania, e, "群管理员变动事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupAdminNotice) error {
		return p.OnGroupAdmin(ctx, b, n)
	})
}

func (ania *AniaBot) onGroupDecreaseEvent(e *adapterEntry, notice message.GroupDecreaseNotice) {
	broadcastNotice(ania, e, "群成员减少事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupDecreaseNotice) error {
		return p.OnGroupDecrease(ctx, b, n)
	})
}

func (ania *AniaBot) onGroupIncreaseEvent(e *adapterEntry, notice message.GroupIncreaseNotice) {
	broadcastNotice(ania, e, "群成员增加事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupIncreaseNotice) error {
		return p.OnGroupIncrease(ctx, b, n)
	})
}

func (ania *AniaBot) onGroupBanEvent(e *adapterEntry, notice message.GroupBanNotice) {
	broadcastNotice(ania, e, "群禁言事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupBanNotice) error {
		return p.OnGroupBan(ctx, b, n)
	})
}

func (ania *AniaBot) onFriendAddEvent(e *adapterEntry, notice message.FriendAddNotice) {
	broadcastNotice(ania, e, "新添加好友事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.FriendAddNotice) error {
		return p.OnFriendAdd(ctx, b, n)
	})
}

func (ania *AniaBot) onGroupRecallEvent(e *adapterEntry, notice message.GroupRecallNotice) {
	broadcastNotice(ania, e, "群消息撤回事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupRecallNotice) error {
		return p.OnGroupRecall(ctx, b, n)
	})
}

func (ania *AniaBot) onFriendRecallEvent(e *adapterEntry, notice message.FriendRecallNotice) {
	broadcastNotice(ania, e, "好友消息撤回事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.FriendRecallNotice) error {
		return p.OnFriendRecall(ctx, b, n)
	})
}

func (ania *AniaBot) onPokeEvent(e *adapterEntry, notice message.PokeNotice) {
	broadcastNotice(ania, e, "戳一戳事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.PokeNotice) error {
		return p.OnPoke(ctx, b, n)
	})
}

func (ania *AniaBot) onLuckyKingEvent(e *adapterEntry, notice message.LuckyKingNotice) {
	broadcastNotice(ania, e, "运气王事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.LuckyKingNotice) error {
		return p.OnLuckyKing(ctx, b, n)
	})
}

func (ania *AniaBot) onHonorEvent(e *adapterEntry, notice message.HonorNotice) {
	broadcastNotice(ania, e, "荣誉变更事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.HonorNotice) error {
		return p.OnHonor(ctx, b, n)
	})
}

func (ania *AniaBot) onGroupMsgEmojiLikeEvent(e *adapterEntry, notice message.GroupMsgEmojiLikeNotice) {
	broadcastNotice(ania, e, "群表情回应事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupMsgEmojiLikeNotice) error {
		return p.OnGroupMsgEmojiLike(ctx, b, n)
	})
}

func (ania *AniaBot) onEssenceEvent(e *adapterEntry, notice message.EssenceNotice) {
	broadcastNotice(ania, e, "群精华事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.EssenceNotice) error {
		return p.OnEssence(ctx, b, n)
	})
}

func (ania *AniaBot) onGroupCardEvent(e *adapterEntry, notice message.GroupCardNotice) {
	broadcastNotice(ania, e, "群名片变更事件", notice, func(p plugin.Plugin, ctx context.Context, b bot.Bot, n message.GroupCardNotice) error {
		return p.OnGroupCard(ctx, b, n)
	})
}

// onPlatformEvent 平台特定事件广播（如飞书卡片回调、机器人入群等）。
func (ania *AniaBot) onPlatformEvent(e *adapterEntry, event message.PlatformEvent) {
	// 登记为进行中的工作：重启前等待事件在插件链中处理完
	endWork := workstate.Begin(workstate.KindPlatform, "平台事件")
	defer endWork()
	ctx := context.Background()
	for _, p := range ania.plugins {
		if !ania.supportsPlatform(p, e.def.Platform) {
			continue
		}
		safeExecute("平台特定事件", p, func(p plugin.Plugin) {
			h, ok := p.(plugin.PlatformEventHandler)
			if !ok {
				return
			}
			eventCtx, cancel := context.WithTimeout(ctx, NoticeEventTimeout)
			err := h.OnPlatformEvent(eventCtx, e.evBot, event)
			logError(err, p, "平台特定事件")
			cancel()
		})
	}
}
