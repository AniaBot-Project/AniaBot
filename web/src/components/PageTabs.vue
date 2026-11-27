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
/* 页签切换器：凹槽式分段控件——两个页签在同一个「凹槽」里，当前项是实心主题色药丸，
   一眼能看出「这里可以切换页面」。说明文字靠右对齐，不和页签读成一句话。 */
.page-tabs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
}

.page-tabs-bar {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  max-width: 100%;
  padding: 3px;
  border: 1.5px solid var(--line-strong);
  border-radius: 999px;
  background: var(--surface-2);
  box-shadow: inset 0 1px 2px rgb(61 52 40 / 0.08);
  overflow-x: auto;
  scrollbar-width: none;
}
.page-tabs-bar::-webkit-scrollbar {
  display: none;
}

.page-tab {
  display: inline-flex;
  align-items: center;
  padding: 5px 18px;
  border-radius: 999px;
  font-size: 13.5px;
  font-weight: 800;
  line-height: 1.5;
  letter-spacing: 0.01em;
  white-space: nowrap;
  color: var(--ink-soft);
  transition: background-color 0.15s, color 0.15s, box-shadow 0.15s;
}
.page-tab:hover {
  color: var(--ink);
  background: var(--surface);
}

/* 当前页签：实心主题色药丸 + 抬起一档（与侧边栏选中项同一套观感） */
.page-tab-active,
.page-tab-active:hover {
  color: var(--nav-active-ink);
  background: var(--accent);
  box-shadow: 0 2px 0 0 var(--accent-strong), inset 0 1px 0 rgb(255 255 255 / 0.35);
}

.page-tabs-hint {
  min-width: 0;
  margin-left: auto;
  font-size: 11px;
  line-height: 1.4;
  text-align: right;
  color: var(--ink-muted);
}

@media (max-width: 767px) {
  .page-tabs {
    gap: 6px;
  }
  .page-tab {
    padding: 5px 14px;
  }
  .page-tabs-hint {
    display: none;
  }
}
</style>
