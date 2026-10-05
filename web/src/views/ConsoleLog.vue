<template>
  <div class="space-y-4">
    <!-- 筛选与操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-1 glass rounded-xl p-1">
        <button
          v-for="t in levelTabs"
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
          <input v-model="autoRefresh" type="checkbox" />
          自动刷新
        </label>
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="clearView">清空显示</button>
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
      </div>
    </div>

    <!-- 终端样式日志区（旧在上、新在下，自动滚到底部；滚动到顶部加载更早记录） -->
    <section class="bg-slate-950/85 rounded-2xl border border-white/10 overflow-hidden shadow-[0_24px_50px_-30px_rgb(15,23,42,0.9)] backdrop-blur-xl">
      <ul ref="listEl" class="h-[60vh] overflow-y-auto px-4 py-3 font-mono text-[11px] leading-relaxed" @scroll="onScroll">
        <li v-if="loadingMore" class="py-1 text-zinc-600 text-center list-none">加载更早的日志…</li>
        <li v-else-if="!hasMore && logs.length" class="py-1 text-zinc-700 text-center list-none">没有更早的日志了</li>
        <li v-if="filtered.length === 0" class="py-12 text-zinc-500 text-center list-none">
          暂无日志（日志保存在内存中，重启后清空，最多保留最近 2000 条）
        </li>
        <li v-for="log in filtered" :key="log.id" class="flex gap-2 py-px">
          <span class="text-zinc-600 shrink-0 whitespace-nowrap">{{ fmtTime(log.time) }}</span>
          <span class="shrink-0 w-8 text-right" :class="levelText[log.level] || 'text-zinc-500'">
            {{ levelTag[log.level] || log.level }}
          </span>
          <div class="min-w-0 flex-1 whitespace-pre-wrap break-all">
            <span :class="levelText[log.level] || 'text-zinc-300'">{{ log.message }}</span>
            <template v-if="log.attrs && log.attrs.length">
              <span v-for="a in log.attrs" :key="a.key" class="text-zinc-500">
                <span class="text-zinc-600"> {{ a.key }}</span>=<span class="text-zinc-400">{{ a.value }}</span>
              </span>
            </template>
          </div>
        </li>
      </ul>

      <!-- 有新日志提示（用户上翻查看历史时） -->
      <div v-if="hasNew" class="border-t border-white/10 px-4 py-2 flex justify-center">
        <button
          class="text-[11px] bg-white text-zinc-950 px-3 py-1.5 rounded-full hover:bg-white/70 font-medium transition-colors shadow-sm"
          @click="scrollToBottom(true)"
        >
          ↓ 有新日志，回到底部
        </button>
      </div>
    </section>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api.js'

const levelTabs = [
  { value: 'all', label: '全部' },
  { value: 'debug', label: 'Debug' },
  { value: 'info', label: 'Info' },
  { value: 'warn', label: 'Warn' },
  { value: 'error', label: 'Error' },
  { value: 'log', label: 'Log' },
]

// 终端配色：按级别着色整行
const levelText = {
  debug: 'text-zinc-500',
  info: 'text-zinc-200',
  warn: 'text-amber-400',
  error: 'text-red-400',
  log: 'text-zinc-400',
}

const levelTag = {
  debug: 'DBG',
  info: 'INF',
  warn: 'WRN',
  error: 'ERR',
  log: 'LOG',
}

const PAGE = 100 // 每页条数

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
  const list = filter.value === 'all' ? logs.value : logs.value.filter((l) => l.level === filter.value)
  return [...list].reverse()
})

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d)) return '-'
  const now = new Date()
  const hms = d.toLocaleTimeString('zh-CN', { hour12: false })
  if (d.toDateString() === now.toDateString()) return hms
  return `${d.getMonth() + 1}/${d.getDate()} ${hms}`
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
  try { page = await api.getConsoleLogs({ limit: PAGE }) } catch { return }
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
    const page = await api.getConsoleLogs({ limit: PAGE, before: oldest })
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

// 清空本地展示（服务端环形缓冲仍继续写入，刷新后可重新看到最新日志）
function clearView() {
  logs.value = []
  hasMore.value = false
  hasNew.value = false
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
