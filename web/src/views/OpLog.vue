<template>
  <div class="space-y-4">
    <!-- 筛选与操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-1 glass rounded-xl p-1 overflow-x-auto">
        <button
          v-for="t in categoryTabs"
          :key="t.value"
          class="px-3 py-1.5 text-xs rounded-md transition-all whitespace-nowrap"
          :class="filters.category === t.value
            ? 'btn-accent font-medium shadow-sm'
            : 'text-slate-500 hover:text-slate-800 hover:bg-white/60'"
          @click="filters.category = t.value; applyFilters()"
        >
          {{ t.label }}
        </button>
      </div>
      <div class="flex items-center gap-3">
        <label class="flex items-center gap-1.5 text-xs text-slate-500 select-none cursor-pointer">
          <input v-model="autoRefresh" type="checkbox" />
          自动刷新
        </label>
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
      </div>
    </div>

    <!-- 条件查询栏 -->
    <section class="bg-white rounded-xl shadow-sm border border-white/60 px-5 py-4">
      <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">开始时间</span>
          <input v-model="filters.start" type="datetime-local" :class="inputClass" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">结束时间</span>
          <input v-model="filters.end" type="datetime-local" :class="inputClass" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">关键词</span>
          <input v-model.trim="filters.keyword" type="text" placeholder="匹配操作名 / 详情" :class="inputClass" @keyup.enter="applyFilters" />
        </label>
        <div class="flex items-end gap-2">
          <button
            class="px-4 py-1.5 text-[11px] tracking-widest uppercase btn-accent rounded-md transition-colors"
            @click="applyFilters"
          >
            查询
          </button>
          <button
            v-if="hasFilter"
            class="px-3 py-1.5 text-[11px] tracking-widest uppercase text-zinc-500 hover:bg-white/60 rounded-md transition-colors"
            @click="resetFilters"
          >
            重置
          </button>
        </div>
      </div>
    </section>

    <!-- 日志列表（新在上，滚动到底部自动加载更早的记录） -->
    <section class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
      <div v-if="logs.length === 0" class="py-12 text-sm text-slate-400 text-center">
        暂无符合条件的操作日志（登录、配置修改、内容管理、AI 工具操作等在此展示）
      </div>
      <ul v-else class="divide-y divide-zinc-100">
        <li v-for="log in logs" :key="log.id" class="px-5 py-3 flex items-start gap-3 hover:bg-white/55 transition-colors">
          <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap mt-0.5" :class="categoryClass(log.category)">
            {{ categoryText(log.category) }}
          </span>
          <div class="min-w-0 flex-1">
            <div class="text-xs font-mono text-zinc-500">{{ log.action }}</div>
            <p class="mt-0.5 text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed">{{ log.detail }}</p>
          </div>
          <span class="ml-auto text-xs text-slate-400 font-mono whitespace-nowrap mt-0.5">{{ fmtTime(log.time) }}</span>
        </li>
      </ul>
    </section>

    <!-- 滚动分页哨兵：进入视口即加载下一页 -->
    <div ref="sentinel" class="h-px" />
    <div v-if="loadingMore" class="py-3 text-xs text-slate-400 text-center">加载更早的记录…</div>
    <div v-else-if="!hasMore && logs.length" class="py-3 text-xs text-slate-300 text-center">没有更早的记录了</div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { api } from '../api.js'

const categoryTabs = [
  { value: '', label: '全部' },
  { value: 'auth', label: '认证' },
  { value: 'config', label: '配置' },
  { value: 'clock', label: '定时任务' },
  { value: 'skill', label: '技能' },
  { value: 'memory', label: '记忆' },
  { value: 'team', label: '团队' },
  { value: 'knowledge', label: '知识库' },
  { value: 'quota', label: '配额' },
  { value: 'system', label: '系统' },
  { value: 'update', label: '更新' },
  { value: 'ai', label: 'AI 操作' },
]

const categoryLabels = Object.fromEntries(categoryTabs.map((t) => [t.value, t.label]))

const inputClass = 'mt-1 w-full border border-zinc-300 rounded-md px-2.5 py-1.5 text-xs focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30 focus:border-[#0071e3]/50 transition-shadow bg-white'

// filters 为编辑中的条件，applied 为实际生效（已点查询/切换分类）的条件，
// 自动刷新沿用 applied，避免输入到一半被轮询带出去
const emptyFilters = () => ({ category: '', start: '', end: '', keyword: '' })
const filters = reactive(emptyFilters())
const applied = reactive(emptyFilters())

const PAGE = 50 // 每页条数

const logs = ref([]) // 新在前
const autoRefresh = ref(true)
const hasMore = ref(false) // 是否还有更早的日志可加载
const loadingMore = ref(false)
const loadedOlder = ref(false) // 是否已加载过更早分页（是则刷新不再重置 hasMore）
const sentinel = ref(null)
let timer = null
let observer = null

const hasFilter = computed(() => Object.values(applied).some((v) => v !== ''))

function applyFilters() {
  Object.assign(applied, filters)
  resetList()
  load()
}

function resetFilters() {
  Object.assign(filters, emptyFilters())
  Object.assign(applied, filters)
  resetList()
  load()
}

function resetList() {
  logs.value = []
  hasMore.value = false
  loadedOlder.value = false
}

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d)) return '-'
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false })
  if (d.toDateString() === new Date().toDateString()) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function categoryText(c) {
  return categoryLabels[c] || c
}

function categoryClass(c) {
  return {
    auth: 'bg-amber-50 text-amber-700 border border-amber-200',
    config: 'btn-accent',
    ai: 'bg-[#34c759]/10 text-[#248a3d] border border-[#34c759]/40',
    system: 'bg-zinc-100 text-zinc-600 border border-zinc-200',
    update: 'bg-zinc-100 text-zinc-600 border border-zinc-200',
  }[c] || 'bg-white text-zinc-600 border border-zinc-300'
}

// 刷新：拉取最新一页，仅把新出现的条目合并到头部，已加载的更早分页保留
async function load() {
  let page
  try { page = await api.getOpLogs({ ...applied, limit: PAGE }) } catch { return }
  mergeHead(page.items || [])
  if (!loadedOlder.value) hasMore.value = page.has_more
}

// 把最新一页合并进列表头部（items 新在前）
function mergeHead(items) {
  if (!logs.value.length) {
    logs.value = items
    return
  }
  const known = new Set(logs.value.map((l) => l.id))
  const fresh = items.filter((it) => !known.has(it.id))
  if (fresh.length) logs.value = [...fresh, ...logs.value]
}

// 加载更早的一页（滚动分页）
async function loadMore() {
  if (loadingMore.value || !hasMore.value || !logs.value.length) return
  loadingMore.value = true
  const before = logs.value[logs.value.length - 1].id
  try {
    const page = await api.getOpLogs({ ...applied, limit: PAGE, before })
    const items = page.items || []
    hasMore.value = page.has_more && items.length > 0
    if (items.length) {
      loadedOlder.value = true
      logs.value = [...logs.value, ...items]
    }
  } catch { /* 忽略，下次滚动重试 */ } finally {
    loadingMore.value = false
  }
}

// 实时刷新：标签页隐藏时暂停，恢复可见时立即刷新
function onVisible() {
  if (!document.hidden && autoRefresh.value) load()
}

onMounted(() => {
  load()
  timer = setInterval(() => { if (!document.hidden && autoRefresh.value) load() }, 5000)
  document.addEventListener('visibilitychange', onVisible)
  observer = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting)) loadMore()
  })
  if (sentinel.value) observer.observe(sentinel.value)
})

onUnmounted(() => {
  clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisible)
  if (observer) observer.disconnect()
})
</script>
