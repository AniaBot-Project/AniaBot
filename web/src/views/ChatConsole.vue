<template>
  <div class="chatconsole-shell" :class="{ 'has-active': !!activeId }">
    <!-- 左侧：试聊会话列表 -->
    <aside class="sess-pane">
      <div class="sess-head">
        <div class="sess-heading">
          <span class="sess-heading-title">试聊会话</span>
          <span class="sess-heading-count">{{ sessions.length }}</span>
        </div>
        <Button type="primary" size="small" class="w-full" @click="openCreate">新建会话</Button>
      </div>

      <div class="sess-scroll">
        <button
          v-for="s in sessions"
          :key="s.id"
          type="button"
          class="sess-item"
          :class="{ active: s.id === activeId }"
          @click="selectSession(s.id, { focus: true })"
        >
          <span class="sess-avatar" :class="'kind-' + s.target_kind">{{ kindGlyph(s) }}</span>
          <span class="sess-main">
            <span class="sess-row">
              <span class="sess-name" :title="s.title">{{ s.title }}</span>
              <span class="sess-time">{{ relTime(s.updated_at) }}</span>
            </span>
            <span class="sess-row">
              <span class="sess-preview">{{ targetLabel(s) }} · {{ s.count }} 条</span>
              <span v-if="s.busy" class="sess-busy" title="正在生成回复" />
            </span>
          </span>
        </button>

        <div v-if="!sessions.length && !loadingSessions" class="sess-empty">
          <span class="sess-empty-ico">💬</span>
          <p>还没有试聊会话</p>
          <small>新建一个，直接在面板里和 AI 对话测试；回复只显示在这里，不会发送到任何平台</small>
        </div>
        <div v-else-if="loadingSessions && !sessions.length" class="sess-loading">加载中…</div>
      </div>

      <div class="sess-foot">
        试聊复用完整对话管线（提示词 / 记忆 / 知识库 / 工具），回复仅回显面板
      </div>
    </aside>

    <!-- 右侧：对话窗口 -->
    <section class="chat-pane">
      <header class="chat-head">
        <button class="back-btn" type="button" title="返回会话列表" @click="backToList">‹</button>
        <div v-if="active" class="head-main">
          <div class="head-title-row">
            <span class="head-title">{{ active.title }}</span>
            <span class="head-chip" :class="'kind-' + active.target_kind">{{ targetLabel(active) }}</span>
          </div>
          <div class="head-sub">
            <span v-if="active.target_id" class="head-id">{{ active.target_id }}</span>
            <span v-else>不绑定会话上下文</span>
            <span class="dot">·</span>
            <span>{{ active.count }} 条消息</span>
            <span v-if="busy" class="head-busy">生成中…</span>
          </div>
        </div>
        <div v-else class="head-main">
          <div class="head-title">试聊室</div>
          <div class="head-sub">从左侧选择一个会话，或新建一个开始对话测试</div>
        </div>

        <div v-if="active" class="head-actions">
          <Button type="text" size="small" :disabled="busy" @click="onClear">清空上下文</Button>
          <Button v-if="busy" type="default" size="small" danger @click="onStop">停止</Button>
          <button class="head-del" type="button" title="删除会话" @click="onDelete">删除</button>
        </div>
      </header>

      <div ref="scrollEl" class="chat-scroll" @scroll="onScroll">
        <div class="chat-inner">
          <div v-if="!active" class="chat-empty">
            <span class="empty-ico">🗨️</span>
            <p>面板内直接对话测试</p>
            <small>
              绑定真实群聊/私聊的试聊会继承该会话的系统提示词覆盖、长期记忆与知识库；<br />
              「通用试聊」只带基础工具，适合快速验证模型与提示词。
            </small>
          </div>

          <div v-else-if="loadingMessages && !messages.length" class="chat-empty">
            <span class="empty-spinner" />
            <p>加载对话…</p>
          </div>

          <div v-else-if="!messages.length" class="chat-empty">
            <span class="empty-ico">✨</span>
            <p>开始第一句话吧</p>
            <small>AI 会像在平台上一样思考、调用工具，并把回复与工具调用展示在这里</small>
          </div>

          <template v-else>
            <div v-for="m in messages" :key="m.seq" class="msg-block">
              <!-- 系统提示 -->
              <div v-if="m.role === 'system'" class="sys-note">
                <span>{{ m.text }}</span>
                <span class="sys-time">{{ clock(m.time) }}</span>
              </div>

              <!-- 用户消息 -->
              <div v-else-if="m.role === 'user'" class="bubble-row me">
                <div class="bubble user-bubble">{{ m.text }}</div>
                <div class="bubble-meta me">{{ clock(m.time) }}</div>
              </div>

              <!-- AI 回复 -->
              <div v-else class="bubble-row bot">
                <span class="bot-avatar">AI</span>
                <div class="bubble-col">
                  <div class="bubble bot-bubble">
                    <div v-if="m.text" class="markdown-body ai-md" v-html="renderMd(m.text)" />
                    <div v-else-if="m.status === 'running'" class="typing"><i /><i /><i /></div>
                    <div v-else class="bubble-placeholder">（无输出）</div>

                    <div v-if="m.attachments && m.attachments.length" class="att-list">
                      <template v-for="(a, ai) in m.attachments" :key="ai">
                        <a
                          v-if="a.kind === 'image'"
                          class="att-image"
                          :href="a.url"
                          :download="'image-' + (ai + 1) + '.png'"
                          :title="'下载原图' + (a.size ? '（' + fmtSize(a.size) + '）' : '')"
                        >
                          <img :src="a.url" alt="AI 产出的图片" loading="lazy" />
                          <span class="att-image-tip">下载</span>
                        </a>
                        <a v-else class="att-file" :href="a.url" :download="a.name || 'file'" :title="'下载 ' + (a.name || '文件')">
                          <span class="att-file-ico">📎</span>
                          <span class="att-file-name">{{ a.name || '文件' }}</span>
                          <span v-if="a.size" class="att-file-size">{{ fmtSize(a.size) }}</span>
                        </a>
                      </template>
                    </div>

                    <div v-if="m.error" class="bubble-error">{{ m.error }}</div>
                    <div v-if="m.status === 'running'" class="bubble-running">
                      <span class="typing-dot" />生成中…
                    </div>
                  </div>

                  <div class="bubble-meta">
                    <span>{{ clock(m.time) }}</span>
                    <span v-if="m.duration_ms">{{ fmtDur(m.duration_ms) }}</span>
                    <span v-if="m.usage && m.usage.total_tokens">{{ fmtInt(m.usage.total_tokens) }} tokens</span>
                    <span v-if="m.usage && m.usage.iterations > 1">{{ m.usage.iterations }} 轮</span>
                    <button
                      v-if="m.tool_calls_total"
                      type="button"
                      class="tool-toggle"
                      @click="toggleTools(m.seq)"
                    >
                      工具 {{ m.tool_calls_total }} 次 {{ toolsOpen[m.seq] ? '▾' : '▸' }}
                    </button>
                  </div>

                  <div v-if="toolsOpen[m.seq] && m.tool_calls && m.tool_calls.length" class="tool-list">
                    <details v-for="(t, ti) in m.tool_calls" :key="ti" class="tool-item">
                      <summary>
                        <span class="tool-name">{{ t.name }}</span>
                        <span v-if="t.error" class="tool-badge error">失败</span>
                        <span v-else class="tool-badge ok">完成</span>
                        <span class="tool-ms">{{ t.duration_ms }}ms</span>
                      </summary>
                      <div v-if="t.arguments" class="tool-field">
                        <span class="tool-label">参数</span>
                        <pre>{{ t.arguments }}</pre>
                      </div>
                      <div v-if="t.error" class="tool-field">
                        <span class="tool-label">错误</span>
                        <pre class="tool-error">{{ t.error }}</pre>
                      </div>
                      <div v-if="t.result" class="tool-field">
                        <span class="tool-label">结果</span>
                        <pre>{{ t.result }}</pre>
                      </div>
                    </details>
                    <p v-if="m.tool_calls_total > m.tool_calls.length" class="tool-more">
                      其余 {{ m.tool_calls_total - m.tool_calls.length }} 次工具调用未展开（完整记录请查看运行日志）
                    </p>
                  </div>
                </div>
              </div>
            </div>
          </template>
        </div>

        <button v-if="!nearBottom && messages.length" type="button" class="to-bottom" @click="scrollToBottom()">
          ↓ 回到最新
        </button>
      </div>

      <footer class="composer">
        <p v-if="errorMsg" class="composer-error">{{ errorMsg }}</p>
        <textarea
          ref="composerEl"
          v-model="draft"
          class="composer-input"
          rows="2"
          :disabled="!active"
          :placeholder="active ? '输入一条消息，Enter 发送、Shift+Enter 换行' : '先选择或新建一个试聊会话'"
          @keydown.enter.exact.prevent="onEnter"
        />
        <div class="composer-bar">
          <span class="composer-hint">
            <template v-if="busy">AI 正在生成回复，可以先输入下一条消息，生成结束后再发送</template>
            <template v-else>
              试聊会真实执行 AI 选中的工具（含文件 / 命令类），回复不会发送到任何平台；每轮对话会记入 Query 日志并计入 Token 统计
            </template>
          </span>
          <Button
            type="primary"
            size="small"
            :disabled="!canSend"
            :loading="sending"
            @click="onSend"
          >
            发送
          </Button>
        </div>
      </footer>
    </section>

    <!-- 新建会话弹窗 -->
    <Modal variant="default" v-model:open="showCreate" title="新建试聊会话" :typewriter="false" :show-footer="false" :width="480">
      <form class="space-y-4 overflow-x-hidden" @submit.prevent="onCreate">
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">对话目标</label>
          <!-- 自定义目标选择器：通讯录可能有几十上百个群/好友，组件库的下拉弹层在窄弹窗里
               会被裁掉，这里用内联搜索 + 列表（不超出弹窗边界，也方便按名字检索） -->
          <div class="target-picker" @keydown.enter.prevent>
            <button type="button" class="target-current" @click="pickerOpen = !pickerOpen">
              <span class="target-badge" :class="'kind-' + selectedTarget.kind">{{ kindGlyph(selectedTarget) }}</span>
              <span class="target-current-name">{{ targetDisplay(selectedTarget) }}</span>
              <span class="target-current-id">{{ selectedTarget.id || '无会话上下文' }}</span>
              <span class="target-current-toggle">{{ pickerOpen ? '收起' : '更换' }}</span>
            </button>

            <div v-if="pickerOpen" class="target-panel">
              <Input v-model="targetQuery" size="small" placeholder="搜索群名 / 备注 / ID" allow-clear />
              <div class="target-list">
                <button
                  v-for="t in filteredTargets"
                  :key="t.key"
                  type="button"
                  class="target-opt"
                  :class="{ active: t.key === newTarget }"
                  @click="pickTarget(t)"
                >
                  <span class="target-badge" :class="'kind-' + t.kind">{{ kindGlyph(t) }}</span>
                  <span class="target-opt-main">
                    <span class="target-opt-name">{{ t.name }}</span>
                    <span class="target-opt-id">{{ t.id || '不绑定任何群聊 / 私聊' }}</span>
                  </span>
                  <span v-if="t.platform" class="target-opt-plat">{{ t.platform }}</span>
                </button>
                <p v-if="!filteredTargets.length" class="target-empty">
                  {{ targetQuery ? '没有匹配「' + targetQuery + '」的目标' : '暂无可选目标' }}
                </p>
              </div>
            </div>
          </div>
          <p v-if="loadingTargets" class="mt-1.5 text-[11px] text-slate-400">正在加载平台通讯录…</p>
          <p v-else class="mt-1.5 text-[11px] text-slate-400 leading-relaxed">{{ targetHint }}</p>
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">会话名称（可空，默认取目标名）</label>
          <Input v-model="newTitle" placeholder="如：群聊提示词调优" maxlength="64" />
        </div>
        <p v-if="createErr" class="text-xs text-[#ff3b30]">{{ createErr }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <Button type="default" @click="showCreate = false">取消</Button>
          <Button type="primary" html-type="submit" :loading="creating">创建</Button>
        </div>
      </form>
    </Modal>
  </div>
</template>

<script setup>
import { confirmAction } from '../ui/confirm'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Button, Input, Modal } from 'animal-island-vue'
import { fmtInt } from '../utils/format.js'
import { api } from '../api.js'

// 试聊室：面板内直接和 AI 对话做测试。回复只回显在本页，不发送到任何平台；
// 会话与转录持久化在后端，刷新页面 / 重启 Bot 后仍在。
const POLL_MS = 900 // 生成中轮询间隔
const IDLE_POLL_MS = 4000 // 空闲时低频轮询，兼顾多标签页同步

const sessions = ref([])
const activeId = ref('')
const messages = ref([])
const busy = ref(false)
const loadingSessions = ref(true)
const loadingMessages = ref(false)
const sending = ref(false)
const errorMsg = ref('')
const draft = ref('')
const toolsOpen = reactive({})
const nearBottom = ref(true)

const scrollEl = ref(null)
const composerEl = ref(null)

const showCreate = ref(false)
const creating = ref(false)
const createErr = ref('')
const newTitle = ref('')
const newTarget = ref('test|')
// 目标列表至少包含「通用试聊」：平台通讯录加载失败时仍可建通用会话
const targets = ref([{ kind: 'test', id: '', name: '通用试聊', key: 'test|' }])
const targetsLoaded = ref(false)
const loadingTargets = ref(false)
// 目标选择器：内联搜索 + 列表（不用组件库下拉弹层，避免在窄弹窗里被裁切）
const pickerOpen = ref(true)
const targetQuery = ref('')

let pollTimer = null
let pollMs = IDLE_POLL_MS

const active = computed(() => sessions.value.find((s) => s.id === activeId.value) || null)
const canSend = computed(() => !!active.value && !busy.value && !sending.value && draft.value.trim() !== '')

const selectedTarget = computed(
  () => targets.value.find((t) => t.key === newTarget.value) || targets.value[0],
)

// 搜索过滤：群名 / 好友备注 / ID / 平台任一命中
const filteredTargets = computed(() => {
  const q = targetQuery.value.trim().toLowerCase()
  if (!q) return targets.value
  return targets.value.filter((t) =>
    [t.name, t.id, t.platform].some((v) => (v || '').toLowerCase().includes(q)),
  )
})

const targetHint = computed(() => {
  const t = selectedTarget.value
  if (!t || t.kind === 'test') return '只带基础工具，不绑定任何群聊/私聊的提示词覆盖、记忆与知识库'
  const plat = t.platform ? '（' + t.platform + '）' : ''
  return '绑定后继承该会话的系统提示词覆盖、长期记忆、知识库与定时任务等工具' + plat
})

// ---------- 渲染工具 ----------

// 渲染 Markdown（AI 回复）：marked 转 HTML 后经 DOMPurify 清洗，避免注入。
// 结果按原文缓存：轮询只更新最后一条，历史气泡重复渲染时不必重新解析。
const mdCache = new Map()
function renderMd(text) {
  const key = text || ''
  const hit = mdCache.get(key)
  if (hit !== undefined) return hit
  let html
  try {
    html = DOMPurify.sanitize(marked.parse(key, { gfm: true, breaks: true }))
  } catch {
    html = escapeHtml(key)
  }
  if (mdCache.size > 200) mdCache.clear()
  mdCache.set(key, html)
  return html
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))
}

// kindGlyph 会话与目标共用：会话用 target_kind，目标用 kind
function kindGlyph(s) {
  const kind = s.kind || s.target_kind
  if (kind === 'group') return '群'
  if (kind === 'friend') return '私'
  return '试'
}

// targetDisplay 目标展示名（通用试聊不带名字）
function targetDisplay(t) {
  if (!t) return ''
  if (t.kind === 'test') return '通用试聊'
  return t.name || t.id || ''
}

function targetLabel(s) {
  if (!s) return ''
  if (s.target_kind === 'test') return '通用试聊'
  return (s.target_kind === 'group' ? '群 · ' : '私聊 · ') + (s.target_name || s.target_id || '')
}

function clock(v) {
  if (!v) return ''
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString('zh-CN', { hour12: false })
}

function relTime(v) {
  if (!v) return ''
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return ''
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  if (sameDay) return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
  const y = new Date(now)
  y.setDate(now.getDate() - 1)
  if (d.toDateString() === y.toDateString()) return '昨天'
  return (d.getMonth() + 1) + '-' + d.getDate()
}

function fmtSize(n) {
  if (!n) return ''
  if (n < 1024) return n + 'B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + 'KB'
  return (n / 1024 / 1024).toFixed(1) + 'MB'
}

function fmtDur(ms) {
  if (!ms || ms < 0) return ''
  if (ms < 1000) return ms + 'ms'
  return (ms / 1000).toFixed(1) + 's'
}

function toggleTools(seq) {
  toolsOpen[seq] = !toolsOpen[seq]
}

// ---------- 数据加载 ----------

async function loadSessions() {
  try {
    const data = await api.getChatConsoleSessions()
    sessions.value = data.items || []
    if (activeId.value && !sessions.value.some((s) => s.id === activeId.value)) {
      activeId.value = ''
      messages.value = []
    }
    if (!activeId.value && sessions.value.length) {
      selectSession(sessions.value[0].id)
    }
  } catch (e) {
    errorMsg.value = e.message
  } finally {
    loadingSessions.value = false
  }
}

// 合并增量消息：服务器总会返回最后一条（用于刷新流式文本），按 seq 覆盖
function applyMessages(items) {
  if (!items || !items.length) return 0
  const map = new Map(messages.value.map((m) => [m.seq, m]))
  let changed = 0
  for (const m of items) {
    const prev = map.get(m.seq)
    if (!prev || JSON.stringify(prev) !== JSON.stringify(m)) changed++
    map.set(m.seq, m)
  }
  if (changed) {
    messages.value = [...map.values()].sort((a, b) => a.seq - b.seq)
  }
  return changed
}

function maxSeq() {
  return messages.value.reduce((max, m) => (m.seq > max ? m.seq : max), 0)
}

async function pollMessages(force = false) {
  const id = activeId.value
  if (!id) return
  try {
    const data = await api.getChatConsoleMessages(id, force ? 0 : maxSeq())
    if (id !== activeId.value) return // 期间切换了会话
    const changed = applyMessages(data.items || [])
    busy.value = !!data.busy
    syncActiveMeta()
    if (changed) {
      if (nearBottom.value) scrollToBottom()
    }
  } catch (e) {
    // 会话可能已在其它标签页被删除：清空选中并回落到列表，避免持续轮询报错
    errorMsg.value = e.message
    if (/不存在/.test(e.message)) {
      activeId.value = ''
      messages.value = []
      busy.value = false
      stopPolling()
      await loadSessions()
      if (activeId.value) setPollSpeed()
    }
  }
}

// 用最新消息更新左侧会话的条数、预览与生成状态（避免整页刷新）
function syncActiveMeta() {
  const s = sessions.value.find((x) => x.id === activeId.value)
  if (!s) return
  s.count = messages.value.length
  s.busy = busy.value
  if (messages.value.length) s.updated_at = messages.value[messages.value.length - 1].time
}

// 移动端：返回会话列表（清空当前选中；服务端生成不受影响，重新选中即可看到结果）
function backToList() {
  activeId.value = ''
  messages.value = []
  busy.value = false
  errorMsg.value = ''
}

// selectSession 切换会话；opts.focus 为真时把焦点交给编辑框（用户点选会话时用，
// 首次进入页面自动选中第一个会话不抢焦点）
async function selectSession(id, opts = {}) {
  if (!id || id === activeId.value) return
  // 用户点选会话后顺手把焦点交给编辑框（同步聚焦，切过去就能直接打字）
  if (opts.focus) focusComposer()
  activeId.value = id
  messages.value = []
  busy.value = false
  errorMsg.value = ''
  for (const k of Object.keys(toolsOpen)) delete toolsOpen[k]
  loadingMessages.value = true
  nearBottom.value = true
  await pollMessages(true)
  loadingMessages.value = false
  scrollToBottom(false)
  setPollSpeed()
}

function setPollSpeed() {
  const want = busy.value ? POLL_MS : IDLE_POLL_MS
  if (pollTimer && want === pollMs) return
  pollMs = want
  startPolling()
}

function startPolling() {
  stopPolling()
  pollTimer = setInterval(async () => {
    if (!activeId.value) return
    await pollMessages()
    const want = busy.value ? POLL_MS : IDLE_POLL_MS
    if (want !== pollMs) setPollSpeed()
  }, pollMs)
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

// ---------- 发送 / 停止 / 清空 / 删除 ----------

// focusComposer 把焦点交还编辑框，光标落到末尾。点「发送」按钮或切换会话后焦点会
// 落在按钮上，不交还的话接着发消息还得再用鼠标点一下编辑框。
// 先同步聚焦（仍在点击事件的同一任务里，移动端也能保住软键盘），编辑框此刻还未
// 启用时（例如刚切换会话、DOM 还没更新）再等一次渲染后重试。
function focusComposer() {
  if (focusComposerNow()) return
  nextTick(focusComposerNow)
}

function focusComposerNow() {
  const el = composerEl.value
  if (!el || el.disabled) return false
  el.focus()
  const end = el.value.length
  try {
    el.setSelectionRange(end, end)
  } catch {
    // 个别浏览器在元素不可聚焦时不允许设置选区，忽略即可
  }
  return true
}

// Enter 发送；中文输入法组合态下的回车只用于选词，不能当发送
function onEnter(e) {
  if (e && (e.isComposing || e.keyCode === 229)) return
  onSend()
}

async function onSend() {
  const text = draft.value.trim()
  const id = activeId.value
  if (!text || !id || busy.value || sending.value) return
  // 点「发送」按钮时焦点落在按钮上，先同步抢回编辑框（移动端软键盘也不会被收起）
  focusComposer()
  sending.value = true
  errorMsg.value = ''
  try {
    await api.sendChatConsoleMessage(id, text)
    draft.value = ''
    // 发送成功后立刻把焦点还给编辑框，不用再点一下才能接着发下一条
    focusComposer()
    // 立即拉一次，让用户消息与「生成中」气泡立刻出现
    await pollMessages()
    busy.value = true
    nearBottom.value = true
    scrollToBottom()
    setPollSpeed()
  } catch (e) {
    errorMsg.value = e.message
    // 失败时草稿还在，焦点也留在编辑框，可以直接改一下再回车重发
    focusComposer()
  } finally {
    sending.value = false
  }
}

async function onStop() {
  if (!activeId.value) return
  try {
    await api.stopChatConsole(activeId.value)
    await pollMessages()
    // 「停止」按钮会随生成结束消失，焦点交还编辑框避免落到页面上
    focusComposer()
  } catch (e) {
    errorMsg.value = e.message
  }
}

async function onClear() {
  const id = activeId.value
  if (!id || !active.value) return
  if (!(await confirmAction('清空该会话的对话记录与 AI 上下文？清空后 AI 会忘记之前的对话。'))) return
  try {
    await api.clearChatConsoleSession(id)
    messages.value = []
    for (const k of Object.keys(toolsOpen)) delete toolsOpen[k]
    await loadSessions()
    focusComposer()
  } catch (e) {
    errorMsg.value = e.message
  }
}

async function onDelete() {
  const id = activeId.value
  if (!id || !active.value) return
  if (!(await confirmAction('删除会话「' + active.value.title + '」？对话记录与 AI 上下文会一并删除。'))) return
  try {
    await api.deleteChatConsoleSession(id)
    activeId.value = ''
    messages.value = []
    busy.value = false
    await loadSessions()
  } catch (e) {
    errorMsg.value = e.message
  }
}

// ---------- 新建会话 ----------

async function openCreate() {
  showCreate.value = true
  createErr.value = ''
  newTitle.value = ''
  targetQuery.value = ''
  pickerOpen.value = true
  if (targetsLoaded.value) return
  loadingTargets.value = true
  try {
    const data = await api.getChatConsoleTargets()
    const rest = (data.items || [])
      .filter((t) => t.kind !== 'test')
      .map((t) => ({ ...t, key: t.kind + '|' + (t.id || '') }))
    targets.value = [{ kind: 'test', id: '', name: '通用试聊', key: 'test|' }, ...rest]
    targetsLoaded.value = true
  } catch (e) {
    // 通讯录拉取失败不影响通用试聊
    createErr.value = '平台通讯录加载失败：' + e.message
  } finally {
    loadingTargets.value = false
    newTarget.value = 'test|'
  }
}

// pickTarget 选中目标并收起列表
function pickTarget(t) {
  newTarget.value = t.key
  pickerOpen.value = false
  targetQuery.value = ''
}

async function onCreate() {
  if (creating.value) return
  createErr.value = ''
  const t = selectedTarget.value
  if (!t) {
    createErr.value = '请选择一个对话目标'
    return
  }
  creating.value = true
  try {
    const sess = await api.createChatConsoleSession({
      target_kind: t.kind,
      target_id: t.id || '',
      target_name: t.name || '',
      title: newTitle.value.trim(),
    })
    showCreate.value = false
    await loadSessions()
    if (sess && sess.id) {
      await selectSession(sess.id, { focus: true })
    }
    // 新建完会话直接可以开始打字（弹窗关闭后焦点不留在页面上）
    focusComposer()
  } catch (e) {
    createErr.value = e.message
  } finally {
    creating.value = false
  }
}

// ---------- 滚动 ----------

function onScroll() {
  const el = scrollEl.value
  if (!el) return
  nearBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 90
}

function scrollToBottom(smooth = true) {
  nextTick(() => {
    const el = scrollEl.value
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
    nearBottom.value = true
  })
}

// ---------- 生命周期 ----------

onMounted(async () => {
  await loadSessions()
  startPolling()
})

onBeforeUnmount(stopPolling)

watch(busy, () => setPollSpeed())
</script>

<style scoped>
.chatconsole-shell {
  display: flex;
  height: calc(100dvh - 9rem);
  min-height: 540px;
  border-radius: 22px;
  border: 2px solid var(--line-strong);
  background: var(--surface);
  box-shadow: var(--shadow-base);
  overflow: hidden;
}

/* ================= 左侧会话列表 ================= */
.sess-pane {
  width: 272px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  border-right: 2px solid var(--line);
  background:
    radial-gradient(circle, rgb(196 184 158 / 0.2) 1px, transparent 1px) 0 0 / 18px 18px,
    linear-gradient(180deg, #fbf6e8 0%, #f7f0dd 100%);
}
.sess-head {
  padding: 13px 12px 10px;
  border-bottom: 1.5px dashed var(--line-strong);
}
.sess-heading {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 9px;
  padding: 0 2px;
}
.sess-heading-title {
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.12em;
  color: var(--ink);
}
.sess-heading-count {
  min-width: 18px;
  height: 17px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--surface-3);
  border: 1.5px solid var(--line-strong);
  color: var(--ink-soft);
  font-size: 10px;
  font-weight: 700;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.sess-scroll {
  flex: 1;
  overflow-y: auto;
  padding: 8px 8px 14px;
}
.sess-item {
  display: flex;
  align-items: center;
  gap: 9px;
  width: 100%;
  padding: 8px 9px;
  margin-bottom: 2px;
  border: 2px solid transparent;
  border-radius: 14px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  transition: background-color 0.15s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}
.sess-item:hover {
  background: rgb(255 253 247 / 0.68);
}
.sess-item.active {
  background: var(--surface);
  border-color: var(--line-strong);
  box-shadow: 0 2px 0 0 rgb(224 210 182 / 0.7);
}
.sess-item.active .sess-name {
  color: var(--accent-strong);
}
.sess-avatar {
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border-radius: 11px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 13px;
  font-weight: 800;
  text-shadow: 0 1px 2px rgb(94 68 35 / 0.26);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.35), 0 2px 0 0 rgb(94 68 35 / 0.12);
  user-select: none;
}
.sess-avatar.kind-test { background: linear-gradient(145deg, #f7cd67, #efb93f); }
.sess-avatar.kind-group { background: linear-gradient(145deg, var(--accent-hover), var(--accent)); }
.sess-avatar.kind-friend { background: linear-gradient(145deg, #8fb7f0, #6a97dd); }
.sess-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.sess-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.sess-name {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  font-weight: 700;
  color: var(--ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.sess-time {
  flex-shrink: 0;
  font-size: 10px;
  color: var(--ink-disabled);
  font-family: var(--font-mono);
}
.sess-preview {
  flex: 1;
  min-width: 0;
  font-size: 10.5px;
  color: var(--ink-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.sess-busy {
  flex-shrink: 0;
  width: 7px;
  height: 7px;
  border-radius: 999px;
  background: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
  animation: pulse 1.1s ease-in-out infinite;
}
@keyframes pulse {
  50% { opacity: 0.35; }
}
.sess-empty,
.sess-loading {
  padding: 34px 18px;
  text-align: center;
  color: var(--ink-muted);
}
.sess-empty-ico {
  display: block;
  font-size: 26px;
  margin-bottom: 6px;
}
.sess-empty p {
  margin: 0 0 4px;
  font-size: 12.5px;
  font-weight: 700;
  color: var(--ink-soft);
}
.sess-empty small {
  display: block;
  font-size: 10.5px;
  line-height: 1.7;
  color: var(--ink-disabled);
}
.sess-loading {
  font-size: 11.5px;
}
.sess-foot {
  padding: 9px 12px 11px;
  border-top: 1.5px dashed var(--line-strong);
  font-size: 10px;
  line-height: 1.6;
  color: var(--ink-disabled);
}

/* ================= 右侧对话 ================= */
.chat-pane {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: var(--surface);
}
.chat-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 16px;
  border-bottom: 2px solid var(--line);
  background: linear-gradient(180deg, #fffdf7, #fdf8ea);
}
.head-main {
  min-width: 0;
  flex: 1;
}
.head-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.head-title {
  font-size: 14px;
  font-weight: 800;
  color: var(--ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.head-chip {
  flex-shrink: 0;
  padding: 1.5px 8px;
  border-radius: 999px;
  font-size: 10px;
  font-weight: 700;
  border: 1.5px solid var(--line-strong);
  background: var(--surface-3);
  color: var(--ink-soft);
}
.head-chip.kind-test { background: #fff4cf; border-color: #f0dfa0; color: var(--warn-ink); }
.head-chip.kind-group { background: var(--accent-tint); border-color: #cdeee8; color: var(--accent-strong); }
.head-chip.kind-friend { background: #eef4fd; border-color: #cfe0f7; color: #3d6ea8; }
.head-sub {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
  font-size: 10.5px;
  color: var(--ink-muted);
  min-width: 0;
}
.head-id {
  font-family: var(--font-mono);
  color: var(--ink-soft);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.head-sub .dot { color: var(--ink-disabled); }
.head-busy {
  color: var(--accent-strong);
  font-weight: 700;
  animation: pulse 1.2s ease-in-out infinite;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
/* 移动端返回会话列表（桌面端不显示） */
.back-btn {
  display: none;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  border-radius: 9px;
  border: 1.5px solid var(--line-strong);
  background: var(--surface);
  color: var(--ink-soft);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
}
.head-del {
  padding: 4px 8px;
  border-radius: 9px;
  border: 1.5px solid transparent;
  background: transparent;
  color: var(--ink-disabled);
  font-size: 11px;
  font-weight: 700;
  cursor: pointer;
}
.head-del:hover {
  color: var(--danger-ink);
  border-color: #f3caca;
  background: var(--danger-soft);
}

.chat-scroll {
  position: relative;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 6px 20px 18px;
  background-image:
    radial-gradient(40rem 24rem at 100% 0%, rgb(25 200 185 / 0.05), transparent 60%),
    radial-gradient(32rem 20rem at 0% 100%, rgb(247 205 103 / 0.08), transparent 60%),
    radial-gradient(circle, rgb(196 184 158 / 0.17) 1px, transparent 1px);
  background-size: auto, auto, 20px 20px;
}
.chat-inner {
  max-width: 860px;
  margin: 0 auto;
  padding-top: 10px;
}
.chat-empty {
  height: 100%;
  min-height: 320px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 5px;
  color: var(--ink-muted);
  text-align: center;
  padding: 24px;
}
.chat-empty p {
  margin: 2px 0 0;
  font-size: 13px;
  font-weight: 700;
  color: var(--ink-soft);
}
.chat-empty small {
  max-width: 430px;
  font-size: 10.5px;
  line-height: 1.8;
  color: var(--ink-disabled);
}
.empty-ico {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 60px;
  height: 60px;
  margin-bottom: 4px;
  font-size: 24px;
  border-radius: 20px;
  background: var(--accent-tint);
  border: 2px solid #cdeee8;
  box-shadow: 0 3px 0 0 #d8ece6;
}
.empty-spinner {
  width: 22px;
  height: 22px;
  margin-bottom: 8px;
  border-radius: 999px;
  border: 3px solid rgb(196 184 158 / 0.35);
  border-top-color: var(--accent);
  animation: spin 0.8s linear infinite;
}
@keyframes spin {
  to { transform: rotate(360deg); }
}

/* 系统提示：居中灰条 */
.sys-note {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  margin: 10px auto;
  max-width: 90%;
  padding: 4px 12px;
  border-radius: 999px;
  background: rgb(255 253 247 / 0.9);
  border: 1.5px dashed var(--line-strong);
  color: var(--ink-muted);
  font-size: 10.5px;
  text-align: center;
}
.sys-time {
  font-family: var(--font-mono);
  font-size: 9.5px;
  color: var(--ink-disabled);
}

/* 气泡 */
.bubble-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 10px 0;
}
.bubble-row.me {
  flex-direction: column;
  align-items: flex-end;
}
.bubble-col {
  min-width: 0;
  max-width: min(78%, 640px);
  display: flex;
  flex-direction: column;
}
.bubble {
  padding: 8px 12px;
  border-radius: 16px;
  font-size: 13px;
  line-height: 1.7;
  word-break: break-word;
  white-space: pre-wrap;
}
.user-bubble {
  max-width: min(78%, 640px);
  background: linear-gradient(145deg, var(--accent-hover), var(--accent));
  border: 2px solid var(--accent-strong);
  box-shadow: 0 2px 0 0 var(--accent-strong);
  color: #fff;
  font-weight: 600;
}
.bot-avatar {
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  margin-top: 2px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10.5px;
  font-weight: 800;
  color: #fff;
  background: linear-gradient(145deg, #e8b45c, #d99b37);
  border: 2px solid #c98d2e;
  box-shadow: 0 2px 0 0 #c98d2e;
  user-select: none;
}
.bot-bubble {
  background: var(--surface);
  border: 2px solid var(--line-strong);
  box-shadow: 0 2px 0 0 rgb(224 210 182 / 0.75);
  color: var(--ink-body);
}
.bot-bubble .markdown-body {
  white-space: normal;
}

/* AI 回复的 Markdown 排版微调（沿用全局 .markdown-body，仅压缩间距） */
.ai-md :deep(> :first-child) { margin-top: 0; }
.ai-md :deep(> :last-child) { margin-bottom: 0; }
.ai-md :deep(p) { margin: 0.35em 0; }
.ai-md :deep(pre) {
  margin: 0.5em 0;
  border-radius: 10px;
  font-size: 11.5px;
}
.ai-md :deep(code) { font-size: 11.5px; }
.ai-md :deep(table) { font-size: 11.5px; }

.bubble-placeholder {
  color: var(--ink-disabled);
  font-size: 12px;
}
.bubble-error {
  margin-top: 6px;
  padding: 5px 9px;
  border-radius: 9px;
  background: var(--danger-soft);
  border: 1.5px solid #f3caca;
  color: var(--danger-ink);
  font-size: 11.5px;
  line-height: 1.6;
  white-space: pre-wrap;
}
.bubble-running {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  font-size: 10.5px;
  color: var(--accent-strong);
}
.typing {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 3px 0;
}
.typing i {
  width: 6px;
  height: 6px;
  border-radius: 999px;
  background: var(--ink-disabled);
  animation: bounce 1s ease-in-out infinite;
}
.typing i:nth-child(2) { animation-delay: 0.15s; }
.typing i:nth-child(3) { animation-delay: 0.3s; }
@keyframes bounce {
  0%, 60%, 100% { transform: translateY(0); opacity: 0.5; }
  30% { transform: translateY(-4px); opacity: 1; }
}
.typing-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 999px;
  background: var(--accent);
  animation: pulse 1.1s ease-in-out infinite;
}

/* 附件 */
.att-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
}
.att-image {
  position: relative;
  display: block;
  max-width: 240px;
  border-radius: 12px;
  overflow: hidden;
  border: 2px solid var(--line-strong);
  background: var(--surface-2);
  cursor: zoom-in;
}
.att-image img {
  display: block;
  width: 100%;
  height: auto;
}
.att-image-tip {
  position: absolute;
  right: 6px;
  bottom: 6px;
  padding: 1px 8px;
  border-radius: 999px;
  background: rgb(0 0 0 / 0.42);
  color: #fff;
  font-size: 10px;
  opacity: 0;
  transition: opacity 0.15s ease;
}
.att-image:hover .att-image-tip { opacity: 1; }
.att-file {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 5px 10px;
  border-radius: 10px;
  border: 1.5px solid var(--line-strong);
  background: var(--surface-2);
  color: var(--ink-body);
  font-size: 11.5px;
  text-decoration: none;
}
.att-file:hover { border-color: var(--accent); color: var(--accent-strong); }
.att-file-name {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.att-file-size { color: var(--ink-disabled); font-size: 10px; }

/* 元信息与工具明细 */
.bubble-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
  padding: 0 3px;
  font-size: 9.5px;
  color: var(--ink-disabled);
  font-family: var(--font-mono);
  flex-wrap: wrap;
}
.bubble-meta.me {
  justify-content: flex-end;
}
.tool-toggle {
  border: none;
  background: transparent;
  padding: 0;
  font-size: 9.5px;
  font-weight: 700;
  color: var(--accent-strong);
  cursor: pointer;
}
.tool-list {
  margin-top: 6px;
  display: flex;
  flex-direction: column;
  gap: 5px;
  max-width: min(78%, 640px);
}
.tool-item {
  border: 1.5px solid var(--line);
  border-radius: 10px;
  background: var(--surface-2);
  padding: 5px 9px;
  font-size: 11px;
}
.tool-item summary {
  display: flex;
  align-items: center;
  gap: 7px;
  cursor: pointer;
  list-style: none;
  color: var(--ink-soft);
}
.tool-item summary::-webkit-details-marker { display: none; }
.tool-name {
  font-family: var(--font-mono);
  font-weight: 700;
  color: var(--ink);
}
.tool-badge {
  padding: 0 6px;
  border-radius: 999px;
  font-size: 9.5px;
  font-weight: 700;
}
.tool-badge.ok { background: var(--ok-soft); color: var(--ok-ink); }
.tool-badge.error { background: var(--danger-soft); color: var(--danger-ink); }
.tool-ms {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: 9.5px;
  color: var(--ink-disabled);
}
.tool-field { margin-top: 5px; }
.tool-label {
  display: block;
  font-size: 9.5px;
  font-weight: 700;
  letter-spacing: 0.06em;
  color: var(--ink-muted);
  margin-bottom: 2px;
}
.tool-field pre {
  margin: 0;
  padding: 6px 8px;
  border-radius: 8px;
  background: var(--surface);
  border: 1px solid var(--line);
  font-family: var(--font-mono);
  font-size: 10.5px;
  line-height: 1.55;
  color: var(--ink-body);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 220px;
  overflow: auto;
}
.tool-field pre.tool-error { color: var(--danger-ink); }
.tool-more {
  margin: 0;
  font-size: 10px;
  color: var(--ink-disabled);
}

.to-bottom {
  position: absolute;
  right: 22px;
  bottom: 12px;
  z-index: 5;
  padding: 5px 13px;
  border-radius: 999px;
  border: 2px solid var(--line-strong);
  background: var(--surface);
  color: var(--ink-soft);
  font-size: 10.5px;
  font-weight: 700;
  cursor: pointer;
  box-shadow: var(--shadow-sm);
}
.to-bottom:hover {
  border-color: var(--accent);
  color: var(--accent-strong);
}

/* 输入区 */
.composer {
  border-top: 2px solid var(--line);
  background: linear-gradient(180deg, #fdf8ea, #fbf3e0);
  padding: 10px 16px 12px;
}
.composer-error {
  margin: 0 0 6px;
  font-size: 11px;
  color: var(--danger-ink);
}
.composer-input {
  width: 100%;
  resize: none;
  padding: 9px 12px;
  border-radius: 14px;
  border: 2px solid var(--line-input);
  background: var(--surface-input);
  color: var(--ink-body);
  font-size: 13px;
  line-height: 1.6;
  font-family: inherit;
  outline: none;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}
.composer-input:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
}
.composer-input:disabled {
  background: var(--surface-2);
  color: var(--ink-disabled);
  cursor: not-allowed;
}
.composer-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 8px;
}
.composer-hint {
  flex: 1;
  min-width: 0;
  font-size: 10px;
  line-height: 1.5;
  color: var(--ink-disabled);
}

/* 新建会话：对话目标选择器（内联搜索 + 列表，不超出弹窗） */
.target-picker {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.target-current {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 10px;
  border-radius: 12px;
  border: 2px solid var(--line-input);
  background: var(--surface-input);
  cursor: pointer;
  text-align: left;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}
.target-current:hover {
  border-color: var(--accent);
}
.target-badge {
  width: 26px;
  height: 26px;
  flex-shrink: 0;
  border-radius: 8px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 11.5px;
  font-weight: 800;
  text-shadow: 0 1px 2px rgb(94 68 35 / 0.26);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.35);
}
.target-badge.kind-test { background: linear-gradient(145deg, #f7cd67, #efb93f); }
.target-badge.kind-group { background: linear-gradient(145deg, var(--accent-hover), var(--accent)); }
.target-badge.kind-friend { background: linear-gradient(145deg, #8fb7f0, #6a97dd); }
.target-current-name {
  flex: 0 1 auto;
  min-width: 0;
  font-size: 13px;
  font-weight: 700;
  color: var(--ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.target-current-id {
  flex: 1 1 auto;
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--ink-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.target-current-toggle {
  margin-left: auto;
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 700;
  color: var(--accent-strong);
}
.target-panel {
  width: 100%;
  padding: 8px;
  border-radius: 12px;
  border: 1.5px solid var(--line-strong);
  background: var(--surface);
  box-shadow: var(--shadow-sm);
}
.target-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 208px;
  margin-top: 6px;
  overflow-y: auto;
  overflow-x: hidden;
}
.target-opt {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 8px;
  border-radius: 9px;
  border: 1.5px solid transparent;
  background: transparent;
  text-align: left;
  cursor: pointer;
}
.target-opt:hover {
  background: var(--surface-2);
}
.target-opt.active {
  background: var(--accent-tint);
  border-color: #cdeee8;
}
.target-opt-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.target-opt-name {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.target-opt-id {
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--ink-disabled);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.target-opt-plat {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--surface-3);
  color: var(--ink-soft);
  font-size: 9.5px;
  font-weight: 700;
}
.target-empty {
  margin: 8px 0 4px;
  text-align: center;
  font-size: 11px;
  color: var(--ink-disabled);
}

@media (max-width: 860px) {
  .chatconsole-shell {
    height: calc(100dvh - 11rem);
    flex-direction: column;
  }
  /* 窄屏：会话列表与对话二选一展示（有选中会话时展示对话，返回按钮回到列表） */
  .sess-pane {
    width: 100%;
    flex: 1;
    max-height: none;
    border-right: none;
    border-bottom: 2px solid var(--line);
  }
  .chatconsole-shell.has-active .sess-pane {
    display: none;
  }
  .chat-pane {
    display: none;
  }
  .chatconsole-shell.has-active .chat-pane {
    display: flex;
    flex: 1;
  }
  .back-btn {
    display: inline-flex;
  }
  .bubble-col,
  .user-bubble {
    max-width: 88%;
  }
  .bubble-col {
    max-width: 92%;
  }
}
</style>
