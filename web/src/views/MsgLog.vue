<template>
  <div class="space-y-4">
    <!-- 筛选与操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-1 glass rounded-xl p-1">
        <button
          v-for="t in typeTabs"
          :key="t.value"
          class="px-3 py-1.5 text-xs rounded-md transition-all"
          :class="filter === t.value
            ? 'btn-accent font-medium shadow-sm'
            : 'text-slate-500 hover:text-slate-800 hover:bg-white/60'"
          @click="filter = t.value"
        >
          {{ t.label }}
        </button>
      </div>
      <div class="flex items-center gap-3">
        <label class="flex items-center gap-1.5 text-xs text-slate-500 select-none cursor-pointer">
          <input v-model="autoRefresh" type="checkbox" class="accent-zinc-800" />
          自动刷新
        </label>
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
      </div>
    </div>

    <!-- 日志列表（旧在上、新在下，自动滚到底部；滚动到顶部加载更早的记录） -->
    <section class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
      <ul ref="listEl" class="h-[60vh] overflow-y-auto px-5 py-3 space-y-3" @scroll="onScroll">
        <li v-if="loadingMore" class="py-2 text-xs text-slate-400 text-center list-none">加载更早的消息…</li>
        <li v-else-if="!hasMore && logs.length" class="py-2 text-xs text-slate-300 text-center list-none">没有更早的消息了</li>
        <li v-if="filtered.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
          暂无消息记录（日志保存在内存中，重启后清空，最多保留最近 500 条）
        </li>
        <li v-for="log in filtered" :key="log.id" class="flex items-start gap-3">
          <span class="text-xs text-slate-400 font-mono whitespace-nowrap pt-0.5 w-16 shrink-0">{{ fmtTime(log.time) }}</span>
          <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap shrink-0" :class="typeClass(log.type)">
            {{ typeText(log) }}
          </span>
          <div class="min-w-0 flex-1">
            <div class="text-xs text-slate-400 mb-0.5">
              <template v-if="log.type === 'notice'">
                <span class="text-zinc-600 font-medium">{{ log.title }}</span>
                <template v-if="log.group_id"> · 群 {{ log.group_id }}</template>
              </template>
              <template v-else>
                <span v-if="log.nickname" class="text-zinc-600 font-medium">{{ log.nickname }}</span>
                <span v-if="log.user_id"> ({{ log.user_id }})</span>
                <template v-if="log.group_id"> · 群 {{ log.group_id }}</template>
              </template>
            </div>
            <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed">{{ log.text }}</p>
          </div>
        </li>
      </ul>

      <!-- 有新消息提示（用户上翻查看历史时） -->
      <div v-if="hasNew" class="border-t border-white/50 px-5 py-2 flex justify-center">
        <button
          class="text-xs btn-accent px-3 py-1.5 rounded-full font-medium transition-colors shadow-sm"
          @click="scrollToBottom(true)"
        >
          ↓ 有新消息，回到底部
        </button>
      </div>
    </section>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api.js'

const typeTabs = [
  { value: 'all', label: '全部' },
  { value: 'group', label: '群消息' },
  { value: 'friend', label: '好友消息' },
  { value: 'notice', label: '通知' },
]

const PAGE = 50 // 每页条数

const logs = ref([]) // 新在前
const filter = ref('all')
const autoRefresh = ref(true)
const listEl = ref(null)
const hasNew = ref(false)
const hasMore = ref(false) // 是否还有更早的日志可加载
const loadingMore = ref(false)
let timer = null

// 接口返回新在前，展示时翻转为旧在上、新在下
const filtered = computed(() => {
  const list = filter.value === 'all' ? logs.value : logs.value.filter((l) => l.type === filter.value)
  return [...list].reverse()
})

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d)) return '-'
  const now = new Date()
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false })
  if (d.toDateString() === now.toDateString()) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function typeText(log) {
  return { group: '群消息', friend: '好友', notice: '通知' }[log.type] || log.type
}

function typeClass(type) {
  return {
    group: 'btn-accent',
    friend: 'bg-zinc-100 text-zinc-700 border border-zinc-200',
    notice: 'bg-white text-zinc-500 border border-zinc-300',
  }[type] || 'bg-slate-100 text-slate-600'
}

function nearBottom() {
  const el = listEl.value
  if (!el) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight < 60
}

function scrollToBottom(smooth = false) {
  const el = listEl.value
  if (!el) return
  el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
  hasNew.value = false
}

// 刷新：拉取最新一页，仅把新出现的条目合并到头部，已加载的更早分页保留
async function load() {
  const stick = nearBottom()
  const prevLatest = logs.value[0]?.id
  let page
  try { page = await api.getMsgLogs({ limit: PAGE }) } catch { return }
  const items = page.items || []
  if (!logs.value.length) {
    logs.value = items
    hasMore.value = page.has_more
  } else {
    const fresh = items.filter((l) => l.id > prevLatest)
    if (fresh.length) logs.value = [...fresh, ...logs.value]
  }
  const gotNew = logs.value[0]?.id !== prevLatest
  if (!gotNew) return
  if (stick) {
    await nextTick()
    scrollToBottom()
  } else {
    hasNew.value = true
  }
}

// 滚动到顶部附近时加载更早的一页，并保持视口位置不跳变
async function loadOlder() {
  if (loadingMore.value || !hasMore.value || !logs.value.length) return
  loadingMore.value = true
  const oldest = logs.value[logs.value.length - 1].id
  const el = listEl.value
  const prevHeight = el?.scrollHeight ?? 0
  try {
    const page = await api.getMsgLogs({ limit: PAGE, before: oldest })
    const items = (page.items || []).filter((l) => l.id < oldest)
    hasMore.value = page.has_more && items.length > 0
    if (items.length) {
      logs.value = [...logs.value, ...items]
      await nextTick()
      if (el) el.scrollTop = el.scrollHeight - prevHeight + el.scrollTop
    }
  } catch { /* 忽略，下次滚动重试 */ } finally {
    loadingMore.value = false
  }
}

function onScroll() {
  const el = listEl.value
  if (el && el.scrollTop < 80) loadOlder()
}

// 实时刷新：标签页隐藏时暂停，恢复可见时立即刷新
function onVisible() {
  if (!document.hidden && autoRefresh.value) load()
}

onMounted(async () => {
  await load()
  await nextTick()
  scrollToBottom()
  timer = setInterval(() => { if (!document.hidden && autoRefresh.value) load() }, 3000)
  document.addEventListener('visibilitychange', onVisible)
})

onUnmounted(() => {
  clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisible)
})
</script>
