package pluginlog

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/bot/utils"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

type LogPlugin struct {
	plugin.Meta
	recorder *msglog.Recorder

	// bot 是 Awake 注入的 core 级 bot 外观：面板媒体地址过期时用它按 ID 前缀
	// 路由到来源适配器重新申请（见 MsgLogMediaURL）。
	botMu sync.RWMutex
	bot   bot.Bot

	// mediaURLs 续期后的媒体地址短缓存（日志 ID + 媒体序号 → 地址）
	mediaMu   sync.Mutex
	mediaURLs map[mediaURLKey]mediaURLValue

	// mentions 群成员昵称缓存：面板读取日志时把 [at:id:…] 还原成 [at:昵称]
	mentions mentionNamer
}

func NewPlugin() *LogPlugin {
	p := &LogPlugin{}
	p.Name = "日志打印插件"
	p.HelpWords = "用于在控制台打印日志信息"
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.AdminOnly = true
	p.Order = plugin.LevelLog
	p.ShowFor = plugininfo.ShowForNone
	return p
}

func (p *LogPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	// 消息日志记录器挂在缓存存储上：memory 驱动时重启清空（同旧版内存环形缓冲），
	// redis 驱动时重启后仍可回看。Storage 由 core 在 Start 前注入。
	p.recorder = msglog.New(p.Storage, 0)

	lastStartTime, ok := p.Storage.GetString(ctx, "last_start_time")
	if !ok {
		lastStartTime = "未保存"
	}
	p.Storage.SetString(ctx, "last_start_time", utils.GetFormattedTime())
	p.Logger.Info("日志打印插件初始化完成", "lastStartTime", lastStartTime)
	return nil
}

// Awake 保存 core 级 bot 外观：面板「消息日志」的媒体地址过期后，需要按
// 日志里的会话 ID / 消息 ID 前缀路由到对应适配器重新申请地址。
// 未收到 Awake（如设置向导未完成）时续期不可用，面板按加载失败展示。
func (p *LogPlugin) Awake(ctx context.Context, b bot.Bot) error {
	p.botMu.Lock()
	p.bot = b
	p.botMu.Unlock()
	return nil
}

// botHandle 取 core 级 bot 外观（Awake 之前为 nil）。
func (p *LogPlugin) botHandle() bot.Bot {
	p.botMu.RLock()
	defer p.botMu.RUnlock()
	return p.bot
}

// mentionNames 返回群的成员昵称表（用户 ID → 昵称），供面板把日志里的
// [at:id:…] 还原成昵称；平台不支持群成员枚举 / 取不到时返回 nil。
// wait<=0 表示只取缓存并顺带发起后台查询，不等待结果（收消息时预热用）。
func (p *LogPlugin) mentionNames(groupId string, wait time.Duration) map[string]string {
	members, _ := p.botHandle().(bot.GroupMembers)
	return p.mentions.names(members, groupId, wait)
}

// warmMentionNames 预热群的成员昵称缓存：只发起一次后台查询（受缓存 TTL 与
// 失败冷却约束），不等待结果，因此不会拖慢收消息。
func (p *LogPlugin) warmMentionNames(groupId message.QID) {
	if groupId == "" {
		return
	}
	p.mentionNames(groupId.String(), 0)
}

// MsgLogPage 供 Web 控制面板分页读取消息日志（实现 adminpanel.MsgLogSource）：
// 新在前，beforeID>0 时仅返回 ID 小于它的更旧日志。返回前把 [at:id:…] 还原成
// 成员昵称（只影响面板展示，留存的日志文本不变）。
func (p *LogPlugin) MsgLogPage(limit int, beforeID uint64) []msglog.Entry {
	if p.recorder == nil {
		return nil
	}
	return p.localizeEntries(p.recorder.Page(limit, beforeID))
}

// MsgLogSessions 返回消息日志的会话摘要（实现 adminpanel.MsgLogSessionSource），
// 供面板左侧会话列表按群聊 / 私聊 / 系统通知分类展示。
func (p *LogPlugin) MsgLogSessions() []msglog.Session {
	if p.recorder == nil {
		return nil
	}
	sessions := p.recorder.Sessions()
	// 会话预览（侧栏最近一条消息）也带上昵称，避免侧栏与气泡一个显示名字、
	// 一个显示 QQ 号
	names := make(map[string]map[string]string)
	deadline := time.Now().Add(mentionWait)
	for i := range sessions {
		sessions[i].LastText = p.localizeText(sessions[i].GroupId, sessions[i].LastText, names, deadline)
	}
	return sessions
}

// MsgLogSessionPage 按会话读取一页日志（新在前）：beforeID>0 取更旧的分页，
// afterID>0 取更新的增量（面板轮询用）。
func (p *LogPlugin) MsgLogSessionPage(session string, limit int, beforeID, afterID uint64) []msglog.Entry {
	if p.recorder == nil {
		return nil
	}
	return p.localizeEntries(p.recorder.PageSession(session, limit, beforeID, afterID))
}

// MsgLogDelete 删除面板里的一条消息日志（实现 adminpanel.MsgLogDeleteSource），
// 供「消息日志」页右键删除单条记录。只删本地留存，不影响平台上的原消息。
func (p *LogPlugin) MsgLogDelete(id uint64) bool {
	if p.recorder == nil {
		return false
	}
	return p.recorder.Delete(id)
}

// MsgLogDeleteSession 删除某个会话的全部日志（实现
// adminpanel.MsgLogSessionDeleteSource），供面板右键会话「删除会话记录」。
func (p *LogPlugin) MsgLogDeleteSession(session string) int {
	if p.recorder == nil {
		return 0
	}
	return p.recorder.DeleteSession(session)
}

// add 记录一条消息日志；recorder 未初始化（Start 之前）时忽略。
func (p *LogPlugin) add(e msglog.Entry) {
	if p.recorder == nil {
		return
	}
	p.recorder.Add(e)
}

// senderName 群名片优先，其次昵称。
func senderName(msg message.Message) string {
	if msg.Sender.Card != "" {
		return msg.Sender.Card
	}
	return msg.Sender.Nickname
}

func (p *LogPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	// 合并转发展开属 QQ 平台能力：仅事件来源为 QQ 时挂载，其余平台回退占位符
	opts := []message.MsgOptFunc{
		message.WithGetMsgFunc(b.GetMsgDetail),
		message.WithNoSenderPrefix(),
	}
	if qb, ok := b.(bot.QQ); ok {
		opts = append(opts, message.WithGetForwardMsgFunc(qb.GetForwardMsg))
	}
	text := msg.FriendlyText(false, opts...)
	// 消息里 @ 了别人：顺手预热本群成员昵称缓存（后台查询，不阻塞收消息），
	// 面板打开「消息日志」时就能直接显示昵称而不是 QQ 号。
	if strings.Contains(text, "[at:id:") {
		p.warmMentionNames(msg.GroupId)
	}
	p.Logger.Info("[收<-群]: "+text, "groupId", msg.GroupId, "userId", msg.Sender.UserId)
	p.add(msglog.Entry{
		Type:      msglog.TypeGroup,
		GroupId:   msg.GroupId.String(),
		UserId:    msg.Sender.UserId.String(),
		Nickname:  senderName(msg),
		Text:      text,
		Media:     extractMedia(msg.Message),
		MessageId: msg.MessageId.String(),
	})
	return true, nil
}

func (p *LogPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	opts := []message.MsgOptFunc{
		message.WithGetMsgFunc(b.GetMsgDetail),
		message.WithNoSenderPrefix(),
	}
	if qb, ok := b.(bot.QQ); ok {
		opts = append(opts, message.WithGetForwardMsgFunc(qb.GetForwardMsg))
	}
	text := msg.FriendlyText(false, opts...)
	p.Logger.Info("[收<-好友]: "+text, "userId", msg.Sender.UserId)
	media := extractMedia(msg.Message)
	resolvePrivateFileURLs(b, msg.Sender.UserId, media)
	p.add(msglog.Entry{
		Type:      msglog.TypeFriend,
		UserId:    msg.Sender.UserId.String(),
		Nickname:  msg.Sender.Nickname,
		Text:      text,
		Media:     media,
		MessageId: msg.MessageId.String(),
	})
	return true, nil
}

// resolvePrivateFileURLs 私聊文件消息通常只带 file_id：向平台换一个临时下载
// 地址（NapCat 的 get_private_file_url），面板的文件卡片才能直接下载。地址会
// 过期，但消息日志只保留最近的记录，用户一般能及时下载；失败时保持无链接，
// 面板展示为「暂无下载地址」。
func resolvePrivateFileURLs(b bot.Bot, userId message.QID, media []msglog.Media) {
	qb, ok := b.(bot.QQ)
	if !ok {
		return
	}
	for i := range media {
		if media[i].Kind != msglog.MediaFile || media[i].URL != "" || media[i].FileId == "" {
			continue
		}
		if u, ok := qb.GetPrivateFileURL(userId, media[i].FileId); ok && isHTTPURL(u) {
			media[i].URL = u
		}
	}
}

// notice 记录一条通知事件日志。
func (p *LogPlugin) notice(groupId, userId message.QID, title, text string) {
	p.add(msglog.Entry{
		Type:    msglog.TypeNotice,
		GroupId: groupId.String(),
		UserId:  userId.String(),
		Title:   title,
		Text:    text,
	})
}

func (p *LogPlugin) OnGroupUpload(ctx context.Context, bot bot.Bot, n message.GroupUploadNotice) error {
	p.notice(n.GroupId, n.UserId, "群文件上传",
		fmt.Sprintf("%s 上传了文件 %s（%d 字节）", n.UserId.String(), n.File.Name, n.File.Size))
	return nil
}

func (p *LogPlugin) OnGroupAdmin(ctx context.Context, bot bot.Bot, n message.GroupAdminNotice) error {
	action := "被设置为管理员"
	if n.SubType == "unset" {
		action = "被取消管理员"
	}
	p.notice(n.GroupId, n.UserId, "群管理员变动",
		fmt.Sprintf("%s %s", n.UserId.String(), action))
	return nil
}

func (p *LogPlugin) OnGroupDecrease(ctx context.Context, bot bot.Bot, n message.GroupDecreaseNotice) error {
	action := map[string]string{
		"leave":   "退出了群聊",
		"kick":    "被踢出群聊",
		"kick_me": "Bot 被踢出群聊",
	}[n.SubType]
	if action == "" {
		action = "离开了群聊"
	}
	p.notice(n.GroupId, n.UserId, "群成员减少",
		fmt.Sprintf("%s %s（操作者 %s）", n.UserId.String(), action, n.OperatorId.String()))
	return nil
}

func (p *LogPlugin) OnGroupIncrease(ctx context.Context, bot bot.Bot, n message.GroupIncreaseNotice) error {
	action := "加入了群聊"
	if n.SubType == "invite" {
		action = "被邀请加入群聊"
	}
	p.notice(n.GroupId, n.UserId, "群成员增加",
		fmt.Sprintf("%s %s（操作者 %s）", n.UserId.String(), action, n.OperatorId.String()))
	return nil
}

func (p *LogPlugin) OnGroupBan(ctx context.Context, bot bot.Bot, n message.GroupBanNotice) error {
	if n.SubType == "lift_ban" {
		p.notice(n.GroupId, n.UserId, "群禁言",
			fmt.Sprintf("%s 被解除禁言（操作者 %s）", n.UserId.String(), n.OperatorId.String()))
	} else {
		p.notice(n.GroupId, n.UserId, "群禁言",
			fmt.Sprintf("%s 被禁言 %d 秒（操作者 %s）", n.UserId.String(), n.Duration, n.OperatorId.String()))
	}
	return nil
}

func (p *LogPlugin) OnFriendAdd(ctx context.Context, bot bot.Bot, n message.FriendAddNotice) error {
	p.notice("", n.UserId, "新好友",
		fmt.Sprintf("%s 添加了 Bot 为好友", n.UserId.String()))
	return nil
}

func (p *LogPlugin) OnGroupRecall(ctx context.Context, bot bot.Bot, n message.GroupRecallNotice) error {
	if n.OperatorId == "" {
		// 部分平台（如飞书）撤回事件不携带操作者，避免渲染出「被  撤回」
		p.notice(n.GroupId, n.UserId, "群消息撤回",
			fmt.Sprintf("%s 的消息（%s）被撤回", n.UserId.String(), n.MessageId.String()))
		return nil
	}
	p.notice(n.GroupId, n.UserId, "群消息撤回",
		fmt.Sprintf("%s 的消息（%s）被 %s 撤回", n.UserId.String(), n.MessageId.String(), n.OperatorId.String()))
	return nil
}

func (p *LogPlugin) OnFriendRecall(ctx context.Context, bot bot.Bot, n message.FriendRecallNotice) error {
	p.notice("", n.UserId, "好友消息撤回",
		fmt.Sprintf("%s 撤回了消息（%s）", n.UserId.String(), n.MessageId.String()))
	return nil
}

func (p *LogPlugin) OnPoke(ctx context.Context, bot bot.Bot, n message.PokeNotice) error {
	var groupId message.QID
	if n.GroupId != nil {
		groupId = *n.GroupId
	}
	p.notice(groupId, n.UserId, "戳一戳",
		fmt.Sprintf("%s 戳了戳 %s", n.UserId.String(), n.TargetId.String()))
	return nil
}

func (p *LogPlugin) OnLuckyKing(ctx context.Context, bot bot.Bot, n message.LuckyKingNotice) error {
	p.notice(n.GroupId, n.TargetId, "运气王",
		fmt.Sprintf("%s 发出的红包，%s 成为运气王", n.UserId.String(), n.TargetId.String()))
	return nil
}

func (p *LogPlugin) OnHonor(ctx context.Context, bot bot.Bot, n message.HonorNotice) error {
	honor := map[string]string{
		"talkative":     "龙王",
		"performer":     "群聊之火",
		"legend":        "群聊炽焰",
		"strong_newbie": "冒尖小春笋",
		"emotion":       "快乐源泉",
	}[n.HonorType]
	if honor == "" {
		honor = n.HonorType
	}
	p.notice(n.GroupId, n.UserId, "群荣誉变更",
		fmt.Sprintf("%s 获得群荣誉「%s」", n.UserId.String(), honor))
	return nil
}

func (p *LogPlugin) OnGroupMsgEmojiLike(ctx context.Context, bot bot.Bot, n message.GroupMsgEmojiLikeNotice) error {
	p.notice(n.GroupId, n.UserId, "表情回应",
		fmt.Sprintf("%s 对消息（%s）添加了 %d 个表情回应", n.UserId.String(), n.MessageId.String(), len(n.Likes)))
	return nil
}

func (p *LogPlugin) OnEssence(ctx context.Context, bot bot.Bot, n message.EssenceNotice) error {
	action := "被设为精华"
	if n.SubType == "delete" {
		action = "被移出精华"
	}
	p.notice(n.GroupId, n.SenderId, "群精华消息",
		fmt.Sprintf("%s 的消息（%s）%s（操作者 %s）", n.SenderId.String(), n.MessageId.String(), action, n.OperatorId.String()))
	return nil
}

func (p *LogPlugin) OnGroupCard(ctx context.Context, bot bot.Bot, n message.GroupCardNotice) error {
	p.notice(n.GroupId, n.UserId, "群名片变更",
		fmt.Sprintf("%s 的群名片由「%s」改为「%s」", n.UserId.String(), n.CardOld, n.CardNew))
	return nil
}
