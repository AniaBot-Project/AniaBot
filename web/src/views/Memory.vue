<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        AI 长期记忆按会话（群聊/私聊）隔离，由 AI 自行记录；在此可查看与修正，修改立即生效，无需重启
      </div>
      <div class="flex items-center gap-3">
        <Button type="text" size="small" @click="load">刷新</Button>
        <Button type="primary" size="small" @click="openCreate">新增记忆</Button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ msg }}</p>

    <div class="flex flex-col lg:flex-row lg:items-start gap-4">
      <!-- 左栏：会话列表 -->
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
        <ul class="divide-y divide-slate-100 max-h-[32rem] overflow-y-auto">
          <!-- 首次加载：先显示加载中，避免请求返回前误判为没有记忆 -->
          <li v-if="loading && !scopes.length" class="list-none">
            <LoadingState text="正在加载记忆列表…" :rows="2" compact />
          </li>
          <li v-else-if="filteredScopes.length === 0" class="py-10 text-xs text-slate-400 text-center list-none px-4">
            暂无记忆。AI 在对话中会自行记录，也可点击右上角手动新增
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
              <Tag variant="soft" size="small" class="shrink-0">{{ s.count }} 条</Tag>
            </div>
            <div class="text-[11px] font-mono text-slate-400 mt-0.5">{{ s.scope }}</div>
          </li>
        </ul>
      </Card>

      <!-- 右栏：记忆条目 -->
      <Card style="padding: 0" class="flex-1 min-w-0 overflow-hidden">
        <div v-if="current" class="px-5 py-3.5 border-b border-white/50 flex items-center justify-between gap-3">
          <div class="min-w-0">
            <span class="text-sm font-semibold text-slate-800">{{ displayName(current) }}</span>
            <span class="ml-2 text-[11px] font-mono text-slate-400">{{ current.scope }}</span>
          </div>
          <div class="flex items-center gap-3 shrink-0">
            <label v-if="entries.length" class="flex items-center gap-1.5 text-[11px] text-slate-500 cursor-pointer select-none">
              <input
                type="checkbox"
                :checked="allSelected"
                :indeterminate.prop="someSelected"
                :disabled="entriesLoading"
                @change="toggleAll($event)"
              />
              全选
            </label>
            <span class="text-xs text-slate-400">{{ entriesLoading ? '加载中…' : entries.length + ' 条记忆' }}</span>
          </div>
        </div>
        <!-- 多选操作栏（有勾选时显示） -->
        <div
          v-if="selectedCount > 0"
          class="px-5 py-2 border-b border-white/50 bg-[#0071e3]/8 flex items-center justify-between gap-3 flex-wrap"
        >
          <span class="text-xs text-slate-600">
            已选 <span class="font-semibold">{{ selectedCount }}</span> 条记忆
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
            <LoadingState text="正在加载记忆…" :rows="2" compact />
          </li>
          <li v-else-if="!current" class="py-12 text-sm text-slate-400 text-center list-none">
            从左侧选择一个会话查看其记忆
          </li>
          <li v-else-if="entriesLoading" class="list-none">
            <LoadingState text="正在加载记忆…" :rows="2" compact />
          </li>
          <li v-else-if="entries.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
            该会话暂无记忆
          </li>
          <li v-for="e in entries" :key="e.id" class="px-5 py-4 flex items-start gap-3">
            <input
              type="checkbox"
              class="mt-1 shrink-0 cursor-pointer"
              :checked="selected.has(e.id)"
              :aria-label="'选择记忆 ' + e.id"
              title="选中以批量删除"
              @change="toggleSelect(e.id)"
            />
            <div class="min-w-0 flex-1">
              <p class="text-sm text-slate-800 leading-relaxed whitespace-pre-wrap break-words">{{ e.content }}</p>
              <div class="flex items-center gap-1.5 mt-2 flex-wrap">
                <span class="text-[11px] font-mono text-slate-400">{{ e.id }}</span>
                <Tag v-if="e.user_id" variant="soft" size="small" color="app-blue">{{ e.user_id }}</Tag>
                <Tag v-for="t in e.tags" :key="t" variant="soft" size="small">{{ t }}</Tag>
                <span class="text-[11px] text-slate-400">记于 {{ fmtDate(e.created_at) }}</span>
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <Button type="text" size="small" @click="openEdit(e)">编辑</Button>
              <Button type="default" size="small" danger @click="onDelete(e)">删除</Button>
            </div>
          </li>
        </ul>
      </Card>
    </div>

    <!-- 新增/编辑弹窗 -->
    <Modal
      variant="default"
      v-model:open="showForm"
      :title="form.id ? '编辑记忆' : '新增记忆'"
      :typewriter="false"
      :show-footer="false"
      :width="448"
    >
      <form class="w-full space-y-4" @submit.prevent="onSubmit">
        <div v-if="!form.id">
          <label class="block text-xs text-slate-500 mb-1.5">会话 scope（g:会话ID / f:用户ID）</label>
          <Input v-model="form.scope" placeholder="如 g:123456 或 g:fs:oc_xxx" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">内容（一条完整自洽的事实）</label>
          <textarea v-model="form.content" rows="4" placeholder="如：群主小明讨厌被半夜@" required :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">关联用户 ID（可空，表示属于整个会话）</label>
          <Input v-model="form.user_id" placeholder="用户 ID（QQ 数字 / LLBot lil:123456 / 飞书 fs:ou_xxx / Telegram tg:123456 / Discord dc:123456）" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">标签（逗号分隔，可空）</label>
          <Input v-model="form.tagsText" placeholder="如：偏好, 称呼" />
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
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const scopes = ref([])
const entries = ref([])
const current = ref(null)
// 首次加载 / 切换会话时显示加载中，而不是「暂无记忆」
const loading = ref(true)
const entriesLoading = ref(false)
const kindFilter = ref('all')
const names = ref({})
const msg = ref('')
const msgOk = ref(false)
// 多选删除：选中项只在当前会话内有效，切换会话时清空，列表刷新后剔除已删除的 ID
const selected = ref(new Set())
const deleting = ref(false)

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

// ---------- 多选删除 ----------
const selectedCount = computed(() => entries.value.filter((e) => selected.value.has(e.id)).length)
const allSelected = computed(() => entries.value.length > 0 && entries.value.every((e) => selected.value.has(e.id)))
const someSelected = computed(() => !allSelected.value && entries.value.some((e) => selected.value.has(e.id)))

function toggleSelect(id) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleAll(e) {
  selected.value = e.target.checked ? new Set(entries.value.map((e) => e.id)) : new Set()
}

function clearSelection() {
  selected.value = new Set()
}

// 列表刷新后剔除已不存在的选中项（记忆被删除或换了会话）
function pruneSelection() {
  const ids = new Set(entries.value.map((e) => e.id))
  const next = new Set([...selected.value].filter((id) => ids.has(id)))
  if (next.size !== selected.value.size) selected.value = next
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
  // 选中项只属于当前会话，切换时清空，避免把别的会话的 ID 带过去
  clearSelection()
  current.value = s
  await loadEntries()
}

async function loadEntries() {
  if (!current.value) return
  const scope = current.value.scope
  entriesLoading.value = true
  // 切换会话时先清空旧条目，避免把上一个会话的记忆显示在新会话下
  entries.value = []
  try {
    entries.value = (await api.getMemories(scope)) || []
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  } finally {
    entriesLoading.value = false
    pruneSelection()
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
  if (!await confirmAction(`确定要删除这条记忆吗？\n\n${e.content}`)) return
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

// 批量删除当前会话中勾选的记忆：一次请求删除，返回实际删除条数
async function onDeleteSelected() {
  if (!current.value || selectedCount.value === 0 || deleting.value) return
  const count = selectedCount.value
  if (!await confirmAction({ title: '批量删除', message: `确定要删除选中的 ${count} 条记忆吗？删除后不可恢复。`, confirmText: '删除' })) return
  msg.value = ''
  deleting.value = true
  try {
    const res = await api.deleteMemories(current.value.scope, [...selected.value])
    clearSelection()
    msgOk.value = true
    msg.value = `已删除 ${res.deleted ?? count} 条记忆`
    await load()
  } catch (err) {
    msgOk.value = false
    msg.value = err.message
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  load()
  loadNames()
})
</script>
