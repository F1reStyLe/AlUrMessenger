<template>
  <div>
    <chat-header />
    <message-list />
    <message-input />
  </div>
</template>

<script setup lang="ts">
import ChatHeader from '@/components/chat/ChatHeader.vue';
import MessageList from '@/components/chat/MessageList.vue';
import MessageInput from '@/components/chat/MessageInput.vue';
</script>