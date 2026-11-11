<template>
  <div class="app-shell relative min-h-screen flex items-center justify-center overflow-hidden select-none p-4">
    <!-- 背景装饰：薄荷青绿 / 鹅黄柔光斑（页面底色与点阵由全局提供） -->
    <div class="absolute -top-44 -left-44 w-125 h-125 rounded-full bg-[#19c8b9]/10 blur-[110px] pointer-events-none" />
    <div class="absolute -bottom-44 -right-44 w-125 h-125 rounded-full bg-[#f7cd67]/16 blur-[110px] pointer-events-none" />

    <!-- 登录卡片：奶油小岛卡片 -->
    <form
      class="login-card relative w-135 max-w-[92vw] rounded-3xl overflow-hidden"
      @submit.prevent="onSubmit"
    >
      <!-- 顶部彩点条：海岛红 / 鹅黄 / 青草绿 -->
      <div class="flex items-center gap-1.5 px-5 py-3.5 bg-black/[0.03] border-b border-black/5" aria-hidden="true">
        <span class="w-3 h-3 rounded-full bg-[#fc736d]" />
        <span class="w-3 h-3 rounded-full bg-[#f7cd67]" />
        <span class="w-3 h-3 rounded-full bg-[#8ac68a]" />
        <span class="ml-3 font-mono text-[11px] text-slate-500">ania@island: ~/console</span>
      </div>

      <div class="p-7 space-y-6">
        <!-- 控制台同款 ASCII 标识（矢量版，见 components/AsciiLogo.vue） -->
        <AsciiLogo class="mx-auto w-full max-w-[440px]" />

        <div class="flex items-center gap-3">
          <span class="h-0.5 flex-1 rounded-full bg-black/8" />
          <h1 class="text-[12px] font-bold tracking-[0.06em] text-slate-500 whitespace-nowrap">AniaBot 控制面板</h1>
          <span class="h-0.5 flex-1 rounded-full bg-black/8" />
        </div>

        <p class="text-center text-[11.5px] leading-relaxed text-slate-400">
          初始密码见首次启动时的控制台输出
        </p>

        <!-- 密码输入：奶油 pill 输入框 + 鹅黄焦点圈 -->
        <input
          v-model="password"
          type="password"
          placeholder="请输入密码"
          required
          autofocus
          aria-label="密码"
          class="w-full px-5 py-3 text-sm"
        />

        <p v-if="auth.notice" class="text-xs text-center text-[#34c759]">✓ {{ auth.notice }}</p>
        <p v-if="error" class="text-xs text-center text-[#ff3b30]">✗ {{ error }}</p>

        <Button type="primary" size="large" block html-type="submit" :loading="loading">
          {{ loading ? '登录中…' : '登 录' }}
        </Button>

        <p class="text-center text-[10.5px] tracking-[0.08em] text-slate-300">ANIABOT · ISLAND CONSOLE</p>
      </div>
    </form>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { Button } from 'animal-island-vue'
import { api, auth } from '../api.js'
import AsciiLogo from '../components/AsciiLogo.vue'

const password = ref('')
const error = ref('')
const loading = ref(false)

async function onSubmit() {
  error.value = ''
  auth.notice = ''
  loading.value = true
  try {
    await api.login(password.value)
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
/* 奶油小岛登录卡片：2px 暖色描边 + 柔和暖阴影 */
.login-card {
  background: var(--surface);
  border: 2px solid var(--line);
  box-shadow: 0 18px 40px -22px rgb(61 52 40 / 0.45), 0 2px 0 0 rgb(255 255 255 / 0.9) inset;
}

/* 主按钮沿用主题库的像素级 3D 按键形态，配色换成面板主色薄荷青绿 */
.login-card :deep(.animal-btn--primary) {
  background: var(--accent);
  border-color: var(--accent);
  color: #fff;
  box-shadow: 0 5px 0 0 var(--accent-strong);
}
.login-card :deep(.animal-btn--primary:hover:not(:disabled)) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
  box-shadow: 0 6px 0 0 var(--accent-strong);
}
.login-card :deep(.animal-btn--primary:active:not(:disabled)) {
  background: var(--accent-strong);
  border-color: var(--accent-strong);
  box-shadow: 0 1px 0 0 var(--accent-strong);
}
</style>
