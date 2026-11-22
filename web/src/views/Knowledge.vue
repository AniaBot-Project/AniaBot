<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        知识库按作用域管理（全局 / 群聊 / 私聊），AI 对话时会检索相关文档；在此可查看与维护，修改立即生效，无需重启
      </div>
      <div class="flex items-center gap-3">
        <Button type="text" size="small" @click="load">刷新</Button>
        <Button type="primary" size="small" @click="openCreate">新增文档</Button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ msg }}</p>

    <div class="flex flex-col lg:flex-row lg:items-start gap-4">
      <!-- 左栏：作用域列表 -->
      <Card style="padding: 0" class="w-full lg:w-72 shrink-0 overflow-hidden">
        <div class="p-2 border-b border-white/50">
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
        <ul class="divide-y divide-slate-100 max-h-128 overflow-y-auto">
          <!-- 首次加载：先显示加载中，避免请求返回前误判为没有文档 -->
          <li v-if="loading && !scopes.length" class="list-none">
            <LoadingState text="正在加载知识库…" :rows="2" compact />
          </li>
          <li v-else-if="filteredScopes.length === 0" class="py-10 text-xs text-slate-400 text-center list-none px-4">
            暂无文档。点击右上角「新增文档」手动添加
          </li>
          <li
            v-for="s in filteredScopes"
            :key="s.scope"
            class="px-4 py-3 cursor-pointer transition-colors"
            :class="current?.scope === s.scope ? 'bg-[#0071e3]/12' : 'hover:bg-white/55'"
            @click="selectScope(s)"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-sm font-medium text-slate-800 truncate">{{ displayName(s) }}</span>
              <Tag variant="soft" size="small" class="shrink-0">{{ s.count }} 篇</Tag>
            </div>
            <div class="text-[11px] font-mono text-slate-400 mt-0.5">{{ s.scope }}</div>
          </li>
        </ul>
      </Card>

      <!-- 右栏：文档列表 -->
      <Card style="padding: 0" class="flex-1 min-w-0 overflow-hidden">
        <div v-if="current" class="px-5 py-3.5 border-b border-white/50 flex items-center justify-between gap-3">
          <div class="min-w-0">
            <span class="text-sm font-semibold text-slate-800">{{ displayName(current) }}</span>
            <span class="ml-2 text-[11px] font-mono text-slate-400">{{ current.scope }}</span>
          </div>
          <div class="flex items-center gap-3 shrink-0">
            <label v-if="docs.length" class="flex items-center gap-1.5 text-[11px] text-slate-500 cursor-pointer select-none">
              <input
                type="checkbox"
                :checked="allSelected"
                :indeterminate.prop="someSelected"
                :disabled="docsLoading"
                @change="toggleAll($event)"
              />
              全选
            </label>
            <span class="text-xs text-slate-400">{{ docsLoading ? '加载中…' : docs.length + ' 篇文档' }}</span>
          </div>
        </div>
        <!-- 多选操作栏（有勾选时显示） -->
        <div
          v-if="selectedCount > 0"
          class="px-5 py-2 border-b border-white/50 bg-[#0071e3]/8 flex items-center justify-between gap-3 flex-wrap"
        >
          <span class="text-xs text-slate-600">
            已选 <span class="font-semibold">{{ selectedCount }}</span> 篇文档
          </span>
          <div class="flex items-center gap-1.5">
            <Button type="text" size="small" :disabled="deleting" @click="clearSelection">取消选择</Button>
            <Button
              type="default"
              size="small"
              danger
              :disabled="deleting"
              :loading="deleting"
              @click="onDeleteSelected"
            >删除所选</Button>
          </div>
        </div>
        <ul class="divide-y divide-slate-100">
          <li v-if="!current && loading" class="list-none">
            <LoadingState text="正在加载文档…" :rows="2" compact />
          </li>
          <li v-else-if="!current" class="py-12 text-sm text-slate-400 text-center list-none">
            从左侧选择一个作用域查看其文档
          </li>
          <li v-else-if="docsLoading" class="list-none">
            <LoadingState text="正在加载文档…" :rows="2" compact />
          </li>
          <li v-else-if="docs.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
            该作用域暂无文档
          </li>
          <li v-for="d in docs" :key="d.id" class="px-5 py-4 flex items-start gap-3">
            <input
              type="checkbox"
              class="mt-1 shrink-0 cursor-pointer"
              :checked="selected.has(d.id)"
              :aria-label="'选择 ' + (d.title || '文档')"
              title="选中以批量删除"
              @change="toggleSelect(d.id)"
            />
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="text-sm font-semibold text-slate-800 truncate">{{ d.title || '(无标题)' }}</span>
                <span v-if="d.source" class="text-[11px] font-mono text-slate-400 truncate max-w-[16rem]">{{ d.source }}</span>
              </div>
              <p class="text-sm text-slate-600 leading-relaxed whitespace-pre-wrap wrap-break-word mt-1.5">{{ preview(d.content) }}</p>
              <div class="flex items-center gap-1.5 mt-2 flex-wrap">
                <span class="text-[11px] font-mono text-slate-400">{{ d.id }}</span>
                <Tag v-for="t in d.tags" :key="t" variant="soft" size="small">{{ t }}</Tag>
                <span class="text-[11px] text-slate-400">记于 {{ fmtDate(d.created_at) }}</span>
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <Button type="text" size="small" @click="openEdit(d)">编辑</Button>
              <Button type="default" size="small" danger @click="onDelete(d)">删除</Button>
            </div>
          </li>
        </ul>
      </Card>
    </div>

    <!-- 新增/编辑弹窗 -->
    <Modal
      variant="default"
      v-model:open="showForm"
      :title="form.id ? '编辑文档' : '新增文档'"
      :typewriter="false"
      :show-footer="false"
      :width="544"
    >
      <form class="w-full space-y-4" @submit.prevent="onSubmit">
        <div v-if="!form.id">
          <label class="block text-xs text-slate-500 mb-1.5">作用域（手输 global / g:会话ID / f:用户ID，支持前缀如 g:fs:oc_xxx）</label>
          <Input v-model="form.scope" placeholder="如 global、g:123456 或 g:fs:oc_xxx" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">标题</label>
          <Input v-model="form.title" placeholder="简短概括内容，如「Docker 部署指南」" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">正文（完整资料/教程，最长 8000 字符）</label>
          <textarea v-model="form.content" rows="8" placeholder="粘贴或输入文档内容..." required :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">标签（逗号分隔，可空）</label>
          <Input v-model="form.tagsText" placeholder="如：部署, docker" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">来源（可空，如网页链接）</label>
          <Input v-model="form.source" placeholder="如 https://example.com" />
        </div>
        <p v-if="form.msg" class="text-sm text-[#ff3b30]">{{ form.msg }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <Button type="default" @click="showForm = false">取消</Button>
          <Button type="primary" html-type="submit">保存</Button>
        </div>
      </form>
    </Modal>
  </div>
</template>

<script setup>
import { confirmAction } from '../ui/confirm'
import { computed, onMounted, reactive, ref } from 'vue'
import { Button, Card, Input, Modal, Tag } from 'animal-island-vue'
import { api } from '../api.js'
import LoadingState from '../components/LoadingState.vue'

const inputClass = 'w-full border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30 focus:border-[#0071e3]/50 transition-shadow'

const kindTabs = [
  { value: 'all', label: '全部' },
  { value: 'global', label: '全局' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const scopes = ref([])
const docs = ref([])
const current = ref(null)
// 首次加载 / 切换作用域时显示加载中，而不是「暂无文档」
const loading = ref(true)
const docsLoading = ref(false)
const kindFilter = ref('all')
const msg = ref('')
const msgOk = ref(false)
// 多选删除：选中项只在当前作用域内有效，切换作用域时清空，列表刷新后剔除已删除的 ID
const selected = ref(new Set())
const deleting = ref(false)

const showForm = ref(false)
const form = reactive({ scope: '', id: '', title: '', content: '', tagsText: '', source: '', msg: '' })

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

// ---------- 多选删除 ----------
const selectedCount = computed(() => docs.value.filter((d) => selected.value.has(d.id)).length)
const allSelected = computed(() => docs.value.length > 0 && docs.value.every((d) => selected.value.has(d.id)))
const someSelected = computed(() => !allSelected.value && docs.value.some((d) => selected.value.has(d.id)))

function toggleSelect(id) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleAll(e) {
  selected.value = e.target.checked ? new Set(docs.value.map((d) => d.id)) : new Set()
}

function clearSelection() {
  selected.value = new Set()
}

// 列表刷新后剔除已不存在的选中项（文档被删除或换了作用域）
function pruneSelection() {
  const ids = new Set(docs.value.map((d) => d.id))
  const next = new Set([...selected.value].filter((id) => ids.has(id)))
  if (next.size !== selected.value.size) selected.value = next
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
      clearSelection()
    }
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  } finally {
    loading.value = false
  }
}

async function selectScope(s) {
  // 选中项只属于当前作用域，切换时清空，避免把别的作用域的 ID 带过去
  clearSelection()
  current.value = s
  await loadDocs()
}

async function loadDocs() {
  if (!current.value) return
  const scope = current.value.scope
  docsLoading.value = true
  // 切换作用域时先清空旧文档，避免把上一个作用域的内容显示在新作用域下
  docs.value = []
  try {
    docs.value = (await api.getKnowledgeDocs(scope)) || []
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  } finally {
    docsLoading.value = false
    pruneSelection()
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
  if (!await confirmAction(`确定要删除这篇文档吗？\n\n${d.title || '(无标题)'}`)) return
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

// 批量删除当前作用域中勾选的文档：一次请求删除，返回实际删除条数
async function onDeleteSelected() {
  if (!current.value || selectedCount.value === 0 || deleting.value) return
  const count = selectedCount.value
  if (!await confirmAction({ title: '批量删除', message: `确定要删除选中的 ${count} 篇文档吗？删除后不可恢复。`, confirmText: '删除' })) return
  msg.value = ''
  deleting.value = true
  try {
    const res = await api.deleteKnowledgeDocs(current.value.scope, [...selected.value])
    clearSelection()
    msgOk.value = true
    msg.value = `已删除 ${res.deleted ?? count} 篇文档`
    await load()
  } catch (err) {
    msgOk.value = false
    msg.value = err.message
  } finally {
    deleting.value = false
  }
}

onMounted(load)
</script>
