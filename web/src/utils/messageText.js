// 消息日志文本解析：把 message.FriendlyText 生成的纯文本还原成聊天气泡可渲染的树。
//
// 记录日志时调用 FriendlyText(false)，所以文本里不会出现真实 URL，媒体只剩
// [图片] / [文件:…] 之类的占位；引用与合并转发分别包在 <reply> / <合并转发消息>
// 标签里，且嵌套内容会再包一层、每层每行多缩进两个空格（见后端
// common/model/message/messageutils.go）。这里按「配对标签 + 缩进」还原层级，
// 不改变后端记录格式。

const SENDER_RE = /^\[nickname:(.*?) id:([^\]]*)\]:\s*/
// 已知的段占位符（与 FriendlyText 的输出一一对应）
const INLINE_RE = /\[(?:at:[^\]]*|at我|QQ表情:[^\]]*|图片(?: [^\]]*)?|文件:[^\]]*|录音(?::[^\]]*)?|视频(?::[^\]]*)?|分享卡片[^\]]*|音乐:[^\]]*|转发消息[^\]]*)\]/g
// 结构标签：引用与合并转发；结束标签按同名标签的嵌套深度配对
const OPEN_RE = /<(reply|合并转发消息)>/g
const TAG_RE = /<\/?(?:reply|合并转发消息)>/g

// 去掉 FriendlyText 为嵌套内容加的一层缩进（每行两个空格）。逐层解析时每次调用
// 只剥一层，深层内容在递归里自然逐层还原。
function unindent(text) {
  return text
    .split('\n')
    .map((line) => (line.startsWith('  ') ? line.slice(2) : line))
    .join('\n')
}

function pushText(blocks, text) {
  // 兜底：不配对的结构标签（旧日志 / 截断文本）不展示成原始标记
  const value = text.replace(TAG_RE, '').replace(/^\n+|\n+$/g, '')
  if (!value.trim()) return
  blocks.push({ type: 'text', text: value })
}

function stripSender(text) {
  const m = SENDER_RE.exec(text)
  if (!m) return { sender: '', senderId: '', text }
  return { sender: m[1], senderId: m[2], text: text.slice(m[0].length) }
}

// 把一个容器（根消息 / 引用正文 / 转发正文）按结构标签切成有序片段：
// {kind:'text', text} | {kind:'reply', body} | {kind:'合并转发消息', body}。
// 只扫本层：body 交给调用方递归解析，因此嵌套多深都能正确配对。
function scanSegments(text) {
  const segments = []
  let pos = 0
  while (pos < text.length) {
    OPEN_RE.lastIndex = pos
    const open = OPEN_RE.exec(text)
    if (!open) break
    const kind = open[1]
    const pairRe = new RegExp(`<${kind}>|</${kind}>`, 'g')
    pairRe.lastIndex = open.index + open[0].length
    let depth = 0
    let end = -1
    let m
    while ((m = pairRe.exec(text)) !== null) {
      if (m[0][1] === '/') {
        if (depth === 0) {
          end = m.index
          break
        }
        depth -= 1
      } else {
        depth += 1
      }
    }
    // 没有配对的结束标签：余下内容按普通文本处理，避免吞掉消息
    if (end < 0) break
    if (open.index > pos) segments.push({ kind: 'text', text: text.slice(pos, open.index) })
    segments.push({ kind, body: text.slice(open.index + open[0].length, end) })
    pos = end + `</${kind}>`.length
  }
  if (pos < text.length) segments.push({ kind: 'text', text: text.slice(pos) })
  return segments
}

// 片段序列 → 展示块：相邻文本合并，引用 / 转发各自递归解析。
function blocksFromSegments(segments) {
  const blocks = []
  let pending = ''
  for (const seg of segments) {
    if (seg.kind === 'text') {
      pending += seg.text
      continue
    }
    pushText(blocks, pending)
    pending = ''
    blocks.push(seg.kind === 'reply' ? parseQuoteBody(seg.body) : parseForwardBody(seg.body))
  }
  pushText(blocks, pending)
  return blocks
}

// 引用正文：去掉一层缩进与开头的发送者前缀，正文里仍可能嵌着合并转发。
function parseQuoteBody(body) {
  const text = unindent(body).replace(/^\n+|\n+$/g, '')
  const parsed = stripSender(text)
  return {
    type: 'quote',
    sender: parsed.sender,
    senderId: parsed.senderId,
    blocks: blocksFromSegments(scanSegments(parsed.text)),
  }
}

// 合并转发正文：每个节点以 [nickname:… id:…]: 开头，节点正文可含换行；
// 节点内再嵌套的转发 / 引用挂在该节点下，保持「谁转发了什么」的层级。
function parseForwardBody(body) {
  const nodes = []
  const blocks = []
  let current = null
  let blank = false
  for (const seg of scanSegments(unindent(body))) {
    if (seg.kind !== 'text') {
      const block = seg.kind === 'reply' ? parseQuoteBody(seg.body) : parseForwardBody(seg.body)
      if (current) current.blocks.push(block)
      else blocks.push(block)
      continue
    }
    for (const rawLine of seg.text.split('\n')) {
      const line = rawLine.replace(/\s+$/, '')
      if (!line.trim()) {
        if (current) blank = true
        continue
      }
      const m = SENDER_RE.exec(line)
      if (m) {
        current = { name: m[1], id: m[2], text: line.slice(m[0].length).trim(), blocks: [] }
        nodes.push(current)
        blank = false
        continue
      }
      if (!current) {
        // 没有发送者前缀的散行：照常展示，但不计入条数
        current = { name: '', id: '', text: line.trim(), blocks: [] }
        nodes.push(current)
        blank = false
        continue
      }
      current.text += (current.text ? (blank ? '\n\n' : '\n') : '') + line.trim()
      blank = false
    }
  }
  // 条数只数本层的转发节点（节点内部的嵌套转发不并入，避免数字虚高）
  const named = nodes.filter((n) => n.name).length
  return { type: 'forward', count: named || nodes.length || 1, nodes, blocks }
}

// 拆分消息正文：普通文字、引用、合并转发（保持原有先后顺序）。
export function splitMessageBlocks(raw) {
  const text = String(raw || '').replace(/\r\n/g, '\n')
  return blocksFromSegments(scanSegments(text))
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
// counts 会在调用间递减（同一份计数被多个正文块共享）。
export function pruneMediaTokens(tokens, counts) {
  const left = counts || {}
  const out = []
  for (const token of tokens) {
    if (token.type === 'image-placeholder' && left.image > 0) {
      left.image -= 1
      continue
    }
    if (token.type === 'file' && left.file > 0) {
      left.file -= 1
      continue
    }
    if (token.type === 'record' && left.record > 0) {
      left.record -= 1
      continue
    }
    out.push(token)
  }
  return out
}

// 会话列表 / 通知里展示的一行预览：只取顶层正文（引用整体略去，合并转发只留占位），
// 去掉结构标签与冗长的段参数；没有正文时再退回展示引用 / 转发里的内容。
export function friendlyPreview(raw, limit = 160) {
  const source = String(raw || '')
  let text = scanSegments(source)
    .map((seg) => {
      if (seg.kind === 'text') return seg.text
      return seg.kind === '合并转发消息' ? ' [合并转发] ' : ' '
    })
    .join('')
  // 只有引用（没有正文）时，展开其中的文字，避免预览为空
  if (!text.trim()) text = source
  text = text
    .replace(TAG_RE, ' ')
    .replace(/\[文件:([^\]\s]+)[^\]]*\]/g, '[文件] $1')
    .replace(/\[图片[^\]]*\]/g, '[图片]')
    .replace(/\[录音[^\]]*\]/g, '[语音]')
    .replace(/\[视频[^\]]*\]/g, '[视频]')
    .replace(/\[QQ表情:([^\]]*)\]/g, '[$1]')
    .replace(/\[at:全体成员\]/g, '@全体成员')
    .replace(/\[at我\]/g, '@我')
    .replace(/\[at:id:([^\]]*)\]/g, (_, id) => '@' + String(id).replace(/^qq:/, ''))
    // 面板读取日志时后端已把 [at:id:…] 还原成成员昵称（[at:小明]）
    .replace(/\[at:([^\]]*)\]/g, '@$1')
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
