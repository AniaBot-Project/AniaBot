<template>
  <!-- 通知事件：聊天软件里居中展示的系统提示 -->
  <div v-if="isNotice" class="msg-notice-wrap" @contextmenu="onContextMenu">
    <div class="msg-notice">
      <span class="notice-mark" />
      <span v-if="context" class="notice-context">{{ context }}</span>
      <span class="notice-title">{{ log.title || '通知' }}</span>
      <span class="notice-body">{{ preview }}</span>
      <span class="notice-time">{{ clock }}</span>
    </div>
  </div>

  <div v-else class="msg-row" :class="{ 'is-continued': !showAvatar }" @contextmenu="onContextMenu">
    <div v-if="showAvatar" class="msg-avatar" :style="avatarStyle" :title="displayName">
      <img
        v-if="avatarSrc && !avatarBroken"
        class="msg-avatar-img"
        :src="avatarSrc"
        alt=""
        loading="lazy"
        referrerpolicy="no-referrer"
        @error="avatarBroken = true"
      />
      <template v-else>{{ initials }}</template>
    </div>
    <div v-else class="msg-avatar-gap" />

    <div class="msg-main">
      <div v-if="showName" class="msg-head">
        <span class="msg-name" :title="userId ? '用户 ID：' + userId : displayName">{{ displayName }}</span>
        <span v-if="context && context !== displayName" class="msg-context">{{ context }}</span>
        <span class="msg-stamp">{{ clock }}</span>
      </div>

      <div class="msg-bubble" :class="{ 'has-media': media.length > 0 }">
        <template v-for="(b, bi) in blocks" :key="'b' + bi">
          <div v-if="b.type === 'text'" class="msg-text">
            <MsgLogTokens :tokens="b.tokens" />
          </div>

          <blockquote v-else-if="b.type === 'quote'" class="msg-quote">
            <div class="quote-head">
              <span class="quote-bar" />
              <span class="quote-name">{{ b.sender || '引用消息' }}</span>
            </div>
            <div class="quote-body">
              <MsgLogTokens :tokens="b.tokens" />
            </div>
          </blockquote>

          <details v-else-if="b.type === 'forward'" class="msg-forward">
            <summary>
              <span class="forward-ico">↪</span>
              <span>合并转发消息</span>
              <span class="forward-count">{{ b.count }} 条</span>
              <span class="forward-tip">展开</span>
            </summary>
            <div class="forward-body">
              <div
                v-for="(n, ni) in b.nodes"
                :key="ni"
                class="forward-line"
                :style="{ marginLeft: Math.min(n.indent, 10) * 4 + 'px' }"
              >
                <template v-if="n.type === 'node'">
                  <span class="forward-name">{{ n.name }}</span>
                  <span class="forward-colon">：</span>
                  <span>{{ n.text }}</span>
                </template>
                <span v-else class="forward-tag">{{ n.text }}</span>
              </div>
            </div>
          </details>
        </template>

        <!-- 图片：点击放大；加载失败时点击重试（面板会先向平台续期过期地址） -->
        <div v-if="images.length" class="msg-images" :class="imageLayout">
          <button
            v-for="(m, i) in images"
            :key="'img' + i"
            type="button"
            class="msg-img"
            :title="isFailed(m) ? '加载失败，点击重试' : '点击查看大图'"
            @click="isFailed(m) ? retry(m) : openImage(i)"
          >
            <img
              v-if="!isFailed(m)"
              :src="mediaUrl(m.url, '', false, refFor(m))"
              :alt="m.name || '图片'"
              loading="lazy"
              @load="$emit('media-load')"
              @error="markFailed(m)"
            />
            <span v-else class="img-broken">
              <Icon name="Image" :size="20" />
              <span>图片已失效</span>
              <small>点击重试</small>
            </span>
          </button>
        </div>

        <!-- 文件：一键下载 -->
        <div v-if="files.length" class="msg-files">
          <div v-for="(f, i) in files" :key="'file' + i" class="file-card" :class="'tone-' + fileBadge(f.name).tone">
            <span class="file-badge">{{ fileBadge(f.name).text }}</span>
            <span class="file-info">
              <span class="file-name" :title="f.name">{{ f.name }}</span>
              <span class="file-meta">
                {{ sizeText(f.size) || '大小未知' }}
                <template v-if="f.url"> · 可下载</template>
                <template v-else-if="f.file_id && log.id"> · 可获取链接</template>
                <template v-else> · 暂无下载地址</template>
              </span>
            </span>
            <a
              v-if="f.url"
              class="file-dl"
              :href="mediaUrl(f.url, f.name, true, refFor(f))"
              :download="f.name"
              @click.stop
            >
              <Icon name="Download" :size="13" />
              下载
            </a>
            <a
              v-else-if="f.file_id && log.id"
              class="file-dl"
              :href="mediaUrl('', f.name, true, refFor(f))"
              :download="f.name"
              @click.stop
            >
              <Icon name="Download" :size="13" />
              获取链接
            </a>
            <span v-else class="file-dl is-disabled">无链接</span>
          </div>
        </div>

        <!-- 语音 / 视频 -->
        <div v-if="records.length || videos.length" class="msg-av">
          <div v-for="(m, i) in records" :key="'rec' + i" class="av-card">
            <span class="av-ico"><Icon name="Mic" :size="15" /></span>
            <span class="av-label">语音消息</span>
            <a v-if="m.url" class="av-action" :href="mediaUrl(m.url, m.name, false, refFor(m))" target="_blank" rel="noopener noreferrer">播放</a>
            <span v-else class="av-action is-disabled">无链接</span>
          </div>
          <template v-for="(m, i) in videos" :key="'vid' + i">
            <video
              v-if="!isFailed(m)"
              class="msg-video"
              :src="mediaUrl(m.url, '', false, refFor(m))"
              controls
              preload="metadata"
              @error="markFailed(m)"
            />
            <div v-else class="av-card">
              <span class="av-label">视频加载失败</span>
              <button type="button" class="av-retry" @click="retry(m)">重试</button>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { Icon } from 'animal-island-vue'
import { msgMediaUrl } from '../api.js'
import { avatarGradient, avatarInitials, qqUserAvatarUrl } from '../utils/avatar.js'
import {
  friendlyPreview,
  parseForwardNodes,
  parseInline,
  pruneMediaTokens,
  shortId,
  splitMessageBlocks,
} from '../utils/messageText.js'
import MsgLogTokens from './MsgLogTokens.vue'

const props = defineProps({
  log: { type: Object, required: true },
  showAvatar: { type: Boolean, default: true },
  showName: { type: Boolean, default: true },
  // 全部消息视图里混排多个会话：用一枚小标签标出发言所属会话
  context: { type: String, default: '' },
})
const emit = defineEmits(['preview', 'media-load', 'contextmenu'])

// 右键菜单（复制 / 删除消息记录）由父组件统一渲染：这里只把事件与当前日志
// 交上去，并阻止浏览器自带的右键菜单。
function onContextMenu(e) {
  e.preventDefault()
  emit('contextmenu', { event: e, log: props.log })
}

const isNotice = computed(() => props.log.type === 'notice')
const clock = computed(() => fmtClock(props.log.time))
const preview = computed(() => friendlyPreview(props.log.text, 140))

const media = computed(() => (Array.isArray(props.log.media) ? props.log.media.filter((m) => m && m.kind) : []))
const images = computed(() => media.value.filter((m) => m.kind === 'image' && m.url))
const files = computed(() => media.value.filter((m) => m.kind === 'file'))
const records = computed(() => media.value.filter((m) => m.kind === 'record' && m.url))
const videos = computed(() => media.value.filter((m) => m.kind === 'video' && m.url))

const imageLayout = computed(() => {
  if (images.value.length === 1) return 'is-single'
  if (images.value.length === 2) return 'is-two'
  return 'is-grid'
})

const displayName = computed(() => {
  if (props.log.nickname) return props.log.nickname
  if (props.log.user_id) return '用户 ' + shortId(props.log.user_id)
  return '未知用户'
})
const userId = computed(() => (props.log.user_id ? shortId(props.log.user_id) : ''))
const initials = computed(() => avatarInitials(displayName.value))
const avatarStyle = computed(() => avatarGradient(props.log.user_id || displayName.value))
// QQ 消息显示真实头像；其他平台 / 取不到 QQ 号时回退渐变首字，加载失败同样回退
const avatarSrc = computed(() => qqUserAvatarUrl(props.log.user_id, 100))
const avatarBroken = ref(false)
watch(() => props.log.user_id, () => {
  avatarBroken.value = false
})

// 媒体在日志里的真实下标：给面板媒体代理带上日志 ID + 下标，旧地址失效时
// （QQ 的 rkey 过期）代理会先向平台续期再重试。
const mediaIndex = computed(() => {
  const map = new Map()
  const list = Array.isArray(props.log.media) ? props.log.media : []
  list.forEach((m, i) => {
    if (m) map.set(m, i)
  })
  return map
})
const attempts = ref({})

function mediaKey(m) {
  const index = mediaIndex.value.get(m)
  if (index === undefined) return m.url || ''
  return `${props.log.id || 0}:${index}`
}

function refFor(m) {
  const index = mediaIndex.value.get(m)
  if (index === undefined || !props.log.id) return null
  return { entry: props.log.id, index, attempt: attempts.value[mediaKey(m)] || 0 }
}

// 文本块与引用块共用一份媒体计数：文本里的 [图片] / [文件:…] 占位会被实际媒体吃掉。
const blocks = computed(() => {
  const counts = {
    image: images.value.length,
    file: files.value.length,
    record: records.value.length + videos.value.length,
  }
  return splitMessageBlocks(props.log.text).map((b) => {
    if (b.type === 'text' || b.type === 'quote') {
      return { ...b, tokens: pruneMediaTokens(parseInline(b.text), counts) }
    }
    if (b.type === 'forward') {
      return { ...b, nodes: parseForwardNodes(b.text) }
    }
    return b
  })
})

const failed = ref(new Set())
function isFailed(m) {
  return failed.value.has(mediaKey(m))
}
function markFailed(m) {
  const next = new Set(failed.value)
  next.add(mediaKey(m))
  failed.value = next
  emit('media-load')
}
// 重试：清掉失败标记并递增 attempt，媒体地址带上新参数以绕过浏览器缓存，
// 面板代理会同时重新向平台申请可用地址。
function retry(m) {
  const key = mediaKey(m)
  const next = new Set(failed.value)
  next.delete(key)
  failed.value = next
  attempts.value = { ...attempts.value, [key]: (attempts.value[key] || 0) + 1 }
}

function openImage(index) {
  const m = images.value[index]
  if (!m) return
  emit('preview', {
    items: images.value.map((item) => ({ media: item, ref: refFor(item) })),
    index,
  })
}

function mediaUrl(url, name = '', download = false, ref = null) {
  return msgMediaUrl(url, name, download, ref)
}

function fmtClock(value) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' })
}

function sizeText(bytes) {
  const n = Number(bytes) || 0
  if (n <= 0) return ''
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(n < 10 * 1024 ? 1 : 0) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

// 文件类型角标：颜色只做区分，不追求覆盖所有扩展名
const FILE_TONES = {
  pdf: 'pdf',
  doc: 'doc', docx: 'doc', txt: 'doc', md: 'doc', rtf: 'doc',
  xls: 'sheet', xlsx: 'sheet', csv: 'sheet', ppt: 'sheet', pptx: 'sheet',
  zip: 'zip', rar: 'zip', '7z': 'zip', tar: 'zip', gz: 'zip',
  png: 'img', jpg: 'img', jpeg: 'img', gif: 'img', webp: 'img', bmp: 'img', svg: 'img',
  mp3: 'audio', wav: 'audio', ogg: 'audio', m4a: 'audio', amr: 'audio', flac: 'audio',
  mp4: 'video', mov: 'video', mkv: 'video', avi: 'video', webm: 'video',
  exe: 'app', msi: 'app', apk: 'app', dmg: 'app',
}
function fileBadge(name) {
  const ext = String(name || '').split('.').pop().toLowerCase()
  const known = ext.length <= 5 && /^[a-z0-9]+$/.test(ext)
  return {
    text: known ? ext.toUpperCase() : 'FILE',
    tone: FILE_TONES[ext] || 'doc',
  }
}
</script>

<style scoped>
/* ---------- 通知 ---------- */
.msg-notice-wrap {
  display: flex;
  justify-content: center;
  margin: 12px 0;
}
.msg-notice {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  flex-wrap: wrap;
  max-width: 88%;
  padding: 6px 14px;
  border-radius: 999px;
  background: rgb(255 244 207 / 0.72);
  border: 1.5px dashed #ecd48c;
  color: var(--warn-ink);
  font-size: 11.5px;
  line-height: 1.5;
}
.notice-mark {
  width: 6px;
  height: 6px;
  border-radius: 999px;
  background: var(--warn);
  box-shadow: 0 0 0 3px rgb(245 195 28 / 0.18);
  flex-shrink: 0;
}
.notice-title {
  font-weight: 800;
  letter-spacing: 0.02em;
  color: #9a6a06;
}
.notice-context {
  color: #b79a63;
  font-weight: 700;
  max-width: 150px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.notice-body {
  color: #a88340;
  min-width: 0;
  overflow-wrap: anywhere;
}
.notice-time {
  color: #b79a63;
  font-family: var(--font-mono);
  font-size: 10.5px;
}

/* ---------- 消息行 ---------- */
.msg-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-top: 16px;
}
.msg-row.is-continued {
  margin-top: 4px;
}
.msg-avatar,
.msg-avatar-gap {
  width: 38px;
  height: 38px;
  flex-shrink: 0;
  margin-top: 1px;
}
.msg-avatar {
  border-radius: 13px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 14px;
  font-weight: 800;
  letter-spacing: 0.02em;
  text-shadow: 0 1px 2px rgb(94 68 35 / 0.28);
  box-shadow: 0 2px 0 0 rgb(94 68 35 / 0.16), inset 0 1px 0 rgb(255 255 255 / 0.35);
  user-select: none;
  overflow: hidden;
}
/* QQ 真实头像：填满圆角块，加载失败时组件回退到渐变首字 */
.msg-avatar-img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  border-radius: inherit;
}
.msg-avatar-gap {
  width: 38px;
  height: 38px;
}
.msg-main {
  min-width: 0;
  max-width: min(680px, 86%);
  display: flex;
  flex-direction: column;
  align-items: flex-start;
}
.msg-head {
  display: flex;
  align-items: baseline;
  gap: 6px;
  padding: 0 4px 3px;
  max-width: 100%;
}
.msg-name {
  font-size: 11.5px;
  font-weight: 800;
  color: #7c6547;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 180px;
}
.msg-context {
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 0 6px;
  border-radius: 999px;
  border: 1px solid var(--line);
  background: var(--surface-2);
  color: var(--ink-muted);
  font-size: 9.5px;
  line-height: 16px;
  font-weight: 600;
}
.msg-stamp {
  font-size: 10px;
  color: var(--ink-disabled);
  font-family: var(--font-mono);
  opacity: 0;
  transition: opacity 0.15s ease;
}
.msg-row:hover .msg-stamp {
  opacity: 1;
}
.msg-bubble {
  max-width: 100%;
  padding: 9px 13px;
  border-radius: 6px 17px 17px 17px;
  background: var(--surface);
  border: 2px solid var(--line);
  box-shadow: 0 2px 0 0 rgb(224 210 182 / 0.55);
  color: var(--ink-body);
  font-size: 13.5px;
  line-height: 1.68;
  overflow-wrap: anywhere;
}
.msg-bubble.has-media {
  padding-bottom: 11px;
}
.msg-text {
  white-space: pre-wrap;
  word-break: break-word;
}
.msg-text + .msg-text {
  margin-top: 4px;
}

/* ---------- 引用 ---------- */
.msg-quote {
  margin: 3px 0 6px;
  padding: 7px 10px 7px 11px;
  border-radius: 11px;
  background: var(--surface-2);
  border: 1.5px solid var(--line);
  position: relative;
}
.quote-head {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-bottom: 2px;
  font-size: 10.5px;
  color: var(--ink-soft);
  min-width: 0;
}
.quote-bar {
  width: 3px;
  height: 11px;
  border-radius: 3px;
  background: var(--accent);
  flex-shrink: 0;
}
.quote-name {
  font-weight: 700;
  color: #7c6547;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.quote-id {
  font-family: var(--font-mono);
  color: var(--ink-muted);
}
.quote-body {
  font-size: 12px;
  color: var(--ink-soft);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 96px;
  overflow: hidden;
}

/* ---------- 合并转发 ---------- */
.msg-forward {
  margin: 5px 0;
  border-radius: 12px;
  background: var(--surface-2);
  border: 1.5px solid var(--line-strong);
  overflow: hidden;
}
.msg-forward summary {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 8px 11px;
  cursor: pointer;
  font-size: 12px;
  font-weight: 700;
  color: #6b5538;
  list-style: none;
  user-select: none;
}
.msg-forward summary::-webkit-details-marker {
  display: none;
}
.msg-forward[open] summary {
  border-bottom: 1.5px dashed var(--line-strong);
}
.forward-ico {
  color: var(--accent-strong);
  font-weight: 800;
}
.forward-count {
  font-size: 10.5px;
  color: var(--ink-muted);
  font-weight: 600;
}
.forward-tip {
  margin-left: auto;
  font-size: 10px;
  color: var(--ink-muted);
  font-weight: 600;
}
.msg-forward[open] .forward-tip {
  opacity: 0;
}
.forward-body {
  padding: 8px 11px 10px;
  max-height: 240px;
  overflow-y: auto;
  font-size: 12px;
  line-height: 1.6;
  color: var(--ink-soft);
  white-space: pre-wrap;
  word-break: break-word;
}
.forward-line + .forward-line {
  margin-top: 2px;
}
.forward-name {
  color: #7c6547;
  font-weight: 700;
}
.forward-tag {
  color: var(--ink-muted);
  font-size: 11px;
}

/* ---------- 图片 ---------- */
.msg-images {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 7px;
}
.msg-img {
  display: block;
  min-width: 72px;
  min-height: 72px;
  padding: 0;
  border: 2px solid var(--line);
  border-radius: 12px;
  overflow: hidden;
  background: var(--surface-2);
  cursor: zoom-in;
  line-height: 0;
}
.msg-img:hover {
  border-color: var(--accent);
}
.msg-images.is-single .msg-img {
  max-width: min(280px, 100%);
}
.msg-images.is-single .msg-img img {
  display: block;
  max-width: min(280px, 100%);
  max-height: 240px;
  width: auto;
  height: auto;
  object-fit: cover;
}
.msg-images.is-two .msg-img,
.msg-images.is-grid .msg-img {
  width: 132px;
  height: 132px;
}
.msg-images.is-two .msg-img img,
.msg-images.is-grid .msg-img img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.img-broken {
  width: 100%;
  height: 100%;
  min-height: 84px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 3px;
  padding: 10px;
  color: var(--ink-muted);
  font-size: 10.5px;
  line-height: 1.3;
  text-align: center;
}
.img-broken small {
  font-size: 9.5px;
  color: var(--ink-disabled);
}

/* ---------- 文件 ---------- */
.msg-files {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 7px;
  min-width: 240px;
}
.file-card {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 8px 9px;
  border-radius: 12px;
  background: var(--surface-2);
  border: 1.5px solid var(--line-strong);
}
.file-badge {
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border-radius: 9px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 9.5px;
  font-weight: 800;
  letter-spacing: 0.02em;
  color: #fff;
  background: #9a8b72;
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.3);
}
.file-card.tone-pdf .file-badge { background: linear-gradient(150deg, #f2908b, #dd615b); }
.file-card.tone-doc .file-badge { background: linear-gradient(150deg, #9db8e8, #6f92d1); }
.file-card.tone-sheet .file-badge { background: linear-gradient(150deg, #9ed4a8, #5fae72); }
.file-card.tone-zip .file-badge { background: linear-gradient(150deg, #f0c481, #d9a04c); }
.file-card.tone-img .file-badge { background: linear-gradient(150deg, #7ed6c7, #35b5a5); }
.file-card.tone-audio .file-badge { background: linear-gradient(150deg, #c9b6ea, #a487d6); }
.file-card.tone-video .file-badge { background: linear-gradient(150deg, #f2b8d4, #dd86b1); }
.file-card.tone-app .file-badge { background: linear-gradient(150deg, #a8dce6, #64b8cc); }
.file-info {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.file-name {
  font-size: 12.5px;
  font-weight: 700;
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.file-meta {
  font-size: 10px;
  color: var(--ink-muted);
  font-family: var(--font-mono);
}
.file-dl {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  padding: 4px 9px;
  border-radius: 999px;
  background: var(--accent);
  border: 1.5px solid var(--accent);
  color: #fff;
  font-size: 11px;
  font-weight: 700;
  text-decoration: none;
  box-shadow: 0 2px 0 0 var(--accent-strong);
}
.file-dl:hover {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.file-dl.is-disabled {
  background: var(--surface-3);
  border-color: var(--line-strong);
  color: var(--ink-muted);
  box-shadow: none;
  cursor: not-allowed;
}

/* ---------- 语音 / 视频 ---------- */
.msg-av {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 7px;
}
.av-card {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border-radius: 12px;
  background: var(--surface-2);
  border: 1.5px solid var(--line-strong);
  font-size: 12px;
  color: var(--ink-body);
}
.av-ico {
  width: 26px;
  height: 26px;
  border-radius: 999px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--accent-tint);
  color: var(--accent-strong);
}
.av-label {
  font-weight: 600;
}
.av-action {
  margin-left: 4px;
  font-size: 11px;
  font-weight: 700;
  color: var(--accent-strong);
}
.av-action.is-disabled {
  color: var(--ink-muted);
  font-weight: 600;
}
.av-retry {
  margin-left: 4px;
  padding: 0;
  border: none;
  background: none;
  color: var(--accent-strong);
  font-size: 11px;
  font-weight: 700;
  cursor: pointer;
}
.av-retry:hover {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.msg-video {
  max-width: min(320px, 100%);
  max-height: 240px;
  border-radius: 12px;
  border: 2px solid var(--line);
  background: #2b2118;
}

@media (max-width: 640px) {
  .msg-images.is-two .msg-img,
  .msg-images.is-grid .msg-img {
    width: 104px;
    height: 104px;
  }
}
</style>
