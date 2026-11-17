<template>
  <div
    class="loading-state"
    :class="[`loading-state--${variant}`, { 'loading-state--compact': compact }]"
    role="status"
    aria-live="polite"
    aria-busy="true"
  >
    <div class="loading-state__head">
      <span class="loading-state__spinner" aria-hidden="true" />
      <span class="loading-state__text">{{ text }}</span>
    </div>
    <div v-if="rows > 0" class="loading-state__body" aria-hidden="true">
      <Skeleton
        v-for="n in rows"
        :key="n"
        loading
        variant="paragraph"
        :rows="lines"
        :row-widths="rowWidths"
        class="loading-state__group"
      />
    </div>
  </div>
</template>

<script setup>
import { Skeleton } from 'animal-island-vue'

// 面板各列表 / 日志页通用加载占位：转圈 + 文案 + 骨架屏，避免首次请求返回前
// 误显示「暂无数据」。延迟 120ms 淡入，加载很快时不会闪一下。
defineProps({
  /** 提示文案 */
  text: { type: String, default: '正在加载…' },
  /** 骨架屏分组数（0 = 只显示转圈与文案） */
  rows: { type: Number, default: 2 },
  /** 每组骨架屏的行数 */
  lines: { type: Number, default: 3 },
  /** 配色：light 浅色页面 / dark 控制台日志等深色背景 */
  variant: { type: String, default: 'light' },
  /** 紧凑模式：用于侧栏小列表 */
  compact: { type: Boolean, default: false },
})

// 每组骨架屏的行宽：短标题行 + 正文行 + 收尾行
const rowWidths = ['46%', '100%', '72%']
</script>

<style scoped>
.loading-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 16px;
  padding: 30px 24px;
  animation: loading-state-fade-in 0.2s ease 0.12s both;
}

.loading-state--compact {
  gap: 12px;
  padding: 18px 16px;
}

.loading-state__head {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--ink-muted);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.04em;
}

.loading-state__spinner {
  width: 14px;
  height: 14px;
  flex-shrink: 0;
  border-radius: 999px;
  border: 2px solid rgb(196 184 158 / 0.35);
  border-top-color: var(--accent);
  animation: loading-state-spin 0.8s linear infinite;
}

.loading-state__body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: 100%;
  max-width: 560px;
}

.loading-state__group {
  width: 100%;
}

.loading-state__group :deep(.animal-skeleton--line) {
  background: rgb(224 210 182 / 0.55);
}

.loading-state--compact .loading-state__body {
  max-width: none;
  gap: 8px;
}

/* 深色背景（控制台日志）：用半透明白色骨架，避免亮块刺眼 */
.loading-state--dark .loading-state__head {
  color: rgb(148 163 184);
}

.loading-state--dark .loading-state__spinner {
  border-color: rgb(148 163 184 / 0.25);
  border-top-color: #82d5bb;
}

.loading-state--dark .loading-state__group :deep(.animal-skeleton) {
  background: rgb(255 255 255 / 0.08);
}

.loading-state--dark .loading-state__group :deep(.animal-skeleton--line) {
  background: rgb(255 255 255 / 0.12);
}

@keyframes loading-state-spin {
  to {
    transform: rotate(360deg);
  }
}

@keyframes loading-state-fade-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

@media (prefers-reduced-motion: reduce) {
  .loading-state {
    animation: none;
  }

  .loading-state__spinner {
    animation: none;
  }
}
</style>
