<template>
  <div class="space-y-6">
    <!-- 说明 + 复位 -->
    <div class="flex items-start justify-between gap-3 flex-wrap">
      <p class="text-xs text-zinc-500 leading-relaxed max-w-3xl">
        调整面板的配色与外观，选中即时生效。偏好只保存在当前浏览器（localStorage），不写入 Bot 配置、不需要重启，也不影响其他人的浏览器。
      </p>
      <Button type="text" size="small" @click="onReset">恢复默认</Button>
    </div>

    <!-- 预设主题 -->
    <Card style="padding: 20px 24px">
      <div class="flex items-baseline justify-between gap-3 flex-wrap">
        <span class="tlabel">Presets · 预设主题</span>
        <span class="text-[11px] text-zinc-400">一键切换整套配色；圆角与动效保持不变</span>
      </div>
      <div class="preset-grid mt-4">
        <button
          v-for="p in THEME_PRESETS"
          :key="p.id"
          type="button"
          class="preset-tile"
          :class="{ 'preset-tile--on': isThemeActive(p) }"
          :aria-pressed="isThemeActive(p)"
          @click="onPickTheme(p)"
        >
          <span class="preset-thumb" :style="{ background: p.page }">
            <span class="preset-thumb__side" :style="{ background: p.side }" />
            <span class="preset-thumb__main">
              <span class="preset-thumb__card" />
              <span class="preset-thumb__card preset-thumb__card--short" />
              <span class="preset-thumb__bar" :style="{ background: p.accent }" />
            </span>
          </span>
          <span class="preset-meta">
            <span class="preset-name">{{ p.name }}</span>
            <span class="preset-desc">{{ p.desc }}</span>
          </span>
          <span v-if="isThemeActive(p)" class="tile-check">✓</span>
        </button>
      </div>
    </Card>

    <!-- 主题色 -->
    <Card style="padding: 20px 24px">
      <div class="flex items-baseline justify-between gap-3 flex-wrap">
        <span class="tlabel">Accent · 主题色</span>
        <span class="text-[11px] text-zinc-400">导航选中项、链接、进度条、Tabs、日期选择都会跟着变</span>
      </div>
      <div class="swatch-row mt-4">
        <button
          v-for="c in ACCENT_PRESETS"
          :key="c.value"
          type="button"
          class="swatch"
          :class="{ 'swatch--on': sameAccent(c.value) }"
          :style="{ background: c.value, color: c.ink }"
          :title="c.name"
          :aria-label="c.name"
          :aria-pressed="sameAccent(c.value)"
          @click="onPickAccent(c.value)"
        >
          <span v-if="sameAccent(c.value)" class="swatch-check">✓</span>
        </button>
      </div>
      <div class="mt-4 flex items-center gap-3 flex-wrap">
        <label class="accent-picker" :style="{ borderColor: appearance.accent }">
          <input type="color" :value="appearance.accent" aria-label="自定义主题色" @input="onCustomColor" />
          <span class="accent-picker__dot" :style="{ background: appearance.accent }" />
          <span>自定义颜色</span>
        </label>
        <input
          class="hex-input"
          :value="hexText"
          maxlength="7"
          spellcheck="false"
          aria-label="主题色十六进制值"
          @input="onHexInput"
          @blur="onHexCommit"
          @keyup.enter="onHexCommit"
        />
        <span class="text-[11px] text-zinc-400">当前主题色 {{ appearance.accent }}</span>
      </div>
    </Card>

    <!-- 页面背景 -->
    <Card style="padding: 20px 24px">
      <div class="flex items-baseline justify-between gap-3 flex-wrap">
        <span class="tlabel">Canvas · 页面背景</span>
        <span class="text-[11px] text-zinc-400">卡片纸张保持奶油白，只换底色与底纹</span>
      </div>
      <div class="mt-4 flex flex-wrap gap-2">
        <button
          v-for="t in BACKGROUND_OPTIONS"
          :key="t.id"
          type="button"
          class="opt-chip"
          :class="{ 'opt-chip-on': appearance.background === t.id }"
          @click="onPatch({ background: t.id })"
        >
          <span class="tone-dot" :style="{ background: t.bg }" />
          {{ t.name }}
        </button>
      </div>
      <div class="dotline my-5" />
      <div class="flex items-baseline justify-between gap-3 flex-wrap">
        <span class="tlabel">Pattern · 背景纹理</span>
        <span class="text-[11px] text-zinc-400">叠加在页面底色的壁纸</span>
      </div>
      <div class="mt-3 flex flex-wrap gap-2">
        <button
          v-for="p in PATTERN_OPTIONS"
          :key="p.id"
          type="button"
          class="opt-chip"
          :class="{ 'opt-chip-on': appearance.pattern === p.id }"
          @click="onPatch({ pattern: p.id })"
        >
          <span class="pattern-thumb" :class="`pattern-thumb--${p.id}`" />
          {{ p.name }}
        </button>
      </div>
    </Card>

    <!-- 侧边栏 -->
    <Card style="padding: 20px 24px">
      <div class="flex items-baseline justify-between gap-3 flex-wrap">
        <span class="tlabel">Sidebar · 侧边栏</span>
        <span class="text-[11px] text-zinc-400">「主题色浅调」会跟随上面选的主色</span>
      </div>
      <div class="side-grid mt-4">
        <button
          v-for="s in SIDEBAR_OPTIONS"
          :key="s.id"
          type="button"
          class="side-tile"
          :class="{ 'side-tile--on': appearance.sidebar === s.id }"
          :aria-pressed="appearance.sidebar === s.id"
          @click="onPatch({ sidebar: s.id })"
        >
          <span class="side-thumb" :style="{ background: sidebarSwatch(s) }">
            <span class="side-thumb__logo" :class="{ 'side-thumb__logo--dark': s.dark }" />
            <span class="side-thumb__item" :class="{ 'side-thumb__item--dark': s.dark }" />
            <span class="side-thumb__item side-thumb__item--accent" :style="{ background: appearance.accent }" />
            <span class="side-thumb__item" :class="{ 'side-thumb__item--dark': s.dark }" />
          </span>
          <span class="side-meta">
            <span class="side-name">{{ s.name }}</span>
            <span class="side-desc">{{ s.desc }}</span>
          </span>
          <span v-if="appearance.sidebar === s.id" class="tile-check">✓</span>
        </button>
      </div>
    </Card>

    <!-- 界面细节 -->
    <Card style="padding: 20px 24px">
      <div class="flex items-baseline justify-between gap-3 flex-wrap">
        <span class="tlabel">Details · 界面细节</span>
        <span class="text-[11px] text-zinc-400">圆角与动效</span>
      </div>
      <div class="mt-4 flex items-baseline gap-3 flex-wrap">
        <span class="sub-label">圆角</span>
        <div class="flex flex-wrap gap-2">
          <button
            v-for="r in RADIUS_OPTIONS"
            :key="r.id"
            type="button"
            class="opt-chip"
            :class="{ 'opt-chip-on': appearance.radius === r.id }"
            @click="onPatch({ radius: r.id })"
          >
            <span class="radius-sample" :style="{ borderRadius: r.sample }" />
            {{ r.name }}
          </button>
        </div>
      </div>
      <div class="dotline my-5" />
      <div class="flex items-baseline gap-3 flex-wrap">
        <span class="sub-label">动效</span>
        <div class="flex flex-wrap gap-2">
          <button
            v-for="m in MOTION_OPTIONS"
            :key="m.id"
            type="button"
            class="opt-chip"
            :class="{ 'opt-chip-on': appearance.motion === m.id }"
            @click="onPatch({ motion: m.id })"
          >
            <span class="motion-dot" :class="{ 'motion-dot--still': m.id === 'reduced' }" />
            {{ m.name }}
          </button>
        </div>
      </div>
      <p class="mt-4 text-[11px] text-zinc-400 leading-relaxed">
        偏好保存在浏览器本地（键 <code class="tiny-code">aniabot.appearance.v1</code>）。换浏览器、换设备或清除站点数据后会回到默认外观。
      </p>
    </Card>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { Button, Card, Notification } from 'animal-island-vue'
import {
  ACCENT_PRESETS,
  BACKGROUND_OPTIONS,
  MOTION_OPTIONS,
  PATTERN_OPTIONS,
  RADIUS_OPTIONS,
  SIDEBAR_OPTIONS,
  THEME_PRESETS,
  appearance,
  updateAppearance,
  resetAppearance,
} from '../appearance'

const hexText = ref(appearance.accent)
watch(() => appearance.accent, (value) => { hexText.value = value })

const sameAccent = (value) => appearance.accent.toLowerCase() === value.toLowerCase()

// 预设主题是否与当前状态完全一致（主题色 + 底色 + 纹理 + 侧栏）
const isThemeActive = (preset) =>
  sameAccent(preset.accent) &&
  appearance.background === preset.background &&
  appearance.pattern === preset.pattern &&
  appearance.sidebar === preset.sidebar

const sidebarSwatch = (item) => (typeof item.swatch === 'function' ? item.swatch(appearance.accent) : item.swatch)

function onPatch(patch) {
  updateAppearance(patch)
}

function onPickTheme(preset) {
  updateAppearance({
    accent: preset.accent,
    background: preset.background,
    pattern: preset.pattern,
    sidebar: preset.sidebar,
  })
}

function onPickAccent(value) {
  updateAppearance({ accent: value })
}

function onCustomColor(event) {
  updateAppearance({ accent: event.target.value })
}

function onHexInput(event) {
  hexText.value = event.target.value.trim()
}

// 手输十六进制：允许省略 #，非法输入回退到当前值
function onHexCommit() {
  const value = hexText.value.trim()
  if (!/^#?[0-9a-f]{6}$/i.test(value)) {
    hexText.value = appearance.accent
    return
  }
  onPickAccent(value.startsWith('#') ? value : '#' + value)
  hexText.value = appearance.accent
}

function onReset() {
  resetAppearance()
  Notification.success({ message: '已恢复默认外观' })
}
</script>

<style scoped>
/* ---- 预设主题卡片 ---- */
.preset-grid {
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(auto-fill, minmax(158px, 1fr));
}
.preset-tile,
.side-tile {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 8px;
  border-radius: var(--r-lg);
  border: 2px solid var(--line);
  background: var(--surface);
  text-align: left;
  cursor: pointer;
  transition: border-color 0.16s ease, box-shadow 0.16s ease, transform 0.16s ease;
}
.preset-tile:hover,
.side-tile:hover {
  border-color: var(--line-strong);
  box-shadow: var(--shadow-base);
  transform: translateY(-1px);
}
.preset-tile--on,
.side-tile--on {
  border-color: var(--accent);
  box-shadow: 0 3px 0 0 var(--accent-strong), var(--shadow-sm);
}
.preset-thumb {
  position: relative;
  display: flex;
  height: 68px;
  overflow: hidden;
  border-radius: calc(var(--r-lg) - 3px);
  border: 1.5px solid rgb(114 93 66 / 0.12);
}
.preset-thumb__side {
  width: 26%;
  flex-shrink: 0;
}
.preset-thumb__main {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding: 9px 9px 0;
}
.preset-thumb__card {
  height: 14px;
  border-radius: 5px;
  background: var(--surface);
  border: 1.5px solid rgb(114 93 66 / 0.1);
}
.preset-thumb__card--short {
  width: 62%;
}
.preset-thumb__bar {
  margin-top: auto;
  height: 10px;
  width: 46%;
  border-radius: 5px 5px 0 0;
}
.preset-meta {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 0 3px 2px;
}
.preset-name {
  font-size: 12.5px;
  font-weight: 700;
  color: var(--ink);
  letter-spacing: 0.01em;
}
.preset-desc {
  font-size: 10.5px;
  color: var(--ink-muted);
}
.tile-check {
  position: absolute;
  top: 6px;
  right: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 800;
  color: var(--nav-active-ink);
  background: var(--accent);
  border: 2px solid var(--surface);
  box-shadow: 0 1px 3px rgb(61 52 40 / 0.3);
}

/* ---- 主题色色板 ---- */
.swatch-row {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.swatch {
  position: relative;
  width: 34px;
  height: 34px;
  border-radius: 999px;
  border: 2px solid var(--surface);
  box-shadow: 0 0 0 1.5px var(--line-strong), 0 2px 4px rgb(61 52 40 / 0.14);
  cursor: pointer;
  transition: transform 0.15s ease, box-shadow 0.15s ease;
}
.swatch:hover {
  transform: translateY(-2px);
  box-shadow: 0 0 0 1.5px var(--line-input), 0 4px 8px rgb(61 52 40 / 0.2);
}
.swatch--on {
  box-shadow: 0 0 0 2.5px var(--ink-body), 0 3px 7px rgb(61 52 40 / 0.24);
}
.swatch-check {
  font-size: 13px;
  font-weight: 800;
  line-height: 1;
}
.accent-picker {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 14px 6px 9px;
  border: 2px dashed var(--line-input);
  border-radius: 999px;
  background: var(--surface);
  color: var(--ink-soft);
  font-size: 12.5px;
  font-weight: 600;
  cursor: pointer;
  overflow: hidden;
}
.accent-picker:hover {
  color: var(--ink);
}
.accent-picker input[type="color"] {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  padding: 0;
  border: 0;
  opacity: 0;
  cursor: pointer;
}
.accent-picker__dot {
  width: 16px;
  height: 16px;
  border-radius: 999px;
  border: 2px solid #fff;
  box-shadow: 0 0 0 1.5px var(--line-strong);
}
.hex-input {
  width: 108px;
  text-align: center;
  font-family: var(--font-mono);
  font-size: 12.5px;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

/* ---- 底色 / 纹理小图标 ---- */
.tone-dot {
  width: 15px;
  height: 15px;
  border-radius: 999px;
  border: 1.5px solid rgb(114 93 66 / 0.22);
  flex-shrink: 0;
}
.pattern-thumb {
  width: 17px;
  height: 17px;
  flex-shrink: 0;
  border-radius: 5px;
  border: 1.5px solid var(--line-input);
  background-color: var(--surface);
}
.pattern-thumb--dots {
  background-image: radial-gradient(circle, var(--ink-disabled) 1px, transparent 1px);
  background-size: 6px 6px;
}
.pattern-thumb--grid {
  background-image: linear-gradient(90deg, var(--ink-disabled) 1px, transparent 1px),
    linear-gradient(var(--ink-disabled) 1px, transparent 1px);
  background-size: 6px 6px;
}
.pattern-thumb--plain {
  background-image: none;
}

/* ---- 侧栏风格 ---- */
.side-grid {
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(auto-fill, minmax(152px, 1fr));
}
.side-thumb {
  display: flex;
  flex-direction: column;
  gap: 5px;
  height: 72px;
  padding: 9px 8px;
  border-radius: calc(var(--r-lg) - 3px);
  border: 1.5px solid rgb(114 93 66 / 0.12);
  overflow: hidden;
}
.side-thumb__logo {
  width: 16px;
  height: 16px;
  border-radius: 6px;
  background: rgb(255 255 255 / 0.75);
  border: 1.5px solid rgb(0 0 0 / 0.08);
  margin-bottom: 2px;
}
.side-thumb__logo--dark {
  background: rgb(255 255 255 / 0.28);
  border-color: rgb(255 255 255 / 0.2);
}
.side-thumb__item {
  height: 8px;
  width: 78%;
  border-radius: 4px;
  background: rgb(0 0 0 / 0.14);
}
.side-thumb__item--dark {
  background: rgb(255 255 255 / 0.24);
}
.side-thumb__item--accent {
  width: 88%;
  box-shadow: 0 1.5px 0 0 rgb(0 0 0 / 0.18);
}
.side-meta {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 0 3px 2px;
}
.side-name {
  font-size: 12.5px;
  font-weight: 700;
  color: var(--ink);
}
.side-desc {
  font-size: 10.5px;
  color: var(--ink-muted);
}

/* ---- 圆角 / 动效小图标 ---- */
.sub-label {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.06em;
  color: var(--ink-muted);
  min-width: 28px;
}
.radius-sample {
  width: 20px;
  height: 20px;
  flex-shrink: 0;
  background: var(--accent);
  border: 1.5px solid var(--accent-strong);
}
.motion-dot {
  width: 9px;
  height: 9px;
  flex-shrink: 0;
  border-radius: 999px;
  background: var(--accent);
  box-shadow: 0 0 0 2px var(--accent-soft);
  animation: appearance-pulse 1.6s ease-in-out infinite;
}
.motion-dot--still {
  animation: none;
}
@keyframes appearance-pulse {
  0%,
  100% {
    transform: scale(0.72);
    opacity: 0.7;
  }
  50% {
    transform: scale(1);
    opacity: 1;
  }
}
.tiny-code {
  font-family: var(--font-mono);
  font-size: 10.5px;
  padding: 1px 5px;
  border-radius: 5px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  color: var(--ink-body);
}

@media (max-width: 640px) {
  .preset-grid,
  .side-grid {
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  }
}
</style>
