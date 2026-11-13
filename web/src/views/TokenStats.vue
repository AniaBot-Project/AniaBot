<template>
  <div class="space-y-6">
    <!-- ===== 总览（全量口径，不随筛选变化） ===== -->
    <div class="space-y-4">
      <div class="flex items-center justify-between">
        <span class="tlabel text-zinc-800">Overview · 总览</span>
        <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">历史留存 + 今日</span>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
        <!-- TOTAL -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">Total Tokens</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />{{ fmtInt(oSummary.requests) }} Runs</Tag>
          </div>
          <div class="flex-1 py-4">
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtInt(oSummary.total_tokens) }}</div>
            <div class="tlabel mt-2">历史留存累计</div>
          </div>
          <div class="border-t border-dotted border-zinc-300 pt-2.5 flex justify-between text-[10px] tracking-[0.12em] uppercase text-zinc-500">
            <span>Prompt {{ fmtInt(oSummary.prompt_tokens) }}</span>
            <span>Completion {{ fmtInt(oSummary.completion_tokens) }}</span>
          </div>
        </Card>

        <!-- TODAY -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">Today</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#34c759] inline-block mr-1.5" />{{ fmtInt(oToday.requests) }} Runs</Tag>
          </div>
          <div class="flex-1 py-4">
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtInt(oToday.total_tokens) }}</div>
            <div class="tlabel mt-2">今日消耗</div>
          </div>
          <div class="border-t border-dotted border-zinc-300 pt-2.5 flex justify-between text-[10px] tracking-[0.12em] uppercase text-zinc-500">
            <span>Prompt {{ fmtInt(oToday.prompt_tokens) }}</span>
            <span>Completion {{ fmtInt(oToday.completion_tokens) }}</span>
          </div>
        </Card>

        <!-- CACHE -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">Cache Hit</span>
            <Tag :color="oSummary.cache_hit_rate > 0 ? 'app-green' : 'default'" variant="solid" size="small">
              <span class="w-1.5 h-1.5 rounded-full inline-block mr-1.5" :class="oSummary.cache_hit_rate > 0 ? 'bg-white/85' : 'bg-[#725d42]'" />
              {{ oSummary.cache_hit_rate > 0 ? 'Active' : 'N/A' }}
            </Tag>
          </div>
          <div class="flex-1 py-4">
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtPct(oSummary.cache_hit_rate) }}</div>
            <div class="tlabel mt-2">Cached / Prompt</div>
          </div>
          <div class="border-t border-dotted border-zinc-300 pt-2.5 text-[10px] tracking-[0.12em] uppercase text-zinc-500">
            命中 {{ fmtInt(oSummary.cached_tokens) }} tokens
          </div>
        </Card>

        <!-- AVERAGE -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">Average</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />Per Run</Tag>
          </div>
          <div class="flex-1 py-4">
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtInt(Math.round(oSummary.avg_total_tokens || 0)) }}</div>
            <div class="tlabel mt-2">单次平均消耗</div>
          </div>
          <div class="border-t border-dotted border-zinc-300 pt-2.5 text-[10px] tracking-[0.12em] uppercase text-zinc-500">
            平均 {{ (overview.avg_iterations || 0).toFixed(1) }} 轮 LLM 调用 / 次
          </div>
        </Card>
      </div>
    </div>

    <!-- ===== 细节（按时间维度筛选） ===== -->
    <div class="space-y-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <span class="tlabel text-zinc-800">Detail · 细节统计</span>
        <!-- 时间维度筛选 -->
        <div class="filter-group flex-wrap">
          <Button
            v-for="r in RANGE_OPTIONS" :key="r.key"
            :type="range === r.key ? 'primary' : 'default'"
            size="small"
            @click="range = r.key"
          >{{ r.label }}</Button>
          <template v-if="range === 'custom'">
            <DatePicker v-model="customStart" size="small" allow-clear :disabled-date="disableFuture" placeholder="开始日期" />
            <span class="text-zinc-400 text-xs">~</span>
            <DatePicker v-model="customEnd" size="small" allow-clear :disabled-date="disableFuture" placeholder="结束日期" />
            <Button type="primary" size="small" @click="applyCustom">查询</Button>
          </template>
        </div>
      </div>
      <p v-if="customError" class="text-[10px] tracking-[0.12em] uppercase text-[#ff3b30]">{{ customError }}</p>

      <!-- 范围摘要条 -->
      <Card class="flex items-center gap-x-6 gap-y-2 flex-wrap">
        <span class="tlabel text-zinc-800">{{ rangeMeta.label }}</span>
        <span class="text-xs text-zinc-600">Total <b class="text-zinc-900">{{ fmtInt(summary.total_tokens) }}</b></span>
        <span class="text-xs text-zinc-600">Runs <b class="text-zinc-900">{{ fmtInt(summary.requests) }}</b></span>
        <span class="text-xs text-zinc-600">Avg <b class="text-zinc-900">{{ fmtInt(Math.round(summary.avg_total_tokens || 0)) }}</b> / run</span>
        <span class="text-xs text-zinc-600">Cache <b class="text-zinc-900">{{ fmtPct(summary.cache_hit_rate) }}</b></span>
        <span class="text-[10px] tracking-[0.12em] uppercase text-zinc-400 ml-auto">{{ daily[0]?.date || '—' }} ~ {{ daily[daily.length - 1]?.date || '—' }}</span>
      </Card>

      <!-- 分来源序列：多天按日、单天（今日/昨日/单日自定义）按小时 -->
      <Card>
        <div class="flex items-center justify-between">
          <span class="tlabel">{{ singleDay ? 'Hourly Tokens · By Source' : 'Daily Tokens · By Source' }}</span>
          <Button type="text" size="small" @click="loadDetail">刷新</Button>
        </div>

        <TokenChart
          :items="columnItems"
          :series="SOURCE_SERIES"
          height="10rem"
          class="pt-5"
          aria-label="按来源的 Token 消耗"
        />
        <div class="flex items-center justify-between mt-2 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
          <template v-if="singleDay">
            <span>00:00</span>
            <span>{{ daily[0]?.date || '—' }}</span>
            <span>23:00</span>
          </template>
          <template v-else>
            <span>{{ daily[0]?.date?.slice(5) || '—' }}</span>
            <span>{{ daily.length }} Days</span>
            <span>{{ daily[daily.length - 1]?.date?.slice(5) || '—' }}</span>
          </template>
        </div>
        <Divider type="dashed-brown" class="my-3" />
        <div class="text-[10px] tracking-[0.14em] uppercase text-zinc-500 truncate">
          Source: query / proactive / cron logs (retained, capped)
          <span class="mx-2 text-zinc-300">//</span>
          Cache metrics depend on upstream API
        </div>
      </Card>

      <!-- 拆分维度 -->
      <div class="grid grid-cols-1 xl:grid-cols-3 gap-4">
        <!-- BY SOURCE -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">By Source</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />对话 / 主动互动 / 任务</Tag>
          </div>
          <div class="flex-1 mt-5 space-y-4">
            <div v-for="s in sourceRows" :key="s.key">
              <div class="flex items-baseline justify-between gap-3">
                <span class="tlabel">{{ s.label }}</span>
                <span class="dimval">{{ fmtInt(s.tokens) }}</span>
              </div>
              <Progress
                class="mt-1.5"
                :percent="s.pct"
                size="small"
                :info-format="pctFmt"
                :aria-label="`${s.label} Token 占比`"
              />
              <div class="mt-1 text-[10px] tracking-[0.1em] uppercase text-zinc-400">
                {{ fmtInt(s.runs) }} runs · avg {{ fmtInt(s.avg) }}
              </div>
            </div>
          </div>
        </Card>

        <!-- BY CHAT TYPE -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">By Chat Type</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />群聊 / 私聊 / 试聊</Tag>
          </div>
          <div class="flex-1 mt-5 space-y-4">
            <div v-for="c in chatTypeRows" :key="c.key">
              <div class="flex items-baseline justify-between gap-3">
                <span class="tlabel">{{ c.label }}</span>
                <span class="dimval">{{ fmtInt(c.tokens) }}</span>
              </div>
              <Progress
                class="mt-1.5"
                :percent="c.pct"
                size="small"
                :info-format="pctFmt"
                :aria-label="`${c.label} Token 占比`"
              />
              <div class="mt-1 text-[10px] tracking-[0.1em] uppercase text-zinc-400">
                {{ fmtInt(c.runs) }} runs
              </div>
            </div>
          </div>
        </Card>

        <!-- BY STATUS -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">By Status</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />{{ fmtInt(statusTotal) }} Finished</Tag>
          </div>
          <div class="flex-1 mt-5 space-y-2">
            <div v-for="s in statusRows" :key="s.key" class="flex items-center gap-2.5">
              <span class="tlabel w-[76px] shrink-0 flex items-center gap-1.5"><span class="tdot" :class="s.dot" />{{ s.label }}</span>
              <Progress
                class="flex-1"
                :percent="s.pct"
                size="small"
                :show-info="false"
                :aria-label="`${s.label} 占比`"
              />
              <span class="dimval w-[92px] text-right shrink-0">
                {{ fmtInt(s.count) }} <span class="text-zinc-400 font-normal">{{ statusPct(s.count) }}</span>
              </span>
            </div>
          </div>
        </Card>
      </div>

      <!-- 小时分布 + 目标排行（单天维度时主图已是小时序列，隐藏小时卡片） -->
      <div class="grid grid-cols-1 xl:grid-cols-12 gap-4">
        <!-- HOURLY -->
        <Card v-if="!singleDay" class="xl:col-span-5 flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">Hourly Distribution</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />24h</Tag>
          </div>
          <!-- 小屏固定高度：单列布局下 flex-1 无可分配空间会把图表区压缩为 0 -->
          <TokenChart
            :items="hourlyItems"
            :series="HOURLY_SERIES"
            :legend="false"
            height="9rem"
            :min-pct="4"
            track
            class="pt-5"
            aria-label="24 小时 Token 分布"
          />
          <div class="flex justify-between mt-2 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
            <span>00</span><span>06</span><span>12</span><span>18</span><span>23</span>
          </div>
          <Divider type="dashed-brown" class="my-3" />
          <div class="text-[10px] tracking-[0.14em] uppercase text-zinc-500">
            Peak {{ peakHourText }}
          </div>
        </Card>

        <!-- TOP TARGETS -->
        <Card style="padding: 0" class="overflow-hidden flex flex-col" :class="singleDay ? 'xl:col-span-12' : 'xl:col-span-7'">
          <div class="px-6 py-4 flex items-center justify-between border-b border-zinc-100">
            <h2 class="tlabel text-zinc-800">Top Targets</h2>
            <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">Top {{ topTargets.length }} by Tokens</span>
          </div>
          <Table
            :columns="targetColumns"
            :data-source="topTargets"
            :row-key="r => r.chat_type + ':' + r.target_id"
            :pagination="false"
            empty-text="暂无消耗记录"
          >
            <template #cell-rank="{ index }">
              <span class="text-zinc-400">{{ index + 1 }}</span>
            </template>
            <template #cell-target_id="{ record }">
              <span class="text-zinc-800 font-medium whitespace-nowrap">
                <Tag variant="outlined" size="small" class="mr-2">{{ chatTypeGlyph(record.chat_type) }}</Tag>
                {{ record.target_id }}
              </span>
            </template>
            <template #cell-share="{ record }">
              <div class="flex items-center gap-2">
                <Progress
                  class="flex-1"
                  :percent="targetShareW(record.total_tokens)"
                  size="small"
                  :show-info="false"
                  :aria-label="`${record.target_id} Token 占比`"
                />
                <span class="text-[10px] text-zinc-400 w-11 text-right shrink-0">{{ targetSharePct(record.total_tokens) }}</span>
              </div>
            </template>
            <template #cell-requests="{ record }">
              <span class="text-zinc-600 whitespace-nowrap">{{ fmtInt(record.requests) }}</span>
            </template>
            <template #cell-total_tokens="{ record }">
              <Tooltip :title="targetTip(record)" placement="top-end">
                <span class="text-zinc-800 font-medium whitespace-nowrap">{{ fmtInt(record.total_tokens) }}</span>
              </Tooltip>
            </template>
          </Table>
        </Card>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Button, Card, DatePicker, Divider, Progress, Table, Tag, Tooltip } from 'animal-island-vue'
import TokenChart from '../ui/TokenChart.vue'
import { fmtInt, fmtPct } from '../utils/format.js'
import { api } from '../api.js'

// 时间维度筛选选项（与后端 resolveTokenRange 对应）
const RANGE_OPTIONS = [
  { key: 'all', label: '全部' },
  { key: 'today', label: '今日' },
  { key: 'yesterday', label: '昨日' },
  { key: '7d', label: '近 7 天' },
  { key: '30d', label: '近 30 天' },
  { key: 'month', label: '本月' },
  { key: 'custom', label: '自定义' },
]

// 目标排行列定义（Table 组件）
const targetColumns = [
  { title: '#', dataIndex: 'rank', width: '8%' },
  { title: '目标', dataIndex: 'target_id' },
  { title: '占比', dataIndex: 'share', width: '26%' },
  { title: '次数', dataIndex: 'requests', width: '12%', align: 'right' },
  { title: 'Tokens', dataIndex: 'total_tokens', width: '14%', align: 'right' },
]

const CUSTOM_MAX_DAYS = 62

// 柱状图系列（自下而上堆叠）
const SOURCE_SERIES = [
  { key: 'query', label: '对话', tone: 'primary' },
  { key: 'proactive', label: '主动互动', tone: 'clay' },
  { key: 'task', label: '定时任务', tone: 'amber' },
]

const HOURLY_SERIES = [{ key: 'total', label: 'Tokens', tone: 'primary' }]

const overview = ref({})
const detail = ref({})
const range = ref('all')
const customStart = ref('')
const customEnd = ref('')
const customError = ref('')
// 实际生效的自定义范围（仅在点「查询」后更新）
const appliedCustom = ref(null)
let timer = null

// 本地日期 → 2006-01-02 格式
function localDate(d) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

const todayStr = localDate(new Date())
// 自定义默认选最近 7 天
customStart.value = localDate(new Date(Date.now() - 6 * 864e5))
customEnd.value = todayStr

// 日期选择器禁用未来日期（对齐原日期输入的 :max="todayStr"）
function disableFuture(d) {
  return localDate(d) > todayStr
}

// 当前范围的展示文案
const rangeMeta = computed(() => {
  if (range.value === 'custom') {
    return { label: appliedCustom.value ? `${appliedCustom.value.start} ~ ${appliedCustom.value.end}` : '自定义范围' }
  }
  const opt = RANGE_OPTIONS.find(r => r.key === range.value) || RANGE_OPTIONS[0]
  return { label: opt.key === 'all' ? '全部留存' : opt.label }
})

// ---- 总览（全量口径） ----

const oSummary = computed(() => overview.value.summary || {})
const oToday = computed(() => overview.value.today || {})

async function loadOverview() {
  try { overview.value = await api.getTokenStatsDetail({ range: 'all' }) } catch { /* 忽略轮询错误 */ }
}

// ---- 细节（按筛选窗口） ----

const summary = computed(() => detail.value.summary || {})
const bySource = computed(() => detail.value.by_source || {})
const byChatType = computed(() => detail.value.by_chat_type || {})
const byStatus = computed(() => detail.value.by_status || {})
const topTargets = computed(() => detail.value.top_targets || [])
const hourly = computed(() => detail.value.hourly || [])
const daily = computed(() => detail.value.daily || [])
const singleDay = computed(() => daily.value.length === 1)

async function loadDetail() {
  const params = { range: range.value }
  if (range.value === 'custom') {
    if (!appliedCustom.value) return
    params.start = appliedCustom.value.start
    params.end = appliedCustom.value.end
  }
  try { detail.value = await api.getTokenStatsDetail(params) } catch { /* 忽略轮询错误 */ }
}

function applyCustom() {
  customError.value = ''
  if (!customStart.value || !customEnd.value) {
    customError.value = '请选择起止日期'
    return
  }
  if (customStart.value > customEnd.value) {
    customError.value = '起始日期不能晚于结束日期'
    return
  }
  const spanDays = Math.round((new Date(customEnd.value) - new Date(customStart.value)) / 864e5) + 1
  if (spanDays > CUSTOM_MAX_DAYS) {
    customError.value = `跨度最长 ${CUSTOM_MAX_DAYS} 天（当前 ${spanDays} 天）`
    return
  }
  appliedCustom.value = { start: customStart.value, end: customEnd.value }
  loadDetail()
}

watch(range, () => {
  customError.value = ''
  if (range.value === 'custom') {
    // 首次切到自定义时用默认范围直接查询
    if (!appliedCustom.value) applyCustom()
    return
  }
  loadDetail()
})

// ---- 状态分布 ----

const STATUS_META = [
  { key: 'success', label: '成功', dot: 'bg-[#34c759]' },
  { key: 'skipped', label: '保持安静', dot: 'bg-[#0071e3]' },
  { key: 'stopped', label: '已停止', dot: 'bg-zinc-400' },
  { key: 'timeout', label: '超时', dot: 'bg-[#ff9f0a]' },
  { key: 'error', label: '错误', dot: 'bg-[#ff3b30]' },
  { key: 'interrupted', label: '中断', dot: 'bg-amber-600' },
]

const statusList = computed(() => STATUS_META.map(m => ({ ...m, count: byStatus.value[m.key] || 0 })))
const statusTotal = computed(() => statusList.value.reduce((s, x) => s + x.count, 0))
const statusRows = computed(() => statusList.value.map(s => ({
  ...s,
  pct: statusTotal.value ? (s.count / statusTotal.value) * 100 : 0,
})))

function statusPct(count) {
  if (!statusTotal.value) return '—'
  return ((count / statusTotal.value) * 100).toFixed(1) + '%'
}

// ---- 图表数据 ----

const columnItems = computed(() => {
  const rows = singleDay.value ? hourly.value : daily.value
  return rows.map((row, i) => {
    const values = {
      query: row.query?.total_tokens || 0,
      proactive: row.proactive?.total_tokens || 0,
      task: row.task?.total_tokens || 0,
    }
    const head = singleDay.value ? `${String(i).padStart(2, '0')}:00` : row.date
    return {
      key: singleDay.value ? i : row.date,
      values,
      tip: [
        `${head} · ${fmtInt(row.total?.requests)} runs`,
        `共 ${fmtInt(row.total?.total_tokens)} tok`,
        `对话 ${fmtInt(values.query)}`,
        `主动互动 ${fmtInt(values.proactive)}`,
        `定时任务 ${fmtInt(values.task)}`,
      ].join('\n'),
    }
  })
})

const hourlyItems = computed(() => hourly.value.map((h, i) => ({
  key: i,
  values: { total: h.total?.total_tokens || 0 },
  tip: `${String(i).padStart(2, '0')}:00 · ${fmtInt(h.total?.total_tokens)} tok · ${fmtInt(h.total?.requests)} runs`,
})))

// 拆分维度：百分比取自本维度合计，Progress 负责画条，文案里保留原始数值
function rowsWithShare(rows) {
  const sum = rows.reduce((s, r) => s + (r.data?.total_tokens || 0), 0)
  return rows.map(r => ({
    key: r.key,
    label: r.label,
    tokens: r.data?.total_tokens || 0,
    runs: r.data?.requests || 0,
    avg: Math.round(r.data?.avg_total_tokens || 0),
    pct: sum ? ((r.data?.total_tokens || 0) / sum) * 100 : 0,
  }))
}

const sourceRows = computed(() => rowsWithShare([
  { key: 'query', label: '对话 Query', data: bySource.value.query },
  { key: 'proactive', label: '主动互动 Proactive', data: bySource.value.proactive },
  { key: 'task', label: '定时任务 Cron', data: bySource.value.task },
]))

const chatTypeRows = computed(() => rowsWithShare([
  { key: 'group', label: '群聊 Group', data: byChatType.value.group },
  { key: 'friend', label: '私聊 Friend', data: byChatType.value.friend },
  { key: 'panel', label: '试聊 Panel', data: byChatType.value.panel },
]))

const pctFmt = p => (p > 0 ? p.toFixed(1) + '%' : '0%')

const peakHourText = computed(() => {
  const max = Math.max(0, ...hourly.value.map(h => h.total?.total_tokens || 0))
  if (!max) return '—'
  const idx = hourly.value.findIndex(h => (h.total?.total_tokens || 0) === max)
  return idx < 0 ? '—' : `${String(idx).padStart(2, '0')}:00 · ${fmtInt(max)}`
})

const targetMax = computed(() => Math.max(0, ...topTargets.value.map(t => t.total_tokens || 0)))

function targetShareW(v) {
  if (!targetMax.value || !v) return 0
  return (v / targetMax.value) * 100
}

function targetSharePct(v) {
  const total = summary.value.total_tokens
  if (!total || !v) return '—'
  return ((v / total) * 100).toFixed(1) + '%'
}

// 会话类型徽标：群聊 / 私聊 / 面板试聊
function chatTypeGlyph(t) {
  return { group: '群', friend: '私', panel: '试' }[t] || '?'
}

function targetTip(record) {
  return [
    `${record.target_id} · ${fmtInt(record.requests)} runs`,
    `合计 ${fmtInt(record.total_tokens)} tok`,
    `输入 ${fmtInt(record.prompt_tokens)}`,
    `输出 ${fmtInt(record.completion_tokens)}`,
    `缓存 ${fmtInt(record.cached_tokens)}`,
  ].join('\n')
}

// ---- 格式化 ----

function load() {
  loadOverview()
  loadDetail()
}

function onVisible() {
  if (!document.hidden) load()
}

onMounted(() => {
  load()
  timer = setInterval(() => { if (!document.hidden) load() }, 30000)
  document.addEventListener('visibilitychange', onVisible)
})

onUnmounted(() => {
  clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisible)
})
</script>

<style scoped>
.dimval {
  color: rgb(39 39 42);
  font-weight: 500;
  white-space: nowrap;
}
</style>
