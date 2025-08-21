<template>
  <div>
    <pods-sidebar />
    <main>
      <router-view />
    </main>
  </div>
</template>

<script setup lang="ts">
import PodsSidebar from './PodsSidebar.vue';
</script>