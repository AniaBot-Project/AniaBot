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
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />{{ oSummary.requests ?? 0 }} Runs</Tag>
          </div>
          <div class="flex-1 py-4">
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtTokens(oSummary.total_tokens) }}</div>
            <div class="tlabel mt-2">历史留存累计</div>
          </div>
          <div class="border-t border-dotted border-zinc-300 pt-2.5 flex justify-between text-[10px] tracking-[0.12em] uppercase text-zinc-500">
            <span>Prompt {{ fmtTokens(oSummary.prompt_tokens) }}</span>
            <span>Completion {{ fmtTokens(oSummary.completion_tokens) }}</span>
          </div>
        </Card>

        <!-- TODAY -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">Today</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#34c759] inline-block mr-1.5" />{{ oToday.requests ?? 0 }} Runs</Tag>
          </div>
          <div class="flex-1 py-4">
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtTokens(oToday.total_tokens) }}</div>
            <div class="tlabel mt-2">今日消耗</div>
          </div>
          <div class="border-t border-dotted border-zinc-300 pt-2.5 flex justify-between text-[10px] tracking-[0.12em] uppercase text-zinc-500">
            <span>Prompt {{ fmtTokens(oToday.prompt_tokens) }}</span>
            <span>Completion {{ fmtTokens(oToday.completion_tokens) }}</span>
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
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ pctText(oSummary.cache_hit_rate) }}</div>
            <div class="tlabel mt-2">Cached / Prompt</div>
          </div>
          <div class="border-t border-dotted border-zinc-300 pt-2.5 text-[10px] tracking-[0.12em] uppercase text-zinc-500">
            命中 {{ fmtTokens(oSummary.cached_tokens) }} tokens
          </div>
        </Card>

        <!-- AVERAGE -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">Average</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />Per Run</Tag>
          </div>
          <div class="flex-1 py-4">
            <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtTokens(Math.round(oSummary.avg_total_tokens || 0)) }}</div>
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
        <span class="text-xs text-zinc-600">Total <b class="text-zinc-900">{{ fmtTokens(summary.total_tokens) }}</b></span>
        <span class="text-xs text-zinc-600">Runs <b class="text-zinc-900">{{ summary.requests ?? 0 }}</b></span>
        <span class="text-xs text-zinc-600">Avg <b class="text-zinc-900">{{ fmtTokens(Math.round(summary.avg_total_tokens || 0)) }}</b> / run</span>
        <span class="text-xs text-zinc-600">Cache <b class="text-zinc-900">{{ pctText(summary.cache_hit_rate) }}</b></span>
        <span class="text-[10px] tracking-[0.12em] uppercase text-zinc-400 ml-auto">{{ daily[0]?.date || '—' }} ~ {{ daily[daily.length - 1]?.date || '—' }}</span>
      </Card>

      <!-- 分来源序列：多天按日、单天（今日/昨日/单日自定义）按小时 -->
      <Card>
        <div class="flex items-center justify-between">
          <span class="tlabel">{{ singleDay ? 'Hourly Tokens · By Source' : 'Daily Tokens · By Source' }}</span>
          <div class="flex items-center gap-3">
            <span class="flex items-center gap-1.5 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
              <i class="w-2 h-2 bg-zinc-700 inline-block rounded-[1px]" />对话
              <i class="w-2 h-2 bg-zinc-500 inline-block rounded-[1px] ml-2" />主动互动
              <i class="w-2 h-2 bg-zinc-300 inline-block rounded-[1px] ml-2" />定时任务
            </span>
            <Button type="text" size="small" @click="loadDetail">刷新</Button>
          </div>
        </div>

        <div class="flex items-end gap-0.75 pt-5 pb-1 h-40">
          <template v-if="singleDay">
            <div v-for="(h, i) in hourly" :key="i" class="flex-1 flex flex-col justify-end h-full min-w-0" :title="hourTip(i, h)">
              <div class="w-full bg-zinc-700" :style="{ height: hourBarH(h.query?.total_tokens) }" />
              <div class="w-full bg-zinc-500" :style="{ height: hourBarH(h.proactive?.total_tokens) }" />
              <div class="w-full bg-zinc-300 rounded-t-sm" :style="{ height: hourBarH(h.task?.total_tokens) }" />
            </div>
          </template>
          <template v-else>
            <div v-for="d in daily" :key="d.date" class="flex-1 flex flex-col justify-end h-full min-w-0" :title="dayTip(d)">
              <div class="w-full bg-zinc-700" :style="{ height: barH(d.query?.total_tokens) }" />
              <div class="w-full bg-zinc-500" :style="{ height: barH(d.proactive?.total_tokens) }" />
              <div class="w-full bg-zinc-300 rounded-t-sm" :style="{ height: barH(d.task?.total_tokens) }" />
            </div>
          </template>
        </div>
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
          <div class="flex h-3 rounded-sm overflow-hidden mt-5 bg-zinc-100">
            <div class="bg-zinc-800" :style="{ width: segW(bySource.query?.total_tokens, bySource.proactive?.total_tokens, bySource.task?.total_tokens) }" />
            <div class="bg-zinc-500" :style="{ width: segW(bySource.proactive?.total_tokens, bySource.query?.total_tokens, bySource.task?.total_tokens) }" />
            <div class="bg-zinc-300" :style="{ width: segW(bySource.task?.total_tokens, bySource.query?.total_tokens, bySource.proactive?.total_tokens) }" />
          </div>
          <dl class="flex-1 mt-3 text-xs">
            <div class="dimrow">
              <dt class="tlabel">对话 Query</dt>
              <dd class="dimval">{{ fmtTokens(bySource.query?.total_tokens) }} <span class="text-zinc-400 font-normal">/ {{ bySource.query?.requests ?? 0 }} runs · avg {{ fmtTokens(Math.round(bySource.query?.avg_total_tokens || 0)) }}</span></dd>
            </div>
            <div class="dimrow">
              <dt class="tlabel">主动互动 Proactive</dt>
              <dd class="dimval">{{ fmtTokens(bySource.proactive?.total_tokens) }} <span class="text-zinc-400 font-normal">/ {{ bySource.proactive?.requests ?? 0 }} runs · avg {{ fmtTokens(Math.round(bySource.proactive?.avg_total_tokens || 0)) }}</span></dd>
            </div>
            <div class="dimrow">
              <dt class="tlabel">定时任务 Cron</dt>
              <dd class="dimval">{{ fmtTokens(bySource.task?.total_tokens) }} <span class="text-zinc-400 font-normal">/ {{ bySource.task?.requests ?? 0 }} runs · avg {{ fmtTokens(Math.round(bySource.task?.avg_total_tokens || 0)) }}</span></dd>
            </div>
          </dl>
        </Card>

        <!-- BY CHAT TYPE -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">By Chat Type</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />群聊 / 私聊</Tag>
          </div>
          <div class="flex h-3 rounded-sm overflow-hidden mt-5 bg-zinc-100">
            <div class="bg-zinc-800" :style="{ width: shareW(byChatType.group?.total_tokens, byChatType.friend?.total_tokens) }" />
            <div class="bg-zinc-300" :style="{ width: shareW(byChatType.friend?.total_tokens, byChatType.group?.total_tokens) }" />
          </div>
          <dl class="flex-1 mt-3 text-xs">
            <div class="dimrow">
              <dt class="tlabel">群聊 Group</dt>
              <dd class="dimval">{{ fmtTokens(byChatType.group?.total_tokens) }} <span class="text-zinc-400 font-normal">/ {{ byChatType.group?.requests ?? 0 }} runs</span></dd>
            </div>
            <div class="dimrow">
              <dt class="tlabel">私聊 Friend</dt>
              <dd class="dimval">{{ fmtTokens(byChatType.friend?.total_tokens) }} <span class="text-zinc-400 font-normal">/ {{ byChatType.friend?.requests ?? 0 }} runs</span></dd>
            </div>
          </dl>
        </Card>

        <!-- BY STATUS -->
        <Card class="flex flex-col">
          <div class="flex items-center justify-between">
            <span class="tlabel">By Status</span>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42] inline-block mr-1.5" />{{ statusTotal }} Finished</Tag>
          </div>
          <div class="flex h-3 rounded-sm overflow-hidden mt-5 bg-zinc-100">
            <div v-for="s in statusList" :key="s.key" :class="s.bar" :style="{ width: statusW(s.count) }" :title="`${s.label} ${s.count}`" />
          </div>
          <dl class="flex-1 mt-3 text-xs">
            <div v-for="s in statusList" :key="s.key" class="dimrow">
              <dt class="tlabel flex items-center gap-1.5"><span class="tdot" :class="s.dot" />{{ s.label }}</dt>
              <dd class="dimval">{{ s.count }} <span class="text-zinc-400 font-normal">{{ statusPct(s.count) }}</span></dd>
            </div>
          </dl>
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
          <div class="flex items-end gap-1 pt-5 pb-1 h-36 xl:flex-1">
            <div v-for="(h, i) in hourly" :key="i" class="flex-1 flex flex-col justify-end h-full" :title="`${i}:00 · ${h.total?.total_tokens || 0} tok · ${h.total?.requests || 0} runs`">
              <div class="w-full rounded-t-sm" :class="(h.total?.total_tokens || 0) > 0 ? 'bg-zinc-700' : 'bg-zinc-200'" :style="{ height: hourBarH(h.total?.total_tokens) }" />
            </div>
          </div>
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
                <Tag variant="outlined" size="small" class="mr-2">{{ record.chat_type === 'group' ? '群' : '私' }}</Tag>
                {{ record.target_id }}
              </span>
            </template>
            <template #cell-share="{ record }">
              <div class="flex items-center gap-2">
                <div class="flex-1 h-1.5 bg-zinc-100 rounded-sm overflow-hidden">
                  <div class="h-full bg-zinc-700" :style="{ width: targetShareW(record.total_tokens) }" />
                </div>
                <span class="text-[10px] text-zinc-400 w-11 text-right shrink-0">{{ targetSharePct(record.total_tokens) }}</span>
              </div>
            </template>
            <template #cell-total_tokens="{ record }">
              <span class="text-zinc-800 font-medium whitespace-nowrap" :title="`${record.total_tokens} tok (prompt ${record.prompt_tokens} / completion ${record.completion_tokens} / cached ${record.cached_tokens})`">{{ fmtTokens(record.total_tokens) }}</span>
            </template>
          </Table>
        </Card>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Button, Card, DatePicker, Divider, Table, Tag } from 'animal-island-vue'
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
  { key: 'success', label: '成功', bar: 'bg-zinc-800', dot: 'bg-[#34c759]' },
  { key: 'skipped', label: '保持安静', bar: 'bg-blue-200', dot: 'bg-[#0071e3]' },
  { key: 'stopped', label: '已停止', bar: 'bg-zinc-400', dot: 'bg-zinc-400' },
  { key: 'timeout', label: '超时', bar: 'bg-zinc-300', dot: 'bg-[#ff9f0a]' },
  { key: 'error', label: '错误', bar: 'bg-zinc-200', dot: 'bg-[#ff3b30]' },
  { key: 'interrupted', label: '中断', bar: 'bg-zinc-200', dot: 'bg-amber-600' },
]

const statusList = computed(() => STATUS_META.map(m => ({ ...m, count: byStatus.value[m.key] || 0 })))
const statusTotal = computed(() => statusList.value.reduce((s, x) => s + x.count, 0))

function statusW(count) {
  if (!statusTotal.value || !count) return '0%'
  return ((count / statusTotal.value) * 100).toFixed(1) + '%'
}

function statusPct(count) {
  if (!statusTotal.value) return '—'
  return ((count / statusTotal.value) * 100).toFixed(1) + '%'
}

// ---- 图表计算 ----

const dayMax = computed(() => Math.max(0, ...daily.value.map(d => d.total?.total_tokens || 0)))

function barH(v) {
  if (!dayMax.value || !v || v <= 0) return '0%'
  return ((v / dayMax.value) * 100).toFixed(1) + '%'
}

const hourMax = computed(() => Math.max(0, ...hourly.value.map(h => h.total?.total_tokens || 0)))

function hourBarH(v) {
  if (!hourMax.value || !v || v <= 0) return '4%'
  return Math.max(4, (v / hourMax.value) * 100).toFixed(1) + '%'
}

const peakHourText = computed(() => {
  if (!hourMax.value) return '—'
  const idx = hourly.value.findIndex(h => (h.total?.total_tokens || 0) === hourMax.value)
  return idx < 0 ? '—' : `${String(idx).padStart(2, '0')}:00 · ${fmtTokens(hourMax.value)}`
})

const targetMax = computed(() => Math.max(0, ...topTargets.value.map(t => t.total_tokens || 0)))

function targetShareW(v) {
  if (!targetMax.value || !v) return '0%'
  return ((v / targetMax.value) * 100).toFixed(1) + '%'
}

function targetSharePct(v) {
  const total = summary.value.total_tokens
  if (!total || !v) return '—'
  return ((v / total) * 100).toFixed(1) + '%'
}

function shareW(a, b) {
  const sum = (a || 0) + (b || 0)
  if (!sum) return '50%'
  return (((a || 0) / sum) * 100).toFixed(1) + '%'
}

// segW 多段堆叠条中某一项的宽度；全为 0 时留空（露出灰底）
function segW(v, ...rest) {
  const sum = (v || 0) + rest.reduce((s, x) => s + (x || 0), 0)
  if (!sum) return '0%'
  return (((v || 0) / sum) * 100).toFixed(1) + '%'
}

// ---- 格式化 ----

function fmtTokens(n) {
  if (n == null || !isFinite(n) || n <= 0) return '0'
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return String(n)
}

function pctText(r) {
  return r != null && r > 0 ? (r * 100).toFixed(1) + '%' : '—'
}

function dayTip(d) {
  return `${d.date} · 共 ${d.total?.total_tokens || 0} tok（对话 ${d.query?.total_tokens || 0} / 主动互动 ${d.proactive?.total_tokens || 0} / 任务 ${d.task?.total_tokens || 0}）· ${d.total?.requests || 0} runs`
}

function hourTip(i, h) {
  return `${String(i).padStart(2, '0')}:00 · 共 ${h.total?.total_tokens || 0} tok（对话 ${h.query?.total_tokens || 0} / 主动互动 ${h.proactive?.total_tokens || 0} / 任务 ${h.task?.total_tokens || 0}）· ${h.total?.requests || 0} runs`
}

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
.dimrow {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.5rem 0;
  border-bottom: 1px dotted rgb(212 212 216);
}
.dimrow:last-child {
  border-bottom: 0;
}
.dimval {
  color: rgb(39 39 42);
  font-weight: 500;
  white-space: nowrap;
}
</style>
