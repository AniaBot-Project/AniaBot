<template>
  <div class="space-y-4">
    <!-- 定时任务管理 -->
    <section class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden">
      <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between">
        <h2 class="text-[10px] tracking-[0.15em] uppercase text-zinc-800 font-medium">定时任务</h2>
        <div class="flex items-center gap-3">
          <button class="text-[10px] tracking-[0.15em] uppercase text-zinc-500 hover:text-zinc-900 font-medium transition-colors" @click="loadClocks">刷新</button>
        <button class="text-[10px] tracking-[0.15em] uppercase btn-accent px-3 py-1.5 rounded-md font-medium transition-colors" @click="openCreate">新建任务</button>
        </div>
      </div>
      <p v-if="clocks.length === 0" class="px-6 py-8 text-xs text-zinc-400 text-center tracking-wide">暂无定时任务，点击右上角「新建任务」创建（也可在群聊/私聊中使用 /clock）</p>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-left text-[10px] tracking-[0.15em] uppercase text-zinc-400 bg-zinc-50/60 border-b border-zinc-100">
              <th class="px-3 py-3 sm:px-6 font-medium">任务</th>
              <th class="px-3 py-3 sm:px-6 font-medium">目标</th>
              <th class="px-3 py-3 sm:px-6 font-medium">Cron</th>
              <th class="px-3 py-3 sm:px-6 font-medium">下次执行</th>
              <th class="px-3 py-3 sm:px-6 font-medium">上次执行</th>
              <th class="px-3 py-3 sm:px-6 font-medium">启用</th>
            </tr>
          </thead>
          <tbody>
          <template v-for="t in clocks" :key="t.id">
            <tr
              class="border-b border-dashed border-zinc-100 last:border-0 hover:bg-white/55 transition-colors cursor-pointer"
              :class="{ 'bg-zinc-50/70': expanded.has(t.id) }"
              @click="toggleExpand(t.id)"
            >
              <td class="px-3 py-3 sm:px-6 text-zinc-800 max-w-48">
                <span class="flex items-center gap-1.5">
                  <span
                    class="[&>svg]:w-3 [&>svg]:h-3 text-zinc-400 transition-transform shrink-0"
                    :class="{ 'rotate-90': expanded.has(t.id) }"
                    v-html="icons.chevron"
                  />
                  <span class="truncate font-medium" :title="t.title">{{ t.title || '(无标题)' }}</span>
                </span>
                <span v-if="t.run_once" class="text-[9px] tracking-[0.12em] uppercase border border-zinc-300 text-zinc-500 px-1.5 py-0.5 rounded ml-4">单次</span>
              </td>
              <td class="px-3 py-3 sm:px-6 text-zinc-600 whitespace-nowrap">{{ t.target_type === 'group' ? '群' : '好友' }} {{ t.target_id }}</td>
              <td class="px-3 py-3 sm:px-6 text-zinc-500 whitespace-nowrap">{{ t.cron }}</td>
              <td class="px-3 py-3 sm:px-6 text-zinc-600 whitespace-nowrap">{{ t.enabled ? fmtTimeFull(t.next_run_at) : '—' }}</td>
              <td class="px-3 py-3 sm:px-6 text-zinc-600 whitespace-nowrap">{{ fmtTimeFull(t.last_run_at) }}</td>
              <td class="px-3 py-3 sm:px-6" @click.stop>
                <button
                  type="button"
                  role="switch"
                  :aria-checked="t.enabled"
                  :disabled="toggling.has(t.id)"
                  class="relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:opacity-50"
                  :class="t.enabled ? 'bg-indigo-500' : 'bg-slate-300/70'"
                  @click="toggleClock(t)"
                >
                  <span
                    class="inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform"
                    :class="t.enabled ? 'translate-x-4.5' : 'translate-x-0.75'"
                  />
                </button>
              </td>
            </tr>
            <!-- 任务详情 -->
            <tr v-if="expanded.has(t.id)" class="border-b border-dashed border-zinc-100 last:border-0 bg-zinc-50/40">
              <td colspan="6" class="px-3 py-4 sm:px-6">
                <dl class="grid grid-cols-1 sm:grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-xs">
                  <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">任务内容</dt>
                  <dd class="text-zinc-700 whitespace-pre-wrap break-all">{{ t.content }}</dd>
                  <template v-if="t.note">
                    <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">备注</dt>
                    <dd class="text-zinc-700 whitespace-pre-wrap break-all">{{ t.note }}</dd>
                  </template>
                  <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">超时时间</dt>
                  <dd class="text-zinc-700">{{ t.timeout_sec > 0 ? t.timeout_sec + ' 秒' : '默认' }}</dd>
                  <template v-if="t.target_type === 'group' && t.created_by">
                    <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">提醒 @</dt>
                    <dd class="text-zinc-700 font-mono">{{ t.created_by }}</dd>
                  </template>
                  <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">创建人</dt>
                  <dd class="text-zinc-700 font-mono">{{ fmtActor(t.creator) }}</dd>
                  <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">更新人</dt>
                  <dd class="text-zinc-700 font-mono">{{ fmtActor(t.updater) }}</dd>
                  <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">创建时间</dt>
                  <dd class="text-zinc-700">{{ fmtTimeFull(t.created_at) }}</dd>
                </dl>
                <div class="mt-4 flex items-center gap-2">
                  <button
                    class="text-[10px] tracking-[0.15em] uppercase btn-accent px-3 py-1.5 rounded-md font-medium transition-colors disabled:opacity-50"
                    :disabled="toggling.has(t.id)"
                    @click="openEdit(t)"
                  >编辑</button>
                  <button
                    class="text-[10px] tracking-[0.15em] uppercase border border-zinc-300 text-zinc-600 px-3 py-1.5 rounded-md hover:bg-white/60 hover:text-red-600 hover:border-red-300 font-medium transition-colors disabled:opacity-50"
                    :disabled="toggling.has(t.id)"
                    @click="removeClock(t)"
                  >删除</button>
                </div>
              </td>
            </tr>
          </template>
          </tbody>
        </table>
      </div>
    </section>

    <!-- 筛选与操作栏 -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-3">
        <h2 class="text-[10px] tracking-[0.15em] uppercase text-zinc-500 font-medium">执行日志</h2>
        <div class="flex items-center gap-1 glass rounded-xl p-1">
        <button
          v-for="t in typeTabs"
          :key="t.value"
          class="px-3 py-1.5 text-xs rounded-md transition-all"
          :class="filters.target_type === t.value
            ? 'btn-accent font-medium shadow-sm'
            : 'text-slate-500 hover:text-slate-800 hover:bg-white/60'"
          @click="filters.target_type = t.value; applyFilters()"
        >
          {{ t.label }}
        </button>
        </div>
      </div>
      <div class="flex items-center gap-3">
        <label class="flex items-center gap-1.5 text-xs text-slate-500 select-none cursor-pointer">
          <input v-model="autoRefresh" type="checkbox" />
          自动刷新
        </label>
        <button class="text-xs text-zinc-700 hover:text-zinc-900 font-medium transition-colors" @click="load">刷新</button>
      </div>
    </div>

    <!-- 条件查询栏 -->
    <section class="bg-white rounded-xl shadow-sm border border-white/60 px-5 py-4">
      <div class="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">状态</span>
          <select v-model="filters.status" :class="inputClass" @change="applyFilters">
            <option value="">全部</option>
            <option value="running">执行中</option>
            <option value="success">成功</option>
            <option value="timeout">超时</option>
            <option value="error">出错</option>
            <option value="interrupted">中断</option>
          </select>
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">目标会话 ID</span>
          <input v-model.trim="filters.target_id" type="text" placeholder="精确匹配" :class="inputClass" @keyup.enter="applyFilters" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">开始时间</span>
          <input v-model="filters.start" type="datetime-local" :class="inputClass" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">结束时间</span>
          <input v-model="filters.end" type="datetime-local" :class="inputClass" />
        </label>
        <label class="block">
          <span class="text-[10px] tracking-[0.15em] uppercase text-zinc-400">关键词</span>
          <input v-model.trim="filters.keyword" type="text" placeholder="匹配任务标题" :class="inputClass" @keyup.enter="applyFilters" />
        </label>
        <div class="flex items-end gap-2">
          <button
            class="px-4 py-1.5 text-[11px] tracking-widest uppercase btn-accent rounded-md transition-colors"
            @click="applyFilters"
          >
            查询
          </button>
          <button
            v-if="hasFilter"
            class="px-3 py-1.5 text-[11px] tracking-widest uppercase text-zinc-500 hover:bg-white/60 rounded-md transition-colors"
            @click="resetFilters"
          >
            重置
          </button>
        </div>
      </div>
    </section>

    <!-- 日志列表（新在上，滚动到底部自动加载更早的记录） -->
    <section class="space-y-3">
      <div v-if="logs.length === 0" class="bg-white rounded-xl shadow-sm border border-white/60 py-12 text-sm text-slate-400 text-center">
        暂无符合条件的执行记录（定时任务触发后在此展示）
      </div>

      <div
        v-for="log in logs"
        :key="log.id"
        class="bg-white rounded-xl shadow-sm border border-white/60 overflow-hidden"
      >
        <!-- 摘要行（点击弹出详情窗口） -->
        <button class="w-full text-left px-5 py-3.5 hover:bg-white/55 transition-colors" @click="detail = log">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap" :class="statusClass(log.status)">
              {{ statusText(log.status) }}
            </span>
            <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap bg-zinc-100 text-zinc-600 border border-zinc-200">
              {{ log.target_type === 'group' ? '群聊' : '私聊' }} · {{ log.target_id }}
            </span>
            <span v-if="log.tool_calls?.length" class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap bg-zinc-100 text-zinc-600 border border-zinc-200">
              {{ log.tool_calls.length }} 次工具调用
            </span>
            <span class="ml-auto text-xs text-slate-400 font-mono whitespace-nowrap">{{ fmtTime(log.trigger_time) }}</span>
          </div>
          <p class="mt-2 text-sm text-slate-700 font-medium truncate">{{ log.task_title || '(无标题)' }}</p>
          <div class="mt-2 flex items-center gap-3 text-[11px] text-slate-400 font-mono flex-wrap">
            <span v-if="log.status !== 'running'">用时 {{ fmtDuration(log.duration_ms) }}</span>
            <span v-if="log.iterations">LLM {{ log.iterations }} 轮</span>
            <template v-if="log.total_tokens">
              <span>tokens 总计 {{ log.total_tokens }}</span>
              <span>输入 {{ log.prompt_tokens }}</span>
              <span>输出 {{ log.completion_tokens }}</span>
              <span v-if="log.cached_tokens">缓存命中 {{ log.cached_tokens }}</span>
            </template>
            <span v-if="log.error" class="text-red-500 truncate max-w-80">{{ log.error }}</span>
            <span class="ml-auto text-zinc-400">详情 ⤢</span>
          </div>
        </button>
      </div>
    </section>

    <!-- 滚动分页哨兵：进入视口即加载下一页 -->
    <div ref="sentinel" class="h-px" />
    <div v-if="loadingMore" class="py-3 text-xs text-slate-400 text-center">加载更早的记录…</div>
    <div v-else-if="!hasMore && logs.length" class="py-3 text-xs text-slate-300 text-center">没有更早的记录了</div>

    <!-- 详情弹窗：点遮罩 / 右上角关闭 / Esc 均可关闭 -->
    <div
      v-if="detail"
      class="fixed inset-0 bg-slate-900/45 backdrop-blur-md flex items-center justify-center z-50 p-4"
      v-backdrop-close="() => (detail = null)"
    >
      <div class="bg-white rounded-xl shadow-2xl border border-zinc-200 w-full max-w-3xl max-h-[85vh] flex flex-col">
        <!-- 弹窗头部 -->
        <div class="flex items-center gap-2 flex-wrap px-5 py-3.5 border-b border-zinc-100 shrink-0">
          <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap" :class="statusClass(detail.status)">
            {{ statusText(detail.status) }}
          </span>
          <span class="text-xs px-2 py-0.5 rounded-full whitespace-nowrap bg-zinc-100 text-zinc-600 border border-zinc-200">
            {{ detail.target_type === 'group' ? '群聊' : '私聊' }} · {{ detail.target_id }}
          </span>
          <span class="text-sm text-zinc-800 font-medium truncate">{{ detail.task_title || '(无标题)' }}</span>
          <button
            class="ml-auto w-7 h-7 flex items-center justify-center rounded-md text-zinc-400 hover:text-zinc-800 hover:bg-white/60 transition-colors"
            title="关闭"
            @click="detail = null"
          >
            ✕
          </button>
        </div>

        <!-- 弹窗内容（可滚动） -->
        <div class="px-5 py-4 space-y-4 overflow-y-auto">
          <!-- 概要指标 -->
          <dl class="grid grid-cols-1 sm:grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-xs">
            <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">任务 ID</dt>
            <dd class="text-zinc-700 font-mono">{{ detail.task_id || '—' }}</dd>
            <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">触发时间</dt>
            <dd class="text-zinc-700 font-mono">{{ fmtTimeFull(detail.trigger_time) }}</dd>
            <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">完成时间</dt>
            <dd class="text-zinc-700 font-mono">{{ fmtTimeFull(detail.finished_at) }}</dd>
            <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">耗时</dt>
            <dd class="text-zinc-700 font-mono">{{ detail.status !== 'running' ? fmtDuration(detail.duration_ms) : '—' }}</dd>
            <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">LLM 轮数</dt>
            <dd class="text-zinc-700 font-mono">{{ detail.iterations || '—' }}</dd>
            <dt class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 self-center">Token 用量</dt>
            <dd class="text-zinc-700 font-mono">
              {{ detail.total_tokens
                ? `总计 ${detail.total_tokens} · 输入 ${detail.prompt_tokens} · 输出 ${detail.completion_tokens}` +
                  (detail.cached_tokens ? ` · 缓存命中 ${detail.cached_tokens}` : '')
                : '—' }}
            </dd>
          </dl>

          <!-- 任务内容（触发时发送给 AI 的内容） -->
          <div v-if="detail.trigger_content">
            <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium mb-2">任务内容</h3>
            <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ detail.trigger_content }}</p>
          </div>

          <!-- 错误信息 -->
          <div v-if="detail.error" class="text-xs text-red-600 bg-red-50 border border-red-100 rounded-lg px-3 py-2 whitespace-pre-wrap break-all">
            {{ detail.error }}
          </div>

          <!-- 工具调用明细 -->
          <div v-if="detail.tool_calls?.length" class="space-y-2">
            <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium">
              工具调用
              <span v-if="detail.tool_calls_total > detail.tool_calls.length" class="normal-case tracking-normal text-zinc-300">
                （共 {{ detail.tool_calls_total }} 次，仅保留前 {{ detail.tool_calls.length }} 条）
              </span>
            </h3>
            <div
              v-for="(tc, i) in detail.tool_calls"
              :key="i"
              class="bg-white border border-white/60 rounded-lg overflow-hidden"
            >
              <div class="flex items-center gap-2 px-3 py-2 border-b border-white/50">
                <span class="text-xs font-mono font-medium text-zinc-800">{{ tc.name }}</span>
                <span v-if="tc.error" class="text-[11px] px-1.5 py-0.5 rounded bg-red-50 text-red-600 border border-red-100">失败</span>
                <span class="ml-auto text-[11px] text-slate-400 font-mono">{{ fmtDuration(tc.duration_ms) }}</span>
              </div>
              <div class="px-3 py-2 space-y-2">
                <div v-if="tc.arguments">
                  <div class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 mb-1">参数</div>
                  <pre class="text-xs text-slate-600 font-mono whitespace-pre-wrap break-all leading-relaxed">{{ tc.arguments }}</pre>
                </div>
                <div v-if="tc.error">
                  <div class="text-[10px] tracking-[0.15em] uppercase text-red-400 mb-1">错误</div>
                  <pre class="text-xs text-red-600 font-mono whitespace-pre-wrap break-all leading-relaxed">{{ tc.error }}</pre>
                </div>
                <div v-else-if="tc.result">
                  <div class="text-[10px] tracking-[0.15em] uppercase text-zinc-400 mb-1">结果</div>
                  <pre class="text-xs text-slate-600 font-mono whitespace-pre-wrap break-all leading-relaxed">{{ tc.result }}</pre>
                </div>
              </div>
            </div>
          </div>

          <!-- 最终回复 -->
          <div v-if="detail.reply">
            <h3 class="text-[11px] tracking-[0.2em] uppercase text-zinc-400 font-medium mb-2">最终回复</h3>
            <p class="text-sm text-slate-700 whitespace-pre-wrap break-all leading-relaxed bg-slate-50 border border-white/60 rounded-lg px-3 py-2">{{ detail.reply }}</p>
          </div>
        </div>
      </div>
    </div>

    <!-- 新建 / 编辑定时任务弹窗 -->
    <Teleport to="body">
      <div v-if="clockForm" class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/45 backdrop-blur-md p-4" v-backdrop-close="() => (clockForm = null)">
        <div class="bg-white rounded-xl shadow-2xl border border-zinc-200 w-full max-w-lg max-h-[90vh] overflow-y-auto">
          <div class="px-6 py-4 border-b border-zinc-100 flex items-center justify-between">
            <h3 class="text-[10px] tracking-[0.15em] uppercase text-zinc-800 font-medium">{{ clockForm.id ? '编辑定时任务' : '新建定时任务' }}</h3>
            <button class="text-zinc-400 hover:text-zinc-700 transition-colors" @click="clockForm = null">✕</button>
          </div>
          <form class="px-6 py-5 space-y-4" @submit.prevent="saveClock">
            <div>
              <label class="form-label">任务标题</label>
              <input v-model.trim="clockForm.title" type="text" class="form-input" placeholder="如：每日晨报" />
            </div>
            <div>
              <label class="form-label">任务内容 <span class="text-red-500">*</span></label>
              <textarea v-model.trim="clockForm.content" rows="3" class="form-input" placeholder="触发时发送给 AI 的内容"></textarea>
            </div>
            <div class="grid grid-cols-2 gap-4">
              <div>
                <label class="form-label">Cron 表达式 <span class="text-red-500">*</span></label>
                <input v-model.trim="clockForm.cron" type="text" class="form-input" placeholder="0 8 * * * 或 @every 1h" />
              </div>
              <div>
                <label class="form-label">超时时间（秒，0 为默认）</label>
                <input v-model.number="clockForm.timeout_sec" type="number" min="0" class="form-input" />
              </div>
            </div>
            <div class="grid grid-cols-2 gap-4">
              <div>
                <label class="form-label">触发对象 <span class="text-red-500">*</span></label>
                <select v-model="clockForm.target_type" class="form-input">
                  <option value="group">群聊</option>
                  <option value="friend">好友</option>
                </select>
              </div>
              <div>
                <label class="form-label">{{ clockForm.target_type === 'group' ? '群 ID' : '用户 ID' }} <span class="text-red-500">*</span></label>
                <input v-model.trim="clockForm.target_id" type="text" class="form-input" placeholder="如 123456 或 fs:oc_xxx" />
              </div>
            </div>
            <div v-if="clockForm.target_type === 'group'">
              <label class="form-label">提醒 @</label>
              <input v-model.trim="clockForm.created_by" type="text" class="form-input" placeholder="触发时 @ 的用户 ID，如 qq:123456，留空不 @" />
            </div>
            <div>
              <label class="form-label">备注</label>
              <input v-model.trim="clockForm.note" type="text" class="form-input" placeholder="可选，触发时附带给 AI" />
            </div>
            <label class="flex items-center gap-2 text-xs text-zinc-700 select-none">
              <input v-model="clockForm.run_once" type="checkbox" />
              单次任务（触发一次后自动删除）
            </label>
            <p v-if="clockFormError" class="text-xs text-red-600">{{ clockFormError }}</p>
            <div class="flex justify-end gap-2 pt-1">
              <button type="button" class="px-4 py-2 text-[11px] tracking-widest uppercase text-zinc-500 hover:text-zinc-800 font-medium transition-colors" @click="clockForm = null">取消</button>
                <button type="submit" class="px-4 py-2 text-[11px] tracking-widest uppercase btn-accent rounded-md font-medium transition-colors disabled:opacity-50" :disabled="clockSaving">
                {{ clockSaving ? '保存中…' : '保存' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { api } from '../api.js'

const typeTabs = [
  { value: '', label: '全部' },
  { value: 'group', label: '群聊' },
  { value: 'friend', label: '私聊' },
]

const icons = {
  chevron: '<svg fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5"/></svg>',
}

// ---- 定时任务管理 ----

const clocks = ref([])
const toggling = ref(new Set())
const expanded = ref(new Set())
const clockForm = ref(null) // 非 null 时显示弹窗；id 为空表示新建
const clockFormError = ref('')
const clockSaving = ref(false)

async function loadClocks() {
  try { clocks.value = await api.getClocks() } catch { /* 忽略 */ }
}

function toggleExpand(id) {
  const s = new Set(expanded.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  expanded.value = s
}

// 乐观更新开关状态，失败时回滚
async function toggleClock(t) {
  if (toggling.value.has(t.id)) return
  toggling.value = new Set(toggling.value).add(t.id)
  const prev = t.enabled
  t.enabled = !prev
  try {
    await api.updateClock(t.id, { enabled: t.enabled })
  } catch (e) {
    t.enabled = prev
    alert(e.message || '操作失败')
  } finally {
    const s = new Set(toggling.value)
    s.delete(t.id)
    toggling.value = s
  }
}

function blankClockForm() {
  return {
    id: '',
    title: '',
    content: '',
    cron: '',
    target_type: 'group',
    target_id: '',
    timeout_sec: 0,
    note: '',
    run_once: false,
    created_by: '',
  }
}

function openCreate() {
  clockFormError.value = ''
  clockForm.value = blankClockForm()
}

function openEdit(t) {
  clockFormError.value = ''
  clockForm.value = {
    id: t.id,
    title: t.title,
    content: t.content,
    cron: t.cron,
    target_type: t.target_type,
    target_id: t.target_id,
    timeout_sec: t.timeout_sec || 0,
    note: t.note || '',
    run_once: t.run_once,
    created_by: t.created_by || '',
  }
}

async function saveClock() {
  const f = clockForm.value
  if (!f.content) { clockFormError.value = '任务内容不能为空'; return }
  if (!f.cron) { clockFormError.value = 'Cron 表达式不能为空'; return }
  if (!f.target_id) { clockFormError.value = '目标 ID 不能为空'; return }
  clockFormError.value = ''
  clockSaving.value = true
  try {
    if (f.id) {
      await api.updateClock(f.id, {
        title: f.title,
        content: f.content,
        cron: f.cron,
        note: f.note,
        timeout_sec: f.timeout_sec || 0,
        target_type: f.target_type,
        target_id: f.target_id,
        run_once: f.run_once,
        created_by: f.target_type === 'group' ? f.created_by : '',
      })
    } else {
      await api.createClock({
        title: f.title,
        content: f.content,
        cron: f.cron,
        target_type: f.target_type,
        target_id: f.target_id,
        enabled: true,
        run_once: f.run_once,
        timeout_sec: f.timeout_sec || 0,
        note: f.note,
        created_by: f.target_type === 'group' ? f.created_by : '',
      })
    }
    clockForm.value = null
    await loadClocks()
  } catch (e) {
    clockFormError.value = e.message || '保存失败'
  } finally {
    clockSaving.value = false
  }
}

async function removeClock(t) {
  if (!confirm(`确定删除定时任务「${t.title || t.id}」吗？`)) return
  toggling.value = new Set(toggling.value).add(t.id)
  try {
    await api.deleteClock(t.id)
    await loadClocks()
  } catch (e) {
    alert(e.message || '删除失败')
  } finally {
    const s = new Set(toggling.value)
    s.delete(t.id)
    toggling.value = s
  }
}

const inputClass = 'mt-1 w-full border border-zinc-300 rounded-md px-2.5 py-1.5 text-xs focus:outline-none focus:ring-2 focus:ring-zinc-400 focus:border-zinc-400 transition-shadow bg-white'

// filters 为编辑中的条件，applied 为实际生效（已点查询/切换类型）的条件，
// 自动刷新沿用 applied，避免输入到一半被轮询带出去
const emptyFilters = () => ({ target_type: '', target_id: '', status: '', start: '', end: '', keyword: '' })
const filters = reactive(emptyFilters())
const applied = reactive(emptyFilters())

const PAGE = 50 // 每页条数

const logs = ref([]) // 新在前
const autoRefresh = ref(true)
// detail 为当前弹窗展示的日志（null 表示弹窗关闭）
const detail = ref(null)
const hasMore = ref(false) // 是否还有更早的日志可加载
const loadingMore = ref(false)
const loadedOlder = ref(false) // 是否已加载过更早分页（是则刷新不再重置 hasMore）
const sentinel = ref(null)
let timer = null
let observer = null

const hasFilter = computed(() => Object.values(applied).some((v) => v !== ''))

function applyFilters() {
  Object.assign(applied, filters)
  resetList()
  load()
}

function resetFilters() {
  Object.assign(filters, emptyFilters())
  Object.assign(applied, filters)
  resetList()
  load()
}

function resetList() {
  logs.value = []
  hasMore.value = false
  loadedOlder.value = false
}

// Esc 关闭详情弹窗 / 任务表单弹窗
function onKeydown(e) {
  if (e.key === 'Escape') {
    detail.value = null
    clockForm.value = null
  }
}

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d)) return '-'
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false })
  if (d.toDateString() === new Date().toDateString()) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

function fmtTimeFull(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d) || d.getFullYear() < 2000) return '—' // Go 零值时间
  return d.toLocaleString('zh-CN', { hour12: false })
}

function fmtDuration(ms) {
  if (!ms && ms !== 0) return '-'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

// 操作人标识人性化：ai / panel 是固定标识，其余为用户 ID 原样展示
function fmtActor(s) {
  if (!s) return '—'
  if (s === 'ai') return 'AI'
  if (s === 'panel') return '面板'
  return s
}

function statusText(s) {
  return { running: '执行中', success: '成功', timeout: '超时', error: '出错', interrupted: '中断' }[s] || s
}

function statusClass(s) {
  return {
    running: 'btn-accent',
    success: 'bg-white text-zinc-600 border border-zinc-300',
    timeout: 'bg-zinc-100 text-zinc-500 border border-zinc-200',
    error: 'bg-red-50 text-red-600 border border-red-200',
    interrupted: 'bg-amber-50 text-amber-600 border border-amber-200',
  }[s] || 'bg-slate-100 text-slate-600'
}

// 刷新：拉取最新一页，新条目插入头部、已有条目原地更新（执行中 → 完成），
// 已加载的更早分页保留
async function load() {
  let page
  try { page = await api.getTaskLogs({ ...applied, limit: PAGE }) } catch { return }
  mergeHead(page.items || [])
  if (!loadedOlder.value) hasMore.value = page.has_more
}

// 把最新一页合并进列表头部（items 新在前）
function mergeHead(items) {
  if (!logs.value.length) {
    logs.value = items
    return
  }
  const index = new Map(logs.value.map((l, i) => [l.id, i]))
  const fresh = []
  const merged = [...logs.value]
  for (const it of items) {
    if (index.has(it.id)) merged[index.get(it.id)] = it
    else fresh.push(it)
  }
  logs.value = fresh.length ? [...fresh, ...merged] : merged
  // 详情弹窗内容随状态更新同步刷新
  if (detail.value) {
    const cur = logs.value.find((l) => l.id === detail.value.id)
    if (cur) detail.value = cur
  }
}

// 加载更早的一页（滚动分页）
async function loadMore() {
  if (loadingMore.value || !hasMore.value || !logs.value.length) return
  loadingMore.value = true
  const before = logs.value[logs.value.length - 1].id
  try {
    const page = await api.getTaskLogs({ ...applied, limit: PAGE, before })
    const known = new Set(logs.value.map((l) => l.id))
    const items = (page.items || []).filter((it) => !known.has(it.id))
    loadedOlder.value = true
    hasMore.value = page.has_more && items.length > 0
    logs.value = [...logs.value, ...items]
  } catch { /* 忽略，下次滚动重试 */ } finally {
    loadingMore.value = false
  }
}

// 实时刷新：日志按自动刷新开关轮询，任务列表始终轮询；标签页隐藏时暂停，恢复可见时立即刷新
function onVisible() {
  if (document.hidden) return
  if (autoRefresh.value) load()
  loadClocks()
}

onMounted(() => {
  load()
  loadClocks()
  timer = setInterval(() => {
    if (document.hidden) return
    if (autoRefresh.value) load()
    loadClocks()
  }, 4000)
  observer = new IntersectionObserver(
    (entries) => { if (entries.some((e) => e.isIntersecting)) loadMore() },
    { rootMargin: '300px' },
  )
  if (sentinel.value) observer.observe(sentinel.value)
  document.addEventListener('visibilitychange', onVisible)
  document.addEventListener('keydown', onKeydown)
})

onUnmounted(() => {
  clearInterval(timer)
  observer?.disconnect()
  document.removeEventListener('visibilitychange', onVisible)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<style scoped>
.form-label {
  display: block;
  font-size: 10px;
  letter-spacing: 0.15em;
  text-transform: uppercase;
  color: rgb(113 113 122);
  margin-bottom: 0.375rem;
}
.form-input {
  width: 100%;
  border: 1px solid rgb(212 212 216);
  border-radius: 0.375rem;
  padding: 0.5rem 0.75rem;
  font-size: 0.75rem;
  color: rgb(39 39 42);
  outline: none;
  transition: border-color 0.15s;
  background: white;
}
.form-input:focus {
  border-color: rgb(113 113 122);
}
.form-input:disabled {
  background: rgb(244 244 245);
  color: rgb(161 161 170);
}
</style>
