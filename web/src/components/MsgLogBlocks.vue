<template>
  <!-- 消息正文块：普通文字 / 引用 / 合并转发。引用与合并转发里可能再嵌一层，
       因此本组件按层级递归渲染自己（Vue SFC 支持按文件名自引用）。 -->
  <template v-for="(b, i) in blocks" :key="'blk' + i">
    <div v-if="b.type === 'text'" class="msg-text">
      <MsgLogTokens :tokens="b.tokens" />
    </div>

    <blockquote v-else-if="b.type === 'quote'" class="msg-quote" :class="{ 'is-nested': depth > 0 }">
      <div class="quote-head">
        <span class="quote-bar" />
        <span class="quote-name">{{ b.sender || '引用消息' }}</span>
      </div>
      <div class="quote-body">
        <MsgLogBlocks :blocks="b.blocks" :depth="depth + 1" compact />
      </div>
    </blockquote>

    <!-- 引用里只放一行占位：引用区本身是限高的预览，展开卡片会被裁掉一半 -->
    <div v-else-if="b.type === 'forward' && compact" class="forward-chip">
      <span class="forward-ico">↪</span>
      <span>合并转发消息</span>
      <span class="forward-count">{{ b.count }} 条</span>
    </div>

    <details v-else-if="b.type === 'forward'" class="msg-forward" :class="{ 'is-nested': depth > 0 }">
      <summary>
        <span class="forward-ico">↪</span>
        <span>合并转发消息</span>
        <span class="forward-count">{{ b.count }} 条</span>
        <span class="forward-tip">展开</span>
      </summary>
      <div class="forward-body">
        <MsgLogBlocks v-if="b.blocks.length" :blocks="b.blocks" :depth="depth + 1" />
        <template v-for="(n, ni) in b.nodes" :key="'node' + ni">
          <div class="forward-line">
            <template v-if="n.name">
              <span class="forward-name" :title="n.id ? '用户 ID：' + shortId(n.id) : ''">{{ n.name }}</span>
              <span class="forward-colon">：</span>
            </template>
            <MsgLogTokens :tokens="n.tokens" />
          </div>
          <!-- 该节点自己转发的内容（多层嵌套时继续展开成下一层卡片） -->
          <div v-if="n.blocks.length" class="forward-children">
            <MsgLogBlocks :blocks="n.blocks" :depth="depth + 1" />
          </div>
        </template>
      </div>
    </details>
  </template>
</template>

<script setup>
import { shortId } from '../utils/messageText.js'
import MsgLogTokens from './MsgLogTokens.vue'

defineProps({
  blocks: { type: Array, default: () => [] },
  // 嵌套层级：合并转发 / 引用的子块逐层加一
  depth: { type: Number, default: 0 },
  // 紧凑模式（引用正文内）：合并转发只显示一行占位，不渲染可展开卡片
  compact: { type: Boolean, default: false },
})
</script>

<style scoped>
.msg-text {
  white-space: pre-wrap;
  word-break: break-word;
}
.msg-text + .msg-text {
  margin-top: 4px;
}

/* ---------- 引用 ---------- */
.msg-quote {
  margin: 3px 0 6px;
  padding: 7px 10px 7px 11px;
  border-radius: 11px;
  background: var(--surface-2);
  border: 1.5px solid var(--line);
  position: relative;
}
.msg-quote.is-nested {
  margin: 4px 0;
  padding: 6px 9px 6px 10px;
  border-radius: 9px;
}
.quote-head {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-bottom: 2px;
  font-size: 10.5px;
  color: var(--ink-soft);
  min-width: 0;
}
.quote-bar {
  width: 3px;
  height: 11px;
  border-radius: 3px;
  background: var(--accent);
  flex-shrink: 0;
}
.quote-name {
  font-weight: 700;
  color: #7c6547;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.quote-id {
  font-family: var(--font-mono);
  color: var(--ink-muted);
}
.quote-body {
  font-size: 12px;
  color: var(--ink-soft);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 132px;
  overflow: hidden;
}

/* ---------- 合并转发 ---------- */
/* 引用里的合并转发占位：一行放得下，不会被引用区的限高裁掉 */
.forward-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin: 2px 0;
  padding: 3px 9px;
  border-radius: 8px;
  background: var(--surface-2);
  border: 1.5px solid var(--line-strong);
  font-size: 11.5px;
  font-weight: 700;
  color: #6b5538;
}
.msg-forward {
  margin: 5px 0;
  border-radius: 12px;
  background: var(--surface-2);
  border: 1.5px solid var(--line-strong);
  overflow: hidden;
}
.msg-forward summary {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 8px 11px;
  cursor: pointer;
  font-size: 12px;
  font-weight: 700;
  color: #6b5538;
  list-style: none;
  user-select: none;
}
.msg-forward summary::-webkit-details-marker {
  display: none;
}
.msg-forward[open] summary {
  border-bottom: 1.5px dashed var(--line-strong);
}
.forward-ico {
  color: var(--accent-strong);
  font-weight: 800;
}
.forward-count {
  font-size: 10.5px;
  color: var(--ink-muted);
  font-weight: 600;
}
.forward-tip {
  margin-left: auto;
  font-size: 10px;
  color: var(--ink-muted);
  font-weight: 600;
}
.msg-forward[open] .forward-tip {
  opacity: 0;
}
.forward-body {
  padding: 8px 11px 10px;
  max-height: 300px;
  overflow-y: auto;
  font-size: 12px;
  line-height: 1.6;
  color: var(--ink-soft);
  white-space: pre-wrap;
  word-break: break-word;
}
/* 嵌套的合并转发 / 引用：更紧凑，并靠左内缩露出层级 */
.msg-forward.is-nested {
  margin: 4px 0 3px;
  border-radius: 10px;
  background: var(--surface);
  border-color: var(--line);
}
.msg-forward.is-nested summary {
  padding: 5px 9px;
  font-size: 11.5px;
}
.msg-forward.is-nested > .forward-body {
  padding: 6px 9px 8px;
  /* 嵌套卡片不再自己滚动：交给外层卡片统一滚动，避免「框中框」和多处滚动条 */
  max-height: none;
  overflow: visible;
}
.forward-line + .forward-line {
  margin-top: 2px;
}
.forward-children {
  margin: 3px 0 5px 7px;
  padding-left: 8px;
  border-left: 1.5px dashed var(--line-strong);
}
.forward-name {
  color: #7c6547;
  font-weight: 700;
}
</style>
