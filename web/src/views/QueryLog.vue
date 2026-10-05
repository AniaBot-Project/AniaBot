<template>
  <div class="space-y-4">
    <!-- 筛选与操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-1 glass rounded-xl p-1">
        <button
          v-for="t in typeTabs"
          :key="t.value"
          class="px-3 py-1.5 text-xs rounded-md transition-all"
          :class="filters.chat_type === t.value
            ? 'btn-accent font-medium shadow-sm'
            : 'text-slate-500 hover:text-slate-800 hover:bg-white/60'"
          @click="filters.chat_type = t.value; applyFilters()"
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
      <div class="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">触发人 ID</span>
          <input v-model.trim="filters.sender" type="text" placeholder="精确匹配" :class="inputClass" @keyup.enter="applyFilters" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">目标会话 ID</span>
          <input v-model.trim="filters.target_id" type="text" placeholder="精确匹配" :class="inputClass" @keyup.enter="applyFilters" />
        </label>
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
          <input v-model.trim="filters.keyword" type="text" placeholder="匹配用户输入" :class="inputClass" @keyup.enter="applyFilters" />
        </label>
        <div class="flex items-end gap-2">
          <button
            class="px-4 py-1.5 text-[11px] tracking-[0.1em] uppercase btn-accent rounded-md transition-colors"
            @click="applyFilters"
          >
            查询
          </button>
          <button
            v-if="hasFilter"
            class="px-3 py-1.5 text-[11px] tracking-[0.1em] uppercase text-zinc-500 hover:bg-white/60 rounded-md transition-colors"
            @click="resetFilters"
          >
            重置
          </button>
        </div>
      </div>
    </section>

    <!-- 日志列表（新在上，滚动到底部自动加载更早的记录） -->
    <section class="space-y-3">
      <div v-if="logs.length === 0" class="bg-white rounded-xl shadow-sm border border-white/60 py-12 text-sm text-slate-400 text-center">
        暂无符合条件的 Query 记录（@ 机器人或私聊触发 AI 回复后在此展示）
      </div>

      <div
        v-for="log in logs"
        :key="log.id"
        class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden"
      >
        <!-- 摘要行（点击弹出详情窗口） -->
        <button class="w-full text-left px-5 py-3.5 hover:bg-white/55 transition-colors" @click="detail = log">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap" :class="statusClass(log.status)">
              {{ statusText(log.status) }}
            </span>
            <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap bg-zinc-100 text-zinc-600 border border-zinc-200">
              {{ log.chat_type === 'group' ? '群聊' : '私聊' }} · {{ log.target_id }}
            </span>
            <span v-if="log.tool_calls?.length" class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap bg-zinc-100 text-zinc-600 border border-zinc-200">
              {{ log.tool_calls_total || log.tool_calls.length }} 次工具调用
            </span>
            <span class="ml-auto text-xs text-slate-400 font-mono whitespace-nowrap">{{ fmtTime(log.time) }}</span>
          </div>
          <p class="mt-2 text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed line-clamp-3">{{ log.query }}</p>
          <div class="mt-2 flex items-center gap-3 text-[11px] text-slate-400 font-mono flex-wrap">
            <span>{{ log.status === 'running' ? '已用时' : '用时' }} {{ fmtDuration(elapsedMs(log)) }}</span>
            <span v-if="log.iterations">LLM {{ log.iterations }} 轮</span>
            <template v-if="log.total_tokens">
              <span>tokens 总计 {{ log.total_tokens }}</span>
              <span>输入 {{ log.prompt_tokens }}</span>
              <span>输出 {{ log.completion_tokens }}</span>
              <span v-if="log.cached_tokens">缓存命中 {{ log.cached_tokens }}</span>
            </template>
            <span v-if="log.senders?.length">来自 {{ log.senders.join(', ') }}</span>
            <span class="ml-auto text-zinc-400">详情 ⤢</span>
          </div>
        </button>
      </div>
    </section>

    <!-- 滚动分页哨兵：进入视口即加载下一页 -->
    <div ref="sentinel" class="h-px" />
    <div v-if="loadingMore" class="py-3 text-xs text-slate-400 text-center">加载更早的记录…</div>
    <div v-else-if="!hasMore && logs.length" class="py-3 text-xs text-slate-300 text-center">没有更早的记录了</div>

    <!-- 详情弹窗：点遮罩 / 右上角关闭 / Esc 均可关闭 -->
    <div
      v-if="detail"
      class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4"
      v-backdrop-close="() => (detail = null)"
    >
      <div class="bg-white rounded-xl shadow-2xl border border-zinc-200 w-full max-w-3xl max-h-[85vh] flex flex-col">
        <!-- 弹窗头部 -->
        <div class="flex items-center gap-2 flex-wrap px-5 py-3.5 border-b border-zinc-100 shrink-0">
          <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap" :class="statusClass(detail.status)">
            {{ statusText(detail.status) }}
          </span>
          <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap bg-zinc-100 text-zinc-600 border border-zinc-200">
            {{ detail.chat_type === 'group' ? '群聊' : '私聊' }} · {{ detail.target_id }}
          </span>
          <span class="text-[11px] text-slate-400 font-mono">{{ fmtTime(detail.time) }}</span>
          <button
            class="ml-auto w-7 h-7 flex items-center justify-center rounded-md text-zinc-400 hover:text-zinc-800 hover:bg-white/60 transition-colors"
            title="关闭"
            @click="detail = null"
          >
            ✕
          </button>
        </div>

        <!-- 弹窗内容（可滚动） -->
        <div class="px-5 py-4 space-y-4 overflow-y-auto">
          <!-- 概要指标 -->
          <div class="flex items-center gap-3 text-[11px] text-slate-400 font-mono flex-wrap">
            <span>{{ detail.status === 'running' ? '已用时' : '用时' }} {{ fmtDuration(elapsedMs(detail)) }}</span>
            <span v-if="detail.iterations">LLM {{ detail.iterations }} 轮</span>
            <template v-if="detail.total_tokens">
              <span>tokens 总计 {{ detail.total_tokens }}</span>
              <span>输入 {{ detail.prompt_tokens }}</span>
              <span>输出 {{ detail.completion_tokens }}</span>
              <span v-if="detail.cached_tokens">缓存命中 {{ detail.cached_tokens }}</span>
            </template>
            <span v-if="detail.senders?.length">来自 {{ detail.senders.join(', ') }}</span>
          </div>

          <!-- 用户输入 -->
          <div>
            <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium mb-2">用户输入</h3>
            <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ detail.query }}</p>
          </div>

          <!-- 错误信息 -->
          <div v-if="detail.error" class="text-xs text-[#ff3b30] bg-red-50 border border-red-100 rounded-lg px-3 py-2 whitespace-pre-wrap break-all">
            {{ detail.error }}
          </div>

          <!-- 工具调用明细 -->
          <div v-if="detail.tool_calls?.length" class="space-y-2">
            <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium">
              工具调用
              <span v-if="detail.tool_calls_total > detail.tool_calls.length" class="normal-case tracking-normal text-zinc-300">
                （共 {{ detail.tool_calls_total }} 次，仅保留前 {{ detail.tool_calls.length }} 条）
              </span>
            </h3>
            <div
              v-for="(tc, i) in detail.tool_calls"
              :key="i"
              class="bg-white border border-white/60 rounded-lg overflow-hidden"
            >
              <div class="flex items-center gap-2 px-3 py-2 border-b border-white/50">
                <span class="text-xs font-mono font-medium text-zinc-800">{{ tc.name }}</span>
                <span v-if="tc.error" class="text-[11px] px-1.5 py-0.5 rounded bg-red-50 text-[#ff3b30] border border-red-100">失败</span>
                <span class="ml-auto text-[11px] text-slate-400 font-mono">{{ fmtDuration(tc.duration_ms) }}</span>
              </div>
              <div class="px-3 py-2 space-y-2">
                <div v-if="tc.arguments">
                  <div class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 mb-1">参数</div>
                  <pre class="text-xs text-slate-600 font-mono whitespace-pre-wrap break-all leading-relaxed">{{ tc.arguments }}</pre>
                </div>
                <div v-if="tc.error">
                  <div class="text-[10px] tracking-[0.15em] uppercase text-red-400 mb-1">错误</div>
                  <pre class="text-xs text-[#ff3b30] font-mono whitespace-pre-wrap break-all leading-relaxed">{{ tc.error }}</pre>
                </div>
                <div v-else-if="tc.result">
                  <div class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 mb-1">结果</div>
                  <pre class="text-xs text-slate-600 font-mono whitespace-pre-wrap break-all leading-relaxed">{{ tc.result }}</pre>
                </div>
              </div>
            </div>
          </div>

          <!-- 最终回复 -->
          <div v-if="detail.reply">
            <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium mb-2">最终回复</h3>
            <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ detail.reply }}</p>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { api } from '../api.js'

const typeTabs = [
  { value: '', label: '全部' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const inputClass = 'mt-1 w-full border border-zinc-300 rounded-md px-2.5 py-1.5 text-xs focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30 focus:border-[#0071e3]/50 transition-shadow bg-white'

// filters 为编辑中的条件，applied 为实际生效（已点查询/切换类型）的条件，
// 自动刷新沿用 applied，避免输入到一半被轮询带出去
const emptyFilters = () => ({ chat_type: '', sender: '', target_id: '', start: '', end: '', keyword: '' })
const filters = reactive(emptyFilters())
const applied = reactive(emptyFilters())

const PAGE = 50 // 每页条数

const logs = ref([]) // 新在前
const autoRefresh = ref(true)
// detail 为当前弹窗展示的日志（null 表示弹窗关闭）
const detail = ref(null)
// now 每秒跳动一次，驱动 running 条目「已用时」实时刷新
const now = ref(Date.now())
const hasMore = ref(false) // 是否还有更早的日志可加载
const loadingMore = ref(false)
const loadedOlder = ref(false) // 是否已加载过更早分页（是则刷新不再重置 hasMore）
const sentinel = ref(null)
let timer = null
let tickTimer = null
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

// Esc 关闭详情弹窗
function onKeydown(e) {
  if (e.key === 'Escape') detail.value = null
}

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d)) return '-'
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false })
  if (d.toDateString() === new Date().toDateString()) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function fmtDuration(ms) {
  if (!ms && ms !== 0) return '-'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

// elapsedMs 条目耗时：running 条目按开始时间实时计算（随 now 每秒跳动），
// 已完成条目用落盘的 duration_ms
function elapsedMs(log) {
  if (log.status !== 'running') return log.duration_ms
  const t = new Date(log.time).getTime()
  if (isNaN(t)) return log.duration_ms
  return Math.max(0, now.value - t)
}

function statusText(s) {
  return { running: '执行中', success: '成功', stopped: '已停止', timeout: '超时', error: '出错', interrupted: '中断' }[s] || s
}

function statusClass(s) {
  return {
    running: 'btn-accent',
    success: 'bg-white text-zinc-600 border border-zinc-300',
    stopped: 'bg-zinc-100 text-zinc-500 border border-zinc-200',
    timeout: 'bg-zinc-100 text-zinc-500 border border-zinc-200',
    error: 'bg-red-50 text-[#ff3b30] border border-red-200',
    interrupted: 'bg-amber-50 text-[#ff9f0a] border border-amber-200',
  }[s] || 'bg-slate-100 text-slate-600'
}

// 刷新：拉取最新一页，新条目插入头部、已有条目原地更新（执行中 → 完成），
// 已加载的更早分页保留
async function load() {
  let page
  try { page = await api.getQueryLogs({ ...applied, limit: PAGE }) } catch { return }
  mergeHead(page.items || [])
  if (!loadedOlder.value) hasMore.value = page.has_more
}

// 把最新一页合并进列表头部（items 新在前）
function mergeHead(items) {
  if (!logs.value.length) {
    logs.value = items
    return
  }
  const index = new Map(logs.value.map((l, i) => [l.id, i]))
  const fresh = []
  const merged = [...logs.value]
  for (const it of items) {
    if (index.has(it.id)) merged[index.get(it.id)] = it
    else fresh.push(it)
  }
  logs.value = fresh.length ? [...fresh, ...merged] : merged
  // 详情弹窗内容随状态更新同步刷新
  if (detail.value) {
    const cur = logs.value.find((l) => l.id === detail.value.id)
    if (cur) detail.value = cur
  }
}

// 加载更早的一页（滚动分页）
async function loadMore() {
  if (loadingMore.value || !hasMore.value || !logs.value.length) return
  loadingMore.value = true
  const before = logs.value[logs.value.length - 1].id
  try {
    const page = await api.getQueryLogs({ ...applied, limit: PAGE, before })
    const known = new Set(logs.value.map((l) => l.id))
    const items = (page.items || []).filter((it) => !known.has(it.id))
    loadedOlder.value = true
    hasMore.value = page.has_more && items.length > 0
    logs.value = [...logs.value, ...items]
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
  timer = setInterval(() => { if (!document.hidden && autoRefresh.value) load() }, 4000)
  tickTimer = setInterval(() => { now.value = Date.now() }, 1000)
  observer = new IntersectionObserver(
    (entries) => { if (entries.some((e) => e.isIntersecting)) loadMore() },
    { rootMargin: '300px' },
  )
  if (sentinel.value) observer.observe(sentinel.value)
  document.addEventListener('visibilitychange', onVisible)
  document.addEventListener('keydown', onKeydown)
})

onUnmounted(() => {
  clearInterval(timer)
  clearInterval(tickTimer)
  observer?.disconnect()
  document.removeEventListener('visibilitychange', onVisible)
  document.removeEventListener('keydown', onKeydown)
})
</script>
