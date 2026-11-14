<template>
  <template v-for="(t, i) in tokens" :key="i">
    <span v-if="t.type === 'text'">{{ t.text }}</span>
    <span v-else-if="t.type === 'at'" class="tok-at">{{ t.text }}</span>
    <span v-else-if="t.type === 'face'" class="tok-face">[{{ t.text }}]</span>
    <!-- 合并转发节点里的真实图片：缩略图独占一行，点击放大（地址失效可重试） -->
    <button
      v-else-if="t.type === 'image' && t.media"
      type="button"
      class="tok-image"
      :class="{ 'is-broken': imageFailed(t.media) }"
      :title="imageFailed(t.media) ? '加载失败，点击重试' : '点击查看大图'"
      @click="onImageClick(t.media)"
    >
      <img
        v-if="!imageFailed(t.media)"
        :src="imageSrc(t.media)"
        alt="图片"
        loading="lazy"
        referrerpolicy="no-referrer"
        @load="imageLoaded()"
        @error="imageError(t.media)"
      />
      <span v-else class="tok-image-broken">
        <Icon name="Image" :size="14" />
        <span>图片已失效</span>
        <small>点击重试</small>
      </span>
    </button>
    <span v-else-if="t.type === 'image-placeholder'" class="tok-media">
      <Icon name="Image" :size="12" />
      {{ t.text }}
    </span>
    <span v-else-if="t.type === 'file'" class="tok-media">
      <Icon name="Paperclip" :size="12" />
      {{ t.text }}
    </span>
    <span v-else-if="t.type === 'record'" class="tok-media">
      <Icon name="Mic" :size="12" />
      {{ t.text }}
    </span>
    <a
      v-else-if="t.type === 'card'"
      class="tok-card"
      :href="t.url || undefined"
      :target="t.url ? '_blank' : undefined"
      rel="noopener noreferrer"
      @click="t.url ? null : $event.preventDefault()"
    >
      <span class="tok-card-title">{{ t.title }}</span>
      <span v-if="t.desc" class="tok-card-desc">{{ t.desc }}</span>
    </a>
  </template>
</template>

<script setup>
import { inject } from 'vue'
import { Icon } from 'animal-island-vue'

defineProps({
  tokens: { type: Array, default: () => [] },
})

// 媒体状态（地址续签 / 失败重试 / 点击放大）由 MsgLogBubble 提供，转发卡片里的
// 图片与气泡底部的图片共用同一份状态；独立使用本组件时（没有 provider）只渲染
// 占位符，不会报错。
const mediaApi = inject('msglogMedia', null)

function imageSrc(media) {
  return mediaApi ? mediaApi.src(media) : ''
}
function imageFailed(media) {
  return mediaApi ? mediaApi.failed(media) : true
}
function imageError(media) {
  if (mediaApi) mediaApi.onError(media)
}
function imageLoaded() {
  if (mediaApi) mediaApi.loaded()
}
function onImageClick(media) {
  if (!mediaApi) return
  if (mediaApi.failed(media)) mediaApi.retry(media)
  else mediaApi.preview(media)
}
</script>

<style scoped>
.tok-at {
  display: inline;
  color: var(--accent-strong);
  font-weight: 700;
  background: var(--accent-tint);
  border-radius: 5px;
  padding: 0 3px;
}
.tok-face {
  display: inline-block;
  margin: 0 1px;
  padding: 0 5px;
  border-radius: 6px;
  background: var(--surface-3);
  border: 1px solid var(--line-strong);
  color: var(--ink-soft);
  font-size: 12px;
  line-height: 18px;
}
.tok-media {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0 6px;
  border-radius: 6px;
  background: var(--surface-2);
  border: 1px dashed var(--line-strong);
  color: var(--ink-soft);
  font-size: 12px;
  line-height: 19px;
  vertical-align: 1px;
}
/* 合并转发卡片里的图片：缩略图独占一行，点击放大 */
.tok-image {
  display: flex;
  width: fit-content;
  align-items: center;
  justify-content: center;
  margin: 3px 0 2px;
  padding: 0;
  max-width: 100%;
  border: 2px solid var(--line);
  border-radius: 9px;
  background: var(--surface-2);
  overflow: hidden;
  cursor: zoom-in;
  font: inherit;
  line-height: 0;
}
.tok-image:hover {
  border-color: var(--accent);
}
.tok-image img {
  display: block;
  width: auto;
  height: auto;
  max-width: min(168px, 100%);
  max-height: 120px;
  object-fit: cover;
}
.tok-image.is-broken {
  border-style: dashed;
  cursor: pointer;
}
.tok-image-broken {
  min-width: 96px;
  min-height: 60px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  padding: 8px 10px;
  color: var(--ink-muted);
  font-size: 10.5px;
  line-height: 1.3;
  white-space: normal;
}
.tok-image-broken small {
  font-size: 9.5px;
  color: var(--ink-disabled);
}
.tok-card {
  display: block;
  margin: 4px 0;
  padding: 8px 11px;
  border-radius: 12px;
  background: var(--surface-2);
  border: 1.5px solid var(--line-strong);
  text-decoration: none;
  color: inherit;
}
.tok-card:hover {
  border-color: var(--accent);
}
.tok-card-title {
  display: block;
  font-size: 12.5px;
  font-weight: 700;
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}
.tok-card-desc {
  display: block;
  margin-top: 2px;
  font-size: 11.5px;
  color: var(--ink-soft);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
