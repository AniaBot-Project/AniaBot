<template>
  <div v-if="!auth.checked" class="app-shell min-h-screen flex items-center justify-center">
    <div class="flex items-center gap-2 text-slate-400 text-sm">
      <span class="w-4 h-4 border-2 border-black/10 border-t-[#0071e3] rounded-full animate-spin" />
      加载中...
    </div>
  </div>

  <Login v-else-if="!auth.loggedIn" />

  <Wizard v-else-if="auth.setupRequired" />

  <div v-else class="app-shell min-h-screen flex">
    <!-- 移动端抽屉遮罩 -->
    <Transition name="fade">
      <div v-if="mobileOpen" class="fixed inset-0 bg-black/30 backdrop-blur-md z-40 md:hidden" @click="mobileOpen = false" />
    </Transition>

    <!-- 侧边导航：深色玻璃，移动端为抽屉，桌面端常驻 -->
    <aside
      class="sidebar-glass fixed inset-y-0 left-0 z-50 w-64 h-screen flex flex-col shrink-0 transition-transform duration-200 md:sticky md:top-0 md:inset-auto md:z-auto md:w-64"
      :class="mobileOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0'"
    >
      <div class="px-5 pt-6 pb-5 flex items-center gap-3">
        <img src="/logo.webp" alt="AniaBot" class="w-9 h-9 rounded-full object-cover shadow-md ring-1 ring-black/5" />
        <div>
          <div class="text-[#1d1d1f] font-semibold leading-tight tracking-[0.2em]">ANIABOT</div>
          <div class="text-[10px] text-slate-500 leading-tight tracking-[0.15em] uppercase mt-0.5">Console · 控制面板</div>
        </div>
        <button class="ml-auto md:hidden text-slate-500 hover:text-slate-900 p-1" aria-label="关闭菜单" @click="mobileOpen = false">
          <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12" /></svg>
        </button>
      </div>
      <div class="mx-5 h-px bg-black/5" />

      <nav class="flex-1 px-3 py-3 space-y-1 overflow-y-auto">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="group relative flex items-center gap-3 px-3 py-2.5 rounded-xl text-[11px] tracking-[0.15em] border transition-all duration-200"
          :class="$route.path === item.to
            ? 'nav-active'
            : 'border-transparent text-slate-600 hover:text-slate-900 hover:bg-black/5'"
        >
          <span v-html="item.icon" class="shrink-0 [&>svg]:w-4 [&>svg]:h-4" />
          {{ item.label }}
          <span v-if="$route.path === item.to" class="ml-auto w-1.5 h-1.5 rounded-full bg-[#0071e3] shadow-[0_0_8px_2px_rgba(0,113,227,0.35)]" />
        </RouterLink>
      </nav>

      <div class="px-4 py-4 border-t border-black/5 flex items-center justify-between">
        <a class="nav-ico" href="https://github.com/jeanhua/AniaBot" target="_blank" rel="noopener noreferrer" aria-label="GitHub 仓库">
          <span v-html="icons.github" class="[&>svg]:w-4 [&>svg]:h-4" />
          <span class="tip">GitHub</span>
        </a>
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
      <!-- 悬浮玻璃工具栏 -->
      <header class="topbar-glass sticky top-3 z-30 mx-3 sm:mx-5 xl:mx-7 mt-3 px-4 sm:px-5 py-3 flex items-center justify-between gap-3">
        <div class="flex items-center gap-2 min-w-0">
          <button class="md:hidden -ml-1 p-1.5 rounded-lg text-slate-600 hover:bg-white/60 transition-colors" aria-label="打开菜单" @click="mobileOpen = true">
            <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6.75h16.5M3.75 12h16.5m-16.5 5.25h16.5" /></svg>
          </button>
          <h1 class="text-[11px] tracking-[0.22em] uppercase text-slate-500 font-medium truncate">
            <span class="text-slate-300 mr-2">//</span>{{ $route.meta.title || '' }}
          </h1>
        </div>
        <span class="tpill shrink-0"><span class="tdot bg-[#34c759] pulse-dot" />Bot Online</span>
      </header>
      <div class="px-3 sm:px-5 xl:px-7 py-4 sm:py-5 flex-1">
        <RouterView v-slot="{ Component }">
          <Transition name="fade" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </div>
    </main>

    <!-- 重启中遮罩 -->
    <div v-if="restarting" class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4">
      <div class="tcard p-8 w-80 max-w-full text-center space-y-3">
        <span class="mx-auto block w-8 h-8 border-[3px] border-black/10 border-t-[#0071e3] rounded-full animate-spin" />
        <div class="text-sm font-semibold text-slate-900 tracking-[0.15em] uppercase">Rebooting</div>
        <p class="text-xs text-slate-500">配置修改将在重启后生效，恢复后页面自动刷新</p>
      </div>
    </div>

    <!-- 修改密码弹窗 -->
    <div v-if="showPwd" class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4" v-backdrop-close="() => (showPwd = false)">
      <form class="tcard p-6 w-96 max-w-full space-y-4" @submit.prevent="onChangePwd">
        <h2 class="text-[11px] tracking-[0.22em] uppercase text-slate-500 font-medium">修改密码</h2>
        <input v-model="pwdForm.next" type="password" placeholder="新密码（至少 6 位）" required minlength="6" :class="inputClass" />
        <p v-if="pwdForm.msg" class="text-xs" :class="pwdForm.ok ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ pwdForm.msg }}</p>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="px-4 py-2 text-[11px] tracking-widest uppercase text-slate-500 hover:bg-black/5 rounded-lg transition-colors" @click="showPwd = false">取消</button>
        <button type="submit" class="btn-accent px-4 py-2 text-[11px] tracking-widest uppercase rounded-lg">保存</button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, auth } from './api.js'
import Login from './views/Login.vue'
import Wizard from './views/Wizard.vue'

const router = useRouter()

const icons = {
  github: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.11.79-.25.79-.55 0-.27-.01-1.17-.02-2.12-3.2.7-3.88-1.36-3.88-1.36-.52-1.33-1.28-1.68-1.28-1.68-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.03 1.75 2.69 1.25 3.34.95.1-.74.4-1.25.72-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.28 1.18-3.09-.12-.29-.51-1.46.11-3.05 0 0 .96-.31 3.16 1.18a11 11 0 0 1 5.76 0c2.2-1.49 3.16-1.18 3.16-1.18.62 1.59.23 2.76.11 3.05.73.81 1.18 1.83 1.18 3.09 0 4.42-2.7 5.39-5.27 5.67.41.36.78 1.06.78 2.14 0 1.54-.01 2.78-.01 3.16 0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z"/></svg>',
  dashboard: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6A2.25 2.25 0 0 1 6 3.75h2.25A2.25 2.25 0 0 1 10.5 6v2.25a2.25 2.25 0 0 1-2.25 2.25H6a2.25 2.25 0 0 1-2.25-2.25V6ZM3.75 15.75A2.25 2.25 0 0 1 6 13.5h2.25a2.25 2.25 0 0 1 2.25 2.25V18a2.25 2.25 0 0 1-2.25 2.25H6A2.25 2.25 0 0 1 3.75 18v-2.25ZM13.5 6a2.25 2.25 0 0 1 2.25-2.25H18A2.25 2.25 0 0 1 20.25 6v2.25A2.25 2.25 0 0 1 18 10.5h-2.25a2.25 2.25 0 0 1-2.25-2.25V6ZM13.5 15.75a2.25 2.25 0 0 1 2.25-2.25H18a2.25 2.25 0 0 1 2.25 2.25V18A2.25 2.25 0 0 1 18 20.25h-2.25A2.25 2.25 0 0 1 13.5 18v-2.25Z"/></svg>',
  config: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z"/><path stroke-linecap="round" stroke-linejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z"/></svg>',
  files: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M17.25 6.75 22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3-4.5 16.5"/></svg>',
  contacts: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z"/></svg>',
  chat: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M20.25 8.511c.884.284 1.5 1.128 1.5 2.097v4.286c0 1.136-.847 2.1-1.98 2.193-.34.027-.68.052-1.02.072v3.091l-3-3c-1.354 0-2.694-.055-4.02-.163a2.115 2.115 0 0 1-.825-.242m9.345-8.334a2.126 2.126 0 0 0-.476-.095 48.64 48.64 0 0 0-8.048 0c-1.131.094-1.976 1.057-1.976 2.192v4.286c0 .837.46 1.58 1.155 1.951m9.345-8.334V6.637c0-1.621-1.152-3.026-2.76-3.235A48.455 48.455 0 0 0 11.25 3c-2.115 0-4.198.137-6.24.402-1.608.209-2.76 1.614-2.76 3.235v6.226c0 1.621 1.152 3.026 2.76 3.235.577.075 1.157.14 1.74.194V21l4.155-4.155"/></svg>',
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
  update: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9 8.25H7.5a2.25 2.25 0 0 0-2.25 2.25v9a2.25 2.25 0 0 0 2.25 2.25h9a2.25 2.25 0 0 0 2.25-2.25v-9a2.25 2.25 0 0 0-2.25-2.25H15m0-3-3-3m0 0-3 3m3-3V15"/></svg>',
  store: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M13.5 21v-7.5a.75.75 0 0 1 .75-.75h3a.75.75 0 0 1 .75.75V21m-4.5 0H2.36m11.14 0H18m0 0h3.64m-1.39 0V9.349M3.75 21V9.349m0 0a3.001 3.001 0 0 0 3.75-.615A2.993 2.993 0 0 0 9.75 9.75c.896 0 1.7-.393 2.25-1.016a2.993 2.993 0 0 0 2.25 1.016c.896 0 1.7-.393 2.25-1.015a3.001 3.001 0 0 0 3.75.614m-16.5 0a3.004 3.004 0 0 1-.621-4.72l1.189-1.19A1.5 1.5 0 0 1 5.378 3h13.243a1.5 1.5 0 0 1 1.06.44l1.19 1.189a3 3 0 0 1-.621 4.72M6.75 18h3.75a.75.75 0 0 0 .75-.75V13.5a.75.75 0 0 0-.75-.75H6.75a.75.75 0 0 0-.75.75v3.75c0 .414.336.75.75.75Z"/></svg>',
  restart: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99"/></svg>',
  key: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z"/></svg>',
  logout: '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M15.75 9V5.25A2.25 2.25 0 0 0 13.5 3h-6a2.25 2.25 0 0 0-2.25 2.25v13.5A2.25 2.25 0 0 0 7.5 21h6a2.25 2.25 0 0 0 2.25-2.25V15m3 0 3-3m0 0-3-3m3 3H9"/></svg>',
}

const navItems = [
  { to: '/', label: '状态总览', icon: icons.dashboard },
  { to: '/console', label: '控制台日志', icon: icons.terminal },
  { to: '/oplogs', label: '操作日志', icon: icons.audit },
  { to: '/msglogs', label: '消息日志', icon: icons.chat },
  { to: '/querylogs', label: 'Query 日志', icon: icons.query },
  { to: '/tokenstats', label: 'Token 统计', icon: icons.chart },
  { to: '/quota', label: '配额管理', icon: icons.gauge },
  { to: '/tasklogs', label: '定时任务', icon: icons.task },
  { to: '/skills', label: '技能管理', icon: icons.skills },
  { to: '/memory', label: '记忆管理', icon: icons.memory },
  { to: '/knowledge', label: '知识库', icon: icons.book },
  { to: '/team', label: 'Agent 团队', icon: icons.team },
  { to: '/config', label: '配置管理', icon: icons.config },
  { to: '/files', label: '扩展配置', icon: icons.files },
  { to: '/contacts', label: '通讯录', icon: icons.contacts },
  { to: '/marketplace', label: '插件市场', icon: icons.store },
  { to: '/update', label: '自动更新', icon: icons.update },
]

const inputClass = 'w-full border rounded-lg px-3 py-2 text-xs focus:outline-none focus:ring-2 transition-shadow'

const showPwd = ref(false)
const pwdForm = reactive({ next: '', msg: '', ok: false })
const restarting = ref(false)

// 移动端抽屉侧边栏：切页后自动收起
const mobileOpen = ref(false)
watch(() => router.currentRoute.value.path, () => {
  mobileOpen.value = false
})

onMounted(() => api.checkLogin())

async function onLogout() {
  await api.logout()
  router.push('/')
}

async function onRestart() {
  if (!confirm('确定要重启 Bot 吗？配置修改将在重启后生效。')) return
  restarting.value = true
  try {
    await api.restart()
  } catch { /* 进程可能已开始退出 */ }
  const up = await api.waitUntilUp()
  if (up) {
    location.reload()
  } else {
    restarting.value = false
    alert('等待重启超时，请检查 Bot 运行状态')
  }
}

async function onChangePwd() {
  pwdForm.msg = ''
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
/* 侧边栏：浅色磨砂玻璃（Apple 侧栏质感） */
.sidebar-glass {
  background: linear-gradient(180deg, rgb(255 255 255 / 0.82) 0%, rgb(255 255 255 / 0.7) 100%);
  border-right: 1px solid rgb(0 0 0 / 0.06);
  box-shadow: 16px 0 40px -38px rgb(0 0 0 / 0.4);
  backdrop-filter: blur(26px) saturate(180%);
  -webkit-backdrop-filter: blur(26px) saturate(180%);
}

/* 顶部工具栏：悬浮玻璃条 */
.topbar-glass {
  border-radius: 16px;
  background: rgb(255 255 255 / 0.72);
  border: 1px solid rgb(0 0 0 / 0.06);
  box-shadow: 0 8px 26px -24px rgb(0 0 0 / 0.45), inset 0 1px 0 rgb(255 255 255 / 0.8);
  backdrop-filter: blur(20px) saturate(180%);
  -webkit-backdrop-filter: blur(20px) saturate(180%);
}

/* 当前导航项：淡淡一层系统蓝 + 蓝色文字（Apple 侧栏选中态） */
.nav-active {
  color: #0066cc;
  font-weight: 600;
  border-color: rgb(0 113 227 / 0.14);
  background: rgb(0 113 227 / 0.1);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.7);
}

/* 侧边栏底部小按钮（图标 + hover 提示） */
.nav-ico {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  border-radius: 0.625rem;
  color: rgb(110 110 115);
  transition: color 0.15s, background-color 0.15s, box-shadow 0.15s;
}
.nav-ico:hover {
  color: rgb(29 29 31);
  background: rgb(0 0 0 / 0.05);
}
.nav-ico .tip {
  position: absolute;
  bottom: calc(100% + 0.5rem);
  left: 50%;
  z-index: 20;
  padding: 0.3rem 0.55rem;
  border-radius: 0.5rem;
  background: rgb(29 29 31 / 0.92);
  border: 1px solid rgb(255 255 255 / 0.14);
  color: rgb(245 245 247);
  font-size: 10px;
  letter-spacing: 0.12em;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transform: translateX(-50%) translateY(3px);
  transition: opacity 0.15s, transform 0.15s;
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
}
.nav-ico:hover .tip {
  opacity: 1;
  transform: translateX(-50%) translateY(0);
}
</style>

