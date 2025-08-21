<template>
  <header>
    <div v-if="chatStore.activeContact">
      <h2>{{ chatStore.activeContact?.name }}</h2>
      <span>{{ chatStore.activeContact.isOnline ? 'online' : 'offline' }}</span>
    </div>
    <div v-else>
      <h2>Выбирите челика</h2>
    </div>
  </header>
</template>

<script setup lang="ts">
import { useChatStore } from '@/stores/chat.store';

const chatStore = useChatStore();
</script>