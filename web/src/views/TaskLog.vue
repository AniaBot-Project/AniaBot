<template>
  <div class="space-y-4">
    <!-- 定时任务管理 -->
    <Card style="padding: 0" class="overflow-hidden">
      <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between">
        <h2 class="text-[10px] tracking-[0.15em] uppercase text-zinc-800 font-medium">定时任务</h2>
        <div class="flex items-center gap-3">
          <Button type="text" size="small" @click="loadClocks">刷新</Button>
          <Button type="primary" size="small" @click="openCreate">新建任务</Button>
        </div>
      </div>
      <p v-if="clocks.length === 0" class="px-6 py-8 text-xs text-zinc-400 text-center tracking-wide">暂无定时任务，点击右上角「新建任务」创建（也可在群聊/私聊中使用 /clock）</p>
      <template v-else>
        <Table
          :columns="clockColumns"
          :data-source="clocks"
          row-key="id"
          :pagination="false"
          :striped="false"
          :on-row="clockRowAttrs"
        >
          <template #cell-title="{ record }">
            <span class="flex items-center gap-1.5">
              <span
                class="[&>svg]:w-3 [&>svg]:h-3 text-zinc-400 transition-transform shrink-0"
                :class="{ 'rotate-90': expanded.has(record.id) }"
                v-html="icons.chevron"
              />
              <span class="truncate font-medium" :title="record.title">{{ record.title || '(无标题)' }}</span>
            </span>
            <Tag v-if="record.run_once" variant="outlined" size="small" class="ml-4">单次</Tag>
          </template>
          <template #cell-target_id="{ record }">
            <span class="text-zinc-600 whitespace-nowrap">{{ record.target_type === 'group' ? '群' : '好友' }} {{ record.target_id }}</span>
          </template>
          <template #cell-cron="{ record }">
            <span class="text-zinc-500 whitespace-nowrap">{{ record.cron }}</span>
          </template>
          <template #cell-next_run_at="{ record }">
            <span class="text-zinc-600 whitespace-nowrap">{{ record.enabled ? fmtTimeFull(record.next_run_at) : '—' }}</span>
          </template>
          <template #cell-last_run_at="{ record }">
            <span class="text-zinc-600 whitespace-nowrap">{{ fmtTimeFull(record.last_run_at) }}</span>
          </template>
          <template #cell-enabled="{ record }">
            <Switch
              :model-value="record.enabled"
              :disabled="toggling.has(record.id)"
              size="small"
              @update:model-value="toggleClock(record)"
              @click.stop
            />
          </template>
        </Table>
        <!-- 任务详情（原表格展开行内容；Table 无行展开 API，展开后渲染在表格下方） -->
        <template v-for="t in clocks" :key="t.id">
          <div v-if="expanded.has(t.id)" class="border-b border-dashed border-zinc-100 last:border-0 bg-zinc-50/40 px-6 py-4">
            <dl class="grid grid-cols-1 sm:grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-xs">
              <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">任务内容</dt>
              <dd class="text-zinc-700 whitespace-pre-wrap break-all">{{ t.content }}</dd>
              <template v-if="t.note">
                <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">备注</dt>
                <dd class="text-zinc-700 whitespace-pre-wrap break-all">{{ t.note }}</dd>
              </template>
              <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">超时时间</dt>
              <dd class="text-zinc-700">{{ t.timeout_sec > 0 ? t.timeout_sec + ' 秒' : '默认' }}</dd>
              <template v-if="t.target_type === 'group' && t.created_by">
                <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">提醒 @</dt>
                <dd class="text-zinc-700 font-mono">{{ t.created_by }}</dd>
              </template>
              <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">创建人</dt>
              <dd class="text-zinc-700 font-mono">{{ fmtActor(t.creator) }}</dd>
              <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">更新人</dt>
              <dd class="text-zinc-700 font-mono">{{ fmtActor(t.updater) }}</dd>
              <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">创建时间</dt>
              <dd class="text-zinc-700">{{ fmtTimeFull(t.created_at) }}</dd>
            </dl>
            <div class="mt-4 flex items-center gap-2">
              <Button type="primary" size="small" :disabled="toggling.has(t.id)" @click="openEdit(t)">编辑</Button>
              <Button type="default" size="small" danger :disabled="toggling.has(t.id)" @click="removeClock(t)">删除</Button>
            </div>
          </div>
        </template>
      </template>
    </Card>

    <!-- 筛选与操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-3">
        <h2 class="text-[10px] tracking-[0.15em] uppercase text-zinc-500 font-medium">执行日志</h2>
        <div class="flex items-center gap-1">
          <Button
            v-for="t in typeTabs"
            :key="t.value"
            :type="filters.target_type === t.value ? 'primary' : 'text'"
            size="small"
            @click="filters.target_type = t.value; applyFilters()"
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
        <Button type="text" size="small" danger :disabled="clearing" @click="clearLogs">清除日志</Button>
        <Button type="text" size="small" @click="load">刷新</Button>
      </div>
    </div>

    <!-- 条件查询栏 -->
    <Card>
      <div class="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">状态</span>
          <Select v-model="filters.status" :options="statusOptions" class="mt-1 w-full" @change="applyFilters" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">目标会话 ID</span>
          <Input
            :model-value="filters.target_id"
            placeholder="精确匹配"
            class="mt-1 w-full"
            @update:model-value="v => (filters.target_id = v.trim())"
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
            :model-value="filters.keyword"
            placeholder="匹配任务标题"
            class="mt-1 w-full"
            @update:model-value="v => (filters.keyword = v.trim())"
            @keyup.enter="applyFilters"
          />
        </label>
        <div class="flex items-end gap-2">
          <Button type="primary" size="small" @click="applyFilters">查询</Button>
          <Button v-if="hasFilter" type="default" size="small" @click="resetFilters">重置</Button>
        </div>
      </div>
    </Card>

    <!-- 日志列表（新在上，滚动到底部自动加载更早的记录） -->
    <section class="space-y-3">
      <Card v-if="logs.length === 0" style="padding: 0">
        <div class="py-12 text-sm text-slate-400 text-center">暂无符合条件的执行记录（定时任务触发后在此展示）</div>
      </Card>

      <Card v-for="log in logs" :key="log.id" hoverable @click="detail = log">
        <!-- 摘要行（点击弹出详情窗口） -->
        <div class="flex items-center gap-2 flex-wrap">
          <Tag variant="solid" size="small" :color="statusColor(log.status)">{{ statusText(log.status) }}</Tag>
          <Tag variant="solid" size="small">
            {{ log.target_type === 'group' ? '群聊' : '私聊' }} · {{ log.target_id }}
          </Tag>
          <Tag v-if="log.tool_calls?.length" variant="soft" size="small">{{ log.tool_calls.length }} 次工具调用</Tag>
          <Tag v-if="log.output_skipped" variant="solid" size="small" color="app-yellow">已跳过输出</Tag>
          <span class="ml-auto text-xs text-slate-400 font-mono whitespace-nowrap">{{ fmtTime(log.trigger_time) }}</span>
        </div>
        <p class="mt-2 text-sm text-slate-700 font-medium truncate">{{ log.task_title || '(无标题)' }}</p>
        <div class="mt-2 flex items-center gap-x-3 gap-y-1 text-[11px] text-slate-400 font-mono flex-wrap">
          <span v-if="log.status !== 'running'">用时 {{ fmtDuration(log.duration_ms) }}</span>
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
          <span v-if="log.error" class="text-[#ff3b30] truncate max-w-80">{{ log.error }}</span>
          <span class="ml-auto text-zinc-400">详情 ⤢</span>
        </div>
      </Card>
    </section>

    <!-- 滚动分页哨兵：进入视口即加载下一页 -->
    <div ref="sentinel" class="h-px" />
    <div v-if="loadingMore" class="py-3 text-xs text-slate-400 text-center">加载更早的记录…</div>
    <div v-else-if="!hasMore && logs.length" class="py-3 text-xs text-slate-300 text-center">没有更早的记录了</div>

    <!-- 详情弹窗：点遮罩 / 关闭按钮 / Esc 均可关闭 -->
    <Modal :open="!!detail" :typewriter="false" variant="default" width="768px" @close="detail = null">
      <template #title>
        <span class="flex items-center gap-2 flex-wrap">
          <Tag variant="solid" size="small" :color="statusColor(detail.status)">{{ statusText(detail.status) }}</Tag>
          <Tag variant="solid" size="small">
            {{ detail.target_type === 'group' ? '群聊' : '私聊' }} · {{ detail.target_id }}
          </Tag>
          <span class="text-sm text-zinc-800 font-medium truncate">{{ detail.task_title || '(无标题)' }}</span>
        </span>
      </template>
      <template #footer>
        <Button type="default" @click="detail = null">关闭</Button>
      </template>

      <div class="w-full space-y-4">
        <!-- 概要指标 -->
        <dl class="grid grid-cols-1 sm:grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-xs">
          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">任务 ID</dt>
          <dd class="text-zinc-700 font-mono">{{ detail.task_id || '—' }}</dd>
          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">触发时间</dt>
          <dd class="text-zinc-700 font-mono">{{ fmtTimeFull(detail.trigger_time) }}</dd>
          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">完成时间</dt>
          <dd class="text-zinc-700 font-mono">{{ fmtTimeFull(detail.finished_at) }}</dd>
          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">耗时</dt>
          <dd class="text-zinc-700 font-mono">{{ detail.status !== 'running' ? fmtDuration(detail.duration_ms) : '—' }}</dd>
          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">LLM 轮数</dt>
          <dd class="text-zinc-700 font-mono">{{ detail.iterations || '—' }}</dd>
          <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">Token 用量</dt>
          <dd class="text-zinc-700 font-mono">
            {{ detail.total_tokens ? tokenUsageText(detail) : '—' }}
          </dd>
        </dl>

        <!-- 任务内容（触发时发送给 AI 的内容） -->
        <div v-if="detail.trigger_content">
          <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium mb-2">任务内容</h3>
          <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ detail.trigger_content }}</p>
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
                <div class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 mb-1">结果</div>
                <pre class="text-xs text-slate-600 font-mono whitespace-pre-wrap break-all leading-relaxed">{{ tc.result }}</pre>
              </div>
            </div>
          </div>
        </div>

        <!-- 最终回复（AI 调用 skip_output 时未发送给目标，仅存档在此） -->
        <div v-if="detail.reply">
          <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium mb-2">
            最终回复
            <span v-if="detail.output_skipped" class="normal-case tracking-normal text-amber-600">（已跳过输出，未发送给目标）</span>
          </h3>
          <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ detail.reply }}</p>
        </div>
        <!-- AI 未输出任何文本但请求了跳过：说明原因，避免只看到空白 -->
        <div v-else-if="detail.output_skipped" class="text-xs text-amber-700 bg-amber-50 border border-amber-100 rounded-lg px-3 py-2">
          本次执行已跳过输出，未向目标发送任何消息{{ detail.skip_reason ? '：' + detail.skip_reason : '' }}
        </div>
      </div>
    </Modal>

    <!-- 新建 / 编辑定时任务弹窗 -->
    <Modal
      :open="!!clockForm"
      :typewriter="false"
      :show-footer="false"
      variant="default"
      width="512px"
      :title="clockForm?.id ? '编辑定时任务' : '新建定时任务'"
      mask-closable
      @close="clockForm = null"
    >
      <form class="w-full space-y-4" @submit.prevent="saveClock">
        <div>
          <label class="form-label">任务标题</label>
          <Input :model-value="clockForm.title" placeholder="如：每日晨报" class="w-full" @update:model-value="v => (clockForm.title = v.trim())" />
        </div>
        <div>
          <label class="form-label">任务内容 <span class="text-[#ff3b30]">*</span></label>
          <textarea v-model.trim="clockForm.content" rows="3" class="form-input" placeholder="触发时发送给 AI 的内容"></textarea>
        </div>
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="form-label">Cron 表达式 <span class="text-[#ff3b30]">*</span></label>
            <Input :model-value="clockForm.cron" placeholder="0 8 * * * 或 @every 1h" class="w-full" @update:model-value="v => (clockForm.cron = v.trim())" />
          </div>
          <div>
            <label class="form-label">超时时间（秒，0 为默认）</label>
            <Input
              :model-value="String(clockForm.timeout_sec)"
              type="number"
              class="w-full"
              @update:model-value="v => (clockForm.timeout_sec = v === '' ? '' : Number(v))"
            />
          </div>
        </div>
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="form-label">触发对象 <span class="text-[#ff3b30]">*</span></label>
            <Select v-model="clockForm.target_type" :options="targetTypeOptions" class="w-full" />
          </div>
          <div>
            <label class="form-label">{{ clockForm.target_type === 'group' ? '群 ID' : '用户 ID' }} <span class="text-[#ff3b30]">*</span></label>
            <Input :model-value="clockForm.target_id" placeholder="如 123456 或 fs:oc_xxx" class="w-full" @update:model-value="v => (clockForm.target_id = v.trim())" />
          </div>
        </div>
        <div v-if="clockForm.target_type === 'group'">
          <label class="form-label">提醒 @</label>
          <Input :model-value="clockForm.created_by" placeholder="触发时 @ 的用户 ID，如 qq:123456，留空不 @" class="w-full" @update:model-value="v => (clockForm.created_by = v.trim())" />
        </div>
        <div>
          <label class="form-label">备注</label>
          <Input :model-value="clockForm.note" placeholder="可选，触发时附带给 AI" class="w-full" @update:model-value="v => (clockForm.note = v.trim())" />
        </div>
        <label class="flex items-center gap-2 text-xs text-zinc-700 select-none cursor-pointer">
          <Switch v-model="clockForm.run_once" size="small" />
          单次任务（触发一次后自动删除）
        </label>
        <p v-if="clockFormError" class="text-xs text-[#ff3b30]">{{ clockFormError }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <Button type="default" size="small" @click="clockForm = null">取消</Button>
          <Button type="primary" size="small" html-type="submit" :disabled="clockSaving">
            {{ clockSaving ? '保存中…' : '保存' }}
          </Button>
        </div>
      </form>
    </Modal>

    <!-- Notification 命令式通知的挂载容器（App.vue 未挂载，页面内自带一份） -->
  </div>
</template>

<script setup>
import { confirmAction } from '../ui/confirm'
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import {
  Button,
  Card,
  Input,
  Modal,
  Notification,
  Select,
  Switch,
  Table,
  Tag,
} from 'animal-island-vue'
import { api } from '../api.js'

const typeTabs = [
  { value: '', label: '全部' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

// 状态筛选下拉（Select 的 modelValue 必须为字符串）
const statusOptions = [
  { key: '', label: '全部' },
  { key: 'running', label: '执行中' },
  { key: 'success', label: '成功' },
  { key: 'timeout', label: '超时' },
  { key: 'error', label: '出错' },
  { key: 'interrupted', label: '中断' },
]

// 定时任务触发对象下拉
const targetTypeOptions = [
  { key: 'group', label: '群聊' },
  { key: 'friend', label: '好友' },
]

// 定时任务表格列定义（Table 组件）
const clockColumns = [
  { title: '任务', dataIndex: 'title' },
  { title: '目标', dataIndex: 'target_id', width: '16%' },
  { title: 'Cron', dataIndex: 'cron', width: '14%' },
  { title: '下次执行', dataIndex: 'next_run_at', width: '17%' },
  { title: '上次执行', dataIndex: 'last_run_at', width: '17%' },
  { title: '启用', dataIndex: 'enabled', width: '10%' },
]

const icons = {
  chevron: '<svg fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5"/></svg>',
}

// ---- 定时任务管理 ----

const clocks = ref([])
const toggling = ref(new Set())
const expanded = ref(new Set())
const clockForm = ref(null) // 非 null 时显示弹窗；id 为空表示新建
const clockFormError = ref('')
const clockSaving = ref(false)

async function loadClocks() {
  try { clocks.value = await api.getClocks() } catch { /* 忽略 */ }
}

function toggleExpand(id) {
  const s = new Set(expanded.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  expanded.value = s
}

// Table 无行展开 API：用 onRow 把行点击挂到 <tr> 上（等价于原来的 @click）
function clockRowAttrs(record) {
  return {
    class: { 'cursor-pointer': true, 'bg-zinc-50/70': expanded.value.has(record.id) },
    onClick: () => toggleExpand(record.id),
  }
}

// 乐观更新开关状态，失败时回滚
async function toggleClock(t) {
  if (toggling.value.has(t.id)) return
  toggling.value = new Set(toggling.value).add(t.id)
  const prev = t.enabled
  t.enabled = !prev
  try {
    await api.updateClock(t.id, { enabled: t.enabled })
  } catch (e) {
    t.enabled = prev
    Notification.error({ message: e.message || '操作失败' })
  } finally {
    const s = new Set(toggling.value)
    s.delete(t.id)
    toggling.value = s
  }
}

function blankClockForm() {
  return {
    id: '',
    title: '',
    content: '',
    cron: '',
    target_type: 'group',
    target_id: '',
    timeout_sec: 0,
    note: '',
    run_once: false,
    created_by: '',
  }
}

function openCreate() {
  clockFormError.value = ''
  clockForm.value = blankClockForm()
}

function openEdit(t) {
  clockFormError.value = ''
  clockForm.value = {
    id: t.id,
    title: t.title,
    content: t.content,
    cron: t.cron,
    target_type: t.target_type,
    target_id: t.target_id,
    timeout_sec: t.timeout_sec || 0,
    note: t.note || '',
    run_once: t.run_once,
    created_by: t.created_by || '',
  }
}

async function saveClock() {
  const f = clockForm.value
  if (!f.content) { clockFormError.value = '任务内容不能为空'; return }
  if (!f.cron) { clockFormError.value = 'Cron 表达式不能为空'; return }
  if (!f.target_id) { clockFormError.value = '目标 ID 不能为空'; return }
  clockFormError.value = ''
  clockSaving.value = true
  try {
    if (f.id) {
      await api.updateClock(f.id, {
        title: f.title,
        content: f.content,
        cron: f.cron,
        note: f.note,
        timeout_sec: f.timeout_sec || 0,
        target_type: f.target_type,
        target_id: f.target_id,
        run_once: f.run_once,
        created_by: f.target_type === 'group' ? f.created_by : '',
      })
    } else {
      await api.createClock({
        title: f.title,
        content: f.content,
        cron: f.cron,
        target_type: f.target_type,
        target_id: f.target_id,
        enabled: true,
        run_once: f.run_once,
        timeout_sec: f.timeout_sec || 0,
        note: f.note,
        created_by: f.target_type === 'group' ? f.created_by : '',
      })
    }
    clockForm.value = null
    await loadClocks()
  } catch (e) {
    clockFormError.value = e.message || '保存失败'
  } finally {
    clockSaving.value = false
  }
}

async function removeClock(t) {
  if (!await confirmAction(`确定删除定时任务「${t.title || t.id}」吗？`)) return
  toggling.value = new Set(toggling.value).add(t.id)
  try {
    await api.deleteClock(t.id)
    await loadClocks()
  } catch (e) {
    Notification.error({ message: e.message || '删除失败' })
  } finally {
    const s = new Set(toggling.value)
    s.delete(t.id)
    toggling.value = s
  }
}

const inputClass = 'mt-1 w-full border border-zinc-300 rounded-md px-2.5 py-1.5 text-xs focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30 focus:border-[#0071e3]/50 transition-shadow bg-white'

// filters 为编辑中的条件，applied 为实际生效（已点查询/切换类型）的条件，
// 自动刷新沿用 applied，避免输入到一半被轮询带出去
const emptyFilters = () => ({ target_type: '', target_id: '', status: '', start: '', end: '', keyword: '' })
const filters = reactive(emptyFilters())
const applied = reactive(emptyFilters())

const PAGE = 50 // 每页条数

const logs = ref([]) // 新在前
const autoRefresh = ref(true)
const exporting = ref(false)
const clearing = ref(false)
// detail 为当前弹窗展示的日志（null 表示弹窗关闭）
const detail = ref(null)
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

// 按当前筛选条件导出执行日志（后端返回 JSON 附件）
async function exportLogs() {
  if (exporting.value) return
  exporting.value = true
  try {
    await api.exportTaskLogs({ ...applied })
  } catch (e) {
    Notification.error({ message: e.message || '导出失败' })
  } finally {
    exporting.value = false
  }
}

// 清除当前筛选条件下的执行日志（无筛选时清空全部），不可恢复
async function clearLogs() {
  if (clearing.value) return
  const scope = hasFilter.value ? '当前筛选条件下的' : '全部'
  if (!await confirmAction(`确定清除${scope}任务执行日志吗？清除后不可恢复，建议先导出备份。`)) return
  clearing.value = true
  try {
    const res = await api.clearTaskLogs({ ...applied })
    resetList()
    await load()
    Notification.success({ message: `已清除 ${res.removed ?? 0} 条日志` })
  } catch (e) {
    Notification.error({ message: e.message || '清除失败' })
  } finally {
    clearing.value = false
  }
}

// Esc 关闭详情弹窗 / 任务表单弹窗
function onKeydown(e) {
  if (e.key === 'Escape') {
    detail.value = null
    clockForm.value = null
  }
}

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d)) return '-'
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false })
  if (d.toDateString() === new Date().toDateString()) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function fmtTimeFull(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d) || d.getFullYear() < 2000) return '—' // Go 零值时间
  return d.toLocaleString('zh-CN', { hour12: false })
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

// 详情弹窗里的单行 token 摘要
function tokenUsageText(e) {
  const parts = [`总计 ${fmtInt(e.total_tokens)}`, `输入 ${fmtInt(e.prompt_tokens)}`, `输出 ${fmtInt(e.completion_tokens)}`]
  if (e.cached_tokens) {
    parts.push(`缓存命中 ${fmtInt(e.cached_tokens)}` + (cacheRate(e) > 0 ? `（命中率 ${cachePct(e)}）` : ''))
  }
  return parts.join(' · ')
}

// 操作人标识人性化：ai / panel 是固定标识，其余为用户 ID 原样展示
function fmtActor(s) {
  if (!s) return '—'
  if (s === 'ai') return 'AI'
  if (s === 'panel') return '面板'
  return s
}

function statusText(s) {
  return { running: '执行中', success: '成功', timeout: '超时', error: '出错', interrupted: '中断' }[s] || s
}

// 状态 → Tag 语义色（success 绿 / interrupted 橙 / error 红 / running 蓝 / 其余默认）
function statusColor(s) {
  return {
    running: 'app-blue',
    success: 'app-green',
    timeout: 'default',
    error: 'app-red',
    interrupted: 'app-orange',
  }[s] || 'default'
}

// 刷新：拉取最新一页，新条目插入头部、已有条目原地更新（执行中 → 完成），
// 已加载的更早分页保留
async function load() {
  let page
  try { page = await api.getTaskLogs({ ...applied, limit: PAGE }) } catch { return }
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
    const page = await api.getTaskLogs({ ...applied, limit: PAGE, before })
    const known = new Set(logs.value.map((l) => l.id))
    const items = (page.items || []).filter((it) => !known.has(it.id))
    loadedOlder.value = true
    hasMore.value = page.has_more && items.length > 0
    logs.value = [...logs.value, ...items]
  } catch { /* 忽略，下次滚动重试 */ } finally {
    loadingMore.value = false
  }
}

// 实时刷新：日志按自动刷新开关轮询，任务列表始终轮询；标签页隐藏时暂停，恢复可见时立即刷新
function onVisible() {
  if (document.hidden) return
  if (autoRefresh.value) load()
  loadClocks()
}

onMounted(() => {
  load()
  loadClocks()
  timer = setInterval(() => {
    if (document.hidden) return
    if (autoRefresh.value) load()
    loadClocks()
  }, 4000)
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
  observer?.disconnect()
  document.removeEventListener('visibilitychange', onVisible)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<style scoped>
.form-label {
  display: block;
  font-size: 10px;
  letter-spacing: 0.15em;
  text-transform: uppercase;
  color: rgb(113 113 122);
  margin-bottom: 0.375rem;
}
.form-input {
  width: 100%;
  border: 1px solid rgb(212 212 216);
  border-radius: 0.375rem;
  padding: 0.5rem 0.75rem;
  font-size: 0.75rem;
  color: rgb(39 39 42);
  outline: none;
  transition: border-color 0.15s;
  background: white;
}
.form-input:focus {
  border-color: rgb(113 113 122);
}
.form-input:disabled {
  background: rgb(244 244 245);
  color: rgb(161 161 170);
}
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
