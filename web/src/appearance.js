// 面板外观偏好：主题色、页面背景、侧栏风格、圆角与动效。
//
// 所有可调外观都落到 <html> 上的 CSS 变量与 data-* 属性，由 style.css 的
// 设计令牌与 Tailwind 兼容层消费，因此改完即时生效、不需要重启 Bot。
// 偏好只保存在当前浏览器（localStorage），不写进 Bot 配置，也不影响其他设备。
//
// 主题色不写死一整套色板：只保存选中的主色，悬停 / 按下 / 浅底 / 描边等
// 派生色在运行时按比例混合得出，自定义任意颜色都能得到协调的一套。

import { reactive } from 'vue'

const STORAGE_KEY = 'aniabot.appearance.v1'

export const DEFAULT_APPEARANCE = Object.freeze({
  accent: '#19c8b9',
  background: 'cream',
  pattern: 'dots',
  sidebar: 'cream',
  radius: 'normal',
  motion: 'full',
})

// ============ 颜色工具 ============

function parseHex(value) {
  let hex = String(value || '').trim().replace(/^#/, '')
  if (hex.length === 3) hex = hex.split('').map((c) => c + c).join('')
  if (!/^[0-9a-f]{6}$/i.test(hex)) return null
  return [parseInt(hex.slice(0, 2), 16), parseInt(hex.slice(2, 4), 16), parseInt(hex.slice(4, 6), 16)]
}

function toHex(rgb) {
  return '#' + rgb.map((v) => Math.max(0, Math.min(255, Math.round(v))).toString(16).padStart(2, '0')).join('')
}

// 把 a 朝 b 混合：ratio=0 得到 a，ratio=1 得到 b
function mix(a, b, ratio) {
  const ca = parseHex(a)
  const cb = parseHex(b)
  if (!ca || !cb) return a
  const t = Math.min(1, Math.max(0, ratio))
  return toHex(ca.map((v, i) => v + (cb[i] - v) * t))
}

const mixWhite = (color, ratio) => mix(color, '#ffffff', ratio)
const mixBlack = (color, ratio) => mix(color, '#000000', ratio)

function rgbCss(color, alpha) {
  const rgb = parseHex(color) || [0, 0, 0]
  return `rgb(${rgb[0]} ${rgb[1]} ${rgb[2]} / ${alpha})`
}

// 深色还是浅色文字压在主题色上（决定导航选中项的文字色）：
// 用 WCAG 相对亮度算两种候选文字的对比度，取更大的那个。
function relativeLuminance(color) {
  const rgb = parseHex(color) || [0, 0, 0]
  const [r, g, b] = rgb.map((v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrastRatio(a, b) {
  const la = relativeLuminance(a)
  const lb = relativeLuminance(b)
  const [hi, lo] = la >= lb ? [la, lb] : [lb, la]
  return (hi + 0.05) / (lo + 0.05)
}

const INK_ON_ACCENT = '#3a2d1c'

function contrastInk(color) {
  const onWhite = contrastRatio(color, '#ffffff')
  const onInk = contrastRatio(color, INK_ON_ACCENT)
  return onWhite >= onInk ? '#ffffff' : INK_ON_ACCENT
}

function normalizeAccent(value) {
  const rgb = parseHex(value)
  return rgb ? toHex(rgb) : DEFAULT_APPEARANCE.accent
}

// ============ 主题色预设 ============

export const ACCENT_PRESETS = [
  { name: '薄荷青绿', value: '#19c8b9' },
  { name: '海盐蓝', value: '#3a8fd8' },
  { name: '雾霭紫', value: '#8b78e6' },
  { name: '樱花粉', value: '#e86a93' },
  { name: '珊瑚橘', value: '#ef7a52' },
  { name: '琥珀金', value: '#d99a1e' },
  { name: '苔原绿', value: '#4aa96c' },
  { name: '松墨青', value: '#2f8f86' },
  { name: '焦糖棕', value: '#b57b45' },
  { name: '石板蓝灰', value: '#5f7d99' },
  { name: '覆盆子', value: '#c2557f' },
  { name: '石墨灰', value: '#5b5b66' },
].map((item) => ({ ...item, ink: contrastInk(item.value) }))

// ============ 页面背景 ============

// 只换「页面底色 + 光晕 + 点阵颜色」，卡片纸张（奶油白）保持统一观感。
const BACKGROUND_STYLES = {
  cream: {
    name: '奶油小岛',
    desc: '暖米白底色',
    bg: '#efe5cd',
    halo1: 'rgb(25 200 185 / 0.12)',
    halo2: 'rgb(247 205 103 / 0.18)',
    dot: 'rgb(196 184 158 / 0.3)',
  },
  snow: {
    name: '月白',
    desc: '冷调浅灰蓝',
    bg: '#e8ecf2',
    halo1: 'rgb(90 140 210 / 0.1)',
    halo2: 'rgb(120 195 210 / 0.14)',
    dot: 'rgb(174 184 196 / 0.32)',
  },
  sakura: {
    name: '樱粉',
    desc: '淡粉暖调',
    bg: '#f5e6e3',
    halo1: 'rgb(240 140 165 / 0.14)',
    halo2: 'rgb(247 200 150 / 0.16)',
    dot: 'rgb(212 180 175 / 0.34)',
  },
  mint: {
    name: '薄荷',
    desc: '青绿浅底',
    bg: '#e1ede4',
    halo1: 'rgb(70 190 160 / 0.14)',
    halo2: 'rgb(180 215 130 / 0.16)',
    dot: 'rgb(170 194 178 / 0.34)',
  },
  oat: {
    name: '燕麦',
    desc: '沙色大地调',
    bg: '#ece3d1',
    halo1: 'rgb(205 165 110 / 0.14)',
    halo2: 'rgb(140 170 150 / 0.1)',
    dot: 'rgb(192 178 152 / 0.32)',
  },
  slate: {
    name: '石板',
    desc: '冷灰工业感',
    bg: '#dfe3e8',
    halo1: 'rgb(100 130 175 / 0.12)',
    halo2: 'rgb(150 175 210 / 0.16)',
    dot: 'rgb(168 178 190 / 0.3)',
  },
}

export const BACKGROUND_OPTIONS = Object.entries(BACKGROUND_STYLES).map(([id, style]) => ({ id, ...style }))

// ============ 背景纹理 ============

export const PATTERN_OPTIONS = [
  { id: 'dots', name: '点阵', desc: '小岛墙纸' },
  { id: 'grid', name: '网格', desc: '方格纸' },
  { id: 'plain', name: '纯色', desc: '只保留光晕' },
]

// ============ 侧边栏 ============

// 侧栏是面积最大的深色/浅色块，换风格时视觉差异最明显。
// 每套风格给出一组令牌；「主题色浅调」按当前主色实时算出浅色渐变。
const SIDEBAR_STYLES = {
  cream: {
    name: '奶油小岛',
    desc: '默认暖米白',
    swatch: 'linear-gradient(180deg, #fdf8ea, #f3ecd8)',
    vars: () => ({
      '--sidebar-bg':
        'radial-gradient(circle, rgb(196 184 158 / 0.22) 1.1px, transparent 1.1px) 0 0 / 20px 20px, linear-gradient(180deg, #fdf8ea 0%, #f8f2e0 46%, #f3ecd8 100%)',
      '--sidebar-border': 'var(--line-strong)',
      '--sidebar-ink': '#87735a',
      '--sidebar-ink-strong': '#5f4423',
      '--sidebar-hover-bg': 'rgb(255 253 247 / 0.7)',
      '--sidebar-divider': 'rgb(114 93 66 / 0.1)',
      '--sidebar-tip-bg': '#2b2118',
      '--sidebar-tip-ink': '#f7ecd6',
    }),
  },
  accent: {
    name: '主题色浅调',
    desc: '跟随主色',
    swatch: (accent) => `linear-gradient(180deg, ${mixWhite(accent, 0.94)}, ${mixWhite(accent, 0.84)})`,
    vars: (accent) => ({
      '--sidebar-bg': `radial-gradient(circle, ${rgbCss(mixBlack(accent, 0.2), 0.1)} 1.1px, transparent 1.1px) 0 0 / 20px 20px, linear-gradient(180deg, ${mixWhite(accent, 0.94)} 0%, ${mixWhite(accent, 0.9)} 46%, ${mixWhite(accent, 0.85)} 100%)`,
      '--sidebar-border': mixBlack(accent, 0.14),
      '--sidebar-ink': mixBlack(accent, 0.55),
      '--sidebar-ink-strong': mixBlack(accent, 0.74),
      '--sidebar-hover-bg': 'rgb(255 255 255 / 0.6)',
      '--sidebar-divider': rgbCss(mixBlack(accent, 0.5), 0.16),
      '--sidebar-tip-bg': mixBlack(accent, 0.55),
      '--sidebar-tip-ink': '#fdf9f1',
    }),
  },
  coffee: {
    name: '深棕咖啡',
    desc: '暖深色',
    dark: true,
    swatch: 'linear-gradient(180deg, #40342a, #2b221a)',
    vars: () => ({
      '--sidebar-bg':
        'radial-gradient(circle, rgb(255 255 255 / 0.05) 1.1px, transparent 1.1px) 0 0 / 20px 20px, linear-gradient(180deg, #40342a 0%, #342a21 46%, #2b221a 100%)',
      '--sidebar-border': '#221a13',
      '--sidebar-ink': '#d6c3a5',
      '--sidebar-ink-strong': '#f8eedb',
      '--sidebar-hover-bg': 'rgb(255 255 255 / 0.08)',
      '--sidebar-divider': 'rgb(255 255 255 / 0.1)',
      '--sidebar-tip-bg': '#f8eedb',
      '--sidebar-tip-ink': '#2b2118',
    }),
  },
  slate: {
    name: '石板深蓝',
    desc: '冷深色',
    dark: true,
    swatch: 'linear-gradient(180deg, #3a4553, #262f3a)',
    vars: () => ({
      '--sidebar-bg':
        'radial-gradient(circle, rgb(255 255 255 / 0.06) 1.1px, transparent 1.1px) 0 0 / 20px 20px, linear-gradient(180deg, #3a4553 0%, #303a47 46%, #262f3a 100%)',
      '--sidebar-border': '#1d2530',
      '--sidebar-ink': '#c2cddb',
      '--sidebar-ink-strong': '#f3f7fc',
      '--sidebar-hover-bg': 'rgb(255 255 255 / 0.09)',
      '--sidebar-divider': 'rgb(255 255 255 / 0.1)',
      '--sidebar-tip-bg': '#f3f7fc',
      '--sidebar-tip-ink': '#262f3a',
    }),
  },
}

export const SIDEBAR_OPTIONS = Object.entries(SIDEBAR_STYLES).map(([id, style]) => ({
  id,
  name: style.name,
  desc: style.desc,
  dark: style.dark === true,
  swatch: style.swatch,
}))

// ============ 圆角 ============

// 与 style.css 中的 --r-* 令牌一一对应；「紧凑」把 pill 形输入框也收成小圆角，
// 「圆润」把卡片 / 表格放大一档。
const RADIUS_STYLES = {
  compact: {
    name: '紧凑',
    desc: '棱角分明',
    vars: {
      '--r-card': '14px',
      '--r-sm': '4px',
      '--r': '6px',
      '--r-md': '8px',
      '--r-lg': '10px',
      '--r-xl': '12px',
      '--r-2xl': '14px',
      '--r-3xl': '16px',
      '--r-input': '12px',
    },
  },
  normal: {
    name: '标准',
    desc: '主题默认',
    vars: {
      '--r-card': '20px',
      '--r-sm': '6px',
      '--r': '8px',
      '--r-md': '10px',
      '--r-lg': '13px',
      '--r-xl': '16px',
      '--r-2xl': '20px',
      '--r-3xl': '24px',
      '--r-input': '999px',
    },
  },
  round: {
    name: '圆润',
    desc: '更大更软',
    vars: {
      '--r-card': '26px',
      '--r-sm': '8px',
      '--r': '11px',
      '--r-md': '14px',
      '--r-lg': '17px',
      '--r-xl': '20px',
      '--r-2xl': '24px',
      '--r-3xl': '28px',
      '--r-input': '999px',
    },
  },
}

export const RADIUS_OPTIONS = Object.entries(RADIUS_STYLES).map(([id, style]) => ({
  id,
  name: style.name,
  desc: style.desc,
  // 小方块上的示意圆角：比卡片实际圆角小一号，三档才分得开
  sample: id === 'compact' ? '5px' : id === 'round' ? '15px' : '10px',
}))

// ============ 动效 ============

export const MOTION_OPTIONS = [
  { id: 'full', name: '标准', desc: '保留过渡与呼吸动画' },
  { id: 'reduced', name: '减弱', desc: '关闭装饰性动效' },
]

// ============ 预设主题 ============

// 整套配色组合：主色 + 页面底色 + 纹理 + 侧栏。圆角与动效不随预设变化。
export const THEME_PRESETS = [
  { id: 'island', name: '薄荷小岛', desc: '奶油底 · 薄荷青绿', accent: '#19c8b9', background: 'cream', pattern: 'dots', sidebar: 'cream' },
  { id: 'sakura', name: '樱花奶油', desc: '樱粉底 · 玫瑰粉', accent: '#e86a93', background: 'sakura', pattern: 'dots', sidebar: 'cream' },
  { id: 'ocean', name: '海盐月白', desc: '月白底 · 海盐蓝', accent: '#3a8fd8', background: 'snow', pattern: 'grid', sidebar: 'accent' },
  { id: 'forest', name: '森林燕麦', desc: '燕麦底 · 苔原绿', accent: '#4aa96c', background: 'oat', pattern: 'dots', sidebar: 'coffee' },
  { id: 'dusk', name: '暮色咖啡', desc: '奶油底 · 琥珀金', accent: '#d99a1e', background: 'cream', pattern: 'plain', sidebar: 'coffee' },
  { id: 'ink', name: '墨石板岩', desc: '石板底 · 蓝灰', accent: '#5f7d99', background: 'slate', pattern: 'grid', sidebar: 'slate' },
].map((preset) => ({
  ...preset,
  page: (BACKGROUND_STYLES[preset.background] || BACKGROUND_STYLES.cream).bg,
  side: typeof SIDEBAR_STYLES[preset.sidebar].swatch === 'function'
    ? SIDEBAR_STYLES[preset.sidebar].swatch(preset.accent)
    : SIDEBAR_STYLES[preset.sidebar].swatch,
}))

// ============ 状态与持久化 ============

function safeParse(text) {
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

export function normalizeAppearance(raw) {
  const source = raw && typeof raw === 'object' ? raw : {}
  return {
    accent: normalizeAccent(source.accent),
    background: BACKGROUND_STYLES[source.background] ? source.background : DEFAULT_APPEARANCE.background,
    pattern: PATTERN_OPTIONS.some((p) => p.id === source.pattern) ? source.pattern : DEFAULT_APPEARANCE.pattern,
    sidebar: SIDEBAR_STYLES[source.sidebar] ? source.sidebar : DEFAULT_APPEARANCE.sidebar,
    radius: RADIUS_STYLES[source.radius] ? source.radius : DEFAULT_APPEARANCE.radius,
    motion: MOTION_OPTIONS.some((m) => m.id === source.motion) ? source.motion : DEFAULT_APPEARANCE.motion,
  }
}

function loadStored() {
  if (typeof localStorage === 'undefined') return { ...DEFAULT_APPEARANCE }
  try {
    const raw = safeParse(localStorage.getItem(STORAGE_KEY))
    // 首次访问（没有保存过偏好）时跟随系统的「减少动态效果」设置
    if (!raw && typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches) {
      return { ...DEFAULT_APPEARANCE, motion: 'reduced' }
    }
    return normalizeAppearance(raw)
  } catch {
    return { ...DEFAULT_APPEARANCE }
  }
}

export const appearance = reactive(loadStored())

function buildVars(state) {
  const background = BACKGROUND_STYLES[state.background] || BACKGROUND_STYLES.cream
  const sidebar = SIDEBAR_STYLES[state.sidebar] || SIDEBAR_STYLES.cream
  const radius = RADIUS_STYLES[state.radius] || RADIUS_STYLES.normal
  return {
    ...radius.vars,
    ...sidebar.vars(state.accent),
    ...accentVars(state.accent),
    '--page-bg': background.bg,
    '--page-halo-1': `radial-gradient(60rem 40rem at 2% -12%, ${background.halo1}, transparent 58%)`,
    '--page-halo-2': `radial-gradient(52rem 36rem at 98% -8%, ${background.halo2}, transparent 56%)`,
    '--page-dot': background.dot,
  }
}

function accentVars(accent) {
  const base = normalizeAccent(accent)
  return {
    '--accent': base,
    '--accent-hover': mixWhite(base, 0.16),
    '--accent-strong': mixBlack(base, 0.2),
    '--accent-active': mixBlack(base, 0.3),
    '--accent-tint': mixWhite(base, 0.9),
    '--accent-soft': rgbCss(base, 0.14),
    '--accent-line': mixWhite(base, 0.66),
    '--accent-mid': mixWhite(base, 0.45),
    '--accent-wash': rgbCss(base, 0.18),
    '--nav-active-bg': base,
    '--nav-active-ink': contrastInk(base),
    '--nav-active-shadow': mixBlack(base, 0.24),
    // 组件库（animal-island-vue）自身的 CSS 变量
    '--animal-primary-color': base,
    '--animal-primary-color-hover': mixWhite(base, 0.16),
    '--animal-primary-color-active': mixBlack(base, 0.3),
    '--animal-primary-color-bg': mixWhite(base, 0.9),
  }
}

// 同步移动端浏览器地址栏的 theme-color，让它跟着页面底色走
function syncThemeColor(color) {
  let meta = document.querySelector('meta[name="theme-color"]')
  if (!meta) {
    meta = document.createElement('meta')
    meta.setAttribute('name', 'theme-color')
    document.head.appendChild(meta)
  }
  meta.setAttribute('content', color)
}

export function applyAppearance() {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  for (const [name, value] of Object.entries(buildVars(appearance))) {
    root.style.setProperty(name, value)
  }
  root.dataset.pattern = appearance.pattern
  root.dataset.motion = appearance.motion
  syncThemeColor((BACKGROUND_STYLES[appearance.background] || BACKGROUND_STYLES.cream).bg)
}

function persist() {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...appearance }))
  } catch {
    /* 隐私模式 / 存储已满：只是记不住偏好，不影响当前页面 */
  }
}

// 合并写入：patch 可以是任意字段子集（如只改主题色）
export function updateAppearance(patch) {
  Object.assign(appearance, normalizeAppearance({ ...appearance, ...patch }))
  persist()
  applyAppearance()
}

export function resetAppearance() {
  updateAppearance({ ...DEFAULT_APPEARANCE })
}

// 启动时调用一次：先应用已保存的偏好，再监听其他标签页的修改
export function initAppearance() {
  applyAppearance()
  if (typeof window === 'undefined') return
  window.addEventListener('storage', (event) => {
    if (event.key !== STORAGE_KEY) return
    Object.assign(appearance, normalizeAppearance(safeParse(event.newValue)))
    applyAppearance()
  })
}
