<template>
  <div class="page-tabs">
    <nav class="page-tabs-bar" aria-label="页面页签">
      <RouterLink
        v-for="t in tabs"
        :key="t.to"
        :to="t.to"
        class="page-tab"
        :class="{ 'page-tab-active': isActive(t.to) }"
        :aria-current="isActive(t.to) ? 'page' : undefined"
      >
        {{ t.label }}
      </RouterLink>
    </nav>
    <p v-if="hint" class="page-tabs-hint">{{ hint }}</p>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'

// 合并页的分组页签：同一组页面共用「侧边栏一个入口 + 页内页签」。
// 页签走路由跳转（不做组件内切换），所以旧链接、书签与深链（?task= / ?task_id= 等）仍然有效。
const props = defineProps({
  // [{ to: '/msglogs', label: '消息日志', hint: '页签说明（可选，默认显示当前页签的）' }]
  tabs: { type: Array, required: true },
})

const route = useRoute()

const activeTab = computed(() => props.tabs.find((t) => t.to === route.path) || null)
const hint = computed(() => activeTab.value?.hint || '')

function isActive(to) {
  return route.path === to
}
</script>

<style scoped>
.page-tabs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 12px;
}

/* 页签条：无外框、只保留页签本身，和页面里的筛选分段按钮（有奶油小框）区分开 */
.page-tabs-bar {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  max-width: 100%;
  overflow-x: auto;
  scrollbar-width: none;
}
.page-tabs-bar::-webkit-scrollbar {
  display: none;
}

.page-tab {
  display: inline-flex;
  align-items: center;
  padding: 4px 14px;
  border-radius: 999px;
  font-size: 12.5px;
  font-weight: 700;
  line-height: 1.5;
  white-space: nowrap;
  color: var(--ink-muted);
  transition: background-color 0.15s, color 0.15s, box-shadow 0.15s;
}
.page-tab:hover {
  color: var(--ink-body);
  background: var(--fill-hover);
}

/* 当前页签：与侧边栏选中项同一套主题色 pill */
.page-tab-active,
.page-tab-active:hover {
  color: var(--nav-active-ink);
  background: var(--accent);
  box-shadow: 0 2px 0 0 var(--accent-strong);
}

.page-tabs-hint {
  min-width: 0;
  font-size: 11px;
  line-height: 1.4;
  color: var(--ink-muted);
}

@media (max-width: 640px) {
  .page-tabs-hint {
    display: none;
  }
}
</style>
