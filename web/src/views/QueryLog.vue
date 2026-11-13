<template>
  <div class="space-y-4">
    <!-- 筛选与操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-2 flex-wrap">
        <div class="filter-group">
          <Button
            v-for="t in typeTabs"
            :key="t.value"
            size="small"
            :type="filters.chat_type === t.value ? 'primary' : 'text'"
            @click="filters.chat_type = t.value; applyFilters()"
          >
            {{ t.label }}
          </Button>
        </div>
        <!-- 来源：普通对话 / 主动互动（AI 自行发起的发言） -->
        <div class="filter-group">
          <Button
            v-for="t in kindTabs"
            :key="t.value"
            size="small"
            :type="filters.kind === t.value ? 'primary' : 'text'"
            @click="filters.kind = t.value; applyFilters()"
          >
            {{ t.label }}
          </Button>
        </div>
      </div>
      <div class="flex items-center gap-3">
        <label class="flex items-center gap-1.5 text-xs text-slate-500 select-none cursor-pointer">
          <Switch v-model="autoRefresh" size="small" />
          自动刷新
        </label>
        <Button type="text" size="small" :disabled="exporting" @click="exportLogs">导出日志</Button>
        <Button type="default" size="small" danger :disabled="clearing" @click="clearLogs">清除日志</Button>
        <Button type="text" size="small" @click="load">刷新</Button>
      </div>
    </div>

    <!-- 条件查询栏 -->
    <Card>
      <div class="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">触发人 ID</span>
          <Input
            class="mt-1 w-full"
            :model-value="filters.sender"
            placeholder="精确匹配"
            @update:modelValue="v => (filters.sender = v.trim())"
            @keyup.enter="applyFilters"
          />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">目标会话 ID</span>
          <Input
            class="mt-1 w-full"
            :model-value="filters.target_id"
            placeholder="精确匹配"
            @update:modelValue="v => (filters.target_id = v.trim())"
            @keyup.enter="applyFilters"
          />
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
          <Input
            class="mt-1 w-full"
            :model-value="filters.keyword"
            placeholder="匹配用户输入"
            @update:modelValue="v => (filters.keyword = v.trim())"
            @keyup.enter="applyFilters"
          />
        </label>
        <div class="flex items-end gap-2">
          <Button type="primary" size="small" @click="applyFilters">查询</Button>
          <Button v-if="hasFilter" type="text" size="small" @click="resetFilters">重置</Button>
        </div>
      </div>
    </Card>

    <!-- 日志列表（新在上，滚动到底部自动加载更早的记录） -->
    <section class="space-y-3">
      <Card v-if="logs.length === 0" style="padding: 0">
        <div class="py-12 text-sm text-slate-400 text-center">
          暂无符合条件的 Query 记录（@ 机器人、私聊或面板试聊触发 AI 回复、或主动互动发言后在此展示）
        </div>
      </Card>

      <Card
        v-for="log in logs"
        :key="log.id"
        hoverable
        style="padding: 0"
        @click="detail = log"
      >
        <!-- 摘要行（点击弹出详情窗口） -->
        <div class="px-5 py-3.5">
          <div class="flex items-center gap-2 flex-wrap">
            <Tag variant="solid" size="small" :color="statusColor(log.status)">
              {{ statusText(log.status) }}
            </Tag>
            <Tag v-if="log.kind === 'proactive'" variant="solid" size="small" color="app-blue">
              主动互动
            </Tag>
            <Tag variant="solid" size="small">
              {{ chatTypeText(log.chat_type) }} · {{ log.target_id }}
            </Tag>
            <Tag v-if="log.tool_calls?.length" variant="solid" size="small">
              {{ log.tool_calls_total || log.tool_calls.length }} 次工具调用
            </Tag>
            <span class="ml-auto text-xs text-slate-400 font-mono whitespace-nowrap">{{ fmtTime(log.time) }}</span>
          </div>
          <p class="mt-2 text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed line-clamp-3">{{ log.query }}</p>
          <div class="mt-2 flex items-center gap-x-3 gap-y-1 text-[11px] text-slate-400 font-mono flex-wrap">
            <span>{{ log.status === 'running' ? '已用时' : '用时' }} {{ fmtDuration(elapsedMs(log)) }}</span>
            <span v-if="log.iterations">LLM {{ log.iterations }} 轮</span>
            <template v-if="log.total_tokens">
              <span>tokens 总计 <span class="tokval">{{ fmtInt(log.total_tokens) }}</span></span>
              <span>输入 <span class="tokval">{{ fmtInt(log.prompt_tokens) }}</span></span>
              <span>输出 <span class="tokval">{{ fmtInt(log.completion_tokens) }}</span></span>
              <span v-if="log.cached_tokens" :title="cacheTip(log)">
                缓存命中 <span class="tokval">{{ fmtInt(log.cached_tokens) }}</span>
                <template v-if="cacheRate(log) > 0"> · 命中率 <span class="ratehit">{{ cachePct(log) }}</span></template>
              </span>
            </template>
            <span v-if="log.senders?.length">来自 {{ log.senders.join(', ') }}</span>
            <span class="ml-auto text-zinc-400">详情 ⤢</span>
          </div>
        </div>
      </Card>
    </section>

    <!-- 滚动分页哨兵：进入视口即加载下一页 -->
    <div ref="sentinel" class="h-px" />
    <div v-if="loadingMore" class="py-3 text-xs text-slate-400 text-center">加载更早的记录…</div>
    <div v-else-if="!hasMore && logs.length" class="py-3 text-xs text-slate-300 text-center">没有更早的记录了</div>

    <!-- 详情弹窗：点遮罩 / 右上角关闭 / Esc 均可关闭 -->
    <Modal v-model:open="detailOpen" variant="default" :typewriter="false" :show-footer="false" :width="768">
      <!-- 弹窗头部 -->
      <template #title>
        <div class="flex items-center gap-2 flex-wrap w-full font-normal">
          <Tag variant="solid" size="small" :color="statusColor(detail.status)">
            {{ statusText(detail.status) }}
          </Tag>
          <Tag v-if="detail.kind === 'proactive'" variant="solid" size="small" color="app-blue">
            主动互动
          </Tag>
          <Tag variant="solid" size="small">
            {{ chatTypeText(detail.chat_type) }} · {{ detail.target_id }}
          </Tag>
          <span class="text-[11px] text-slate-400 font-mono">{{ fmtTime(detail.time) }}</span>
          <Tooltip class="ml-auto" title="关闭" placement="bottom">
            <Button type="text" size="small" @click="detail = null">✕</Button>
          </Tooltip>
        </div>
      </template>

      <!-- 弹窗内容（可滚动） -->
      <div class="w-full space-y-4 font-normal">
        <!-- 概要指标 -->
        <div class="flex items-center gap-x-3 gap-y-1 text-[11px] text-slate-400 font-mono flex-wrap">
          <span>{{ detail.status === 'running' ? '已用时' : '用时' }} {{ fmtDuration(elapsedMs(detail)) }}</span>
          <span v-if="detail.iterations">LLM {{ detail.iterations }} 轮</span>
          <template v-if="detail.total_tokens">
            <span>tokens 总计 <span class="tokval">{{ fmtInt(detail.total_tokens) }}</span></span>
            <span>输入 <span class="tokval">{{ fmtInt(detail.prompt_tokens) }}</span></span>
            <span>输出 <span class="tokval">{{ fmtInt(detail.completion_tokens) }}</span></span>
            <span v-if="detail.cached_tokens" :title="cacheTip(detail)">
              缓存命中 <span class="tokval">{{ fmtInt(detail.cached_tokens) }}</span>
              <template v-if="cacheRate(detail) > 0"> · 命中率 <span class="ratehit">{{ cachePct(detail) }}</span></template>
            </span>
          </template>
          <span v-if="detail.senders?.length">来自 {{ detail.senders.join(', ') }}</span>
        </div>

        <!-- 用户输入（主动互动为触发说明） -->
        <div>
          <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium mb-2">{{ detail.kind === 'proactive' ? '触发说明' : '用户输入' }}</h3>
          <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ detail.query }}</p>
        </div>

        <!-- 错误信息 -->
        <div v-if="detail.error" class="text-xs text-[#ff3b30] bg-red-50 border border-red-100 rounded-lg px-3 py-2 whitespace-pre-wrap break-all">
          {{ detail.error }}
        </div>

        <!-- 补充说明（如主动互动保持安静的原因） -->
        <div v-if="detail.note" class="text-xs text-slate-600 bg-slate-50 border border-white/60 rounded-lg px-3 py-2 whitespace-pre-wrap break-all">
          {{ detail.note }}
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
              <Tag v-if="tc.error" variant="solid" size="small" color="app-red">失败</Tag>
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
                <div class="flex items-center gap-2 mb-1">
                  <div class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">结果</div>
                  <span v-if="isLongText(tc.result)" class="text-[10px] text-zinc-400 font-mono">{{ charCount(tc.result) }} 字符</span>
                </div>
                <pre class="text-xs text-slate-600 font-mono whitespace-pre-wrap break-all leading-relaxed">{{ shownResult(detail, i, tc.result) }}</pre>
                <Button
                  v-if="isLongText(tc.result)"
                  class="mt-1.5"
                  type="link"
                  size="small"
                  @click="toggleResult(detail, i)"
                >
                  {{ resultExpanded(detail, i) ? '收起' : `展开全部（超过 ${FOLD_CHARS} 字符）` }}
                </Button>
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
    </Modal>

    <!-- 命令式通知（替换 alert）的渲染容器 -->
  </div>
</template>

<script setup>
import { confirmAction } from '../ui/confirm'
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { Button, Card, Input, Modal, Notification, Switch, Tag, Tooltip } from 'animal-island-vue'
import { api } from '../api.js'

const typeTabs = [
  { value: '', label: '全部' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
  { value: 'panel', label: '试聊' },
]

// 来源筛选：普通对话（含升级前的历史记录） / 主动互动
const kindTabs = [
  { value: '', label: '全部来源' },
  { value: 'chat', label: '对话' },
  { value: 'proactive', label: '主动互动' },
]

const inputClass = 'mt-1 w-full border border-zinc-300 rounded-md px-2.5 py-1.5 text-xs focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30 focus:border-[#0071e3]/50 transition-shadow bg-white'

// filters 为编辑中的条件，applied 为实际生效（已点查询/切换类型）的条件，
// 自动刷新沿用 applied，避免输入到一半被轮询带出去
const emptyFilters = () => ({ kind: '', chat_type: '', sender: '', target_id: '', start: '', end: '', keyword: '' })
const filters = reactive(emptyFilters())
const applied = reactive(emptyFilters())

const PAGE = 50 // 每页条数

const logs = ref([]) // 新在前
const autoRefresh = ref(true)
const exporting = ref(false)
const clearing = ref(false)
// 工具结果超过该字符数时折叠展示，点击可展开（后端记录完整内容）
const FOLD_CHARS = 1000
const expandedResults = reactive(new Set())
// detail 为当前弹窗展示的日志（null 表示弹窗关闭）
const detail = ref(null)
// 弹窗开关：Modal 需要布尔 open，这里与 detail 互转（关闭时清空 detail）
const detailOpen = computed({
  get: () => !!detail.value,
  set: (open) => { if (!open) detail.value = null },
})
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

// 按当前筛选条件导出 Query 日志（后端返回 JSON 附件，含完整工具结果）
async function exportLogs() {
  if (exporting.value) return
  exporting.value = true
  try {
    await api.exportQueryLogs({ ...applied })
  } catch (e) {
    Notification.error({ message: e.message || '导出失败' })
  } finally {
    exporting.value = false
  }
}

// 清除当前筛选条件下的 Query 日志（无筛选时清空全部），不可恢复
async function clearLogs() {
  if (clearing.value) return
  const scope = hasFilter.value ? '当前筛选条件下的' : '全部'
  if (!await confirmAction(`确定清除${scope} Query 日志吗？清除后不可恢复，建议先导出备份。`)) return
  clearing.value = true
  try {
    const res = await api.clearQueryLogs({ ...applied })
    resetList()
    await load()
    Notification.success({ message: `已清除 ${res.removed ?? 0} 条日志` })
  } catch (e) {
    Notification.error({ message: e.message || '清除失败' })
  } finally {
    clearing.value = false
  }
}

// ---- 工具结果折叠：超过 FOLD_CHARS 字符默认只显示前 FOLD_CHARS 个字符 ----
function charCount(text) {
  return Array.from(text || '').length
}

function isLongText(text) {
  return typeof text === 'string' && charCount(text) > FOLD_CHARS
}

function resultKey(log, index) {
  return `${log.id}:${index}`
}

function resultExpanded(log, index) {
  return expandedResults.has(resultKey(log, index))
}

function shownResult(log, index, text) {
  if (!isLongText(text) || resultExpanded(log, index)) return text
  return Array.from(text).slice(0, FOLD_CHARS).join('')
}

function toggleResult(log, index) {
  const key = resultKey(log, index)
  if (expandedResults.has(key)) expandedResults.delete(key)
  else expandedResults.add(key)
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

// token 数量级很大，加千分位后更易扫读
function fmtInt(n) {
  const v = Math.round(Number(n) || 0)
  return v.toLocaleString('en-US')
}

// 缓存命中率 = 命中 token / 输入 token（与「Token 统计」页口径一致：cached / prompt）
function cacheRate(log) {
  const prompt = Number(log?.prompt_tokens) || 0
  const cached = Number(log?.cached_tokens) || 0
  if (prompt <= 0 || cached <= 0) return 0
  return Math.min(1, cached / prompt)
}

function cachePct(log) {
  return `${(cacheRate(log) * 100).toFixed(1)}%`
}

function cacheTip(log) {
  return `缓存命中 ${fmtInt(log?.cached_tokens)} / 输入 ${fmtInt(log?.prompt_tokens)}`
}

// elapsedMs 条目耗时：running 条目按开始时间实时计算（随 now 每秒跳动），
// 已完成条目用落盘的 duration_ms
function elapsedMs(log) {
  if (log.status !== 'running') return log.duration_ms
  const t = new Date(log.time).getTime()
  if (isNaN(t)) return log.duration_ms
  return Math.max(0, now.value - t)
}

// 会话类型展示：group 群聊 / friend 私聊 / panel 面板试聊
function chatTypeText(t) {
  return { group: '群聊', friend: '私聊', panel: '试聊' }[t] || t || '未知'
}

function statusText(s) {
  return { running: '执行中', success: '成功', skipped: '保持安静', stopped: '已停止', timeout: '超时', error: '出错', interrupted: '中断' }[s] || s
}

// 状态药丸颜色（Tag color）：成功绿、警告橙、错误红，其余走默认/主题色
function statusColor(s) {
  return {
    running: 'app-teal',
    success: 'app-green',
    skipped: 'app-blue',
    stopped: 'default',
    timeout: 'app-orange',
    error: 'app-red',
    interrupted: 'app-orange',
  }[s] || 'default'
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

<style scoped>
/* token 数值比标签稍深一档，配合千分位更易扫读 */
.tokval {
  color: rgb(100 116 139);
  font-weight: 500;
}
/* 缓存命中率：系统绿加深版，小字号下也有足够对比度 */
.ratehit {
  color: #248a3d;
  font-weight: 500;
}
</style>
