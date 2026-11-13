// 面板数值格式化。
//
// Token 数量级动辄百万，此前用 M / k 缩写（如 39.80M）：一眼看不出量级之间的差距，
// 柱状图悬停提示又是原始数字（39789412），很难扫读。这里统一改成千分位，
// 展示与悬浮提示共用同一套格式。

/** 千分位整数（四舍五入）。非数字统一按 0 处理。 */
export function fmtInt(n) {
  const v = Number(n)
  if (!Number.isFinite(v)) return '0'
  return Math.round(v).toLocaleString('en-US')
}

/** 比例（0–1）→ 百分比文案，非正数显示占位符。 */
export function fmtPct(r, digits = 1) {
  const v = Number(r)
  if (!Number.isFinite(v) || v <= 0) return '—'
  return (v * 100).toFixed(digits) + '%'
}
