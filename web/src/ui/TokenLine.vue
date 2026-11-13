<template>
  <div class="token-line" :style="{ height }" role="img" :aria-label="ariaLabel">
    <svg class="token-line__svg" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient :id="gradId" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="#19c8b9" stop-opacity="0.3" />
          <stop offset="100%" stop-color="#19c8b9" stop-opacity="0.02" />
        </linearGradient>
      </defs>
      <line x1="0" y1="99.4" x2="100" y2="99.4" class="token-line__axis" />
      <template v-if="points.length > 1">
        <polygon :points="areaPoints" :fill="`url(#${gradId})`" />
        <polyline :points="linePoints" class="token-line__path" vector-effect="non-scaling-stroke" />
      </template>
    </svg>

    <!-- 每个采样点一条透明竖带：悬停用主题原生 Tooltip 显示数值，并在折线上点亮该点 -->
    <div v-if="points.length" class="token-line__hits">
      <Tooltip v-for="(p, i) in points" :key="i" :title="p.tip" :placement="placementFor(i)" class="token-line__hit">
        <span class="token-line__band">
          <span class="token-line__dot" :style="{ top: p.y + '%' }" />
        </span>
      </Tooltip>
    </div>
    <div v-else class="token-line__empty">SAMPLING…</div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { Tooltip } from 'animal-island-vue'

const props = defineProps({
  /** 数值序列（旧在前），第一个点在最左侧 */
  values: { type: Array, default: () => [] },
  height: { type: String, default: '3rem' },
  /** 纵轴归一化下限：低于该值的波动也会被放大，避免曲线贴着底边 */
  minMax: { type: Number, default: 10 },
  ariaLabel: { type: String, default: '折线图' },
  /** (value, index) => 悬停提示文案 */
  tip: { type: Function, default: null },
})

const gradId = `token-line-grad-${Math.random().toString(36).slice(2, 8)}`

// 上下各留一点余量，曲线不会贴边
const TOP = 4
const BOTTOM = 96

const maxValue = computed(() => {
  const values = props.values.map(v => Math.max(0, Number(v) || 0))
  return Math.max(props.minMax || 0, ...values, 1)
})

const points = computed(() => {
  const values = props.values.map(v => Math.max(0, Number(v) || 0))
  const n = values.length
  if (!n) return []
  const max = maxValue.value
  return values.map((v, i) => {
    const ratio = Math.min(1, v / max)
    return {
      // 首末点贴住左右边缘；只有一个点时居中
      x: n === 1 ? 50 : (i / (n - 1)) * 100,
      y: BOTTOM - ratio * (BOTTOM - TOP),
      tip: props.tip ? props.tip(v, i) : String(v),
    }
  })
})

const linePoints = computed(() => points.value.map(p => `${p.x.toFixed(2)},${p.y.toFixed(2)}`).join(' '))

const areaPoints = computed(() => {
  const pts = points.value
  if (pts.length < 2) return ''
  return `${pts[0].x.toFixed(2)},100 ${linePoints.value} ${pts[pts.length - 1].x.toFixed(2)},100`
})

// 首尾两个气泡靠边放，避免溢出卡片
function placementFor(i) {
  if (points.value.length <= 3) return 'top'
  if (i < 2) return 'top-start'
  if (i >= points.value.length - 2) return 'top-end'
  return 'top'
}
</script>

<style scoped>
.token-line {
  position: relative;
  width: 100%;
}

.token-line__svg {
  display: block;
  width: 100%;
  height: 100%;
}

.token-line__axis {
  stroke: var(--line-strong);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.token-line__path {
  fill: none;
  stroke: var(--accent);
  stroke-width: 2;
  stroke-linejoin: round;
  stroke-linecap: round;
}

.token-line__hits {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: stretch;
}

.token-line__hits :deep(.animal-tooltip) {
  flex: 1 1 0;
  min-width: 0;
  height: 100%;
}

.token-line__hits :deep(.animal-tooltip__trigger) {
  display: flex;
  width: 100%;
  height: 100%;
}

.token-line__band {
  position: relative;
  display: block;
  width: 100%;
  height: 100%;
  cursor: crosshair;
  transition: background-color 0.15s ease;
}

.token-line__band:hover {
  background: rgb(25 200 185 / 0.08);
}

.token-line__dot {
  position: absolute;
  left: 50%;
  display: block;
  width: 8px;
  height: 8px;
  margin-top: -4px;
  border: 2px solid #fffdf7;
  border-radius: 999px;
  background: var(--accent-strong);
  box-shadow: 0 1px 3px rgb(61 52 40 / 0.28);
  opacity: 0;
  transform: translateX(-50%);
  transition: opacity 0.15s ease;
  pointer-events: none;
}

.token-line__band:hover .token-line__dot {
  opacity: 1;
}

.token-line__empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--ink-disabled);
}
</style>
