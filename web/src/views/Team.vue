<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        Agent 团队按会话（群聊/私聊）隔离，由 AI 通过 team_create 等工具自行管理；在此可查看与修正，修改立即生效，无需重启
      </div>
      <div class="flex items-center gap-3">
        <Button type="text" size="small" @click="load">刷新</Button>
        <Button type="primary" size="small" @click="openCreate">新增团队</Button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ msg }}</p>

    <div class="flex flex-col lg:flex-row lg:items-start gap-4">
      <!-- 左栏：会话列表 -->
      <Card style="padding: 0" class="w-full lg:w-72 shrink-0 overflow-hidden">
        <div class="p-2">
          <div class="filter-group">
            <Button
              v-for="t in kindTabs"
              :key="t.value"
              size="small"
              :type="kindFilter === t.value ? 'primary' : 'text'"
              @click="kindFilter = t.value"
            >
              {{ t.label }}
            </Button>
          </div>
        </div>
        <ul class="divide-y divide-slate-100 max-h-128 overflow-y-auto">
          <li v-if="filteredScopes.length === 0" class="py-10 text-xs text-slate-400 text-center list-none px-4">
            暂无团队。可让 AI 在对话中调用 team_create 创建，也可点击右上角手动新增
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
              <span class="text-[11px] px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600 shrink-0">{{ s.count }} 个</span>
            </div>
            <div class="text-[11px] font-mono text-slate-400 mt-0.5">{{ s.scope }}</div>
          </li>
        </ul>
      </Card>

      <!-- 右栏：团队列表 -->
      <Card style="padding: 0" class="flex-1 min-w-0 overflow-hidden">
        <div v-if="current" class="px-5 py-3.5 border-b border-white/50 flex items-center justify-between gap-3">
          <div class="min-w-0">
            <span class="text-sm font-semibold text-slate-800">{{ displayName(current) }}</span>
            <span class="ml-2 text-[11px] font-mono text-slate-400">{{ current.scope }}</span>
          </div>
          <span class="text-xs text-slate-400 shrink-0">{{ teams.length }} 个团队</span>
        </div>
        <ul class="divide-y divide-slate-100">
          <li v-if="!current" class="py-12 text-sm text-slate-400 text-center list-none">
            从左侧选择一个会话查看其团队
          </li>
          <li v-else-if="teams.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
            该会话暂无团队
          </li>
          <li v-for="t in teams" :key="t.name" class="px-5 py-4 flex items-start gap-4">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 flex-wrap">
                <span class="text-sm font-semibold text-slate-800">{{ t.name }}</span>
                <span class="text-[11px] px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600">{{ t.members.length }} 名成员</span>
                <span v-if="t.created_at" class="text-[11px] text-slate-400">创建于 {{ fmtDate(t.created_at) }}</span>
              </div>
              <p v-if="t.desc" class="text-xs text-slate-500 mt-1.5">{{ t.desc }}</p>
              <ul class="mt-2.5 space-y-1.5">
                <li v-for="m in t.members" :key="m.name" class="flex items-start gap-2">
                  <span class="text-[11px] px-2 py-0.5 rounded-md bg-[#0071e3]/10 border border-zinc-200/60 font-mono text-zinc-700 shrink-0 mt-px">{{ m.name }}</span>
                  <span class="text-xs text-slate-500 leading-relaxed wrap-break-word">{{ m.role || '（无角色描述）' }}</span>
                </li>
              </ul>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <Button type="text" size="small" @click="openEdit(t)">编辑</Button>
              <Button type="text" size="small" danger @click="onDelete(t)">删除</Button>
            </div>
          </li>
        </ul>
      </Card>
    </div>

    <!-- 新增/编辑弹窗 -->
    <Modal
      variant="default"
      v-model:open="showForm"
      :title="form.name ? '编辑团队' : '新增团队'"
      :width="512"
      :typewriter="false"
      :show-footer="false"
    >
      <form class="w-full space-y-4" @submit.prevent="onSubmit">
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-slate-500 mb-1.5">团队名</label>
            <Input v-model="form.name" placeholder="如：开发小组" :disabled="!!form.originName" />
          </div>
          <div v-if="!form.originName">
            <label class="block text-xs text-slate-500 mb-1.5">会话 scope</label>
            <Input v-model="form.scope" placeholder="如 global、g:123456 或 g:fs:oc_xxx" />
          </div>
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">团队说明（可空）</label>
          <Input v-model="form.desc" placeholder="用途、适合的任务类型等" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">成员（1 至 10 个：可选预置角色自动填充名字，或用 name + 角色描述自定义；角色描述为空且名字命中预置角色时，该成员按预置角色执行）</label>
          <div class="space-y-2">
            <div v-for="(m, i) in form.members" :key="i" class="border border-white/60 rounded-lg p-2.5 space-y-2">
              <div class="flex flex-wrap items-center gap-2">
                <Select
                  :model-value="presetOf(m)"
                  :options="roleOptions"
                  class="w-44 shrink-0"
                  @change="onPresetChange(m, $event)"
                />
                <Input v-model="m.name" placeholder="成员名（选预置角色后自动填充）" />
                <Button
                  type="text"
                  size="small"
                  danger
                  :disabled="form.members.length <= 1"
                  @click="form.members.splice(i, 1)"
                >
                  移除
                </Button>
              </div>
              <Input v-model="m.role" placeholder="角色描述，如：负责代码审查（可空；填了则优先于预置角色生效）" />
            </div>
          </div>
          <Button
            type="dashed"
            size="small"
            class="mt-2"
            :disabled="form.members.length >= 10"
            @click="form.members.push({ name: '', role: '' })"
          >
            + 添加成员
          </Button>
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
import { Button, Card, Input, Modal, Select } from 'animal-island-vue'
import { api } from '../api.js'

const kindTabs = [
  { value: 'all', label: '全部' },
  { value: 'global', label: '全局' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const scopes = ref([])
const teams = ref([])
const current = ref(null)
const kindFilter = ref('all')
const names = ref({})
const roles = ref([])
const msg = ref('')
const msgOk = ref(false)

const showForm = ref(false)
const form = reactive({ scope: '', name: '', originName: '', desc: '', members: [], msg: '' })

const scopePattern = /^(global|[gf]:.+)$/

const filteredScopes = computed(() =>
  kindFilter.value === 'all' ? scopes.value : scopes.value.filter((s) => s.kind === kindFilter.value),
)

// 成员行预置角色下拉选项（Select 组件）：首项为「自定义」
const roleOptions = computed(() => [
  { key: '', label: '自定义' },
  ...roles.value.map((r) => ({ key: r.name, label: `${r.name} · ${r.summary}` })),
])

// 成员行预置角色下拉的当前值：成员名命中预置角色时显示该角色，否则显示「自定义」
function presetOf(m) {
  const r = roles.value.find((r) => r.name === m.name)
  return r ? r.name : ''
}

// 选中预置角色：自动填充成员名并清空角色描述（空角色描述时 team_run 按预置角色执行）
function onPresetChange(m, name) {
  if (name) {
    m.name = name
    m.role = ''
  }
}

function displayName(s) {
  if (names.value[s.scope]) return names.value[s.scope]
  if (s.scope === 'global') return '全局团队'
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
    scopes.value = (await api.getTeamScopes()) || []
    // 当前选中的 scope 可能已变化，同步其数量；不存在时清空选择
    if (current.value) {
      const found = scopes.value.find((s) => s.scope === current.value.scope)
      current.value = found || null
    }
    if (!current.value && scopes.value.length > 0) {
      await selectScope(scopes.value[0])
    } else if (current.value) {
      await loadTeams()
    } else {
      teams.value = []
    }
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

async function selectScope(s) {
  current.value = s
  await loadTeams()
}

async function loadTeams() {
  if (!current.value) return
  try {
    teams.value = (await api.getTeams(current.value.scope)) || []
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

function openCreate() {
  form.scope = current.value?.scope || ''
  form.name = ''
  form.originName = ''
  form.desc = ''
  form.members = [{ name: '', role: '' }]
  form.msg = ''
  showForm.value = true
}

function openEdit(t) {
  form.scope = current.value.scope
  form.name = t.name
  form.originName = t.name
  form.desc = t.desc || ''
  form.members = (t.members || []).map((m) => ({ name: m.name, role: m.role || '' }))
  if (form.members.length === 0) form.members = [{ name: '', role: '' }]
  form.msg = ''
  showForm.value = true
}

async function onSubmit() {
  form.msg = ''
  const scope = form.scope.trim()
  if (!scopePattern.test(scope)) {
    form.msg = '会话 scope 应为 global、g:会话ID 或 f:用户ID（如 g:fs:oc_xxx）'
    return
  }
  const team = {
    scope,
    name: form.name.trim(),
    desc: form.desc.trim(),
    members: form.members
      .map((m) => ({ name: m.name.trim(), role: m.role.trim() }))
      .filter((m) => m.name),
  }
  if (team.members.length === 0) {
    form.msg = '至少需要 1 名成员'
    return
  }
  try {
    if (form.originName) {
      await api.updateTeam(team)
    } else {
      await api.createTeam(team)
    }
    showForm.value = false
    msgOk.value = true
    msg.value = form.originName ? '团队已更新' : '团队已新增'
    await load()
    // 新增时若目标是未选中的 scope，切过去
    if (!form.originName && current.value?.scope !== scope) {
      const found = scopes.value.find((s) => s.scope === scope)
      if (found) await selectScope(found)
    }
  } catch (e) {
    form.msg = e.message
  }
}

async function onDelete(t) {
  if (!await confirmAction(`确定要删除团队「${t.name}」吗？`)) return
  msg.value = ''
  try {
    await api.deleteTeam(current.value.scope, t.name)
    msgOk.value = true
    msg.value = '团队已删除'
    await load()
  } catch (err) {
    msgOk.value = false
    msg.value = err.message
  }
}

onMounted(async () => {
  load()
  loadNames()
  // 预置角色列表（加载失败时成员行只有自定义选项，不影响其余功能）
  try {
    roles.value = (await api.getTeamRoles()) || []
  } catch { /* 功能未启用时忽略 */ }
})
</script>
