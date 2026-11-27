<template>
  <div class="space-y-4">
    <!-- 说明与全局操作 -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <p class="text-xs text-slate-500 max-w-3xl leading-relaxed">
        按会话查看 AI 实际持有的完整上下文：<b class="text-slate-600">系统提示词</b>（静态提示词 + 技能表 + 场景描述）、
        <b class="text-slate-600">消息历史</b>（含工具调用与工具结果）与<b class="text-slate-600">工具定义</b>。
        记忆 / 知识库 / 待办提醒等每轮注入只作用于当轮请求、不写入历史，因此不在快照内；试聊室会话在「试聊室」页单独管理。
      </p>
      <div class="flex items-center gap-2 shrink-0 flex-wrap">
        <label class="flex items-center gap-1.5 text-[11px] text-slate-500 cursor-pointer select-none">
          <Switch v-model="autoRefresh" size="small" />
          自动刷新列表（10s）
        </label>
        <Button type="text" size="small" :disabled="loading" @click="loadSessions()">刷新</Button>
        <Button type="text" size="small" :disabled="!sessions.length || exporting" :loading="exporting" @click="exportKeys([])">
          导出全部
        </Button>
        <Button type="default" size="small" danger :disabled="!sessions.length || clearing" :loading="clearing" @click="clearAll">
          清空全部上下文
        </Button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs whitespace-pre-wrap" :class="msgOk ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ msg }}</p>

    <div class="flex flex-col lg:flex-row lg:items-start gap-4">
      <!-- 左栏：会话列表 -->
      <Card style="padding: 0" class="w-full lg:w-80 shrink-0 overflow-hidden">
        <div class="p-2 border-b border-white/50 space-y-2">
          <Input v-model="keyword" size="small" placeholder="搜索群名 / 会话 ID" />
          <div class="filter-group">
            <Button
              v-for="t in kindTabs"
              :key="t.value"
              size="small"
              class="flex-1"
              :type="kindFilter === t.value ? 'primary' : 'text'"
              @click="kindFilter = t.value"
            >
              {{ t.label }}
            </Button>
          </div>
        </div>
        <!-- 多选操作栏 -->
        <div
          v-if="selectedCount > 0"
          class="px-3 py-2 border-b border-white/50 bg-[#0071e3]/8 flex items-center justify-between gap-2 flex-wrap"
        >
          <span class="text-[11px] text-slate-600">已选 <b>{{ selectedCount }}</b> 个会话</span>
          <div class="flex items-center gap-1">
            <Button type="text" size="small" :disabled="exporting" @click="exportKeys([...selected])">导出</Button>
            <Button type="default" size="small" danger :disabled="clearing" @click="clearKeys([...selected])">清空</Button>
            <Button type="text" size="small" @click="clearSelection">取消</Button>
          </div>
        </div>

        <ul class="divide-y divide-slate-100 max-h-[36rem] overflow-y-auto">
          <li v-if="loading && !sessions.length" class="list-none">
            <LoadingState text="正在加载会话列表…" :rows="2" compact />
          </li>
          <li v-else-if="filteredSessions.length === 0" class="py-10 px-4 text-xs text-slate-400 text-center list-none">
            {{ sessions.length ? '没有匹配的会话' : '暂无会话上下文。AI 产生对话后会自动出现在这里' }}
          </li>
          <li
            v-for="s in filteredSessions"
            :key="s.key"
            class="px-3 py-2.5 cursor-pointer transition-colors"
            :class="currentKey === s.key ? 'bg-[#0071e3]/12' : 'hover:bg-white/55'"
            @click="select(s.key)"
          >
            <div class="flex items-start gap-2">
              <input
                type="checkbox"
                class="mt-1 shrink-0 cursor-pointer"
                :checked="selected.has(s.key)"
                :aria-label="'选择会话 ' + s.key"
                title="选中以批量导出 / 清空"
                @click.stop="toggleSelect(s.key)"
              />
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-1.5">
                  <span class="text-[13px] font-medium text-slate-800 truncate">{{ displayName(s) }}</span>
                  <Tag v-if="s.busy" variant="solid" size="small" color="app-red" class="shrink-0">执行中</Tag>
                  <Tag v-else-if="!s.resident" variant="soft" size="small" color="app-orange" class="shrink-0">未驻留</Tag>
                </div>
                <div class="text-[11px] font-mono text-slate-400 mt-0.5 truncate" :title="s.key">{{ s.key }}</div>
                <div class="mt-1 flex items-center gap-2 flex-wrap text-[10px] text-slate-400">
                  <span>{{ s.message_count }} 条消息</span>
                  <span v-if="s.pending">· {{ s.pending }} 条排队</span>
                  <span v-if="s.subagents">· {{ s.subagents }} 个子代理</span>
                  <span v-if="s.plan_mode">· 计划模式</span>
                  <span class="ml-auto">{{ fmtTime(s.last_active || s.updated_at) }}</span>
                </div>
                <div v-if="s.busy && s.stage" class="mt-0.5 text-[10px] text-[#ff3b30] truncate">{{ s.stage }}</div>
              </div>
            </div>
          </li>
        </ul>
      </Card>

      <!-- 右栏：完整上下文 -->
      <Card v-if="detail" style="padding: 0" class="flex-1 min-w-0 overflow-hidden">
        <!-- 头部 -->
        <div class="px-5 py-3.5 border-b border-white/50 flex items-start justify-between gap-3 flex-wrap">
          <div class="min-w-0">
            <div class="flex items-center gap-1.5 flex-wrap">
              <span class="text-sm font-semibold text-slate-800">{{ displayName(detail) }}</span>
              <Tag variant="soft" size="small">{{ detail.kind === 'group' ? '群聊' : '私聊' }}</Tag>
              <Tag variant="solid" size="small" :color="detail.source === 'live' ? 'app-green' : 'app-orange'">
                {{ detail.source === 'live' ? '实时上下文' : '持久化快照' }}
              </Tag>
              <Tag v-if="detail.busy" variant="solid" size="small" color="app-red">执行中</Tag>
              <Tag v-if="detail.plan_mode" variant="soft" size="small" color="app-teal">计划模式</Tag>
            </div>
            <div class="text-[11px] font-mono text-slate-400 mt-1 break-all">{{ detail.key }}</div>
          </div>
          <div class="flex items-center gap-1 shrink-0 flex-wrap">
            <Button type="text" size="small" :loading="detailLoading" @click="loadDetail(detail.key)">刷新</Button>
            <Button type="text" size="small" :disabled="exporting" @click="exportKeys([detail.key])">导出此会话</Button>
            <Button type="text" size="small" danger :disabled="clearing" @click="clearKeys([detail.key])">清空上下文</Button>
            <Button type="default" size="small" danger :disabled="clearing" @click="deleteKey(detail.key)">删除会话</Button>
          </div>
        </div>

        <!-- 数据来源说明 -->
        <p v-if="detail.hint" class="px-5 py-2 text-[11px] leading-relaxed text-amber-800 bg-[#ff9500]/10 border-b border-white/50">
          {{ detail.hint }}
        </p>

        <!-- 统计 -->
        <div class="px-5 py-4 border-b border-white/50">
          <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
            <div v-for="stat in stats" :key="stat.label">
              <span class="tlabel">{{ stat.label }}</span>
              <div class="mt-1 text-sm font-semibold text-slate-800">{{ stat.value }}</div>
              <div class="text-[10px] text-slate-400">{{ stat.sub }}</div>
            </div>
          </div>
          <div v-if="detail.max_context_tokens > 0" class="mt-4">
            <div class="flex justify-between text-[10px] tracking-[0.12em] uppercase text-slate-400 mb-1">
              <span>最近一次请求 prompt {{ fmtNum(detail.last_prompt_tokens) }} / 上限 {{ fmtNum(detail.max_context_tokens) }}</span>
              <span>{{ promptPercent }}%</span>
            </div>
            <Progress :percent="promptPercent" :show-info="false" size="small" aria-label="上下文占用" />
          </div>
          <div class="mt-3 flex items-center gap-1.5 flex-wrap text-[10px] text-slate-400">
            <span v-if="detail.session_token">会话 token <code class="font-mono text-slate-500">{{ detail.session_token }}</code></span>
            <span v-if="detail.created_at">· 创建 {{ fmtTimeFull(detail.created_at) }}</span>
            <span v-if="detail.updated_at">· 更新 {{ fmtTimeFull(detail.updated_at) }}</span>
          </div>
          <p class="mt-2 text-[10px] text-slate-400 leading-relaxed">
            请求组装顺序：system（静态提示词 → 技能表 → 场景描述）+ 历史消息 + 当前用户消息；历史里 tool 消息紧跟对应的 assistant 工具调用。
            每轮注入（记忆 / 知识库 / 待办提醒 / 计划模式附言）拼在当轮用户消息之前、不写入历史，因此不在快照中。
          </p>
        </div>

        <!-- 任务清单（每轮以 todo_reminder 注入） -->
        <div v-if="todoItems.length" class="px-5 py-3 border-b border-white/50">
          <div class="text-[11px] font-semibold text-slate-600 mb-1.5">
            任务清单 <span class="font-normal text-slate-400">（每轮以 todo_reminder 尾部注入上下文）</span>
          </div>
          <ul class="space-y-1">
            <li v-for="(t, i) in todoItems" :key="i" class="text-xs text-slate-700 flex items-start gap-1.5">
              <Tag variant="soft" size="small" :color="todoColor(t.status)" class="shrink-0">{{ todoLabel(t.status) }}</Tag>
              <span class="break-words">{{ t.content }}<span v-if="t.active_form" class="text-slate-400"> · {{ t.active_form }}</span></span>
            </li>
          </ul>
        </div>

        <!-- 系统提示词 -->
        <div class="border-b border-white/50">
          <div class="px-5 pt-3 pb-1 flex items-center justify-between gap-2">
            <span class="tlabel">System · 系统提示词（按真正下发的顺序）</span>
            <span class="text-[10px] text-slate-400 font-mono">{{ systemSections.length }} 段 · ~{{ fmtNum(detail.system_tokens) }} token</span>
          </div>
          <div v-if="!systemSections.length" class="px-5 pb-3 text-xs text-slate-400">（空）</div>
          <details v-for="s in systemSections" :key="s.key" class="ctx-block">
            <summary>
              <span class="text-xs font-semibold text-slate-700">{{ s.title }}</span>
              <span class="text-[10px] font-mono text-slate-400">{{ fmtNum(s.chars) }} 字 · ~{{ fmtNum(s.est_tokens) }} token</span>
            </summary>
            <div class="px-5 pb-3">
              <pre class="ctx-pre">{{ s.content }}</pre>
              <div class="flex justify-end">
                <Button type="text" size="small" @click="copyText(s.content, s.title)">复制</Button>
              </div>
            </div>
          </details>
        </div>

        <!-- 工具定义 -->
        <div class="border-b border-white/50">
          <div class="px-5 pt-3 pb-1 flex items-center justify-between gap-2">
            <span class="tlabel">Tools · 工具定义（实际下发给模型）</span>
            <span class="text-[10px] text-slate-400 font-mono">{{ contextTools.length }} 个 · ~{{ fmtNum(detail.tools_tokens) }} token</span>
          </div>
          <div v-if="!contextTools.length" class="px-5 pb-3 text-xs text-slate-400">
            会话未驻留内存时读不到工具定义；会话产生一条消息后即可重建并读取
          </div>
          <div class="px-5 pb-3 flex flex-wrap gap-1">
            <Tag v-for="t in contextTools" :key="t.name" variant="soft" size="small" :title="`~${t.est_tokens} token`">
              {{ t.name }}
            </Tag>
          </div>
          <details v-if="contextTools.length" class="ctx-block">
            <summary>
              <span class="text-xs font-semibold text-slate-700">查看工具说明与参数 schema</span>
              <span class="text-[10px] font-mono text-slate-400">{{ fmtNum(detail.tools_chars) }} 字</span>
            </summary>
            <div class="px-5 pb-3 space-y-2">
              <details v-for="t in contextTools" :key="t.name" class="ctx-tool">
                <summary>
                  <span class="font-mono text-[11px] text-slate-700">{{ t.name }}</span>
                  <span class="text-[10px] text-slate-400">~{{ fmtNum(t.est_tokens) }} token</span>
                </summary>
                <div class="pl-3 pt-1 space-y-1">
                  <p class="text-[11px] text-slate-600 whitespace-pre-wrap break-words">{{ t.description }}</p>
                  <pre class="ctx-pre text-[10px]">{{ prettyJSON(t.parameters) }}</pre>
                </div>
              </details>
            </div>
          </details>
        </div>

        <!-- 消息历史 -->
        <div>
          <div class="px-5 py-3 flex items-center justify-between gap-2">
            <span class="tlabel">History · 消息历史（{{ contextMessages.length }} 条）</span>
            <span class="text-[10px] text-slate-400 font-mono">{{ fmtNum(detail.history_chars) }} 字 · ~{{ fmtNum(detail.history_tokens) }} token</span>
          </div>
          <div v-if="!contextMessages.length" class="px-5 pb-6 text-xs text-slate-400">
            该会话还没有历史消息（或被压缩成一条摘要）
          </div>
          <ul class="divide-y divide-slate-100">
            <li v-for="(m, i) in contextMessages" :key="i" class="ctx-msg">
              <div class="flex items-center gap-1.5 flex-wrap">
                <span class="ctx-role" :data-role="m.role">{{ roleLabel(m.role) }}</span>
                <span class="text-[10px] font-mono text-slate-400">#{{ i + 1 }} · {{ fmtNum(m.chars) }} 字 · ~{{ fmtNum(m.est_tokens) }} token</span>
                <Tag v-if="m.tool_name" variant="soft" size="small" color="app-blue">{{ m.tool_name }}</Tag>
                <Tag v-if="m.tool_call_id && !m.tool_name" variant="soft" size="small">{{ m.tool_call_id }}</Tag>
                <Tag v-if="m.images" variant="soft" size="small" color="app-pink">{{ m.images }} 张图片</Tag>
              </div>
              <!-- assistant 的工具调用 -->
              <div v-if="m.tool_calls && m.tool_calls.length" class="mt-1.5 space-y-1">
                <details v-for="(tc, j) in m.tool_calls" :key="j" class="ctx-tool">
                  <summary>
                    <span class="text-[11px] text-slate-600">工具调用</span>
                    <span class="font-mono text-[11px] text-slate-700">{{ tc.name }}</span>
                  </summary>
                  <pre class="ctx-pre text-[10px]">{{ prettyJSON(tc.arguments) }}</pre>
                </details>
              </div>
              <pre v-if="m.text" class="ctx-text">{{ m.text }}</pre>
              <details v-if="m.reasoning" class="ctx-tool">
                <summary><span class="text-[11px] text-slate-600">推理内容</span></summary>
                <pre class="ctx-pre text-[10px]">{{ m.reasoning }}</pre>
              </details>
            </li>
          </ul>
        </div>
      </Card>

      <Card v-else class="flex-1">
        <div v-if="detailLoading" class="py-10">
          <LoadingState text="正在读取会话上下文…" :rows="2" />
        </div>
        <div v-else class="py-12 text-sm text-slate-400 text-center">
          从左侧选择一个会话，查看它完整的 AI 上下文
        </div>
      </Card>
    </div>
  </div>
</template>

<script setup>
import { confirmAction } from '../ui/confirm'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Button, Card, Input, Notification, Progress, Switch, Tag } from 'animal-island-vue'
import { api } from '../api.js'
import LoadingState from '../components/LoadingState.vue'

const kindTabs = [
  { value: 'all', label: '全部' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const sessions = ref([])
const detail = ref(null)
const loading = ref(true)
const detailLoading = ref(false)
const keyword = ref('')
const kindFilter = ref('all')
const autoRefresh = ref(false)
const clearing = ref(false)
const exporting = ref(false)
const msg = ref('')
const msgOk = ref(false)
const names = ref({})
// 多选只在当前列表内有效，列表刷新后剔除已消失的 ID
const selected = ref(new Set())

const currentKey = computed(() => detail.value?.key || '')

// 后端三个数组字段一律返回空数组，这里再做一次兜底：历史版本 / 异常响应返回
// null 时页面不至于因为读 .length 直接渲染失败（曾经导致非驻留会话打不开）。
const systemSections = computed(() => detail.value?.system || [])
const contextTools = computed(() => detail.value?.tools || [])
const contextMessages = computed(() => detail.value?.messages || [])
const todoItems = computed(() => detail.value?.todo || [])

const filteredSessions = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return sessions.value.filter((s) => {
    if (kindFilter.value !== 'all' && s.kind !== kindFilter.value) return false
    if (!kw) return true
    return (
      s.key.toLowerCase().includes(kw) ||
      String(s.target || '').toLowerCase().includes(kw) ||
      String(names.value[s.key] || '').toLowerCase().includes(kw)
    )
  })
})

const selectedCount = computed(() => sessions.value.filter((s) => selected.value.has(s.key)).length)
const busyKeys = computed(() => new Set(sessions.value.filter((s) => s.busy).map((s) => s.key)))

const stats = computed(() => {
  const d = detail.value
  if (!d) return []
  return [
    { label: 'System · 系统提示词', value: `${systemSections.value.length} 段`, sub: `${fmtNum(d.system_chars)} 字 · ~${fmtNum(d.system_tokens)} token` },
    { label: 'History · 消息历史', value: `${contextMessages.value.length} 条`, sub: `${fmtNum(d.history_chars)} 字 · ~${fmtNum(d.history_tokens)} token` },
    { label: 'Tools · 工具定义', value: `${contextTools.value.length} 个`, sub: `${fmtNum(d.tools_chars)} 字 · ~${fmtNum(d.tools_tokens)} token` },
    { label: '合计估算', value: `~${fmtNum(d.total_tokens)} token`, sub: `${fmtNum(d.total_chars)} 字（约 2 字符折 1 token）` },
  ]
})

const promptPercent = computed(() => {
  const d = detail.value
  if (!d || !d.max_context_tokens) return 0
  return Math.min(100, Math.round((d.last_prompt_tokens / d.max_context_tokens) * 100))
})

// ---------- 展示辅助 ----------

function displayName(s) {
  if (!s) return ''
  const named = names.value[s.key]
  if (named) return named
  return s.kind === 'group' ? `群 ${s.target}` : `私聊 ${s.target}`
}

function fmtNum(n) {
  const v = Number(n) || 0
  if (v >= 1000000) return `${(v / 1000000).toFixed(1)}M`
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`
  return String(v)
}

function fmtTime(sec) {
  if (!sec) return ''
  const d = new Date(sec * 1000)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return ''
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' })
  if (d.toDateString() === new Date().toDateString()) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function fmtTimeFull(sec) {
  if (!sec) return '—'
  const d = new Date(sec * 1000)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—'
  return d.toLocaleString('zh-CN', { hour12: false })
}

const roleLabels = { system: 'SYSTEM', user: 'USER', assistant: 'ASSISTANT', tool: 'TOOL', developer: 'DEV' }

function roleLabel(role) {
  return roleLabels[role] || String(role || '').toUpperCase()
}

function todoColor(status) {
  if (status === 'completed') return 'app-green'
  if (status === 'in_progress') return 'app-blue'
  return 'app-yellow'
}

function todoLabel(status) {
  if (status === 'completed') return '已完成'
  if (status === 'in_progress') return '进行中'
  return '待办'
}

// 工具参数里的 arguments 是模型给的 JSON 字符串，能解析就格式化，否则原样展示
function prettyJSON(text) {
  if (!text) return ''
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

async function copyText(text, label = '内容') {
  try {
    await navigator.clipboard.writeText(text || '')
    msgOk.value = true
    msg.value = `已复制${label}`
  } catch {
    Notification.error({ message: '复制失败，请手动选择文本复制' })
  }
}

// ---------- 数据加载 ----------

async function loadNames() {
  // 群/好友名称映射（静默失败：回退显示会话 ID）
  names.value = await api.getContactNameMap()
}

async function loadSessions(silent = false) {
  if (!silent) loading.value = true
  try {
    const data = await api.getContexts()
    sessions.value = data.items || []
    pruneSelection()
    if (!silent) {
      if (detail.value && !sessions.value.some((s) => s.key === detail.value.key)) {
        detail.value = null
      }
      if (!detail.value && sessions.value.length) {
        await loadDetail(sessions.value[0].key, true)
      }
    }
  } catch (e) {
    if (!silent) {
      msgOk.value = false
      msg.value = e.message
    }
  } finally {
    loading.value = false
  }
}

async function loadDetail(key, silent = false) {
  if (!key) return
  if (!silent) detailLoading.value = true
  try {
    const data = await api.getContextDetail(key)
    // 接口异常时宁可报错，也不要把不完整对象塞进详情（缺字段会让整块渲染失败）
    if (!data || !data.key) throw new Error('接口返回的上下文数据不完整，请刷新重试')
    detail.value = data
    msg.value = ''
  } catch (e) {
    // 会话已被删除 / 清空：回到列表空态，不把接口错误留在页面上
    if (String(e.message || '').includes('会话不存在')) {
      detail.value = null
      await loadSessions(true)
      return
    }
    msgOk.value = false
    msg.value = e.message
  } finally {
    detailLoading.value = false
  }
}

async function select(key) {
  if (key === detail.value?.key) return
  await loadDetail(key)
}

async function refreshAll() {
  await loadSessions()
  if (detail.value) await loadDetail(detail.value.key, true)
}

// ---------- 多选 ----------

function toggleSelect(key) {
  const next = new Set(selected.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  selected.value = next
}

function clearSelection() {
  selected.value = new Set()
}

function pruneSelection() {
  const keys = new Set(sessions.value.map((s) => s.key))
  const next = new Set([...selected.value].filter((k) => keys.has(k)))
  if (next.size !== selected.value.size) selected.value = next
}

// ---------- 导出 ----------

async function exportKeys(keys) {
  if (exporting.value) return
  exporting.value = true
  msg.value = ''
  try {
    await api.exportContexts(keys || [])
    msgOk.value = true
    msg.value = keys && keys.length ? `已导出 ${keys.length} 个会话的完整上下文` : '已导出全部会话的完整上下文'
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  } finally {
    exporting.value = false
  }
}

// ---------- 清空 / 删除 ----------

// reportClear 汇总清空结果（成功数 / 终止的请求数 / 失败明细）
function reportClear(res, verb = '清空') {
  const failed = res.failed || []
  const parts = [`已${verb} ${res.cleared || 0} 个会话的上下文`]
  if (res.terminated) parts.push(`终止 ${res.terminated} 个执行中的请求`)
  if (failed.length) parts.push(`${failed.length} 个失败：` + failed.map((f) => `${f.key}（${f.error || '未知原因'}）`).join('；'))
  msgOk.value = failed.length === 0
  msg.value = parts.join('，')
}

// busyNotice 生成「正在执行」提示，让用户清楚清空会先终止请求
function busyNotice(keys) {
  const busy = keys.filter((k) => busyKeys.value.has(k))
  if (!busy.length) return ''
  const head = busy.slice(0, 3).join('、')
  const more = busy.length > 3 ? ` 等 ${busy.length} 个会话` : ''
  return `${head}${more}正在执行请求，操作会先终止请求（用户会收到「AI 响应已被停止」），然后立即生效。`
}

async function clearKeys(keys) {
  const list = [...new Set(keys)].filter(Boolean)
  if (!list.length || clearing.value) return
  const target = list.length === 1 ? `会话「${displayName(sessions.value.find((s) => s.key === list[0]))}」` : `${list.length} 个会话`
  const notice = busyNotice(list)
  const ok = await confirmAction({
    title: '清空上下文',
    message: `${notice}\n\n确定清空${target}的上下文吗？AI 将忘记此前的全部对话（含工具调用记录），且不可恢复。`,
    confirmText: '清空',
  })
  if (!ok) return
  clearing.value = true
  msg.value = ''
  try {
    let res = await api.clearContexts(list, false, Boolean(notice))
    if (!res.ok && res.failed?.length) {
      // 调用期间又有会话进入执行状态：二次确认后终止并清空
      const force = await confirmAction({
        title: '终止并清空',
        message: `${res.failed.length} 个会话仍在执行请求，是否终止后清空？`,
        confirmText: '终止并清空',
      })
      if (!force) {
        reportClear(res)
        return
      }
      const retry = await api.clearContexts(res.failed.map((f) => f.key), false, true)
      res = { ...retry, cleared: (res.cleared || 0) + (retry.cleared || 0), terminated: (res.terminated || 0) + (retry.terminated || 0) }
    }
    reportClear(res)
    await reloadAfterMutation()
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  } finally {
    clearing.value = false
  }
}

async function clearAll() {
  if (clearing.value || !sessions.value.length) return
  const notice = busyNotice(sessions.value.map((s) => s.key))
  const ok = await confirmAction({
    title: '清空全部上下文',
    message: `${notice}\n\n确定清空全部 ${sessions.value.length} 个会话的上下文吗？所有 AI 对话记忆都会被删除，且不可恢复。`,
    confirmText: '全部清空',
  })
  if (!ok) return
  clearing.value = true
  msg.value = ''
  try {
    let res = await api.clearContexts([], true, Boolean(notice))
    if (!res.ok && res.failed?.length) {
      const force = await confirmAction({
        title: '终止并清空',
        message: `${res.failed.length} 个会话仍在执行请求，是否终止后全部清空？`,
        confirmText: '终止并清空',
      })
      if (!force) {
        reportClear(res)
        return
      }
      const retry = await api.clearContexts([], true, true)
      res = { ...retry, cleared: (res.cleared || 0) + (retry.cleared || 0), terminated: (res.terminated || 0) + (retry.terminated || 0) }
    }
    reportClear(res)
    await reloadAfterMutation()
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  } finally {
    clearing.value = false
  }
}

async function deleteKey(key) {
  if (!key || clearing.value) return
  const busy = busyKeys.value.has(key)
  const ok = await confirmAction({
    title: '删除会话',
    message:
      `${busy ? '该会话正在执行请求，删除会先终止请求（用户会收到「AI 响应已被停止」）。\n\n' : ''}` +
      `确定删除会话「${displayName(sessions.value.find((s) => s.key === key))}」吗？\n\n` +
      '删除会清空该会话的完整上下文，并移除长任务目标、任务清单、计划模式等会话级状态。「AI 运行记录 → 后台任务」里的历史记录会保留，且不可恢复。',
    confirmText: '删除',
  })
  if (!ok) return
  clearing.value = true
  msg.value = ''
  try {
    await api.deleteContext(key, busy)
    msgOk.value = true
    msg.value = `已删除会话 ${key}`
    await reloadAfterMutation()
  } catch (e) {
    // 409：调用期间会话进入执行状态，确认后终止并删除
    if (String(e.message || '').includes('正在执行请求')) {
      const force = await confirmAction({
        title: '终止并删除',
        message: '该会话仍在执行请求，是否终止后删除？',
        confirmText: '终止并删除',
      })
      if (force) {
        try {
          await api.deleteContext(key, true)
          msgOk.value = true
          msg.value = `已终止请求并删除会话 ${key}`
          await reloadAfterMutation()
          return
        } catch (err) {
          msgOk.value = false
          msg.value = err.message
          return
        }
      }
    }
    msgOk.value = false
    msg.value = e.message
  } finally {
    clearing.value = false
  }
}

// reloadAfterMutation 清空 / 删除后同步列表与详情：会话消失时自动选中下一个
async function reloadAfterMutation() {
  const keep = detail.value?.key
  await loadSessions(true)
  if (keep && sessions.value.some((s) => s.key === keep)) {
    await loadDetail(keep, true)
  } else if (sessions.value.length) {
    await loadDetail(sessions.value[0].key, true)
  } else {
    detail.value = null
  }
  clearSelection()
}

// ---------- 自动刷新 ----------

let timer = null

function stopTimer() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

watch(autoRefresh, (on) => {
  stopTimer()
  if (on) timer = setInterval(() => loadSessions(true), 10000)
})

onBeforeUnmount(stopTimer)

onMounted(async () => {
  await loadSessions()
  loadNames()
})
</script>

<style scoped>
/* 折叠块（系统提示词 / 工具说明）：用原生 details，默认收起、展开不占额外状态 */
.ctx-block {
  border-top: 1px solid rgb(226 232 240 / 0.7);
}

.ctx-block > summary,
.ctx-tool > summary {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 20px;
  cursor: pointer;
  list-style: none;
  user-select: none;
}

.ctx-block > summary::-webkit-details-marker,
.ctx-tool > summary::-webkit-details-marker {
  display: none;
}

.ctx-block > summary::before,
.ctx-tool > summary::before {
  content: '▸';
  flex-shrink: 0;
  font-size: 10px;
  color: var(--ink-muted);
  transition: transform 0.15s ease;
}

.ctx-block[open] > summary::before,
.ctx-tool[open] > summary::before {
  transform: rotate(90deg);
}

.ctx-block > summary:hover,
.ctx-tool > summary:hover {
  background: rgb(255 255 255 / 0.5);
}

.ctx-tool {
  border-radius: 8px;
  background: rgb(255 255 255 / 0.45);
  border: 1px solid rgb(226 232 240 / 0.8);
  overflow: hidden;
}

.ctx-tool > summary {
  padding: 5px 8px;
  gap: 6px;
}

/* 长文本块：可滚动，避免一条超长工具结果把页面撑到几万像素 */
.ctx-pre,
.ctx-text {
  margin: 0;
  padding: 8px 10px;
  border-radius: 8px;
  background: rgb(255 255 255 / 0.6);
  border: 1px solid rgb(226 232 240 / 0.8);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  line-height: 1.55;
  color: var(--ink-soft, #3f3a33);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 22rem;
  overflow: auto;
}

.ctx-text {
  margin-top: 6px;
  font-family: inherit;
  font-size: 12.5px;
}

.ctx-msg {
  padding: 10px 20px;
}

.ctx-role {
  display: inline-flex;
  align-items: center;
  padding: 1px 7px;
  border-radius: 999px;
  font-size: 9.5px;
  font-weight: 700;
  letter-spacing: 0.06em;
  background: rgb(148 163 184 / 0.16);
  color: #475569;
}

.ctx-role[data-role='user'] {
  background: rgb(0 113 227 / 0.12);
  color: #0059b3;
}

.ctx-role[data-role='assistant'] {
  background: rgb(52 199 89 / 0.14);
  color: #1d7a3a;
}

.ctx-role[data-role='tool'] {
  background: rgb(255 149 0 / 0.16);
  color: #8a5200;
}

.ctx-role[data-role='system'] {
  background: rgb(88 86 214 / 0.14);
  color: #3b3a9e;
}
</style>
