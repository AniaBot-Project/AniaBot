<template>
  <div class="space-y-4">
    <!-- 操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="text-xs text-slate-500">
        <template v-if="dir">
          Skills 目录：<span class="font-mono text-zinc-700">{{ dir }}</span>
          <span class="ml-2 text-slate-400">上传/删除立即生效，无需重启</span>
        </template>
      </div>
      <div class="flex items-center gap-3">
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
        <button
          class="text-xs btn-accent px-3.5 py-2 rounded-lg font-medium transition-colors shadow-sm disabled:opacity-50"
          :disabled="uploading"
          @click="fileInput?.click()"
        >
          {{ uploading ? '上传中...' : '上传 Skill（zip）' }}
        </button>
        <input ref="fileInput" type="file" accept=".zip" class="hidden" @change="onUpload" />
      </div>
    </div>

    <!-- 白名单提示 -->
    <div v-if="whitelist.length" class="bg-amber-50 border border-amber-200/70 rounded-xl px-4 py-3 text-xs text-amber-700">
      当前启用了 Skills 白名单（{{ whitelist.join('、') }}），不在白名单中的 skill 不会被加载。可在「配置管理」中调整。
    </div>

    <!-- 上传结果提示 -->
    <p v-if="msg" class="text-xs" :class="msgOk ? 'text-emerald-600' : 'text-red-600'">{{ msg }}</p>

    <!-- Skill 列表 -->
    <section class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
      <ul class="divide-y divide-slate-100">
        <li v-if="skills.length === 0" class="py-12 text-sm text-slate-400 text-center list-none">
          暂无已加载的 skill，点击右上角上传 zip 压缩包（需包含 SKILL.md）
        </li>
        <li v-for="s in skills" :key="s.name" class="px-5 py-4 flex items-start gap-4">
          <div class="w-9 h-9 rounded-lg bg-zinc-100 flex items-center justify-center shrink-0 text-zinc-500">
            <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke-width="1.6" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9.813 15.904 9 18.75l-.813-2.846a4.5 4.5 0 0 0-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 0 0 3.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 0 0 3.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 0 0-3.09 3.09ZM18.259 8.715 18 9.75l-.259-1.035a3.375 3.375 0 0 0-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 0 0 2.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 0 0 2.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 0 0-2.456 2.456Z"/></svg>
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-sm font-semibold text-slate-800">{{ s.name }}</span>
              <span class="text-[11px] font-mono text-slate-400">{{ s.location }}</span>
            </div>
            <p v-if="s.description" class="text-xs text-slate-500 mt-0.5 leading-relaxed">{{ s.description }}</p>
            <div v-if="(s.refs?.length || s.extras?.length)" class="flex items-center gap-1.5 mt-2 flex-wrap">
              <span v-for="f in s.refs" :key="'r' + f" class="text-[11px] px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600 font-mono">{{ f }}</span>
              <span v-for="f in s.extras" :key="'e' + f" class="text-[11px] px-2 py-0.5 rounded-full bg-indigo-500/10 text-zinc-500 font-mono border border-zinc-200/60">{{ f }}</span>
            </div>
          </div>
          <div class="flex items-center gap-1 shrink-0">
            <button
              class="text-xs text-zinc-600 hover:text-zinc-900 hover:bg-white/60 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
              @click="onDetail(s)"
            >
              详情
            </button>
            <button
              class="text-xs text-red-500 hover:text-red-600 hover:bg-red-50 px-2.5 py-1.5 rounded-lg font-medium transition-colors"
              @click="onDelete(s)"
            >
              删除
            </button>
          </div>
        </li>
      </ul>
    </section>

    <!-- Skill 详情弹窗 -->
    <div
      v-if="detail"
      class="fixed inset-0 bg-slate-900/45 backdrop-blur-md flex items-center justify-center z-50 p-4"
      v-backdrop-close="() => (detail = null)"
    >
      <div class="bg-white rounded-xl shadow-2xl border border-zinc-200 w-full max-w-4xl max-h-[85vh] flex flex-col">
        <div class="flex items-start gap-3 px-5 py-3.5 border-b border-zinc-100 shrink-0">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2 flex-wrap">
              <h2 class="text-sm font-semibold text-slate-800">{{ detail.name }}</h2>
              <span class="text-[11px] font-mono text-slate-400">{{ detail.location }}</span>
            </div>
            <p v-if="detail.description" class="text-xs text-slate-500 mt-1 leading-relaxed">{{ detail.description }}</p>
          </div>
          <button
            class="w-7 h-7 flex items-center justify-center rounded-md text-zinc-400 hover:text-zinc-800 hover:bg-white/60 transition-colors"
            title="关闭"
            @click="detail = null"
          >
            ✕
          </button>
        </div>

        <div class="px-5 py-4 overflow-y-auto space-y-4">
          <section class="space-y-2">
            <div class="flex items-center justify-between gap-3">
              <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium">{{ currentFileName }}</h3>
              <div class="flex items-center gap-2 shrink-0">
                <span v-if="detailLoading" class="text-[11px] text-slate-400">加载中...</span>
                <button
                  v-if="isMarkdownFile && currentContent"
                  class="text-[11px] px-2 py-1 rounded-md border border-white/60 text-zinc-600 hover:bg-white/60 hover:text-zinc-900 font-medium transition-colors"
                  @click="showSource = !showSource"
                >
                  {{ showSource ? '渲染视图' : '查看源码' }}
                </button>
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
                :class="selectedFile === 'SKILL.md' ? 'bg-indigo-500/10' : 'bg-white hover:bg-white/55'"
                @click="selectFile('SKILL.md')"
              >
                SKILL.md
              </li>
              <li
                v-for="f in detail.files"
                :key="f.name"
                class="px-3 py-2 flex items-center gap-2 text-[11px] cursor-pointer transition-colors"
                :class="selectedFile === f.name ? 'bg-indigo-500/10' : 'bg-white hover:bg-white/55'"
                @click="selectFile(f.name)"
              >
                <span class="font-mono text-zinc-700 truncate">{{ f.name }}</span>
                <span class="ml-auto shrink-0 px-1.5 py-0.5 rounded-full bg-zinc-100 text-zinc-500">{{ fileKindLabel(f) }}</span>
                <span v-if="f.size" class="shrink-0 text-slate-400 font-mono">{{ fmtSize(f.size) }}</span>
              </li>
            </ul>
          </section>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
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

async function onDelete(s) {
  if (!confirm(`确定要删除 skill「${s.name}」吗？将同时删除其目录下的所有文件。`)) return
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
