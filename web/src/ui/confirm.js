// 全局确认弹窗：替代系统 confirm()，用主题库的 Modal + Button 渲染。
// 用法：if (!(await confirmAction('确定删除吗？'))) return
//       if (!(await confirmAction({ title: '删除', message: '...', danger: true }))) return
import { reactive } from 'vue'

export const confirmState = reactive({
  open: false,
  title: '请确认',
  message: '',
  confirmText: '确定',
  cancelText: '取消',
  danger: true,
  resolve: null,
})

/**
 * 弹出主题确认框，返回 Promise<boolean>。
 * @param {string | { title?: string, message?: string, confirmText?: string, cancelText?: string, danger?: boolean }} input
 * @returns {Promise<boolean>}
 */
export function confirmAction(input) {
  const opts = typeof input === 'string' ? { message: input } : input || {}
  return new Promise((resolve) => {
    // 多次弹窗时先结束上一个，避免悬挂
    if (confirmState.resolve) confirmState.resolve(false)
    confirmState.message = opts.message ?? ''
    confirmState.title = opts.title ?? '请确认'
    confirmState.confirmText = opts.confirmText ?? '确定'
    confirmState.cancelText = opts.cancelText ?? '取消'
    confirmState.danger = opts.danger !== false
    confirmState.resolve = resolve
    confirmState.open = true
  })
}

/** 结算当前确认框（true=确定，false=取消） */
export function settleConfirm(ok) {
  confirmState.open = false
  const resolve = confirmState.resolve
  confirmState.resolve = null
  if (resolve) resolve(ok)
}
