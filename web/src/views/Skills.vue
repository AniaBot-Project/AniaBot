<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        <template v-if="dir">
          Skills 目录：<span class="font-mono text-zinc-700">{{ dir }}</span>
          <span class="ml-2 text-slate-400">上传/删除/启停立即生效，无需重启</span>
        </template>
      </div>
      <div class="flex items-center gap-3">
        <Button type="text" size="small" @click="load">刷新</Button>
        <Button
          type="primary"
          size="small"
          :disabled="uploading"
          @click="fileInput?.click()"
        >
          {{ uploading ? '上传中...' : '上传 Skill（zip）' }}
        </Button>
        <input ref="fileInput" type="file" accept=".zip" class="hidden" @change="onUpload" />
      </div>
    </div>

    <!-- 白名单提示 -->
    <Card v-if="whitelist.length" color="app-yellow" style="padding: 12px 20px">
      <span class="text-xs">当前启用了 Skills 白名单（{{ whitelist.join('、') }}），不在白名单中的外置 skill 不会被加载；内置 skill 始终加载。可在「配置管理」中调整。</span>
    </Card>

    <!-- 上传结果提示 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-[#34c759]' : 'text-[#ff3b30]'">{{ msg }}</p>

    <!-- Skill 列表 -->
    <Card style="padding: 0" class="overflow-hidden">
      <ul class="divide-y divide-slate-100">
        <li v-if="skills.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
          暂无已加载的 skill，点击右上角上传 zip 压缩包（需包含 SKILL.md）
        </li>
        <li v-for="s in skills" :key="s.name" class="px-5 py-4 flex items-start gap-4 transition-opacity" :class="s.disabled ? 'opacity-60' : ''">
          <div class="w-9 h-9 rounded-lg bg-zinc-100 flex items-center justify-center shrink-0 text-zinc-500">
            <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke-width="1.6" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9.813 15.904 9 18.75l-.813-2.846a4.5 4.5 0 0 0-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 0 0 3.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 0 0 3.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 0 0-3.09 3.09ZM18.259 8.715 18 9.75l-.259-1.035a3.375 3.375 0 0 0-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 0 0 2.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 0 0 2.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 0 0-2.456 2.456Z"/></svg>
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-sm font-semibold text-slate-800">{{ s.name }}</span>
              <Tooltip v-if="s.builtin" title="内置技能：不可删除、不可禁用" placement="bottom">
                <Tag variant="solid" size="small" color="app-blue">内置</Tag>
              </Tooltip>
              <Tooltip v-else-if="s.disabled" title="已禁用：描述不进入上下文" placement="bottom">
                <Tag variant="solid" size="small">已禁用</Tag>
              </Tooltip>
              <span class="text-[11px] font-mono text-slate-400">{{ s.location }}</span>
            </div>
            <p v-if="s.description" class="text-xs text-slate-500 mt-0.5 leading-relaxed">{{ s.description }}</p>
            <div v-if="(s.refs?.length || s.extras?.length)" class="flex items-center gap-1.5 mt-2 flex-wrap">
              <Tag v-for="f in s.refs" :key="'r' + f" variant="soft" size="small" class="font-mono">{{ f }}</Tag>
              <Tag v-for="f in s.extras" :key="'e' + f" variant="outlined" size="small" class="font-mono">{{ f }}</Tag>
            </div>
          </div>
          <div class="flex items-center gap-1 shrink-0">
            <Button type="text" size="small" @click="onDetail(s)">详情</Button>
            <Button v-if="!s.builtin" type="text" size="small" @click="onToggle(s)">
              {{ s.disabled ? '启用' : '禁用' }}
            </Button>
            <Button v-if="!s.builtin" type="text" size="small" danger @click="onDelete(s)">删除</Button>
          </div>
        </li>
      </ul>
    </Card>

    <!-- Skill 详情弹窗 -->
    <Modal
      :open="!!detail"
      :typewriter="false"
      :show-footer="false"
      variant="default"
      :width="896"
      @close="detail = null"
    >
      <!-- 弹窗头部 -->
      <template #title>
        <div v-if="detail" class="w-full">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="text-sm font-semibold text-slate-800">{{ detail.name }}</span>
            <span class="text-[11px] font-mono text-slate-400">{{ detail.location }}</span>
            <Tooltip class="ml-auto" title="关闭" placement="bottom">
              <Button type="text" size="small" @click="detail = null">✕</Button>
            </Tooltip>
          </div>
          <p v-if="detail.description" class="text-xs text-slate-500 mt-1 leading-relaxed font-normal">{{ detail.description }}</p>
        </div>
      </template>

      <!-- 弹窗内容（可滚动） -->
      <div v-if="detail" class="w-full space-y-4 font-normal">
        <section class="space-y-2">
          <div class="flex items-center justify-between gap-3">
            <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium">{{ currentFileName }}</h3>
            <div class="flex items-center gap-2 shrink-0">
              <span v-if="detailLoading" class="text-[11px] text-slate-400">加载中...</span>
              <Button
                v-if="isMarkdownFile && currentContent"
                type="default"
                size="small"
                @click="showSource = !showSource"
              >
                {{ showSource ? '渲染视图' : '查看源码' }}
              </Button>
            </div>
          </div>
          <div
            v-if="isMarkdownFile && !showSource"
            class="markdown-body bg-white border border-white/60 rounded-lg px-4 py-3"
            v-html="renderedContent"
          />
          <pre v-else class="text-xs text-slate-700 font-mono whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ currentContent }}</pre>
        </section>

        <section v-if="detail.files?.length" class="space-y-2">
          <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium">文件</h3>
          <ul class="divide-y divide-slate-100 border border-white/60 rounded-lg overflow-hidden">
            <li
              class="px-3 py-2 text-[11px] font-mono text-zinc-600 cursor-pointer transition-colors"
              :class="selectedFile === 'SKILL.md' ? 'bg-[#0071e3]/10' : 'bg-white hover:bg-white/55'"
              @click="selectFile('SKILL.md')"
            >
              SKILL.md
            </li>
            <li
              v-for="f in detail.files"
              :key="f.name"
              class="px-3 py-2 flex items-center gap-2 text-[11px] cursor-pointer transition-colors"
              :class="selectedFile === f.name ? 'bg-[#0071e3]/10' : 'bg-white hover:bg-white/55'"
              @click="selectFile(f.name)"
            >
              <span class="font-mono text-zinc-700 truncate">{{ f.name }}</span>
              <Tag variant="soft" size="small" class="ml-auto shrink-0">{{ fileKindLabel(f) }}</Tag>
              <span v-if="f.size" class="shrink-0 text-slate-400 font-mono">{{ fmtSize(f.size) }}</span>
            </li>
          </ul>
        </section>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import { confirmAction } from '../ui/confirm'
import { computed, onMounted, ref } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Button, Card, Modal, Tag, Tooltip } from 'animal-island-vue'
import { api } from '../api.js'

const skills = ref([])
const dir = ref('')
const whitelist = ref([])
const fileInput = ref(null)
const uploading = ref(false)
const msg = ref('')
const msgOk = ref(false)
const detail = ref(null)
const detailLoading = ref(false)
const selectedFile = ref('SKILL.md')
const showSource = ref(false)

const currentFileName = computed(() => selectedFile.value || 'SKILL.md')

// 是否为 Markdown 文件（SKILL.md 与附属 md 文档默认渲染展示，可切换源码）
const isMarkdownFile = computed(() => /\.(md|markdown)$/i.test(currentFileName.value))

// 当前选中的文件（SKILL.md 或附属文件）
const currentFile = computed(() => {
  if (!detail.value) return null
  if (selectedFile.value === 'SKILL.md') {
    return { name: 'SKILL.md', kind: 'reference', content: detail.value.content || '' }
  }
  return detail.value.files?.find((f) => f.name === selectedFile.value) || null
})

const currentContent = computed(() => {
  const f = currentFile.value
  if (!f) return ''
  if (f.content) return f.content
  return f.kind === 'reference' ? '' : '该文件为二进制/非文本文件，无法在面板中预览'
})

// 渲染 Markdown：去掉 YAML frontmatter 后转为 HTML，再经 DOMPurify 清洗
const renderedContent = computed(() => {
  const md = currentContent.value
  if (!md) return ''
  return DOMPurify.sanitize(marked.parse(stripFrontmatter(md), { gfm: true, breaks: true }))
})

function stripFrontmatter(md) {
  const m = md.match(/^---\r?\n([\s\S]*?)\r?\n---[ \t]*\r?\n/)
  return m ? md.slice(m[0].length) : md
}

function selectFile(name) {
  selectedFile.value = name
  showSource.value = false
}

async function load() {
  try {
    const data = await api.getSkills()
    skills.value = data.skills || []
    dir.value = data.dir || ''
    whitelist.value = data.whitelist || []
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

async function onUpload(e) {
  const file = e.target.files?.[0]
  e.target.value = ''
  if (!file) return
  if (!file.name.toLowerCase().endsWith('.zip')) {
    msgOk.value = false
    msg.value = '请上传 zip 格式的压缩包'
    return
  }
  uploading.value = true
  msg.value = ''
  try {
    await api.uploadSkill(file)
    msgOk.value = true
    msg.value = `「${file.name}」上传成功`
    await load()
  } catch (err) {
    msgOk.value = false
    msg.value = err.message
  } finally {
    uploading.value = false
  }
}

async function onToggle(s) {
  const enabled = !!s.disabled
  msg.value = ''
  try {
    await api.setSkillEnabled(s.name, enabled)
    msgOk.value = true
    msg.value = `「${s.name}」已${enabled ? '启用' : '禁用'}`
    await load()
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

async function onDelete(s) {
  if (!await confirmAction(`确定要删除 skill「${s.name}」吗？将同时删除其目录下的所有文件。`)) return
  msg.value = ''
  try {
    await api.deleteSkill(s.name)
    msgOk.value = true
    msg.value = `「${s.name}」已删除`
    await load()
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
  }
}

async function onDetail(s) {
  msg.value = ''
  selectedFile.value = 'SKILL.md'
  showSource.value = false
  detail.value = {
    name: s.name,
    description: s.description,
    location: s.location,
    content: '加载中...',
    files: [],
  }
  detailLoading.value = true
  try {
    const data = await api.getSkillDetail(s.name)
    detail.value = data
  } catch (e) {
    msgOk.value = false
    msg.value = e.message
    detail.value = null
  } finally {
    detailLoading.value = false
  }
}

function fileKindLabel(f) {
  if (f.kind === 'reference') return '文档'
  return f.content ? '文本' : '附带'
}

function fmtSize(bytes) {
  if (!bytes && bytes !== 0) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

onMounted(load)
</script>

<style>
/* Markdown 渲染视图（技能详情弹窗内），与面板等宽字体风格保持一致 */
.markdown-body {
  font-size: 13px;
  line-height: 1.75;
  color: #334155;
  word-break: break-word;
}
.markdown-body > :first-child {
  margin-top: 0;
}
.markdown-body > :last-child {
  margin-bottom: 0;
}
.markdown-body h1,
.markdown-body h2,
.markdown-body h3,
.markdown-body h4 {
  font-weight: 600;
  color: #1e293b;
  line-height: 1.4;
  margin: 1.25em 0 0.5em;
}
.markdown-body h1 { font-size: 1.25rem; }
.markdown-body h2 { font-size: 1.125rem; }
.markdown-body h3 { font-size: 1rem; }
.markdown-body h4 { font-size: 0.9rem; }
.markdown-body p {
  margin: 0.5em 0;
}
.markdown-body ul,
.markdown-body ol {
  margin: 0.5em 0;
  padding-left: 1.5em;
}
.markdown-body ul { list-style: disc; }
.markdown-body ol { list-style: decimal; }
.markdown-body li {
  margin: 0.25em 0;
}
.markdown-body a {
  color: #2563eb;
  text-decoration: underline;
}
.markdown-body code {
  font-family: var(--font-mono);
  font-size: 0.85em;
  background: #f1f5f9;
  border: 1px solid #e2e8f0;
  border-radius: 4px;
  padding: 0.1em 0.35em;
  color: #be185d;
}
.markdown-body pre {
  background: #0f172a;
  color: #e2e8f0;
  border-radius: 8px;
  padding: 12px;
  overflow-x: auto;
  margin: 0.75em 0;
}
.markdown-body pre code {
  background: transparent;
  border: 0;
  padding: 0;
  color: inherit;
  font-size: 12px;
}
.markdown-body blockquote {
  border-left: 3px solid #cbd5e1;
  padding-left: 12px;
  color: #64748b;
  margin: 0.75em 0;
}
.markdown-body table {
  border-collapse: collapse;
  width: 100%;
  margin: 0.75em 0;
  font-size: 12px;
}
.markdown-body th,
.markdown-body td {
  border: 1px solid #e2e8f0;
  padding: 6px 10px;
  text-align: left;
}
.markdown-body th {
  background: #f8fafc;
  font-weight: 600;
}
.markdown-body hr {
  border: 0;
  border-top: 1px solid #e2e8f0;
  margin: 1em 0;
}
.markdown-body img {
  max-width: 100%;
  border-radius: 6px;
}
.markdown-body strong {
  font-weight: 600;
}
</style>
