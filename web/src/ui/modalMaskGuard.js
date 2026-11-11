// 主题库 Modal 的遮罩用 @click 直接关闭，而「在弹窗内拖选文本、鼠标移到遮罩上松开」时，
// 浏览器会把 click 派发到按下点与松开点的公共祖先 —— 恰好就是遮罩层，
// 于是弹窗被误关（与旧的自写弹窗同一个坑，旧实现见 directives/backdropClose.js）。
//
// 这里在捕获阶段做一次统一拦截：只有 mousedown 与 mouseup 都落在遮罩本身时，
// 才放行遮罩上的 click（即真正的「点遮罩关闭」）。对所有 animal-island-vue 弹窗生效。
const MASK_CLASS = 'animal-modal__mask'

let downOnMask = false
let upOnMask = false

function isMask(target) {
  return !!target && !!target.classList && target.classList.contains(MASK_CLASS)
}

export function installModalMaskGuard() {
  if (typeof document === 'undefined') return
  if (window.__aniaModalMaskGuard) return
  window.__aniaModalMaskGuard = true

  document.addEventListener(
    'mousedown',
    (e) => {
      downOnMask = isMask(e.target)
    },
    true,
  )

  document.addEventListener(
    'mouseup',
    (e) => {
      upOnMask = isMask(e.target)
    },
    true,
  )

  document.addEventListener(
    'click',
    (e) => {
      // 只处理落在遮罩上的 click；弹窗内部的点击原样放行
      if (!isMask(e.target)) {
        downOnMask = false
        upOnMask = false
        return
      }
      const realMaskClick = downOnMask && upOnMask
      downOnMask = false
      upOnMask = false
      if (realMaskClick) return
      // 拖选 / 从遮罩按下再拖进弹窗等场景：吃掉这次 click，遮罩不关弹窗
      e.stopPropagation()
      e.preventDefault()
    },
    true,
  )
}
