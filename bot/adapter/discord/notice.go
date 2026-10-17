package discord

import (
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// ---------- 通知事件映射 ----------

// recallAuditLogDelay 等待删除事件对应审计日志落库的时长：Discord 审计日志有传播
// 延迟，立即查询大概率缺条目（会把管理删除误判为自删）。var 便于测试注入零延迟。
var recallAuditLogDelay = 2 * time.Second

// recallAuditMatchWindow 审计条目匹配窗口：仅采纳窗口内的条目，避免同一作者同频道
// 的历史删除条目张冠李戴（审计条目不含消息 ID，只能按作者+频道+时近匹配）。
const recallAuditMatchWindow = 10 * time.Second

// onMessageDelete 消息删除 → 撤回通知。
// Discord 删除事件本身不携带删除者：guild 场景尽力经审计日志解析（管理删除会落
// 审计条目；本人自删不落，据「无匹配条目」推断自删），作者从内存缓存反查（无缓存
// 则操作者留空——审计条目不含消息 ID，无作者无法匹配）。解析需等待审计日志传播，
// 异步处理避免阻塞网关事件分发。
func (a *discordAdapter) onMessageDelete(s *discordgo.Session, e *discordgo.MessageDelete) {
	if e.Message == nil {
		return
	}
	trig := a.triggerOf()
	now := uint(time.Now().Unix())
	mid := msgID(e.ChannelID, e.ID)
	var authorID message.QID
	if cached, ok := a.msgCache.Find(e.ChannelID, e.ID); ok {
		authorID = cached.UserId
	}
	if e.GuildID == "" {
		// DM 频道中只能删除自己（或机器人）的消息，作者即删除者
		if trig.OnFriendRecall == nil {
			return
		}
		n := message.FriendRecallNotice{UserId: authorID, MessageId: mid}
		n.Time = now
		n.PostType = "notice"
		n.NoticeType = "friend_recall"
		n.SelfId = a.SelfID()
		n.SetPlatform(Platform)
		trig.OnFriendRecall(n)
		return
	}
	if trig.OnGroupRecall == nil {
		return
	}
	// 通知投递本身很快（不阻塞事件循环）；耗时的审计日志查询在 resolveDeleter
	// 内单独限流，避免批量删除触发 429 风暴。
	go a.emitGroupRecall(trig, e, authorID, mid, now)
}

// emitGroupRecall 解析删除者后投递群撤回通知（异步：等待审计日志传播）。
func (a *discordAdapter) emitGroupRecall(trig adapter.TriggerWrapper, e *discordgo.MessageDelete, authorID, mid message.QID, now uint) {
	n := message.GroupRecallNotice{
		GroupId:    message.QID(idPrefix + e.ChannelID),
		UserId:     authorID,
		OperatorId: a.resolveDeleter(e, authorID),
		MessageId:  mid,
	}
	n.Time = now
	n.PostType = "notice"
	n.NoticeType = "group_recall"
	n.SelfId = a.SelfID()
	n.SetPlatform(Platform)
	trig.OnGroupRecall(n)
}

// resolveDeleter 尽力解析删除者：作者未知或查询失败（无 View Audit Log 权限等）
// 返回空（不作自删假设）；有窗口内匹配条目返回操作者；无匹配条目（审计日志不记录
// 本人自删）返回作者。
//
// 审计日志查询带固定延迟且是 REST 调用，必须有并发上限：批量删除/purge（一次
// 几百上千条）会同时产生同量查询，撞 Discord 速率限制（429 风暴）并影响正常发送。
// 令牌不足时跳过查询、操作者留空（通知照常投递，降级语义与查询失败一致）。
func (a *discordAdapter) resolveDeleter(e *discordgo.MessageDelete, authorID message.QID) message.QID {
	if a.api == nil {
		return ""
	}
	authorRaw := strings.TrimPrefix(authorID.String(), idPrefix)
	if authorRaw == "" {
		return ""
	}
	select {
	case a.auditSem <- struct{}{}:
		defer func() { <-a.auditSem }()
	default:
		a.logger.Debug("审计日志查询并发已达上限，撤回操作者留空", "channelId", e.ChannelID)
		return ""
	}
	time.Sleep(recallAuditLogDelay)
	al, err := a.api.GuildAuditLog(e.GuildID, "", "", int(discordgo.AuditLogActionMessageDelete), 5)
	if err != nil {
		a.logger.Debug("Discord 审计日志查询失败，撤回操作者留空", "error", err)
		return ""
	}
	for _, entry := range al.AuditLogEntries {
		if entry.TargetID != authorRaw || entry.Options == nil || entry.Options.ChannelID != e.ChannelID {
			continue
		}
		if time.Since(snowflakeTime(entry.ID)) > recallAuditMatchWindow {
			continue // 历史条目：同作者同频道的旧删除，与本次无关
		}
		return userQID(entry.UserID)
	}
	return authorID // 审计日志不记录本人自删：无匹配条目即自删
}

// snowflakeTime Discord snowflake ID → 创建时间。
func snowflakeTime(id string) time.Time {
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(int64(v>>22) + 1420070400000)
}

// onReactionAdd 表情回应 → 群消息表情回应通知（仅 guild；DM 反应无对应公共通知，
// 与 Telegram 私聊回应不投递一致）。EmojiId 取 Unicode 字符或自定义表情 ID。
func (a *discordAdapter) onReactionAdd(s *discordgo.Session, e *discordgo.MessageReactionAdd) {
	if e.MessageReaction == nil || e.GuildID == "" || e.UserID == "" {
		return
	}
	trig := a.triggerOf()
	if trig.OnGroupMsgEmojiLike == nil {
		return
	}
	emojiID := e.Emoji.Name
	if emojiID == "" {
		emojiID = e.Emoji.ID // 自定义表情
	}
	if emojiID == "" {
		return
	}
	n := message.GroupMsgEmojiLikeNotice{
		GroupId:   message.QID(idPrefix + e.ChannelID),
		UserId:    userQID(e.UserID),
		MessageId: msgID(e.ChannelID, e.MessageID),
	}
	n.Likes = append(n.Likes, struct {
		EmojiId string `json:"emoji_id"`
		Count   int    `json:"count"`
	}{EmojiId: emojiID, Count: 1})
	n.Time = uint(time.Now().Unix())
	n.PostType = "notice"
	n.NoticeType = "group_msg_emoji_like"
	n.SelfId = a.SelfID()
	n.SetPlatform(Platform)
	trig.OnGroupMsgEmojiLike(n)
}

func (a *discordAdapter) emitPlatformEvent(eventType string, data any) {
	trig := a.triggerOf()
	if trig.OnPlatformEvent == nil {
		return
	}
	trig.OnPlatformEvent(message.PlatformEvent{
		Platform: Platform,
		Type:     eventType,
		Data:     data,
	})
}
