<template>
  <div class="space-y-5">
    <Card v-if="error" color="app-red">
      <p class="text-sm">{{ error }}</p>
    </Card>

    <Card v-if="!sources.length && loaded" class="text-center">
      <div class="text-sm text-slate-500">当前没有已启用的适配器支持通讯录</div>
      <p class="mt-1 text-xs text-slate-400">Telegram、QQ 官方等平台没有联系人枚举接口，故不提供群/好友列表</p>
    </Card>

    <template v-else>
      <!-- 平台切换：一个适配器一枚按钮 -->
      <div class="flex flex-wrap gap-2">
        <Button
          v-for="s in sources"
          :key="s.adapter"
          size="small"
          :type="adapter === s.adapter ? 'primary' : 'default'"
          @click="switchPlatform(s.adapter)"
        >
          {{ platformLabel(s.platform) }}
          <span class="text-xs opacity-60 ml-1">{{ s.adapter }}</span>
        </Button>
      </div>

      <!-- 群聊 / 好友 列表：页签 + 表格 -->
      <Card style="padding: 0" class="overflow-hidden">
        <Tabs :model-value="current" :items="contactTabs" @update:modelValue="switchTab">
          <template #groups>
            <div class="flex items-center justify-end mb-3 text-xs text-slate-400">共 {{ groups.length }} 条</div>
            <Table
              key="groups"
              :columns="groupColumns"
              :data-source="groups"
              row-key="group_id"
              :pagination="false"
              :scroll="{ x: '100%' }"
              empty-text="该平台暂无群聊"
            >
              <template #cell-group_id="{ value }">
                <span class="font-mono text-xs">{{ value }}</span>
              </template>
              <template #cell-group_name="{ value }">
                {{ value || '—' }}
              </template>
              <template #cell-member_count="{ record }">
                {{ memberText(record) }}
              </template>
            </Table>
          </template>
          <template #friends>
            <div class="flex items-center justify-end mb-3 text-xs text-slate-400">共 {{ friends.length }} 条</div>
            <Table
              key="friends"
              :columns="friendColumns"
              :data-source="friends"
              row-key="user_id"
              :pagination="false"
              :scroll="{ x: '100%' }"
              empty-text="该平台无好友列表（平台不支持枚举私聊对端）"
            >
              <template #cell-user_id="{ value }">
                <span class="font-mono text-xs">{{ value }}</span>
              </template>
              <template #cell-nickname="{ value }">
                {{ value || '—' }}
              </template>
              <template #cell-remark="{ value }">
                {{ value || '—' }}
              </template>
            </Table>
          </template>
        </Tabs>
      </Card>
    </template>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Button, Card, Table, Tabs } from 'animal-island-vue'
import { api } from '../api.js'

const PLATFORM_LABELS = {
  qq: 'QQ',
  qqofficial: 'QQ 官方',
  feishu: '飞书',
  telegram: 'Telegram',
  discord: 'Discord',
}

// 群 / 好友页签与表格列定义（Tabs / Table 组件）
const contactTabs = [
  { key: 'groups', label: '群列表' },
  { key: 'friends', label: '好友列表' },
]
const groupColumns = [
  { title: '群 ID', dataIndex: 'group_id' },
  { title: '群名称', dataIndex: 'group_name' },
  { title: '成员数', dataIndex: 'member_count' },
]
const friendColumns = [
  { title: '用户 ID', dataIndex: 'user_id' },
  { title: '昵称', dataIndex: 'nickname' },
  { title: '备注', dataIndex: 'remark' },
]

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
