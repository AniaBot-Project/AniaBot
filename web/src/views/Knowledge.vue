<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        知识库按作用域管理（全局 / 群聊 / 私聊），AI 对话时会检索相关文档；在此可查看与维护，修改立即生效，无需重启
      </div>
      <div class="flex items-center gap-3">
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
        <button
          class="text-xs bg-white text-zinc-700 px-3.5 py-2 rounded-lg border border-zinc-200 hover:bg-white/55 font-medium transition-colors shadow-sm"
          @click="openImport"
        >
          导入 URL
        </button>
        <button
          class="text-xs btn-accent px-3.5 py-2 rounded-lg font-medium transition-colors shadow-sm"
          @click="openCreate"
        >
          新增文档
        </button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-emerald-600' : 'text-red-600'">{{ msg }}</p>

    <div class="flex flex-col lg:flex-row lg:items-start gap-4">
      <!-- 左栏：作用域列表 -->
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
        <ul class="divide-y divide-slate-100 max-h-128 overflow-y-auto">
          <li v-if="filteredScopes.length === 0" class="py-10 text-xs text-slate-400 text-center list-none px-4">
            暂无文档。可在面板手动新增，或点击右上角导入 URL
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
              <span class="text-[11px] px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600 shrink-0">{{ s.count }} 篇</span>
            </div>
            <div class="text-[11px] font-mono text-slate-400 mt-0.5">{{ s.scope }}</div>
          </li>
        </ul>
      </section>

      <!-- 右栏：文档列表 -->
      <section class="flex-1 min-w-0 bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
        <div v-if="current" class="px-5 py-3.5 border-b border-white/50 flex items-center justify-between gap-3">
          <div class="min-w-0">
            <span class="text-sm font-semibold text-slate-800">{{ displayName(current) }}</span>
            <span class="ml-2 text-[11px] font-mono text-slate-400">{{ current.scope }}</span>
          </div>
          <span class="text-xs text-slate-400 shrink-0">{{ docs.length }} 篇文档</span>
        </div>
        <ul class="divide-y divide-slate-100">
          <li v-if="!current" class="py-12 text-sm text-slate-400 text-center list-none">
            从左侧选择一个作用域查看其文档
          </li>
          <li v-else-if="docs.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
            该作用域暂无文档
          </li>
          <li v-for="d in docs" :key="d.id" class="px-5 py-4 flex items-start gap-4">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="text-sm font-semibold text-slate-800 truncate">{{ d.title || '(无标题)' }}</span>
                <span v-if="d.source" class="text-[11px] font-mono text-slate-400 truncate max-w-[16rem]">{{ d.source }}</span>
              </div>
              <p class="text-sm text-slate-600 leading-relaxed whitespace-pre-wrap wrap-break-word mt-1.5">{{ preview(d.content) }}</p>
              <div class="flex items-center gap-1.5 mt-2 flex-wrap">
                <span class="text-[11px] font-mono text-slate-400">{{ d.id }}</span>
                <span v-for="t in d.tags" :key="t" class="text-[11px] px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600">{{ t }}</span>
                <span class="text-[11px] text-slate-400">记于 {{ fmtDate(d.created_at) }}</span>
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <button
                class="text-xs text-zinc-600 hover:text-zinc-900 hover:bg-white/60 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                @click="openEdit(d)"
              >
                编辑
              </button>
              <button
                class="text-xs text-red-500 hover:text-red-600 hover:bg-red-50 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                @click="onDelete(d)"
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
      <form class="bg-white rounded-2xl shadow-2xl p-6 w-136 max-w-full space-y-4" @submit.prevent="onSubmit">
        <h2 class="text-base font-semibold text-slate-800">{{ form.id ? '编辑文档' : '新增文档' }}</h2>
        <div v-if="!form.id">
          <label class="block text-xs text-slate-500 mb-1.5">作用域（下拉可选用已有作用域，或手输 global / g:会话ID / f:用户ID，支持前缀如 g:fs:oc_xxx）</label>
          <input v-model="form.scope" list="kb-scope-options" placeholder="如 global、g:123456 或 g:fs:oc_xxx" required :class="inputClass" />
          <datalist id="kb-scope-options">
            <option v-for="s in scopeSuggestions" :key="s" :value="s" />
          </datalist>
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">标题</label>
          <input v-model="form.title" placeholder="简短概括内容，如「Docker 部署指南」" :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">正文（完整资料/教程，最长 8000 字符）</label>
          <textarea v-model="form.content" rows="8" placeholder="粘贴或输入文档内容..." required :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">标签（逗号分隔，可空）</label>
          <input v-model="form.tagsText" placeholder="如：部署, docker" :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">来源（可空，如网页链接）</label>
          <input v-model="form.source" placeholder="如 https://example.com" :class="inputClass" />
        </div>
        <p v-if="form.msg" class="text-sm text-red-600">{{ form.msg }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="px-4 py-2 text-sm text-slate-600 hover:bg-white/60 rounded-lg transition-colors" @click="showForm = false">取消</button>
        <button type="submit" class="px-4 py-2 text-sm btn-accent rounded-lg transition-colors">保存</button>
        </div>
      </form>
    </div>

    <!-- 导入 URL 弹窗 -->
    <div v-if="showImport" class="fixed inset-0 bg-slate-900/45 backdrop-blur-md flex items-center justify-center z-50 p-4" v-backdrop-close="() => (showImport = false)">
      <form class="bg-white rounded-2xl shadow-2xl p-6 w-136 max-w-full space-y-4" @submit.prevent="onImport">
        <h2 class="text-base font-semibold text-slate-800">从 URL 导入</h2>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">作用域（下拉可选用已有作用域，或手输 global / g:会话ID / f:用户ID，支持前缀如 g:fs:oc_xxx）</label>
          <input v-model="importForm.scope" list="kb-scope-options" placeholder="如 global 或 g:123456" required :class="inputClass" />
          <datalist id="kb-scope-options">
            <option v-for="s in scopeSuggestions" :key="s" :value="s" />
          </datalist>
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">网页地址</label>
          <input v-model="importForm.url" placeholder="https://..." required :class="inputClass" />
          <p class="text-[11px] text-slate-400 mt-1.5">通过 Jina Reader 抓取正文入库，需要已配置 Jina AI Token</p>
        </div>
        <p v-if="importForm.msg" class="text-sm text-red-600">{{ importForm.msg }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="px-4 py-2 text-sm text-slate-600 hover:bg-white/60 rounded-lg transition-colors" @click="showImport = false">取消</button>
        <button type="submit" class="px-4 py-2 text-sm btn-accent rounded-lg transition-colors">导入</button>
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
  { value: 'global', label: '全局' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const scopes = ref([])
const docs = ref([])
const current = ref(null)
const kindFilter = ref('all')
const msg = ref('')
const msgOk = ref(false)

const showForm = ref(false)
const form = reactive({ scope: '', id: '', title: '', content: '', tagsText: '', source: '', msg: '' })

const showImport = ref(false)
const importForm = reactive({ scope: '', url: '', msg: '' })

const scopePattern = /^(global|[gf]:.+)$/

const filteredScopes = computed(() =>
  kindFilter.value === 'all' ? scopes.value : scopes.value.filter((s) => s.kind === kindFilter.value),
)

// 作用域下拉建议：全局 + 左侧已有的作用域（去重），手输新作用域仍被允许
const scopeSuggestions = computed(() => {
  const list = ['global']
  for (const s of scopes.value) {
    if (s.scope !== 'global' && !list.includes(s.scope)) list.push(s.scope)
  }
  return list
})

function displayName(s) {
  if (s.scope === 'global') return '全局知识库'
  return s.kind === 'group' ? `群 ${s.target ?? ''}` : `私聊 ${s.target ?? ''}`
}

function fmtDate(iso) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function preview(content) {
  if (!content) return ''
  return content.length > 240 ? content.slice(0, 240) + '…' : content
}

async function load() {
  try {
    scopes.value = (await api.getKnowledgeScopes()) || []
    if (current.value) {
      const found = scopes.value.find((s) => s.scope === current.value.scope)
      current.value = found || null
    }
    if (!current.value && scopes.value.length > 0) {
      await selectScope(scopes.value[0])
    } else if (current.value) {
      await loadDocs()
    } else {
      docs.value = []
    }
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

async function selectScope(s) {
  current.value = s
  await loadDocs()
}

async function loadDocs() {
  if (!current.value) return
  try {
    docs.value = (await api.getKnowledgeDocs(current.value.scope)) || []
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

function openCreate() {
  form.scope = current.value?.scope || 'global'
  form.id = ''
  form.title = ''
  form.content = ''
  form.tagsText = ''
  form.source = ''
  form.msg = ''
  showForm.value = true
}

function openEdit(d) {
  form.scope = current.value.scope
  form.id = d.id
  form.title = d.title || ''
  form.content = d.content
  form.tagsText = (d.tags || []).join(', ')
  form.source = d.source || ''
  form.msg = ''
  showForm.value = true
}

async function onSubmit() {
  form.msg = ''
  const scope = form.scope.trim()
  if (!scopePattern.test(scope)) {
    form.msg = '作用域应为 global、g:会话ID 或 f:用户ID（如 g:fs:oc_xxx）'
    return
  }
  const entry = {
    scope,
    id: form.id || undefined,
    title: form.title.trim(),
    content: form.content.trim(),
    tags: form.tagsText.split(/[,，]/).map((t) => t.trim()).filter(Boolean),
    source: form.source.trim(),
  }
  try {
    if (form.id) {
      await api.updateKnowledge(entry)
    } else {
      await api.createKnowledge(entry)
    }
    showForm.value = false
    msgOk.value = true
    msg.value = form.id ? '文档已更新' : '文档已新增'
    await load()
    if (!form.id && current.value?.scope !== scope) {
      const found = scopes.value.find((s) => s.scope === scope)
      if (found) await selectScope(found)
    }
  } catch (e) {
    form.msg = e.message
  }
}

async function onDelete(d) {
  if (!confirm(`确定要删除这篇文档吗？\n\n${d.title || '(无标题)'}`)) return
  msg.value = ''
  try {
    await api.deleteKnowledge(current.value.scope, d.id)
    msgOk.value = true
    msg.value = '文档已删除'
    await load()
  } catch (err) {
    msgOk.value = false
    msg.value = err.message
  }
}

function openImport() {
  importForm.scope = current.value?.scope || 'global'
  importForm.url = ''
  importForm.msg = ''
  showImport.value = true
}

async function onImport() {
  importForm.msg = ''
  const scope = importForm.scope.trim()
  if (!scopePattern.test(scope)) {
    importForm.msg = '作用域应为 global、g:会话ID 或 f:用户ID（如 g:fs:oc_xxx）'
    return
  }
  try {
    await api.importKnowledgeURL(scope, importForm.url.trim())
    showImport.value = false
    msgOk.value = true
    msg.value = 'URL 已导入知识库'
    await load()
    if (current.value?.scope !== scope) {
      const found = scopes.value.find((s) => s.scope === scope)
      if (found) await selectScope(found)
    }
  } catch (e) {
    importForm.msg = e.message
  }
}

onMounted(load)
</script>
