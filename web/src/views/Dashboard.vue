<template>
  <div class="space-y-4">
    <!-- 适配器未连接提示 -->
    <Card
      v-if="status.adapter_status && status.adapter_status !== 'connected'"
      color="app-yellow"
      style="padding: 12px 20px"
      class="flex items-center justify-between gap-4"
    >
      <span class="flex items-center gap-3 text-xs text-[#5f4423]">
        <span class="[&>svg]:w-4 [&>svg]:h-4 shrink-0" v-html="icons.warn" />
        <span class="tracking-wide">
          <span class="uppercase tracking-[0.15em] font-bold">Link Down</span>
          <span class="mx-2 opacity-50">//</span>
          平台适配器未连接<template v-if="status.adapter_detail">：{{ status.adapter_detail }}</template>，Bot 会持续重试
        </span>
      </span>
      <RouterLink to="/config" class="shrink-0">
        <Button type="link" size="small">修改配置并重启 →</Button>
      </RouterLink>
    </Card>

    <!-- 仪器面板 bento 区 -->
    <div class="grid grid-cols-1 xl:grid-cols-12 gap-4">
      <!-- DEVICE CLOCK -->
      <Card class="xl:col-span-5 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">Device Clock</span>
          <Tag color="app-green" variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-white/85" />Sync OK</Tag>
        </div>

        <div class="flex-1 flex items-center justify-between gap-4 py-5">
          <div class="flex items-center gap-5 min-w-0">
            <div class="dotgrid w-14 h-12 shrink-0 hidden sm:block" />
            <div class="text-[44px] sm:text-[56px] leading-none font-semibold tracking-tight text-zinc-900 whitespace-nowrap">
              {{ hh }}<span class="blink">:</span>{{ mm }}<span class="text-2xl text-zinc-400 font-medium ml-1">{{ ss }}</span>
            </div>
          </div>
          <dl class="text-right shrink-0 space-y-1.5">
            <div>
              <dt class="tlabel">Uptime</dt>
              <dd class="text-base font-semibold text-zinc-900 whitespace-nowrap">{{ uptime }}</dd>
            </div>
            <div class="border-t border-dotted border-zinc-300 pt-1.5">
              <dt class="tlabel">Goroutines</dt>
              <dd class="text-base font-semibold text-zinc-900">{{ status.goroutines ?? '—' }}</dd>
            </div>
            <div class="border-t border-dotted border-zinc-300 pt-1.5">
              <dt class="tlabel">Plugins</dt>
              <dd class="text-base font-semibold text-zinc-900">{{ status.plugin_count ?? '—' }}</dd>
            </div>
          </dl>
        </div>

        <div class="text-[10px] tracking-[0.18em] uppercase text-zinc-500">{{ dateLine }}</div>
        <Divider type="dashed-brown" class="my-3" />
        <div class="text-[10px] tracking-[0.14em] uppercase text-zinc-500 truncate">
          Adapter link {{ linked ? 'live' : 'down' }}
          <span class="mx-2 text-zinc-300">//</span>
          Plugin registry {{ status.plugin_count ?? 0 }} loaded
          <span class="mx-2 text-zinc-300">//</span>
          Scheduler {{ armedCount }} armed
        </div>
      </Card>

      <!-- ADAPTER LINK -->
      <Card class="xl:col-span-4 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">Adapter Link</span>
          <Tag :color="linked ? 'app-green' : 'app-orange'" variant="solid" size="small">
            <span class="w-1.5 h-1.5 rounded-full bg-white/85" />
            {{ linked ? 'Link Live' : 'Link Down' }}
          </Tag>
        </div>

        <div class="flex-1 py-5">
          <div class="text-4xl font-semibold tracking-tight" :class="linked ? 'text-zinc-900' : 'text-[#ff9f0a]'">{{ adapterText }}</div>
          <div class="tlabel mt-2">{{ adapterPlatforms }}</div>
        </div>

        <Progress :percent="Math.round((linkSegs / 12) * 100)" :show-info="false" size="small" aria-label="适配器连接状态" />
        <div class="flex items-center justify-between mt-2 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
          <span class="truncate" :title="status.adapter_detail">{{ status.adapter_detail || 'No detail' }}</span>
          <span class="shrink-0 ml-3">Sample 5s</span>
        </div>
      </Card>

      <!-- SCHEDULER -->
      <Card class="xl:col-span-3 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">Scheduler</span>
          <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42]" />{{ clocks.length }} Jobs</Tag>
        </div>

        <div class="flex-1 py-5">
          <div class="text-4xl font-semibold tracking-tight text-zinc-900">
            {{ armedCount }}<span class="text-xl text-zinc-400 font-medium">/{{ clocks.length }}</span>
          </div>
          <div class="tlabel mt-2">Armed jobs</div>
        </div>

        <div class="border-t border-dotted border-zinc-300 pt-3">
          <div class="tlabel">Upcoming runs</div>
          <div v-if="!upcomingRuns.length" class="text-sm font-medium text-zinc-800 mt-1">—</div>
          <ul v-else class="mt-1.5 space-y-1">
            <li
              v-for="(r, i) in upcomingRuns"
              :key="r.id"
              class="flex items-baseline justify-between gap-3"
              :class="i === 0 ? 'text-sm font-semibold text-zinc-900' : 'text-xs text-zinc-600'"
              :title="r.title || r.text"
            >
              <span class="truncate">{{ r.title || r.text }}</span>
              <span class="shrink-0 tabular-nums whitespace-nowrap">
                <template v-if="r.rel">{{ r.rel }} · </template>{{ r.text }}
              </span>
            </li>
          </ul>
        </div>
      </Card>
    </div>

    <!-- 主机监控 -->
    <div class="grid grid-cols-1 xl:grid-cols-12 gap-4">
      <!-- CPU LOAD -->
      <Card class="xl:col-span-4 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">CPU Load</span>
          <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42]" />{{ host.cpu_cores ?? '—' }} Cores</Tag>
        </div>

        <div class="flex items-end justify-between gap-4 pt-4 pb-3">
          <div class="text-4xl font-semibold tracking-tight text-zinc-900">
            {{ cpuText }}<span class="text-xl text-zinc-400 font-medium">%</span>
          </div>
          <span class="tlabel text-zinc-400 pb-1">Sample 5s</span>
        </div>

        <TokenLine
          :values="cpuHistory"
          :tip="cpuTip"
          height="3rem"
          aria-label="CPU 占用率历史"
        />

        <div class="flex items-center justify-between mt-3 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
          <span class="truncate" :title="host.cpu_model">{{ host.cpu_model || 'Unknown CPU' }}</span>
        </div>
      </Card>

      <!-- MEMORY -->
      <Card class="xl:col-span-4 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">Memory</span>
          <Tag :color="host.mem_percent >= 90 ? 'app-red' : host.mem_percent >= 75 ? 'app-orange' : 'app-green'" variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-white/85" />{{ memPctText }}</Tag>
        </div>

        <div class="flex-1 flex items-end justify-between gap-4 pt-4 pb-3">
          <div class="text-4xl font-semibold tracking-tight text-zinc-900">
            {{ fmtBytes(host.mem_used) }}<span class="text-xl text-zinc-400 font-medium">/{{ fmtBytes(host.mem_total) }}</span>
          </div>
          <span class="tlabel text-zinc-400 pb-1">Phys RAM</span>
        </div>

        <Progress :percent="host.mem_total ? Math.round(host.mem_percent || 0) : 0" :show-info="false" size="small" aria-label="内存使用率" />

        <div class="flex items-center justify-between mt-3 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
          <span>{{ fmtBytes(host.mem_total - host.mem_used) }} free</span>
          <span>Bot heap {{ fmtBytes(host.go_mem_alloc) }} · {{ host.go_version || '—' }}</span>
        </div>
      </Card>

      <!-- HOST INFO -->
      <Card class="xl:col-span-4 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">Host Info</span>
          <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42]" />{{ host.os || '—' }} · {{ host.arch || '—' }}</Tag>
        </div>

        <dl class="flex-1 mt-3 text-xs">
          <div class="hostrow">
            <dt class="tlabel">Hostname</dt>
            <dd class="hostval" :title="host.hostname">{{ host.hostname || '—' }}</dd>
          </div>
          <div class="hostrow">
            <dt class="tlabel">System</dt>
            <dd class="hostval" :title="host.os_version">{{ host.os_version || '—' }}</dd>
          </div>
          <div class="hostrow">
            <dt class="tlabel">Kernel</dt>
            <dd class="hostval">{{ host.kernel || '—' }}</dd>
          </div>
          <div class="hostrow">
            <dt class="tlabel">Uptime</dt>
            <dd class="hostval">{{ hostUptimeText }}</dd>
          </div>
        </dl>
      </Card>
    </div>

    <!-- TOKEN USAGE -->
    <div class="grid grid-cols-1 xl:grid-cols-12 gap-4">
      <!-- TOTALS -->
      <Card class="xl:col-span-4 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">Token Usage</span>
          <Tag :color="tokenSummary.cache_hit_rate > 0 ? 'app-green' : 'default'" variant="solid" size="small">
            <span class="w-1.5 h-1.5 rounded-full" :class="tokenSummary.cache_hit_rate > 0 ? 'bg-white/85' : 'bg-[#725d42]'" />
            Cache Hit {{ cacheHitText }}
          </Tag>
        </div>

        <div class="flex-1 py-5">
          <div class="text-4xl font-semibold tracking-tight text-zinc-900">{{ fmtInt(tokenSummary.total_tokens) }}</div>
          <div class="tlabel mt-2">Total tokens · {{ fmtInt(tokenSummary.requests) }} runs</div>
        </div>

        <dl class="text-xs">
          <div class="tokenrow">
            <dt class="tlabel">Today</dt>
            <dd class="tokenval">{{ fmtInt(tokenToday.total_tokens) }} <span class="text-zinc-400 font-normal">/ {{ fmtInt(tokenToday.requests) }} runs</span></dd>
          </div>
          <div class="tokenrow">
            <dt class="tlabel">Prompt / Completion</dt>
            <dd class="tokenval">{{ fmtInt(tokenSummary.prompt_tokens) }} / {{ fmtInt(tokenSummary.completion_tokens) }}</dd>
          </div>
          <div class="tokenrow">
            <dt class="tlabel">Cached</dt>
            <dd class="tokenval">{{ fmtInt(tokenSummary.cached_tokens) }} <span class="text-zinc-400 font-normal">hit {{ cacheHitText }}</span></dd>
          </div>
        </dl>
      </Card>

      <!-- DAILY -->
      <Card class="flex flex-col" :class="balanceEnabled ? 'xl:col-span-5' : 'xl:col-span-8'">
        <div class="flex items-center justify-between">
          <span class="tlabel">Daily Tokens</span>
          <span class="flex items-center gap-3">
            <RouterLink to="/tokenstats">
              <Button type="link" size="small">详细统计 ⤢</Button>
            </RouterLink>
            <Tag variant="solid" size="small"><span class="w-1.5 h-1.5 rounded-full bg-[#725d42]" />14 Days</Tag>
          </span>
        </div>

        <!-- 小屏固定高度：单列布局下 flex-1 无可分配空间会把图表区压缩为 0 -->
        <TokenChart
          :items="dailyColumns"
          :series="TOKEN_SERIES"
          height="9rem"
          class="pt-5"
          aria-label="近 14 天 Token 消耗"
        />
        <div class="flex items-center justify-between mt-1 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
          <span>{{ tokenDaily[0]?.date?.slice(5) || '—' }}</span>
          <span>14 Days</span>
          <span>{{ tokenDaily[tokenDaily.length - 1]?.date?.slice(5) || '—' }}</span>
        </div>
        <Divider type="dashed-brown" class="my-3" />
        <div class="text-[10px] tracking-[0.14em] uppercase text-zinc-500 truncate">
          Source: query &amp; cron logs (retained)
          <span class="mx-2 text-zinc-300">//</span>
          Cache metrics depend on upstream API
        </div>
      </Card>

      <!-- API BALANCE -->
      <Card v-if="balanceEnabled" class="xl:col-span-3 flex flex-col">
        <div class="flex items-center justify-between">
          <span class="tlabel">API Balance</span>
          <Button type="default" size="small" :loading="balanceLoading" @click="loadBalance(true)">刷新</Button>
        </div>

        <div class="flex-1 py-5">
          <div class="text-3xl font-semibold tracking-tight text-zinc-900 break-all">{{ balance?.value || '—' }}</div>
          <div class="tlabel mt-2">LLM API 余额</div>
        </div>

        <div class="border-t border-dotted border-zinc-300 pt-3">
          <div v-if="balance?.error" class="text-[11px] text-[#ff9f0a] truncate" :title="balance.error">查询失败：{{ balance.error }}</div>
          <div class="flex items-center justify-between mt-1 text-[10px] tracking-[0.12em] uppercase text-zinc-400">
            <span>Updated {{ balanceUpdatedText }}</span>
            <span class="shrink-0 ml-2">{{ balance?.cached ? 'Cached' : 'Live' }} · {{ balance?.ttl ?? 0 }}s</span>
          </div>
        </div>
      </Card>
    </div>

    <!-- 插件列表 -->
    <Card style="padding: 0" class="overflow-hidden">
      <div class="px-6 py-4 flex items-center justify-between border-b border-zinc-100">
        <h2 class="tlabel text-zinc-800">Plugin Registry</h2>
        <Tag variant="outlined" size="small">{{ plugins.length }} Modules</Tag>
      </div>
      <Table :columns="pluginColumns" :data-source="plugins" row-key="name" :pagination="false">
        <template #cell-name="{ value }">
          <span class="font-bold text-[#5f4423]">{{ value }}</span>
        </template>
        <template #cell-version="{ value }">
          <Tag variant="soft" size="small">{{ value }}</Tag>
        </template>
        <template #cell-admin_only="{ value }">
          <Tag v-if="value" color="app-orange" variant="solid" size="small">Admin</Tag>
          <Tag v-else variant="outlined" size="small">All</Tag>
        </template>
      </Table>
    </Card>

  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Button, Card, Divider, Progress, Table, Tag } from 'animal-island-vue'
import TokenChart from '../ui/TokenChart.vue'
import TokenLine from '../ui/TokenLine.vue'
import { fmtInt } from '../utils/format.js'
import { api } from '../api.js'

const icons = {
  warn: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z"/></svg>',
}

// 插件注册表列定义（Table 组件）
const pluginColumns = [
  { title: '名称', dataIndex: 'name', width: '20%' },
  { title: '说明', dataIndex: 'help_words' },
  { title: '作者', dataIndex: 'author', width: '16%' },
  { title: '版本', dataIndex: 'version', width: '12%' },
  { title: '可见性', dataIndex: 'admin_only', width: '12%' },
]

const status = ref({})
const host = ref({})
const plugins = ref([])
const clocks = ref([])
const tokenStats = ref({ summary: {}, today: {}, daily: [] })
const balance = ref(null)
const balanceLoading = ref(false)
const now = ref(new Date())
const cpuHistory = ref([]) // CPU 占用率历史（最近 48 个采样点）
let timer = null
let clockTimer = null

// ---- 仪器时钟 ----

const hh = computed(() => String(now.value.getHours()).padStart(2, '0'))
const mm = computed(() => String(now.value.getMinutes()).padStart(2, '0'))
const ss = computed(() => String(now.value.getSeconds()).padStart(2, '0'))

const dateLine = computed(() => {
  const d = now.value
  const weekdays = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
  const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
  const off = -d.getTimezoneOffset() / 60
  const tz = `UTC${off >= 0 ? '+' : '-'}${String(Math.abs(off)).padStart(2, '0')}:00`
  return `${weekdays[d.getDay()]} · ${String(d.getDate()).padStart(2, '0')} ${months[d.getMonth()]} ${d.getFullYear()} · ${tz}`
})

// ---- 状态 ----

const linked = computed(() => status.value.adapter_status === 'connected')

const linkSegs = computed(() => {
  if (linked.value) return 12
  const s = status.value.adapter_status
  if (s === 'connecting' || s === 'reconnecting') return 5
  return 0
})

const uptime = computed(() => {
  const s = status.value.uptime_sec
  if (s == null) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const pad = n => String(n).padStart(2, '0')
  if (d > 0) return `${d}d ${pad(h)}:${pad(m)}`
  return `${pad(h)}:${pad(m)}:${pad(s % 60)}`
})

const adapterText = computed(() => ({
  connected: '已连接',
  connecting: '连接中',
  reconnecting: '重连中',
  setup_pending: '待配置',
  not_started: '未连接',
  unknown: '未知',
}[status.value.adapter_status] || status.value.adapter_status || '—'))

// 各平台适配器状态摘要（如 "qq · 已连接 / feishu · 重连中"）
const adapterPlatforms = computed(() => {
  const list = status.value.adapters
  if (!Array.isArray(list) || list.length === 0) return '—'
  const stateText = { connected: '已连接', connecting: '连接中', reconnecting: '重连中', unknown: '未知' }
  return list.map(a => `${a.platform} · ${stateText[a.state] || a.state || '未知'}`).join(' / ')
})

const armedCount = computed(() => clocks.value.filter(t => t.enabled).length)

// ---- token 消耗监控 ----

const tokenSummary = computed(() => tokenStats.value.summary || {})
const tokenToday = computed(() => tokenStats.value.today || {})
const tokenDaily = computed(() => tokenStats.value.daily || [])

const cacheHitText = computed(() => {
  const r = tokenSummary.value.cache_hit_rate
  return r != null ? (r * 100).toFixed(1) + '%' : '—'
})

// 柱状图系列（自下而上堆叠）：未命中缓存的输入 / 命中缓存 / 输出
const TOKEN_SERIES = [
  { key: 'prompt', label: 'Prompt', tone: 'primary' },
  { key: 'cached', label: 'Cached', tone: 'clay' },
  { key: 'completion', label: 'Completion', tone: 'amber' },
]

const cpuTip = v => `CPU ${Number(v).toFixed(1)}%`

const dailyColumns = computed(() => tokenDaily.value.map(d => {
  const prompt = d.prompt_tokens || 0
  const cached = d.cached_tokens || 0
  return {
    key: d.date,
    values: {
      prompt: Math.max(0, prompt - cached),
      cached,
      completion: d.completion_tokens || 0,
    },
    tip: [
      `${d.date} · ${d.requests || 0} runs`,
      `共 ${fmtInt(d.total_tokens)} tok`,
      `输入 ${fmtInt(prompt)}`,
      `缓存 ${fmtInt(cached)}`,
      `输出 ${fmtInt(d.completion_tokens)}`,
    ].join('\n'),
  }
}))

// ---- API 余额（服务端执行自定义 JS 查询，结果有缓存） ----

const balanceEnabled = computed(() => balance.value?.enabled === true)

const balanceUpdatedText = computed(() => {
  const t = balance.value?.updated_at
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d)) return '—'
  const pad = n => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
})

async function loadBalance(refresh = false) {
  if (refresh) balanceLoading.value = true
  try { balance.value = await api.getBalance(refresh) } catch { /* 忽略轮询错误 */ }
  finally { balanceLoading.value = false }
}

// ---- 主机监控 ----

const cpuText = computed(() => {
  const p = host.value.cpu_percent
  return p != null && p >= 0 ? p.toFixed(1) : '—'
})

const memPctText = computed(() => {
  const p = host.value.mem_percent
  return p != null && host.value.mem_total ? p.toFixed(1) + '%' : '—'
})

const hostUptimeText = computed(() => {
  const s = host.value.uptime_sec
  if (!s) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const pad = n => String(n).padStart(2, '0')
  return d > 0 ? `${d}d ${pad(h)}:${pad(m)}` : `${pad(h)}:${pad(m)}`
})

function fmtBytes(b) {
  if (b == null || !isFinite(b) || b <= 0) return '0M'
  const gb = b / 1073741824
  if (gb >= 10) return gb.toFixed(0) + 'G'
  if (gb >= 1) return gb.toFixed(1) + 'G'
  return Math.round(b / 1048576) + 'M'
}

// 即将执行的定时任务（启用且下次触发时间有效，按时间升序，最多 5 条）。
// 历史数据可能残留上次触发时间（如昨天），读取时过滤过期时间并依赖服务端刷新。
const UPCOMING_RUNS_LIMIT = 5

const upcomingRuns = computed(() => {
  const nowTs = Date.now()
  return clocks.value
    .filter(t => t.enabled && t.next_run_at)
    .map(t => {
      const d = new Date(t.next_run_at)
      return { id: t.id, title: t.title || '', raw: d, ts: d.getTime() }
    })
    .filter(x => !isNaN(x.ts) && x.raw.getFullYear() >= 2000 && x.ts > nowTs - 60_000)
    .sort((a, b) => a.ts - b.ts)
    .slice(0, UPCOMING_RUNS_LIMIT)
    .map(x => ({ ...x, text: fmtRunTime(x.raw), rel: fmtRunRelative(x.raw) }))
})

function fmtRunTime(d) {
  const pad = n => String(n).padStart(2, '0')
  const base = `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  return d.getFullYear() === new Date().getFullYear() ? base : `${d.getFullYear()}-${base}`
}

function fmtRunRelative(d) {
  const min = Math.round((d.getTime() - Date.now()) / 60000)
  if (min <= 0) return '即将触发'
  if (min < 60) return `${min} 分钟后`
  const hour = Math.round(min / 60)
  if (hour < 24) return `${hour} 小时后`
  const day = Math.round(hour / 24)
  if (day < 7) return `${day} 天后`
  return ''
}

async function loadStatus() {
  try { status.value = await api.getStatus() } catch { /* 忽略轮询错误 */ }
}

async function loadHost() {
  try {
    const h = await api.getHost()
    host.value = h
    if (Array.isArray(h.cpu_history) && h.cpu_history.length) {
      // 服务端缓存的完整历史（新在后），打开页面即有完整曲线，取最近 48 个点绘图
      cpuHistory.value = h.cpu_history.slice(-48)
    } else if (h.cpu_percent != null && h.cpu_percent >= 0) {
      // 兼容无历史缓存的旧后端：本地逐点积累
      const hist = [...cpuHistory.value, h.cpu_percent]
      cpuHistory.value = hist.length > 48 ? hist.slice(hist.length - 48) : hist
    }
  } catch { /* 忽略轮询错误 */ }
}

async function loadClocks() {
  try { clocks.value = await api.getClocks() } catch { /* 忽略 */ }
}

async function loadTokenStats() {
  try { tokenStats.value = await api.getTokenStats() } catch { /* 忽略轮询错误 */ }
}

// 实时刷新：状态 / 定时任务统一轮询；标签页隐藏时暂停，恢复可见时立即刷新
function poll() {
  loadStatus()
  loadHost()
  loadClocks()
  loadTokenStats()
  loadBalance()
}

function onVisible() {
  if (!document.hidden) poll()
}

onMounted(async () => {
  poll()
  try { plugins.value = await api.getPlugins() } catch { /* 忽略 */ }
  timer = setInterval(() => { if (!document.hidden) poll() }, 5000)
  clockTimer = setInterval(() => { now.value = new Date() }, 1000)
  document.addEventListener('visibilitychange', onVisible)
})

onUnmounted(() => {
  clearInterval(timer)
  clearInterval(clockTimer)
  document.removeEventListener('visibilitychange', onVisible)
})
</script>

<style scoped>
.hostrow {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.5rem 0;
  border-bottom: 1px dotted rgb(212 212 216);
}
.hostrow:last-child {
  border-bottom: 0;
}
.tokenrow {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.5rem 0;
  border-bottom: 1px dotted rgb(212 212 216);
}
.tokenrow:last-child {
  border-bottom: 0;
}
.tokenval {
  color: rgb(39 39 42);
  font-weight: 500;
  white-space: nowrap;
}
.hostval {
  color: rgb(39 39 42);
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
