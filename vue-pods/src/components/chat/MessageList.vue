<template>
  <div ref="messagesContainer">
    <message-one
      v-for="message in chatStore.activeChatMessages"
      :key="message.id"
      :message="message"
      :is-own="message.userId == chatStore.currentUser.id"
    />
  </div>
</template>

<script setup lang="ts">
import { watch, ref, nextTick } from 'vue';
import { useChatStore } from '@/stores/chat.store';
import MessageOne from './MessageOne.vue';

const chatStore = useChatStore();
const messagesContainer = ref<HTMLElement>();

// Автопрокрутка к новому сообщению
watch(() => chatStore.activeChatMessages.length, async () => {
  await nextTick();
  if (messagesContainer.value) {
    messagesContainer.value.scrollTop = messagesContainer.value.scrollHeight;
  }
}, { immediate: true });
</script>