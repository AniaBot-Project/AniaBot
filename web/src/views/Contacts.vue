<template>
  <div class="space-y-5">
    <div v-if="error" class="bg-red-50 border border-red-200 text-red-700 text-sm rounded-xl px-4 py-3">{{ error }}</div>

    <div v-if="!sources.length && loaded" class="bg-white border border-white/60 rounded-xl px-5 py-8 text-center text-sm text-slate-500 shadow-sm">
      当前没有已启用的适配器支持通讯录
      <p class="mt-1 text-xs text-slate-400">Telegram、QQ 官方等平台没有联系人枚举接口，故不提供群/好友列表</p>
    </div>

    <template v-else>
      <!-- 平台标签页：一个适配器一枚 -->
      <div class="flex flex-wrap gap-2">
        <button
          v-for="s in sources"
          :key="s.adapter"
          class="px-3.5 py-1.5 text-sm rounded-lg border transition-colors"
          :class="adapter === s.adapter
            ? 'btn-accent font-medium shadow-sm'
            : 'bg-white text-slate-500 border-white/60 hover:text-slate-800 hover:border-slate-300/60'"
          @click="switchPlatform(s.adapter)"
        >
          {{ platformLabel(s.platform) }}
          <span class="text-xs opacity-60 ml-1">{{ s.adapter }}</span>
        </button>
      </div>

      <div class="flex items-center justify-between">
        <div class="flex gap-1 glass rounded-xl p-1">
          <button
            v-for="tab in [['groups', '群列表'], ['friends', '好友列表']]"
            :key="tab[0]"
            class="px-4 py-1.5 text-sm rounded-md transition-colors"
            :class="current === tab[0] ? 'btn-accent font-medium shadow-sm' : 'text-slate-500 hover:text-slate-800'"
            @click="switchTab(tab[0])"
          >
            {{ tab[1] }}
          </button>
        </div>
        <span class="text-xs text-slate-400">共 {{ current === 'groups' ? groups.length : friends.length }} 条</span>
      </div>

      <section class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
        <div v-if="current === 'groups'" class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-left text-xs text-slate-400 bg-slate-50/60 border-b border-white/50">
                <th class="px-3 py-3 sm:px-6 font-medium whitespace-nowrap">群 ID</th>
                <th class="px-3 py-3 sm:px-6 font-medium">群名称</th>
                <th class="px-3 py-3 sm:px-6 font-medium whitespace-nowrap">成员数</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="g in groups" :key="g.group_id" class="border-b border-slate-50 last:border-0 hover:bg-white/55 transition-colors">
                <td class="px-3 py-3 sm:px-6 text-slate-500 font-mono text-xs">{{ g.group_id }}</td>
                <td class="px-3 py-3 sm:px-6 text-slate-700 font-medium">{{ g.group_name || '—' }}</td>
                <td class="px-3 py-3 sm:px-6 text-slate-600 whitespace-nowrap">{{ memberText(g) }}</td>
              </tr>
              <tr v-if="!groups.length">
                <td colspan="3" class="px-6 py-8 text-center text-slate-400">该平台暂无群聊</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-else class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-left text-xs text-slate-400 bg-slate-50/60 border-b border-white/50">
                <th class="px-3 py-3 sm:px-6 font-medium whitespace-nowrap">用户 ID</th>
                <th class="px-3 py-3 sm:px-6 font-medium">昵称</th>
                <th class="px-3 py-3 sm:px-6 font-medium">备注</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="f in friends" :key="f.user_id" class="border-b border-slate-50 last:border-0 hover:bg-white/55 transition-colors">
                <td class="px-3 py-3 sm:px-6 text-slate-500 font-mono text-xs">{{ f.user_id }}</td>
                <td class="px-3 py-3 sm:px-6 text-slate-700 font-medium">{{ f.nickname || '—' }}</td>
                <td class="px-3 py-3 sm:px-6 text-slate-600">{{ f.remark || '—' }}</td>
              </tr>
              <tr v-if="!friends.length">
                <td colspan="3" class="px-6 py-8 text-center text-slate-400">该平台无好友列表（平台不支持枚举私聊对端）</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { api } from '../api.js'

const PLATFORM_LABELS = {
  qq: 'QQ',
  qqofficial: 'QQ 官方',
  feishu: '飞书',
  telegram: 'Telegram',
  discord: 'Discord',
}

const sources = ref([])
const adapter = ref('')
const current = ref('groups')
const groups = ref([])
const friends = ref([])
const error = ref('')
const loaded = ref(false)

function platformLabel(platform) {
  return PLATFORM_LABELS[platform] || platform
}

function memberText(g) {
  if (!g.member_count && !g.max_member_count) return '—'
  return `${g.member_count} / ${g.max_member_count}`
}

async function load() {
  if (!adapter.value) return
  error.value = ''
  try {
    if (current.value === 'groups') {
      groups.value = await api.getContacts(adapter.value, 'groups')
    } else {
      friends.value = await api.getContacts(adapter.value, 'friends')
    }
  } catch (e) {
    error.value = e.message
  }
}

function switchPlatform(name) {
  adapter.value = name
  groups.value = []
  friends.value = []
  load()
}

function switchTab(name) {
  current.value = name
  load()
}

onMounted(async () => {
  try {
    sources.value = await api.getContactSources()
    if (sources.value.length) {
      adapter.value = sources.value[0].adapter
      await load()
    }
  } catch (e) {
    error.value = e.message
  } finally {
    loaded.value = true
  }
})
</script>
