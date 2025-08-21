<template>
  <div :class="isOwn ? 'align-self: flex-end' : 'align-self: flex-start'">
    <p>{{ message.text }}</p>
    <span>{{ formatTime(message.timestamp) }}</span>
    <span v-if="isOwn">{{ message.status }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import type { Message } from '@/stores/chat.store';

defineProps<{ message: Message; isOwn: boolean;}>();

const formatTime = (date: Date) => {
  return date.toLocaleTimeString('ru-RU', {
    hour: '2-digit',
    minute: '2-digit'
  });
};
</script>