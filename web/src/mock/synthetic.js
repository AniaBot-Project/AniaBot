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

const AI_REPLIES = [
  '这是我的回答：NapCat 建议跟随官方最新版，风控问题多与账号环境有关，可以先在面板里把消息处理超时调大试试。',
  '插件热更新是插件市场通过重新编译 + 重启实现的，安装即生效，具体可以看文档的插件市场章节。',
  '今天多云转晴，18~26℃，东北风 3 级，适合出门～',
  '已经帮你记下了：每周三晚上游戏开黑。到时候我会提醒大家。',
  'MCP 的 streamable 端点在「扩展配置 → MCP」里加一条 JSON 即可，transport 填 streamable。',
]

//  消息日志（msglog.Entry 形状）
//
// 演示数据覆盖 3 个群聊 + 2 个私聊 + 群通知：包含图片、文件、引用、合并转发、
// @、表情与分享卡片，用来展示聊天式面板的各类消息形态。图片使用自绘 SVG
// data URI，离线也能显示，不依赖外部图床。

function svgPhoto(from, to, label, sub = 'AniaBot demo') {
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="720" height="480" viewBox="0 0 720 480">'
    + '<defs><linearGradient id="bg" x1="0" y1="0" x2="1" y2="1">'
    + `<stop offset="0" stop-color="${from}"/><stop offset="1" stop-color="${to}"/>`
    + '</linearGradient></defs>'
    + '<rect width="720" height="480" fill="url(#bg)"/>'
    + '<circle cx="576" cy="108" r="56" fill="#ffffff" opacity=".5"/>'
    + '<path d="M0 356 Q180 280 360 344 T720 326 V480 H0Z" fill="#ffffff" opacity=".2"/>'
    + '<path d="M0 400 Q220 338 440 396 T720 374 V480 H0Z" fill="#ffffff" opacity=".14"/>'
    + `<text x="360" y="238" text-anchor="middle" font-family="Nunito, 'PingFang SC', sans-serif" font-size="34" font-weight="800" fill="#ffffff" opacity=".95">${label}</text>`
    + `<text x="360" y="276" text-anchor="middle" font-family="Nunito, 'PingFang SC', sans-serif" font-size="16" fill="#ffffff" opacity=".72">${sub}</text>`
    + '</svg>'
  return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg)
}

const PHOTO_SEA = svgPhoto('#5fc8d8', '#2a7fbe', '海边的周末')
const PHOTO_SUNSET = svgPhoto('#f2a65a', '#c8556d', '傍晚的云')
const PHOTO_CAT = svgPhoto('#9fd49b', '#4f8f6b', '猫猫巡逻中')
const PHOTO_MENU = svgPhoto('#f4d29a', '#d99a55', '楼下新开的面馆')

const DEMO_MD = 'data:text/markdown;charset=utf-8,' + encodeURIComponent(
  '# NapCat 部署踩坑记录\n\n1. Docker 部署记得挂载 data 目录\n2. WS 模式注意 worker 数量\n3. 出现风控先检查账号环境\n',
)

export function buildMsgLogs(now) {
  const raw = []
  const add = (minutesAgo, entry) => {
    raw.push({ minutesAgo, ...entry })
  }
  const group = (minutesAgo, groupId, userId, nickname, text, media) =>
    add(minutesAgo, { type: 'group', group_id: groupId, user_id: userId, nickname, text, ...(media ? { media } : {}) })
  const friend = (minutesAgo, userId, nickname, text, media) =>
    add(minutesAgo, { type: 'friend', user_id: userId, nickname, text, ...(media ? { media } : {}) })

  const G1 = 'qq:123456' // AniaBot 交流群
  const G2 = 'qq:7891011' // 摸鱼打字机
  const G3 = 'qq:3456789' // Go 夜读 · 中文社区

  /* ------- 群 1：AniaBot 交流群（最新的会话，默认打开） ------- */
  group(152, G1, 'qq:10001', '星野', '早，今天好热')
  group(150, G1, 'qq:10002', '阿白', '上海已经 35 度了，出门像进烤箱')
  group(147, G1, 'qq:10003', '林然', '我这边还下雨，凉快得想穿外套')
  group(140, G1, 'qq:10001', '星野', '问下大佬，插件热更新是怎么实现的呀')
  group(138, G1, 'qq:10000', '管理员',
    '<reply>\n  [nickname:星野 id:qq:10001]: 问下大佬，插件热更新是怎么实现的呀\n</reply>\n插件市场那边是重新编译 + 重启，安装完就生效')
  group(136, G1, 'qq:10001', '星野', '懂了，谢谢～')
  group(128, G1, 'qq:10005', '深夜程序员', '[at:id:qq:10000] 帮我看下这个报错：MCP 的 SSE 端点一直 401')
  group(125, G1, 'qq:10000', '管理员',
    '<reply>\n  [nickname:深夜程序员 id:qq:10005]: [at:id:qq:10000] 帮我看下这个报错：MCP 的 SSE 端点一直 401\n</reply>\n先检查 mcp_json 里的 headers 有没有过期，401 一般是 token 失效')
  group(123, G1, 'qq:10005', '深夜程序员', '确实是 token 过期了，改了 [QQ表情:捂脸]')
  group(110, G1, 'qq:10006', '咕咕鸟', '我也踩过，还以为是被墙了')
  group(96, G1, 'qq:10001', '星野', '[图片]', [{
    kind: 'image', name: '海边的周末.png', url: PHOTO_SEA,
  }])
  group(94, G1, 'qq:10002', '阿白', '好出片，求机位')
  group(88, G1, 'qq:10001', '星野', '就普通手机拍的，傍晚光线好')
  group(72, G1, 'qq:10005', '深夜程序员',
    '[文件:NapCat 部署踩坑记录.md fileId:fid-demo url:https://cdn.example.com/demo.md]',
    [{ kind: 'file', name: 'NapCat 部署踩坑记录.md', size: 18234, url: DEMO_MD, file_id: 'fid-demo' }])
  group(70, G1, 'qq:10002', '阿白', '收到，正好下周要重装')
  add(64, {
    type: 'notice', group_id: G1, user_id: 'qq:10003', title: '戳一戳',
    text: 'qq:10003 戳了戳 qq:10000',
  })
  group(58, G1, 'qq:10001', '星野',
    '<reply>\n  [nickname:阿白 id:qq:10002]: 收到，正好下周要重装\n</reply>\n同求 +1')
  group(44, G1, 'qq:10000', '管理员',
    '\n<合并转发消息>\n  [nickname:管理员 id:qq:10000]: 周末活动安排：周六晚 8 点组队开黑，语音频道见\n  [nickname:星野 id:qq:10001]: 收到，带我一个\n  [nickname:咕咕鸟 id:qq:10006]: +1\n</合并转发消息>\n')
  // 嵌套合并转发（转发节点自己又是一条转发）：文本格式与 FriendlyText 一致，
  // 内层块整体多缩进两个空格；引用它的消息同样把整段内容缩进一层。转发内部的
  // 图片以 in_forward 媒体记录，面板会按 [图片] 占位顺序填回对应节点。
  const nestedForward = '<合并转发消息>'
    + '\n  [nickname:管理员 id:qq:10000]: '
    + '\n  <合并转发消息>'
    + '\n    [nickname:星野 id:qq:10001]: 周末的联机房间开好了[图片]'
    + '\n    [nickname:咕咕鸟 id:qq:10006]: 我十点到'
    + '\n  </合并转发消息>'
    + '\n  [nickname:阿白 id:qq:10002]: 这也能转发？'
    + '\n  <合并转发消息>'
    + '\n    [nickname:星野 id:qq:10001]: 可以，聊天记录能套娃[图片]'
    + '\n    [nickname:林然 id:qq:10003]: 孩子们'
    + '\n  </合并转发消息>'
    + '\n</合并转发消息>'
  group(43, G1, 'qq:10000', '管理员', '\n' + nestedForward + '\n', [
    { kind: 'image', name: '联机房间.png', url: PHOTO_SEA, in_forward: true },
    { kind: 'image', name: '套娃.png', url: PHOTO_MENU, in_forward: true },
  ])
  group(41, G1, 'qq:10003', '林然',
    '<reply>\n  [nickname:管理员 id:qq:10000]: \n  '
      + nestedForward.replace(/\n/g, '\n  ') + '\n</reply>\n我看不懂了')
  group(36, G1, 'qq:10003', '林然', '周六晚上我可能十点后才能上')
  group(30, G1, 'qq:10002', '阿白', '没事，语音频道一直开着')
  group(22, G1, 'qq:10001', '星野', '[图片][图片]', [
    { kind: 'image', name: '傍晚的云-1.png', url: PHOTO_SUNSET },
    { kind: 'image', name: '傍晚的云-2.png', url: PHOTO_MENU },
  ])
  group(20, G1, 'qq:10006', '咕咕鸟', '第二张绝了')
  group(8, G1, 'qq:10002', '阿白', '这周天气好像都不错')
  group(3, G1, 'qq:10001', '星野', '那就周六见')
  group(1, G1, 'qq:10000', '管理员',
    '[文件:周末活动安排.xlsx fileId:fid-demo-2 url:]',
    [{ kind: 'file', name: '周末活动安排.xlsx', size: 20312, file_id: 'fid-demo-2' }])

  /* ------- 群 2：摸鱼打字机 ------- */
  group(118, G2, 'qq:10007', 'Neko', '这周有人打羽毛球吗')
  group(112, G2, 'qq:10009', 'Momo', '周三晚上可以，老地方？')
  group(104, G2, 'qq:10007', 'Neko', '就周三，七点')
  add(48, {
    type: 'notice', group_id: G2, user_id: 'qq:10009', title: '群管理员变动',
    text: 'qq:10009 被设置为管理员',
  })
  group(40, G2, 'qq:10007', 'Neko', '[图片]', [{ kind: 'image', name: '猫.png', url: PHOTO_CAT }])
  group(12, G2, 'qq:10009', 'Momo', '这猫也太胖了')

  /* ------- 群 3：Go 夜读 ------- */
  group(100, G3, 'qq:10008', '夜航西飞',
    '[分享卡片,标题: Go 1.25 发布,描述: 新版本在类型推断与工具链上带来一批改进,链接: (https://go.dev/blog/go1.25)]')
  group(92, G3, 'qq:10003', '林然', '这个新特性我还没用过')
  group(68, G3, 'qq:10008', '夜航西飞',
    '<reply>\n  [nickname:林然 id:qq:10003]: 这个新特性我还没用过\n</reply>\n类型推断那块值得试试，写起来省不少样板代码')

  /* ------- 私聊：同事-K / 夜航西飞 ------- */
  friend(135, 'qq:10004', '同事-K', '下班一起去吃饭？')
  friend(133, 'qq:10004', '同事-K', '楼下新开了家面馆')
  friend(60, 'qq:10004', '同事-K', '[图片]', [{ kind: 'image', name: '菜单.png', url: PHOTO_MENU }])
  friend(35, 'qq:10004', '同事-K', '那个服务器日志我发你邮箱了')
  add(18, {
    type: 'notice', user_id: 'qq:10004', title: '好友消息撤回',
    text: 'qq:10004 撤回了一条消息',
  })
  friend(90, 'qq:10008', '夜航西飞', '周末去爬山吗')
  friend(84, 'qq:10008', '夜航西飞', '路线我发你，全程大概四小时')

  // 先按时间排序再分配 ID：真实日志的 ID 就是到达顺序，会话摘要按 ID 排序
  raw.sort((a, b) => b.minutesAgo - a.minutesAgo)
  raw.forEach((entry, i) => {
    entry.id = 8000 + i
    entry.time = toLocalIso(new Date(now - entry.minutesAgo * 60 * 1000))
    delete entry.minutesAgo
  })
  return raw.reverse() // 新的在前，id 随时间递增，翻页游标用 id
}

//  Query 日志（querylog.Entry 形状）

const QUERIES = [
  { q: '帮我总结一下今天的群聊都聊了什么', tools: [['qq_get_msg_history', '读取群最近 200 条消息'], ['subagent_run', '归纳话题并写成简报']] },
  { q: 'MCP 的 streamable 和 sse 有什么区别？', tools: [['skill_read', '读取内置 web 技能用法'], ['bash', 'webctl.py search --deepseek "MCP 传输协议"']] },
  { q: '今天有什么科技新闻', tools: [['bash', 'webctl.py search --deepseek "今日科技新闻"'], ['bash', 'webctl.py fetch --deepseek "https://example.com/news"']] },
  { q: '记一下：周五晚上团建', tools: [['memory_save', '保存到群聊长期记忆']] },
  { q: '看看这张图里报错是什么意思', tools: [['load_images', '加载用户发送的截图']] },
  { q: '帮我建个每天早上 8 点的早报任务', tools: [['clock_create', '创建定时任务「每日早报」']] },
  { q: 'AniaBot 怎么部署到 docker', tools: [['bash', 'webctl.py fetch --deepseek "https://docs.aniabot.dev/deploy"']] },
  { q: '写一个快速排序，Go 语言的', tools: [] },
  { q: '群友小张的 QQ 号是多少', tools: [['qq_get_group_member_list', '检索群成员列表']] },
  { q: '现在服务器负载怎么样', tools: [['bash', 'uptime && free -h']] },
]
const QUERY_STATUSES = ['success', 'success', 'success', 'success', 'running', 'error', 'timeout', 'interrupted']

// 主动互动的触发说明（与后端的 triggerSummary 形状一致）
const PROACTIVE_TRIGGERS = [
  '【主动互动 · 冷场触发】意图 abcd1234\n会话已安静 1 小时 25 分钟，由 AI 判断是否开口。\n最近消息：\n[14:02] 小明: 今天好热',
  '【主动互动 · AI 预约】意图 9f8e7d6c\n到点执行：\n问问面试结果怎么样了\n备注：上周三面的试',
  '【主动互动 · 手动触发】意图 5a4b3c2d\n管理员希望你说：\n提醒大家今晚 8 点开会',
]

export function buildQueryLogs(now) {
  const items = []
  let n = 1
  let t = now - ri(100, 130) * 60 * 1000
  while (t < now - 60 * 1000) {
    const proactive = n % 6 === 0
    const q = proactive ? { q: pick(PROACTIVE_TRIGGERS), tools: [] } : pick(QUERIES)
    const status = proactive ? pick(['success', 'success', 'skipped']) : pick(QUERY_STATUSES)
    const done = status !== 'running'
    const prompt = ri(2, 18) * 1000
    const completion = ri(300, 6000)
    const cached = Math.floor(prompt * (0.3 + rand() * 0.3))
    const iterations = q.tools.length + ri(0, 2)
    const isGroup = rand() < 0.7
    // 隔几条插入一条面板试聊记录，演示 Query 日志的「试聊」筛选
    const panel = !proactive && n % 9 === 0
    const item = {
      id: pad(n, 8), // 定长字符串 id，字典序 == 时间序
      time: toLocalIso(new Date(t)),
      chat_type: panel ? 'panel' : isGroup ? 'group' : 'friend',
      target_id: panel ? '通用试聊' : isGroup ? DEMO_GROUP : pick([DEMO_ADMIN, 'qq:10004']),
      query: q.q,
      status,
      duration_ms: ri(2, 60) * 1000,
      iterations,
      prompt_tokens: prompt,
      completion_tokens: completion,
      total_tokens: prompt + completion,
      cached_tokens: cached,
    }
    if (proactive) item.kind = 'proactive'
    if (panel) item.note = '面板试聊会话「通用试聊」'
    else item.senders = [pick(MEMBERS).id]
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
      if (status === 'skipped') item.note = 'AI 判断此刻无需发言：这个话题已经过去了'
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
  const statusNames = ['success', 'skipped', 'error', 'timeout', 'stopped', 'interrupted']
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
    { chat_type: 'panel', target_id: '通用试聊', ...split(sumAcc(daily.slice(-3)), 0.03) },
  ]
  const hourly = Array.from({ length: 24 }, (_, h) => {
    const q = ri(0, 14), p = ri(0, 2), tk = ri(0, 6)
    const mk = (n) => ({ requests: n, prompt_tokens: n * ri(2, 8) * 1000, completion_tokens: n * ri(400, 1500), total_tokens: 0, cached_tokens: 0, cache_hit_rate: ri(30, 60) / 100 })
    const q1 = mk(q), p1 = mk(p), t1 = mk(tk)
    for (const x of [q1, p1, t1]) {
      x.total_tokens = x.prompt_tokens + x.completion_tokens
      x.cached_tokens = Math.floor(x.prompt_tokens * 0.4)
    }
    const total = sumAcc([q1, p1, t1])
    return { date: '', query: q1, proactive: p1, task: t1, total }
  })
  const dailyOut = daily.map((d) => {
    const q = { requests: Math.round(d.requests * 0.65), prompt_tokens: Math.round(d.prompt_tokens * 0.65), completion_tokens: Math.round(d.completion_tokens * 0.65), total_tokens: 0, cached_tokens: Math.round(d.cached_tokens * 0.65), cache_hit_rate: d.cache_hit_rate }
    const p = { requests: Math.max(0, Math.round(d.requests * 0.05)), prompt_tokens: Math.round(d.prompt_tokens * 0.05), completion_tokens: Math.round(d.completion_tokens * 0.05), total_tokens: 0, cached_tokens: Math.round(d.cached_tokens * 0.05), cache_hit_rate: d.cache_hit_rate }
    const t = { requests: Math.max(0, d.requests - q.requests - p.requests), prompt_tokens: Math.max(0, d.prompt_tokens - q.prompt_tokens - p.prompt_tokens), completion_tokens: Math.max(0, d.completion_tokens - q.completion_tokens - p.completion_tokens), total_tokens: 0, cached_tokens: Math.max(0, d.cached_tokens - q.cached_tokens - p.cached_tokens), cache_hit_rate: d.cache_hit_rate }
    for (const x of [q, p, t]) x.total_tokens = x.prompt_tokens + x.completion_tokens
    const total = sumAcc([q, p, t])
    return { date: d.date, query: q, proactive: p, task: t, total }
  })

  return {
    range: range || 'all',
    summary: numAcc(summaryNum),
    today: numAcc(daily[daily.length - 1]),
    by_source: { query: numAcc(split(summaryNum, 0.65)), proactive: numAcc(split(summaryNum, 0.05)), task: numAcc(split(summaryNum, 0.3)) },
    by_chat_type: { group: numAcc(split(summaryNum, 0.7)), friend: numAcc(split(summaryNum, 0.25)), panel: numAcc(split(summaryNum, 0.05)) },
    by_status: byStatus,
    top_targets: topTargets.map((t) => ({ chat_type: t.chat_type, target_id: t.target_id, ...numAcc(t) })),
    hourly: hourly.map((h) => ({ date: h.date, query: h.query, proactive: h.proactive, task: h.task, total: h.total })),
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
    name: 'web', description: '联网搜索与网页抓取（内置 CLI，需启用 bash 工具）',
    location: '.builtin/web/SKILL.md', builtin: true, refs: [], extras: ['webctl.py'],
    content: '---\nname: web\ndescription: 联网搜索与网页抓取（内置 CLI，需启用 bash 工具）：查最新资料、核实外部事实、读取网页正文\n---\n\n# 联网搜索与网页抓取\n\n需要最新资讯、外部事实或网页正文时，先读取本技能，再按说明用 bash 执行附带脚本。\n\n## 命令\n\n```bash\npython3 "<webctl.py 的绝对路径>" search --deepseek "查询词" [--count 8] [--content]\npython3 "<webctl.py 的绝对路径>" fetch --deepseek "https://example.com/article" [--offset 8000]\n```\n\n- 需要 DEEPSEEK_API_KEY 环境变量\n- 搜索结果摘要够用就不必抓正文；长文用 --offset 续读\n',
    files: [
      { name: 'webctl.py', kind: 'extra', size: 8214, content: '#!/usr/bin/env python3\n"""AniaBot 内置 web 技能 CLI：搜索与抓取（DeepSeek）"""\n' },
    ],
  },
  {
    name: 'daily-news', description: '按主题抓取 RSS 与搜索结果，生成每日新闻早报',
    location: 'daily-news', refs: ['sources.md'], extras: ['render.py'],
    content: '---\nname: daily-news\ndescription: 按主题抓取 RSS 与搜索结果，生成每日新闻早报\n---\n\n# 每日早报技能\n\n## 步骤\n\n1. 读取 `sources.md` 中的订阅源列表\n2. 使用内置 web 技能搜索补充当日热点\n3. 挑选 5 条，用 `render.py` 渲染为 Markdown 早报\n4. 发送到目标群聊\n\n## 注意\n\n- 每条新闻附原文链接\n- 早报开头写一句轻松的问候语\n',
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

export function buildSkills(disabled = new Set()) {
  return {
    skills: SKILLS.map(({ name, description, location, refs, extras, builtin }) => ({
      name, description, location, refs, extras,
      builtin: !!builtin,
      disabled: !builtin && disabled.has(name),
    })),
    dir: './data/skills',
    whitelist: [],
  }
}
export function buildSkillDetail(name, disabled = new Set()) {
  const sk = SKILLS.find((x) => x.name === name)
  if (!sk) return null
  return {
    name: sk.name, description: sk.description, location: sk.location,
    content: sk.content, files: sk.files,
    builtin: !!sk.builtin, disabled: !sk.builtin && disabled.has(name),
  }
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
