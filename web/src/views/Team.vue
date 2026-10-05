<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        Agent 团队按会话（群聊/私聊）隔离，由 AI 通过 team_create 等工具自行管理；在此可查看与修正，修改立即生效，无需重启
      </div>
      <div class="flex items-center gap-3">
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
        <button
          class="text-xs btn-accent px-3.5 py-2 rounded-lg font-medium transition-colors shadow-sm"
          @click="openCreate"
        >
          新增团队
        </button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ msg }}</p>

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
      </section>

      <!-- 右栏：团队列表 -->
      <section class="flex-1 min-w-0 bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
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
              <button
                class="text-xs text-zinc-600 hover:text-zinc-900 hover:bg-white/60 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                @click="openEdit(t)"
              >
                编辑
              </button>
              <button
                class="text-xs text-[#ff3b30] hover:text-[#ff3b30] hover:bg-red-50 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
                @click="onDelete(t)"
              >
                删除
              </button>
            </div>
          </li>
        </ul>
      </section>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4" v-backdrop-close="() => (showForm = false)">
      <form class="bg-white rounded-2xl shadow-2xl p-6 w-lg max-w-full space-y-4" @submit.prevent="onSubmit">
        <h2 class="text-base font-semibold text-slate-800">{{ form.name ? '编辑团队' : '新增团队' }}</h2>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-slate-500 mb-1.5">团队名</label>
            <input v-model="form.name" placeholder="如：开发小组" required :disabled="!!form.originName" :class="[inputClass, form.originName && 'bg-slate-50 text-slate-400']" />
          </div>
          <div v-if="!form.originName">
            <label class="block text-xs text-slate-500 mb-1.5">会话 scope</label>
            <input v-model="form.scope" list="team-scope-options" placeholder="如 global、g:123456 或 g:fs:oc_xxx" required :class="inputClass" />
            <datalist id="team-scope-options">
              <option v-for="s in scopeOptions" :key="s" :value="s" />
            </datalist>
          </div>
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">团队说明（可空）</label>
          <input v-model="form.desc" placeholder="用途、适合的任务类型等" :class="inputClass" />
        </div>
        <div>
          <label class="block text-xs text-slate-500 mb-1.5">成员（1 至 10 个：可选预置角色自动填充名字，或用 name + 角色描述自定义；角色描述为空且名字命中预置角色时，该成员按预置角色执行）</label>
          <div class="space-y-2">
            <div v-for="(m, i) in form.members" :key="i" class="border border-white/60 rounded-lg p-2.5 space-y-2">
              <div class="flex flex-wrap items-center gap-2">
                <select :value="presetOf(m)" class="w-44 shrink-0 border border-slate-300/60 rounded-lg px-2 py-2 text-sm bg-white focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30" @change="onPresetChange(m, $event)">
                  <option value="">自定义</option>
                  <option v-for="r in roles" :key="r.name" :value="r.name" :title="r.summary">
                    {{ r.name }} · {{ r.summary }}
                  </option>
                </select>
                <input v-model="m.name" placeholder="成员名（选预置角色后自动填充）" required :class="inputClass" />
                <button
                  type="button"
                  class="text-xs text-red-400 hover:text-[#ff3b30] px-2 py-1.5 rounded-lg hover:bg-red-50 font-medium shrink-0 transition-colors"
                  :disabled="form.members.length <= 1"
                  @click="form.members.splice(i, 1)"
                >
                  移除
                </button>
              </div>
              <input v-model="m.role" placeholder="角色描述，如：负责代码审查（可空；填了则优先于预置角色生效）" :class="inputClass" />
            </div>
          </div>
          <button
            type="button"
            class="mt-2 text-xs text-zinc-600 hover:text-zinc-900 hover:bg-white/60 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
            :disabled="form.members.length >= 10"
            @click="form.members.push({ name: '', role: '' })"
          >
            + 添加成员
          </button>
        </div>
        <p v-if="form.msg" class="text-sm text-[#ff3b30]">{{ form.msg }}</p>
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

const inputClass = 'w-full border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30 focus:border-[#0071e3]/50 transition-shadow'

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

// 新建团队的作用域建议：全局 + 左侧已有的作用域（去重），手输新作用域仍被允许
const scopeOptions = computed(() => {
  const list = ['global']
  for (const s of scopes.value) {
    if (s.scope !== 'global' && !list.includes(s.scope)) list.push(s.scope)
  }
  return list
})

// 成员行预置角色下拉的当前值：成员名命中预置角色时显示该角色，否则显示「自定义」
function presetOf(m) {
  const r = roles.value.find((r) => r.name === m.name)
  return r ? r.name : ''
}

// 选中预置角色：自动填充成员名并清空角色描述（空角色描述时 team_run 按预置角色执行）
function onPresetChange(m, event) {
  const name = event.target.value
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
  if (!confirm(`确定要删除团队「${t.name}」吗？`)) return
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
