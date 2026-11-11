// 消息日志文本解析：把 message.FriendlyText 生成的纯文本还原成聊天气泡可渲染的结构。
//
// 记录日志时调用 FriendlyText(false)，所以文本里不会出现真实 URL，媒体只剩
// [图片] / [文件:…] 之类的占位；引用与合并转发分别包在 <reply> / <合并转发消息>
// 标签里。这里只做展示层解析，不改变后端记录格式。

const SENDER_RE = /^\[nickname:(.*?) id:([^\]]*)\]:\s*/
const BLOCK_RE = /<reply>\r?\n?([\s\S]*?)\r?\n?<\/reply>|<合并转发消息>\r?\n?([\s\S]*?)\r?\n?<\/合并转发消息>/g
// 已知的段占位符（与 FriendlyText 的输出一一对应）
const INLINE_RE = /\[(?:at:[^\]]*|at我|QQ表情:[^\]]*|图片(?: [^\]]*)?|文件:[^\]]*|录音(?::[^\]]*)?|视频(?::[^\]]*)?|分享卡片[^\]]*|音乐:[^\]]*|转发消息[^\]]*)\]/g

// 去掉 FriendlyText 为嵌套内容加的一层缩进（每行两个空格）。
function unindent(text) {
  return text
    .split('\n')
    .map((line) => (line.startsWith('  ') ? line.slice(2) : line))
    .join('\n')
}

// 嵌套内容里可能还包着引用 / 合并转发：展示为纯文本时去掉标签外壳。
function humanizeBody(text) {
  return text
    .replace(/<\/?(?:reply|合并转发消息)>/g, '')
    .replace(SENDER_RE, '$1：')
    .trim()
}

function pushText(blocks, text) {
  const value = text.replace(/^\n+|\n+$/g, '')
  if (!value.trim()) return
  blocks.push({ type: 'text', text: value })
}

function stripSender(text) {
  const m = SENDER_RE.exec(text)
  if (!m) return { sender: '', senderId: '', text }
  return { sender: m[1], senderId: m[2], text: text.slice(m[0].length) }
}

// 拆分消息正文：引用块、合并转发块与普通文本块（保持原有先后顺序）。
export function splitMessageBlocks(raw) {
  const text = String(raw || '').replace(/\r\n/g, '\n')
  const blocks = []
  const re = new RegExp(BLOCK_RE.source, 'g')
  let last = 0
  let m
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) pushText(blocks, text.slice(last, m.index))
    if (m[1] !== undefined) {
      const body = unindent(m[1]).trim()
      const parsed = stripSender(body)
      blocks.push({
        type: 'quote',
        sender: parsed.sender,
        senderId: parsed.senderId,
        text: humanizeBody(parsed.text),
      })
    } else {
      const body = unindent(m[2])
      blocks.push({ type: 'forward', text: body, count: countForwardNodes(body) })
    }
    last = m.index + m[0].length
  }
  if (last < text.length) pushText(blocks, text.slice(last))
  if (blocks.length === 0 && text.trim()) blocks.push({ type: 'text', text: text.trim() })
  return blocks
}

function countForwardNodes(body) {
  const m = body.match(/^\s*\[nickname:/gm)
  return m ? m.length : 1
}

// 合并转发正文按行拆成节点：保留缩进层级，并把 [nickname:x id:y]: 前缀还原成昵称。
export function parseForwardNodes(text) {
  const nodes = []
  for (const rawLine of String(text || '').split('\n')) {
    if (!rawLine.trim()) continue
    const indent = rawLine.length - rawLine.trimStart().length
    const line = rawLine.trim()
    if (/^<\/?(?:reply|合并转发消息)>$/.test(line)) {
      nodes.push({ type: 'tag', indent, text: line.includes('reply') ? '引用内容' : '合并转发' })
      continue
    }
    const m = SENDER_RE.exec(line)
    if (m) {
      nodes.push({ type: 'node', indent, name: m[1], id: m[2], text: line.slice(m[0].length) })
      continue
    }
    nodes.push({ type: 'line', indent, text: line })
  }
  return nodes
}

// 把一个文本块拆成「普通文字 + 结构化的占位符 token」。
export function parseInline(text) {
  const tokens = []
  const re = new RegExp(INLINE_RE.source, 'g')
  let last = 0
  let m
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) tokens.push({ type: 'text', text: text.slice(last, m.index) })
    tokens.push(classifyToken(m[0]))
    last = m.index + m[0].length
  }
  if (last < text.length) tokens.push({ type: 'text', text: text.slice(last) })
  return tokens
}

function classifyToken(raw) {
  const inner = raw.slice(1, -1)
  if (inner === 'at我') return { type: 'at', text: '@我' }
  if (inner.startsWith('at:')) {
    const value = inner.slice(3)
    if (value === '全体成员') return { type: 'at', text: '@全体成员' }
    if (value.startsWith('id:')) return { type: 'at', text: '@' + shortId(value.slice(3)) }
    return { type: 'at', text: '@' + value }
  }
  if (inner.startsWith('QQ表情:')) return { type: 'face', text: inner.slice(5) || '表情' }
  if (inner === '图片' || inner.startsWith('图片 ')) return { type: 'image-placeholder', text: '图片' }
  if (inner.startsWith('文件:')) {
    const rest = inner.slice(3)
    const name = rest.split(' fileId:')[0].split(' url:')[0] || '文件'
    return { type: 'file', name, text: name }
  }
  if (inner === '录音' || inner.startsWith('录音:')) return { type: 'record', text: '语音' }
  if (inner === '视频' || inner.startsWith('视频:')) return { type: 'record', text: '视频' }
  if (inner.startsWith('分享卡片')) {
    const m = /^分享卡片,标题:\s*(.*?),描述:\s*(.*?)(?:,链接:\s*\((.*)\))?$/.exec(inner)
    if (m) return { type: 'card', title: m[1] || '分享卡片', desc: m[2] || '', url: m[3] || '' }
    return { type: 'card', title: '分享卡片', desc: '', url: '' }
  }
  if (inner.startsWith('音乐:')) return { type: 'card', title: inner.slice(3), desc: '音乐分享', url: '' }
  if (inner.startsWith('转发消息')) return { type: 'text', text: '[合并转发消息]' }
  return { type: 'text', text: raw }
}

// 每个媒体只吃掉文本里对应的一个占位符，避免同一张图既显示占位又显示缩略图。
export function pruneMediaTokens(tokens, counts) {
  const left = { image: 0, file: 0, record: 0, ...counts }
  const out = []
  for (const token of tokens) {
    if (token.type === 'image-placeholder' && left.image > 0) {
      left.image--
      continue
    }
    if (token.type === 'file' && left.file > 0) {
      left.file--
      continue
    }
    if (token.type === 'record' && left.record > 0) {
      left.record--
      continue
    }
    out.push(token)
  }
  return out
}

// 会话列表 / 通知里展示的一行预览：优先展示回复正文，去掉结构标签与冗长的段参数；
// 只有引用/转发的消息（没有正文）再退回展示其中的内容。
export function friendlyPreview(raw, limit = 160) {
  const source = String(raw || '')
  let text = source
    .replace(/<reply>[\s\S]*?<\/reply>/g, ' ')
    .replace(/<合并转发消息>[\s\S]*?<\/合并转发消息>/g, ' [合并转发] ')
  if (!text.trim()) {
    text = source.replace(/<\/?(?:reply|合并转发消息)>/g, ' ')
  }
  text = text
    .replace(/\[文件:([^\]\s]+)[^\]]*\]/g, '[文件] $1')
    .replace(/\[图片[^\]]*\]/g, '[图片]')
    .replace(/\[录音[^\]]*\]/g, '[语音]')
    .replace(/\[视频[^\]]*\]/g, '[视频]')
    .replace(/\[QQ表情:([^\]]*)\]/g, '[$1]')
    .replace(/\[at:全体成员\]/g, '@全体成员')
    .replace(/\[at我\]/g, '@我')
    .replace(/\[at:id:([^\]]*)\]/g, (_, id) => '@' + String(id).replace(/^qq:/, ''))
    .replace(/\[分享卡片[^\]]*\]/g, '[分享卡片]')
    .replace(/\[音乐:([^\]]*)\]/g, '[音乐] $1')
    .replace(/\[nickname:(.*?) id:([^\]]*)\]:\s*/g, '$1：')
    .replace(/\s+/g, ' ')
    .trim()
  if (limit > 0 && [...text].length > limit) text = [...text].slice(0, limit).join('') + '…'
  return text
}

// 展示用的短 ID：QQ 的 qq: 前缀对用户没有意义，其余平台前缀保留（用于区分来源）。
export function shortId(id) {
  return String(id || '').replace(/^qq:/, '')
}
