// 演示模式合成数据生成器
// 结构与真实 API 响应逐字段对齐（参考 bot/adminpanel 各 DTO 与真实抓取的 seed/*.json），
// 数值为围绕固定种子随机生成的合理值。仅被 handlers.js 引用，正常构建不进产物。

//  基础工具 

// 固定种子伪随机：同一会话内每次生成都稳定，轮询/翻页时数据不跳变
let s = 20261008
function rand() {
  s |= 0; s = (s + 0x6d2b79f5) | 0
  let t = Math.imul(s ^ (s >>> 15), 1 | s)
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296
}
const ri = (min, max) => min + Math.floor(rand() * (max - min + 1))
const pick = (arr) => arr[Math.floor(rand() * arr.length)]
const pad = (n, w = 2) => String(n).padStart(w, '0')

// 本地时区 RFC3339（与真实接口返回格式一致）
export function toLocalIso(date) {
  const off = -date.getTimezoneOffset()
  const sign = off >= 0 ? '+' : '-'
  const abs = Math.abs(off)
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}` +
    `${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`
}
export const dateStr = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`

//  演示场景人物 

export const DEMO_GROUP = 'qq:123456'
export const DEMO_ADMIN = 'qq:10000'

const MEMBERS = [
  { id: 10000, nick: '管理员' },
  { id: 10001, nick: '星野' },
  { id: 10002, nick: '阿白' },
  { id: 10003, nick: '林然' },
  { id: 10005, nick: '深夜程序员' },
  { id: 10006, nick: '咕咕鸟' },
]

// 群聊语料（用户发言 → 偶尔触发 AI 回复）
const GROUP_CHATTER = [
  '今天 NapCat 更新了没，有人遇到风控吗',
  '问下大佬，插件的热更新是怎么实现的呀',
  '刚在群里 @机器人 查了天气，回复还挺快',
  '这个面板的 Token 统计图表好酷',
  '有人玩过 computer_use 工具吗，AI 自己操作屏幕有点离谱',
  'docker 部署记得挂载 data 目录，血的教训',
  '定时任务每天早上八点给我发新闻早报，舒服了',
  '飞书适配器有人接吗？webhook 要公网有点麻烦',
  '@mcp 大佬 问下 MCP 的 sse 端点怎么配',
  '刚让 AI 帮我写了段油猴脚本，直接能跑',
  '群主什么时候出一期插件开发教程',
  '昨晚机器人抽风，一直重复回复，后来发现是复读机插件开了',
]

const AI_REPLIES = [
  '这是我的回答：NapCat 建议跟随官方最新版，风控问题多与账号环境有关，可以先在面板里把消息处理超时调大试试。',
  '插件热更新是插件市场通过重新编译 + 重启实现的，安装即生效，具体可以看文档的插件市场章节。',
  '今天多云转晴，18~26℃，东北风 3 级，适合出门～',
  '已经帮你记下了：每周三晚上游戏开黑。到时候我会提醒大家。',
  'MCP 的 streamable 端点在「扩展配置 → MCP」里加一条 JSON 即可，transport 填 streamable。',
]

const NOTICE_TITLES = ['戳一戳', '群文件上传', '成员入群', '精华消息设置', '全群禁言']

//  消息日志（msglog.Entry 形状）

export function buildMsgLogs(now) {
  const items = []
  let id = 8000
  // 过去 ~3 小时，平均每 2~4 分钟一条
  let t = now - ri(150, 200) * 60 * 1000
  while (t < now - 30 * 1000) {
    const r = rand()
    if (r < 0.72) {
      const m = pick(MEMBERS)
      items.push({
        id, time: toLocalIso(new Date(t)), type: 'group',
        group_id: 123456, user_id: m.id, nickname: m.nick, text: pick(GROUP_CHATTER),
      })
    } else if (r < 0.88) {
      items.push({
        id, time: toLocalIso(new Date(t)), type: 'group',
        group_id: 123456, user_id: 2841701201, nickname: 'Ania', text: pick(AI_REPLIES),
      })
    } else if (r < 0.95) {
      items.push({
        id, time: toLocalIso(new Date(t)), type: 'friend',
        user_id: 10004, nickname: '同事K', text: pick([
          '下班一起去吃饭？', '那个服务器日志我发你邮箱了', '周末组队不',
        ]),
      })
    } else {
      const m = pick(MEMBERS)
      items.push({
        id, time: toLocalIso(new Date(t)), type: 'notice',
        group_id: 123456, user_id: m.id, nickname: m.nick, title: pick(NOTICE_TITLES),
      })
    }
    id++
    t += ri(2, 4) * 60 * 1000
  }
  return items.reverse() // 新的在前，id 随时间递增，翻页游标用 id
}

//  Query 日志（querylog.Entry 形状）

const QUERIES = [
  { q: '帮我总结一下今天的群聊都聊了什么', tools: [['qq_get_msg_history', '读取群最近 200 条消息'], ['subagent_run', '归纳话题并写成简报']] },
  { q: 'MCP 的 streamable 和 sse 有什么区别？', tools: [['web_search', '搜索 MCP 传输协议对比']] },
  { q: '今天有什么科技新闻', tools: [['web_search', '搜索今日科技新闻'], ['web_explore', '阅读其中 3 篇原文']] },
  { q: '记一下：周五晚上团建', tools: [['memory_save', '保存到群聊长期记忆']] },
  { q: '看看这张图里报错是什么意思', tools: [['load_images', '加载用户发送的截图']] },
  { q: '帮我建个每天早上 8 点的早报任务', tools: [['clock_create', '创建定时任务「每日早报」']] },
  { q: 'AniaBot 怎么部署到 docker', tools: [['web_explore', '阅读官方文档部署章节']] },
  { q: '写一个快速排序，Go 语言的', tools: [] },
  { q: '群友小张的 QQ 号是多少', tools: [['qq_get_group_member_list', '检索群成员列表']] },
  { q: '现在服务器负载怎么样', tools: [['bash', 'uptime && free -h']] },
]
const QUERY_STATUSES = ['success', 'success', 'success', 'success', 'running', 'error', 'timeout', 'interrupted']

export function buildQueryLogs(now) {
  const items = []
  let n = 1
  let t = now - ri(100, 130) * 60 * 1000
  while (t < now - 60 * 1000) {
    const q = pick(QUERIES)
    const status = pick(QUERY_STATUSES)
    const done = status !== 'running'
    const prompt = ri(2, 18) * 1000
    const completion = ri(300, 6000)
    const cached = Math.floor(prompt * (0.3 + rand() * 0.3))
    const iterations = q.tools.length + ri(0, 2)
    const isGroup = rand() < 0.7
    const item = {
      id: pad(n, 8), // 定长字符串 id，字典序 == 时间序
      time: toLocalIso(new Date(t)),
      chat_type: isGroup ? 'group' : 'friend',
      target_id: isGroup ? DEMO_GROUP : pick([DEMO_ADMIN, 'qq:10004']),
      senders: [pick(MEMBERS).id],
      query: q.q,
      status,
      duration_ms: ri(2, 60) * 1000,
      iterations,
      prompt_tokens: prompt,
      completion_tokens: completion,
      total_tokens: prompt + completion,
      cached_tokens: cached,
    }
    if (q.tools.length) {
      item.tool_calls = q.tools.map(([name, args]) => ({
        name, arguments: args,
        result: name === 'bash' ? 'up 12 days, load 0.42 …' : '（执行成功）',
        duration_ms: ri(1, 9) * 1000,
      }))
      item.tool_calls_total = q.tools.length + (rand() < 0.3 ? 1 : 0)
    }
    if (done) {
      if (status === 'success') item.reply = pick(AI_REPLIES)
      if (status === 'error') item.error = '上游 API 返回 429：请求过于频繁，已重试 3 次'
      if (status === 'timeout') item.error = '执行超时（300s），已被强制中止'
      if (status === 'interrupted') item.error = '被用户 /stop 指令中止'
    }
    items.push(item)
    n++
    t += ri(4, 9) * 60 * 1000
  }
  return items.reverse()
}

//  定时任务日志（tasklog.Entry 形状，围绕 3 个真实任务）

export function buildTaskLogs(now) {
  const tasks = [
    { id: '1', title: '每日早报', runs: 5, content: '今日科技早报：1) Go 1.27 发布…（共 5 条）' },
    { id: '2', title: '周五群活跃度周报', runs: 2, content: '本周群里最热的话题是「插件热更新」，其次是…' },
    { id: '3', title: '月度服务器巡检提醒', runs: 1, content: '巡检提醒已发送给管理员。' },
  ]
  const items = []
  let n = 1
  for (const task of tasks) {
    for (let k = task.runs; k >= 1; k--) {
      const t = now - k * ri(20, 30) * 3600 * 1000 - ri(0, 3600) * 1000
      const prompt = ri(8, 40) * 1000
      const completion = ri(500, 4000)
      const status = rand() < 0.85 ? 'success' : pick(['timeout', 'error'])
      const item = {
        id: pad(n, 8),
        task_id: task.id,
        task_title: task.title,
        target_type: task.id === '3' ? 'friend' : 'group',
        target_id: task.id === '3' ? DEMO_ADMIN : DEMO_GROUP,
        trigger_time: toLocalIso(new Date(t)),
        trigger_content: `【定时任务】${task.title}`,
        status,
        duration_ms: ri(10, 90) * 1000,
        iterations: ri(2, 6),
        tool_calls: [
          { name: 'web_search', arguments: `主题：${task.title}`, duration_ms: ri(2, 6) * 1000 },
          { name: 'subagent_run', arguments: '整理并生成最终文案', duration_ms: ri(5, 20) * 1000 },
        ],
        tool_calls_total: ri(2, 5),
        prompt_tokens: prompt,
        completion_tokens: completion,
        total_tokens: prompt + completion,
        cached_tokens: Math.floor(prompt * 0.4),
      }
      if (status === 'success') {
        item.reply = task.content
        item.finished_at = toLocalIso(new Date(t + item.duration_ms))
      } else if (status === 'timeout') {
        item.error = '任务执行超时（180s）'
      } else {
        item.error = '上游 API 网络错误，已重试 3 次'
      }
      items.push(item)
      n++
    }
  }
  return items.sort((a, b) => (a.trigger_time < b.trigger_time ? 1 : -1))
}

//  控制台日志追加池（真实启动日志来自 seed/consolelogs.json）

const CONSOLE_POOL = [
  { level: 'info', message: '收到群消息', attrs: [{ key: 'group', value: '123456' }, { key: 'user', value: '10001' }] },
  { level: 'info', message: 'AI 回复完成', attrs: [{ key: '耗时', value: '8.3s' }, { key: '迭代', value: '3' }] },
  { level: 'info', message: '定时任务触发', attrs: [{ key: 'task', value: '每日早报' }] },
  { level: 'info', message: '上下文压缩完成', attrs: [{ key: '会话', value: 'g:qq:123456' }, { key: '压缩后', value: '4.2k tokens' }] },
  { level: 'warn', message: '上游 API 限流，退避重试', attrs: [{ key: 'attempt', value: '2' }] },
  { level: 'info', message: '长期记忆注入', attrs: [{ key: '条数', value: '2' }] },
  { level: 'info', message: '图片识别完成（OCR 回退）', attrs: [{ key: 'hash', value: 'a3f9c2' }] },
  { level: 'info', message: 'MCP 工具按需加载', attrs: [{ key: 'server', value: 'fetch' }, { key: 'tools', value: '1' }] },
]
export function nextConsoleLine(now, id) {
  const line = pick(CONSOLE_POOL)
  return { id, time: toLocalIso(new Date(now)), level: line.level, message: line.message, attrs: line.attrs }
}

//  Token 统计（tokenstats.go 形状：汇总为数值，detail 为字符串数值）

function dayAcc(date, requests, prompt, completion, cached) {
  return {
    date,
    requests,
    prompt_tokens: prompt,
    completion_tokens: completion,
    total_tokens: prompt + completion,
    cached_tokens: cached,
    cache_hit_rate: prompt ? Math.round((cached / prompt) * 1000) / 1000 : 0,
  }
}
function sumAcc(list) {
  const acc = { requests: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0, cached_tokens: 0 }
  for (const d of list) {
    acc.requests += d.requests
    acc.prompt_tokens += d.prompt_tokens
    acc.completion_tokens += d.completion_tokens
    acc.total_tokens += d.total_tokens
    acc.cached_tokens += d.cached_tokens
  }
  acc.cache_hit_rate = acc.prompt_tokens ? Math.round((acc.cached_tokens / acc.prompt_tokens) * 1000) / 1000 : 0
  return acc
}
const numAcc = (a) => ({
  requests: a.requests,
  prompt_tokens: a.prompt_tokens,
  completion_tokens: a.completion_tokens,
  total_tokens: a.total_tokens,
  cached_tokens: a.cached_tokens,
  cache_hit_rate: a.cache_hit_rate,
  avg_total_tokens: a.requests ? Math.round(a.total_tokens / a.requests) : 0,
})

// 生成过去 N 天（含今天）的每日用量
function genDays(now, days) {
  const out = []
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(now - i * 86400000)
    const isToday = i === 0
    const requests = isToday ? ri(40, 120) : ri(90, 320)
    const prompt = requests * ri(3, 9) * 1000
    const completion = requests * ri(600, 1800)
    const cached = Math.floor(prompt * (0.35 + rand() * 0.25))
    out.push(dayAcc(dateStr(d), requests, prompt, completion, cached))
  }
  return out
}

export function buildTokenStats(now) {
  const daily = genDays(now, 14)
  return { summary: sumAcc(daily), today: { ...daily[daily.length - 1] }, daily }
}

export function buildTokenStatsDetail(range, now, startStr, endStr) {
  let days
  if (range === 'today') days = 1
  else if (range === 'yesterday') days = 1
  else if (range === '7d') days = 7
  else if (range === '30d') days = 30
  else if (range === 'month') days = new Date(now.getFullYear(), now.getMonth() + 1, 0).getDate()
  else if (range === 'custom' && startStr && endStr) {
    const st = Date.parse(startStr)
    const et = Date.parse(endStr)
    days = Math.max(1, Math.min(62, Math.round((et - st) / 86400000) + 1))
  } else days = 30

  const daily = genDays(now, days)
  if (range === 'yesterday') daily[0] = daily[0] // 结构一致即可
  const summaryNum = sumAcc(daily)

  // 分维度拆分：来源（query/task 7:3）、会话类型（group/friend 7:3）
  const split = (a, ratio) => {
    const r = { requests: Math.round(a.requests * ratio), prompt_tokens: Math.round(a.prompt_tokens * ratio), completion_tokens: Math.round(a.completion_tokens * ratio), total_tokens: 0, cached_tokens: Math.round(a.cached_tokens * ratio), cache_hit_rate: a.cache_hit_rate }
    r.total_tokens = r.prompt_tokens + r.completion_tokens
    return r
  }
  const statusNames = ['success', 'error', 'timeout', 'stopped', 'interrupted']
  const byStatus = {}
  let rest = summaryNum.requests
  for (const st of statusNames) {
    const c = st === 'success' ? Math.floor(rest * 0.93) : ri(0, Math.max(1, Math.floor(rest * 0.02)))
    byStatus[st] = c
    rest = Math.max(0, rest - c)
  }
  const topTargets = [
    { chat_type: 'group', target_id: DEMO_GROUP, ...split(sumAcc(daily.slice(-5)), 0.8) },
    { chat_type: 'friend', target_id: DEMO_ADMIN, ...split(sumAcc(daily.slice(-5)), 0.12) },
    { chat_type: 'friend', target_id: 'qq:10004', ...split(sumAcc(daily.slice(-7)), 0.06) },
    { chat_type: 'group', target_id: 'qq:7891011', ...split(sumAcc(daily.slice(-9)), 0.04) },
  ]
  const hourly = Array.from({ length: 24 }, (_, h) => {
    const q = ri(0, 14), tk = ri(0, 6)
    const mk = (n) => ({ requests: n, prompt_tokens: n * ri(2, 8) * 1000, completion_tokens: n * ri(400, 1500), total_tokens: 0, cached_tokens: 0, cache_hit_rate: ri(30, 60) / 100 })
    const q1 = mk(q), t1 = mk(tk)
    for (const x of [q1, t1]) {
      x.total_tokens = x.prompt_tokens + x.completion_tokens
      x.cached_tokens = Math.floor(x.prompt_tokens * 0.4)
    }
    const total = sumAcc([q1, t1])
    return { date: '', query: q1, task: t1, total }
  })
  const dailyOut = daily.map((d) => {
    const q = { requests: Math.round(d.requests * 0.7), prompt_tokens: Math.round(d.prompt_tokens * 0.7), completion_tokens: Math.round(d.completion_tokens * 0.7), total_tokens: 0, cached_tokens: Math.round(d.cached_tokens * 0.7), cache_hit_rate: d.cache_hit_rate }
    const t = { requests: d.requests - q.requests, prompt_tokens: d.prompt_tokens - q.prompt_tokens, completion_tokens: d.completion_tokens - q.completion_tokens, total_tokens: 0, cached_tokens: d.cached_tokens - q.cached_tokens, cache_hit_rate: d.cache_hit_rate }
    for (const x of [q, t]) x.total_tokens = x.prompt_tokens + x.completion_tokens
    const total = sumAcc([q, t])
    return { date: d.date, query: q, task: t, total }
  })

  return {
    range: range || 'all',
    summary: numAcc(summaryNum),
    today: numAcc(daily[daily.length - 1]),
    by_source: { query: numAcc(split(summaryNum, 0.7)), task: numAcc(split(summaryNum, 0.3)) },
    by_chat_type: { group: numAcc(split(summaryNum, 0.72)), friend: numAcc(split(summaryNum, 0.28)) },
    by_status: byStatus,
    top_targets: topTargets.map((t) => ({ chat_type: t.chat_type, target_id: t.target_id, ...numAcc(t) })),
    hourly: hourly.map((h) => ({ date: h.date, query: h.query, task: h.task, total: h.total })),
    daily: dailyOut,
    iterations: summaryNum.requests * 3,
    avg_iterations: summaryNum.requests ? Math.round((summaryNum.requests * 3.4 / summaryNum.requests) * 10) / 10 : 0,
  }
}

//  配额（plugininfo/quota.go 形状）

export function buildQuota(now) {
  const mk = (key, kind, target, used) => {
    const limit = 200000
    return { key, kind, target, used, limit, remaining: Math.max(0, limit - used), reached: used >= limit }
  }
  const sessions = [
    mk('g:qq:123456', 'group', DEMO_GROUP, ri(20, 130) * 1000),
    mk('f:qq:10000', 'friend', DEMO_ADMIN, ri(2, 40) * 1000),
    mk('f:qq:10004', 'friend', 'qq:10004', ri(1, 20) * 1000),
  ]
  const globalUsed = sessions.reduce((a, x) => a + x.used, 0)
  const globalLimit = 2000000
  return {
    date: dateStr(new Date(now)),
    global_used: globalUsed,
    global_limit: globalLimit,
    global_remaining: globalLimit - globalUsed,
    global_reached: false,
    sessions,
  }
}

//  通讯录（OneBot groupdetail/friendlist 形状）

export function buildContacts(kind) {
  if (kind === 'friends') {
    return [
      { user_id: 10000, nickname: 'Ania 管理员', remark: '我自己' },
      { user_id: 10004, nickname: 'K', remark: '同事-K' },
      { user_id: 10007, nickname: 'Neko', remark: '' },
      { user_id: 10008, nickname: '夜航西飞', remark: '服务器搭子' },
      { user_id: 10009, nickname: 'Momo', remark: '' },
    ]
  }
  return [
    { group_id: 123456, group_name: 'AniaBot 交流群', member_count: 486, max_member_count: 500 },
    { group_id: 7891011, group_name: '摸鱼打字机', member_count: 92, max_member_count: 200 },
    { group_id: 3456789, group_name: 'Go 夜读 · 中文社区', member_count: 2380, max_member_count: 3000 },
  ]
}

//  技能（plugininfo/skill.go 形状）

const SKILLS = [
  {
    name: 'daily-news', description: '按主题抓取 RSS 与搜索结果，生成每日新闻早报',
    location: 'daily-news', refs: ['sources.md'], extras: ['render.py'],
    content: '---\nname: daily-news\ndescription: 按主题抓取 RSS 与搜索结果，生成每日新闻早报\n---\n\n# 每日早报技能\n\n## 步骤\n\n1. 读取 `sources.md` 中的订阅源列表\n2. 调用 web_search 补充当日热点\n3. 挑选 5 条，用 `render.py` 渲染为 Markdown 早报\n4. 发送到目标群聊\n\n## 注意\n\n- 每条新闻附原文链接\n- 早报开头写一句轻松的问候语\n',
    files: [
      { name: 'sources.md', kind: 'reference', size: 512, content: '# 订阅源\n\n- https://example.com/feed/tech.xml\n- https://example.com/feed/ai.xml\n' },
      { name: 'render.py', kind: 'extra', size: 1204 },
    ],
  },
  {
    name: 'group-rules', description: '回答群规相关问题，新人入群时自动发送群规摘要',
    location: 'group-rules.md', refs: [], extras: [],
    content: '---\nname: group-rules\ndescription: 回答群规相关问题，新人入群时自动发送群规摘要\n---\n\n# 群规技能\n\n## 群规要点\n\n1. 禁止广告与刷屏\n2. 提问请附上报错信息与复现步骤\n3. 每周三晚上为固定游戏开黑时间\n\n新人入群时，发送以上摘要并表示欢迎。\n',
    files: [],
  },
  {
    name: 'image-style', description: '把用户发的图片描述成不同风格文案（小红书/知乎/诗歌）',
    location: 'image-style', refs: ['styles.md'], extras: [],
    content: '---\nname: image-style\ndescription: 把用户发的图片描述成不同风格文案\n---\n\n# 图片风格化文案\n\n用户发送图片并指定风格时：\n\n1. 调用 load_images 查看图片\n2. 参考 `styles.md` 中的风格模板\n3. 生成对应风格的文案（默认小红书体）\n',
    files: [
      { name: 'styles.md', kind: 'reference', size: 890, content: '# 风格模板\n\n## 小红书体\nemoji 开头、短句、结尾引导互动…\n\n## 知乎体\n「先说结论」开头，分层论证…\n' },
    ],
  },
]

export function buildSkills() {
  return {
    skills: SKILLS.map(({ name, description, location, refs, extras }) => ({ name, description, location, refs, extras })),
    dir: './data/skills',
    whitelist: [],
  }
}
export function buildSkillDetail(name) {
  const sk = SKILLS.find((x) => x.name === name)
  if (!sk) return null
  return { name: sk.name, description: sk.description, location: sk.location, content: sk.content, files: sk.files }
}

//  主机动态指标（hostinfo.go HostSnapshot 形状）

export function createHostSim(baseHost) {
  let cpu = baseHost.cpu_percent ?? 12
  let target = 15
  const history = Array.from({ length: 60 }, () => Math.round((8 + rand() * 20) * 10) / 10)
  let memPercent = baseHost.mem_percent ?? 30
  const loadAt = Date.now()
  return function snapshot() {
    if (Math.abs(cpu - target) < 0.8) target = 6 + rand() * 30
    cpu += (target - cpu) * 0.25 + (rand() - 0.5) * 2
    cpu = Math.max(2, Math.min(95, cpu))
    history.push(Math.round(cpu * 10) / 10)
    if (history.length > 60) history.shift()
    memPercent = Math.max(20, Math.min(90, memPercent + (rand() - 0.5) * 1.6))
    const memTotal = baseHost.mem_total
    return {
      ...baseHost,
      cpu_percent: Math.round(cpu * 10) / 10,
      cpu_history: history.slice(),
      mem_used: Math.floor(memTotal * memPercent / 100),
      mem_percent: Math.round(memPercent * 100) / 100,
      uptime_sec: (baseHost.uptime_sec ?? 0) + Math.floor((Date.now() - loadAt) / 1000),
      go_mem_alloc: 2400000 + ri(0, 900000),
    }
  }
}
