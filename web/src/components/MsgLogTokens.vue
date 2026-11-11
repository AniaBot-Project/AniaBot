<template>
  <template v-for="(t, i) in tokens" :key="i">
    <span v-if="t.type === 'text'">{{ t.text }}</span>
    <span v-else-if="t.type === 'at'" class="tok-at">{{ t.text }}</span>
    <span v-else-if="t.type === 'face'" class="tok-face">[{{ t.text }}]</span>
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
import { Icon } from 'animal-island-vue'

defineProps({
  tokens: { type: Array, default: () => [] },
})
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
