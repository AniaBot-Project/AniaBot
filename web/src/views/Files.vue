<template>
  <div class="space-y-5">
    <Transition name="fade">
      <div v-if="saved" class="bg-emerald-50 border border-emerald-200 text-emerald-800 text-sm rounded-xl px-4 py-3" v-html="savedHint" />
    </Transition>

    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex gap-1 glass rounded-xl p-1 overflow-x-auto">
        <button
          v-for="tab in tabs"
          :key="tab.name"
          class="px-4 py-1.5 text-sm rounded-md transition-colors"
          :class="current === tab.name ? 'btn-accent font-medium shadow-sm' : 'text-slate-500 hover:text-slate-800'"
          @click="switchTab(tab.name)"
        >
          {{ tab.label }}
        </button>
      </div>
      <div class="flex gap-2">
        <button v-if="hasForm" class="px-3 py-1.5 text-sm rounded-lg border border-slate-300/60 text-slate-600 hover:bg-white/55 transition-colors" @click="toggleRaw">
          {{ rawMode ? '表单模式' : '源码模式 (JSON)' }}
        </button>
          <button v-if="!rawMode && hasForm && current !== 'prompt'" :disabled="saving" class="px-4 py-1.5 text-sm rounded-lg btn-accent disabled:opacity-40 transition-colors" @click="onSave">
          {{ saving ? '保存中...' : '保存' }}
        </button>
      </div>
    </div>

    <!-- 源码模式：原始 JSON（无表单的 tab 恒为源码模式） -->
    <section v-if="rawMode || !hasForm" class="bg-white rounded-xl shadow-sm border border-white/60 p-6 space-y-3">
      <p class="text-xs text-slate-500">{{ currentTab.desc }}</p>
      <textarea
        v-model="rawText"
        rows="20"
        spellcheck="false"
        class="w-full bg-slate-950/85 text-slate-200 rounded-lg px-4 py-3 text-xs font-mono leading-relaxed focus:outline-none focus:ring-2 focus:ring-zinc-400"
      />
      <div class="flex items-center gap-3">
        <button class="px-3 py-1.5 text-sm rounded-lg border border-slate-300/60 text-slate-600 hover:bg-white/55 transition-colors" @click="formatRaw">格式化</button>
          <button :disabled="saving" class="px-4 py-1.5 text-sm rounded-lg btn-accent disabled:opacity-40 transition-colors" @click="onSaveRaw">
          {{ saving ? '保存中...' : '保存' }}
        </button>
        <span v-if="error" class="text-sm text-red-600">{{ error }}</span>
      </div>
    </section>

    <!-- MCP 服务器：图形化表单 -->
    <template v-else-if="current === 'mcp'">
      <p class="text-xs text-slate-500">配置 AI 可调用的 MCP 服务器，修改保存后重启生效。</p>

      <section v-for="(srv, i) in mcpServers" :key="i" class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
        <div class="px-6 py-3.5 border-b border-white/50 flex items-center justify-between">
          <h2 class="text-sm font-semibold text-slate-800 flex items-center gap-2">
            <span class="w-2 h-2 rounded-full" :class="srv.name ? 'bg-emerald-400' : 'bg-slate-300'" />
            {{ srv.name || `服务器 ${i + 1}` }}
          </h2>
          <button class="text-xs text-red-500 hover:text-red-700 transition-colors" @click="mcpServers.splice(i, 1)">删除</button>
        </div>
        <div class="p-6 grid grid-cols-1 lg:grid-cols-2 gap-x-8 gap-y-5">
          <div>
            <label :class="labelClass">名称 <span class="text-red-500">*</span></label>
            <input v-model="srv.name" type="text" placeholder="如 weather" :class="inputClass" />
          </div>
          <div>
            <label :class="labelClass">连接方式</label>
            <select v-model="srv.transport" :class="inputClass">
              <option value="stdio">stdio（本地命令）</option>
              <option value="streamable">streamable（HTTP）</option>
              <option value="sse">sse（HTTP）</option>
            </select>
          </div>
          <div>
            <label :class="labelClass">超时时间（秒）</label>
            <input v-model="srv.timeout" type="number" min="0" step="1" placeholder="默认" :class="inputClass" />
          </div>
          <div>
            <label :class="labelClass">描述</label>
            <input v-model="srv.description" type="text" placeholder="这个服务器是做什么的" :class="inputClass" />
          </div>

          <!-- stdio 字段 -->
          <template v-if="srv.transport === 'stdio'">
            <div>
              <label :class="labelClass">启动命令 <span class="text-red-500">*</span></label>
              <input v-model="srv.command" type="text" placeholder="如 npx / uvx / node" :class="inputClass" />
            </div>
            <div>
              <label :class="labelClass">命令参数</label>
              <input v-model="srv.argsText" type="text" placeholder="以空格分隔，如 -y @modelcontextprotocol/server-everything" :class="inputClass" />
            </div>
            <div class="lg:col-span-2">
              <label :class="labelClass">环境变量</label>
              <KvEditor v-model="srv.env" key-placeholder="变量名" value-placeholder="值" />
            </div>
          </template>

          <!-- HTTP 字段 -->
          <template v-else>
            <div class="lg:col-span-2">
              <label :class="labelClass">服务地址 (Endpoint) <span class="text-red-500">*</span></label>
              <input v-model="srv.endpoint" type="text" placeholder="如 http://localhost:8080/mcp" :class="inputClass" />
            </div>
            <div class="lg:col-span-2">
              <label :class="labelClass">请求头</label>
              <KvEditor v-model="srv.headers" key-placeholder="Header 名" value-placeholder="值" />
            </div>
          </template>
        </div>
      </section>

      <button class="w-full py-3 text-sm rounded-xl border-2 border-dashed border-slate-300/60 text-slate-400 hover:border-zinc-500 hover:text-zinc-700 hover:bg-white/60 transition-all" @click="addServer">
        + 添加 MCP 服务器
      </button>
      <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
    </template>

    <!-- Prompt 覆盖：列表 + 弹窗编辑 -->
    <template v-else-if="current === 'prompt'">
      <p class="text-xs text-slate-500">按群聊 / 好友覆盖 AI 的系统提示词，点击条目弹出编辑框，保存后立即生效（无需重启）。</p>

      <template v-for="section in promptSections" :key="section.kind">
        <section class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
          <div class="px-6 py-3.5 border-b border-white/50 flex items-center justify-between">
            <h2 class="text-sm font-semibold text-slate-800">
              {{ section.title }}
              <span class="ml-2 text-xs font-normal text-slate-400">{{ section.items.length }} 条</span>
            </h2>
            <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="openPromptEditor(section.kind)">+ 添加</button>
          </div>
          <p v-if="section.items.length === 0" class="px-6 py-8 text-xs text-slate-400">{{ section.empty }}</p>
          <ul v-else class="divide-y divide-slate-100">
            <li
              v-for="(item, i) in section.items"
              :key="i"
              class="px-5 py-3.5 flex items-start gap-4 cursor-pointer hover:bg-white/55 transition-colors"
              @click="openPromptEditor(section.kind, item, i)"
            >
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2 min-w-0">
                  <span class="text-sm font-medium text-slate-800 font-mono truncate">{{ item.id || '未填写 ID' }}</span>
                  <span class="text-[11px] text-slate-400 shrink-0">{{ (item.prompt || '').length }} 字</span>
                </div>
                <p class="text-xs text-slate-500 leading-relaxed whitespace-pre-wrap break-all mt-1 line-clamp-2">{{ previewPrompt(item.prompt) }}</p>
              </div>
              <div class="flex items-center gap-1 shrink-0">
                <button
                  class="text-xs text-zinc-600 hover:text-zinc-900 hover:bg-white/60 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                  @click.stop="openPromptEditor(section.kind, item, i)"
                >
                  编辑
                </button>
                <button
                  class="text-xs text-red-500 hover:text-red-600 hover:bg-red-50 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                  @click.stop="deletePrompt(section.kind, i)"
                >
                  删除
                </button>
              </div>
            </li>
          </ul>
        </section>
      </template>
      <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
    </template>

    <!-- Prompt 覆盖：新增 / 编辑弹窗 -->
    <Teleport to="body">
      <div
        v-if="showPromptEditor"
        class="fixed inset-0 bg-slate-900/45 backdrop-blur-md flex items-center justify-center z-50 p-4"
        v-backdrop-close="closePromptEditor"
      >
        <form
          class="bg-white rounded-xl shadow-2xl border border-zinc-200 w-full max-w-4xl flex flex-col max-h-[92vh]"
          @submit.prevent="savePromptEditor"
        >
          <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between">
            <h3 class="text-sm font-semibold text-slate-800">{{ promptDraft.id ? '编辑覆盖' : '新增覆盖' }}</h3>
            <button type="button" class="text-slate-400 hover:text-slate-700 transition-colors" @click="closePromptEditor">✕</button>
          </div>
          <div class="px-6 py-5 space-y-4 overflow-y-auto flex-1">
            <div>
              <label class="block text-xs font-medium text-slate-600 mb-1.5">
                {{ promptDraft.kind === 'friends' ? '用户 ID' : '群 ID' }} <span class="text-red-500">*</span>
              </label>
              <input
                v-model.trim="promptDraft.id"
                type="text"
                :placeholder="promptDraft.kind === 'friends' ? '用户 ID（统一带前缀，如 qq:123456 或 fs:ou_xxx）' : '群 ID（统一带前缀，如 qq:123456 或 fs:oc_xxx）'"
                :class="inputClass"
              />
            </div>
            <div>
              <label class="block text-xs font-medium text-slate-600 mb-1.5">系统提示词 <span class="text-red-500">*</span></label>
              <textarea
                v-model.trim="promptDraft.prompt"
                rows="16"
                placeholder="该会话使用的系统提示词"
                :class="inputClass + ' font-mono leading-relaxed resize-y min-h-[55vh]'"
              />
              <p class="text-[11px] text-slate-400 mt-1.5">{{ promptDraft.prompt.length }} 字</p>
            </div>
            <p v-if="promptEditorError" class="text-sm text-red-600">{{ promptEditorError }}</p>
            <div class="flex justify-end gap-2 pt-1">
              <button type="button" class="px-4 py-2 text-sm text-slate-600 hover:bg-white/60 rounded-lg transition-colors" @click="closePromptEditor">取消</button>
            <button type="submit" :disabled="saving" class="px-4 py-2 text-sm btn-accent rounded-lg disabled:opacity-40 transition-colors">{{ saving ? '保存中...' : '保存并生效' }}</button>
            </div>
          </div>
        </form>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { computed, defineComponent, h, onMounted, reactive, ref } from 'vue'
import { api } from '../api.js'

const inputClass = 'w-full border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400 transition-shadow'
const labelClass = 'block text-xs font-medium text-slate-600 mb-1.5'

// 键值对编辑器（环境变量 / 请求头）
const KvEditor = defineComponent({
  props: {
    modelValue: { type: Array, default: () => [] }, // [{k, v}]
    keyPlaceholder: { type: String, default: '键' },
    valuePlaceholder: { type: String, default: '值' },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    const update = (i, field, val) => {
      const next = props.modelValue.map((row, j) => (j === i ? { ...row, [field]: val } : row))
      emit('update:modelValue', next)
    }
    const remove = (i) => emit('update:modelValue', props.modelValue.filter((_, j) => j !== i))
    const add = () => emit('update:modelValue', [...props.modelValue, { k: '', v: '' }])
    return () =>
      h('div', { class: 'space-y-2' }, [
        ...props.modelValue.map((row, i) =>
          h('div', { class: 'flex items-center gap-2', key: i }, [
            h('input', {
              class: inputClass,
              type: 'text',
              placeholder: props.keyPlaceholder,
              value: row.k,
              onInput: (e) => update(i, 'k', e.target.value),
            }),
            h('input', {
              class: inputClass,
              type: 'text',
              placeholder: props.valuePlaceholder,
              value: row.v,
              onInput: (e) => update(i, 'v', e.target.value),
            }),
            h('button', { class: 'text-xs text-red-500 hover:text-red-700 shrink-0 transition-colors', onClick: () => remove(i) }, '删除'),
          ])
        ),
        h(
          'button',
          { class: 'text-xs text-zinc-700 hover:text-zinc-900 transition-colors', onClick: add },
          '+ 添加一行'
        ),
      ])
  },
})

const tabs = [
  { name: 'mcp', label: 'MCP 服务器', desc: '格式: {"servers": [{name, transport(stdio/streamable/sse), command/endpoint, args, env, headers, timeout, description}]}' },
  { name: 'prompt', label: 'Prompt 覆盖', desc: '格式: {"groups": {"群ID": "prompt"}, "friends": {"用户ID": "prompt"}}（统一带平台前缀，如 qq:123456 或 fs:oc_xxx）', hot: true },
  { name: 'hooks', label: 'AI 钩子', desc: '格式: {"hooks": {"事件名": [{matcher(工具名正则,可空), command, timeout_sec(可空)}]}}。事件: SessionStart/UserPromptSubmit/PreToolUse/PostToolUse/Stop/SubagentStop/PreCompact。stdin 接收 JSON 载荷；退出码 0=通过(stdout 注入上下文) / 2=阻断(stderr 为原因) / 其他=仅记日志。保存后数秒内生效', hot: true },
  { name: 'commands', label: '自定义命令', desc: '格式: {"commands": {"命令名": "提示词模板"}}。模板中 $args 为用户参数占位符（无占位符时参数追加到末尾）；命令名字母开头、最长 32 字符，不得与内置命令撞名。保存后数秒内生效', hot: true },
]

const current = ref('mcp')
const error = ref('')
const saved = ref(false)
const saving = ref(false)
const rawMode = ref(false)
const rawText = ref('')

// 图形化表单模型
const mcpServers = ref([])
const promptGroups = ref([])
const promptFriends = ref([])

const showPromptEditor = ref(false)
const promptEditorError = ref('')
const promptEditingIndex = ref(-1)
const promptDraft = reactive({ kind: 'groups', id: '', prompt: '' })

const promptSections = computed(() => [
  { kind: 'groups', title: '群聊覆盖', empty: '暂无群聊 Prompt 覆盖', items: promptGroups.value },
  { kind: 'friends', title: '好友覆盖', empty: '暂无好友 Prompt 覆盖', items: promptFriends.value },
])

const currentTab = computed(() => tabs.find((t) => t.name === current.value))

// 仅 mcp/prompt 有图形化表单；hooks/commands 恒为源码模式（保存后热生效，无需重启）
const hasForm = computed(() => current.value === 'mcp' || current.value === 'prompt')
const savedHint = computed(() => (currentTab.value?.hot ? '已保存，<b>数秒内自动生效</b>。' : '已保存，将在 <b>重启 Bot 后生效</b>。'))

// ---- 解析：JSON -> 表单模型 ----

function kvToRows(obj) {
  return Object.entries(obj || {}).map(([k, v]) => ({ k, v }))
}

function parseMcp(content) {
  if (!content.trim()) return []
  const data = JSON.parse(content)
  return (data.servers || []).map((s) => {
    const transport = (s.transport || 'stdio').toLowerCase()
    const isHttp = transport === 'streamable' || transport === 'streamable-http' || transport === 'sse'
    return {
      name: s.name || '',
      transport: isHttp ? (transport === 'sse' ? 'sse' : 'streamable') : 'stdio',
      command: s.command || '',
      argsText: (s.args || []).join(' '),
      env: kvToRows(s.env),
      endpoint: s.endpoint || '',
      headers: kvToRows(s.headers),
      timeout: s.timeout ? String(s.timeout) : '',
      description: s.description || '',
    }
  })
}

function parsePrompt(content) {
  if (!content.trim()) return { groups: [], friends: [] }
  const data = JSON.parse(content)
  return {
    groups: Object.entries(data.groups || {}).map(([id, prompt]) => ({ id: normalizePromptID(id), prompt })),
    friends: Object.entries(data.friends || {}).map(([id, prompt]) => ({ id: normalizePromptID(id), prompt })),
  }
}

// Prompt 覆盖 ID 统一带平台前缀（qq:/lil:/qo:/fs:/tg:/dc:/wx:）：纯数字自动补 qq: 前缀，
// 其他 ID 手动带各自前缀；返回规范化后的 ID。
function normalizePromptID(id) {
  id = id.trim()
  if (/^\d+$/.test(id)) return 'qq:' + id
  const m = id.match(/^([^:]+):(.*)$/)
  if (m) return m[1].toLowerCase() + ':' + m[2]
  return id
}

// validatePromptID 校验前缀与各平台 ID 格式；kind 为 groups/friends（飞书群 oc_/用户 ou_）。
function validatePromptID(id, kind) {
  const m = id.match(/^([^:]+):(.+)$/)
  if (!m) return '请带上平台前缀（如 qq:123456、fs:oc_xxx）'
  const prefix = m[1].toLowerCase()
  const rest = m[2]
  switch (prefix) {
    case 'qq':
      if (!/^\d+$/.test(rest)) return 'QQ ID 应为纯数字，如 qq:123456'
      break
    case 'tg':
      if (!/^-?\d+$/.test(rest)) return 'Telegram ID 应为数字，如 tg:123456 或 tg:-1001234567'
      break
    case 'dc':
      if (!/^\d+$/.test(rest)) return 'Discord ID 应为数字，如 dc:123456789'
      break
    case 'qo':
      if (!/^[A-Za-z0-9_-]+$/.test(rest)) return 'QQ 官方 openid 格式不合法，如 qo:xxxxxxxx'
      break
    case 'fs':
      if (kind === 'friends') {
        if (!/^ou_/.test(rest)) return '飞书用户 ID 应以 ou_ 开头，如 fs:ou_xxx'
      } else if (!/^oc_/.test(rest)) {
        return '飞书群 ID 应以 oc_ 开头，如 fs:oc_xxx'
      }
      break
    case 'lil':
      if (!/^\d+$/.test(rest)) return 'QQ(LLBot) ID 应为纯数字，如 lil:123456'
      break
    case 'wx':
      if (!/^[A-Za-z0-9_-]+$/.test(rest)) return '微信用户 ID 格式不合法，如 wx:wxid_xxx'
      break
    default:
      return '未知平台前缀「' + prefix + '」（支持 qq: / lil: / qo: / fs: / tg: / dc: / wx:）'
  }
  return ''
}

// normalizePromptContent 源码模式保存前整体规范化并逐条校验（群/好友都校验），
// 保证落库的 files.prompt_json 统一带平台前缀。
function normalizePromptContent(content) {
  if (!content.trim()) return content
  const data = JSON.parse(content)
  const out = { groups: {}, friends: {} }
  for (const [id, prompt] of Object.entries(data.groups || {})) {
    const nid = normalizePromptID(id)
    const err = validatePromptID(nid, 'groups')
    if (err) throw new Error('群 ID「' + id + '」' + err)
    out.groups[nid] = prompt
  }
  for (const [id, prompt] of Object.entries(data.friends || {})) {
    const nid = normalizePromptID(id)
    const err = validatePromptID(nid, 'friends')
    if (err) throw new Error('用户 ID「' + id + '」' + err)
    out.friends[nid] = prompt
  }
  return JSON.stringify(out, null, 2)
}

function previewPrompt(prompt) {
  return (prompt || '').trim() || '（未填写系统提示词）'
}

function openPromptEditor(kind, item = null, index = -1) {
  promptDraft.kind = kind
  promptDraft.id = item?.id || ''
  promptDraft.prompt = item?.prompt || ''
  promptEditingIndex.value = index
  promptEditorError.value = ''
  showPromptEditor.value = true
}

function closePromptEditor() {
  showPromptEditor.value = false
  promptEditorError.value = ''
}

async function savePromptEditor() {
  const prompt = promptDraft.prompt.trim()
  if (!promptDraft.id.trim()) {
    promptEditorError.value = '请填写 ID'
    return
  }
  if (!prompt) {
    promptEditorError.value = '请填写系统提示词'
    return
  }
  // 统一带平台前缀：QQ 纯数字自动补 qq:，其余逐条校验格式
  const id = normalizePromptID(promptDraft.id)
  const idError = validatePromptID(id, promptDraft.kind)
  if (idError) {
    promptEditorError.value = idError
    return
  }

  const list = promptDraft.kind === 'friends' ? promptFriends : promptGroups
  if (promptEditingIndex.value >= 0 && list.value[promptEditingIndex.value]) {
    const duplicateIndex = list.value.findIndex((row) => row.id === id)
    if (duplicateIndex >= 0 && duplicateIndex !== promptEditingIndex.value) {
      promptEditorError.value = '该 ID 已存在'
      return
    }
    list.value[promptEditingIndex.value] = { id, prompt }
  } else {
    if (list.value.some((row) => row.id === id)) {
      promptEditorError.value = '该 ID 已存在'
      return
    }
    list.value.push({ id, prompt })
  }
  // 弹窗保存即持久化：写回配置中心并热生效，无需再点页面顶部的保存按钮
  try {
    await persistPrompt()
    closePromptEditor()
  } catch (e) {
    // 保存失败保留弹窗与草稿，方便修正后重试
    promptEditorError.value = e.message || '保存失败'
  }
}

async function deletePrompt(kind, index) {
  const list = kind === 'friends' ? promptFriends : promptGroups
  const item = list.value[index]
  if (!item) return
  if (!confirm(`确定删除「${item.id || '未填写 ID'}」的覆盖吗？`)) return
  list.value.splice(index, 1)
  if (promptDraft.kind === kind && promptEditingIndex.value === index) closePromptEditor()
  // 删除同样立即落库生效
  try {
    await persistPrompt()
  } catch { /* error 已设置 */ }
}

// ---- 序列化：表单模型 -> JSON ----

function rowsToKv(rows) {
  const out = {}
  for (const { k, v } of rows) {
    if (k.trim()) out[k.trim()] = v
  }
  return out
}

function serializeMcp() {
  const servers = []
  for (const [i, s] of mcpServers.value.entries()) {
    if (!s.name.trim()) throw new Error(`第 ${i + 1} 个服务器缺少名称`)
    const entry = { name: s.name.trim(), transport: s.transport }
    const timeout = parseInt(s.timeout, 10)
    if (!Number.isNaN(timeout) && timeout > 0) entry.timeout = timeout
    if (s.description.trim()) entry.description = s.description.trim()
    if (s.transport === 'stdio') {
      if (!s.command.trim()) throw new Error(`服务器「${entry.name}」缺少启动命令`)
      entry.command = s.command.trim()
      const args = s.argsText.split(/\s+/).filter(Boolean)
      if (args.length) entry.args = args
      const env = rowsToKv(s.env)
      if (Object.keys(env).length) entry.env = env
    } else {
      if (!s.endpoint.trim()) throw new Error(`服务器「${entry.name}」缺少服务地址`)
      entry.endpoint = s.endpoint.trim()
      const headers = rowsToKv(s.headers)
      if (Object.keys(headers).length) entry.headers = headers
    }
    servers.push(entry)
  }
  return JSON.stringify({ servers }, null, 2)
}

function serializePrompt() {
  const groups = {}
  for (const { id, prompt } of promptGroups.value) {
    if (id.trim() && prompt.trim()) groups[id.trim()] = prompt
  }
  const friends = {}
  for (const { id, prompt } of promptFriends.value) {
    if (id.trim() && prompt.trim()) friends[id.trim()] = prompt
  }
  return JSON.stringify({ groups, friends }, null, 2)
}

// persistPrompt 把 Prompt 覆盖表单整体写回配置中心（弹窗保存/删除都走这里），
// 保存即热生效；同步 rawText，切到源码模式时看到的是最新内容。
async function persistPrompt() {
  error.value = ''
  const content = serializePrompt()
  await save(content)
  rawText.value = content
}

// ---- 加载 / 切换 / 保存 ----

async function load() {
  error.value = ''
  const data = await api.getFile(current.value)
  rawText.value = data.content || ''
  if (!hasForm.value) return // hooks/commands 为纯源码模式，无需解析表单
  try {
    if (current.value === 'mcp') {
      mcpServers.value = parseMcp(rawText.value)
    } else {
      const { groups, friends } = parsePrompt(rawText.value)
      promptGroups.value = groups
      promptFriends.value = friends
    }
  } catch {
    // 已有内容不是合法 JSON，回退到源码模式让用户修复
    rawMode.value = true
    error.value = '现有内容不是合法 JSON，已切换到源码模式'
  }
}

function switchTab(name) {
  current.value = name
  saved.value = false
  rawMode.value = false
  load()
}

function toggleRaw() {
  if (!rawMode.value) {
    // 表单 -> 源码：先用当前表单内容刷新源码，校验失败则保留磁盘上的原文
    try {
      rawText.value = current.value === 'mcp' ? serializeMcp() : serializePrompt()
      error.value = ''
    } catch {
      /* 表单未通过校验，保留原始内容 */
    }
  }
  rawMode.value = !rawMode.value
}

function formatRaw() {
  error.value = ''
  if (rawText.value.trim() === '') return
  try {
    rawText.value = JSON.stringify(JSON.parse(rawText.value), null, 2)
  } catch {
    error.value = 'JSON 格式错误'
  }
}

async function save(content) {
  saving.value = true
  try {
    await api.saveFile(current.value, content)
    saved.value = true
  } catch (e) {
    error.value = e.message
    throw e
  } finally {
    saving.value = false
  }
}

async function onSave() {
  error.value = ''
  try {
    const content = current.value === 'mcp' ? serializeMcp() : serializePrompt()
    await save(content)
    rawText.value = content
  } catch (e) {
    if (!error.value) error.value = e.message
  }
}

async function onSaveRaw() {
  error.value = ''
  let content = rawText.value
  if (rawText.value.trim() !== '') {
    try {
      // Prompt 覆盖在源码模式下同样统一带前缀并逐条校验（群/好友都是）
      if (current.value === 'prompt') {
        content = normalizePromptContent(rawText.value)
      } else {
        JSON.parse(rawText.value)
      }
    } catch (e) {
      error.value = e instanceof SyntaxError ? 'JSON 格式错误' : (e.message || '保存失败')
      return
    }
  }
  try {
    await save(content)
    rawText.value = content
  } catch { /* error 已设置 */ }
}

function addServer() {
  mcpServers.value.push({
    name: '',
    transport: 'stdio',
    command: '',
    argsText: '',
    env: [],
    endpoint: '',
    headers: [],
    timeout: '',
    description: '',
  })
}

onMounted(load)
</script>
