// 演示模式 API 路由表与可变状态
// 静态数据来自 seed/*.json，动态数据由 synthetic.js 生成；
// 增删改会真实修改内存状态，刷新页面后恢复初始演示数据。

import me from './seed/me.json'
import seedStatus from './seed/status.json'
import seedHost from './seed/host.json'
import plugins from './seed/plugins.json'
import configSchema from './seed/config_schema.json'
import seedConfig from './seed/config.json'
import seedFiles from './seed/files_mcp.json'
import seedPrompt from './seed/files_prompt.json'
import contactSources from './seed/contact_sources.json'
import seedMemoryScopes from './seed/memory_scopes.json'
import seedMemoryList from './seed/memory_list.json'
import teamRoles from './seed/team_roles.json'
import seedTeamScopes from './seed/team_scopes.json'
import seedTeamList from './seed/team_list.json'
import seedKnowledgeScopes from './seed/knowledge_scopes.json'
import seedKnowledgeList from './seed/knowledge_list.json'
import seedClocks from './seed/clocks.json'
import seedBalance from './seed/balance.json'
import seedMarketInfo from './seed/marketplace_info.json'
import seedMarketPlugins from './seed/marketplace_plugins.json'
import seedUpdateInfo from './seed/update_info.json'
import seedOpLogs from './seed/oplogs.json'
import seedConsoleLogs from './seed/consolelogs.json'
import seedQrSources from './seed/qrlogin_sources.json'

import {
  toLocalIso, dateStr, buildMsgLogs, buildQueryLogs, buildTaskLogs,
  nextConsoleLine, buildTokenStats, buildTokenStatsDetail, buildQuota,
  buildContacts, buildSkills, buildSkillDetail, createHostSim,
} from './synthetic.js'

// 时间基准：把抓取时刻整体平移到"现在"

const CAPTURE_MS = Date.parse('2026-10-08T15:35:00+08:00')
const SHIFT_MS = Date.now() - CAPTURE_MS
const SHIFT_DAYS = Math.round(SHIFT_MS / 86400000)
const pad = (n, w = 2) => String(n).padStart(w, '0')

function shiftIso(v) {
  const t = Date.parse(v)
  if (Number.isNaN(t) || t < Date.parse('2001-01-02T00:00:00Z')) return v // 跳过零值时间
  return toLocalIso(new Date(t + SHIFT_MS))
}
function shiftDay(v) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v)
  if (!m) return v
  const d = new Date(+m[1], +m[2] - 1, +m[3] + SHIFT_DAYS)
  return dateStr(d)
}
const ISO_KEYS = new Set(['time', 'created_at', 'updated_at', 'finished_at', 'trigger_time', 'last_run_at', 'next_run_at', 'started_at'])
function rebase(value, key) {
  if (Array.isArray(value)) return value.map((x) => rebase(x))
  if (value && typeof value === 'object') {
    const out = {}
    for (const [k, v] of Object.entries(value)) {
      if (k === 'index_synced_at' && typeof v === 'number') out[k] = v + Math.floor(SHIFT_MS / 1000)
      else out[k] = rebase(v, k)
    }
    return out
  }
  if (typeof value === 'string') {
    if (ISO_KEYS.has(key) && value.includes('T')) return shiftIso(value)
    if (key === 'date' && /^\d{4}-\d{2}-\d{2}$/.test(value)) return shiftDay(value)
  }
  return value
}

// 状态

const now0 = Date.now()
const bootAt = now0 - (2 * 3600 + 42 * 60) * 1000 // 演示：Bot 已运行 2 小时 42 分

const state = {
  config: { ...rebase(seedConfig) },
  presets: [{
    name: '初始配置',
    created_at: toLocalIso(new Date(now0 - 3600_000)),
    updated_at: toLocalIso(new Date(now0 - 3600_000)),
    key_count: Object.keys(seedConfig).length,
  }],
  files: {
    mcp: seedFiles.content,
    prompt: seedPrompt.content,
    hooks: '',
    commands: '',
  },
  clocks: rebase(seedClocks).map((c) => ({
    ...c,
    // 抓取值平移后会偏离 cron 周期，按当前时间重新推算下次触发
    next_run_at: c.enabled ? nextCronRun(c.cron) : '0001-01-01T00:00:00Z',
  })),
  memories: [
    ...rebase(seedMemoryList).map((x) => ({ ...x, scope: 'g:qq:123456' })),
    { scope: 'f:qq:10000', id: 'dcfdf676', user_id: 'qq:10000', content: '管理员正在学习 Go 和 Vue，相关话题可以聊得更技术化', tags: ['背景'], created_at: toLocalIso(new Date(now0 - 5400_000)) },
  ],
  teams: [
    ...rebase(seedTeamList).map((x) => ({ ...x, scope: 'g:qq:123456' })),
    { scope: 'f:qq:10000', name: 'research', desc: '资料检索与整理', members: [{ name: 'searcher' }], created_at: toLocalIso(new Date(now0 - 5400_000)) },
  ],
  knowledge: rebase(seedKnowledgeList).map((x) => ({ ...x, scope: 'g:qq:123456' })),
  opLogs: rebase(seedOpLogs.items),
  consoleLogs: rebase(seedConsoleLogs.items),
  msgLogs: buildMsgLogs(now0),
  queryLogs: buildQueryLogs(now0),
  taskLogs: buildTaskLogs(now0),
  // 演示：预置 3 个已安装的市场插件（rss 为旧版本 → 显示可更新）
  installed: {},
  marketJob: null,
  quotaResetAt: 0,
  updated: false,
}

// 跨"重启"状态：安装插件/更新完成后前端会整页刷新（等同 Bot 重启），
// 这些状态需要与真实后端一样在重启后保留，故持久化到 sessionStorage
function loadPersisted() {
  try { return JSON.parse(sessionStorage.getItem('ania-demo-persist') || '{}') } catch { return {} }
}
function savePersisted() {
  try {
    sessionStorage.setItem('ania-demo-persist', JSON.stringify({ installed: state.installed, updated: state.updated }))
  } catch { /* 隐私模式等场景下不可用，忽略 */ }
}
{
  const saved = loadPersisted()
  if (saved.installed) {
    state.installed = saved.installed
    state.updated = !!saved.updated
  } else {
    for (const p of seedMarketPlugins.plugins) {
      if (['antiwithdrawal', 'checkin', 'rss'].includes(p.id)) {
        const older = p.version.replace(/(\d+)$/, (m) => String(Math.max(0, +m - 1)))
        state.installed[p.id] = p.id === 'rss' ? older : p.version
      }
    }
    savePersisted()
  }
}

const hostSnapshot = createHostSim(seedHost)
const hexId = () => Math.floor(Math.random() * 0xffffffff).toString(16).padStart(8, '0')
const b36Max = state.opLogs.reduce((a, x) => Math.max(a, parseInt(x.id, 36) || 0), 0)
let opSeq = b36Max
let consoleSeq = state.consoleLogs.reduce((a, x) => Math.max(a, x.id || 0), 0)

function addOpLog(category, action, detail) {
  state.opLogs.unshift({
    id: (++opSeq).toString(36),
    time: toLocalIso(new Date()),
    category, action, detail,
  })
  if (state.opLogs.length > 500) state.opLogs.length = 500
}

// 控制台日志定时追加
setInterval(() => {
  state.consoleLogs.unshift(nextConsoleLine(Date.now(), ++consoleSeq))
  if (state.consoleLogs.length > 300) state.consoleLogs.length = 300
}, 18_000)

// 通用工具

function paginate(items, query, idOf, compare) {
  const limit = Math.min(200, Math.max(1, parseInt(query.get('limit') || '50', 10) || 50))
  const before = query.get('before')
  let list = items
  if (before) list = list.filter((x) => (compare ? compare(idOf(x), before) : idOf(x) < before))
  const page = list.slice(0, limit)
  return { items: page, has_more: list.length > limit }
}
const inRange = (timeStr, query) => {
  const start = query.get('start')
  const end = query.get('end')
  if (!start && !end) return true
  const t = Date.parse(timeStr)
  if (Number.isNaN(t)) return true
  if (start && t < Date.parse(start)) return false
  if (end && t > Date.parse(end) + 86400_000 - 1) return false
  return true
}
const hasKeyword = (item, kw) => !kw || JSON.stringify(item).toLowerCase().includes(kw.toLowerCase())
const scopeCounts = (entries) => {
  const map = new Map()
  for (const e of entries) {
    const scope = e.scope
    const kind = scope.startsWith('g:') ? 'group' : 'friend'
    const target = scope.slice(2)
    if (!map.has(scope)) map.set(scope, { scope, kind, target, count: 0 })
    map.get(scope).count++
  }
  return [...map.values()]
}

// 简易 5 段 cron 下次触发时间（支持 * 数字 区间 列表 步进）
function nextCronRun(cron, from = new Date()) {
  const fields = cron.trim().split(/\s+/)
  if (fields.length !== 5) return '0001-01-01T00:00:00Z'
  const expand = (f, lo, hi) => {
    const out = new Set()
    for (const part of f.split(',')) {
      const [range, step] = part.split('/')
      const st = step ? parseInt(step, 10) : 1
      let a = lo, b = hi
      if (range !== '*') {
        const m = /^(\d+)-(\d+)$/.exec(range)
        if (m) { a = +m[1]; b = +m[2] } else a = b = parseInt(range, 10)
      }
      for (let v = a; v <= b; v += (st || 1)) out.add(v)
    }
    return [...out]
  }
  const mins = expand(fields[0], 0, 59)
  const hours = expand(fields[1], 0, 23)
  const doms = expand(fields[2], 1, 31)
  const mons = expand(fields[3], 1, 12)
  const dows = expand(fields[4], 0, 6)
  if (!mins.length || !hours.length) return '0001-01-01T00:00:00Z'
  const d = new Date(from.getTime() + 60_000)
  d.setSeconds(0, 0)
  for (let day = 0; day < 400; day++) {
    if (!mons.includes(d.getMonth() + 1) || !doms.includes(d.getDate()) || !dows.includes(d.getDay())) {
      d.setDate(d.getDate() + 1)
      d.setHours(0, 0, 0, 0)
      continue
    }
    for (const h of hours) for (const m of mins) {
      const t = new Date(d.getFullYear(), d.getMonth(), d.getDate(), h, m)
      if (t > from) return toLocalIso(t)
    }
    d.setDate(d.getDate() + 1)
    d.setHours(0, 0, 0, 0)
  }
  return '0001-01-01T00:00:00Z'
}

function statusSnapshot() {
  return {
    ...seedStatus,
    started_at: toLocalIso(new Date(bootAt)),
    uptime_sec: Math.floor((Date.now() - bootAt) / 1000),
    goroutines: 42 + (Math.floor(Date.now() / 5000) % 7),
  }
}

function marketPluginsList() {
  return {
    plugins: seedMarketPlugins.plugins.map((p) => {
      const v = state.installed[p.id]
      if (!v) return { ...p, installed: false, update_available: false }
      return { ...p, installed: true, installed_version: v, installed_commit: 'e3a1c9f', update_available: v !== p.version }
    }),
  }
}
function marketInfo() {
  return { ...seedMarketInfo, installed: Object.keys(state.installed).length }
}
// 安装/卸载任务模拟：几秒内推进 拉取→编译→重启，完成后提交状态变更
function marketStatus() {
  const job = state.marketJob
  if (!job) return { running: false, restarting: false, action: '', plugin_id: '', phase: '', logs: [], error: '', errKind: '' }
  const t = (Date.now() - job.start) / 1000
  const timeline = [
    [0, '正在拉取插件源码…', `git clone --depth 1 AniaBot-Plugins/${job.plugin_id} (main)`],
    [1.6, '拉取完成，开始编译插件…', 'go build -buildmode=plugin ./plugins/*'],
    [4.0, '编译完成，复制到插件目录…', `cp -r ${job.plugin_id} ./data/plugins/`],
    [5.0, '正在重启 Bot…', '重启完成后插件生效'],
  ]
  const logs = timeline.filter(([at]) => t >= at).map(([, ...ls]) => ls.join(' '))
  let phase = t < 1.6 ? 'download' : t < 4.0 ? 'build' : t < 6.2 ? 'restart' : 'done'
  if (t >= 6.2 && !job.committed) {
    // 提交状态变更（只做一次）
    job.committed = true
    if (job.action === 'install' || job.action === 'batch') {
      for (const id of job.install) {
        const p = seedMarketPlugins.plugins.find((x) => x.id === id)
        state.installed[id] = p ? p.version : '1.0.0'
      }
      addOpLog('plugin', 'plugin_install', `面板安装市场插件: ${job.install.join(', ')}`)
    }
    if (job.action === 'uninstall' || job.action === 'batch') {
      for (const id of job.uninstall) delete state.installed[id]
      addOpLog('plugin', 'plugin_uninstall', `面板卸载市场插件: ${job.uninstall.join(', ')}`)
    }
    savePersisted()
  }
  if (t >= 20) {
    // done 保持约 14s 供前端轮询观察，之后再清除任务
    state.marketJob = null
    return { running: false, restarting: false, action: '', plugin_id: '', phase: '', logs: [], error: '', errKind: '' }
  }
  if (t >= 6.2) {
    return { running: false, restarting: false, action: job.action, plugin_id: job.plugin_id, phase: 'done', logs: [...logs, '操作完成'], error: '', errKind: '' }
  }
  return { running: true, restarting: phase === 'restart', action: job.action, plugin_id: job.plugin_id, phase, logs, error: '', errKind: '' }
}

// 路由表

const routes = [
  // 认证
  { m: 'POST', re: /^\/api\/login$/, h: (c) => { addOpLog('auth', 'login', '面板登录成功，IP: 127.0.0.1'); return { ok: true } } },
  { m: 'POST', re: /^\/api\/logout$/, h: () => ({ ok: true }) },
  { m: 'GET', re: /^\/api\/me$/, h: () => me },
  { m: 'POST', re: /^\/api\/setup\/complete$/, h: () => ({ ok: true }) },
  { m: 'PUT', re: /^\/api\/password$/, h: () => ({ ok: true }) },

  // 配置
  { m: 'GET', re: /^\/api\/config\/schema$/, h: () => configSchema },
  { m: 'GET', re: /^\/api\/config$/, h: () => state.config },
  { m: 'GET', re: /^\/api\/config\/export$/, h: () => ({ status: 200, body: JSON.stringify(state.config, null, 2), headers: { 'Content-Disposition': `attachment; filename="aniabot-config-${Math.floor(Date.now() / 1000)}.json"` } }) },
  {
    m: 'PUT', re: /^\/api\/config$/, h: (c) => {
      const updates = c.body || {}
      const keys = Object.keys(updates).filter((k) => updates[k] !== '********')
      for (const [k, v] of Object.entries(updates)) {
        if (v === null) delete state.config[k]
        else if (v !== '********') state.config[k] = v
      }
      if (keys.length) addOpLog('config', 'config_update', `面板更新配置（${keys.length} 项）: ${keys.join(', ')}`)
      return { ok: true, need_restart: true }
    },
  },
  { m: 'GET', re: /^\/api\/config\/presets$/, h: () => state.presets },
  {
    m: 'POST', re: /^\/api\/config\/presets$/, h: (c) => {
      const name = (c.body && c.body.name) || '未命名'
      state.presets.unshift({ name, created_at: toLocalIso(new Date()), updated_at: toLocalIso(new Date()), key_count: Object.keys(state.config).length })
      addOpLog('config', 'preset_save', `面板保存配置预设: ${name}`)
      return { ok: true }
    },
  },
  {
    m: 'POST', re: /^\/api\/config\/presets\/([^/]+)\/apply$/, h: (c) => {
      addOpLog('config', 'preset_apply', `面板应用配置预设: ${decodeURIComponent(c.params[0])}`)
      return { ok: true, keys: Object.keys(state.config).length, need_restart: true }
    },
  },
  {
    m: 'DELETE', re: /^\/api\/config\/presets\/([^/]+)$/, h: (c) => {
      const name = decodeURIComponent(c.params[0])
      state.presets = state.presets.filter((p) => p.name !== name)
      return { ok: true }
    },
  },
  {
    m: 'PUT', re: /^\/api\/files\/(mcp|prompt|hooks|commands)$/, h: (c) => {
      const name = c.params[0]
      const content = (c.body && c.body.content) || ''
      if (content && content.trim()) {
        try { JSON.parse(content) } catch { return { status: 400, body: { error: '内容不是合法的 JSON' } } }
      }
      state.files[name] = content
      addOpLog('config', 'file_update', `面板修改扩展配置文件: ${name}`)
      return { ok: true, need_restart: name === 'mcp' }
    },
  },

  // 状态 / 系统
  { m: 'GET', re: /^\/api\/status$/, h: statusSnapshot },
  { m: 'GET', re: /^\/api\/host$/, h: () => hostSnapshot() },
  { m: 'GET', re: /^\/api\/plugins$/, h: () => plugins },
  { m: 'GET', re: /^\/api\/balance$/, h: () => ({ ...rebase(seedBalance), updated_at: toLocalIso(new Date(Date.now() - 120_000)) }) },
  { m: 'POST', re: /^\/api\/restart$/, h: () => { addOpLog('system', 'restart', '面板请求重启 Bot，IP: 127.0.0.1'); return { ok: true } } },

  // 通讯录
  { m: 'GET', re: /^\/api\/contact\/sources$/, h: () => contactSources },
  { m: 'GET', re: /^\/api\/contacts$/, h: (c) => buildContacts(c.query.get('kind') || 'groups') },

  // 日志（分页 + 过滤）
  { m: 'GET', re: /^\/api\/msglogs$/, h: (c) => paginate(state.msgLogs, c.query, (x) => x.id) },
  {
    m: 'GET', re: /^\/api\/consolelogs$/, h: (c) => paginate(state.consoleLogs, c.query, (x) => x.id),
  },
  {
    m: 'GET', re: /^\/api\/oplogs$/, h: (c) => {
      const category = c.query.get('category')
      let list = state.opLogs
      if (category) list = list.filter((x) => x.category === category)
      list = list.filter((x) => inRange(x.time, c.query) && hasKeyword(x, c.query.get('keyword')))
      return paginate(list, c.query, (x) => x.id, (a, b) => parseInt(a, 36) < parseInt(b, 36))
    },
  },
  {
    m: 'GET', re: /^\/api\/querylogs$/, h: (c) => {
      let list = state.queryLogs
      const chatType = c.query.get('chat_type')
      const targetId = c.query.get('target_id')
      const sender = c.query.get('sender')
      if (chatType) list = list.filter((x) => x.chat_type === chatType)
      if (targetId) list = list.filter((x) => x.target_id === targetId)
      if (sender) list = list.filter((x) => (x.senders || []).some((s) => String(s).includes(sender)))
      list = list.filter((x) => inRange(x.time, c.query) && hasKeyword({ q: x.query, r: x.reply, e: x.error }, c.query.get('keyword')))
      return paginate(list, c.query, (x) => x.id)
    },
  },
  {
    m: 'GET', re: /^\/api\/tasklogs$/, h: (c) => {
      let list = state.taskLogs
      const f = (k, fn) => { const v = c.query.get(k); if (v) list = list.filter(fn(v)) }
      f('target_type', (v) => (x) => x.target_type === v)
      f('target_id', (v) => (x) => x.target_id === v)
      f('task_id', (v) => (x) => x.task_id === v)
      f('status', (v) => (x) => x.status === v)
      list = list.filter((x) => inRange(x.trigger_time, c.query) && hasKeyword(x, c.query.get('keyword')))
      return paginate(list, c.query, (x) => x.id)
    },
  },

  // 定时任务
  { m: 'GET', re: /^\/api\/clocks$/, h: () => state.clocks },
  {
    m: 'POST', re: /^\/api\/clocks$/, h: (c) => {
      const b = c.body || {}
      if (!/^\S+\s+\S+\s+\S+\s+\S+\s+\S+$/.test(b.cron || '')) return { status: 400, body: { error: 'cron 表达式无效: 需要 5 个字段' } }
      const id = String(state.clocks.reduce((a, x) => Math.max(a, parseInt(x.id, 10) || 0), 0) + 1)
      state.clocks.unshift({
        id, title: b.title || '未命名任务', content: b.content || '', note: b.note || '',
        cron: b.cron, target_type: b.target_type || 'group', target_id: b.target_id || '',
        enabled: b.enabled !== false, run_once: !!b.run_once, timeout_sec: b.timeout_sec || 120,
        created_by: b.created_by || 'user', creator: 'panel', updater: '',
        created_at: toLocalIso(new Date()),
        last_run_at: '0001-01-01T00:00:00Z',
        next_run_at: b.enabled !== false ? nextCronRun(b.cron) : '0001-01-01T00:00:00Z',
      })
      addOpLog('clock', 'clock_create', `面板创建定时任务 ${id}（${b.title}）`)
      return { ok: true, id }
    },
  },
  {
    m: 'PUT', re: /^\/api\/clocks\/([^/]+)$/, h: (c) => {
      const id = decodeURIComponent(c.params[0])
      const task = state.clocks.find((x) => x.id === id)
      if (!task) return { status: 404, body: { error: '任务不存在' } }
      const b = c.body || {}
      for (const k of ['title', 'content', 'note', 'cron', 'target_type', 'target_id', 'timeout_sec', 'enabled', 'run_once', 'created_by']) {
        if (b[k] !== undefined && b[k] !== null) task[k] = b[k]
      }
      task.updater = 'panel'
      if (b.cron !== undefined || b.enabled !== undefined) task.next_run_at = task.enabled ? nextCronRun(task.cron) : '0001-01-01T00:00:00Z'
      addOpLog('clock', 'clock_update', `面板修改定时任务 ${id}（${task.title}）`)
      return { ok: true }
    },
  },
  {
    m: 'DELETE', re: /^\/api\/clocks\/([^/]+)$/, h: (c) => {
      const id = decodeURIComponent(c.params[0])
      const task = state.clocks.find((x) => x.id === id)
      state.clocks = state.clocks.filter((x) => x.id !== id)
      addOpLog('clock', 'clock_delete', `面板删除定时任务 ${id}（${task ? task.title : ''}）`)
      return { ok: true }
    },
  },

  // Token 统计 / 配额
  { m: 'GET', re: /^\/api\/tokenstats$/, h: () => buildTokenStats(Date.now()) },
  {
    m: 'GET', re: /^\/api\/tokenstats\/detail$/, h: (c) =>
      buildTokenStatsDetail(c.query.get('range') || 'all', Date.now(), c.query.get('start'), c.query.get('end')),
  },
  { m: 'GET', re: /^\/api\/quota$/, h: () => buildQuota(Date.now()) },
  {
    m: 'POST', re: /^\/api\/quota\/reset$/, h: (c) => {
      addOpLog('quota', 'quota_reset', `面板清零配额 ${(c.body && c.body.scope) || 'all'}`)
      state.quotaResetAt = Date.now()
      return { ok: true }
    },
  },

  // 技能
  { m: 'GET', re: /^\/api\/skills$/, h: () => buildSkills() },
  { m: 'GET', re: /^\/api\/skills\/([^/]+)$/, h: (c) => buildSkillDetail(decodeURIComponent(c.params[0])) || { status: 404, body: { error: '技能不存在' } } },
  { m: 'POST', re: /^\/api\/skills$/, h: () => ({ ok: true }) },
  { m: 'DELETE', re: /^\/api\/skills\/([^/]+)$/, h: () => ({ ok: true }) },

  // 记忆
  { m: 'GET', re: /^\/api\/memory\/scopes$/, h: () => scopeCounts(state.memories) },
  {
    m: 'GET', re: /^\/api\/memory\/list$/, h: (c) => {
      const scope = c.query.get('scope') || ''
      return state.memories.filter((x) => x.scope === scope).map(({ scope: _s, ...rest }) => rest)
    },
  },
  {
    m: 'POST', re: /^\/api\/memory$/, h: (c) => {
      const b = c.body || {}
      const entry = { scope: b.scope, id: hexId(), user_id: b.user_id || '', content: b.content || '', tags: b.tags || [], created_at: toLocalIso(new Date()) }
      if (!entry.user_id) delete entry.user_id
      state.memories.unshift(entry)
      addOpLog('memory', 'memory_create', `面板新增记忆 ${entry.scope}/${entry.id}`)
      return { ok: true, id: entry.id }
    },
  },
  {
    m: 'PUT', re: /^\/api\/memory$/, h: (c) => {
      const b = c.body || {}
      const entry = state.memories.find((x) => x.scope === b.scope && x.id === b.id)
      if (!entry) return { status: 404, body: { error: '记忆不存在' } }
      if (b.content !== undefined) entry.content = b.content
      if (b.tags !== undefined) entry.tags = b.tags
      if (b.user_id !== undefined) entry.user_id = b.user_id
      return { ok: true }
    },
  },
  {
    m: 'DELETE', re: /^\/api\/memory$/, h: (c) => {
      const scope = c.query.get('scope'), id = c.query.get('id')
      state.memories = state.memories.filter((x) => !(x.scope === scope && x.id === id))
      return { ok: true }
    },
  },

  // Agent 团队
  { m: 'GET', re: /^\/api\/team\/roles$/, h: () => teamRoles },
  { m: 'GET', re: /^\/api\/team\/scopes$/, h: () => scopeCounts(state.teams) },
  {
    m: 'GET', re: /^\/api\/team\/list$/, h: (c) => {
      const scope = c.query.get('scope') || ''
      return state.teams.filter((x) => x.scope === scope).map(({ scope: _s, ...rest }) => rest)
    },
  },
  {
    m: 'POST', re: /^\/api\/team$/, h: (c) => {
      const b = c.body || {}
      if (!/^[\w\u4e00-\u9fa5-]{1,20}$/.test(b.name || '')) return { status: 400, body: { error: '团队名只能包含中文/字母/数字/下划线/连字符，长度 1-20 字符' } }
      state.teams.unshift({ scope: b.scope, name: b.name, desc: b.desc || '', members: b.members || [], created_at: toLocalIso(new Date()) })
      addOpLog('team', 'team_create', `面板创建团队 ${b.scope}/${b.name}`)
      return { ok: true }
    },
  },
  {
    m: 'PUT', re: /^\/api\/team$/, h: (c) => {
      const b = c.body || {}
      const team = state.teams.find((x) => x.scope === b.scope && x.name === b.name)
      if (!team) return { status: 404, body: { error: '团队不存在' } }
      if (b.desc !== undefined) team.desc = b.desc
      if (b.members !== undefined) team.members = b.members
      return { ok: true }
    },
  },
  {
    m: 'DELETE', re: /^\/api\/team$/, h: (c) => {
      const scope = c.query.get('scope'), name = c.query.get('name')
      state.teams = state.teams.filter((x) => !(x.scope === scope && x.name === name))
      return { ok: true }
    },
  },

  // 知识库
  { m: 'GET', re: /^\/api\/knowledge\/scopes$/, h: () => scopeCounts(state.knowledge).map(({ target: _t, ...r }) => r) },
  {
    m: 'GET', re: /^\/api\/knowledge\/list$/, h: (c) => {
      const scope = c.query.get('scope') || ''
      return state.knowledge.filter((x) => x.scope === scope).map(({ scope: _s, ...rest }) => rest)
    },
  },
  {
    m: 'POST', re: /^\/api\/knowledge$/, h: (c) => {
      const b = c.body || {}
      const entry = { scope: b.scope, id: hexId(), title: b.title || '未命名', content: b.content || '', tags: b.tags || [], created_at: toLocalIso(new Date()) }
      state.knowledge.unshift(entry)
      addOpLog('knowledge', 'knowledge_create', `面板新增知识库文档 ${entry.scope}/${entry.id}（${entry.title}）`)
      return { ok: true, id: entry.id }
    },
  },
  {
    m: 'POST', re: /^\/api\/knowledge\/import-url$/, h: (c) => {
      const b = c.body || {}
      const entry = { scope: b.scope, id: hexId(), title: '网页导入的文档', content: `（演示环境不真实抓取网页。原始 URL：${b.url}）\n\n导入的正文会保存到这里，作为知识库检索的语料。`, tags: [], source: `url:${b.url}`, created_at: toLocalIso(new Date()) }
      state.knowledge.unshift(entry)
      addOpLog('knowledge', 'knowledge_import', `面板从 URL 导入知识库文档 ${entry.scope}/${entry.id}`)
      return { ok: true, id: entry.id }
    },
  },
  {
    m: 'PUT', re: /^\/api\/knowledge$/, h: (c) => {
      const b = c.body || {}
      const entry = state.knowledge.find((x) => x.scope === b.scope && x.id === b.id)
      if (!entry) return { status: 404, body: { error: '文档不存在' } }
      for (const k of ['title', 'content', 'tags']) if (b[k] !== undefined) entry[k] = b[k]
      return { ok: true }
    },
  },
  {
    m: 'DELETE', re: /^\/api\/knowledge$/, h: (c) => {
      const scope = c.query.get('scope'), id = c.query.get('id')
      state.knowledge = state.knowledge.filter((x) => !(x.scope === scope && x.id === id))
      return { ok: true }
    },
  },

  // 扩展文件读取（放最后，避免吞掉上面的 config 路由）
  {
    m: 'GET', re: /^\/api\/files\/(mcp|prompt|hooks|commands)$/, h: (c) => ({ content: state.files[c.params[0]] ?? '' }),
  },

  // 插件市场
  { m: 'GET', re: /^\/api\/marketplace\/info$/, h: marketInfo },
  { m: 'GET', re: /^\/api\/marketplace\/plugins$/, h: marketPluginsList },
  {
    m: 'GET', re: /^\/api\/marketplace\/plugins\/([^/]+)$/, h: (c) => import(`./seed/marketplace_detail_${decodeURIComponent(c.params[0])}.json`).then((m) => {
      const d = rebase(m.default)
      const v = state.installed[c.params[0]]
      d.installed = !!v
      d.installed_version = v || ''
      d.installed_commit = v ? 'e3a1c9f' : ''
      return d
    }).catch(() => ({ status: 404, body: { error: '插件不存在' } })),
  },
  { m: 'GET', re: /^\/api\/marketplace\/status$/, h: marketStatus },
  {
    m: 'POST', re: /^\/api\/marketplace\/(install|uninstall|batch|rollback)$/, h: (c) => {
      const action = c.params[0]
      if (state.marketJob) return { status: 409, body: { error: '已有任务正在进行中，请稍候' } }
      const b = c.body || {}
      if (action === 'install') state.marketJob = { action, plugin_id: b.id, install: [b.id], uninstall: [], start: Date.now() }
      else if (action === 'uninstall') state.marketJob = { action, plugin_id: b.id, install: [], uninstall: [b.id], start: Date.now() }
      else if (action === 'batch') state.marketJob = { action, plugin_id: (b.install && b.install[0]) || (b.uninstall && b.uninstall[0]) || '', install: b.install || [], uninstall: b.uninstall || [], start: Date.now() }
      else return { ok: true } // rollback：演示环境直接成功
      return { ok: true }
    },
  },
  {
    m: 'POST', re: /^\/api\/marketplace\/oauth\/start$/, h: () => ({ user_code: 'ABCD-1234', verification_uri: 'https://github.com/login/device', expires_in: 899, interval: 5 }),
  },
  { m: 'GET', re: /^\/api\/marketplace\/oauth\/status$/, h: () => ({ active: false, status: 'pending', user: '', error: '' }) },
  { m: 'POST', re: /^\/api\/marketplace\/oauth\/cancel$/, h: () => ({ ok: true }) },

  // 自动更新
  {
    m: 'GET', re: /^\/api\/update\/info$/, h: () => {
      const info = rebase(seedUpdateInfo)
      if (state.updated) {
        info.currentCommit = info.remoteCommit
        info.updateAvailable = false
      }
      return info
    },
  },
  {
    m: 'POST', re: /^\/api\/update\/start$/, h: () => {
      if (state.updateJob) return { status: 409, body: { error: '已有更新任务正在进行中' } }
      state.updateJob = { start: Date.now() }
      addOpLog('update', 'update_start', '面板发起自动更新')
      return { ok: true }
    },
  },
  {
    m: 'GET', re: /^\/api\/update\/status$/, h: () => {
      const job = state.updateJob
      if (!job) return { running: false, restarting: false, phase: '', logs: [], error: '', errKind: '' }
      const t = (Date.now() - job.start) / 1000
      // 与 Update.vue 的 phases 对齐：env → fetch → deps → web → build → swap → restart
      const steps = [
        [0, 'env', '检查构建环境…', 'git 2.54.0 / go1.27.1 / node v24.16.0 / npm 11.13.0'],
        [1.5, 'fetch', '拉取远端代码…', 'git fetch origin main → b7d4f21'],
        [3.5, 'deps', 'go mod download …', '依赖已是最优'],
        [5.5, 'web', '构建管理面板前端…', 'npm ci && npm run build → dist 19 个资源'],
        [9.5, 'build', '编译 AniaBot…', 'go build -ldflags "-s -w" → build/AniaBot'],
        [13.5, 'swap', '替换二进制并准备重启…', '旧二进制已备份为 AniaBot.bak'],
        [15.5, 'restart', '重启 Bot…', '新版本 b7d4f21 已就绪'],
      ]
      const logs = steps.filter(([at]) => t >= at).flatMap(([, , ...ls]) => ls)
      const passed = steps.filter(([at]) => t >= at).length
      // done 阶段保持约 14s，确保前端 1.5s 轮询一定能观察到 running→done 的转换
      if (t >= 32) {
        state.updateJob = null
        return { running: false, restarting: false, phase: '', logs: [], error: '', errKind: '' }
      }
      if (t >= 18) {
        state.updated = true
        savePersisted()
        return { running: false, restarting: false, phase: 'done', logs: [...logs, '更新完成'], error: '', errKind: '' }
      }
      const phase = passed >= 7 ? 'restart' : steps[passed][1]
      return { running: true, restarting: t >= 15.5, phase, logs, error: '', errKind: '' }
    },
  },

  // 扫码登录（真实抓取为无可用源，如实呈现）
  { m: 'GET', re: /^\/api\/qrlogin\/sources$/, h: () => seedQrSources },
  { m: 'POST', re: /^\/api\/qrlogin\/([^/]+)\/start$/, h: () => ({ status: 400, body: { error: '没有可用的扫码登录源' } }) },
  { m: 'GET', re: /^\/api\/qrlogin\/([^/]+)\/status$/, h: () => ({ state: 'idle', detail: '', qr_data_url: '' }) },
  { m: 'POST', re: /^\/api\/qrlogin\/([^/]+)\/verify$/, h: () => ({ ok: 'true' }) },
]

// 分发入口

export function route(method, url, init) {
  if (!url.startsWith('/api/')) return null
  const qAt = url.indexOf('?')
  const path = qAt === -1 ? url : url.slice(0, qAt)
  const query = new URLSearchParams(qAt === -1 ? '' : url.slice(qAt + 1))
  let body
  if (init && typeof init.body === 'string') {
    try { body = JSON.parse(init.body) } catch { body = undefined }
  }
  for (const r of routes) {
    if (r.m !== method) continue
    const m = r.re.exec(path)
    if (!m) continue
    const result = r.h({ params: m.slice(1), query, body, init })
    if (result && typeof result.then === 'function') return result.then(normalize)
    return normalize(result)
  }
  return { status: 404, body: { error: '演示环境未实现该接口' } }
}
function normalize(result) {
  if (result === undefined || result === null) return { status: 200, body: { ok: true } }
  // 仅当 status 是合法 HTTP 状态码时才视为响应描述符；业务载荷本身可能带 status 字段
  // （如 oauth/status 返回 { status: 'pending' }），不能一概透传为响应状态
  if (typeof result.status === 'number' && result.status >= 200 && result.status <= 599) return result
  return { status: 200, body: result }
}
