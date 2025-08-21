<template>
  <div>
    <input-text 
      v-model="messageText" 
      placeholder="Черкани сида..." 
      @keyup.enter="sendMessage"
      :disabled="!chatStore.activeContact"
    />
    <button-send  
      @click="sendMessage" 
      :disabled="!messageText.trim() || !chatStore.activeContact"
    />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import { useChatStore } from '@/stores/chat.store';
import InputText from 'primevue/inputtext';
import ButtonSend from 'primevue/inputtext';

const chatStore = useChatStore();
const messageText = ref('');

const sendMessage = () => {
  if(messageText.value.trim() && chatStore.activeContact) {
    chatStore.sendMessage(messageText.value);
    messageText.value = '';
  }
};
</script>