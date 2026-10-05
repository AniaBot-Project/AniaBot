<template>
  <section v-if="visible" class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
    <div class="w-full flex items-center justify-between gap-3 px-6 py-4">
      <span class="flex items-center gap-2.5 min-w-0">
        <span class="w-7 h-7 rounded-lg flex items-center justify-center text-white text-xs shrink-0 bg-emerald-500">微</span>
        <span class="text-sm font-semibold text-slate-800 truncate">微信扫码登录</span>
        <span class="text-xs font-normal text-slate-400 hidden sm:inline">iLink bot 无静态 Token，扫码即完成授权</span>
      </span>
      <button
        v-if="ready && (state === 'idle' || state === 'failed' || state === 'connected' || !state)"
        :disabled="starting"
        class="shrink-0 px-3 py-1.5 text-xs rounded-lg btn-accent disabled:opacity-40 transition-colors"
        @click="start"
      >
        {{ starting ? '获取二维码中…' : state === 'connected' ? '重新扫码' : '扫码登录' }}
      </button>
    </div>

    <div class="px-6 pb-6 border-t border-white/50 pt-5 space-y-4">
      <!-- 平台未生效：引导启用/重启 -->
      <div v-if="!ready" class="bg-amber-50 border border-amber-200 text-amber-800 text-sm rounded-xl px-4 py-3 space-y-1">
        <p v-if="weixinEnabled">
          微信平台已启用，<b>重启 Bot 后</b>即可在此扫码登录（面板修改配置均需重启生效）。
        </p>
        <p v-else>
          尚未启用微信平台：在下方「平台适配器」分类中勾选 <b>启用微信平台</b> 并保存、重启 Bot，然后回到本页扫码登录。
        </p>
        <p class="text-xs text-amber-600/80">也可在 Bot 控制台扫码（启用并重启后控制台会打印二维码）。</p>
      </div>

      <template v-else>
        <!-- 等待扫码 / 已扫码 / 待配对：展示二维码 -->
        <div v-if="state === 'pending' || state === 'scaned' || state === 'need_verify'" class="flex flex-col items-center gap-3">
          <img v-if="qr" :src="qr" alt="微信登录二维码" class="w-52 h-52 rounded-lg border border-white/60" />
          <p class="text-sm text-slate-600">
            {{ state === 'pending' ? '请用手机微信扫描二维码，并在手机上确认授权' : '' }}
            {{ state === 'scaned' ? '已扫码，请在手机上确认授权' : '' }}
            {{ state === 'need_verify' ? '请在手机微信上查看显示的数字并输入' : '' }}
          </p>
          <div v-if="state === 'need_verify'" class="flex items-center gap-2">
            <input
              v-model="verifyCode"
              type="text"
              inputmode="numeric"
              maxlength="12"
              placeholder="手机上显示的数字"
              class="w-40 border border-slate-300/60 rounded-lg px-3 py-2 text-sm text-center tracking-widest focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400"
              @keyup.enter="submitVerify"
            />
            <button
              :disabled="!verifyCode.trim() || verifying"
              class="px-3 py-2 text-sm rounded-lg btn-accent disabled:opacity-40 transition-colors"
              @click="submitVerify"
            >
              {{ verifying ? '提交中…' : '提交' }}
            </button>
          </div>
          <p class="text-xs text-slate-400">二维码 8 分钟内有效，过期自动刷新；也可在 Bot 控制台扫码</p>
        </div>

        <!-- 成功 -->
        <div v-else-if="state === 'connected'" class="bg-emerald-50 border border-emerald-200 text-emerald-800 text-sm rounded-xl px-4 py-3">
          ✅ 登录成功，凭据已保存。
          <span v-if="detail"> {{ detail }}</span>
        </div>

        <!-- 失败 -->
        <div v-else-if="state === 'failed'" class="bg-red-50 border border-red-200 text-red-700 text-sm rounded-xl px-4 py-3">
          {{ detail || '登录未完成' }}
        </div>

        <!-- 未发起 -->
        <p v-else class="text-sm text-slate-500">
          点击「扫码登录」生成二维码，用手机微信扫码并确认即可完成授权，无需在控制台操作。
        </p>
      </template>
    </div>
  </section>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api.js'

// weixinEnabled 配置表单中「启用微信平台」当前是否勾选（含尚未重启生效的 scenario）
const props = defineProps({
  weixinEnabled: { type: Boolean, default: false },
})

const hasSource = ref(false) // 微信适配器已运行（面板可发起扫码）
const state = ref('')        // idle/pending/scaned/need_verify/connected/failed
const detail = ref('')
const qr = ref('')           // 二维码 data URL
const verifyCode = ref('')
const starting = ref(false)
const verifying = ref(false)
let pollTimer = null

const visible = computed(() => hasSource.value || props.weixinEnabled)
const ready = computed(() => hasSource.value)

const ACTIVE_STATES = ['pending', 'scaned', 'need_verify']

function applyStatus(data) {
  state.value = data.state || 'idle'
  detail.value = data.detail || ''
  if (data.qr_data_url) qr.value = data.qr_data_url
  schedulePoll()
}

function schedulePoll() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
  if (!ready.value || !ACTIVE_STATES.includes(state.value)) return
  pollTimer = setTimeout(async () => {
    try {
      applyStatus(await api.getQRLoginStatus('weixin'))
    } catch {
      schedulePoll() // 网络抖动时继续轮询
    }
  }, 2500)
}

async function start() {
  starting.value = true
  try {
    const data = await api.startQRLogin('weixin')
    state.value = 'pending'
    detail.value = ''
    if (data.qr_data_url) qr.value = data.qr_data_url
    schedulePoll()
  } catch (e) {
    state.value = 'failed'
    detail.value = e.message || '获取二维码失败'
  } finally {
    starting.value = false
  }
}

async function submitVerify() {
  const code = verifyCode.value.trim()
  if (!code) return
  verifying.value = true
  try {
    await api.submitQRLoginVerify('weixin', code)
    verifyCode.value = ''
    // 立即拉取一次状态（服务端已带码重轮询）
    applyStatus(await api.getQRLoginStatus('weixin'))
  } catch (e) {
    detail.value = e.message || '配对码提交失败'
  } finally {
    verifying.value = false
  }
}

onMounted(async () => {
  try {
    const data = await api.getQRLoginSources()
    hasSource.value = (data.sources || []).some((s) => s.platform === 'weixin')
    if (!hasSource.value) return
    // 页面刷新时恢复进行中的登录会话
    applyStatus(await api.getQRLoginStatus('weixin'))
  } catch {
    hasSource.value = false
  }
})

onUnmounted(() => {
  if (pollTimer) clearTimeout(pollTimer)
})
</script>
