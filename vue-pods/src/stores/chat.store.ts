import { defineStore } from 'pinia';
import { ref, computed } from 'vue';

export interface User {
  id: string;
  name: string;
  avatar?: string;
  isOnline: boolean;
}

export interface Message {
  id: string | number;
  text: string;
  userId: string;
  timestamp: Date;
  status: 'отправка' | 'отправлено' | 'доставлено' | 'прочитано'
}

export const useChatStore = defineStore(
  'chat', () => {
    
    const currentUser = ref<User>({
      id: '1',
      name: 'popovev',
      isOnline: true
    });

    const contacts = ref<User[]>([
      { id: '2', name: 'ПЛеха', isOnline: true },
      { id: '3', name: 'Аегоис', isOnline: true },
      { id: '4', name: 'Владеатр', isOnline: false },
    ]);

    const messages = ref<Message[]>([]);
    const activeContact = ref<User | null>(contacts.value[0]);
    const isConnected = ref<boolean>(false);

    //выводим сообщения если айдишник сообщения равен моему или активному пользаку
    //сортировка по датавремя
    const activeChatMessages = computed(() => {
      if(!activeContact.value) return [];
      return messages.value.filter( msg => 
        msg.userId === activeContact.value?.id || msg.userId === currentUser.value.id
      ).sort((a, b) => a.timestamp.getTime() - b.timestamp.getTime());
    });

    const sendMessage = (text: string) => { //function sendMessage(text: string) {
      if (!text.trim() || !activeContact.value) return;

      const newMessage: Message = {
        id: Date.now(),
        text: text.trim(),
        userId: currentUser.value.id,
        timestamp: new Date(),
        status: 'отправка'
      };

      messages.value.push(newMessage);

      //типо с задержкой поменяем статус
      setTimeout(() => {
        const messageIndex = messages.value.findIndex(m => m.id === newMessage.id);
        if (messageIndex !== -1) {
          messages.value[messageIndex].status = 'отправлено';
        }
      }, 5000);
    };

    //сеттеры
    const receiveMessahe = (newMessage: Message) => {
      messages.value.push(newMessage);
    };

    const setActiveContact = (contact: User) => {
      activeContact.value = contact;
    };

    const setConnectionStatus = (status: boolean) => {
      isConnected.value = status;
    };

    return {
      //Свойства
      currentUser,
      contacts,
      messages,
      activeContact,
      isConnected,
      //геттеры
      activeChatMessages,
      //события
      sendMessage,
      receiveMessahe,
      setActiveContact,
      setConnectionStatus
    };
  }
);