<template>
  <div class="space-y-5 max-w-4xl">
    <!-- 运行模式 / 配置提示 -->
    <div v-if="info && info.mode === 'dev'" class="tcard p-4 border-l-2 border-l-amber-400 flex items-start gap-3">
      <span class="tdot bg-[#ff9f0a] mt-1.5 shrink-0" />
      <div class="text-xs text-zinc-600 leading-relaxed">
        当前为 <span class="font-semibold text-zinc-900">go run 开发模式</span>运行，自动更新已禁用。
        请以编译后的二进制方式部署后再使用此功能。
      </div>
    </div>
    <div v-else-if="info && !info.configured" class="tcard p-4 border-l-2 border-l-amber-400 flex items-start gap-3">
      <span class="tdot bg-[#ff9f0a] mt-1.5 shrink-0" />
      <div class="text-xs text-zinc-600 leading-relaxed">
        尚未配置源码目录。请先在
        <RouterLink to="/config" class="font-semibold text-zinc-900 underline underline-offset-2">配置管理</RouterLink>
        的「自动更新」分组中设置 <span class="font-mono">bot.update.source_dir</span>（AniaBot 仓库的克隆路径）。
      </div>
    </div>

    <!-- 版本信息 -->
    <div class="tcard p-5">
      <div class="flex items-center justify-between mb-4">
        <span class="tlabel">Version / 版本信息</span>
        <span v-if="info && info.updateAvailable" class="tpill"><span class="tdot bg-[#34c759]" />有新版本</span>
        <span v-else-if="info && info.remoteCommit" class="tpill"><span class="tdot bg-zinc-300" />已是最新</span>
      </div>
      <div v-if="info" class="grid grid-cols-2 md:grid-cols-4 gap-4">
        <div>
          <div class="tlabel mb-1">当前 Commit</div>
          <div class="font-mono text-sm text-zinc-900">{{ info.currentCommit || '—' }}</div>
        </div>
        <div>
          <div class="tlabel mb-1">远端 Commit</div>
          <div class="font-mono text-sm text-zinc-900">{{ info.remoteCommit || '—' }}</div>
          <div v-if="info.remoteError" class="text-[10px] text-[#ff3b30] mt-1">{{ info.remoteError }}</div>
        </div>
        <div>
          <div class="tlabel mb-1">跟踪分支</div>
          <div class="font-mono text-sm text-zinc-900">{{ info.branch }}</div>
        </div>
        <div>
          <div class="tlabel mb-1">源码目录</div>
          <div class="font-mono text-[11px] text-zinc-500 break-all">{{ info.sourceDir || '未配置' }}</div>
        </div>
      </div>
      <!-- 环境检测 -->
      <div v-if="info" class="mt-4 pt-4 border-t border-zinc-100 flex flex-wrap gap-x-5 gap-y-1.5">
        <span v-for="t in envTools" :key="t.key" class="flex items-center gap-1.5 text-[11px]">
          <span class="tdot" :class="info.env[t.key] ? 'bg-[#34c759]' : 'bg-red-400'" />
          <span class="text-zinc-500 uppercase tracking-wider">{{ t.key }}</span>
          <span class="font-mono text-zinc-700">{{ info.env[t.key] || '未安装' }}</span>
        </span>
      </div>
      <div v-if="info && info.needClone" class="mt-4 pt-4 border-t border-zinc-100 flex items-start gap-2 text-[11px] text-[#ff9f0a]">
        <span class="tdot bg-[#ff9f0a] mt-1 shrink-0" />
        源码目录为空或不存在，开始更新时将自动从 git 地址克隆仓库
      </div>
      <div v-if="info && info.dirError" class="mt-4 pt-4 border-t border-zinc-100 flex items-start gap-2 text-[11px] text-[#ff3b30]">
        <span class="tdot bg-red-400 mt-1 shrink-0" />
        {{ info.dirError }}
      </div>
    </div>

    <!-- 操作 -->
    <div class="flex items-center gap-3">
      <button
        class="text-[10px] tracking-[0.15em] uppercase btn-accent px-3 py-1.5 rounded-md font-medium transition-colors disabled:opacity-50"
        :disabled="!canUpdate"
        @click="onStart"
      >开始更新</button>
      <button
        class="text-[10px] tracking-[0.15em] uppercase text-zinc-500 hover:text-zinc-900 font-medium transition-colors disabled:opacity-50"
        :disabled="infoLoading || status.running"
        @click="loadInfo"
      >{{ infoLoading ? '检查中...' : '检查更新' }}</button>
      <span v-if="startMsg" class="text-xs text-[#ff3b30]">{{ startMsg }}</span>
    </div>

    <!-- 失败原因 -->
    <div v-if="status.error" class="tcard p-4 border-l-2 border-l-red-400">
      <div class="flex items-center gap-2 mb-1.5">
        <span class="tpill"><span class="tdot bg-red-400" />{{ status.errKind || '更新失败' }}</span>
      </div>
      <p class="text-xs text-[#ff3b30] font-mono break-all leading-relaxed">{{ status.error }}</p>
      <p class="text-[10px] text-zinc-400 mt-2">更新已中止，当前运行的版本未受影响。修复问题后可重新点击「开始更新」。</p>
    </div>

    <!-- 进度 -->
    <div v-if="started" class="tcard p-5">
      <div class="tlabel mb-4">Progress / 更新进度</div>
      <div v-if="status.restarting" class="mb-3 text-xs text-zinc-500">更新完成，等待 Bot 重启...</div>
      <div class="flex flex-wrap items-center gap-y-2 mb-5">
        <template v-for="(p, i) in phases" :key="p.key">
          <div class="flex items-center gap-1.5">
            <span
              class="w-4 h-4 rounded-full border flex items-center justify-center text-[9px] font-mono"
              :class="phaseClass(p.key)"
            >{{ phaseDone(p.key) ? '✓' : i + 1 }}</span>
            <span class="text-[10px] tracking-widest uppercase" :class="phaseTextClass(p.key)">{{ p.label }}</span>
          </div>
          <span v-if="i < phases.length - 1" class="mx-2 h-px w-5 bg-zinc-200" />
        </template>
      </div>
      <div
        ref="logEl"
        class="bg-slate-950/85 rounded-xl p-3 h-72 overflow-y-auto font-mono text-[11px] leading-relaxed text-slate-300 border border-white/10 shadow-inner"
      >
        <div v-for="(l, i) in status.logs" :key="i" :class="logLineClass(l)">{{ l }}</div>
        <div v-if="status.running" class="flex items-center gap-2 text-zinc-500 mt-1">
          <span class="w-3 h-3 border-2 border-zinc-700 border-t-zinc-300 rounded-full animate-spin" />
          执行中...
        </div>
      </div>
    </div>

    <!-- 重启中遮罩 -->
    <div v-if="rebooting" class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4">
      <div class="tcard p-8 w-80 max-w-full text-center space-y-3">
        <span class="mx-auto block w-8 h-8 border-[3px] border-zinc-200 border-t-zinc-800 rounded-full animate-spin" />
        <div class="text-sm font-semibold text-zinc-900 tracking-[0.15em] uppercase">Updating</div>
        <p class="text-xs text-zinc-500">更新完成，Bot 正在重启，恢复后页面自动刷新</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { api } from '../api.js'

const phases = [
  { key: 'env', label: '环境检查' },
  { key: 'fetch', label: '拉取代码' },
  { key: 'deps', label: '拉取依赖' },
  { key: 'web', label: '构建前端' },
  { key: 'build', label: '编译' },
  { key: 'swap', label: '替换二进制' },
]
const phaseOrder = phases.map((p) => p.key)

const envTools = [
  { key: 'git' }, { key: 'go' }, { key: 'node' }, { key: 'npm' },
]

const info = ref(null)
const infoLoading = ref(false)
const startMsg = ref('')
const started = ref(false)      // 本页面观察到过更新任务在运行
const rebooting = ref(false)
const logEl = ref(null)
const status = reactive({ running: false, restarting: false, phase: '', logs: [], error: '', errKind: '' })

const canUpdate = computed(() =>
  info.value
  && info.value.mode === 'binary'
  && info.value.configured
  && !info.value.dirError
  && !(info.value.needClone && !info.value.gitUrl)
  && !status.running
  && !status.restarting
  && !rebooting.value,
)

let timer = null
onMounted(() => {
  loadInfo()
  pollStatus()
  timer = setInterval(() => {
    if (!document.hidden) pollStatus()
  }, 1500)
})
onUnmounted(() => clearInterval(timer))

async function loadInfo() {
  infoLoading.value = true
  try {
    info.value = await api.getUpdateInfo()
  } catch (e) {
    startMsg.value = e.message
  } finally {
    infoLoading.value = false
  }
}

async function pollStatus() {
  try {
    const s = await api.getUpdateStatus()
    const wasRunning = status.running
    Object.assign(status, s)
    if (s.running) started.value = true
    // 观察过运行中的任务进入 done → 等待重启完成
    if (started.value && (wasRunning || s.phase === 'done') && !s.running && s.phase === 'done' && !rebooting.value) {
      waitReboot()
    }
    nextTick(scrollLog)
  } catch { /* 服务可能正在重启 */ }
}

async function onStart() {
  startMsg.value = ''
  if (!confirm('确定要开始更新吗？将拉取最新代码、重新编译并重启 Bot，过程约需几分钟。')) return
  try {
    await api.startUpdate()
    started.value = true
    await pollStatus()
  } catch (e) {
    startMsg.value = e.message
  }
}

async function waitReboot() {
  rebooting.value = true
  const up = await api.waitUntilUp(120000)
  if (up) {
    location.reload()
  } else {
    rebooting.value = false
    alert('等待重启超时，请检查 Bot 运行状态')
  }
}

function scrollLog() {
  if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
}

function phaseIndex(key) {
  if (key === 'done' || key === 'restart') return phaseOrder.length
  return phaseOrder.indexOf(key)
}
function phaseDone(key) {
  return phaseIndex(status.phase) > phaseIndex(key)
}
function phaseClass(key) {
  if (status.error && status.phase === key) return 'border-red-400 text-[#ff3b30]'
  if (phaseDone(key)) return 'border-[#34c759]/40 bg-[#34c759] text-white'
  if (status.phase === key) return 'border-zinc-900 text-zinc-900'
  return 'border-zinc-300 text-zinc-400'
}
function phaseTextClass(key) {
  if (status.error && status.phase === key) return 'text-[#ff3b30] font-semibold'
  if (status.phase === key && status.running) return 'text-zinc-900 font-semibold'
  if (phaseDone(key)) return 'text-zinc-600'
  return 'text-zinc-400'
}
function logLineClass(l) {
  if (l.startsWith('✗')) return 'text-red-400'
  if (l.startsWith('==')) return 'text-zinc-100 font-semibold mt-2'
  if (l.startsWith('$')) return 'text-zinc-500'
  return ''
}
</script>
