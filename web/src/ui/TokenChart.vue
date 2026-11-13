<template>
  <div class="token-chart">
    <!-- 柱体：主题库没有图表组件，这里用它的 Progress 填充色 / 圆角胶囊语言画柱体，
         悬停提示直接用原生 Tooltip（奶油气泡、支持多行），不再用浏览器 title。 -->
    <div class="token-chart__plot" :style="{ height }" role="img" :aria-label="ariaLabel">
      <Tooltip
        v-for="(item, i) in items"
        :key="item.key"
        :title="item.tip"
        :placement="placementFor(i)"
        class="token-chart__col"
      >
        <span class="token-chart__track" :class="{ 'token-chart__track--solid': track }">
          <span
            v-for="seg in segmentsOf(item)"
            :key="seg.key"
            class="token-chart__seg"
            :class="[`is-${toneOf(seg.key)}`, { 'is-top': seg.top }]"
            :style="{ height: seg.pct + '%' }"
          />
        </span>
      </Tooltip>
    </div>

    <div v-if="legend && series.length > 1" class="token-chart__legend">
      <Tag v-for="s in series" :key="s.key" variant="soft" size="small">
        <span class="token-chart__swatch" :class="`is-${s.tone}`" />{{ s.label }}
      </Tag>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { Tag, Tooltip } from 'animal-island-vue'

const props = defineProps({
  /** 每一列：{ key, tip, values: { [seriesKey]: number } } */
  items: { type: Array, default: () => [] },
  /** 系列：{ key, label, tone }，tone 取 primary（薄荷）| amber（鹅黄）| clay（暖棕） */
  series: { type: Array, default: () => [] },
  height: { type: String, default: '10rem' },
  /** 非零数据的最小可见高度（百分比），避免小值完全看不见 */
  minPct: { type: Number, default: 1.5 },
  /** 单系列时给柱子一层浅色底槽（对齐主题 Progress 的轨道观感） */
  track: { type: Boolean, default: false },
  legend: { type: Boolean, default: true },
  ariaLabel: { type: String, default: '柱状图' },
})

const maxTotal = computed(() => {
  let max = 0
  for (const item of props.items) {
    let sum = 0
    for (const s of props.series) sum += Math.max(0, Number(item.values?.[s.key]) || 0)
    if (sum > max) max = sum
  }
  return max
})

function toneOf(key) {
  return props.series.find(s => s.key === key)?.tone || 'primary'
}

// 堆叠柱：高度按「该列合计 / 全图最大列合计」归一。加最小可见高度后若超过 100%，
// 等比压回 100%，保证不会顶出绘图区。
function segmentsOf(item) {
  const max = maxTotal.value
  const values = props.series.map(s => Math.max(0, Number(item.values?.[s.key]) || 0))
  if (!max) return props.series.map(s => ({ key: s.key, pct: 0, top: false }))

  let pcts = values.map(v => (v > 0 ? Math.max(props.minPct, (v / max) * 100) : 0))
  const sum = pcts.reduce((a, b) => a + b, 0)
  if (sum > 100) pcts = pcts.map(p => (p / sum) * 100)

  let topIdx = -1
  for (let i = pcts.length - 1; i >= 0; i--) {
    if (pcts[i] > 0) {
      topIdx = i
      break
    }
  }
  return props.series.map((s, i) => ({
    key: s.key,
    pct: Number(pcts[i].toFixed(2)),
    top: i === topIdx,
  }))
}

// 首尾两列的气泡靠边放，避免溢出卡片
function placementFor(i) {
  if (props.items.length <= 3) return 'top'
  if (i < 2) return 'top-start'
  if (i >= props.items.length - 2) return 'top-end'
  return 'top'
}
</script>

<style scoped>
.token-chart {
  width: 100%;
}

.token-chart__plot {
  display: flex;
  align-items: stretch;
  gap: 2px;
  width: 100%;
}

/* Tooltip 根节点是 inline-flex 的 span，这里让每个柱子均分列宽并撑满绘图区 */
.token-chart__plot :deep(.animal-tooltip) {
  flex: 1 1 0;
  min-width: 0;
  height: 100%;
}

.token-chart__plot :deep(.animal-tooltip__trigger) {
  display: flex;
  width: 100%;
  height: 100%;
}

.token-chart__track {
  display: flex;
  flex-direction: column-reverse;
  justify-content: flex-start;
  width: 100%;
  height: 100%;
  border-radius: 8px;
  transition: background-color 0.18s ease;
}

/* 单系列：浅奶油底槽 + 细描边，呼应主题 Progress 的轨道 */
.token-chart__track--solid {
  background: #f7f3df;
  box-shadow: inset 0 0 0 1.5px #ece0c9;
}

.token-chart__col:hover .token-chart__track {
  background: rgb(25 200 185 / 0.09);
}

.token-chart__seg {
  display: block;
  flex: 0 0 auto;
  width: 100%;
  min-height: 0;
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.45);
}

.token-chart__seg.is-top {
  border-radius: 7px 7px 3px 3px;
}

.token-chart__seg.is-primary {
  background: linear-gradient(180deg, #3dd4c6, #19c8b9 60%, #14b6a8);
}

.token-chart__seg.is-amber {
  background: linear-gradient(180deg, #ffe08a, #f7cd67 70%, #eebd4a);
}

.token-chart__seg.is-clay {
  background: linear-gradient(180deg, #c0a577, #a88c62 70%, #9a835a);
}

.token-chart__legend {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}

.token-chart__swatch {
  display: inline-block;
  width: 8px;
  height: 8px;
  margin-right: 6px;
  border-radius: 3px;
  vertical-align: -1px;
}

.token-chart__swatch.is-primary {
  background: linear-gradient(180deg, #3dd4c6, #14b6a8);
}

.token-chart__swatch.is-amber {
  background: linear-gradient(180deg, #ffe08a, #eebd4a);
}

.token-chart__swatch.is-clay {
  background: linear-gradient(180deg, #c0a577, #9a835a);
}
</style>
