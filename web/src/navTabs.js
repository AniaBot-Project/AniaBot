// 合并页的页签定义：侧边栏一个入口，页内用 PageTabs 切换。
// 同一组的两个页面共用这一份定义，避免两边各写一份导致文案/顺序不一致。
// 路由路径保持不变（旧链接、书签与 ?task= / ?task_id= 深链继续有效）。

// 消息与操作日志（/#/msglogs | /#/oplogs）
export const LOG_TABS = [
  {
    to: '/msglogs',
    label: '消息日志',
    hint: '机器人收发的全部消息：按群聊 / 私聊 / 系统通知分会话，含 Bot 自己发出的消息',
  },
  {
    to: '/oplogs',
    label: '操作日志',
    hint: '面板与 AI 的管理动作审计：改配置、重启、装插件、审批等',
  },
]

// AI 运行记录（/#/querylogs | /#/agent-tasks）
export const AI_RUN_TABS = [
  {
    to: '/querylogs',
    label: '对话记录',
    hint: '每次 AI 回复的完整过程：触发、工具调用、token 消耗与最终输出',
  },
  {
    to: '/agent-tasks',
    label: '后台任务',
    hint: '子代理 / 交接 / 工作流 / 团队运行等后台工作的执行记录',
  },
]

// Token 用量（/#/tokenstats | /#/quota）
export const USAGE_TABS = [
  {
    to: '/tokenstats',
    label: '用量统计',
    hint: '按来源与会话统计 Token 消耗、趋势与缓存命中',
  },
  {
    to: '/quota',
    label: '配额限制',
    hint: '每日会话 / 全局 Token 上限与当日用量，可单独清零',
  },
]

// 记忆与知识库（/#/memory | /#/knowledge）
export const MEMORY_TABS = [
  {
    to: '/memory',
    label: '长期记忆',
    hint: 'AI 在对话中自行记录的长期记忆，可查看、修正与批量删除',
  },
  {
    to: '/knowledge',
    label: '知识库',
    hint: '按全局 / 群聊 / 私聊作用域维护的文档，对话时按需检索',
  },
]

// 配置管理（/#/config | /#/files）
export const CONFIG_TABS = [
  {
    to: '/config',
    label: '参数配置',
    hint: '按分组编辑全部配置项，保存后重启生效',
  },
  {
    to: '/files',
    label: '扩展配置',
    hint: '四份 JSON 配置：MCP 服务器 / Prompt 覆盖 / AI 钩子 / 自定义命令',
  },
]
