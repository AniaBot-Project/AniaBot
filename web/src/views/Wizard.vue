<template>
  <div class="app-shell min-h-screen relative overflow-hidden flex items-center justify-center p-4">
    <!-- 背景光斑（页面底色由全局浅灰底提供） -->
    <div class="absolute -top-40 -left-40 w-125 h-125 rounded-full bg-[#0071e3]/10 blur-[120px]" />
    <div class="absolute -bottom-40 -right-40 w-125 h-125 rounded-full bg-[#5ac8fa]/12 blur-[120px]" />

    <Card style="padding: 0" class="w-full max-w-lg overflow-hidden">
      <!-- 步骤指示 -->
      <div class="flex border-b border-black/5">
        <div
          v-for="(label, i) in ['欢迎', '平台接入', 'AI 配置', '完成']"
          :key="i"
          class="flex-1 py-3.5 text-center text-xs transition-colors"
          :class="i === step ? 'text-zinc-700 font-semibold border-b-2 border-[#0071e3] -mb-px' : i < step ? 'text-[#34c759]' : 'text-slate-400'"
        >
          {{ i + 1 }}. {{ label }}
        </div>
      </div>

      <div class="p-5 sm:p-8">
        <!-- 步骤 0: 欢迎 -->
        <div v-if="step === 0" class="space-y-5 text-center">
          <div class="mx-auto w-14 h-14 rounded-2xl bg-linear-to-br from-[#0a84ff] to-[#0071e3] flex items-center justify-center text-white font-bold text-2xl shadow-lg">
            A
          </div>
          <Title size="middle" color="app-teal">欢迎使用 AniaBot 🎉</Title>
          <p class="text-sm text-slate-500 leading-relaxed">
            这是首次启动，接下来用两步完成最基本的配置：<br />
            <b>接入平台</b>（QQ / QQ 官方 / 飞书 / Telegram / Discord / 微信，可多选）和 <b>AI 对话模型</b>。<br />
            其余配置（插件、MCP、Prompt 覆盖等）可稍后在控制面板中完善。
          </p>
          <p class="text-xs text-slate-400">所有配置保存在数据库中，也可随时跳过，之后在「配置管理」中修改。</p>
        </div>

        <!-- 步骤 1: 平台接入 -->
        <div v-else-if="step === 1" class="space-y-4">
          <Title size="small" color="app-teal">平台接入</Title>

          <!-- QQ(NapCat) -->
          <Card class="space-y-3">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <Switch v-model="form.enableNapcat" />
              <span class="text-sm font-medium text-slate-700">
                QQ（NapCat）
                <span class="text-xs text-slate-400 font-normal">· OneBot v11 协议端，默认启用</span>
              </span>
            </label>
            <template v-if="form.enableNapcat">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">连接模式</label>
                <Select v-model="form.mode" class="w-full" :options="adapterModeOptions" />
              </div>
              <div v-if="form.mode === 'ws'">
                <label class="block text-xs font-medium text-slate-600 mb-1.5">WebSocket 地址</label>
                <Input v-model="form.wsAddress" placeholder="ws://localhost:4455" />
                <p class="text-xs text-slate-400 mt-1.5">NapCat 的 WebSocket 服务端地址；Docker 部署时把 localhost 换成内网 IP</p>
              </div>
              <template v-else>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP 目标地址</label>
                  <Input v-model="form.httpTargetUrl" placeholder="http://localhost:6680" />
                  <p class="text-xs text-slate-400 mt-1.5">NapCat 开放的 HTTP 调用地址；Docker 部署时把 localhost 换成内网 IP</p>
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP 监听端口</label>
                  <Input
                    :model-value="String(form.httpListenPort)"
                    type="number"
                    placeholder="6679"
                    @update:modelValue="v => (form.httpListenPort = v === '' ? '' : Number(v))"
                  />
                  <p class="text-xs text-slate-400 mt-1.5">本机端口，NapCat 的 HTTP Client 向此端口上报事件</p>
                </div>
              </template>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Access Token（可选）</label>
                <Input v-model="form.token" type="password" placeholder="NapCat 端设置了 token 时填写" />
              </div>
            </template>
          </Card>

          <!-- QQ(Luckylilia / LLBot) -->
          <Card class="space-y-3">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <Switch v-model="form.enableLuckylilia" />
              <span class="text-sm font-medium text-slate-700">
                QQ（Luckylilia / LLBot）
                <span class="text-xs text-slate-400 font-normal">· OneBot v11 协议端，可与 NapCat 并存（第二个 QQ 账号）</span>
              </span>
            </label>
            <template v-if="form.enableLuckylilia">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">连接模式</label>
                <Select v-model="form.lilMode" class="w-full" :options="adapterModeOptions" />
              </div>
              <div v-if="form.lilMode === 'ws'">
                <label class="block text-xs font-medium text-slate-600 mb-1.5">WebSocket 地址</label>
                <Input v-model="form.lilWsAddress" placeholder="ws://localhost:3001" />
                <p class="text-xs text-slate-400 mt-1.5">LLBot 的 WebSocket 服务端地址；Docker 部署时把 localhost 换成内网 IP</p>
              </div>
              <template v-else>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">LLBot HTTP 地址</label>
                  <Input v-model="form.lilHttpTargetUrl" placeholder="http://localhost:6690" />
                  <p class="text-xs text-slate-400 mt-1.5">LLBot 开放的「HTTP 服务器」地址；Docker 部署时把 localhost 换成内网 IP</p>
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP 监听端口</label>
                  <Input
                    :model-value="String(form.lilHttpListenPort)"
                    type="number"
                    placeholder="6689"
                    @update:modelValue="v => (form.lilHttpListenPort = v === '' ? '' : Number(v))"
                  />
                  <p class="text-xs text-slate-400 mt-1.5">本机端口，LLBot 的「HTTP 客户端」向此端口上报事件</p>
                </div>
              </template>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Access Token（可选）</label>
                <Input v-model="form.lilToken" type="password" placeholder="LLBot 端设置了 token 时填写" />
              </div>
            </template>
          </Card>

          <!-- QQ 官方机器人 -->
          <Card class="space-y-3">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <Switch v-model="form.enableQQOfficial" />
              <span class="text-sm font-medium text-slate-700">
                QQ 官方机器人
                <span class="text-xs text-slate-400 font-normal">· QQ 开放平台官方接口，WebSocket 收事件无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableQQOfficial">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">AppID</label>
                <Input v-model="form.qqofficialAppId" placeholder="机器人 AppID" />
                <p class="text-xs text-slate-400 mt-1.5">QQ 开放平台（q.qq.com）管理端「开发 → 开发设置」中的 AppID</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">AppSecret</label>
                <Input v-model="form.qqofficialAppSecret" type="password" placeholder="机器人 AppSecret" />
                <p class="text-xs text-slate-400 mt-1.5">用于换取 access_token；旧版 Token 鉴权已废弃，请勿填写 Token</p>
              </div>
              <label class="flex items-center gap-2.5 cursor-pointer select-none">
                <Switch v-model="form.qqofficialSandbox" />
                <span class="text-xs text-slate-600">沙箱环境（机器人未上架前联调使用）</span>
              </label>
              <p class="text-xs text-slate-400">还需在开放平台「功能配置」中勾选群聊/单聊场景的事件订阅（WebSocket 方式）</p>
            </template>
          </Card>

          <!-- 飞书(Lark) -->
          <Card class="space-y-3">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <Switch v-model="form.enableFeishu" />
              <span class="text-sm font-medium text-slate-700">
                飞书（Lark）
                <span class="text-xs text-slate-400 font-normal">· 官方 SDK，默认长连接无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableFeishu">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">App ID</label>
                <Input v-model="form.feishuAppId" placeholder="cli_xxxxxxxx" />
                <p class="text-xs text-slate-400 mt-1.5">飞书开放平台「凭证与基础信息」中的应用 App ID</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">App Secret</label>
                <Input v-model="form.feishuAppSecret" type="password" placeholder="应用 App Secret" />
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">事件订阅方式</label>
                <Select v-model="form.feishuMode" class="w-full" :options="feishuModeOptions" />
              </div>
              <template v-if="form.feishuMode === 'webhook'">
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">监听地址</label>
                  <Input v-model="form.feishuWebhookListen" placeholder="127.0.0.1:7777" />
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">回调路径</label>
                  <Input v-model="form.feishuWebhookPath" placeholder="/webhook/event" />
                  <p class="text-xs text-slate-400 mt-1.5">飞书后台请求地址填 https://&lt;公网地址&gt;&lt;此路径&gt;</p>
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">Verification Token（可选）</label>
                  <Input v-model="form.feishuVerificationToken" placeholder="飞书「事件订阅」页配置" />
                </div>
                <div>
                  <label class="block text-xs font-medium text-slate-600 mb-1.5">Encrypt Key（可选）</label>
                  <Input v-model="form.feishuEncryptKey" type="password" placeholder="事件加密密钥" />
                </div>
              </template>
              <p class="text-xs text-slate-400">还需在飞书开放平台开通权限：im:message、im:message:send_as_bot、im:resource 等</p>
            </template>
          </Card>

          <!-- Telegram -->
          <Card class="space-y-3">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <Switch v-model="form.enableTelegram" />
              <span class="text-sm font-medium text-slate-700">
                Telegram
                <span class="text-xs text-slate-400 font-normal">· Bot API 长轮询，无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableTelegram">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Bot Token</label>
                <Input v-model="form.telegramToken" type="password" placeholder="123456:ABC-DEF..." />
                <p class="text-xs text-slate-400 mt-1.5">向 @BotFather 创建机器人后获取；国内部署可另配代理或自建 API 网关</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">API Base URL（可选）</label>
                <Input v-model="form.telegramApiBase" placeholder="https://api.telegram.org" />
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP/SOCKS5 代理（可选）</label>
                <Input v-model="form.telegramProxy" placeholder="http://127.0.0.1:7890 或 socks5://..." />
              </div>
            </template>
          </Card>

          <!-- Discord -->
          <Card class="space-y-3">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <Switch v-model="form.enableDiscord" />
              <span class="text-sm font-medium text-slate-700">
                Discord
                <span class="text-xs text-slate-400 font-normal">· Gateway WebSocket 收事件，无需公网地址</span>
              </span>
            </label>
            <template v-if="form.enableDiscord">
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">Bot Token</label>
                <Input v-model="form.discordToken" type="password" placeholder="MTIz..." />
                <p class="text-xs text-slate-400 mt-1.5">Discord Developer Portal → Applications → Bot 页面获取；必须在同页面开启 <b>Message Content Intent</b>，否则无法连接</p>
              </div>
              <div>
                <label class="block text-xs font-medium text-slate-600 mb-1.5">HTTP/SOCKS5 代理（可选）</label>
                <Input v-model="form.discordProxy" placeholder="http://127.0.0.1:7890 或 socks5://..." />
              </div>
            </template>
          </Card>

          <!-- 微信 -->
          <Card class="space-y-3">
            <label class="flex items-center gap-2.5 cursor-pointer select-none">
              <Switch v-model="form.enableWeixin" />
              <span class="text-sm font-medium text-slate-700">
                微信
                <span class="text-xs text-slate-400 font-normal">· iLink bot 长轮询，无需公网地址；一对一私聊</span>
              </span>
            </label>
            <p v-if="form.enableWeixin" class="text-xs text-slate-500">
              微信没有静态 Token：完成向导并重启后，在「配置管理」页顶部的 <b>微信扫码登录</b> 卡片扫码授权（也可在 Bot 控制台扫码）。
            </p>
          </Card>

          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">管理员 ID（可多个）</label>
            <textarea v-model="form.adminIds" rows="3" placeholder="每行一个；QQ 为 qq:QQ号，其他平台为带前缀的 ID（如 fs:ou_xxx）。管理员接收启动/异常通知，并可执行 /reboot 等管理命令" :class="inputClass + ' font-mono'" />
          </div>
        </div>

        <!-- 步骤 2: AI 配置 -->
        <div v-else-if="step === 2" class="space-y-4">
          <Title size="small" color="app-teal">AI 对话模型</Title>
          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">Base URL</label>
            <Input v-model="form.baseUrl" placeholder="https://api.deepseek.com" />
            <p class="text-xs text-slate-400 mt-1.5">任意兼容 OpenAI 规范的 API 地址</p>
          </div>
          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">API Key</label>
            <Input v-model="form.apiKey" type="password" placeholder="sk-..." />
          </div>
          <div>
            <label class="block text-xs font-medium text-slate-600 mb-1.5">模型</label>
            <Input v-model="form.model" placeholder="deepseek-chat" />
          </div>
        </div>

        <!-- 步骤 3: 完成 -->
        <div v-else class="space-y-4 text-center">
          <template v-if="!restarting">
            <div class="mx-auto w-14 h-14 rounded-full bg-[#34c759]/15 text-[#34c759] flex items-center justify-center [&>svg]:w-7 [&>svg]:h-7" v-html="iconCheck" />
            <Title size="middle" color="app-green">配置完成</Title>
            <p class="text-sm text-slate-500 leading-relaxed">
              配置已保存到数据库，<b>重启后生效</b>。<br />
              插件、MCP 服务器、Prompt 覆盖等更多配置可在「配置管理」与「扩展配置」页继续完善。
            </p>
          </template>
          <template v-else>
            <span class="mx-auto block w-8 h-8 border-[3px] border-white/60 border-t-zinc-500 rounded-full animate-spin" />
            <Title size="middle" color="app-teal">正在重启 Bot...</Title>
            <p class="text-sm text-slate-500">恢复后页面自动刷新</p>
          </template>
        </div>

        <p v-if="error" class="text-sm text-[#ff3b30] mt-4">{{ error }}</p>

        <!-- 操作按钮 -->
        <div class="flex justify-between mt-8" v-if="!restarting">
          <Button v-if="step > 0 && step < 3" type="default" @click="step--">上一步</Button>
          <Button v-else type="text" @click="onSkip">跳过引导</Button>

          <Button v-if="step === 1" type="primary" @click="onNext">下一步</Button>
          <Button v-else-if="step === 2" type="primary" @click="onSave">保存并继续</Button>
          <Button v-else-if="step === 0" type="primary" @click="step++">下一步</Button>
          <Button v-else type="primary" @click="onRestart">重启 Bot 生效</Button>
        </div>
      </div>
    </Card>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { Button, Card, Input, Select, Switch, Title } from 'animal-island-vue'
import { api, auth } from '../api.js'

const inputClass = 'w-full border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-[#0071e3]/35 focus:border-[#0071e3]/50 transition-shadow'
const iconCheck = '<svg fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m4.5 12.75 6 6 9-13.5"/></svg>'

// 适配器连接模式（Select 的选项为 [{ key, label }]，值统一为字符串）
const adapterModeOptions = [
  { key: 'ws', label: 'WebSocket（推荐）' },
  { key: 'http', label: 'HTTP（Webhook 上报）' },
]

// 飞书事件订阅方式
const feishuModeOptions = [
  { key: 'ws', label: 'WebSocket 长连接（推荐，无需公网地址）' },
  { key: 'webhook', label: 'Webhook（需公网 HTTPS 回调地址）' },
]

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
  adminIds: '',
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
    // 管理员 ID 列表（可多个）；兼容旧版单值键 bot.admin_id
    const adminIds = cfg['bot.admin_ids']
    if (Array.isArray(adminIds) && adminIds.length > 0) {
      form.adminIds = adminIds.join('\n')
    } else if (cfg['bot.admin_id']) {
      form.adminIds = String(cfg['bot.admin_id'])
    }
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

  // 管理员 ID 列表（每行一个，可多个）
  const adminIds = form.adminIds.split('\n').map((s) => s.trim()).filter(Boolean)
  if (adminIds.length > 0) updates['bot.admin_ids'] = adminIds

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
