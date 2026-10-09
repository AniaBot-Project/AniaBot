import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

export default withMermaid(defineConfig({
  title: 'AniaBot',
  description: '一个插件驱动型多平台（QQ / 飞书 / Telegram / Discord / 微信）机器人框架 —— Go 语言开发，内置 AI 对话 / MCP / 定时任务',
  lang: 'zh-CN',
  base: '/AniaBot/',
  cleanUrls: true,
  lastUpdated: true,

  head: [
    ['link', { rel: 'icon', type: 'image/webp', href: '/AniaBot/logo.webp' }],
    ['meta', { name: 'theme-color', content: '#e8547f' }],
    ['meta', { name: 'og:title', content: 'AniaBot 文档' }],
    ['meta', { name: 'og:description', content: '插件驱动型多平台（QQ / 飞书 / Telegram / Discord / 微信）机器人框架' }],
  ],
  markdown: {
    lineNumbers: true,
  },

  themeConfig: {
    logo: '/logo.webp',
    siteTitle: 'AniaBot',

    nav: [
      { text: '指南', link: '/guide/introduction', activeMatch: '/guide/' },
      { text: '插件开发', link: '/plugin/overview', activeMatch: '/plugin/' },
      { text: 'API 参考', link: '/api/events', activeMatch: '/api/' },
      { text: '技术原理', link: '/internals/', activeMatch: '/internals/' },
      {
        text: '链接',
        items: [
          { text: '在线体验 Demo', link: '/demo/#/' },
          { text: 'GitHub', link: 'https://github.com/AniaBot-Project/AniaBot' },
          { text: '提交 Issue', link: 'https://github.com/AniaBot-Project/AniaBot/issues' },
          { text: 'Releases', link: 'https://github.com/AniaBot-Project/AniaBot/releases' },
        ],
      },
    ],

    sidebar: {
      '/guide/': [
        {
          text: '开始使用',
          items: [
            { text: '项目介绍', link: '/guide/introduction' },
            { text: '快速开始', link: '/guide/getting-started' },
            { text: '容器部署', link: '/guide/docker' },
            { text: '配置详解', link: '/guide/configuration' },
            { text: 'Web 控制面板', link: '/guide/web-panel' },
          ],
        },
        {
          text: '插件生态',
          items: [
            { text: '内置插件', link: '/guide/builtin-plugins' },
            { text: '插件市场', link: '/guide/plugin-marketplace' },
            { text: '常见问题', link: '/guide/faq' },
          ],
        },
      ],
      '/plugin/': [
        {
          text: '插件开发',
          items: [
            { text: '插件系统概览', link: '/plugin/overview' },
            { text: '第一个插件', link: '/plugin/first-plugin' },
            { text: '从零开发完整插件', link: '/plugin/tutorial' },
            { text: '命令解析', link: '/plugin/commands' },
            { text: '消息构造器', link: '/plugin/message-builder' },
          ],
        },
        {
          text: '进阶能力',
          items: [
            { text: '数据存储', link: '/plugin/storage' },
            { text: '定时任务', link: '/plugin/cron' },
            { text: '面板服务注册', link: '/plugin/panel-services' },
          ],
        },
        {
          text: '实战',
          items: [
            { text: '完整示例', link: '/plugin/examples' },
            { text: '常见模式', link: '/plugin/patterns' },
          ],
        },
      ],
      '/api/': [
        {
          text: 'API 参考',
          items: [
            { text: '事件接口', link: '/api/events' },
            { text: 'Bot 接口', link: '/api/bot' },
            { text: '存储接口', link: '/api/storage' },
          ],
        },
      ],
      '/internals/': [
        {
          text: '技术原理',
          items: [
            { text: '总览', link: '/internals/' },
            { text: '框架核心原理', link: '/internals/framework' },
            { text: 'AI 引擎（一）LLM 与对话循环', link: '/internals/agent-llm' },
            { text: 'AI 引擎（二）上下文与记忆', link: '/internals/agent-context' },
            { text: 'AI 引擎（三）工具与编排', link: '/internals/agent-tools' },
          ],
        },
      ],
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/AniaBot-Project/AniaBot' },
    ],

    footer: {
      message: '基于 MIT 许可证发布',
      copyright: 'Copyright © 2026 jeanhua',
    },

    search: {
      provider: 'local',
      options: {
        translations: {
          button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
          modal: {
            noResultsText: '没有找到相关结果',
            resetButtonTitle: '清除查询条件',
            footer: { selectText: '选择', navigateText: '切换', closeText: '关闭' },
          },
        },
      },
    },

    editLink: {
      pattern: 'https://github.com/AniaBot-Project/AniaBot/edit/main/docs/:path',
      text: '在 GitHub 上编辑此页',
    },

    lastUpdated: {
      text: '最后更新于',
    },

    outline: {
      label: '本页目录',
      level: [2, 3],
    },

    docFooter: {
      prev: '上一页',
      next: '下一页',
    },

    darkModeSwitchLabel: '外观',
    lightModeSwitchTitle: '切换到浅色模式',
    darkModeSwitchTitle: '切换到深色模式',
    sidebarMenuLabel: '菜单',
    returnToTopLabel: '返回顶部',
  },
}))

