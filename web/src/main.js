import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import backdropClose from './directives/backdropClose'
import { installModalMaskGuard } from './ui/modalMaskGuard.js'
import { initAppearance } from './appearance'
// 主题库样式（animal-island-vue 的同步副本，见 scripts/sync-theme.mjs）必须先于面板自身样式
import './styles/animal-island.vendor.css'
import './style.css'

const routes = [
  { path: '/', name: 'dashboard', component: () => import('./views/Dashboard.vue'), meta: { title: '状态总览' } },
  { path: '/console', name: 'console', component: () => import('./views/ConsoleLog.vue'), meta: { title: '控制台日志' } },
  { path: '/oplogs', name: 'oplogs', component: () => import('./views/OpLog.vue'), meta: { title: '操作日志' } },
  { path: '/msglogs', name: 'msglogs', component: () => import('./views/MsgLog.vue'), meta: { title: '消息日志' } },
  { path: '/chatconsole', name: 'chatconsole', component: () => import('./views/ChatConsole.vue'), meta: { title: '试聊室' } },
  { path: '/querylogs', name: 'querylogs', component: () => import('./views/QueryLog.vue'), meta: { title: 'Query 日志' } },
  { path: '/tokenstats', name: 'tokenstats', component: () => import('./views/TokenStats.vue'), meta: { title: 'Token 统计' } },
  { path: '/quota', name: 'quota', component: () => import('./views/Quota.vue'), meta: { title: '配额管理' } },
  { path: '/tasklogs', name: 'tasklogs', component: () => import('./views/TaskLog.vue'), meta: { title: '定时任务' } },
  { path: '/skills', name: 'skills', component: () => import('./views/Skills.vue'), meta: { title: '技能管理' } },
  { path: '/memory', name: 'memory', component: () => import('./views/Memory.vue'), meta: { title: '记忆管理' } },
  { path: '/knowledge', name: 'knowledge', component: () => import('./views/Knowledge.vue'), meta: { title: '知识库' } },
  { path: '/team', name: 'team', component: () => import('./views/Team.vue'), meta: { title: 'Agent 团队' } },
  { path: '/config', name: 'config', component: () => import('./views/Config.vue'), meta: { title: '配置管理' } },
  { path: '/files', name: 'files', component: () => import('./views/Files.vue'), meta: { title: '扩展配置' } },
  { path: '/contacts', name: 'contacts', component: () => import('./views/Contacts.vue'), meta: { title: '通讯录' } },
  { path: '/marketplace', name: 'marketplace', component: () => import('./views/Marketplace.vue'), meta: { title: '插件市场' } },
  { path: '/update', name: 'update', component: () => import('./views/Update.vue'), meta: { title: '自动更新' } },
  { path: '/appearance', name: 'appearance', component: () => import('./views/Appearance.vue'), meta: { title: '外观设置' } },
]

// 主内容用 <Transition name="fade" mode="out-in"> 淡入（见 App.vue / style.css 的 0.22s），
// 导航瞬间新页面还没挂载，恢复旧位置要等过渡结束，否则会被当时旧页面的高度截断。
const RESTORE_SCROLL_MS = 250

// 页面切换的滚动行为：新页面回到顶部，浏览器前进/后退恢复离开时的位置。
// vue-router 4 在没有 scrollBehavior 时完全不接管滚动，而面板是 hash 路由的 SPA
// （文档不会重新加载），窗口的滚动偏移会原样带到下一个页面——因此从滚动过的页面
// 点开 Token 统计、配置管理这类随窗口滚动的长页面时，会直接停在中间而不是顶部。
export const router = createRouter({
  history: createWebHashHistory(),
  routes,
  scrollBehavior(to, from, savedPosition) {
    if (savedPosition) {
      return new Promise((resolve) => {
        setTimeout(() => resolve(savedPosition), RESTORE_SCROLL_MS)
      })
    }
    return { top: 0 }
  },
})

async function boot() {
  // 面板外观偏好（主题色 / 侧栏 / 圆角…）在挂载前应用，避免首屏闪一下默认配色
  initAppearance()
  // 演示模式（--mode demo）：装载本地 mock，替换 fetch；正式构建中该分支被静态消除
  if (import.meta.env.MODE === 'demo') {
    const { installMock } = await import('./mock')
    installMock()
  }
  // 组件库弹窗的遮罩点击防误关（拖选文本后松开鼠标落在遮罩上时不应关闭）
  installModalMaskGuard()
  createApp(App).directive('backdrop-close', backdropClose).use(router).mount('#app')
}

boot()
