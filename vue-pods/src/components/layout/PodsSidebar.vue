<template>
  <aside>
    <h2>Контакты</h2>
    <div
      v-for="contact in chatStore.contacts"
      :key="contact.id"
      @click="selectContact(contact)"
    >
      <div>
        <span>{{ contact.name }}</span>
        <span>{{ contact.isOnline ? 'online' : 'offline' }}</span>
      </div>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { useChatStore } from '@/stores/chat.store';

const chatStore = useChatStore();

const selectContact = (contact: any) => {
  chatStore.setActiveContact(contact);
};
</script>