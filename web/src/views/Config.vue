<template>
  <div class="flex flex-col lg:flex-row lg:items-start gap-6">
    <!-- 分组导航：移动端隐藏，改用上方下拉选择 -->
    <aside v-if="!rawMode" class="hidden lg:block w-52 shrink-0 sticky top-24 space-y-5 max-h-[calc(100vh-8rem)] overflow-y-auto pr-1">
      <div class="relative">
        <span class="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 [&>svg]:w-4 [&>svg]:h-4" v-html="iconSearch" />
        <input
          v-model="search"
          type="text"
          placeholder="搜索配置项..."
          class="w-full bg-white border border-white/60 rounded-lg pl-9 pr-3 py-2 text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400 transition-shadow"
        />
      </div>

      <div v-for="cat in cards" :key="cat.name">
        <button
          class="w-full flex items-center justify-between px-3 mb-1.5 text-[11px] font-semibold uppercase tracking-wider transition-colors"
          :class="activeCategory === cat.name ? 'text-zinc-900' : 'text-slate-400 hover:text-zinc-600'"
          @click="selectCategory(cat.name)"
        >
          <span>{{ cat.name }}</span>
          <span class="text-[10px] font-normal normal-case tracking-normal">{{ cat.total }} 项</span>
        </button>
        <nav class="space-y-0.5">
          <template v-for="node in cat.nodes" :key="node.name">
            <!-- 插件卡片（有子分组）：插件名小标题 + 缩进的子分组 -->
            <template v-if="hasSubs(node)">
              <button
                class="w-full flex items-center justify-between px-3 pt-1.5 text-xs font-medium text-slate-500 hover:text-zinc-700 transition-colors"
                @click="jumpTo(node.sections[0].name)"
              >
                <span class="truncate">{{ node.name }}</span>
                <span class="text-[10px] text-slate-400 ml-2 shrink-0">{{ cardCount(node) }}</span>
              </button>
              <button
                v-for="s in node.sections"
                :key="s.name"
                class="w-full flex items-center justify-between pl-6 pr-3 py-1.5 rounded-lg text-[13px] transition-colors"
                :class="activeGroup === s.name
                  ? 'bg-zinc-100 text-zinc-900 font-medium'
                  : 'text-slate-600 hover:bg-white/70'"
                @click="jumpTo(s.name)"
              >
                <span class="truncate">{{ s.label }}</span>
                <span class="text-[11px] text-slate-400 ml-2 shrink-0">{{ s.fields.length }}</span>
              </button>
            </template>
            <!-- 无子分组的平铺分组 -->
            <button
              v-else
              class="w-full flex items-center justify-between px-3 py-1.5 rounded-lg text-[13px] transition-colors"
              :class="activeGroup === node.name
                ? 'bg-zinc-100 text-zinc-900 font-medium'
                : 'text-slate-600 hover:bg-white/70'"
              @click="jumpTo(node.name)"
            >
              <span class="truncate">{{ node.name }}</span>
              <span class="text-[11px] text-slate-400 ml-2 shrink-0">{{ cardCount(node) }}</span>
            </button>
          </template>
        </nav>
      </div>
      <p v-if="cards.length === 0" class="px-3 text-xs text-slate-400">没有匹配「{{ search }}」的配置项</p>
    </aside>

    <!-- 配置主体 -->
    <div class="flex-1 min-w-0 space-y-5">
      <!-- 移动端分组选择（桌面端用左侧导航） -->
      <div v-if="!rawMode" class="lg:hidden">
        <select
          class="w-full bg-white border border-white/60 rounded-lg px-3 py-2.5 text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400 transition-shadow"
          :value="activeCategory"
          @change="selectCategory($event.target.value)"
        >
          <option v-for="cat in cards" :key="cat.name" :value="cat.name">{{ cat.name }}（{{ cat.total }} 项）</option>
        </select>
      </div>

      <Transition name="fade">
        <div v-if="saved" class="bg-emerald-50 border border-emerald-200 text-emerald-800 text-sm rounded-xl px-4 py-3 flex items-center gap-2">
          <span class="[&>svg]:w-4 [&>svg]:h-4" v-html="iconCheck" />
          配置已保存到数据库，将在 <b>重启 Bot 后生效</b>。
        </div>
      </Transition>

      <!-- 微信扫码登录卡片：适配器在运行，或已勾选启用（待重启）时显示 -->
      <QrLoginCard :weixin-enabled="form['bot.platform.weixin.enable'] === true" />

      <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div class="min-w-0">
          <p class="text-sm text-slate-500">配置存储在数据库中，修改保存后重启生效。</p>
          <p class="text-xs text-slate-400 mt-0.5">共 {{ schema.length }} 项配置</p>
        </div>
        <div class="flex items-center gap-2 shrink-0">
          <button
            class="px-3 py-1.5 text-sm rounded-lg border border-slate-300/60 text-slate-600 hover:bg-white/55 transition-colors"
            title="导出完整配置为 JSON 文件（含密钥等敏感字段，请妥善保管）"
            @click="onExportConfig"
          >
            导出 JSON
          </button>
          <button class="px-3 py-1.5 text-sm rounded-lg border border-slate-300/60 text-slate-600 hover:bg-white/55 transition-colors" @click="rawMode = !rawMode">
            {{ rawMode ? '表单模式' : '高级模式 (JSON)' }}
          </button>
        </div>
      </div>

      <!-- 配置预设 -->
      <section v-if="!rawMode" class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
        <button
          class="w-full flex items-center justify-between gap-3 px-4 py-4 sm:px-6 text-sm font-semibold text-slate-800 hover:bg-white/55 transition-colors"
          @click="presetsOpen = !presetsOpen"
        >
          <span class="flex items-center gap-2.5">
            <span class="w-7 h-7 rounded-lg flex items-center justify-center text-white text-xs bg-zinc-700 [&>svg]:w-4 [&>svg]:h-4" v-html="iconBookmark" />
            配置预设
            <span class="text-xs font-normal text-slate-400">{{ presets.length }} 个</span>
          </span>
          <span class="flex items-center gap-1.5 text-xs font-normal text-slate-400">
            {{ presetsOpen ? '收起' : '展开' }}
            <svg class="w-4 h-4 transition-transform duration-200" :class="presetsOpen ? 'rotate-180' : ''" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m19.5 8.25-7.5 7.5-7.5-7.5" /></svg>
          </span>
        </button>
        <Transition name="fade">
          <div v-show="presetsOpen" class="p-4 sm:p-6 space-y-4 border-t border-white/50">
            <p class="text-xs text-slate-500">把当前全部配置（含密钥、MCP / Prompt 覆盖）保存为一份快照，之后可一键切换。应用预设后重启生效。</p>

            <div class="flex gap-2">
              <input
                v-model="presetName"
                type="text"
                placeholder="预设名称，如：DeepSeek 日常"
                class="flex-1 border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400 transition-shadow"
                @keyup.enter="onSavePreset()"
              />
              <button
                :disabled="presetSaving || !presetName.trim()"
                class="px-4 py-2 text-sm rounded-lg btn-accent disabled:opacity-40 transition-colors shrink-0"
                @click="onSavePreset()"
              >
                {{ presetSaving ? '保存中...' : '保存当前配置' }}
              </button>
            </div>

            <p v-if="presets.length === 0" class="text-sm text-slate-400">还没有预设。调整好配置后，在上方输入名称即可保存。</p>
            <ul v-else class="divide-y divide-slate-100 border border-white/60 rounded-lg">
              <li v-for="p in presets" :key="p.name" class="flex items-center gap-3 px-4 py-3">
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium text-slate-800 truncate">{{ p.name }}</p>
                  <p class="text-xs text-slate-400 mt-0.5">{{ p.key_count }} 项配置 · 更新于 {{ formatPresetTime(p.updated_at) }}</p>
                </div>
                <button
                  class="px-3 py-1.5 text-xs rounded-lg btn-accent transition-colors shrink-0"
                  @click="onApplyPreset(p)"
                >
                  应用
                </button>
                <button
                  class="px-3 py-1.5 text-xs rounded-lg border border-slate-300/60 text-slate-600 hover:bg-white/55 transition-colors shrink-0"
                  title="用当前配置覆盖该预设"
                  @click="onSavePreset(p.name)"
                >
                  更新
                </button>
                <button
                  class="px-3 py-1.5 text-xs rounded-lg border border-slate-300/60 text-slate-500 hover:text-red-600 hover:border-red-300 transition-colors shrink-0"
                  @click="onDeletePreset(p)"
                >
                  删除
                </button>
              </li>
            </ul>
          </div>
        </Transition>
      </section>

      <!-- 表单模式 -->
      <template v-if="!rawMode">
        <!-- 分类页签 -->
        <div v-if="!searching" class="flex items-center gap-2 flex-wrap">
          <button
            v-for="cat in cards"
            :key="cat.name"
            class="px-4 py-2 rounded-lg text-sm transition-colors"
            :class="activeCategory === cat.name
              ? 'btn-accent font-medium shadow-sm'
              : 'bg-white border border-white/60 text-slate-600 hover:bg-white/55'"
            @click="selectCategory(cat.name)"
          >
            {{ cat.name }}
            <span class="ml-1.5 text-[11px]" :class="activeCategory === cat.name ? 'text-zinc-300' : 'text-slate-400'">{{ cat.total }} 项</span>
          </button>
          <button
            class="ml-auto px-3 py-2 text-xs rounded-lg border border-white/60 text-slate-500 hover:bg-white/55 transition-colors"
            @click="toggleAll"
          >
            {{ allOpen ? '收起全部' : '展开全部' }}
          </button>
        </div>

        <section
          v-for="node in displayCards"
          :key="node.name"
          :id="sectionId(node.name)"
          class="bg-white rounded-xl shadow-sm border border-white/60 scroll-mt-24 overflow-hidden"
        >
          <button
            class="w-full flex items-center justify-between gap-3 px-4 py-4 sm:px-6 text-left hover:bg-white/55 transition-colors"
            @click="toggleGroup(node.name)"
          >
            <span class="flex items-center gap-2.5 min-w-0">
              <span class="w-7 h-7 rounded-lg flex items-center justify-center text-white text-xs shrink-0 [&>svg]:w-4 [&>svg]:h-4" :class="groupColor(node)" v-html="groupIcon(node)" />
              <span class="text-sm font-semibold text-slate-800 truncate">{{ node.name }}</span>
              <span class="text-xs font-normal text-slate-400 shrink-0">{{ cardCount(node) }} 项</span>
            </span>
            <span class="flex items-center gap-3 shrink-0">
              <span v-if="groupChanged(node)" class="text-[11px] text-amber-600">● 有修改</span>
              <svg class="w-4 h-4 text-slate-400 transition-transform duration-200" :class="isOpen(node) ? 'rotate-180' : ''" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m19.5 8.25-7.5 7.5-7.5-7.5" /></svg>
            </span>
          </button>

          <Transition name="fade">
            <div v-show="isOpen(node)" class="p-4 sm:p-6 border-t border-white/50" :class="{ 'space-y-5': hasSubs(node) }">
              <!-- 子分组分节：线框归类；平铺分组只有一个无名分节，直接铺字段 -->
              <div
                v-for="s in node.sections"
                :key="s.name"
                :id="s.name !== node.name ? sectionId(s.name) : undefined"
                :class="hasSubs(node) ? 'rounded-lg border border-white/60 p-5 scroll-mt-24' : ''"
              >
                <p v-if="s.label" class="flex items-center gap-2 text-xs font-semibold text-slate-500 mb-4">
                  <span class="w-1 h-3.5 rounded-full bg-zinc-400" />
                  {{ s.label }}
                  <span class="text-[10px] font-normal text-slate-400">{{ s.fields.length }} 项</span>
                </p>
                <div class="grid grid-cols-1 lg:grid-cols-2 gap-x-8 gap-y-5">
                  <div v-for="field in s.fields" :key="field.key" :class="{ 'lg:col-span-2': ['text', 'strings', 'ints', 'multiselect'].includes(field.type) }">
                <label class="block text-xs font-medium text-slate-700 mb-1.5">
                  {{ field.label }}
                  <span class="text-slate-400 font-normal ml-1">{{ field.key }}</span>
                </label>

                <input v-if="field.type === 'string'" v-model="form[field.key]" type="text" :placeholder="placeholderOf(field)" :class="inputClass" />
                <div v-else-if="field.type === 'password'" class="flex gap-2">
                  <input v-model="form[field.key]" type="password" :placeholder="placeholderOf(field)" :class="inputClass" @input="cleared[field.key] = false" />
                  <button
                    v-if="valueOf(field.key) === MASK"
                    type="button"
                    class="shrink-0 px-3 py-2 text-xs rounded-lg border border-slate-300/60 text-slate-600 hover:bg-white/55 transition-colors"
                    @click="clearField(field)"
                  >清除</button>
                </div>
                <input v-else-if="field.type === 'int'" v-model="form[field.key]" type="number" step="1" :placeholder="placeholderOf(field)" :class="inputClass" />
                <input v-else-if="field.type === 'float'" v-model="form[field.key]" type="number" step="any" :placeholder="placeholderOf(field)" :class="inputClass" />
                <select v-else-if="field.type === 'select'" v-model="form[field.key]" :class="inputClass">
                  <option v-if="!hasDefault(field)" value="">（留空）</option>
                  <option v-for="opt in field.options || []" :key="opt" :value="opt">{{ opt }}</option>
                </select>
                <div v-else-if="field.type === 'multiselect'" class="space-y-2">
                  <div class="flex flex-wrap gap-1.5">
                    <button
                      v-for="opt in field.options || []"
                      :key="opt"
                      type="button"
                      class="opt-chip"
                      :class="{ 'opt-chip-on': isSelected(field, opt) }"
                      :aria-pressed="isSelected(field, opt)"
                      @click="toggleOption(field, opt)"
                    >
                      <span class="opt-box" />
                      {{ opt }}
                    </button>
                  </div>
                  <div class="flex items-center gap-2.5 text-[11px] text-slate-400">
                    <span>已选 {{ selectedCount(field) }}/{{ (field.options || []).length }}</span>
                    <span class="h-3 w-px bg-slate-300/70" />
                    <button type="button" class="hover:text-indigo-600 transition-colors" @click="selectAllOptions(field)">全选</button>
                    <button type="button" class="hover:text-indigo-600 transition-colors" @click="clearOptions(field)">清空</button>
                  </div>
                </div>

                <label v-else-if="field.type === 'bool'" class="inline-flex items-center gap-2.5 cursor-pointer select-none py-1" @click.prevent="form[field.key] = !form[field.key]">
                  <span
                    class="relative inline-flex w-9 h-5 rounded-full transition-colors duration-200"
                    :class="form[field.key] ? 'bg-indigo-500' : 'bg-slate-300/70'"
                  >
                    <span
                      class="absolute top-0.5 w-4 h-4 rounded-full bg-white shadow transition-all duration-200"
                      :class="form[field.key] ? 'left-4.5' : 'left-0.5'"
                    />
                  </span>
                  <span class="text-sm" :class="form[field.key] ? 'text-slate-700' : 'text-slate-400'">{{ form[field.key] ? '已启用' : '已关闭' }}</span>
                </label>

                <textarea v-else-if="field.type === 'text'" v-model="form[field.key]" rows="4" :placeholder="placeholderOf(field)" :class="inputClass + ' font-mono'" />
                <textarea v-else-if="field.type === 'strings' || field.type === 'ints'" v-model="form[field.key]" rows="3" placeholder="每行一个" :class="inputClass + ' font-mono'" />

                <p v-if="field.help" class="text-xs text-slate-400 mt-1.5">{{ field.help }}</p>
                <p
                  v-if="field.key === 'bot.admin_panel.enable' && form[field.key] === false"
                  class="text-xs text-amber-600 mt-1.5"
                >
                  关闭并重启后将无法访问本面板。如需重新开启，可设置环境变量
                  <code class="font-mono bg-amber-50 px-1 rounded">ANIA_BOT_ADMIN_PANEL_ENABLE=true</code>
                  覆盖配置后重启 Bot。
                </p>
                  </div>
                </div>
              </div>
            </div>
          </Transition>
        </section>
      </template>

      <!-- 高级模式：原始 JSON -->
      <section v-else class="bg-white rounded-xl shadow-sm border border-white/60 p-4 sm:p-6 space-y-3">
        <p class="text-xs text-slate-500">全部配置键的扁平 JSON 视图（键为小写点分路径）。编辑后点击保存。</p>
        <textarea v-model="rawText" rows="24" spellcheck="false" class="w-full bg-slate-950/85 text-slate-200 rounded-lg px-4 py-3 text-xs font-mono leading-relaxed focus:outline-none focus:ring-2 focus:ring-zinc-400" />
        <div class="flex items-center gap-3">
          <button class="px-3 py-1.5 text-sm rounded-lg border border-slate-300/60 text-slate-600 hover:bg-white/55 transition-colors" @click="formatRaw">格式化</button>
          <button :disabled="saving" class="px-4 py-1.5 text-sm rounded-lg btn-accent disabled:opacity-40 transition-colors" @click="onSaveRaw">保存</button>
          <span v-if="rawError" class="text-sm text-red-600">{{ rawError }}</span>
        </div>
      </section>
    </div>

    <!-- 浮动保存条 -->
    <Transition name="fade">
      <div
        v-if="!rawMode && dirty"
      class="fixed bottom-6 left-1/2 -translate-x-1/2 z-40 flex items-center gap-4 btn-accent rounded-full pl-5 pr-2 py-2 shadow-2xl shadow-slate-900/30"
      >
        <span class="text-sm text-slate-300">有未保存的修改</span>
        <button class="text-sm text-slate-400 hover:text-white transition-colors" @click="resetForm">放弃</button>
        <button
          :disabled="saving"
          class="px-4 py-1.5 text-sm rounded-full bg-white text-zinc-900 hover:bg-white/70 disabled:opacity-40 transition-colors font-medium"
          @click="onSave"
        >
          {{ saving ? '保存中...' : '保存修改' }}
        </button>
      </div>
    </Transition>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { api } from '../api.js'
import QrLoginCard from '../components/QrLoginCard.vue'

const MASK = '********'
const inputClass = 'w-full border border-slate-300/60 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400 transition-shadow'

const iconSearch = '<svg fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z"/></svg>'
const iconCheck = '<svg fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z"/></svg>'
const iconPuzzle = '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M14.25 6.087c0-.355.186-.676.401-.959.221-.29.349-.634.349-1.003 0-1.036-1.007-1.875-2.25-1.875s-2.25.84-2.25 1.875c0 .369.128.713.349 1.003.215.283.401.604.401.959v.431c0 .46-.335.84-.782.927a7.59 7.59 0 0 1-1.181.093h-.77c-.254 0-.487.09-.668.24-.297.246-.451.619-.371 1.014.073.361.026.74-.145 1.086-.199.402-.576.65-1.007.65H7.5c-.621 0-1.125.504-1.125 1.125v.77c0 .418.314.82.77 1.118.198.13.37.305.48.515.16.308.165.674.014.97-.168.333-.502.521-.864.521H4.875A1.875 1.875 0 0 1 3 14.25v-1.77c0-.358-.215-.68-.543-.822A1.87 1.87 0 0 0 1.875 9.75c0-1.243 1.007-2.25 2.25-2.25.369 0 .713.128 1.003.349.283.215.604.401.959.401h.413"/></svg>'
const iconCube = '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9"/></svg>'
const iconBookmark = '<svg fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M17.593 3.322c1.1.128 1.907 1.077 1.907 2.185V21L12 17.25 4.5 21V5.507c0-1.108.806-2.057 1.907-2.185a48.507 48.507 0 0 1 11.186 0Z"/></svg>'

const schema = ref([])
const values = ref({})
const form = reactive({}) // field.key -> 编辑中的字符串/布尔值
const original = reactive({})
const cleared = reactive({}) // 用户显式点击「清除」的敏感字段：field.key -> true，保存时发送 null 删除该键（留空不修改与清空恢复未配置两种语义分离）
const saved = ref(false)
const saving = ref(false)
const rawMode = ref(false)
const rawText = ref('')
const rawError = ref('')
const search = ref('')
const activeGroup = ref('')
const presets = ref([])
const presetName = ref('')
const presetSaving = ref(false)

// ---- 页面整理状态 ----
const presetsOpen = ref(false) // 配置预设折叠面板（默认收起，避免抢占页面空间）
const activeCategory = ref('框架基础') // 当前分类页签
const openGroups = ref(new Set()) // 已展开的卡片（父分组名，默认全部展开，方便一眼看全配置）

const searching = computed(() => search.value.trim() !== '')

const groups = computed(() => {
  const map = new Map()
  for (const f of schema.value) {
    if (!map.has(f.group)) map.set(f.group, [])
    map.get(f.group).push(f)
  }
  return [...map.entries()].map(([name, fields]) => ({ name, fields }))
})

// 分类：按配置键前缀判断，bot.* 为框架基础，其余归入插件（AI 对话插件也是插件，不单独设分类）
const CAT_ORDER = ['框架基础', '插件']

// 分组名支持子分组：以 "." 或 " · " 分隔（如 "地震预警.基础"、"AI 对话 · 模型"），
// 前段为父分组（通常是插件名），后段为子分组名
function splitGroupName(name) {
  const m = name.match(/^(.+?)(?:\s·\s|\.)(.+)$/)
  return m ? { parent: m[1].trim(), child: m[2].trim() } : { parent: name, child: '' }
}

function categoryOf(group) {
  const key = group.fields[0]?.key || ''
  return key.startsWith('bot.') ? '框架基础' : '插件'
}

// 搜索过滤后的分组，按分类排序（与左侧导航一致，插件在最后）
const filteredGroups = computed(() => {
  const q = search.value.trim().toLowerCase()
  const list = !q
    ? groups.value
    : groups.value
      .map((g) => ({
        name: g.name,
        fields: g.fields.filter((f) =>
          f.label.toLowerCase().includes(q) ||
          f.key.toLowerCase().includes(q) ||
          (f.help || '').toLowerCase().includes(q)
        ),
      }))
      .filter((g) => g.fields.length > 0)
  return [...list].sort((a, b) => CAT_ORDER.indexOf(categoryOf(a)) - CAT_ORDER.indexOf(categoryOf(b)))
})

// 卡片：一个插件（父分组）一张卡片，子分组作为卡片内的分节（保持注册顺序）。
// 无子分组的平铺分组自成一张卡片（唯一分节 label 为空，直接铺字段）；
// 平铺分组与同名子分组并存时，平铺组显示为「通用」分节。
const cards = computed(() => {
  const cats = []
  const byCat = new Map()
  const nodeOf = (catName, parent) => {
    let cat = byCat.get(catName)
    if (!cat) {
      cat = { name: catName, nodes: [], byParent: new Map(), total: 0 }
      byCat.set(catName, cat)
      cats.push(cat)
    }
    let node = cat.byParent.get(parent)
    if (!node) {
      node = { name: parent, cat: catName, sections: [] }
      cat.byParent.set(parent, node)
      cat.nodes.push(node)
    }
    return { cat, node }
  }
  for (const g of filteredGroups.value) {
    const { parent, child } = splitGroupName(g.name)
    const { cat, node } = nodeOf(categoryOf(g), parent)
    cat.total += g.fields.length
    node.sections.push({ name: g.name, label: child, fields: g.fields })
  }
  // 平铺分组与同名子分组并存：平铺组分节改标「通用」并移到最前
  for (const cat of cats) {
    for (const node of cat.nodes) {
      const bare = node.sections.find((s) => !s.label)
      if (bare && node.sections.length > 1) {
        bare.label = '通用'
        node.sections.splice(node.sections.indexOf(bare), 1)
        node.sections.unshift(bare)
      }
    }
  }
  return cats
})

// 是否有子分组分节（否则平铺直排字段）
function hasSubs(node) {
  return node.sections.length > 1 || (node.sections.length === 1 && node.sections[0].label !== '')
}

function cardCount(node) {
  return node.sections.reduce((n, s) => n + s.fields.length, 0)
}

const totalCards = computed(() => cards.value.reduce((n, c) => n + c.nodes.length, 0))

const allOpen = computed(() => openGroups.value.size >= totalCards.value) // 是否展开全部卡片

const activeCatNodes = computed(() => {
  const cat = cards.value.find((c) => c.name === activeCategory.value)
  return cat ? cat.nodes : []
})

// 展示的卡片：搜索时展示所有匹配卡片（方便扫读），否则只展示当前分类
const displayCards = computed(() => (searching.value ? cards.value.flatMap((c) => c.nodes) : activeCatNodes.value))

function sectionId(name) {
  return 'grp-' + name.replace(/[^\w一-龥]+/g, '-')
}

function isOpen(node) {
  return searching.value || openGroups.value.has(node.name)
}

function groupChanged(node) {
  return node.sections.some((s) => s.fields.some((f) => cleared[f.key] || !sameFormValue(form[f.key], original[f.key])))
}

function toggleGroup(name) {
  const s = new Set(openGroups.value)
  if (s.has(name)) s.delete(name)
  else s.add(name)
  openGroups.value = s
  activeGroup.value = name
}

function toggleAll() {
  const all = cards.value.flatMap((c) => c.nodes.map((n) => n.name))
  openGroups.value = allOpen.value ? new Set() : new Set(all)
}

function selectCategory(name) {
  search.value = ''
  activeCategory.value = name
  const cat = cards.value.find((c) => c.name === name)
  activeGroup.value = cat && cat.nodes.length ? cat.nodes[0].name : ''
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function jumpTo(name) {
  // name 为卡片名（父分组）或分节名（子分组）：展开所属卡片后滚动到对应位置
  let card = null
  for (const cat of cards.value) {
    for (const n of cat.nodes) {
      if (n.name === name || n.sections.some((s) => s.name === name)) {
        card = n
        break
      }
    }
    if (card) break
  }
  if (!card) return
  search.value = ''
  activeCategory.value = card.cat
  const s = new Set(openGroups.value)
  s.add(card.name)
  openGroups.value = s
  activeGroup.value = name
  await nextTick()
  document.getElementById(sectionId(name))?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

function groupColor(node) {
  return node.cat === '框架基础' ? 'bg-zinc-500' : 'bg-zinc-700'
}

function groupIcon(node) {
  return node.cat === '框架基础' ? iconCube : iconPuzzle
}

// multiselect 的表单值是数组，比较时按集合语义（与顺序无关）
function sameFormValue(a, b) {
  if (Array.isArray(a) || Array.isArray(b)) {
    const x = Array.isArray(a) ? a : []
    const y = Array.isArray(b) ? b : []
    return x.length === y.length && x.every((v) => y.includes(v))
  }
  return a === b
}

function copyFormValue(v) {
  return Array.isArray(v) ? [...v] : v
}

const dirty = computed(() => schema.value.some((f) => cleared[f.key] || !sameFormValue(form[f.key], original[f.key])))

function valueOf(key) {
  return values.value[key.toLowerCase()]
}

function placeholderOf(field) {
  const v = valueOf(field.key)
  if (field.sensitive && v === MASK) return '已设置（留空保持不变，点「清除」删除该配置）'
  if (v === undefined || v === null || v === '') return field.optional ? '未设置（不传该参数）' : '未设置'
  return ''
}

function toFormValue(field) {
  const v = valueOf(field.key)
  if (field.type === 'multiselect') return Array.isArray(v) ? [...v] : []
  if (field.type === 'bool') return v === true
  if (v === undefined || v === null) return ''
  if (field.type === 'strings' || field.type === 'ints') return Array.isArray(v) ? v.join('\n') : ''
  if (field.sensitive) return '' // 敏感字段不回显，留空表示不修改
  return String(v)
}

function fromFormValue(field) {
  const raw = form[field.key]
  if (field.type === 'bool') return raw === true
  if (field.type === 'int') {
    if (field.optional && raw.trim() === '') return null // 可选参数：清空=删除该键，不向下游传
    const n = parseInt(raw, 10); return Number.isNaN(n) ? 0 : n
  }
  if (field.type === 'float') {
    if (field.optional && raw.trim() === '') return null // 可选参数：清空=删除该键，不向下游传
    const n = parseFloat(raw); return Number.isNaN(n) ? 0 : n
  }
  if (field.type === 'multiselect') return Array.isArray(raw) ? [...raw] : []
  if (field.type === 'strings') return raw.split('\n').map((s) => s.trim()).filter(Boolean)
  if (field.type === 'select') return raw === '' ? null : raw // 留空=删除该键，恢复「跟随主模型格式」等未配置语义
  if (field.type === 'ints') return raw.split('\n').map((s) => parseInt(s.trim(), 10)).filter((n) => !Number.isNaN(n))
  return raw
}

function isSelected(field, opt) {
  const v = form[field.key]
  return Array.isArray(v) && v.includes(opt)
}

// 已选项计数：只统计仍然存在的选项（历史值里可能有已下线的选项）
function selectedCount(field) {
  const v = form[field.key]
  if (!Array.isArray(v)) return 0
  const opts = field.options || []
  return v.filter((o) => opts.includes(o)).length
}

function toggleOption(field, opt) {
  const cur = Array.isArray(form[field.key]) ? [...form[field.key]] : []
  const i = cur.indexOf(opt)
  if (i >= 0) cur.splice(i, 1)
  else cur.push(opt)
  form[field.key] = cur
}

function selectAllOptions(field) {
  form[field.key] = [...(field.options || [])]
}

function clearOptions(field) {
  form[field.key] = []
}

function hasDefault(field) {
  return field.default !== undefined && field.default !== null && field.default !== ''
}

// 敏感字段显式清空：与「留空=不修改」分离，保存时发送 null 删除该键
function clearField(field) {
  if (!confirm(`清空「${field.label}」后将删除该配置项，重启后生效。确定清空？`)) return
  form[field.key] = ''
  cleared[field.key] = true
}

function resetForm() {
  for (const f of schema.value) { form[f.key] = copyFormValue(original[f.key]); cleared[f.key] = false }
}

async function reloadValues() {
  values.value = await api.getConfig()
  for (const f of schema.value) {
    form[f.key] = toFormValue(f)
    original[f.key] = copyFormValue(form[f.key])
    cleared[f.key] = false
  }
  rawText.value = JSON.stringify(values.value, null, 2)
}

onMounted(async () => {
  const [s, v, p] = await Promise.all([api.getSchema(), api.getConfig(), api.getPresets()])
  schema.value = s
  presets.value = p
  values.value = v
  for (const f of s) {
    form[f.key] = toFormValue(f)
    original[f.key] = copyFormValue(form[f.key])
    cleared[f.key] = false
  }
  rawText.value = JSON.stringify(v, null, 2)
  if (cards.value.length) {
    activeCategory.value = cards.value[0].name
    activeGroup.value = cards.value[0].nodes.length ? cards.value[0].nodes[0].name : ''
    openGroups.value = new Set(cards.value.flatMap((c) => c.nodes.map((n) => n.name))) // 默认全部展开，方便一眼看全配置
  }
})

async function onSave() {
  const updates = {}
  for (const f of schema.value) {
    if (cleared[f.key]) {
      updates[f.key] = null // 显式清空敏感字段=删除该键，恢复「留空使用主模型」等未配置语义
      continue
    }
    if (sameFormValue(form[f.key], original[f.key])) continue
    if (f.sensitive && form[f.key] === '') continue // 未填写 = 不修改
    updates[f.key] = fromFormValue(f)
  }
  if (Object.keys(updates).length === 0) return
  saving.value = true
  try {
    await api.saveConfig(updates)
    saved.value = true
    // 重新加载，同步掩码与原始值
    await reloadValues()
  } catch (e) {
    alert(e.message)
  } finally {
    saving.value = false
  }
}

function formatRaw() {
  rawError.value = ''
  try {
    rawText.value = JSON.stringify(JSON.parse(rawText.value), null, 2)
  } catch {
    rawError.value = 'JSON 格式错误'
  }
}

async function onSaveRaw() {
  rawError.value = ''
  let parsed
  try {
    parsed = JSON.parse(rawText.value)
  } catch {
    rawError.value = 'JSON 格式错误'
    return
  }
  saving.value = true
  try {
    await api.saveConfig(parsed)
    saved.value = true
    await reloadValues()
  } catch (e) {
    rawError.value = e.message
  } finally {
    saving.value = false
  }
}

// ---- 导出 ----

async function onExportConfig() {
  if (!confirm('导出的 JSON 包含密钥等敏感信息，请妥善保管。继续导出？')) return
  try {
    await api.exportConfig()
  } catch (e) {
    alert(e.message)
  }
}

// ---- 配置预设 ----

function formatPresetTime(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString()
}

async function refreshPresets() {
  presets.value = await api.getPresets()
}

// name 为空时保存输入框中的新预设，否则用当前配置覆盖同名预设
async function onSavePreset(name) {
  const target = (name || presetName.value).trim()
  if (!target) return
  if (name && !confirm(`用当前配置覆盖预设「${target}」？`)) return
  if (dirty.value && !confirm('当前有未保存的修改，预设保存的是数据库中已保存的配置（不含未保存的修改）。继续？')) return
  presetSaving.value = true
  try {
    await api.savePreset(target)
    presetName.value = ''
    await refreshPresets()
  } catch (e) {
    alert(e.message)
  } finally {
    presetSaving.value = false
  }
}

async function onApplyPreset(p) {
  if (dirty.value && !confirm(`当前有未保存的修改，应用预设「${p.name}」将重新加载配置，未保存的修改会丢失。继续？`)) return
  else if (!dirty.value && !confirm(`应用预设「${p.name}」？快照中的配置项将覆盖当前配置（含密钥），重启 Bot 后生效。`)) return
  try {
    await api.applyPreset(p.name)
    saved.value = true
    await reloadValues()
  } catch (e) {
    alert(e.message)
  }
}

async function onDeletePreset(p) {
  if (!confirm(`删除预设「${p.name}」？只删除快照，不影响当前配置。`)) return
  try {
    await api.deletePreset(p.name)
    await refreshPresets()
  } catch (e) {
    alert(e.message)
  }
}
</script>
