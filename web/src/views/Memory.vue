<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        AI 长期记忆按会话（群聊/私聊）隔离，由 AI 自行记录；在此可查看与修正，修改立即生效，无需重启
      </div>
      <div class="flex items-center gap-3">
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
        <button
          class="text-xs btn-accent px-3.5 py-2 rounded-lg font-medium transition-colors shadow-sm"
          @click="openCreate"
        >
          新增记忆
        </button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-emerald-600' : 'text-red-600'">{{ msg }}</p>

    <div class="flex flex-col lg:flex-row lg:items-start gap-4">
      <!-- 左栏：会话列表 -->
      <section class="w-full lg:w-72 shrink-0 bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
        <div class="p-2 border-b border-white/50">
          <div class="flex items-center gap-1 bg-slate-50 rounded-lg p-1">
            <button
              v-for="t in kindTabs"
              :key="t.value"
              class="flex-1 px-2 py-1.5 text-xs rounded-md transition-all"
              :class="kindFilter === t.value ? 'btn-accent font-medium shadow-sm' : 'text-slate-500 hover:text-slate-800'"
              @click="kindFilter = t.value"
            >
              {{ t.label }}
            </button>
          </div>
        </div>
        <ul class="divide-y divide-slate-100 max-h-[32rem] overflow-y-auto">
          <li v-if="filteredScopes.length === 0" class="py-10 text-xs text-slate-400 text-center list-none px-4">
            暂无记忆。AI 在对话中会自行记录，也可点击右上角手动新增
          </li>
          <li
            v-for="s in filteredScopes"
            :key="s.scope"
            class="px-4 py-3 cursor-pointer transition-colors"
            :class="current?.scope === s.scope ? 'bg-indigo-500/15' : 'hover:bg-white/55'"
            @click="selectScope(s)"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-sm font-medium text-slate-800 truncate">{{ displayName(s) }}</span>
              <span class="text-[11px] px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600 shrink-0">{{ s.count }} 条</span>
            </div>
            <div class="text-[11px] font-mono text-slate-400 mt-0.5">{{ s.scope }}</div>
          </li>
        </ul>
      </section>

      <!-- 右栏：记忆条目 -->
      <section class="flex-1 min-w-0 bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
        <div v-if="current" class="px-5 py-3.5 border-b border-white/50 flex items-center justify-between gap-3">
          <div class="min-w-0">
            <span class="text-sm font-semibold text-slate-800">{{ displayName(current) }}</span>
            <span class="ml-2 text-[11px] font-mono text-slate-400">{{ current.scope }}</span>
          </div>
          <span class="text-xs text-slate-400 shrink-0">{{ entries.length }} 条记忆</span>
        </div>
        <ul class="divide-y divide-slate-100">
          <li v-if="!current" class="py-12 text-sm text-slate-400 text-center list-none">
            从左侧选择一个会话查看其记忆
          </li>
          <li v-else-if="entries.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
            该会话暂无记忆
          </li>
          <li v-for="e in entries" :key="e.id" class="px-5 py-4 flex items-start gap-4">
            <div class="min-w-0 flex-1">
              <p class="text-sm text-slate-800 leading-relaxed whitespace-pre-wrap break-words">{{ e.content }}</p>
              <div class="flex items-center gap-1.5 mt-2 flex-wrap">
                <span class="text-[11px] font-mono text-slate-400">{{ e.id }}</span>
                <span v-if="e.user_id" class="text-[11px] px-2 py-0.5 rounded-full bg-indigo-500/10 text-zinc-500 border border-zinc-200/60 font-mono">{{ e.user_id }}</span>
                <span v-for="t in e.tags" :key="t" class="text-[11px] px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600">{{ t }}</span>
                <span class="text-[11px] text-slate-400">记于 {{ fmtDate(e.created_at) }}</span>
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <button
                class="text-xs text-zinc-600 hover:text-zinc-900 hover:bg-white/60 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                @click="openEdit(e)"
              >
                编辑
              </button>
              <button
                class="text-xs text-red-500 hover:text-red-600 hover:bg-red-50 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                @click="onDelete(e)"
              >
                删除
              </button>
            </div>
          </li>
        </ul>
      </section>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-slate-900/45 backdrop-blur-md flex items-center justify-center z-50 p-4" v-backdrop-close="() => (showForm = false)">
      <form class="bg-white rounded-2xl shadow-2xl p-6 w-[28rem] max-w-full space-y-4" @submit.prevent="onSubmit">
        <h2 class="text-base font-semibold text-slate-800">{{ form.id ? '编辑记忆' : '新增记忆' }}</h2>
        <div v-if="!form.id">
          <label class="block text-xs text-slate-500 mb-1.5">会话 scope（g:会话ID / f:用户ID）</label>
          <input v-model="form.scope" placeholder="如 g:123456 或 g:fs:oc_xxx" required :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">内容（一条完整自洽的事实）</label>
          <textarea v-model="form.content" rows="4" placeholder="如：群主小明讨厌被半夜@" required :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">关联用户 ID（可空，表示属于整个会话）</label>
          <input v-model="form.user_id" placeholder="用户 ID（QQ 数字 / LLBot lil:123456 / 飞书 fs:ou_xxx / Telegram tg:123456 / Discord dc:123456）" :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">标签（逗号分隔，可空）</label>
          <input v-model="form.tagsText" placeholder="如：偏好, 称呼" :class="inputClass" />
        </div>
        <p v-if="form.msg" class="text-sm text-red-600">{{ form.msg }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="px-4 py-2 text-sm text-slate-600 hover:bg-white/60 rounded-lg transition-colors" @click="showForm = false">取消</button>
        <button type="submit" class="px-4 py-2 text-sm btn-accent rounded-lg transition-colors">保存</button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { api } from '../api.js'

const inputClass = 'w-full border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400 transition-shadow'

const kindTabs = [
  { value: 'all', label: '全部' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const scopes = ref([])
const entries = ref([])
const current = ref(null)
const kindFilter = ref('all')
const names = ref({})
const msg = ref('')
const msgOk = ref(false)

const showForm = ref(false)
const form = reactive({ scope: '', id: '', user_id: '', content: '', tagsText: '', msg: '' })

const scopePattern = /^[gf]:.+$/

const filteredScopes = computed(() =>
  kindFilter.value === 'all' ? scopes.value : scopes.value.filter((s) => s.kind === kindFilter.value),
)

function displayName(s) {
  if (names.value[s.scope]) return names.value[s.scope]
  return s.kind === 'group' ? `群 ${s.target}` : `私聊 ${s.target}`
}

function fmtDate(iso) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

// 加载群/好友名称映射（失败时静默回退为显示号码）
async function loadNames() {
  names.value = await api.getContactNameMap()
}

async function load() {
  try {
    scopes.value = (await api.getMemoryScopes()) || []
    // 当前选中的 scope 可能已变化，同步其条数；不存在时清空选择
    if (current.value) {
      const found = scopes.value.find((s) => s.scope === current.value.scope)
      current.value = found || null
    }
    if (!current.value && scopes.value.length > 0) {
      await selectScope(scopes.value[0])
    } else if (current.value) {
      await loadEntries()
    } else {
      entries.value = []
    }
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

async function selectScope(s) {
  current.value = s
  await loadEntries()
}

async function loadEntries() {
  if (!current.value) return
  try {
    entries.value = (await api.getMemories(current.value.scope)) || []
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

function openCreate() {
  form.scope = current.value?.scope || ''
  form.id = ''
  form.user_id = ''
  form.content = ''
  form.tagsText = ''
  form.msg = ''
  showForm.value = true
}

function openEdit(e) {
  form.scope = current.value.scope
  form.id = e.id
  form.user_id = e.user_id || ''
  form.content = e.content
  form.tagsText = (e.tags || []).join(', ')
  form.msg = ''
  showForm.value = true
}

async function onSubmit() {
  form.msg = ''
  const scope = form.scope.trim()
  if (!scopePattern.test(scope)) {
    form.msg = '会话 scope 格式应为 g:会话ID 或 f:用户ID（如 g:fs:oc_xxx）'
    return
  }
  const entry = {
    scope,
    id: form.id || undefined,
    user_id: form.user_id.trim(),
    content: form.content.trim(),
    tags: form.tagsText.split(/[,，]/).map((t) => t.trim()).filter(Boolean),
  }
  try {
    if (form.id) {
      await api.updateMemory(entry)
    } else {
      await api.createMemory(entry)
    }
    showForm.value = false
    msgOk.value = true
    msg.value = form.id ? '记忆已更新' : '记忆已新增'
    await load()
    // 新增时若目标是未选中的 scope，切过去
    if (!form.id && current.value?.scope !== scope) {
      const found = scopes.value.find((s) => s.scope === scope)
      if (found) await selectScope(found)
    }
  } catch (e) {
    form.msg = e.message
  }
}

async function onDelete(e) {
  if (!confirm(`确定要删除这条记忆吗？\n\n${e.content}`)) return
  msg.value = ''
  try {
    await api.deleteMemory(current.value.scope, e.id)
    msgOk.value = true
    msg.value = '记忆已删除'
    await load()
  } catch (err) {
    msgOk.value = false
    msg.value = err.message
  }
}

onMounted(() => {
  load()
  loadNames()
})
</script>
