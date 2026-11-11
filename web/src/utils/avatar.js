// 会话 / 用户头像的纯前端生成：没有真实头像可用时，用稳定的渐变 + 首字作为
// 占位（与聊天软件里常见的字母头像一致），同一用户每次渲染颜色不变。

const PALETTES = [
  ['#f7c866', '#e8a23c'],
  ['#7ed6c7', '#2fb3a3'],
  ['#a5c4f0', '#7295d8'],
  ['#f5aca5', '#e07d75'],
  ['#c4d98a', '#8dbb53'],
  ['#c9b6ea', '#a487d6'],
  ['#f2b8d4', '#dd86b1'],
  ['#a8dce6', '#64b8cc'],
]

function hash(text) {
  let h = 0
  const s = String(text || '?')
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0
  return h
}

// 由任意稳定键取一组头像渐变色。
export function avatarGradient(key) {
  const [from, to] = PALETTES[hash(key) % PALETTES.length]
  return { background: `linear-gradient(145deg, ${from}, ${to})` }
}

// 首字：中文取第一个字，拉丁字母取前两个字母并大写。
export function avatarInitials(name) {
  const chars = [...String(name || '?').trim()]
  if (!chars.length) return '?'
  if (/[\u3400-\u9fff]/.test(chars[0])) return chars[0]
  return chars.slice(0, 2).join('').toUpperCase()
}

// 群 / 私聊会话统一用同一种头像键：群用群 ID，私聊用用户 ID。
export function sessionAvatarKey(session) {
  if (!session) return '?'
  if (session.kind === 'group') return 'g:' + (session.group_id || session.key)
  if (session.kind === 'friend') return 'f:' + (session.user_id || session.key)
  return session.key || 'system'
}
