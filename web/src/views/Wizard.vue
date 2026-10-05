<template>
  <div class="app-shell min-h-screen relative overflow-hidden flex items-center justify-center p-4">
    <!-- 背景光斑（页面底色由全局极光背景提供） -->
    <div class="absolute -top-40 -left-40 w-125 h-125 rounded-full bg-indigo-400/25 blur-[120px]" />
    <div class="absolute -bottom-40 -right-40 w-125 h-125 rounded-full bg-teal-400/25 blur-[120px]" />

    <div class="tcard rounded-2xl w-full max-w-lg overflow-hidden">
      <!-- 步骤指示 -->
      <div class="flex border-b border-white/60">
        <div
          v-for="(label, i) in ['欢迎', '平台接入', 'AI 配置', '完成']"
          :key="i"
          class="flex-1 py-3.5 text-center text-xs transition-colors"
          :class="i === step ? 'text-zinc-700 font-semibold border-b-2 border-indigo-500 -mb-px' : i < step ? 'text-emerald-600' : 'text-slate-400'"
        >
          {{ i + 1 }}. {{ label }}
        </div>
      </div>

      <div class="p-5 sm:p-8">
        <!-- 步骤 0: 欢迎 -->
        <div v-if="step === 0" class="space-y-5 text-center">
          <div class="mx-auto w-14 h-14 rounded-2xl bg-linear-to-br from-indigo-500 to-indigo-600 flex items-center justify-center text-white font-bold text-2xl shadow-lg">
            A
          </div>
          <h1 class="text-xl font-bold text-slate-800">欢迎使用 AniaBot 🎉</h1>
          <p class="text-sm text-slate-500 leading-relaxed">
            这是首次启动，接下来用两步完成最基本的配置：<br />
            <b>接入平台</b>（QQ / QQ 官方 / 飞书 / Telegram / Discord / 微信，可多选）和 <b>AI 对话模型</b>。<br />
            其余配置（插件、MCP、Prompt 覆盖等）可稍后在控制面板中完善。
          </p>
          <p class="text-xs text-slate-400">所有配置保存在数据库中，也可随时跳过，之后在「配置管理」中修改。</p>
        </div>

        <!-- 步骤 1: 平台接入 -->
        <div v-else-if="step === 1" class="space-y-4">
          <h2 class="text-base font-semibold text-slate-800">平台接入</h2>

          <!-- QQ(NapCat) -->
          <div :class="['border rounded-xl p-4 space-y-3 transition-colors', form.enableNapcat ? 'border-slate-300/60 bg-slate-50' : 'border-white/60']">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <input type="checkbox" v-model="form.enableNapcat" class="w-4 h-4 accent-zinc-900" />
              <span class="text-sm font-medium text-slate-700">
                QQ（NapCat）
                <span class="text-xs text-slate-400 font-normal">· OneBot v11 协议端，默认启用</span>
              </span>
            </label>
            <template v-if="form.enableNapcat">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">连接模式</label>
                <select v-model="form.mode" :class="inputClass">
                  <option value="ws">WebSocket（推荐）</option>
                  <option value="http">HTTP（Webhook 上报）</option>
                </select>
              </div>
              <div v-if="form.mode === 'ws'">
                <label class="block text-xs font-medium text-slate-600 mb-1.5">WebSocket 地址</label>
                <input v-model="form.wsAddress" type="text" placeholder="ws://localhost:4455" :class="inputClass" />
                <p class="text-xs text-slate-400 mt-1.5">NapCat 的 WebSocket 服务端地址；Docker 部署时把 localhost 换成内网 IP</p>
              </div>
              <template v-else>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP 目标地址</label>
                  <input v-model="form.httpTargetUrl" type="text" placeholder="http://localhost:6680" :class="inputClass" />
                  <p class="text-xs text-slate-400 mt-1.5">NapCat 开放的 HTTP 调用地址；Docker 部署时把 localhost 换成内网 IP</p>
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP 监听端口</label>
                  <input v-model="form.httpListenPort" type="number" placeholder="6679" :class="inputClass" />
                  <p class="text-xs text-slate-400 mt-1.5">本机端口，NapCat 的 HTTP Client 向此端口上报事件</p>
                </div>
              </template>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Access Token（可选）</label>
                <input v-model="form.token" type="password" placeholder="NapCat 端设置了 token 时填写" :class="inputClass" />
              </div>
            </template>
          </div>

          <!-- QQ(Luckylilia / LLBot) -->
          <div :class="['border rounded-xl p-4 space-y-3 transition-colors', form.enableLuckylilia ? 'border-slate-300/60 bg-slate-50' : 'border-white/60']">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <input type="checkbox" v-model="form.enableLuckylilia" class="w-4 h-4 accent-zinc-900" />
              <span class="text-sm font-medium text-slate-700">
                QQ（Luckylilia / LLBot）
                <span class="text-xs text-slate-400 font-normal">· OneBot v11 协议端，可与 NapCat 并存（第二个 QQ 账号）</span>
              </span>
            </label>
            <template v-if="form.enableLuckylilia">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">连接模式</label>
                <select v-model="form.lilMode" :class="inputClass">
                  <option value="ws">WebSocket（推荐）</option>
                  <option value="http">HTTP（Webhook 上报）</option>
                </select>
              </div>
              <div v-if="form.lilMode === 'ws'">
                <label class="block text-xs font-medium text-slate-600 mb-1.5">WebSocket 地址</label>
                <input v-model="form.lilWsAddress" type="text" placeholder="ws://localhost:3001" :class="inputClass" />
                <p class="text-xs text-slate-400 mt-1.5">LLBot 的 WebSocket 服务端地址；Docker 部署时把 localhost 换成内网 IP</p>
              </div>
              <template v-else>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">LLBot HTTP 地址</label>
                  <input v-model="form.lilHttpTargetUrl" type="text" placeholder="http://localhost:6690" :class="inputClass" />
                  <p class="text-xs text-slate-400 mt-1.5">LLBot 开放的「HTTP 服务器」地址；Docker 部署时把 localhost 换成内网 IP</p>
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP 监听端口</label>
                  <input v-model="form.lilHttpListenPort" type="number" placeholder="6689" :class="inputClass" />
                  <p class="text-xs text-slate-400 mt-1.5">本机端口，LLBot 的「HTTP 客户端」向此端口上报事件</p>
                </div>
              </template>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Access Token（可选）</label>
                <input v-model="form.lilToken" type="password" placeholder="LLBot 端设置了 token 时填写" :class="inputClass" />
              </div>
            </template>
          </div>

          <!-- QQ 官方机器人 -->
          <div :class="['border rounded-xl p-4 space-y-3 transition-colors', form.enableQQOfficial ? 'border-slate-300/60 bg-slate-50' : 'border-white/60']">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <input type="checkbox" v-model="form.enableQQOfficial" class="w-4 h-4 accent-zinc-900" />
              <span class="text-sm font-medium text-slate-700">
                QQ 官方机器人
                <span class="text-xs text-slate-400 font-normal">· QQ 开放平台官方接口，WebSocket 收事件无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableQQOfficial">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">AppID</label>
                <input v-model="form.qqofficialAppId" type="text" placeholder="机器人 AppID" :class="inputClass" />
                <p class="text-xs text-slate-400 mt-1.5">QQ 开放平台（q.qq.com）管理端「开发 → 开发设置」中的 AppID</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">AppSecret</label>
                <input v-model="form.qqofficialAppSecret" type="password" placeholder="机器人 AppSecret" :class="inputClass" />
                <p class="text-xs text-slate-400 mt-1.5">用于换取 access_token；旧版 Token 鉴权已废弃，请勿填写 Token</p>
              </div>
              <label class="flex items-center gap-2.5 cursor-pointer select-none">
                <input type="checkbox" v-model="form.qqofficialSandbox" class="w-4 h-4 accent-zinc-900" />
                <span class="text-xs text-slate-600">沙箱环境（机器人未上架前联调使用）</span>
              </label>
              <p class="text-xs text-slate-400">还需在开放平台「功能配置」中勾选群聊/单聊场景的事件订阅（WebSocket 方式）</p>
            </template>
          </div>

          <!-- 飞书(Lark) -->
          <div :class="['border rounded-xl p-4 space-y-3 transition-colors', form.enableFeishu ? 'border-slate-300/60 bg-slate-50' : 'border-white/60']">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <input type="checkbox" v-model="form.enableFeishu" class="w-4 h-4 accent-zinc-900" />
              <span class="text-sm font-medium text-slate-700">
                飞书（Lark）
                <span class="text-xs text-slate-400 font-normal">· 官方 SDK，默认长连接无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableFeishu">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">App ID</label>
                <input v-model="form.feishuAppId" type="text" placeholder="cli_xxxxxxxx" :class="inputClass" />
                <p class="text-xs text-slate-400 mt-1.5">飞书开放平台「凭证与基础信息」中的应用 App ID</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">App Secret</label>
                <input v-model="form.feishuAppSecret" type="password" placeholder="应用 App Secret" :class="inputClass" />
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">事件订阅方式</label>
                <select v-model="form.feishuMode" :class="inputClass">
                  <option value="ws">WebSocket 长连接（推荐，无需公网地址）</option>
                  <option value="webhook">Webhook（需公网 HTTPS 回调地址）</option>
                </select>
              </div>
              <template v-if="form.feishuMode === 'webhook'">
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">监听地址</label>
                  <input v-model="form.feishuWebhookListen" type="text" placeholder="127.0.0.1:7777" :class="inputClass" />
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">回调路径</label>
                  <input v-model="form.feishuWebhookPath" type="text" placeholder="/webhook/event" :class="inputClass" />
                  <p class="text-xs text-slate-400 mt-1.5">飞书后台请求地址填 https://&lt;公网地址&gt;&lt;此路径&gt;</p>
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">Verification Token（可选）</label>
                  <input v-model="form.feishuVerificationToken" type="text" placeholder="飞书「事件订阅」页配置" :class="inputClass" />
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">Encrypt Key（可选）</label>
                  <input v-model="form.feishuEncryptKey" type="password" placeholder="事件加密密钥" :class="inputClass" />
                </div>
              </template>
              <p class="text-xs text-slate-400">还需在飞书开放平台开通权限：im:message、im:message:send_as_bot、im:resource 等</p>
            </template>
          </div>

          <!-- Telegram -->
          <div :class="['border rounded-xl p-4 space-y-3 transition-colors', form.enableTelegram ? 'border-slate-300/60 bg-slate-50' : 'border-white/60']">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <input type="checkbox" v-model="form.enableTelegram" class="w-4 h-4 accent-zinc-900" />
              <span class="text-sm font-medium text-slate-700">
                Telegram
                <span class="text-xs text-slate-400 font-normal">· Bot API 长轮询，无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableTelegram">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Bot Token</label>
                <input v-model="form.telegramToken" type="password" placeholder="123456:ABC-DEF..." :class="inputClass" />
                <p class="text-xs text-slate-400 mt-1.5">向 @BotFather 创建机器人后获取；国内部署可另配代理或自建 API 网关</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">API Base URL（可选）</label>
                <input v-model="form.telegramApiBase" type="text" placeholder="https://api.telegram.org" :class="inputClass" />
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP/SOCKS5 代理（可选）</label>
                <input v-model="form.telegramProxy" type="text" placeholder="http://127.0.0.1:7890 或 socks5://..." :class="inputClass" />
              </div>
            </template>
          </div>

          <!-- Discord -->
          <div :class="['border rounded-xl p-4 space-y-3 transition-colors', form.enableDiscord ? 'border-slate-300/60 bg-slate-50' : 'border-white/60']">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <input type="checkbox" v-model="form.enableDiscord" class="w-4 h-4 accent-zinc-900" />
              <span class="text-sm font-medium text-slate-700">
                Discord
                <span class="text-xs text-slate-400 font-normal">· Gateway WebSocket 收事件，无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableDiscord">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Bot Token</label>
                <input v-model="form.discordToken" type="password" placeholder="MTIz..." :class="inputClass" />
                <p class="text-xs text-slate-400 mt-1.5">Discord Developer Portal → Applications → Bot 页面获取；必须在同页面开启 <b>Message Content Intent</b>，否则无法连接</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP/SOCKS5 代理（可选）</label>
                <input v-model="form.discordProxy" type="text" placeholder="http://127.0.0.1:7890 或 socks5://..." :class="inputClass" />
              </div>
            </template>
          </div>

          <!-- 微信 -->
          <div :class="['border rounded-xl p-4 space-y-3 transition-colors', form.enableWeixin ? 'border-slate-300/60 bg-slate-50' : 'border-white/60']">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <input type="checkbox" v-model="form.enableWeixin" class="w-4 h-4 accent-zinc-900" />
              <span class="text-sm font-medium text-slate-700">
                微信
                <span class="text-xs text-slate-400 font-normal">· iLink bot 长轮询，无需公网地址；一对一私聊</span>
              </span>
            </label>
            <p v-if="form.enableWeixin" class="text-xs text-slate-500">
              微信没有静态 Token：完成向导并重启后，在「配置管理」页顶部的 <b>微信扫码登录</b> 卡片扫码授权（也可在 Bot 控制台扫码）。
            </p>
          </div>

          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">管理员 ID</label>
            <input v-model="form.adminId" type="text" placeholder="QQ 为 qq:QQ号，其他平台为带前缀的 ID（如 fs:ou_xxx），接收启动/异常通知" :class="inputClass" />
          </div>
        </div>

        <!-- 步骤 2: AI 配置 -->
        <div v-else-if="step === 2" class="space-y-4">
          <h2 class="text-base font-semibold text-slate-800">AI 对话模型</h2>
          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">Base URL</label>
            <input v-model="form.baseUrl" type="text" placeholder="https://api.deepseek.com" :class="inputClass" />
            <p class="text-xs text-slate-400 mt-1.5">任意兼容 OpenAI 规范的 API 地址</p>
          </div>
          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">API Key</label>
            <input v-model="form.apiKey" type="password" placeholder="sk-..." :class="inputClass" />
          </div>
          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">模型</label>
            <input v-model="form.model" type="text" placeholder="deepseek-chat" :class="inputClass" />
          </div>
        </div>

        <!-- 步骤 3: 完成 -->
        <div v-else class="space-y-4 text-center">
          <template v-if="!restarting">
            <div class="mx-auto w-14 h-14 rounded-full bg-emerald-100 text-emerald-600 flex items-center justify-center [&>svg]:w-7 [&>svg]:h-7" v-html="iconCheck" />
            <h1 class="text-xl font-bold text-slate-800">配置完成</h1>
            <p class="text-sm text-slate-500 leading-relaxed">
              配置已保存到数据库，<b>重启后生效</b>。<br />
              插件、MCP 服务器、Prompt 覆盖等更多配置可在「配置管理」与「扩展配置」页继续完善。
            </p>
          </template>
          <template v-else>
            <span class="mx-auto block w-8 h-8 border-[3px] border-white/60 border-t-zinc-500 rounded-full animate-spin" />
            <h1 class="text-xl font-bold text-slate-800">正在重启 Bot...</h1>
            <p class="text-sm text-slate-500">恢复后页面自动刷新</p>
          </template>
        </div>

        <p v-if="error" class="text-sm text-red-600 mt-4">{{ error }}</p>

        <!-- 操作按钮 -->
        <div class="flex justify-between mt-8" v-if="!restarting">
          <button v-if="step > 0 && step < 3" class="px-4 py-2 text-sm text-slate-500 hover:bg-white/60 rounded-xl transition-colors" @click="step--">上一步</button>
          <button v-else class="px-4 py-2 text-sm text-slate-400 hover:text-slate-600 transition-colors" @click="onSkip">跳过引导</button>

            <button v-if="step === 1" class="px-5 py-2 text-sm btn-accent rounded-xl" @click="onNext">下一步</button>
              <button v-else-if="step === 2" class="px-5 py-2 text-sm btn-accent rounded-xl" @click="onSave">保存并继续</button>
                <button v-else-if="step === 0" class="px-5 py-2 text-sm btn-accent rounded-xl" @click="step++">下一步</button>
        <button v-else class="px-5 py-2 text-sm btn-accent rounded-xl" @click="onRestart">重启 Bot 生效</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { api, auth } from '../api.js'

const inputClass = 'w-full border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-indigo-400/50 focus:border-indigo-400/60 transition-shadow'
const iconCheck = '<svg fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5"/></svg>'

const step = ref(0)
const error = ref('')
const restarting = ref(false)
const form = reactive({
  enableNapcat: true,
  enableLuckylilia: false,
  enableQQOfficial: false,
  enableFeishu: false,
  enableTelegram: false,
  mode: 'ws',
  wsAddress: '',
  httpTargetUrl: '',
  httpListenPort: '',
  token: '',
  lilMode: 'ws',
  lilWsAddress: '',
  lilHttpTargetUrl: '',
  lilHttpListenPort: '',
  lilToken: '',
  qqofficialAppId: '',
  qqofficialAppSecret: '',
  qqofficialSandbox: false,
  feishuAppId: '',
  feishuAppSecret: '',
  feishuMode: 'ws',
  feishuWebhookListen: '',
  feishuWebhookPath: '',
  feishuVerificationToken: '',
  feishuEncryptKey: '',
  telegramToken: '',
  telegramApiBase: '',
  telegramProxy: '',
  enableDiscord: false,
  discordToken: '',
  discordProxy: '',
  enableWeixin: false,
  adminId: '',
  baseUrl: '',
  apiKey: '',
  model: '',
})

// 预填当前默认/已迁移值（敏感字段不回填，用户留空则不修改）
onMounted(async () => {
  try {
    const cfg = await api.getConfig()
    form.enableNapcat = cfg['bot.platform.napcat.enable'] !== false
    form.enableLuckylilia = cfg['bot.platform.luckylilia.enable'] === true
    form.enableQQOfficial = cfg['bot.platform.qqofficial.enable'] === true
    form.enableFeishu = cfg['bot.platform.feishu.enable'] === true
    form.mode = cfg['bot.adapter.mode'] || 'ws'
    form.wsAddress = cfg['bot.adapter.ws.address'] || ''
    form.httpTargetUrl = cfg['bot.adapter.http.target_url'] || ''
    form.httpListenPort = cfg['bot.adapter.http.listen_port'] ? String(cfg['bot.adapter.http.listen_port']) : ''
    form.lilMode = cfg['bot.luckylilia.mode'] || 'ws'
    form.lilWsAddress = cfg['bot.luckylilia.ws.address'] || ''
    form.lilHttpTargetUrl = cfg['bot.luckylilia.http.target_url'] || ''
    form.lilHttpListenPort = cfg['bot.luckylilia.http.listen_port'] ? String(cfg['bot.luckylilia.http.listen_port']) : ''
    form.qqofficialAppId = cfg['bot.qqofficial.app_id'] || ''
    form.qqofficialSandbox = cfg['bot.qqofficial.sandbox'] === true
    form.feishuAppId = cfg['bot.feishu.app_id'] || ''
    form.feishuMode = cfg['bot.feishu.mode'] || 'ws'
    form.feishuWebhookListen = cfg['bot.feishu.webhook.listen'] || ''
    form.feishuWebhookPath = cfg['bot.feishu.webhook.path'] || ''
    form.enableTelegram = cfg['bot.platform.telegram.enable'] === true
    form.telegramApiBase = cfg['bot.telegram.api_base'] || ''
    form.telegramProxy = cfg['bot.telegram.proxy'] || ''
    form.enableDiscord = cfg['bot.platform.discord.enable'] === true
    form.discordProxy = cfg['bot.discord.proxy'] || ''
    form.enableWeixin = cfg['bot.platform.weixin.enable'] === true
    form.baseUrl = cfg['plugin.ai_chat_bot.base_url'] || ''
    form.model = cfg['plugin.ai_chat_bot.model'] || ''
    const adminId = cfg['bot.admin_id']
    if (adminId) form.adminId = String(adminId)
  } catch { /* 忽略，使用空表单 */ }
})

// 平台步骤校验：至少启用一个平台
function onNext() {
  error.value = ''
  if (!form.enableNapcat && !form.enableLuckylilia && !form.enableQQOfficial && !form.enableFeishu && !form.enableTelegram && !form.enableDiscord && !form.enableWeixin) {
    error.value = '请至少启用一个平台（QQ、QQ(LLBot)、飞书、Telegram、Discord 或微信），也可「跳过引导」稍后在配置管理中设置'
    return
  }
  step.value++
}

async function onSave() {
  error.value = ''
  if (!form.enableNapcat && !form.enableLuckylilia && !form.enableQQOfficial && !form.enableFeishu && !form.enableTelegram && !form.enableDiscord && !form.enableWeixin) {
    error.value = '请至少启用一个平台（QQ、QQ(LLBot)、飞书、Telegram、Discord 或微信），也可「跳过引导」稍后在配置管理中设置'
    return
  }
  const updates = {}

  // 平台开关
  updates['bot.platform.napcat.enable'] = form.enableNapcat
  updates['bot.platform.luckylilia.enable'] = form.enableLuckylilia
  updates['bot.platform.qqofficial.enable'] = form.enableQQOfficial
  updates['bot.platform.feishu.enable'] = form.enableFeishu
  updates['bot.platform.telegram.enable'] = form.enableTelegram
  updates['bot.platform.discord.enable'] = form.enableDiscord
  updates['bot.platform.weixin.enable'] = form.enableWeixin

  // QQ(NapCat)
  if (form.enableNapcat) {
    updates['bot.adapter.mode'] = form.mode
    if (form.mode === 'ws') {
      if (form.wsAddress.trim()) updates['bot.adapter.ws.address'] = form.wsAddress.trim()
    } else {
      if (form.httpTargetUrl.trim()) updates['bot.adapter.http.target_url'] = form.httpTargetUrl.trim()
      const port = parseInt(form.httpListenPort, 10)
      if (!Number.isNaN(port) && port > 0) updates['bot.adapter.http.listen_port'] = port
    }
    if (form.token.trim()) updates['bot.adapter.token'] = form.token.trim()
  }

  // QQ(Luckylilia / LLBot)（Token 敏感字段：留空不修改）
  if (form.enableLuckylilia) {
    updates['bot.luckylilia.mode'] = form.lilMode
    if (form.lilMode === 'ws') {
      if (form.lilWsAddress.trim()) updates['bot.luckylilia.ws.address'] = form.lilWsAddress.trim()
    } else {
      if (form.lilHttpTargetUrl.trim()) updates['bot.luckylilia.http.target_url'] = form.lilHttpTargetUrl.trim()
      const port = parseInt(form.lilHttpListenPort, 10)
      if (!Number.isNaN(port) && port > 0) updates['bot.luckylilia.http.listen_port'] = port
    }
    if (form.lilToken.trim()) updates['bot.luckylilia.token'] = form.lilToken.trim()
  }

  // QQ 官方（AppSecret 敏感字段：留空不修改）
  if (form.enableQQOfficial) {
    if (form.qqofficialAppId.trim()) updates['bot.qqofficial.app_id'] = form.qqofficialAppId.trim()
    if (form.qqofficialAppSecret.trim()) updates['bot.qqofficial.app_secret'] = form.qqofficialAppSecret.trim()
    updates['bot.qqofficial.sandbox'] = form.qqofficialSandbox
  }

  // 飞书
  if (form.enableFeishu) {
    if (form.feishuAppId.trim()) updates['bot.feishu.app_id'] = form.feishuAppId.trim()
    if (form.feishuAppSecret.trim()) updates['bot.feishu.app_secret'] = form.feishuAppSecret.trim()
    updates['bot.feishu.mode'] = form.feishuMode
    if (form.feishuMode === 'webhook') {
      if (form.feishuWebhookListen.trim()) updates['bot.feishu.webhook.listen'] = form.feishuWebhookListen.trim()
      if (form.feishuWebhookPath.trim()) updates['bot.feishu.webhook.path'] = form.feishuWebhookPath.trim()
      if (form.feishuVerificationToken.trim()) updates['bot.feishu.webhook.verification_token'] = form.feishuVerificationToken.trim()
      if (form.feishuEncryptKey.trim()) updates['bot.feishu.webhook.encrypt_key'] = form.feishuEncryptKey.trim()
    }
  }

  // Telegram（Token 敏感字段：留空不修改）
  if (form.enableTelegram) {
    if (form.telegramToken.trim()) updates['bot.telegram.token'] = form.telegramToken.trim()
    if (form.telegramApiBase.trim()) updates['bot.telegram.api_base'] = form.telegramApiBase.trim()
    if (form.telegramProxy.trim()) updates['bot.telegram.proxy'] = form.telegramProxy.trim()
  }

  // Discord（Token 敏感字段：留空不修改）
  if (form.enableDiscord) {
    if (form.discordToken.trim()) updates['bot.discord.token'] = form.discordToken.trim()
    if (form.discordProxy.trim()) updates['bot.discord.proxy'] = form.discordProxy.trim()
  }

  // 管理员 ID（字符串，可带平台前缀）
  if (form.adminId.trim()) updates['bot.admin_id'] = form.adminId.trim()

  // AI 对话
  if (form.baseUrl.trim()) updates['plugin.ai_chat_bot.base_url'] = form.baseUrl.trim()
  if (form.apiKey.trim()) updates['plugin.ai_chat_bot.api_key'] = form.apiKey.trim()
  if (form.model.trim()) updates['plugin.ai_chat_bot.model'] = form.model.trim()

  try {
    if (Object.keys(updates).length > 0) {
      await api.saveConfig(updates)
    }
    await api.completeSetup()
    step.value = 3
  } catch (e) {
    error.value = e.message
  }
}

async function onSkip() {
  try { await api.completeSetup() } catch { /* 忽略 */ }
  auth.setupRequired = false
}

async function onRestart() {
  restarting.value = true
  try {
    await api.restart()
  } catch { /* 进程可能已开始退出 */ }
  auth.setupRequired = false
  const up = await api.waitUntilUp()
  if (up) {
    location.reload()
  } else {
    restarting.value = false
    error.value = '等待重启超时，请检查 Bot 运行状态'
  }
}
</script>
