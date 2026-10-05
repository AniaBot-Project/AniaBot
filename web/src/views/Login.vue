<template>
  <div class="min-h-screen flex items-center justify-center relative overflow-hidden select-none p-4">
    <!-- 背景光斑 + 点阵网格（页面底色由全局极光背景提供） -->
    <div class="absolute -top-40 -left-40 w-125 h-125 rounded-full bg-indigo-400/25 blur-[120px]" />
    <div class="absolute -bottom-40 -right-40 w-125 h-125 rounded-full bg-sky-400/25 blur-[120px]" />
    <div
      class="absolute inset-0 pointer-events-none"
      style="background-image: radial-gradient(rgba(30, 41, 90, 0.06) 1px, transparent 1px); background-size: 28px 28px;"
    />

    <!-- 终端窗口式登录卡片：深色磨砂玻璃，呼应控制台输出 -->
    <form
      class="login-glass relative w-135 max-w-[92vw] rounded-2xl overflow-hidden"
      @submit.prevent="onSubmit"
    >
      <!-- 终端标题栏 -->
      <div class="flex items-center gap-1.5 px-4 py-3 bg-white/5 border-b border-white/10" aria-hidden="true">
        <span class="w-3 h-3 rounded-full bg-[#ff5f57]" />
        <span class="w-3 h-3 rounded-full bg-[#febc2e]" />
        <span class="w-3 h-3 rounded-full bg-[#28c840]" />
        <span class="ml-3 font-mono text-xs text-slate-400">ania@bot: ~/console</span>
      </div>

      <div class="p-7 space-y-6">
        <!-- 控制台同款 ASCII 标识（矢量版，见 components/AsciiLogo.vue） -->
        <AsciiLogo class="mx-auto w-full max-w-[460px]" />

        <div class="flex items-center gap-3">
          <span class="h-px flex-1 bg-white/12" />
          <h1 class="text-[11px] tracking-[0.35em] text-slate-300 font-medium whitespace-nowrap">AniaBot 控制面板</h1>
          <span class="h-px flex-1 bg-white/12" />
        </div>

        <!-- 模拟启动日志 -->
        <div class="font-mono text-[11px] leading-relaxed space-y-1" aria-hidden="true">
          <p class="text-slate-500"># 初始密码见首次启动时的控制台输出</p>
          <p><span class="text-emerald-400">[ OK ]</span> <span class="text-slate-400">插件系统就绪</span></p>
          <p><span class="text-emerald-400">[ OK ]</span> <span class="text-slate-400">Web 控制面板已启动</span></p>
          <p><span class="text-amber-400">[ .. ]</span> <span class="text-slate-400">等待管理员认证<span class="cursor-blink">▋</span></span></p>
        </div>

        <!-- 终端提示符式密码输入 -->
        <div class="flex items-center gap-2.5 font-mono">
          <span class="text-emerald-400 text-sm select-none" aria-hidden="true">➜</span>
          <input
            v-model="password"
            type="password"
            placeholder="请输入密码"
            required
            autofocus
            aria-label="密码"
            class="flex-1 min-w-0 bg-transparent! border-0! border-b! border-white/20! focus:border-emerald-400/70! shadow-none! rounded-none! px-1 py-1.5 text-sm text-white placeholder-slate-500 focus:outline-none transition-colors"
          />
        </div>

        <p v-if="auth.notice" class="font-mono text-xs text-emerald-400">✓ {{ auth.notice }}</p>
        <p v-if="error" class="font-mono text-xs text-red-400">✗ {{ error }}</p>

        <button
          type="submit"
          :disabled="loading"
        class="w-full py-2.5 btn-accent rounded-xl text-sm"
        >
          {{ loading ? '登录中...' : '登录' }}
        </button>
      </div>
    </form>
  </div>
</template>

<script setup>
import { ref } from 'vue'
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
/* 深色磨砂玻璃登录卡片：透出底层极光，边缘一道高光棱线 */
.login-glass {
  background: linear-gradient(165deg, rgb(30 36 58 / 0.82) 0%, rgb(14 18 32 / 0.86) 100%);
  border: 1px solid rgb(255 255 255 / 0.14);
  box-shadow:
    0 40px 80px -40px rgb(15 23 42 / 0.75),
    0 2px 0 0 rgb(255 255 255 / 0.06) inset,
    0 0 0 1px rgb(255 255 255 / 0.04) inset;
  backdrop-filter: blur(28px) saturate(180%);
  -webkit-backdrop-filter: blur(28px) saturate(180%);
}

/* 终端光标闪烁 */
.cursor-blink {
  display: inline-block;
  margin-left: 2px;
  animation: cursor-blink 1.1s steps(1) infinite;
}
@keyframes cursor-blink {
  50% {
    opacity: 0;
  }
}
</style>
