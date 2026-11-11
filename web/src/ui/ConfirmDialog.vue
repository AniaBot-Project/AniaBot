<template>
  <Modal
    :open="confirmState.open"
    :title="confirmState.title"
    variant="default"
    :width="440"
    :typewriter="false"
    :show-footer="false"
    @close="settleConfirm(false)"
  >
    <p class="text-sm leading-relaxed whitespace-pre-wrap text-[#725d42]">{{ confirmState.message }}</p>
    <div class="flex justify-end gap-2 pt-5">
      <Button type="text" @click="settleConfirm(false)">{{ confirmState.cancelText }}</Button>
      <Button type="primary" :danger="confirmState.danger" @click="settleConfirm(true)">
        {{ confirmState.confirmText }}
      </Button>
    </div>
  </Modal>
</template>

<script setup>
import { Button, Modal } from 'animal-island-vue'
import { confirmState, settleConfirm } from './confirm.js'
</script>
