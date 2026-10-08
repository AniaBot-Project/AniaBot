// 演示模式 fetch 拦截器
// 仅在 vite demo 模式（--mode demo）下由 main.js 动态装载：
//   window.fetch 被替换为本地路由分发，所有 /api/* 请求由 handlers.js 用
//   真实抓取的种子数据 + 合成动态数据作答；其余请求走原始 fetch。
// 正式构建中 import.meta.env.MODE === 'demo' 为静态 false，
// mock 模块经 tree-shake 完全剔除，不会进入产物。

import { route } from './handlers.js'

const realFetch = window.fetch.bind(window)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const latency = () => 60 + Math.floor(Math.random() * 180) // 模拟 60~240ms 网络延迟

let installed = false

export function installMock() {
  if (installed) return
  installed = true

  window.fetch = async function mockFetch(input, init) {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input)
    if (!url.startsWith('/api/')) return realFetch(input, init)

    const method = ((init && init.method) || (input && input.method) || 'GET').toUpperCase()
    const res = await route(method, url, init)
    if (!res) return realFetch(input, init)

    await sleep(latency())
    const bodyIsString = typeof res.body === 'string'
    return new Response(bodyIsString ? res.body : JSON.stringify(res.body), {
      status: res.status || 200,
      headers: {
        'Content-Type': bodyIsString ? 'application/json; charset=utf-8' : 'application/json; charset=utf-8',
        ...(res.headers || {}),
      },
    })
  }

  console.info('[demo] AniaBot 演示模式已启用')
}
