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
          <Button type="text" size="small" :disabled="exporting" @click="exportLogs">导出日志</Button>
          <Button type="default" size="small" danger :disabled="clearing" @click="clearLogs">清除日志</Button>
          <Button type="text" size="small" :disabled="loading" @click="load">刷新</Button>
        </div>
      </div>
    </Card>

    <!-- 从「Query 日志」跳转而来：定位任务详情；不在最近记录中时说明原因 -->
    <Card v-if="linkedTaskID && filters.keyword === linkedTaskID" style="padding: 0">
      <div class="px-5 py-3 flex items-center gap-2 flex-wrap text-xs text-slate-600">
        <template v-if="deepLinkMissing">
          <span>
            未在最近 {{ tasks.length }} 条后台任务中找到
            <span class="font-mono text-slate-700">{{ linkedTaskID }}</span>（可能已过保留期或不在最近记录中）。
            普通回合不写入任务表，完整过程可在 Query 日志查看。
          </span>
          <RouterLink
            class="text-[#0071e3] hover:underline"
            :to="{ path: '/querylogs', query: { task_id: linkedTaskID } }"
          >
            查看 Query 日志 →
          </RouterLink>
        </template>
        <template v-else>
          <span>
            正在定位任务 <span class="font-mono text-slate-700">{{ linkedTaskID }}</span>
            <template v-if="detail">（详情已打开）</template>
          </span>
        </template>
        <Button class="ml-auto" type="text" size="small" @click="clearDeepLink">清除定位</Button>
      </div>
    </Card>

    <!-- 任务列表 -->
    <Card style="padding: 0" class="overflow-hidden">
      <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between gap-3">
        <div class="min-w-0">
          <h2 class="text-[10px] tracking-[0.15em] uppercase text-zinc-800 font-medium">后台任务</h2>
          <p class="text-[11px] text-zinc-400 mt-1">子代理 / 交接 / 工作流 / 团队运行等后台工作的完整执行记录都在这里；普通回合的完整过程请到「Query 日志」查看</p>
        </div>
        <span class="text-[11px] text-zinc-400 shrink-0">
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
        {{ tasks.length ? '没有符合筛选条件的任务' : '暂无后台任务（子代理 / 交接 / 工作流 / 团队运行等工作会登记在这里；普通回合见 Query 日志）' }}
      </p>
      <template v-else>
        <!-- 桌面 / 平板横屏：宽表格 -->
        <div class="task-table-wrap">
          <table class="task-table">
            <thead>
              <tr>
                <th v-for="col in columns" :key="col.key" :style="col.width ? { width: col.width } : undefined">{{ col.title }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="record in filtered"
                :key="record.id"
                class="task-table-row"
                title="点击查看任务详情"
                @click="openDetail(record)"
              >
                <td><span class="block truncate font-mono text-[11px]" :title="record.id">{{ record.id }}</span></td>
                <td><Tag variant="outlined" size="small">{{ kindText(record.kind) }}</Tag></td>
                <td><Tag variant="solid" size="small" :color="statusColor(record.status)">{{ statusText(record.status) }}</Tag></td>
                <td class="task-title-cell">
                  <!-- 标题最多两行（超出省略），悬停看全文；团队成员（有 parent_id）加 ↳ 前缀便于识别归属 -->
                  <p class="task-title font-medium" :title="record.title || ''">
                    <span v-if="record.parent_id" class="text-zinc-300 mr-1" title="团队成员：归属某个团队运行">↳</span>{{ record.title || '(无标题)' }}
                  </p>
                </td>
                <td><span class="block truncate font-mono text-[11px] text-zinc-600" :title="record.session_key || ''">{{ record.session_key || '—' }}</span></td>
                <td><span class="block truncate font-mono text-[11px]" :title="record.tokens ? fmtInt(record.tokens) : ''">{{ record.tokens ? fmtInt(record.tokens) : '—' }}</span></td>
                <td><span class="block truncate text-zinc-500">{{ fmtDuration(record) }}</span></td>
                <td><span class="block truncate text-zinc-600" :title="fmtTimeFull(record.updated_at)">{{ fmtTime(record.updated_at) }}</span></td>
                <td>
                  <span
                    class="block truncate text-[11px]"
                    :class="record.error ? 'text-[#ff3b30]' : 'text-zinc-400'"
                    :title="record.error || record.stop_reason || ''"
                  >
                    {{ record.error ? '错误' : stopText(record.stop_reason) }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 手机 / 窄屏：九列在 500px 内必然互相挤压，改成卡片列表（信息不丢，点击同样打开详情） -->
        <div class="task-cards">
          <div
            v-for="record in filtered"
            :key="record.id"
            class="task-card"
            role="button"
            tabindex="0"
            @click="openDetail(record)"
            @keydown.enter.prevent="openDetail(record)"
            @keydown.space.prevent="openDetail(record)"
          >
            <div class="task-card-top">
              <Tag variant="solid" size="small" :color="statusColor(record.status)">{{ statusText(record.status) }}</Tag>
              <Tag variant="outlined" size="small">{{ kindText(record.kind) }}</Tag>
              <span class="task-card-time" :title="fmtTimeFull(record.updated_at)">{{ fmtTime(record.updated_at) }}</span>
            </div>
            <p class="task-card-title" :title="record.title || ''">
              <span v-if="record.parent_id" class="text-zinc-300 mr-1" title="团队成员：归属某个团队运行">↳</span>{{ record.title || '(无标题)' }}
            </p>
            <div class="task-card-meta">
              <span class="task-card-id font-mono" :title="record.id">{{ shortID(record.id) }}</span>
              <span v-if="record.session_key" class="task-card-session font-mono" :title="record.session_key">{{ record.session_key }}</span>
              <span v-if="record.tokens">{{ fmtInt(record.tokens) }} tok</span>
              <span>{{ fmtDuration(record) }}</span>
              <span
                v-if="record.error || record.stop_reason"
                class="task-card-note"
                :class="record.error ? 'text-[#ff3b30]' : ''"
                :title="record.error || record.stop_reason || ''"
              >{{ record.error ? '错误' : stopText(record.stop_reason) }}</span>
            </div>
          </div>
        </div>
      </template>
    </Card>

    <!-- 任务详情弹窗（与 Query 日志一致：点击行打开，避免就地展开占满表格高度） -->
    <Modal v-model:open="detailOpen" variant="default" :typewriter="false" :show-footer="false" :width="768">
      <template #title>
        <div v-if="detail" class="flex items-center gap-2 flex-wrap w-full font-normal">
          <Tag variant="solid" size="small" :color="statusColor(detail.status)">{{ statusText(detail.status) }}</Tag>
          <Tag variant="outlined" size="small">{{ kindText(detail.kind) }}</Tag>
          <span class="font-mono text-[11px] text-slate-500">{{ detail.session_key || '—' }}</span>
          <span class="ml-auto text-[11px] text-slate-400 font-mono">{{ fmtTimeFull(detail.updated_at) }}</span>
          <Button type="text" size="small" @click="detail = null">✕</Button>
        </div>
      </template>

      <div v-if="detail" class="w-full space-y-3 font-normal">
        <dl class="grid grid-cols-1 sm:grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-xs">
          <dt class="task-dt">任务 ID</dt>
          <dd class="font-mono text-zinc-700 break-all">{{ detail.id }}</dd>
          <dt class="task-dt">标题</dt>
          <dd class="text-zinc-700 whitespace-pre-wrap break-all">{{ detail.title || '—' }}</dd>
          <dt class="task-dt">会话</dt>
          <dd class="font-mono text-zinc-700 break-all">{{ detail.session_key || '—' }}</dd>
          <template v-if="detail.parent_id">
            <dt class="task-dt">父任务</dt>
            <dd class="font-mono text-zinc-700 break-all">{{ detail.parent_id }}</dd>
          </template>
          <dt class="task-dt">创建 / 开始 / 结束</dt>
          <dd class="text-zinc-700">{{ fmtTimeFull(detail.created_at) }} → {{ fmtTimeFull(detail.started_at) }} → {{ fmtTimeFull(detail.finished_at) }}</dd>
          <dt class="task-dt">状态 / 停止原因</dt>
          <dd class="text-zinc-700">{{ statusText(detail.status) }} / {{ stopText(detail.stop_reason) }}</dd>
          <template v-if="detail.prompt">
            <dt class="task-dt">任务指令</dt>
            <dd><pre class="task-pre task-pre-open">{{ detail.prompt }}</pre></dd>
          </template>
        </dl>

        <!-- 子代理/成员执行概况（Meta 的 LLM 轮数与毫秒级耗时；执行中只有耗时） -->
        <p v-if="metaOf(detail).iterations || metaOf(detail).duration_ms" class="text-[11px] text-zinc-500">
          <template v-if="metaOf(detail).iterations">LLM 轮数 {{ metaOf(detail).iterations }}</template>
          <template v-if="metaOf(detail).iterations && metaOf(detail).duration_ms"> · </template>
          <template v-if="metaOf(detail).duration_ms">耗时 {{ fmtMs(metaOf(detail).duration_ms) }}</template>
          <template v-if="detail.status === 'running'"> · 执行中</template>
        </p>

        <!-- 团队成员摘要（team_run 的 Meta.members）：成员产出在该成员的子记录行里 -->
        <div v-if="metaOf(detail).members?.length" class="space-y-2">
          <h3 class="task-subtitle">团队成员</h3>
          <div class="space-y-1.5">
            <div v-for="(m, i) in metaOf(detail).members" :key="i" class="task-member-row">
              <div class="flex items-center gap-2 flex-wrap">
                <span class="text-xs font-medium text-zinc-800">{{ m.name }}</span>
                <Tag variant="solid" size="small" :color="statusColor(m.status)">{{ statusText(m.status) }}</Tag>
                <span v-if="m.tokens" class="text-[11px] text-zinc-400">{{ fmtInt(m.tokens) }} tok</span>
                <span v-if="m.duration_ms" class="text-[11px] text-zinc-400">{{ fmtMs(m.duration_ms) }}</span>
              </div>
              <p v-if="m.degraded" class="text-[11px] text-[#ff9f0a] mt-0.5">{{ m.degraded }}</p>
              <p v-if="m.error" class="text-[11px] text-[#ff3b30] break-all mt-0.5">{{ m.error }}</p>
            </div>
          </div>
        </div>

        <!-- 工具调用步骤（Meta.tool_calls）：看这次子代理/成员具体做了什么 -->
        <div v-if="metaOf(detail).tool_calls?.length" class="space-y-2">
          <h3 class="task-subtitle">
            工具调用
            <!-- 执行中：步骤按约 3 秒增量落盘，面板 10 秒轮询即可看到进展 -->
            <span v-if="detail.status === 'running'" class="normal-case tracking-normal text-zinc-400">
              （执行中，已完成 {{ metaOf(detail).tool_calls_total }} 步）
            </span>
            <span v-else-if="metaOf(detail).tool_calls_total > metaOf(detail).tool_calls.length" class="normal-case tracking-normal text-zinc-300">
              （共 {{ metaOf(detail).tool_calls_total }} 次，仅保留前 {{ metaOf(detail).tool_calls.length }} 条）
            </span>
          </h3>
          <div v-for="(tc, i) in metaOf(detail).tool_calls" :key="i" class="task-tool">
            <div class="flex items-center gap-2 px-3 py-2 border-b border-zinc-100">
              <span class="text-xs font-mono font-medium text-zinc-800">{{ tc.name }}</span>
              <Tag v-if="tc.error" variant="solid" size="small" color="app-red">失败</Tag>
              <span class="ml-auto text-[11px] text-zinc-400 font-mono">{{ fmtMs(tc.duration_ms) }}</span>
            </div>
            <div class="px-3 py-2 space-y-2">
              <div v-if="tc.arguments">
                <div class="task-tool-label">参数</div>
                <pre class="task-tool-pre">{{ tc.arguments }}</pre>
              </div>
              <div v-if="tc.error">
                <div class="task-tool-label text-red-400">错误</div>
                <pre class="task-tool-pre text-[#ff3b30]">{{ tc.error }}</pre>
              </div>
              <div v-else-if="tc.result">
                <div class="task-tool-label">结果</div>
                <pre class="task-tool-pre">{{ tc.result }}</pre>
              </div>
            </div>
          </div>
        </div>

        <!-- 结果 / 错误 / 原始 Meta -->
        <template v-if="detail.error">
          <h3 class="task-subtitle">错误</h3>
          <pre class="task-pre" :class="{ 'task-pre-open': isFieldOpen(detail, 'error') }">{{ detail.error }}</pre>
          <Button v-if="detail.error.length > 200" type="text" size="small" @click="toggleField(detail, 'error')">
            {{ isFieldOpen(detail, 'error') ? '收起' : '展开全部' }}
          </Button>
        </template>
        <template v-if="detail.result">
          <h3 class="task-subtitle">结果</h3>
          <pre class="task-pre" :class="{ 'task-pre-open': isFieldOpen(detail, 'result') }">{{ detail.result }}</pre>
          <Button v-if="detail.result.length > 200" type="text" size="small" @click="toggleField(detail, 'result')">
            {{ isFieldOpen(detail, 'result') ? '收起' : '展开全部' }}
          </Button>
        </template>
        <template v-if="detail.meta && !hasMetaDetail(detail)">
          <h3 class="task-subtitle">Meta</h3>
          <pre class="task-pre task-pre-open">{{ prettyMeta(detail.meta) }}</pre>
        </template>
        <p v-if="!detail.error && !detail.result && !detail.meta && !detail.prompt" class="text-xs text-zinc-400">
          该任务尚无结果 / 错误 / Meta 记录（可能仍在执行中）
        </p>
      </div>
    </Modal>

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
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { Button, Card, Input, Modal, Notification, Select, Switch, Tag } from 'animal-island-vue'
import { api } from '../api.js'
import { confirmAction } from '../ui/confirm'
import LoadingState from '../components/LoadingState.vue'

const route = useRoute()
const router = useRouter()

const REFRESH_MS = 10000

// 后台任务类型：与插件 taskstore.go 的 taskKind* 常量保持一致。
// 普通回合（历史 turn）不再由接口返回，这里只列当前会产生的后台工作类型。
const kindLabels = {
  subagent: '子代理',
  handoff: '交接',
  workflow: '后台工作流',
  team_task: '团队任务',
  team_run: '团队运行',
  team_member: '团队成员',
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
  { key: 'id', title: '任务 ID', dataIndex: 'id', width: '10%' },
  { key: 'kind', title: '类型', dataIndex: 'kind', width: '9%' },
  { key: 'status', title: '状态', dataIndex: 'status', width: '9%' },
  { key: 'title', title: '标题', dataIndex: 'title' },
  { key: 'session_key', title: '会话', dataIndex: 'session_key', width: '9%' },
  { key: 'tokens', title: 'Token', dataIndex: 'tokens', width: '8%' },
  { key: 'duration', title: '耗时', dataIndex: 'duration', width: '8%' },
  { key: 'updated_at', title: '更新时间', dataIndex: 'updated_at', width: '9%' },
  { key: 'note', title: '备注', dataIndex: 'note', width: '8%' },
]

const tasks = ref([])
const goals = ref([])
const loading = ref(true)
const loadError = ref('')
const autoRefresh = ref(true)
const exporting = ref(false)
const clearing = ref(false)
// 从 Query 日志跳转时定位的任务 ID（URL query.task）
const linkedTaskID = ref('')
// 深链只在首次加载/路由变化后自动打开一次，避免自动刷新把用户关掉的弹窗又弹开
let deepLinkOpened = false
// 详情弹窗：detail 为当前任务（null 表示关闭）
const detail = ref(null)
const detailOpen = computed({
  get: () => !!detail.value,
  set: (open) => { if (!open) detail.value = null },
})
const openFields = ref(new Set())
const filters = reactive({ status: '', kind: '', keyword: '' })

// 打开任务详情弹窗（与 Query 日志一致，避免就地展开占满表格高度）
function openDetail(record) {
  detail.value = record
}

// 显式选择具体类型时按类型筛选
const filtered = computed(() => {
  const kw = filters.keyword.toLowerCase()
  return tasks.value.filter((t) => {
    if (filters.status && t.status !== filters.status) return false
    if (filters.kind && t.kind !== filters.kind) return false
    if (!kw) return true
    return [t.title, t.id, t.session_key, t.prompt].some((s) => (s || '').toLowerCase().includes(kw))
  })
})

// 深链定位的任务不在当前列表（已过保留期 / 更旧）时给出说明
const deepLinkMissing = computed(() =>
  !!linkedTaskID.value &&
  filters.keyword === linkedTaskID.value &&
  !loading.value &&
  !tasks.value.some((t) => t.id === linkedTaskID.value),
)

// 统计卡只看当前后台任务，不受状态/关键词筛选影响
const stats = computed(() => {
  const count = (...states) => tasks.value.filter((t) => states.includes(t.status)).length
  return [
    { key: 'running', label: '运行中', ink: 'text-[#0071e3]', value: count('running'), hint: 'running' },
    { key: 'waiting', label: '等待', ink: 'text-[#ff9f0a]', value: count('queued', 'waiting'), hint: 'queued / waiting' },
    { key: 'completed', label: '完成', ink: 'text-[#34c759]', value: count('completed'), hint: 'completed' },
    { key: 'failed', label: '失败 / 异常', ink: 'text-[#ff3b30]', value: count('failed', 'timeout', 'cancelled', 'interrupted'), hint: 'failed / timeout / cancelled / interrupted' },
  ]
})

// 深链定位时多取一些（任务页最多 500 条），提高旧记录命中率
function taskLimit() {
  return linkedTaskID.value ? 500 : 100
}

async function load() {
  loading.value = true
  try {
    const [taskList, goalList] = await Promise.all([api.getAgentTasks(taskLimit()), api.getAgentGoals()])
    // 面板数据源已排除历史 turn；前端再防御一次，避免旧接口/自定义数据源把回合混进来
    tasks.value = (Array.isArray(taskList) ? taskList : []).filter((t) => t.kind !== 'turn')
    goals.value = Array.isArray(goalList) ? goalList : []
    loadError.value = ''
    // 弹窗内容随轮询更新（执行中进度、状态变化）
    if (detail.value) {
      const cur = tasks.value.find((t) => t.id === detail.value.id)
      if (cur) detail.value = cur
    }
    // 深链命中时自动打开详情（只一次）
    if (linkedTaskID.value && !deepLinkOpened) {
      const hit = tasks.value.find((t) => t.id === linkedTaskID.value)
      if (hit) {
        deepLinkOpened = true
        openDetail(hit)
      }
    }
  } catch (e) {
    loadError.value = e.message || '请求失败'
  } finally {
    loading.value = false
  }
}

// 读取 URL 上的 task（由 Query 日志跳转）：写入关键词并打开命中的任务详情
function applyDeepLink() {
  const id = String(route.query.task || '').trim()
  const prev = linkedTaskID.value
  linkedTaskID.value = id
  deepLinkOpened = false
  if (!id) {
    // 浏览器后退等场景把定位参数去掉了：清掉只用于定位的关键词
    if (filters.keyword === prev) filters.keyword = ''
    return
  }
  // 定位优先：清掉可能把目标任务筛掉的状态/类型条件
  filters.status = ''
  filters.kind = ''
  filters.keyword = id
}

// 清除定位：恢复默认范围与关键词，并去掉 URL 参数
function clearDeepLink() {
  linkedTaskID.value = ''
  deepLinkOpened = false
  filters.keyword = ''
  if (route.query.task) {
    const query = { ...route.query }
    delete query.task
    router.replace({ query })
  }
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

// 导出/清除使用与列表相同的筛选口径（后端按条件过滤，可覆盖列表未加载的更早记录）
const hasFilter = computed(() => !!(filters.status || filters.kind || filters.keyword))

function filterParams() {
  return { status: filters.status, kind: filters.kind, keyword: filters.keyword }
}

// 按当前筛选条件导出后台任务日志（JSON 附件，含完整任务指令 / 结果 / 工具步骤）
async function exportLogs() {
  if (exporting.value) return
  exporting.value = true
  try {
    await api.exportAgentTasks(filterParams())
  } catch (e) {
    Notification.error({ message: e.message || '导出失败' })
  } finally {
    exporting.value = false
  }
}

// 清除当前筛选条件下已结束的后台任务日志（运行中/排队中的任务保留），不可恢复
async function clearLogs() {
  if (clearing.value) return
  const scope = hasFilter.value ? '当前筛选条件下的' : '全部'
  if (!await confirmAction(`确定清除${scope}后台任务日志吗？只会删除已结束的任务（运行中/排队中的任务保留），清除后不可恢复，建议先导出备份。`)) return
  clearing.value = true
  try {
    const res = await api.clearAgentTasks(filterParams())
    await load()
    // 详情弹窗对应的任务已被清除时关闭弹窗
    if (detail.value && !tasks.value.some((t) => t.id === detail.value.id)) detail.value = null
    Notification.success({ message: `已清除 ${res.removed ?? 0} 条后台任务日志` })
  } catch (e) {
    Notification.error({ message: e.message || '清除失败' })
  } finally {
    clearing.value = false
  }
}

function kindText(k) {
  return kindLabels[k] || k || '—'
}

// 移动端卡片里的任务 ID：只显示前 10 位，完整 ID 放在 title / 详情弹窗里
function shortID(id) {
  if (!id) return '—'
  return id.length > 10 ? `${id.slice(0, 10)}…` : id
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

// Meta 结构化解析：团队运行（members）/ 子代理与团队成员（tool_calls）在详情里
// 单独渲染，解析失败或未知结构时回退为原始 JSON。按任务 ID 缓存，避免同一行
// 在模板里多次读取时反复 JSON.parse。
const metaById = computed(() => {
  const m = new Map()
  for (const t of tasks.value) m.set(t.id, parseMeta(t.meta))
  return m
})

function parseMeta(raw) {
  if (!raw) return {}
  try {
    const v = JSON.parse(raw)
    return v && typeof v === 'object' ? v : {}
  } catch {
    return { _raw: raw }
  }
}

function metaOf(t) {
  return metaById.value.get(t.id) || {}
}

// 是否有结构化详情（成员摘要 / 工具步骤 / 执行概况）：有则不再重复展示原始 Meta。
function hasMetaDetail(t) {
  const m = metaOf(t)
  return !!(m.members?.length || m.tool_calls?.length || m.iterations || m.duration_ms)
}

// 毫秒级耗时（工具调用与成员的 Meta 字段）
function fmtMs(ms) {
  const n = Number(ms) || 0
  if (n <= 0) return '—'
  if (n < 1000) return `${n}ms`
  if (n < 60000) return `${(n / 1000).toFixed(1)}s`
  const sec = Math.round(n / 1000)
  return `${Math.floor(sec / 60)}m${sec % 60}s`
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

// 同一页面内再次从 Query 日志跳转时重新定位
watch(() => route.query.task, () => {
  applyDeepLink()
  load()
})

onMounted(() => {
  applyDeepLink()
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
/* 九列表格在手机 / 竖屏平板上必然挤压（标签与数字会压到相邻列），窄屏改用卡片列表 */
.task-cards {
  display: none;
}
@media (max-width: 1279px) {
  .task-table-wrap {
    display: none;
  }
  .task-cards {
    display: block;
  }
}
/* 1280–1439 的笔记本侧栏会吃掉宽度：缩小单元格内边距，保证标签不被裁切 */
@media (max-width: 1439px) {
  .task-table thead th,
  .task-table tbody td {
    padding-left: 0.6rem;
    padding-right: 0.6rem;
  }
}
.task-card {
  padding: 0.85rem 1rem;
  border-bottom: 1px solid #f4f4f5;
  cursor: pointer;
  transition: background 0.15s ease;
}
.task-card:last-child {
  border-bottom: 0;
}
.task-card:hover {
  background: rgba(0, 0, 0, 0.02);
}
.task-card:focus-visible {
  background: rgba(0, 0, 0, 0.02);
  outline: 2px solid var(--accent, #19c8b9);
  outline-offset: -2px;
}
.task-card-top {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  flex-wrap: wrap;
}
.task-card-time {
  margin-left: auto;
  font-size: 11px;
  color: #a1a1aa;
  white-space: nowrap;
}
/* 卡片标题最多三行，超出省略；完整标题在悬停提示与详情弹窗中查看 */
.task-card-title {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  line-clamp: 3;
  overflow: hidden;
  margin: 0.5rem 0 0;
  font-size: 13px;
  font-weight: 500;
  line-height: 1.45;
  color: var(--ink-2, #3f3a33);
  overflow-wrap: anywhere;
}
.task-card-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.2rem 0.75rem;
  margin-top: 0.45rem;
  font-size: 11px;
  color: #a1a1aa;
}
.task-card-id {
  max-width: 8.5rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.task-card-session {
  max-width: 8rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.task-card-note {
  margin-left: auto;
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
/* 详情字段名 / 分区标题 */
.task-dt {
  font-size: 10px;
  letter-spacing: 0.15em;
  text-transform: uppercase;
  color: #a1a1aa;
  align-self: center;
}
.task-subtitle {
  margin: 0;
  font-size: 10px;
  letter-spacing: 0.15em;
  text-transform: uppercase;
  font-weight: 500;
  color: #71717a;
}
/* 团队成员摘要行 */
.task-member-row {
  padding: 0.45rem 0.6rem;
  border: 1px solid var(--line, #ece8e1);
  border-radius: 10px;
  background: var(--surface, #fff);
}
/* 工具调用步骤卡片 */
.task-tool {
  border: 1px solid var(--line, #ece8e1);
  border-radius: 10px;
  background: var(--surface, #fff);
  overflow: hidden;
}
.task-tool-label {
  font-size: 10px;
  letter-spacing: 0.15em;
  text-transform: uppercase;
  color: #a1a1aa;
  margin-bottom: 0.2rem;
}
.task-tool-pre {
  margin: 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--ink-2, #3f3a33);
  white-space: pre-wrap;
  word-break: break-all;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
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
