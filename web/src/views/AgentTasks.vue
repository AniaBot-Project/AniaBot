<template>
  <div class="space-y-4">
    <!-- 统计卡：运行中 / 等待 / 完成 / 失败 -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <Card v-for="s in stats" :key="s.key" style="padding: 0">
        <div class="px-5 py-4">
          <div class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">{{ s.label }}</div>
          <div class="mt-1 flex items-baseline gap-2">
            <span class="text-2xl font-bold" :class="s.ink">{{ s.value }}</span>
            <span class="text-[11px] text-zinc-400">{{ s.hint }}</span>
          </div>
        </div>
      </Card>
    </div>

    <!-- 筛选与操作栏 -->
    <Card>
      <div class="flex items-end flex-wrap gap-3">
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">状态</span>
          <Select v-model="filters.status" :options="statusOptions" class="mt-1 w-40" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">类型</span>
          <Select v-model="filters.kind" :options="kindOptions" class="mt-1 w-40" />
        </label>
        <label class="block flex-1 min-w-52">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">关键词</span>
          <Input
            :model-value="filters.keyword"
            placeholder="匹配标题 / 任务 ID / 会话"
            class="mt-1 w-full"
            @update:model-value="v => (filters.keyword = v.trim())"
          />
        </label>
        <div class="flex items-center gap-3 pb-0.5">
          <label class="flex items-center gap-1.5 text-xs text-slate-500 select-none cursor-pointer">
            <Switch v-model="autoRefresh" size="small" />
            自动刷新（10s）
          </label>
          <Button type="text" size="small" :disabled="loading" @click="load">刷新</Button>
        </div>
      </div>
    </Card>

    <!-- 任务列表 -->
    <Card style="padding: 0" class="overflow-hidden">
      <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between">
        <h2 class="text-[10px] tracking-[0.15em] uppercase text-zinc-800 font-medium">后台任务</h2>
        <span class="text-[11px] text-zinc-400">
          最近 {{ tasks.length }} 条<template v-if="filtered.length !== tasks.length"> · 筛选后 {{ filtered.length }} 条</template>
        </span>
      </div>

      <div v-if="loading && !tasks.length" class="px-6 py-6">
        <LoadingState text="正在加载后台任务…" :rows="3" compact />
      </div>
      <p v-else-if="loadError && !tasks.length" class="px-6 py-8 text-xs text-[#ff3b30] text-center">
        加载失败：{{ loadError }}
        <Button class="ml-2" type="text" size="small" @click="load">重试</Button>
      </p>
      <p v-else-if="!filtered.length" class="px-6 py-8 text-xs text-zinc-400 text-center tracking-wide">
        {{ tasks.length ? '没有符合筛选条件的任务' : '暂无后台任务（AI 处理中 / 子代理 / 定时触发等工作会登记在这里）' }}
      </p>
      <template v-else>
        <div class="task-table-wrap">
          <table class="task-table">
            <thead>
              <tr>
                <th v-for="col in columns" :key="col.key" :style="col.width ? { width: col.width } : undefined">{{ col.title }}</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="record in filtered" :key="record.id">
                <tr class="task-table-row" :class="{ 'is-expanded': expanded.has(record.id) }" @click="toggleExpand(record.id)">
                  <td>
                    <span class="flex items-center gap-1.5 min-w-0">
                      <span
                        class="[&>svg]:w-3 [&>svg]:h-3 text-zinc-400 transition-transform shrink-0"
                        :class="{ 'rotate-90': expanded.has(record.id) }"
                        v-html="icons.chevron"
                      />
                      <span class="font-mono text-[11px] truncate" :title="record.id">{{ record.id }}</span>
                    </span>
                  </td>
                  <td><Tag variant="outlined" size="small">{{ kindText(record.kind) }}</Tag></td>
                  <td><Tag variant="solid" size="small" :color="statusColor(record.status)">{{ statusText(record.status) }}</Tag></td>
                  <td class="task-title-cell">
                    <!-- 标题最多两行（超出省略），悬停看全文；点击行在下方就地展开完整详情 -->
                    <p class="task-title font-medium" :title="record.title || ''">{{ record.title || '(无标题)' }}</p>
                  </td>
                  <td><span class="block truncate font-mono text-[11px] text-zinc-600" :title="record.session_key || ''">{{ record.session_key || '—' }}</span></td>
                  <td><span class="font-mono text-[11px] whitespace-nowrap">{{ record.tokens ? fmtInt(record.tokens) : '—' }}</span></td>
                  <td><span class="text-zinc-500 whitespace-nowrap">{{ fmtDuration(record) }}</span></td>
                  <td><span class="text-zinc-600 whitespace-nowrap" :title="fmtTimeFull(record.updated_at)">{{ fmtTime(record.updated_at) }}</span></td>
                  <td>
                    <span
                      class="text-[11px] whitespace-nowrap"
                      :class="record.error ? 'text-[#ff3b30]' : 'text-zinc-400'"
                      :title="record.error || record.stop_reason || ''"
                    >
                      {{ record.error ? '错误' : stopText(record.stop_reason) }}
                    </span>
                  </td>
                </tr>
                <!-- 展开详情：就地渲染在对应任务行下方（完整标题 / 结果 / 错误 / Meta） -->
                <tr v-if="expanded.has(record.id)" class="task-table-detail-row">
                  <td :colspan="columns.length">
                    <div class="px-6 py-4">
                      <dl class="grid grid-cols-1 sm:grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-xs">
                        <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">任务 ID</dt>
                        <dd class="font-mono text-zinc-700 break-all">{{ record.id }}</dd>
                        <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">标题</dt>
                        <dd class="text-zinc-700 whitespace-pre-wrap break-all">{{ record.title || '—' }}</dd>
                        <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">会话</dt>
                        <dd class="font-mono text-zinc-700">{{ record.session_key || '—' }}</dd>
                        <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">创建 / 开始 / 结束</dt>
                        <dd class="text-zinc-700">{{ fmtTimeFull(record.created_at) }} → {{ fmtTimeFull(record.started_at) }} → {{ fmtTimeFull(record.finished_at) }}</dd>
                        <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">状态 / 停止原因</dt>
                        <dd class="text-zinc-700">{{ statusText(record.status) }} / {{ stopText(record.stop_reason) }}</dd>
                        <template v-if="record.error">
                          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">错误</dt>
                          <dd>
                            <pre class="task-pre" :class="{ 'task-pre-open': isFieldOpen(record, 'error') }">{{ record.error }}</pre>
                            <Button v-if="record.error.length > 200" type="text" size="small" @click.stop="toggleField(record, 'error')">
                              {{ isFieldOpen(record, 'error') ? '收起' : '展开全部' }}
                            </Button>
                          </dd>
                        </template>
                        <template v-if="record.result">
                          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">结果</dt>
                          <dd>
                            <pre class="task-pre" :class="{ 'task-pre-open': isFieldOpen(record, 'result') }">{{ record.result }}</pre>
                            <Button v-if="record.result.length > 200" type="text" size="small" @click.stop="toggleField(record, 'result')">
                              {{ isFieldOpen(record, 'result') ? '收起' : '展开全部' }}
                            </Button>
                          </dd>
                        </template>
                        <template v-if="record.meta">
                          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">Meta</dt>
                          <dd><pre class="task-pre task-pre-open">{{ prettyMeta(record.meta) }}</pre></dd>
                        </template>
                        <template v-if="!record.error && !record.result && !record.meta">
                          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">详情</dt>
                          <dd class="text-zinc-400">该任务尚无结果 / 错误 / Meta 记录（可能仍在执行中）</dd>
                        </template>
                      </dl>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </template>
    </Card>

    <!-- 当前 active 的长任务目标 -->
    <Card style="padding: 0" class="overflow-hidden">
      <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between">
        <h2 class="text-[10px] tracking-[0.15em] uppercase text-zinc-800 font-medium">进行中的长任务目标</h2>
        <span class="text-[11px] text-zinc-400">{{ goals.length }} 个 active 目标</span>
      </div>
      <p v-if="!goals.length" class="px-6 py-6 text-xs text-zinc-400 text-center tracking-wide">
        当前没有 active 目标（AI 可用 goal 工具登记跨回合目标）
      </p>
      <div v-else class="divide-y divide-zinc-100">
        <div v-for="g in goals" :key="g.session_key" class="px-6 py-3">
          <div class="flex items-center gap-2 flex-wrap">
            <Tag variant="solid" size="small" color="app-teal">{{ g.phase }}</Tag>
            <span class="font-mono text-[11px] text-zinc-600">{{ g.session_key }}</span>
            <span class="text-[11px] text-zinc-400">轮次 {{ g.rounds_started }} / {{ g.max_rounds }}</span>
            <span v-if="g.blocked_code" class="text-[11px] text-[#ff9f0a]">{{ g.blocked_code }} {{ g.blocked_message }}</span>
            <span class="ml-auto text-[11px] text-zinc-400">{{ fmtTime(g.updated_at) }}</span>
          </div>
          <p class="mt-1 text-sm text-zinc-700 whitespace-pre-wrap break-all">{{ g.objective }}</p>
        </div>
      </div>
    </Card>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { Button, Card, Input, Select, Switch, Tag } from 'animal-island-vue'
import { api } from '../api.js'
import LoadingState from '../components/LoadingState.vue'

const REFRESH_MS = 10000

const icons = {
  chevron: '<svg fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5"/></svg>',
}

// 后台任务类型：与插件 taskstore.go 的 taskKind* 常量保持一致
const kindLabels = {
  turn: '会话回合',
  subagent: '子代理',
  handoff: '交接',
  bash_job: '后台命令',
  clock: '定时任务',
  proactive: '主动互动',
  goal_round: '目标轮次',
  workflow: '后台工作流',
  team_task: '团队任务',
}

const statusLabels = {
  queued: '排队',
  running: '运行中',
  waiting: '等待',
  completed: '完成',
  failed: '失败',
  timeout: '超时',
  cancelled: '取消',
  interrupted: '中断',
}

const stopLabels = {
  completed: '正常完成',
  aborted: '中止',
  max_tokens: '达到上限',
  error: '出错',
  watchdog: '循环保护',
  timeout: '超时',
  user_stop: '用户停止',
}

const statusOptions = [
  { key: '', label: '全部' },
  ...Object.entries(statusLabels).map(([key, label]) => ({ key, label })),
]
const kindOptions = [
  { key: '', label: '全部' },
  ...Object.entries(kindLabels).map(([key, label]) => ({ key, label })),
]

const columns = [
  { key: 'id', title: '任务 ID', dataIndex: 'id', width: '16%' },
  { key: 'kind', title: '类型', dataIndex: 'kind', width: '8%' },
  { key: 'status', title: '状态', dataIndex: 'status', width: '7%' },
  { key: 'title', title: '标题', dataIndex: 'title' },
  { key: 'session_key', title: '会话', dataIndex: 'session_key', width: '11%' },
  { key: 'tokens', title: 'Token', dataIndex: 'tokens', width: '7%' },
  { key: 'duration', title: '耗时', dataIndex: 'duration', width: '7%' },
  { key: 'updated_at', title: '更新时间', dataIndex: 'updated_at', width: '10%' },
  { key: 'note', title: '备注', dataIndex: 'note', width: '8%' },
]

const tasks = ref([])
const goals = ref([])
const loading = ref(true)
const loadError = ref('')
const autoRefresh = ref(true)
const expanded = ref(new Set())
const openFields = ref(new Set())
const filters = reactive({ status: '', kind: '', keyword: '' })

const filtered = computed(() => {
  const kw = filters.keyword.toLowerCase()
  return tasks.value.filter((t) => {
    if (filters.status && t.status !== filters.status) return false
    if (filters.kind && t.kind !== filters.kind) return false
    if (!kw) return true
    return [t.title, t.id, t.session_key].some((s) => (s || '').toLowerCase().includes(kw))
  })
})

const stats = computed(() => {
  const count = (...states) => tasks.value.filter((t) => states.includes(t.status)).length
  return [
    { key: 'running', label: '运行中', ink: 'text-[#0071e3]', value: count('running'), hint: 'running' },
    { key: 'waiting', label: '等待', ink: 'text-[#ff9f0a]', value: count('queued', 'waiting'), hint: 'queued / waiting' },
    { key: 'completed', label: '完成', ink: 'text-[#34c759]', value: count('completed'), hint: 'completed' },
    { key: 'failed', label: '失败 / 异常', ink: 'text-[#ff3b30]', value: count('failed', 'timeout', 'cancelled', 'interrupted'), hint: 'failed / timeout / cancelled / interrupted' },
  ]
})

async function load() {
  loading.value = true
  try {
    const [taskList, goalList] = await Promise.all([api.getAgentTasks(100), api.getAgentGoals()])
    tasks.value = Array.isArray(taskList) ? taskList : []
    goals.value = Array.isArray(goalList) ? goalList : []
    loadError.value = ''
  } catch (e) {
    loadError.value = e.message || '请求失败'
  } finally {
    loading.value = false
  }
}

// Table 无行展开 API：on-row 把点击挂到 <tr> 上
function rowAttrs(record) {
  return {
    class: { 'cursor-pointer': true, 'bg-zinc-50/70': expanded.value.has(record.id) },
    onClick: () => toggleExpand(record.id),
  }
}

function toggleExpand(id) {
  const s = new Set(expanded.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  expanded.value = s
}

function fieldKey(t, name) {
  return `${t.id}:${name}`
}

function isFieldOpen(t, name) {
  return openFields.value.has(fieldKey(t, name))
}

function toggleField(t, name) {
  const key = fieldKey(t, name)
  const s = new Set(openFields.value)
  if (s.has(key)) s.delete(key)
  else s.add(key)
  openFields.value = s
}

function kindText(k) {
  return kindLabels[k] || k || '—'
}

function statusText(s) {
  return statusLabels[s] || s || '—'
}

function stopText(s) {
  return stopLabels[s] || s || '—'
}

function statusColor(s) {
  return {
    queued: 'default',
    running: 'app-blue',
    waiting: 'app-orange',
    completed: 'app-green',
    failed: 'app-red',
    timeout: 'app-orange',
    cancelled: 'default',
    interrupted: 'app-orange',
  }[s] || 'default'
}

// 时间戳均为 unix 秒
function fmtTime(sec) {
  if (!sec) return '—'
  const d = new Date(sec * 1000)
  if (isNaN(d)) return '—'
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false })
  if (d.toDateString() === new Date().toDateString()) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function fmtTimeFull(sec) {
  if (!sec) return '—'
  const d = new Date(sec * 1000)
  if (isNaN(d) || d.getFullYear() < 2000) return '—'
  return d.toLocaleString('zh-CN', { hour12: false })
}

// 耗时：已结束用 finished-started；未结束用 now-started（展示「进行中」）
function fmtDuration(t) {
  const start = t.started_at || t.created_at
  if (!start) return '—'
  const end = t.finished_at || Math.floor(Date.now() / 1000)
  const sec = Math.max(0, end - start)
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`
  return `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m`
}

function fmtInt(n) {
  return Math.round(Number(n) || 0).toLocaleString('en-US')
}

// Meta 是 JSON 字符串：解析失败时原样展示
function prettyMeta(s) {
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}

let timer = null

function applyAutoRefresh() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  if (autoRefresh.value) {
    timer = setInterval(() => {
      // 自动刷新不显示 loading 骨架，避免每 10s 闪一下
      load().catch(() => {})
    }, REFRESH_MS)
  }
}

watch(autoRefresh, applyAutoRefresh)

onMounted(() => {
  load()
  applyAutoRefresh()
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
/* 任务表：自定义 table 以支持"点击行在对应行下方就地展开详情"（组件库 Table 无行展开能力） */
.task-table-wrap {
  overflow-x: auto;
}
.task-table {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
}
.task-table thead th {
  padding: 0.65rem 1rem;
  text-align: left;
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0.15em;
  text-transform: uppercase;
  color: #a1a1aa;
  white-space: nowrap;
  border-bottom: 1px solid var(--line, #ece8e1);
}
.task-table tbody td {
  padding: 0.7rem 1rem;
  font-size: 12px;
  color: var(--ink-2, #3f3a33);
  vertical-align: middle;
  border-bottom: 1px solid #f4f4f5;
}
.task-table-row {
  cursor: pointer;
  transition: background 0.15s ease;
}
.task-table-row:hover {
  background: rgba(0, 0, 0, 0.02);
}
.task-table-row.is-expanded {
  background: #fafafa;
}
.task-table-detail-row > td {
  padding: 0;
  background: #fafafa;
  border-bottom: 1px dashed var(--line, #ece8e1);
}
/* 标题列：限制两行、超出省略；完整标题在悬停提示与展开详情中查看 */
.task-title-cell {
  min-width: 0;
}
.task-title {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  overflow: hidden;
  white-space: normal;
  overflow-wrap: anywhere;
  line-height: 1.45;
  margin: 0;
}
.task-pre {
  margin: 0;
  max-height: 9rem;
  overflow: auto;
  padding: 0.5rem 0.65rem;
  border-radius: 10px;
  background: var(--surface-sunken, #faf9f6);
  border: 1px solid var(--line, #ece8e1);
  font-size: 11px;
  line-height: 1.5;
  color: var(--ink-2, #3f3a33);
  white-space: pre-wrap;
  word-break: break-all;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.task-pre-open {
  max-height: none;
}
</style>
