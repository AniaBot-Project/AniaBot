<template>
  <div class="msglog-shell" :class="{ 'chat-open': mobileChat }">
    <!-- 左侧会话列表：按群聊 / 私聊 / 系统通知分类 -->
    <aside class="conv-pane">
      <div class="conv-head">
        <div class="conv-heading">
          <span class="conv-heading-title">消息会话</span>
          <span class="conv-heading-count">{{ sessions.length }}</span>
        </div>
        <Input v-model="search" size="small" placeholder="搜索群 / 好友 / 消息" allow-clear class="conv-search">
          <template #prefix><Icon name="Search" :size="13" /></template>
        </Input>
      </div>

      <div class="conv-scroll">
        <button type="button" class="conv-item" :class="{ active: isAll }" @click="selectSession('all')">
          <div class="conv-avatar all-avatar"><Icon name="Chat" :size="18" /></div>
          <div class="conv-main">
            <div class="conv-row">
              <span class="conv-name">全部消息</span>
              <span class="conv-time">{{ allTime }}</span>
            </div>
            <div class="conv-row">
              <span class="conv-preview">{{ allPreview }}</span>
            </div>
          </div>
        </button>

        <div v-if="loading && !sessions.length" class="conv-loading">加载会话中…</div>

        <template v-else>
          <section v-if="groups.length" class="conv-section">
            <div class="conv-section-title">
              <span>群聊</span>
              <span class="conv-section-count">{{ groups.length }}</span>
            </div>
            <button
              v-for="s in groups"
              :key="s.key"
              type="button"
              class="conv-item"
              :class="{ active: activeKey === s.key }"
              @click="selectSession(s.key)"
              @contextmenu.prevent="openSessionMenu($event, s)"
            >
              <div class="conv-avatar" :style="avatarGradient(sessionAvatarKey(s))">
                <img
                  v-if="sessionAvatarUrl(s) && !avatarBroken[s.key]"
                  class="conv-avatar-img"
                  :src="sessionAvatarUrl(s)"
                  alt=""
                  loading="lazy"
                  referrerpolicy="no-referrer"
                  @error="markAvatarBroken(s)"
                />
                <template v-else>{{ avatarInitials(sessionName(s)) }}</template>
                <i v-if="isUnread(s)" class="unread-dot" />
              </div>
              <div class="conv-main">
                <div class="conv-row">
                  <span class="conv-name" :class="{ unread: isUnread(s) }">{{ sessionName(s) }}</span>
                  <span class="conv-time">{{ sideTime(s.last_time) }}</span>
                </div>
                <div class="conv-row">
                  <span class="conv-preview">{{ sessionPreview(s) }}</span>
                  <span v-if="isUnread(s)" class="unread-mark">新</span>
                </div>
              </div>
            </button>
          </section>

          <section v-if="friends.length" class="conv-section">
            <div class="conv-section-title">
              <span>私聊</span>
              <span class="conv-section-count">{{ friends.length }}</span>
            </div>
            <button
              v-for="s in friends"
              :key="s.key"
              type="button"
              class="conv-item"
              :class="{ active: activeKey === s.key }"
              @click="selectSession(s.key)"
              @contextmenu.prevent="openSessionMenu($event, s)"
            >
              <div class="conv-avatar" :style="avatarGradient(sessionAvatarKey(s))">
                <img
                  v-if="sessionAvatarUrl(s) && !avatarBroken[s.key]"
                  class="conv-avatar-img"
                  :src="sessionAvatarUrl(s)"
                  alt=""
                  loading="lazy"
                  referrerpolicy="no-referrer"
                  @error="markAvatarBroken(s)"
                />
                <template v-else>{{ avatarInitials(sessionName(s)) }}</template>
                <i v-if="isUnread(s)" class="unread-dot" />
              </div>
              <div class="conv-main">
                <div class="conv-row">
                  <span class="conv-name" :class="{ unread: isUnread(s) }">{{ sessionName(s) }}</span>
                  <span class="conv-time">{{ sideTime(s.last_time) }}</span>
                </div>
                <div class="conv-row">
                  <span class="conv-preview">{{ sessionPreview(s) }}</span>
                  <span v-if="isUnread(s)" class="unread-mark">新</span>
                </div>
              </div>
            </button>
          </section>

          <section v-if="systems.length" class="conv-section">
            <div class="conv-section-title">
              <span>系统通知</span>
              <span class="conv-section-count">{{ systems.length }}</span>
            </div>
            <button
              v-for="s in systems"
              :key="s.key"
              type="button"
              class="conv-item"
              :class="{ active: activeKey === s.key }"
              @click="selectSession(s.key)"
              @contextmenu.prevent="openSessionMenu($event, s)"
            >
              <div class="conv-avatar system-avatar" :style="avatarGradient('system')">
                <Icon name="Bell" :size="17" />
                <i v-if="isUnread(s)" class="unread-dot" />
              </div>
              <div class="conv-main">
                <div class="conv-row">
                  <span class="conv-name" :class="{ unread: isUnread(s) }">{{ sessionName(s) }}</span>
                  <span class="conv-time">{{ sideTime(s.last_time) }}</span>
                </div>
                <div class="conv-row">
                  <span class="conv-preview">{{ sessionPreview(s) }}</span>
                  <span v-if="isUnread(s)" class="unread-mark">新</span>
                </div>
              </div>
            </button>
          </section>

          <div v-if="!sessions.length" class="conv-empty">
            <span class="conv-empty-ico"><Icon name="Inbox" :size="22" /></span>
            还没有任何会话
            <small>Bot 收到消息或通知后会在这里出现</small>
          </div>
          <div v-else-if="!visibleSessions.length" class="conv-empty">
            <span class="conv-empty-ico"><Icon name="Search" :size="20" /></span>
            没有匹配「{{ search }}」的会话
          </div>
        </template>
      </div>
    </aside>

    <!-- 右侧聊天视图 -->
    <section class="chat-pane">
      <header class="chat-head">
        <button type="button" class="back-btn" title="返回会话列表" @click="mobileChat = false">
          <Icon name="ChevronLeft" :size="18" />
        </button>

        <div v-if="isAll" class="head-avatar all-avatar"><Icon name="Chat" :size="19" /></div>
        <div v-else class="head-avatar" :style="headAvatarStyle">
          <img
            v-if="headAvatarSrc && !avatarBroken[activeKey]"
            class="head-avatar-img"
            :src="headAvatarSrc"
            alt=""
            referrerpolicy="no-referrer"
            @error="markAvatarBroken(activeSession)"
          />
          <template v-else>{{ headInitials }}</template>
        </div>

        <div class="head-main">
          <div class="head-title">{{ headName }}</div>
          <div class="head-sub">{{ headSubtitle }}</div>
        </div>

        <div class="head-actions">
          <div v-if="isAll" class="type-tabs">
            <button
              v-for="t in typeTabs"
              :key="t.value"
              type="button"
              :class="{ active: typeFilter === t.value }"
              @click="typeFilter = t.value"
            >
              {{ t.label }}
            </button>
          </div>
          <label class="auto-refresh">
            <Switch v-model="autoRefresh" size="small" />
            <span>自动刷新</span>
          </label>
          <button type="button" class="icon-btn" title="立即刷新" @click="manualRefresh">
            <Icon name="Refresh" :size="16" />
          </button>
        </div>
      </header>

      <div class="chat-body">
        <div ref="listEl" class="chat-scroll" @scroll.passive="onScroll">
          <div v-if="loading" class="chat-empty">
            <span class="empty-spinner" />
            <p>正在加载消息…</p>
          </div>

          <div v-else-if="!rows.length" class="chat-empty">
            <span class="empty-ico"><Icon name="Chat" :size="30" /></span>
            <p>{{ isAll ? '暂无消息记录' : '这个会话还没有消息' }}</p>
            <small v-if="isAll">日志保存在缓存存储中（memory 驱动重启后清空，Redis 驱动可持久化，最多保留最近 5000 条）</small>
            <small v-else>收到新消息后会自动出现在这里</small>
          </div>

          <div v-else class="chat-inner">
            <div class="load-tip">
              <span v-if="loadingMore">正在加载更早的消息…</span>
              <span v-else-if="hasMoreActive">滚动到顶部加载更早的消息</span>
              <span v-else>已经是最早的消息了</span>
            </div>

            <template v-for="row in rows" :key="row.key">
              <div v-if="row.kind === 'day'" class="day-divider"><span>{{ row.label }}</span></div>
              <div v-else-if="row.kind === 'time'" class="time-divider"><span>{{ row.label }}</span></div>
              <MsgLogBubble
                v-else
                :log="row.log"
                :show-avatar="row.showAvatar"
                :show-name="row.showName"
                :context="row.context"
                @preview="openPreview"
                @media-load="onMediaLoad"
                @contextmenu="openContextMenu"
              />
            </template>
          </div>
        </div>

        <button v-if="pendingNew > 0" type="button" class="new-pill" @click="jumpToLatest">
          ↓ {{ pendingNew }} 条新消息
        </button>
      </div>

      <footer class="chat-foot">
        <span class="live-dot" :class="{ paused: !autoRefresh }" />
        <span>{{ autoRefresh ? '实时监听中' : '自动刷新已暂停' }}</span>
        <span class="foot-sep">·</span>
        <span>最后更新 {{ lastUpdatedText }}</span>
        <span class="foot-sep foot-sep-hint">|</span>
        <span class="foot-hint">右键会话可删除记录，右键消息可复制或删除</span>
        <span class="foot-actions">
          <button type="button" @click="jumpToLatest">回到最新</button>
        </span>
      </footer>
    </section>

    <!-- 图片预览 -->
    <Teleport to="body">
      <Transition name="fade">
        <div v-if="lightboxCurrent" class="lightbox" @click.self="closePreview">
          <figure class="lightbox-figure">
            <img
              :src="msgMediaUrl(lightboxCurrent.media.url, '', false, lightboxCurrent.ref)"
              :alt="lightboxCurrent.media.name || '图片'"
            />
            <figcaption class="lightbox-bar">
              <span class="lightbox-name">{{ lightboxCurrent.media.name || '图片' }}</span>
              <span class="lightbox-index" v-if="lightbox.items.length > 1">
                {{ lightbox.index + 1 }} / {{ lightbox.items.length }}
              </span>
              <a
                :href="msgMediaUrl(lightboxCurrent.media.url, '', false, lightboxCurrent.ref)"
                target="_blank"
                rel="noopener noreferrer"
              >打开原图</a>
              <a :href="msgMediaUrl(lightboxCurrent.media.url, lightboxCurrent.media.name || 'image', true, lightboxCurrent.ref)">下载</a>
              <button type="button" @click="closePreview">关闭</button>
            </figcaption>
          </figure>
          <button
            v-if="lightbox.items.length > 1"
            type="button"
            class="lightbox-nav prev"
            title="上一张"
            @click.stop="stepPreview(-1)"
          >
            ‹
          </button>
          <button
            v-if="lightbox.items.length > 1"
            type="button"
            class="lightbox-nav next"
            title="下一张"
            @click.stop="stepPreview(1)"
          >
            ›
          </button>
        </div>
      </Transition>
    </Teleport>

    <!-- 右键菜单：消息（复制 / 删除单条）与会话（复制会话 ID / 删除整个会话） -->
    <Teleport to="body">
      <div
        v-if="contextMenu"
        class="ctx-menu"
        :style="{ left: contextMenu.x + 'px', top: contextMenu.y + 'px' }"
        @contextmenu.prevent
        @mousedown.stop
      >
        <div class="ctx-hint">{{ contextMenuLabel }}</div>
        <template v-if="contextMenu.type === 'session'">
          <button type="button" class="ctx-item" @click="copySessionId(contextMenu.session)">
            <Icon name="Copy" :size="13" />
            复制会话 ID
          </button>
          <button type="button" class="ctx-item danger" @click="deleteSession(contextMenu.session)">
            <Icon name="Trash" :size="13" />
            删除会话记录
          </button>
        </template>
        <template v-else>
          <button type="button" class="ctx-item" @click="copyLogText(contextMenu.log)">
            <Icon name="Copy" :size="13" />
            复制文本
          </button>
          <button type="button" class="ctx-item danger" @click="deleteLog(contextMenu.log)">
            <Icon name="Trash" :size="13" />
            删除消息
          </button>
        </template>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { Icon, Input, Notification, Switch } from 'animal-island-vue'
import { api, msgMediaUrl } from '../api.js'
import { confirmAction } from '../ui/confirm'
import { avatarGradient, avatarInitials, sessionAvatarKey, sessionAvatarUrl } from '../utils/avatar.js'
import { friendlyPreview, shortId } from '../utils/messageText.js'
import MsgLogBubble from '../components/MsgLogBubble.vue'

const PAGE = 50
// MAX_KEEP 前端最多保留的消息条数（与服务端留存上限一致）：面板常开时新消息会不断
// 前插，不裁剪的话数组和 DOM 会一直增长到刷新页面为止。
const MAX_KEEP = 5000
const ACTIVE_KEY = 'aniabot.msglog.active'
const SEEN_KEY = 'aniabot.msglog.seen'
const SEED_KEY = 'aniabot.msglog.seeded'

const typeTabs = [
  { value: 'all', label: '全部' },
  { value: 'group', label: '群消息' },
  { value: 'friend', label: '好友' },
  { value: 'notice', label: '通知' },
]

const sessions = ref([])
const allLogs = ref([]) // 全部消息，新在前
const sessionStore = reactive({}) // key -> { items: 新在前, hasMore, loaded, loading }
const activeKey = ref('all')
const typeFilter = ref('all')
const search = ref('')
const autoRefresh = ref(true)
const loading = ref(true)
const loadingMore = ref(false)
const hasMoreAll = ref(false)
const pendingNew = ref(0)
const lastUpdated = ref(0)
const listEl = ref(null)
const mobileChat = ref(false)
const nameMap = ref({})
const seen = reactive(readSeen())
// 是否希望聊天视图保持贴底（用户主动上翻后关闭，图片加载撑高时不再强行回到底部）
let autoStick = true

let timer = null
let refreshing = false

/* ---------------- 派生数据 ---------------- */

const isAll = computed(() => activeKey.value === 'all')
const activeSession = computed(() => sessions.value.find((s) => s.key === activeKey.value) || null)

const visibleSessions = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return sessions.value
  return sessions.value.filter((s) =>
    [sessionName(s), s.key, s.group_id, s.user_id, s.last_text, s.title, s.name]
      .some((v) => v && String(v).toLowerCase().includes(q)),
  )
})
const groups = computed(() => visibleSessions.value.filter((s) => s.kind === 'group'))
const friends = computed(() => visibleSessions.value.filter((s) => s.kind === 'friend'))
const systems = computed(() => visibleSessions.value.filter((s) => s.kind === 'system'))
const sessionIndex = computed(() => new Map(sessions.value.map((s) => [s.key, s])))

const rows = computed(() => {
  const list = isAll.value
    ? allLogs.value.filter((l) => typeFilter.value === 'all' || l.type === typeFilter.value)
    : (sessionStore[activeKey.value]?.items || [])

  const out = []
  let prev = null
  let lastDay = ''
  let lastTime = 0
  for (let i = list.length - 1; i >= 0; i--) {
    const log = list[i]
    const time = new Date(log.time).getTime()
    const day = dayKey(log.time)
    if (day !== lastDay) {
      out.push({ kind: 'day', key: 'day-' + day + '-' + (log.id || i), label: dayLabel(log.time) })
      lastDay = day
      prev = null
      lastTime = 0
    }
    if (!prev || time - lastTime > 5 * 60 * 1000) {
      out.push({ kind: 'time', key: 'time-' + (log.id || i), label: clock(log.time) })
      prev = null
    }
    const sameSender = !!prev &&
      prev.type === log.type &&
      prev.user_id === log.user_id &&
      prev.nickname === log.nickname &&
      time - lastTime <= 3 * 60 * 1000
    out.push({
      kind: 'msg',
      key: 'msg-' + (log.id || i),
      log,
      showAvatar: !sameSender || log.type === 'notice',
      showName: !sameSender && log.type !== 'notice',
      context: isAll.value && typeFilter.value !== 'friend' ? contextForLog(log) : '',
    })
    prev = log
    lastTime = time
  }
  return out
})

const hasMoreActive = computed(() => {
  if (isAll.value) return hasMoreAll.value
  return !!sessionStore[activeKey.value]?.hasMore
})

const headName = computed(() => {
  if (isAll.value) return '全部消息'
  return sessionName(activeSession.value)
})
const headSubtitle = computed(() => {
  if (isAll.value) return '全部会话 · 实时收信流水'
  const s = activeSession.value
  if (!s) return ''
  const parts = [s.kind === 'group' ? '群聊' : s.kind === 'friend' ? '私聊' : '系统通知']
  if (s.kind === 'group' && s.group_id) parts.push(shortId(s.group_id))
  if (s.kind === 'friend' && s.user_id) parts.push(shortId(s.user_id))
  if (s.count) parts.push(`共 ${s.count} 条`)
  return parts.join(' · ')
})
const headAvatarStyle = computed(() => avatarGradient(sessionAvatarKey(activeSession.value)))
const headInitials = computed(() =>
  activeSession.value?.kind === 'system' ? '通' : avatarInitials(headName.value),
)
// QQ 群 / 用户头像：其他平台或加载失败时回退渐变首字（按会话记录失败状态）
const avatarBroken = ref({})
function markAvatarBroken(s) {
  if (!s) return
  avatarBroken.value = { ...avatarBroken.value, [s.key]: true }
}
const headAvatarSrc = computed(() => sessionAvatarUrl(activeSession.value))

const allTime = computed(() => {
  const s = sessions.value[0]
  if (s) return sideTime(s.last_time)
  return allLogs.value[0] ? sideTime(allLogs.value[0].time) : ''
})
const allPreview = computed(() => {
  const s = sessions.value[0]
  if (s) {
    const head = sessionName(s) + '：'
    if (s.last_type === 'notice') {
      return head + ([s.title, friendlyPreview(s.last_text, 50)].filter(Boolean).join(' · ') || '通知')
    }
    const who = s.kind === 'group' && s.name ? s.name + '：' : ''
    return head + who + friendlyPreview(s.last_text, 60)
  }
  const log = allLogs.value[0]
  if (!log) return '暂无消息'
  if (log.type === 'notice') return [log.title, friendlyPreview(log.text, 50)].filter(Boolean).join(' · ')
  const who = log.nickname || shortId(log.user_id)
  return (who ? who + '：' : '') + friendlyPreview(log.text, 60)
})
const lastUpdatedText = computed(() => (lastUpdated.value ? clock(lastUpdated.value) : '—'))

/* ---------------- 通用格式化 ---------------- */

// 与后端 msglog.Entry.SessionKey 一致：群消息 / 群通知归入群会话，其余按用户 ID。
function logSessionKey(log) {
  if (log.type === 'group' || (log.type === 'notice' && log.group_id)) return 'g:' + log.group_id
  if (log.type === 'friend' || (log.type === 'notice' && log.user_id)) return 'f:' + log.user_id
  return 'n:system'
}

// 全部消息视图里混排多个会话时，给消息标出所属群 / 好友。
function contextForLog(log) {
  const s = sessionIndex.value.get(logSessionKey(log))
  return s ? sessionName(s) : ''
}

function sessionName(s) {
  if (!s) return '全部消息'
  if (s.kind === 'system') return '系统通知'
  const mapped = lookupContactName(s)
  if (mapped) return mapped
  if (s.kind === 'group') return '群聊 ' + shortId(s.group_id || s.key)
  return s.name || '用户 ' + shortId(s.user_id || s.key)
}

// 通讯录接口返回的群 / 好友 ID 可能带平台前缀（qq:123），也可能是裸数字，
// 两种都试一遍，避免群名匹配不上只能显示 ID。
function lookupContactName(s) {
  const keys = []
  if (s.kind === 'group') {
    const raw = String(s.group_id || s.key.replace(/^g:/, ''))
    keys.push('g:' + raw)
    if (raw.startsWith('qq:')) keys.push('g:' + raw.slice(3))
    else if (/^\d+$/.test(raw)) keys.push('g:qq:' + raw)
  } else if (s.kind === 'friend') {
    const raw = String(s.user_id || s.key.replace(/^f:/, ''))
    keys.push('f:' + raw)
    if (raw.startsWith('qq:')) keys.push('f:' + raw.slice(3))
    else if (/^\d+$/.test(raw)) keys.push('f:qq:' + raw)
  }
  for (const key of keys) {
    if (nameMap.value[key]) return nameMap.value[key]
  }
  return ''
}

function sessionPreview(s) {
  if (!s) return ''
  const tag = { image: '[图片]', file: '[文件]', record: '[语音]', video: '[视频]' }[s.last_media] || ''
  const text = friendlyPreview(s.last_text, 90)
  let preview = text
  if (!preview && tag) preview = tag
  else if (tag && !preview.startsWith('[')) preview = tag + ' ' + preview
  if (s.last_type === 'notice') {
    return [s.title, preview].filter(Boolean).join(' · ') || '通知'
  }
  // 群聊预览带上最后一位发言人，未解析出群名时也能快速认出上下文
  if (s.kind === 'group' && s.name && preview && !preview.startsWith(s.name + '：')) {
    preview = s.name + '：' + preview
  }
  return preview || '（无内容）'
}

function clock(value) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' })
  if (sameDay) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function sideTime(value) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' })
  }
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1)
  if (d.toDateString() === yesterday.toDateString()) return '昨天'
  if (now.getTime() - d.getTime() < 7 * 86400000) {
    return '周' + '日一二三四五六'[d.getDay()]
  }
  return `${d.getMonth() + 1}/${d.getDate()}`
}

function dayKey(value) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return 'unknown'
  return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`
}

function dayLabel(value) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const now = new Date()
  const base = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const target = new Date(d.getFullYear(), d.getMonth(), d.getDate())
  const diff = Math.round((base - target) / 86400000)
  if (diff === 0) return '今天'
  if (diff === 1) return '昨天'
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1} 月 ${d.getDate()} 日`
  return `${d.getFullYear()} 年 ${d.getMonth() + 1} 月 ${d.getDate()} 日`
}

/* ---------------- 未读状态（仅本地记录） ---------------- */

function readSeen() {
  try {
    return JSON.parse(localStorage.getItem(SEEN_KEY) || '{}') || {}
  } catch {
    return {}
  }
}
function isUnread(s) {
  return (s.last_id || 0) > (seen[s.key] || 0)
}
function markSeen(key) {
  const s = sessions.value.find((x) => x.key === key)
  if (!s) return
  if ((s.last_id || 0) > (seen[key] || 0)) {
    seen[key] = s.last_id
    try {
      localStorage.setItem(SEEN_KEY, JSON.stringify(seen))
    } catch { /* 隐私模式下忽略 */ }
  }
}
function markActiveSeen() {
  if (!isAll.value) markSeen(activeKey.value)
}

// 首次打开面板时，把尚未记录过的会话标记为已读：历史日志不应全部顶着红点，
// 只有之后新到的消息才算未读。用独立标记区分「第一次访问」与「新增会话」：
// 后者没有 seen 记录时应显示未读。
let seenSeeded = false
function seedSeen() {
  if (seenSeeded) return
  seenSeeded = true
  try {
    if (localStorage.getItem(SEED_KEY)) return
    localStorage.setItem(SEED_KEY, '1')
  } catch { /* 隐私模式下忽略，本次仍按首次访问处理 */ }
  let changed = false
  for (const s of sessions.value) {
    if (seen[s.key] === undefined) {
      seen[s.key] = s.last_id
      changed = true
    }
  }
  if (changed) {
    try {
      localStorage.setItem(SEEN_KEY, JSON.stringify(seen))
    } catch { /* 忽略 */ }
  }
}

/* ---------------- 数据加载 ---------------- */

function readActive() {
  try {
    return localStorage.getItem(ACTIVE_KEY) || ''
  } catch {
    return ''
  }
}
function persistActive() {
  try {
    localStorage.setItem(ACTIVE_KEY, activeKey.value)
  } catch { /* 忽略 */ }
}

async function loadSessions() {
  try {
    const res = await api.getMsgLogSessions()
    sessions.value = res.items || []
    // 会话可能因容量淘汰 / 进程重启而消失：回退到全部消息视图
    if (!isAll.value && !sessions.value.some((s) => s.key === activeKey.value)) {
      activeKey.value = 'all'
      persistActive()
    }
  } catch {
    // 旧版后端没有会话接口：退化为只有「全部消息」
  }
}

async function loadAll({ initial = false } = {}) {
  const gen = refreshGen
  try {
    const page = await api.getMsgLogs({ limit: PAGE })
    if (gen !== refreshGen) return // 期间删过消息：旧响应会把已删的条目写回来
    const items = page.items || []
    if (initial || !allLogs.value.length) {
      allLogs.value = keepLatest(items)
      hasMoreAll.value = !!page.has_more
      return
    }
    const latest = allLogs.value[0]?.id || 0
    const fresh = items.filter((x) => x.id > latest)
    if (fresh.length) allLogs.value = keepLatest([...fresh, ...allLogs.value])
  } catch { /* 保持上一次数据 */ } finally {
    lastUpdated.value = Date.now()
  }
}

// keepLatest 只保留最新的 n 条（列表为新在前），丢弃已被服务端淘汰的旧条目。
function keepLatest(list, n = MAX_KEEP) {
  return list.length > n ? list.slice(0, n) : list
}

function ensureStore(key) {
  if (!sessionStore[key]) {
    sessionStore[key] = { items: [], hasMore: true, loaded: false, loading: false }
  }
  return sessionStore[key]
}

async function loadSession(key) {
  const store = ensureStore(key)
  if (store.loading) return
  const gen = refreshGen
  store.loading = true
  try {
    const page = await api.getMsgLogs({ session: key, limit: PAGE })
    if (gen !== refreshGen) return // 期间删过消息：丢弃过期响应
    store.items = keepLatest(page.items || [])
    store.hasMore = !!page.has_more
    store.loaded = true
    lastUpdated.value = Date.now()
  } catch { /* 保留旧数据 */ } finally {
    store.loading = false
  }
}

async function loadOlder() {
  if (loadingMore.value) return
  const gen = refreshGen
  if (isAll.value) {
    if (!hasMoreAll.value || !allLogs.value.length) return
    loadingMore.value = true
    const el = listEl.value
    const prevHeight = el?.scrollHeight || 0
    try {
      const oldest = allLogs.value[allLogs.value.length - 1].id
      const page = await api.getMsgLogs({ limit: PAGE, before: oldest })
      if (gen !== refreshGen) return // 期间删过消息：丢弃过期响应
      const items = (page.items || []).filter((x) => x.id < oldest)
      hasMoreAll.value = !!page.has_more && items.length > 0
      if (items.length) {
        allLogs.value = [...allLogs.value, ...items]
        if (!isAll.value) return
        await nextTick()
        if (el) el.scrollTop = el.scrollHeight - prevHeight + el.scrollTop
      }
    } catch { /* 下次滚动重试 */ } finally {
      loadingMore.value = false
    }
    return
  }

  const store = sessionStore[activeKey.value]
  if (!store || !store.loaded || !store.hasMore || !store.items.length) return
  const key = activeKey.value
  loadingMore.value = true
  const el = listEl.value
  const prevHeight = el?.scrollHeight || 0
  try {
    const oldest = store.items[store.items.length - 1].id
    const page = await api.getMsgLogs({ session: key, limit: PAGE, before: oldest })
    if (gen !== refreshGen) return // 期间删过消息：丢弃过期响应
    const items = (page.items || []).filter((x) => x.id < oldest)
    store.hasMore = !!page.has_more && items.length > 0
    if (items.length) {
      store.items = [...store.items, ...items]
      if (activeKey.value !== key) return
      await nextTick()
      if (el) el.scrollTop = el.scrollHeight - prevHeight + el.scrollTop
    }
  } catch { /* 下次滚动重试 */ } finally {
    loadingMore.value = false
  }
}

/* ---------------- 实时刷新 ---------------- */

// refreshGen 数据代次：删除会就地改动列表，在途的刷新 / 翻页响应基于删除前的
// 数据，写回列表会让已删除的记录重新出现，因此代次变化后一律丢弃旧响应。
let refreshGen = 0

async function refreshAll() {
  const gen = refreshGen
  const latest = allLogs.value[0]?.id || 0
  const stick = atBottom()
  let fresh = []
  let cursor = 0
  for (let round = 0; round < 5; round++) {
    const page = await api.getMsgLogs({ limit: PAGE, ...(cursor ? { before: cursor } : {}) })
    const items = page.items || []
    if (!items.length) break
    fresh = [...fresh, ...items.filter((x) => x.id > latest)]
    // 翻到了已知区间（出现 <= latest 的条目）说明最新数据已经连续，无需继续
    const overlap = items.some((x) => x.id <= latest)
    if (overlap || !page.has_more) break
    cursor = items[items.length - 1].id
    if (cursor <= latest + 1) break
  }
  if (!fresh.length || gen !== refreshGen) return
  allLogs.value = keepLatest([...fresh, ...allLogs.value])
  if (!isAll.value) return // 请求期间已切到会话视图：只更新缓存，不改动当前视图的滚动
  await afterIncoming(fresh.length, stick)
}

async function refreshSession(key) {
  const store = sessionStore[key]
  if (!store || !store.loaded || store.loading) return
  const gen = refreshGen
  const latest = store.items[0]?.id || 0
  const stick = atBottom()
  const page = await api.getMsgLogs({ session: key, limit: PAGE, ...(latest ? { after: latest } : {}) })
  if (gen !== refreshGen) return // 期间删过消息：丢弃过期响应
  const items = (page.items || []).filter((x) => x.id > latest)
  if (!items.length) return
  // 一次来了超过一页的新消息：直接重载最新一页，避免中间出现空洞
  if (items.length >= PAGE && page.has_more) {
    await loadSession(key)
    if (key === activeKey.value) await afterIncoming(PAGE, stick)
    return
  }
  store.items = keepLatest([...items, ...store.items])
  if (key !== activeKey.value) return
  await afterIncoming(items.length, stick)
}

async function afterIncoming(count, stick) {
  lastUpdated.value = Date.now()
  if (stick) {
    pendingNew.value = 0
    await nextTick()
    scrollToBottom()
    markActiveSeen()
    return
  }
  // 用户正在上翻历史：新消息插到列表顶部会把内容整体往下推，补偿滚动位置，
  // 保持当前阅读位置不跳动，同时提示还有新消息。
  const el = listEl.value
  const prevHeight = el ? el.scrollHeight : 0
  await nextTick()
  if (el) el.scrollTop += el.scrollHeight - prevHeight
  pendingNew.value += count
}

async function refreshTick() {
  if (document.hidden || !autoRefresh.value || loading.value || refreshing) return
  refreshing = true
  try {
    await loadSessions()
    if (isAll.value) {
      await refreshAll()
    } else if (!sessionStore[activeKey.value]?.loaded) {
      await loadSession(activeKey.value)
    } else {
      await refreshSession(activeKey.value)
    }
    lastUpdated.value = Date.now()
  } catch { /* 网络抖动时静默 */ } finally {
    refreshing = false
  }
}

async function manualRefresh() {
  if (refreshing) return
  refreshing = true
  try {
    await loadSessions()
    if (isAll.value) await refreshAll()
    else if (!sessionStore[activeKey.value]?.loaded) await loadSession(activeKey.value)
    else await refreshSession(activeKey.value)
  } finally {
    refreshing = false
  }
  if (!Object.keys(nameMap.value).length) loadNames()
}

async function loadNames() {
  try {
    nameMap.value = await api.getContactNameMap()
  } catch { /* 通讯录不可用时回退为 ID 命名 */ }
}

/* ---------------- 滚动 / 会话切换 ---------------- */

function atBottom() {
  const el = listEl.value
  if (!el) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight < 80
}

function scrollToBottom(smooth = false) {
  const el = listEl.value
  if (!el) return
  autoStick = true
  el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
}

function onScroll() {
  const el = listEl.value
  if (!el) return
  if (el.scrollTop < 80 && !loadingMore.value) loadOlder()
  if (atBottom()) {
    pendingNew.value = 0
    autoStick = true
    markActiveSeen()
  } else {
    autoStick = false
  }
}

// 图片异步加载会把气泡撑高；如果用户本来就在底部，补一次滚动避免最新消息被顶出视口。
async function onMediaLoad() {
  if (!autoStick) return
  await nextTick()
  scrollToBottom()
}

function jumpToLatest() {
  pendingNew.value = 0
  scrollToBottom(true)
  markActiveSeen()
}

async function selectSession(key) {
  if (activeKey.value === key) {
    mobileChat.value = true
    if (key !== 'all' && !sessionStore[key]?.loaded) await loadSession(key)
    return
  }
  activeKey.value = key
  pendingNew.value = 0
  mobileChat.value = true
  persistActive()
  if (key === 'all') {
    await loadAll({ initial: !allLogs.value.length })
  } else {
    const store = ensureStore(key)
    if (!store.loaded) await loadSession(key)
    else await refreshSession(key)
    markSeen(key)
  }  await nextTick()
  scrollToBottom()
  maybeFill()
}

// 首屏 / 消息较少时内容撑不满滚动区，自动补一页更早的消息（最多 3 页）。
async function maybeFill() {
  for (let i = 0; i < 3; i++) {
    const el = listEl.value
    if (!el || loadingMore.value || !hasMoreActive.value) return
    if (el.scrollHeight > el.clientHeight + 8) return
    await loadOlder()
    await nextTick()
  }
}

/* ---------------- 图片预览 ---------------- */

const lightbox = ref(null) // { items: [{ media, ref }], index }
const lightboxCurrent = computed(() => {
  const box = lightbox.value
  if (!box || !box.items?.length) return null
  return box.items[Math.min(box.index, box.items.length - 1)] || null
})

function openPreview(payload) {
  const items = payload.items || []
  if (!items.length) return
  lightbox.value = { items, index: Math.min(payload.index || 0, items.length - 1) }
}
function closePreview() {
  lightbox.value = null
}
function stepPreview(step) {
  if (!lightbox.value) return
  const len = lightbox.value.items.length
  lightbox.value = { ...lightbox.value, index: (lightbox.value.index + step + len) % len }
}
function onKeydown(e) {
  if (e.key === 'Escape' && contextMenu.value) {
    closeContextMenu()
    return
  }
  if (!lightbox.value) return
  if (e.key === 'Escape') closePreview()
  else if (e.key === 'ArrowLeft') stepPreview(-1)
  else if (e.key === 'ArrowRight') stepPreview(1)
}

/* ---------------- 右键菜单：消息 / 会话 ---------------- */

const contextMenu = ref(null) // { x, y, type: 'message' | 'session', log?, session? }
const contextMenuLabel = computed(() => {
  const menu = contextMenu.value
  if (!menu) return ''
  if (menu.type === 'session') return sessionName(menu.session)
  const log = menu.log
  if (!log) return ''
  if (log.type === 'notice') return log.title || '通知'
  return log.nickname || shortId(log.user_id) || '消息'
})

// 在光标处弹出；靠近视口右 / 下边缘时向内收，避免菜单被裁掉
function menuPosition(event, height = 124) {
  const width = 176
  const x = Math.min(event.clientX, window.innerWidth - width - 8)
  const y = Math.min(event.clientY, window.innerHeight - height - 8)
  return { x: Math.max(8, x), y: Math.max(8, y) }
}

function openContextMenu({ event, log }) {
  if (!log || !log.id) return
  contextMenu.value = { ...menuPosition(event), type: 'message', log }
}

function openSessionMenu(event, session) {
  if (!session?.key) return
  contextMenu.value = { ...menuPosition(event), type: 'session', session }
}

function closeContextMenu() {
  contextMenu.value = null
}

// 点击空白处 / 滚动 / 缩放窗口时收起菜单（菜单自身的 mousedown 已 stop）
function onDocMouseDown() {
  if (contextMenu.value) closeContextMenu()
}

async function copyLogText(log) {
  closeContextMenu()
  const text = [log?.title, log?.text].filter(Boolean).join(' · ').trim()
  if (!text) {
    Notification.warning({ message: '这条消息没有可复制的文本' })
    return
  }
  if (await writeClipboard(text)) {
    Notification.success({ message: '已复制消息文本' })
  } else {
    Notification.error({ message: '复制失败，请手动选中文本复制' })
  }
}

// 群号 / QQ 号对排查问题最有用；系统通知没有 ID 时退回会话键
async function copySessionId(session) {
  closeContextMenu()
  const text = String(session?.group_id || session?.user_id || session?.key || '')
  if (!text) return
  if (await writeClipboard(text)) {
    Notification.success({ message: '已复制会话 ID' })
  } else {
    Notification.error({ message: '复制失败，请手动选中复制' })
  }
}

// 优先用异步剪贴板（https / localhost）；失败时退回临时 textarea 方案
async function writeClipboard(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch { /* 非安全上下文或权限被拒：走兜底方案 */ }
  try {
    const el = document.createElement('textarea')
    el.value = text
    el.setAttribute('readonly', '')
    el.style.position = 'fixed'
    el.style.top = '-1000px'
    el.style.opacity = '0'
    document.body.appendChild(el)
    el.select()
    const ok = document.execCommand('copy')
    el.remove()
    return ok
  } catch {
    return false
  }
}

// 删除一条消息记录：只影响面板留存的日志，不会撤回平台上的原消息
async function deleteLog(log) {
  closeContextMenu()
  if (!log || !log.id) return
  const who = log.type === 'notice'
    ? (log.title || '通知')
    : (log.nickname || shortId(log.user_id) || '未知用户')
  const preview = friendlyPreview(log.text, 60)
  const ok = await confirmAction({
    title: '删除消息记录',
    message: `确定删除这条消息记录吗？\n${who}${preview ? '：' + preview : ''}\n\n只会删除面板本地的日志，平台上的消息不受影响，删除后不可恢复。`,
    confirmText: '删除',
  })
  if (!ok) return
  try {
    await api.deleteMsgLog(log.id)
  } catch (e) {
    Notification.error({ message: e.message || '删除失败' })
    return
  }
  refreshGen += 1 // 丢弃删除前的在途刷新响应
  removeLogLocally(log.id)
  Notification.success({ message: '已删除消息记录' })
  // 会话摘要（条数 / 最后一条预览）由后端重建，顺带处理「会话已空」的回落
  await loadSessions()
}

// 删除整个会话的消息记录：会话从侧栏消失，平台上的消息不受影响
async function deleteSession(session) {
  closeContextMenu()
  if (!session?.key) return
  const name = sessionName(session)
  const count = Number(session.count) || 0
  const ok = await confirmAction({
    title: '删除会话记录',
    message: `确定删除「${name}」的全部消息记录吗？${count ? `共 ${count} 条，` : ''}删除后不可恢复。\n\n只会删除面板本地的日志，平台上的消息不受影响。`,
    confirmText: '删除会话记录',
  })
  if (!ok) return
  let removed = 0
  try {
    const res = await api.deleteMsgLogSession(session.key)
    removed = Number(res.removed) || 0
  } catch (e) {
    Notification.error({ message: e.message || '删除失败' })
    return
  }
  refreshGen += 1 // 丢弃删除前的在途刷新响应
  removeSessionLocally(session.key)
  const wasActive = activeKey.value === session.key
  pendingNew.value = 0
  Notification.success({ message: `已删除 ${removed} 条消息记录` })
  // 会话列表由后端重建：清空后的会话会从侧栏消失（当前会话若被删空则回落到「全部消息」）
  await loadSessions()
  await nextTick()
  if (wasActive && activeKey.value === session.key) {
    // 删除期间该会话又收到了新消息（后端仍返回它）：重新拉一页
    await loadSession(session.key)
  }
  if (isAll.value) {
    await loadAll()
    scrollToBottom()
    maybeFill()
  }
}

// 本地先摘掉这条日志，避免等下一轮轮询才消失
function removeLogLocally(id) {
  allLogs.value = allLogs.value.filter((x) => x.id !== id)
  for (const store of Object.values(sessionStore)) {
    if (!store?.items?.length) continue
    const next = store.items.filter((x) => x.id !== id)
    if (next.length !== store.items.length) store.items = next
  }
}

// 本地先清掉整个会话：「全部消息」流水里该会话的记录与按会话缓存一起移除
function removeSessionLocally(key) {
  allLogs.value = allLogs.value.filter((x) => logSessionKey(x) !== key)
  delete sessionStore[key]
}

/* ---------------- 生命周期 ---------------- */

function onVisible() {
  if (!document.hidden && autoRefresh.value) refreshTick()
}

watch(typeFilter, async () => {
  pendingNew.value = 0
  await nextTick()
  scrollToBottom()
  maybeFill()
})

watch(autoRefresh, (on) => {
  if (on) refreshTick()
})

watch(lightbox, (value) => {
  document.body.style.overflow = value ? 'hidden' : ''
})

onMounted(async () => {
  loading.value = true
  // 通讯录名称（群名 / 备注）只影响展示，后台加载，不阻塞消息渲染
  loadNames()
  try {
    await Promise.all([loadSessions(), loadAll({ initial: true })])
    seedSeen()
    const saved = readActive()
    const valid = saved === 'all' || sessions.value.some((s) => s.key === saved)
    const key = valid ? saved : (sessions.value[0]?.key || 'all')
    activeKey.value = key
    if (key !== 'all') {
      await loadSession(key)
      markSeen(key)
    }
  } finally {
    loading.value = false
  }
  await nextTick()
  scrollToBottom()
  lastUpdated.value = Date.now()
  maybeFill()

  timer = setInterval(refreshTick, 3000)
  document.addEventListener('visibilitychange', onVisible)
  document.addEventListener('keydown', onKeydown)
  document.addEventListener('mousedown', onDocMouseDown)
  window.addEventListener('scroll', closeContextMenu, true)
  window.addEventListener('resize', closeContextMenu)
})

onUnmounted(() => {
  clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisible)
  document.removeEventListener('keydown', onKeydown)
  document.removeEventListener('mousedown', onDocMouseDown)
  window.removeEventListener('scroll', closeContextMenu, true)
  window.removeEventListener('resize', closeContextMenu)
  document.body.style.overflow = ''
})
</script>

<style scoped>
/* ================= 外层骨架 ================= */
.msglog-shell {
  display: flex;
  height: calc(100dvh - 9rem);
  min-height: 520px;
  border-radius: 22px;
  border: 2px solid var(--line-strong);
  background: var(--surface);
  box-shadow: var(--shadow-base);
  overflow: hidden;
}

/* ================= 左侧会话列表 ================= */
.conv-pane {
  width: 300px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  border-right: 2px solid var(--line);
  background:
    radial-gradient(circle, rgb(196 184 158 / 0.2) 1px, transparent 1px) 0 0 / 18px 18px,
    linear-gradient(180deg, #fbf6e8 0%, #f7f0dd 100%);
}
.conv-head {
  padding: 14px 13px 10px;
  border-bottom: 1.5px dashed var(--line-strong);
}
.conv-heading {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 9px;
  padding: 0 2px;
}
.conv-heading-title {
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.12em;
  color: var(--ink);
}
.conv-heading-count {
  min-width: 18px;
  height: 17px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--surface-3);
  border: 1.5px solid var(--line-strong);
  color: var(--ink-soft);
  font-size: 10px;
  font-weight: 700;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.conv-search :deep(.animal-input__inner) {
  font-size: 12px;
}
.conv-scroll {
  flex: 1;
  overflow-y: auto;
  padding: 8px 8px 14px;
}
.conv-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px 9px;
  border: 2px solid transparent;
  border-radius: 14px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  transition: background-color 0.15s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}
.conv-item:hover {
  background: rgb(255 253 247 / 0.68);
}
.conv-item.active {
  background: var(--surface);
  border-color: var(--line-strong);
  box-shadow: 0 2px 0 0 rgb(224 210 182 / 0.7);
}
.conv-item.active .conv-name {
  color: var(--accent-strong);
}
.conv-avatar {
  position: relative;
  width: 40px;
  height: 40px;
  flex-shrink: 0;
  border-radius: 13px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 14px;
  font-weight: 800;
  text-shadow: 0 1px 2px rgb(94 68 35 / 0.26);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.35), 0 2px 0 0 rgb(94 68 35 / 0.12);
  user-select: none;
}
.conv-avatar.all-avatar,
.head-avatar.all-avatar {
  background: linear-gradient(145deg, var(--accent-hover), var(--accent));
  color: #fff;
}
/* QQ 真实头像：填满圆角块；加载失败时模板回退到渐变首字 */
.conv-avatar-img,
.head-avatar-img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  border-radius: inherit;
}
.conv-avatar.system-avatar {
  color: #fff;
}
.unread-dot {
  position: absolute;
  top: -2px;
  right: -2px;
  width: 9px;
  height: 9px;
  border-radius: 999px;
  background: var(--danger);
  border: 2px solid var(--surface);
}
.conv-main {
  min-width: 0;
  flex: 1;
}
.conv-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.conv-row + .conv-row {
  margin-top: 2px;
}
.conv-name {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  font-weight: 700;
  color: var(--ink-body);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.conv-name.unread {
  color: var(--ink);
}
.conv-time {
  flex-shrink: 0;
  font-size: 10px;
  color: var(--ink-muted);
  font-family: var(--font-mono);
}
.conv-preview {
  flex: 1;
  min-width: 0;
  font-size: 11px;
  color: var(--ink-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.unread-mark {
  flex-shrink: 0;
  font-size: 9px;
  font-weight: 800;
  color: #fff;
  background: var(--danger);
  border-radius: 999px;
  padding: 1px 5px;
  line-height: 1.3;
}
.conv-section {
  margin-top: 10px;
}
.conv-section-title {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px 3px;
  color: var(--ink-muted);
  font-size: 10px;
  font-weight: 800;
  letter-spacing: 0.1em;
}
.conv-section-count {
  font-weight: 700;
  color: var(--ink-disabled);
  letter-spacing: 0;
}
.conv-loading,
.conv-empty {
  padding: 26px 12px;
  text-align: center;
  color: var(--ink-muted);
  font-size: 11.5px;
  line-height: 1.7;
}
.conv-empty-ico {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 44px;
  height: 44px;
  margin: 0 auto 7px;
  border-radius: 14px;
  background: var(--surface-2);
  border: 1.5px solid var(--line);
  color: var(--ink-muted);
}
.conv-empty small {
  display: block;
  color: var(--ink-disabled);
  font-size: 10px;
}

/* ================= 右侧聊天区 ================= */
.chat-pane {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: var(--surface);
}
.chat-head {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 11px 16px;
  border-bottom: 2px solid var(--line);
  background: rgb(255 253 247 / 0.9);
  backdrop-filter: blur(4px);
}
.back-btn {
  display: none;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border-radius: 10px;
  border: 2px solid var(--line);
  background: var(--surface);
  color: var(--ink-soft);
  cursor: pointer;
}
.head-avatar {
  width: 40px;
  height: 40px;
  flex-shrink: 0;
  border-radius: 13px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 14px;
  font-weight: 800;
  text-shadow: 0 1px 2px rgb(94 68 35 / 0.26);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.35), 0 2px 0 0 rgb(94 68 35 / 0.12);
  user-select: none;
}
.head-main {
  min-width: 0;
  flex: 1;
}
.head-title {
  font-size: 14px;
  font-weight: 800;
  color: var(--ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.head-sub {
  margin-top: 1px;
  font-size: 10.5px;
  color: var(--ink-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.type-tabs {
  display: inline-flex;
  align-items: center;
  gap: 1px;
  padding: 2px;
  border-radius: 999px;
  background: var(--surface-2);
  border: 1.5px solid var(--line);
}
.type-tabs button {
  padding: 3px 9px;
  border: none;
  border-radius: 999px;
  background: transparent;
  color: var(--ink-soft);
  font-size: 10.5px;
  font-weight: 700;
  cursor: pointer;
  line-height: 1.5;
}
.type-tabs button:hover {
  color: var(--ink);
}
.type-tabs button.active {
  background: var(--accent);
  color: #fff;
  box-shadow: 0 1.5px 0 0 var(--accent-strong);
}
.auto-refresh {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 10.5px;
  color: var(--ink-soft);
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 29px;
  height: 29px;
  border-radius: 9px;
  border: 2px solid var(--line);
  background: var(--surface);
  color: var(--ink-soft);
  cursor: pointer;
  transition: color 0.15s ease, border-color 0.15s ease;
}
.icon-btn:hover {
  color: var(--accent-strong);
  border-color: var(--accent);
}

.chat-body {
  position: relative;
  flex: 1;
  min-height: 0;
}
.chat-scroll {
  height: 100%;
  overflow-y: auto;
  padding: 4px 20px 18px;
  background-image:
    radial-gradient(40rem 24rem at 100% 0%, rgb(25 200 185 / 0.05), transparent 60%),
    radial-gradient(32rem 20rem at 0% 100%, rgb(247 205 103 / 0.08), transparent 60%),
    radial-gradient(circle, rgb(196 184 158 / 0.17) 1px, transparent 1px);
  background-size: auto, auto, 20px 20px;
}
.chat-inner {
  max-width: 820px;
  margin: 0 auto;
  padding-bottom: 4px;
}
.load-tip {
  padding: 10px 0 2px;
  text-align: center;
  font-size: 10px;
  color: var(--ink-disabled);
  letter-spacing: 0.02em;
}
.day-divider {
  display: flex;
  justify-content: center;
  margin: 16px 0 4px;
}
.day-divider span {
  padding: 3px 12px;
  border-radius: 999px;
  background: rgb(255 253 247 / 0.85);
  border: 1.5px solid var(--line);
  color: var(--ink-muted);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.06em;
}
.time-divider {
  display: flex;
  justify-content: center;
  margin: 10px 0 0;
}
.time-divider span {
  font-size: 9.5px;
  color: var(--ink-disabled);
  font-family: var(--font-mono);
}
.chat-empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 5px;
  color: var(--ink-muted);
  text-align: center;
  padding: 24px;
}
.chat-empty p {
  margin: 2px 0 0;
  font-size: 13px;
  font-weight: 700;
  color: var(--ink-soft);
}
.chat-empty small {
  max-width: 380px;
  font-size: 10.5px;
  line-height: 1.7;
  color: var(--ink-disabled);
}
.empty-ico {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 66px;
  height: 66px;
  margin-bottom: 4px;
  border-radius: 22px;
  background: var(--accent-tint);
  border: 2px solid #cdeee8;
  color: var(--accent-strong);
  box-shadow: 0 3px 0 0 #d8ece6;
}
.empty-spinner {
  width: 22px;
  height: 22px;
  border-radius: 999px;
  border: 3px solid rgb(196 184 158 / 0.35);
  border-top-color: var(--accent);
  animation: spin 0.8s linear infinite;
}
@keyframes spin {
  to { transform: rotate(360deg); }
}
.new-pill {
  position: absolute;
  right: 26px;
  bottom: 14px;
  z-index: 5;
  padding: 6px 14px;
  border-radius: 999px;
  border: 2px solid var(--accent);
  background: var(--accent);
  color: #fff;
  font-size: 11px;
  font-weight: 700;
  cursor: pointer;
  box-shadow: 0 3px 0 0 var(--accent-strong), var(--shadow-sm);
}
.new-pill:hover {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.new-pill:active {
  transform: translateY(2px);
  box-shadow: 0 1px 0 0 var(--accent-strong);
}
.chat-foot {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 7px 16px;
  border-top: 1.5px solid var(--line);
  background: var(--surface);
  color: var(--ink-muted);
  font-size: 10px;
  white-space: nowrap;
  overflow: hidden;
}
.live-dot {
  width: 6px;
  height: 6px;
  border-radius: 999px;
  background: var(--ok);
  box-shadow: 0 0 0 3px rgb(111 186 44 / 0.16);
  flex-shrink: 0;
  animation: breathe 2.4s ease-out infinite;
}
.live-dot.paused {
  background: var(--ink-disabled);
  box-shadow: none;
  animation: none;
}
@keyframes breathe {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}
.foot-sep {
  color: var(--ink-disabled);
}
.foot-hint {
  color: var(--ink-disabled);
}
.foot-actions {
  margin-left: auto;
  flex-shrink: 0;
}
.foot-actions button {
  border: none;
  background: none;
  padding: 0;
  color: var(--accent-strong);
  font-size: 10px;
  font-weight: 700;
  cursor: pointer;
}
.foot-actions button:hover {
  text-decoration: underline;
  text-underline-offset: 2px;
}

/* ================= 图片预览 ================= */
.lightbox {
  position: fixed;
  inset: 0;
  z-index: 120;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px 64px;
  background: rgb(43 33 24 / 0.82);
  backdrop-filter: blur(6px);
}
.lightbox-figure {
  margin: 0;
  max-width: min(1180px, 92vw);
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
}
.lightbox-figure img {
  max-width: min(1180px, 92vw);
  max-height: calc(88vh - 54px);
  border-radius: 14px;
  object-fit: contain;
  box-shadow: 0 22px 60px -20px rgb(0 0 0 / 0.6);
  background: #1d1712;
}
.lightbox-bar {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 6px 16px;
  border-radius: 999px;
  background: rgb(255 253 247 / 0.12);
  border: 1px solid rgb(255 253 247 / 0.18);
  color: #f4ead7;
  font-size: 11.5px;
  backdrop-filter: blur(8px);
}
.lightbox-name {
  max-width: 340px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 700;
}
.lightbox-index {
  font-family: var(--font-mono);
  color: rgb(244 234 215 / 0.65);
}
.lightbox-bar a {
  color: #ffd977;
  text-decoration: none;
  font-weight: 700;
}
.lightbox-bar a:hover {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.lightbox-bar button {
  border: none;
  background: none;
  color: #f4ead7;
  font-size: 11.5px;
  font-weight: 700;
  cursor: pointer;
  opacity: 0.8;
}
.lightbox-bar button:hover {
  opacity: 1;
}
.lightbox-nav {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  width: 44px;
  height: 44px;
  border-radius: 999px;
  border: 1px solid rgb(255 253 247 / 0.22);
  background: rgb(255 253 247 / 0.12);
  color: #f4ead7;
  font-size: 28px;
  line-height: 1;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
}
.lightbox-nav:hover {
  background: rgb(255 253 247 / 0.22);
}
.lightbox-nav.prev {
  left: 18px;
}
.lightbox-nav.next {
  right: 18px;
}

/* ================= 消息右键菜单 ================= */
.ctx-menu {
  position: fixed;
  z-index: 130;
  min-width: 168px;
  padding: 5px;
  border-radius: 14px;
  border: 2px solid var(--line-strong);
  background: var(--surface);
  box-shadow: 0 16px 36px -16px rgb(70 48 20 / 0.45), 0 3px 0 0 rgb(224 210 182 / 0.7);
  animation: ctx-in 0.12s ease-out;
}
@keyframes ctx-in {
  from { opacity: 0; transform: translateY(-3px) scale(0.98); }
  to { opacity: 1; transform: none; }
}
.ctx-hint {
  max-width: 200px;
  padding: 3px 9px 5px;
  margin-bottom: 3px;
  border-bottom: 1.5px dashed var(--line);
  color: var(--ink-muted);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.02em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ctx-item {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  padding: 7px 9px;
  border: none;
  border-radius: 9px;
  background: transparent;
  color: var(--ink-body);
  font-size: 12px;
  font-weight: 700;
  text-align: left;
  cursor: pointer;
}
.ctx-item:hover {
  background: var(--surface-2);
}
.ctx-item.danger {
  color: var(--danger);
}
.ctx-item.danger:hover {
  background: rgb(215 87 87 / 0.1);
}

/* ================= 响应式 ================= */
@media (max-width: 1023px) {
  .msglog-shell {
    height: calc(100dvh - 8.25rem);
  }
  .conv-pane {
    width: 100%;
    border-right: none;
  }
  .msglog-shell.chat-open .conv-pane {
    display: none;
  }
  .msglog-shell:not(.chat-open) .chat-pane {
    display: none;
  }
  .back-btn {
    display: inline-flex;
  }
  .head-actions .auto-refresh span {
    display: none;
  }
  .chat-scroll {
    padding: 4px 12px 14px;
  }
  .chat-foot .foot-hint,
  .chat-foot .foot-sep-hint {
    display: none;
  }
  .lightbox {
    padding: 20px 12px;
  }
  .lightbox-nav {
    width: 36px;
    height: 36px;
    font-size: 22px;
  }
}

/* 窄屏：右侧操作（类型筛选 / 刷新）换到第二行，避免把标题与按钮挤出屏幕 */
@media (max-width: 640px) {
  .chat-head {
    flex-wrap: wrap;
    padding: 8px 10px;
    row-gap: 6px;
  }
  .head-main {
    flex: 1 1 60%;
  }
  .head-actions {
    width: 100%;
    justify-content: flex-end;
    gap: 8px;
  }
  .head-actions .type-tabs {
    margin-right: auto;
  }
  .type-tabs button {
    padding: 3px 8px;
  }
  .conv-head {
    padding: 12px 12px 9px;
  }
}
</style>
