<template>
  <div class="space-y-6">
    <!-- 头部 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <span class="text-xs text-zinc-500">
        按每会话与全局两个维度限制每日 Token 消耗，超限后 AI 请求被拒绝；计数含子代理、定时任务的消耗。在「配置管理 → AI 对话 · 配额」启用
      </span>
      <div class="flex items-center gap-3">
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
        <button
          class="text-xs btn-accent px-3.5 py-2 rounded-lg font-medium transition-colors shadow-sm"
          @click="resetAll"
        >
          全部清零
        </button>
      </div>
    </div>

    <!-- 操作反馈 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ msg }}</p>

    <!-- 未启用 -->
    <section v-if="notEnabled" class="tcard p-12 text-center">
      <div class="tlabel mb-2">Quota · 配额限制</div>
      <p class="text-sm text-zinc-500">
        配额功能未启用，请在「配置管理」中设置 plugin.ai_chat_bot.quota.enable=true 并重启 Bot 后使用
      </p>
    </section>

    <template v-else>
      <!-- 全局总览 -->
      <section class="tcard p-6">
        <div class="flex items-center justify-between flex-wrap gap-x-8 gap-y-3">
          <div>
            <span class="tlabel">Global · 全局今日用量</span>
            <div class="mt-1.5 text-3xl font-semibold tracking-tight text-zinc-900">{{ fmt(global.used) }}</div>
          </div>
          <div>
            <span class="tlabel">全局上限</span>
            <div class="mt-1.5 text-sm text-zinc-600">{{ global.limit ? fmt(global.limit) : '不限' }}</div>
          </div>
          <div>
            <span class="tlabel">剩余</span>
            <div class="mt-1.5 text-sm text-zinc-600">{{ global.limit ? fmt(global.remaining) : '—' }}</div>
          </div>
          <div class="ml-auto">
            <span class="tpill">
              <span class="tdot" :class="global.reached ? 'bg-[#ff3b30]' : 'bg-[#34c759]'" />
              {{ global.reached ? '已用尽' : '正常' }}
            </span>
          </div>
        </div>
        <div v-if="global.limit > 0" class="mt-4 h-1.5 bg-zinc-100 rounded-full overflow-hidden">
          <div
            class="h-full transition-all"
            :class="global.reached ? 'bg-[#ff3b30]' : 'bg-zinc-800'"
            :style="{ width: pct(global.used, global.limit) + '%' }"
          />
        </div>
        <div class="tlabel mt-3">{{ data.date }}</div>
      </section>

      <!-- 会话明细 -->
      <section class="tcard overflow-hidden">
        <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between">
          <span class="tlabel">Sessions · 会话用量</span>
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">{{ sessions.length }} 个会话</span>
        </div>
        <div v-if="sessions.length === 0" class="py-12 text-sm text-zinc-400 text-center">
          今日还没有 AI 调用记录
        </div>
        <ul v-else class="divide-y divide-zinc-100">
          <li v-for="s in sessions" :key="s.key" class="px-6 py-4 flex items-center gap-x-8 gap-y-2 flex-wrap">
            <div class="w-52 shrink-0 min-w-0">
              <div class="text-sm font-medium text-zinc-800 break-all">{{ s.kind === 'group' ? '群聊' : '私聊' }} {{ s.target }}</div>
              <div class="text-[11px] font-mono text-zinc-400 mt-0.5 break-all">{{ s.key }}</div>
            </div>
            <div class="flex-1 min-w-44">
              <div class="flex justify-between text-[10px] tracking-[0.12em] uppercase text-zinc-400 mb-1">
                <span>已用 {{ fmt(s.used) }} / {{ s.limit ? fmt(s.limit) : '不限' }}</span>
                <span>{{ s.limit ? '剩余 ' + fmt(s.remaining) : '' }}</span>
              </div>
              <div v-if="s.limit > 0" class="h-1 bg-zinc-100 rounded-full overflow-hidden">
                <div
                  class="h-full"
                  :class="s.reached ? 'bg-[#ff3b30]' : 'bg-zinc-800'"
                  :style="{ width: pct(s.used, s.limit) + '%' }"
                />
              </div>
            </div>
            <span class="tpill shrink-0">
              <span class="tdot" :class="s.reached ? 'bg-[#ff3b30]' : 'bg-[#34c759]'" />
              {{ s.reached ? '已用尽' : '正常' }}
            </span>
            <button
              class="text-[11px] text-zinc-500 hover:text-[#ff3b30] font-medium transition-colors shrink-0"
              @click="resetOne(s.key)"
            >
              清零
            </button>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { api } from '../api.js'

const data = ref({})
const notEnabled = ref(false)
const msg = ref('')
const msgOk = ref(true)

// 后端返回扁平的 global_used / global_limit / ... 字段，这里映射成模板用的结构
const global = computed(() => ({
  used: data.value.global_used || 0,
  limit: data.value.global_limit || 0,
  remaining: data.value.global_remaining || 0,
  reached: data.value.global_reached || false,
}))
const sessions = computed(() => data.value.sessions || [])

function fmt(n) {
  return Number(n || 0).toLocaleString()
}

function pct(used, limit) {
  if (!limit) return 0
  return Math.min(100, Math.round((used / limit) * 100))
}

async function load() {
  try {
    data.value = await api.getQuota()
    notEnabled.value = false
  } catch {
    data.value = {}
    notEnabled.value = true
  }
}

async function doReset(scope, label) {
  if (!window.confirm(`确定清零${label}的今日用量？`)) return
  try {
    await api.resetQuota(scope)
    msg.value = '已清零'
    msgOk.value = true
    await load()
  } catch (e) {
    msg.value = e.message || '清零失败'
    msgOk.value = false
  }
}

const resetAll = () => doReset('all', '全部会话')
const resetOne = (key) => doReset(key, `会话 ${key}`)

onMounted(load)
</script>
