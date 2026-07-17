<script setup lang="ts">
import { RouterLink } from 'vue-router'

import AppAlert from './AppAlert.vue'

defineProps<{ message: string; jobId?: number }>()
</script>

<template>
  <AppAlert tone="danger">
    <p>{{ message }}</p>
    <RouterLink
      v-if="jobId !== undefined"
      :to="`/jobs?job=${jobId}`"
      class="mt-1 inline-block font-medium text-rose-200 underline underline-offset-2 hover:text-rose-100"
    >
      View job #{{ jobId }} →
    </RouterLink>
  </AppAlert>
</template>
