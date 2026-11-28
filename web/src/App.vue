<template>
  <div v-if="!auth.checked" class="app-shell min-h-dvh flex items-center justify-center">
    <div class="flex items-center gap-2 text-slate-400 text-sm">
      <span class="w-4 h-4 border-2 border-black/10 border-t-[#0071e3] rounded-full animate-spin" />
      加载中...
    </div>
  </div>

  <Login v-else-if="!auth.loggedIn" />

  <Wizard v-else-if="auth.setupRequired" />

  <div v-else class="app-shell min-h-dvh flex">
    <!-- 移动端抽屉遮罩 -->
    <Transition name="fade">
      <div v-if="mobileOpen" class="fixed inset-0 bg-black/30 backdrop-blur-[2px] z-40 md:hidden" @click="mobileOpen = false" />
    </Transition>

    <!-- 侧边导航：奶油小岛侧栏，移动端为抽屉，桌面端常驻 -->
    <aside
      class="sidebar-island fixed inset-y-0 left-0 z-50 w-64 h-dvh flex flex-col shrink-0 transition-transform duration-200 md:sticky md:top-0 md:inset-auto md:z-auto md:w-64"
      :class="mobileOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0'"
    >
      <div class="px-5 pt-6 pb-5 flex items-center gap-3">
        <img :src="logoUrl" alt="AniaBot" class="w-10 h-10 rounded-2xl object-cover border-2 border-white shadow-[0_2px_0_0_var(--shadow-3d)]" />
        <div class="min-w-0">
          <div class="brand-name">AniaBot</div>
          <div class="brand-sub">CONSOLE · 控制面板</div>
        </div>
        <button class="ml-auto md:hidden nav-close" aria-label="关闭菜单" @click="mobileOpen = false">
          <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
        </button>
      </div>
      <!-- 演示模式徽章（demo 构建专属，正式构建静态为 false 不渲染） -->
      <div v-if="isDemo" class="mx-5 mb-3">
        <span class="demo-badge">
          <span class="w-1.5 h-1.5 rounded-full bg-amber-500 animate-pulse" />
          演示模式
        </span>
      </div>
      <div class="nav-divider mx-4" />

      <nav class="flex-1 px-2.5 py-3 overflow-y-auto">
        <div v-for="group in navGroups" :key="group.label" class="nav-group">
          <div class="nav-group-label">{{ group.label }}</div>
          <div class="space-y-0.5">
            <RouterLink
              v-for="item in group.items"
              :key="item.to"
              :to="item.to"
              class="group relative flex items-center gap-3 px-3 h-9 rounded-xl text-[13px] font-semibold border-2 border-transparent transition-all duration-150"
              :class="isNavActive(item) ? 'nav-active' : 'nav-link'"
            >
              <span v-html="item.icon" class="shrink-0 [&>svg]:w-[17px] [&>svg]:h-[17px]" />
              {{ item.label }}
              <span v-if="isNavActive(item)" class="nav-active-dot ml-auto" />
            </RouterLink>
          </div>
        </div>
      </nav>

      <div class="nav-footer px-4 py-4 flex items-center justify-between">
        <a class="nav-ico" href="https://github.com/AniaBot-Project/AniaBot" target="_blank" rel="noopener noreferrer" aria-label="GitHub 仓库">
          <span v-html="icons.github" class="[&>svg]:w-4 [&>svg]:h-4" />
          <span class="tip">GitHub</span>
        </a>
        <RouterLink to="/appearance" class="nav-ico" aria-label="外观设置">
          <span v-html="icons.palette" class="[&>svg]:w-4 [&>svg]:h-4" />
          <span class="tip">外观设置</span>
        </RouterLink>
        <button class="nav-ico" @click="onRestart" aria-label="重启 Bot">
          <span v-html="icons.restart" class="[&>svg]:w-4 [&>svg]:h-4" />
          <span class="tip">重启 Bot</span>
        </button>
        <button class="nav-ico" @click="showPwd = true" aria-label="修改密码">
          <span v-html="icons.key" class="[&>svg]:w-4 [&>svg]:h-4" />
          <span class="tip">修改密码</span>
        </button>
        <button class="nav-ico hover:text-red-300!" @click="onLogout" aria-label="退出登录">
          <span v-html="icons.logout" class="[&>svg]:w-4 [&>svg]:h-4" />
          <span class="tip">退出登录</span>
        </button>
      </div>
    </aside>

    <!-- 主内容 -->
    <main class="flex-1 min-w-0 flex flex-col">
      <!-- 悬浮工具栏：奶油贴纸条 -->
      <header class="topbar-island sticky top-3 z-30 mx-3 sm:mx-5 xl:mx-7 mt-3 px-4 sm:px-5 py-2.5 flex items-center justify-between gap-3">
        <div class="flex items-center gap-2 min-w-0">
          <button class="md:hidden -ml-1 p-1.5 rounded-xl text-slate-600 hover:bg-black/5 transition-colors" aria-label="打开菜单" @click="mobileOpen = true">
            <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6.75h16.5M3.75 12h16.5m-16.5 5.25h16.5" /></svg>
          </button>
          <h1 class="text-[12.5px] font-bold tracking-[0.04em] text-slate-600 truncate">
            <span class="text-slate-300 mr-2">✦</span>{{ $route.meta.title || '' }}
          </h1>
        </div>
        <div class="flex items-center gap-2 shrink-0">
          <button
            v-if="restart.pending"
            class="restart-chip"
            :title="restartTitle"
            @click="focusRestartBanner"
          >
            <span class="w-1.5 h-1.5 rounded-full bg-white/90 animate-pulse" />
            重启等待中 · {{ restart.busy }}
          </button>
          <Tag variant="solid" color="app-green" size="small" class="shrink-0">
            <span class="w-1.5 h-1.5 rounded-full bg-white/90 pulse-dot" />Bot Online
          </Tag>
        </div>
      </header>

      <!-- 重启等待横幅：有进行中的消息 / AI 请求时，重启会等它们完成再执行 -->
      <div v-if="restart.pending" ref="restartBannerEl" class="restart-banner mx-3 sm:mx-5 xl:mx-7 mt-3 px-4 py-3">
        <div class="flex items-start gap-3 flex-wrap">
          <span class="mt-1 w-4 h-4 shrink-0 rounded-full border-2 border-[var(--warn-line)] border-t-[var(--warn-ink)] animate-spin" />
          <div class="min-w-0 flex-1">
            <div class="text-xs font-bold tracking-[0.04em] text-[var(--warn-ink)]">{{ restartTitle }}</div>
            <div class="text-[11px] text-[var(--warn-ink)] opacity-80 mt-0.5">{{ restartSubtitle }}</div>
            <ul v-if="restart.items && restart.items.length" class="mt-1.5 space-y-0.5 text-[11px] text-[var(--warn-ink)] opacity-80">
              <li v-for="it in restart.items" :key="it.id" class="truncate">
                · {{ it.label }}<span class="opacity-70">（{{ elapsedText(it.since) }}）</span>
              </li>
            </ul>
          </div>
          <Button type="default" size="small" danger :disabled="forcingRestart" @click="onForceRestart">
            {{ forcingRestart ? '重启中…' : '立即强制重启' }}
          </Button>
        </div>
      </div>
      <div class="px-3 sm:px-5 xl:px-7 pt-4 sm:pt-5 pb-10 safe-bottom flex-1">
        <RouterView v-slot="{ Component }">
          <Transition name="fade" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </div>
    </main>

    <!-- 修改密码弹窗 -->
    <Modal v-model:open="showPwd" title="修改密码" :typewriter="false" :show-footer="false" :width="420">
      variant="default"
      <form class="space-y-4" @submit.prevent="onChangePwd">
        <Input v-model="pwdForm.next" type="password" placeholder="新密码（至少 6 位）" size="middle" />
        <p v-if="pwdForm.msg" class="text-xs" :class="pwdForm.ok ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ pwdForm.msg }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <Button type="text" @click="showPwd = false">取消</Button>
          <Button type="primary" html-type="submit">保存</Button>
        </div>
      </form>
    </Modal>

    <!-- 命令式通知宿主（全站唯一，避免重复弹窗） -->
    <NotificationContainer />
    <ConfirmDialog />
  </div>
</template>

<script setup>
import { confirmAction } from './ui/confirm'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Button, Input, Modal, Notification, NotificationContainer, Tag } from 'animal-island-vue'
import { api, auth } from './api.js'
import ConfirmDialog from './ui/ConfirmDialog.vue'
import Login from './views/Login.vue'
import Wizard from './views/Wizard.vue'

const router = useRouter()
const route = useRoute()

// 演示模式标记：正式构建中为静态 false，徽章不渲染
const isDemo = import.meta.env.MODE === 'demo'

// logo 走运行时拼接（public 资源 + 相对 base），子路径部署（GitHub Pages）下也能正确解析
const logoUrl = import.meta.env.BASE_URL + 'logo.webp'

const icons = {
  github: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.11.79-.25.79-.55 0-.27-.01-1.17-.02-2.12-3.2.7-3.88-1.36-3.88-1.36-.52-1.33-1.28-1.68-1.28-1.68-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.03 1.75 2.69 1.25 3.34.95.1-.74.4-1.25.72-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.28 1.18-3.09-.12-.29-.51-1.46.11-3.05 0 0 .96-.31 3.16 1.18a11 11 0 0 1 5.76 0c2.2-1.49 3.16-1.18 3.16-1.18.62 1.59.23 2.76.11 3.05.73.81 1.18 1.83 1.18 3.09 0 4.42-2.7 5.39-5.27 5.67.41.36.78 1.06.78 2.14 0 1.54-.01 2.78-.01 3.16 0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z"/></svg>',
  dashboard: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6A2.25 2.25 0 0 1 6 3.75h2.25A2.25 2.25 0 0 1 10.5 6v2.25a2.25 2.25 0 0 1-2.25 2.25H6a2.25 2.25 0 0 1-2.25-2.25V6ZM3.75 15.75A2.25 2.25 0 0 1 6 13.5h2.25a2.25 2.25 0 0 1 2.25 2.25V18a2.25 2.25 0 0 1-2.25 2.25H6A2.25 2.25 0 0 1 3.75 18v-2.25ZM13.5 6a2.25 2.25 0 0 1 2.25-2.25H18A2.25 2.25 0 0 1 20.25 6v2.25A2.25 2.25 0 0 1 18 10.5h-2.25a2.25 2.25 0 0 1-2.25-2.25V6ZM13.5 15.75a2.25 2.25 0 0 1 2.25-2.25H18a2.25 2.25 0 0 1 2.25 2.25V18A2.25 2.25 0 0 1 18 20.25h-2.25A2.25 2.25 0 0 1 13.5 18v-2.25Z"/></svg>',
  config: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z"/><path stroke-linecap="round" stroke-linejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z"/></svg>',
  files: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M17.25 6.75 22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3-4.5 16.5"/></svg>',
  contacts: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z"/></svg>',
  chat: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M20.25 8.511c.884.284 1.5 1.128 1.5 2.097v4.286c0 1.136-.847 2.1-1.98 2.193-.34.027-.68.052-1.02.072v3.091l-3-3c-1.354 0-2.694-.055-4.02-.163a2.115 2.115 0 0 1-.825-.242m9.345-8.334a2.126 2.126 0 0 0-.476-.095 48.64 48.64 0 0 0-8.048 0c-1.131.094-1.976 1.057-1.976 2.192v4.286c0 .837.46 1.58 1.155 1.951m9.345-8.334V6.637c0-1.621-1.152-3.026-2.76-3.235A48.455 48.455 0 0 0 11.25 3c-2.115 0-4.198.137-6.24.402-1.608.209-2.76 1.614-2.76 3.235v6.226c0 1.621 1.152 3.026 2.76 3.235.577.075 1.157.14 1.74.194V21l4.155-4.155"/></svg>',
  trychat: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M7.5 8.25h9m-9 3H12m-9.75 1.51c0 1.6 1.123 2.994 2.707 3.227 1.129.166 2.27.293 3.423.379.35.026.67.21.865.501L12 21l2.755-4.133a1.14 1.14 0 0 1 .865-.501 48.172 48.172 0 0 0 3.423-.379c1.584-.233 2.707-1.626 2.707-3.228V6.741c0-1.602-1.123-2.995-2.707-3.228A48.394 48.394 0 0 0 12 3c-2.392 0-4.744.175-7.043.513C3.373 3.746 2.25 5.14 2.25 6.741v6.018Z"/></svg>',
  context: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m2.25 0H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"/></svg>',
  terminal: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m6.75 7.5 3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 18V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v12a2.25 2.25 0 0 0 2.25 2.25Z"/></svg>',
  audit: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9 12h3.75M9 15h3.75M9 18h3.75m3 .75H18a2.25 2.25 0 0 0 2.25-2.25V6.108c0-1.135-.845-2.098-1.976-2.192a48.424 48.424 0 0 0-1.123-.08m-5.801 0c-.065.21-.1.433-.1.664 0 .414.336.75.75.75h4.5a.75.75 0 0 0 .75-.75 2.25 2.25 0 0 0-.1-.664m-5.8 0A2.251 2.251 0 0 1 13.5 2.25H15c1.012 0 1.867.668 2.15 1.586m-5.8 0c-.376.023-.75.05-1.124.08C9.095 4.01 8.25 4.973 8.25 6.108V8.25m0 0H4.875c-.621 0-1.125.504-1.125 1.125v11.25c0 .621.504 1.125 1.125 1.125h9.75c.621 0 1.125-.504 1.125-1.125V9.375c0-.621-.504-1.125-1.125-1.125H8.25Z"/></svg>',
  skills: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9.813 15.904 9 18.75l-.813-2.846a4.5 4.5 0 0 0-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 0 0 3.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 0 0 3.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 0 0-3.09 3.09ZM18.259 8.715 18 9.75l-.259-1.035a3.375 3.375 0 0 0-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 0 0 2.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 0 0 2.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 0 0-2.456 2.456ZM16.894 20.567 16.5 21.75l-.394-1.183a2.25 2.25 0 0 0-1.423-1.423L13.5 18.75l1.183-.394a2.25 2.25 0 0 0 1.423-1.423l.394-1.183.394 1.183a2.25 2.25 0 0 0 1.423 1.423l1.183.394-1.183.394a2.25 2.25 0 0 0-1.423 1.423Z"/></svg>',
  memory: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M17.593 3.322c1.1.128 1.907 1.077 1.907 2.185V21L12 17.25 4.5 21V5.507c0-1.108.806-2.057 1.907-2.185a48.507 48.507 0 0 1 11.186 0Z"/></svg>',
  book: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M12 6.042A8.967 8.967 0 0 0 6 3.75c-1.052 0-2.062.18-3 .512v14.25A8.987 8.987 0 0 1 6 18c2.305 0 4.408.867 6 2.292m0-14.25a8.966 8.966 0 0 1 6-2.292c1.052 0 2.062.18 3 .512v14.25A8.987 8.987 0 0 0 18 18a8.967 8.967 0 0 0-6 2.292m0-14.25v14.25"/></svg>',
  team: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M18 18.72a9.094 9.094 0 0 0 3.741-.479 3 3 0 0 0-4.682-2.72m.94 3.198.001.031c0 .225-.012.447-.037.666A11.944 11.944 0 0 1 12 21c-2.17 0-4.207-.576-5.963-1.584A6.062 6.062 0 0 1 6 18.719m12 0a5.971 5.971 0 0 0-.941-3.197m0 0A5.995 5.995 0 0 0 12 12.75a5.995 5.995 0 0 0-5.058 2.772m0 0a3 3 0 0 0-4.681 2.72 8.986 8.986 0 0 0 3.74.477m.94-3.197a5.971 5.971 0 0 0-.94 3.197M15 6.75a3 3 0 1 1-6 0 3 3 0 0 1 6 0Zm6 3a2.25 2.25 0 1 1-4.5 0 2.25 2.25 0 0 1 4.5 0Zm-13.5 0a2.25 2.25 0 1 1-4.5 0 2.25 2.25 0 0 1 4.5 0Z"/></svg>',
  query: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z"/></svg>',
  chart: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3 13.125c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v6.75c0 .621-.504 1.125-1.125 1.125h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z"/></svg>',
  gauge: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3.75 13.5 10.5 19.5l3.75-3.75m4.125-9.75L12 12m0 0a2.25 2.25 0 1 0 0-4.5 2.25 2.25 0 0 0 0 4.5Zm3.75-3 4.5 4.5M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z"/></svg>',
  task: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6.75 3v2.25M17.25 3v2.25M3 18.75V7.5a2.25 2.25 0 0 1 2.25-2.25h13.5A2.25 2.25 0 0 1 21 7.5v11.25m-18 0A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75m-18 0v-7.5A2.25 2.25 0 0 1 5.25 9h13.5A2.25 2.25 0 0 1 21 11.25v7.5M12 12.75v3M10.5 14.25h3"/></svg>',
  agenttask: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6.429 9.75 2.25 12l4.179 2.25m0-4.5 5.571 3 5.571-3m-11.142 0L2.25 7.5 12 2.25l9.75 5.25-4.179 2.25m0 0L21.75 12l-4.179 2.25m0 0 4.179 2.25L12 21.75 2.25 16.5l4.179-2.25m11.142 0-5.571 3-5.571-3"/></svg>',
  update: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9 8.25H7.5a2.25 2.25 0 0 0-2.25 2.25v9a2.25 2.25 0 0 0 2.25 2.25h9a2.25 2.25 0 0 0 2.25-2.25v-9a2.25 2.25 0 0 0-2.25-2.25H15m0-3-3-3m0 0-3 3m3-3V15"/></svg>',
  store: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M13.5 21v-7.5a.75.75 0 0 1 .75-.75h3a.75.75 0 0 1 .75.75V21m-4.5 0H2.36m11.14 0H18m0 0h3.64m-1.39 0V9.349M3.75 21V9.349m0 0a3.001 3.001 0 0 0 3.75-.615A2.993 2.993 0 0 0 9.75 9.75c.896 0 1.7-.393 2.25-1.016a2.993 2.993 0 0 0 2.25 1.016c.896 0 1.7-.393 2.25-1.015a3.001 3.001 0 0 0 3.75.614m-16.5 0a3.004 3.004 0 0 1-.621-4.72l1.189-1.19A1.5 1.5 0 0 1 5.378 3h13.243a1.5 1.5 0 0 1 1.06.44l1.19 1.189a3 3 0 0 1-.621 4.72M6.75 18h3.75a.75.75 0 0 0 .75-.75V13.5a.75.75 0 0 0-.75-.75H6.75a.75.75 0 0 0-.75.75v3.75c0 .414.336.75.75.75Z"/></svg>',
  restart: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99"/></svg>',
  palette: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9.53 16.122a3 3 0 0 0-5.78 1.128 2.25 2.25 0 0 1-2.4 2.245 4.5 4.5 0 0 0 8.4-2.245c0-.399-.078-.78-.22-1.128Zm0 0a15.998 15.998 0 0 0 3.388-1.62m-5.043-.025a15.994 15.994 0 0 1 1.622-3.395m3.42 3.42a15.995 15.995 0 0 0 4.764-4.648l3.876-5.814a1.151 1.151 0 0 0-1.597-1.597L14.146 6.32a15.996 15.996 0 0 0-4.649 4.763m3.42 3.42a6.776 6.776 0 0 0-3.42-3.42"/></svg>',
  key: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z"/></svg>',
  logout: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15.75 9V5.25A2.25 2.25 0 0 0 13.5 3h-6a2.25 2.25 0 0 0-2.25 2.25v13.5A2.25 2.25 0 0 0 7.5 21h6a2.25 2.25 0 0 0 2.25-2.25V15m3 0 3-3m0 0-3-3m3 3H9"/></svg>',
}

// 侧边栏导航：按用途分组，一个分组入口对应一组页面。
// 合并页（消息与操作日志 / AI 运行记录 / Token 用量 / 记忆与知识库 / 配置管理）
// 在页内用 PageTabs 切分页签，侧边栏只留一个入口；match 列出该入口覆盖的全部路由，
// 保证切到组内任一页签时侧边栏仍高亮当前分组。
const navGroups = [
  {
    label: '总览',
    items: [
      { to: '/', label: '状态总览', icon: icons.dashboard },
      { to: '/tokenstats', label: 'Token 用量', icon: icons.chart, match: ['/tokenstats', '/quota'] },
    ],
  },
  {
    label: '对话',
    items: [
      { to: '/chatconsole', label: '试聊室', icon: icons.trychat },
      { to: '/contexts', label: '会话上下文', icon: icons.context },
    ],
  },
  {
    label: '日志',
    items: [
      { to: '/console', label: '控制台日志', icon: icons.terminal },
      { to: '/msglogs', label: '消息与操作日志', icon: icons.chat, match: ['/msglogs', '/oplogs'] },
      { to: '/querylogs', label: 'AI 运行记录', icon: icons.query, match: ['/querylogs', '/agent-tasks'] },
    ],
  },
  {
    label: 'AI 能力',
    items: [
      { to: '/tasklogs', label: 'AI 定时任务', icon: icons.task },
      { to: '/memory', label: '记忆与知识库', icon: icons.memory, match: ['/memory', '/knowledge'] },
      { to: '/skills', label: '技能管理', icon: icons.skills },
      { to: '/team', label: 'Agent 团队', icon: icons.team },
    ],
  },
  {
    label: '系统',
    items: [
      { to: '/config', label: '配置管理', icon: icons.config, match: ['/config', '/files'] },
      { to: '/contacts', label: '通讯录', icon: icons.contacts },
      { to: '/marketplace', label: '插件市场', icon: icons.store },
      { to: '/update', label: '自动更新', icon: icons.update },
    ],
  },
]

// 当前路由是否属于该入口（合并页按其 match 列表判断）
function isNavActive(item) {
  return (item.match || [item.to]).includes(route.path)
}

const showPwd = ref(false)
const pwdForm = reactive({ next: '', msg: '', ok: false })

// ---------- 重启等待状态（全局横幅 + 进程重启自动刷新）----------
// 重启默认会等待进行中的消息 / AI 请求 / 后台任务完成；等待期间在后端
// sysrestart 中保持 pending，这里轮询展示任务明细并提供强制重启入口。
const restart = reactive({
  pending: false,
  busy: 0,
  items: [],
  source: '',
  reason: '',
  timeout_sec: 0,
  remaining_sec: -1,
  forced: false,
  started_at: 0,
})
const forcingRestart = ref(false)
const restartBannerEl = ref(null)
let restartTimer = null
let bootId = '' // 进程实例标识：变化说明 Bot 已重启，自动刷新页面

const restartTitle = computed(() => {
  if (!restart.pending) return ''
  if (restart.busy > 0) return `正在等待重启：${restart.busy} 个任务处理中`
  return '正在重启 Bot…'
})
const restartSubtitle = computed(() => {
  if (!restart.pending) return ''
  const parts = []
  if (restart.reason) parts.push(restart.reason)
  if (restart.forced) {
    parts.push('已请求强制重启')
  } else if (restart.timeout_sec > 0 && restart.remaining_sec >= 0) {
    parts.push(`${restart.remaining_sec} 秒后自动强制重启`)
  } else if (restart.timeout_sec === 0) {
    parts.push('将一直等待到全部任务完成')
  }
  parts.push('重启后页面会自动刷新')
  return parts.join(' · ')
})

// elapsedText 把开始时间格式化为「已进行 12 秒」。
function elapsedText(since) {
  if (!since) return '刚刚'
  const ms = Date.now() - new Date(since).getTime()
  const sec = Math.max(0, Math.round(ms / 1000))
  if (sec < 60) return `${sec} 秒`
  const min = Math.floor(sec / 60)
  return `${min} 分 ${sec % 60} 秒`
}

// pollRestart 轮询重启状态：发现进程实例变化即刷新页面；
// 等待中更新横幅；等待被取消（重启未能启动）时提示。
async function pollRestart() {
  if (!auth.loggedIn || auth.setupRequired || document.hidden) return
  try {
    const s = await api.getRestartStatus()
    if (bootId && s.boot && s.boot !== bootId) {
      location.reload()
      return
    }
    if (s.boot) bootId = s.boot
    const wasPending = restart.pending
    Object.assign(restart, s)
    if (wasPending && !s.pending) {
      Notification.error({ message: '重启未能启动，已取消本次重启等待' })
    }
  } catch {
    // 服务可能正在重启：等新进程回来后会因 boot 变化自动刷新
  }
}

function startRestartPoll() {
  if (restartTimer) clearInterval(restartTimer)
  pollRestart()
  restartTimer = setInterval(pollRestart, 2000)
}

function focusRestartBanner() {
  restartBannerEl.value?.scrollIntoView({ behavior: 'smooth', block: 'center' })
}

// onRestart 点击侧边栏「重启 Bot」：等待进行中的任务结束后重启。
async function onRestart() {
  if (restart.pending) {
    if (!await confirmAction('Bot 正在等待重启：仍有任务处理中。确定要立即强制重启吗？\n\n未完成的回复与工具调用会被中断。')) return
    await onForceRestart()
    return
  }
  if (!await confirmAction('确定要重启 Bot 吗？配置修改将在重启后生效。\n\n有正在处理的消息或 AI 请求时会先等它们完成，也可以稍后在顶部提示中强制重启。')) return
  try {
    const r = await api.restart(false)
    if (r.status) {
      if (r.status.boot) bootId = r.status.boot
      Object.assign(restart, r.status)
    }
    // 演示模式没有真实进程重启，直接刷新模拟
    if (isDemo) setTimeout(() => location.reload(), 800)
  } catch {
    // 进程可能已开始重启，交给轮询判断
  }
}

// onForceRestart 立即强制重启（跳过等待进行中的任务）。
async function onForceRestart() {
  if (forcingRestart.value) return
  forcingRestart.value = true
  try {
    const r = await api.restart(true)
    if (r.status) Object.assign(restart, r.status)
  } catch {
    // 进程可能已开始重启
  } finally {
    forcingRestart.value = false
  }
}

// 移动端抽屉侧边栏：切页后自动收起
const mobileOpen = ref(false)
watch(() => router.currentRoute.value.path, () => {
  mobileOpen.value = false
})

onMounted(() => {
  api.checkLogin()
  startRestartPoll()
})

onUnmounted(() => {
  if (restartTimer) clearInterval(restartTimer)
})

async function onLogout() {
  await api.logout()
  router.push('/')
}

async function onChangePwd() {
  pwdForm.msg = ''
  if (!pwdForm.next || pwdForm.next.length < 6) {
    pwdForm.ok = false
    pwdForm.msg = '新密码至少 6 位'
    return
  }
  try {
    await api.changePassword(pwdForm.next)
    // 修改密码后服务端会销毁所有会话，退回登录页重新登录
    showPwd.value = false
    pwdForm.next = ''
    auth.notice = '密码已更新，请使用新密码重新登录'
    auth.loggedIn = false
  } catch (e) {
    pwdForm.ok = false
    pwdForm.msg = e.message
  }
}
</script>

<style scoped>
/* 侧边栏：背景、描边与文字色全部由外观设置注入的令牌决定（见 src/appearance.js），
   所以换侧栏风格 / 主题色时无需改模板。 */
.sidebar-island {
  background: var(--sidebar-bg);
  border-right: 2px solid var(--sidebar-border);
  box-shadow: 10px 0 30px -26px rgb(61 52 40 / 0.9);
}

/* 品牌区 */
.brand-name {
  font-size: 15px;
  font-weight: 800;
  line-height: 1.15;
  letter-spacing: 0.02em;
  color: var(--sidebar-ink-strong);
}
.brand-sub {
  margin-top: 2px;
  font-size: 10px;
  line-height: 1.2;
  letter-spacing: 0.12em;
  color: var(--sidebar-ink);
  opacity: 0.85;
}
.nav-close {
  padding: 4px;
  border-radius: 10px;
  color: var(--sidebar-ink);
  transition: color 0.15s, background-color 0.15s;
}
.nav-close:hover {
  color: var(--sidebar-ink-strong);
  background: var(--sidebar-hover-bg);
}

/* 演示模式徽章 */
.demo-badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 999px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.02em;
  background: var(--warn-soft);
  color: var(--warn-ink);
  border: 1.5px solid var(--warn-line);
}

.nav-divider {
  height: 2px;
  border-radius: 999px;
  background: var(--sidebar-divider);
}

/* 顶栏「重启等待中」小标 + 内容区重启等待横幅（有任务进行中时显示，
   可查看明细并强制重启） */
.restart-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 10px;
  border-radius: 999px;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.02em;
  color: var(--warn-ink);
  background: var(--warn-soft);
  border: 1.5px solid var(--warn-line);
  transition: filter 0.15s;
}
.restart-chip:hover {
  filter: brightness(0.97);
}
.restart-banner {
  border-radius: var(--r-card);
  background: var(--warn-soft);
  border: 2px solid var(--warn-line);
  box-shadow: var(--shadow-sm);
  backdrop-filter: blur(6px);
  -webkit-backdrop-filter: blur(6px);
}

/* 导航分组标题：小号字距标签，弱化到「分隔」层级，不抢选中项的视觉 */
.nav-group-label {
  padding: 10px 12px 4px;
  font-size: 10px;
  font-weight: 800;
  line-height: 1.4;
  letter-spacing: 0.16em;
  color: var(--sidebar-ink);
  opacity: 0.6;
  user-select: none;
}
.nav-group:first-child .nav-group-label {
  padding-top: 2px;
}

/* 顶部工具栏：纸张色贴纸条 */
.topbar-island {
  border-radius: var(--r-card);
  background: var(--surface-topbar);
  border: 2px solid var(--line);
  box-shadow: var(--shadow-sm);
  backdrop-filter: blur(6px);
  -webkit-backdrop-filter: blur(6px);
}

/* 普通导航项 */
.nav-link {
  color: var(--sidebar-ink);
}
.nav-link:hover {
  color: var(--sidebar-ink-strong);
  background: var(--sidebar-hover-bg);
}

/* 当前导航项：主题色 pill + 自动适配的文字色 */
.nav-active {
  color: var(--nav-active-ink);
  font-weight: 700;
  background: var(--nav-active-bg);
  box-shadow: 0 2px 0 0 var(--nav-active-shadow), inset 0 1px 0 rgb(255 255 255 / 0.35);
}
.nav-active-dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
  background: var(--nav-active-ink);
  opacity: 0.85;
  box-shadow: 0 0 0 2px rgb(255 255 255 / 0.3);
}

/* 侧边栏底部工具条 */
.nav-footer {
  border-top: 1.5px solid var(--sidebar-divider);
}

/* 侧边栏底部小按钮（图标 + hover 提示） */
.nav-ico {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 2.25rem;
  height: 2.25rem;
  border-radius: 12px;
  color: var(--sidebar-ink);
  transition: color 0.15s, background-color 0.15s;
}
.nav-ico:hover {
  color: var(--sidebar-ink-strong);
  background: var(--sidebar-hover-bg);
}
.nav-ico .tip {
  position: absolute;
  bottom: calc(100% + 0.5rem);
  left: 50%;
  z-index: 20;
  padding: 0.3rem 0.6rem;
  border-radius: 999px;
  background: var(--sidebar-tip-bg);
  color: var(--sidebar-tip-ink);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.04em;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transform: translateX(-50%) translateY(3px);
  transition: opacity 0.15s, transform 0.15s;
}
.nav-ico:hover .tip {
  opacity: 1;
  transform: translateX(-50%) translateY(0);
}
</style>
