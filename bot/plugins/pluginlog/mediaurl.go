package pluginlog

import (
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// mediaURLCacheTTL 续期地址的内存缓存时长：平台签发的地址本身可用约 2 小时，
// 面板一次翻页可能对同一张图片重复请求，短缓存即可避免反复打扰协议端。
const mediaURLCacheTTL = 10 * time.Minute

// mediaURLCacheCap 缓存条目上限，超出后整体清空（媒体续期是低频操作，够用即可）。
const mediaURLCacheCap = 512

type mediaURLKey struct {
	entryID uint64
	index   int
}

type mediaURLValue struct {
	url     string
	expires time.Time
}

// MsgLogMediaURL 重新解析一条日志里第 index 个媒体的可访问地址，实现
// adminpanel.MsgLogMediaSource：面板媒体代理遇到非 2xx（QQ 的 rkey 过期、
// 文件直链失效等）时调用，拿到新地址后由代理重试。
//
// 解析顺序：先按平台文件 ID 直接续期（不依赖原消息还在不在），失败再重新
// 拉取原消息（适配器会把消息里的媒体地址重新签发一遍）。都失败返回 false，
// 面板按加载失败展示。
func (p *LogPlugin) MsgLogMediaURL(entryID uint64, index int) (string, bool) {
	if p.recorder == nil {
		return "", false
	}
	if u, ok := p.cachedMediaURL(entryID, index); ok {
		return u, true
	}
	entry, ok := p.recorder.Get(entryID)
	if !ok || index < 0 || index >= len(entry.Media) {
		return "", false
	}
	m := entry.Media[index]

	b := p.mediaBot()
	if b == nil {
		return "", false
	}

	// 1) 按平台文件 ID 直接续期：QQ 的 get_image / get_file / get_group_file_url
	//    等接口会重新签发地址，与消息是否还能读取无关。协议端偶尔会回同一条
	//    地址（鉴权参数没变），此时不缓存、继续尝试下面的消息回捞。
	if m.FileId != "" {
		if mr, ok := b.(bot.MediaRefresher); ok {
			if u, ok := mr.RefreshMediaURL(segmentKindOf(m.Kind), m.FileId, entry.GroupId, entry.UserId); ok &&
				isHTTPURL(u) && u != m.URL {
				p.cacheMediaURL(entryID, index, u)
				return u, true
			}
		}
	}

	// 2) 退化为重新拉取原消息：适配器把消息重新编码时会带上新签发的地址
	//    （NapCat 文档明确 get_msg 可刷新过期 URL），按文件 ID 匹配同一条媒体。
	if entry.MessageId != "" {
		if msg, ok := b.GetMsgDetail(message.QID(entry.MessageId)); ok && msg != nil {
			if u := matchMediaURL(extractMedia(msg.Message), entry.Media, index); u != "" {
				p.cacheMediaURL(entryID, index, u)
				return u, true
			}
		}
	}
	return "", false
}

// mediaBot 返回媒体续期用的 bot 外观：Awake 注入的 core 级外观按 ID 前缀
// 路由到来源适配器（多平台 / 双 QQ 账号并存时都能命中正确的协议端）。
func (p *LogPlugin) mediaBot() bot.Bot {
	p.botMu.RLock()
	defer p.botMu.RUnlock()
	return p.bot
}

func (p *LogPlugin) cachedMediaURL(entryID uint64, index int) (string, bool) {
	p.mediaMu.Lock()
	defer p.mediaMu.Unlock()
	v, ok := p.mediaURLs[mediaURLKey{entryID: entryID, index: index}]
	if !ok || time.Now().After(v.expires) {
		return "", false
	}
	return v.url, true
}

func (p *LogPlugin) cacheMediaURL(entryID uint64, index int, url string) {
	p.mediaMu.Lock()
	defer p.mediaMu.Unlock()
	if p.mediaURLs == nil || len(p.mediaURLs) >= mediaURLCacheCap {
		p.mediaURLs = make(map[mediaURLKey]mediaURLValue, mediaURLCacheCap)
	}
	p.mediaURLs[mediaURLKey{entryID: entryID, index: index}] = mediaURLValue{
		url:     url,
		expires: time.Now().Add(mediaURLCacheTTL),
	}
}
