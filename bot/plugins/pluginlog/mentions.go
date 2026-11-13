package pluginlog

import (
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/msglog"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

/*
消息日志里的 @ 提及本地化。

message.FriendlyText 对被 @ 的用户只输出 [at:id:qq:123]（被 @ 用户在本群的
昵称只有平台知道，猜错会造成张冠李戴），消息日志把这段文本原样留存。面板
展示时按群成员列表把 ID 还原成昵称，形如 [at:小明]——面板的文本解析会把
[at:…] 渲染成高亮的 @ 标签。

还原只发生在「读取日志」这一侧：日志里存的仍是 id 占位，AI 拿到的是稳定
的 ID；群成员表按群缓存，查不到（平台不支持群成员枚举、协议端未连接、
昵称被清空）时保留原来的 ID 占位。
*/

const (
	// mentionCacheTTL 群成员昵称的缓存时长。群名片会变，但翻看历史日志时
	// 没必要每条消息都向协议端要一次成员表。
	mentionCacheTTL = 10 * time.Minute
	// mentionWait 面板单次请求等待成员查询的总时长预算（同一页里多个群共享）。
	// 超出就先用现有昵称表（通常是空的，即先显示 ID），查询在后台继续，结果
	// 写回缓存给下一次请求用，避免协议端卡住时把面板拖到请求超时。
	mentionWait = 1200 * time.Millisecond
	// mentionFailTTL 查询失败后的冷却时长：协议端未连接时避免每次轮询都打一遍。
	mentionFailTTL = 30 * time.Second
	// mentionNameMaxRunes 昵称展示长度上限（群名片可能很长，撑破气泡没有意义）。
	mentionNameMaxRunes = 24
)

// atIDToken 匹配 FriendlyText 输出的 @ 占位符，捕获其中的用户 ID（如 qq:123）。
var atIDToken = regexp.MustCompile(`\[at:id:([^\]]*)\]`)

// mentionNameReplacer 昵称里的方括号 / 换行会破坏 [at:…] 占位符与文本排版，
// 统一换成全角括号与空格。
var mentionNameReplacer = strings.NewReplacer(
	"[", "【",
	"]", "】",
	"\r", " ",
	"\n", " ",
)

// mentionNamer 按群缓存成员昵称表（用户 ID → 昵称 / 群名片）。
// 并发安全：面板可能同时读取多个视图。
type mentionNamer struct {
	mu     sync.Mutex
	groups map[string]*mentionGroup
}

// mentionGroup 单个群的成员昵称表状态。
type mentionGroup struct {
	names    map[string]string // 用户 ID（qq:123）→ 昵称，nil 表示还没有成功过
	fetched  time.Time         // 最近一次成功获取的时间
	failedAt time.Time         // 最近一次失败的时间（冷却用）
	loading  bool              // 后台查询进行中，避免并发重复查询
	done     chan struct{}     // 进行中查询的完成信号（loading 为 true 时有效）
}

// names 返回群的成员昵称表：缓存新鲜时直接返回；过期或从未获取时触发一次
// 后台查询，本次最多等待 wait（wait<=0 只发起查询、不等待）。已有查询在跑时
// 同样只等 wait（面板先拉会话列表、再拉消息，第二次请求能等到第一次的结果）。
// 取不到时返回 nil（调用方保留 ID 占位）。
func (n *mentionNamer) names(members bot.GroupMembers, groupId string, wait time.Duration) map[string]string {
	if members == nil || groupId == "" {
		return nil
	}
	n.mu.Lock()
	if n.groups == nil {
		n.groups = make(map[string]*mentionGroup)
	}
	g := n.groups[groupId]
	if g == nil {
		g = &mentionGroup{}
		n.groups[groupId] = g
	}
	// 缓存有效
	if len(g.names) > 0 && time.Since(g.fetched) < mentionCacheTTL {
		names := g.names
		n.mu.Unlock()
		return names
	}
	// 已有查询在跑：等它出结果，不再重复发一次
	if g.loading {
		done := g.done
		n.mu.Unlock()
		waitFor(done, wait)
		n.mu.Lock()
		names := g.names
		n.mu.Unlock()
		return names
	}
	// 刚失败过：冷却期内不重试（有过昵称表就继续沿用旧值，总比显示号码好）
	if !g.failedAt.IsZero() && time.Since(g.failedAt) < mentionFailTTL {
		names := g.names
		n.mu.Unlock()
		return names
	}
	g.loading = true
	g.done = make(chan struct{})
	done := g.done
	n.mu.Unlock()

	go func() {
		names, ok := fetchMentionNames(members, groupId)
		n.mu.Lock()
		g.loading = false
		if ok {
			g.names = names
			g.fetched = time.Now()
			g.failedAt = time.Time{}
		} else {
			g.failedAt = time.Now()
		}
		n.mu.Unlock()
		close(done)
	}()

	waitFor(done, wait)
	n.mu.Lock()
	defer n.mu.Unlock()
	return g.names
}

// waitFor 在 wait 内等 done 关闭（wait<=0 表示不等待，直接返回）。
func waitFor(done <-chan struct{}, wait time.Duration) {
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// fetchMentionNames 向平台要一次群成员表，转换成「用户 ID → 昵称」映射：
// 群名片优先，其次昵称；两者都没有的用户跳过（保留 ID 占位）。
func fetchMentionNames(members bot.GroupMembers, groupId string) (map[string]string, bool) {
	list, ok := members.GetGroupMemberList(message.QID(groupId), false)
	if !ok || list == nil || len(*list) == 0 {
		return nil, false
	}
	names := make(map[string]string, len(*list))
	for _, m := range *list {
		id := m.UserID.String()
		if id == "" {
			continue
		}
		name := sanitizeMentionName(m.Card)
		if name == "" {
			name = sanitizeMentionName(m.Nickname)
		}
		if name == "" {
			continue
		}
		names[id] = name
	}
	if len(names) == 0 {
		return nil, false
	}
	return names, true
}

// sanitizeMentionName 清理昵称：去掉首尾空白、换行与方括号，并限制长度。
func sanitizeMentionName(name string) string {
	name = strings.TrimSpace(mentionNameReplacer.Replace(name))
	if name == "" {
		return ""
	}
	if utf8.RuneCountInString(name) > mentionNameMaxRunes {
		name = string([]rune(name)[:mentionNameMaxRunes])
	}
	return name
}

// rewriteMentions 把文本里的 [at:id:…] 占位还原成 [at:昵称]；昵称表里没有的
// 目标保持 ID 占位（如已退群 / 昵称不可得的用户）。
func rewriteMentions(text string, names map[string]string) string {
	if len(names) == 0 || !strings.Contains(text, "[at:id:") {
		return text
	}
	return atIDToken.ReplaceAllStringFunc(text, func(token string) string {
		id := token[len("[at:id:") : len(token)-1]
		name := names[id]
		if name == "" {
			// 兼容裸数字 ID 的旧日志（统一成 qq: 前缀后再查一次）
			name = names[message.FromString(id).String()]
		}
		if name == "" {
			return token
		}
		return "[at:" + name + "]"
	})
}

// localizeText 把一段日志文本里的 @ 提及还原成昵称。
// names 为本次请求的「群 ID → 昵称表」缓存，同一个群只查一次；
// deadline 为本次请求等待查询的总截止时间（同一页里的多个群共享预算）。
func (p *LogPlugin) localizeText(groupId, text string, names map[string]map[string]string, deadline time.Time) string {
	if groupId == "" || !strings.Contains(text, "[at:id:") {
		return text
	}
	table, ok := names[groupId]
	if !ok {
		table = p.mentionNames(groupId, time.Until(deadline))
		names[groupId] = table
	}
	return rewriteMentions(text, table)
}

// localizeEntries 为面板读取的日志补上 @ 昵称（不修改留存内容）。
func (p *LogPlugin) localizeEntries(entries []msglog.Entry) []msglog.Entry {
	if len(entries) == 0 {
		return entries
	}
	names := make(map[string]map[string]string)
	deadline := time.Now().Add(mentionWait)
	for i := range entries {
		entries[i].Text = p.localizeText(entries[i].GroupId, entries[i].Text, names, deadline)
	}
	return entries
}
