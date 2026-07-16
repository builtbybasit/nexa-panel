<script setup lang="ts">
import type { JobEvent } from '@/modules/jobs/api'

import ProgressBar from './ProgressBar.vue'

defineProps<{ event: JobEvent }>()
</script>

<template>
  <div class="rounded-xl border border-outline bg-raised/60 px-4 py-3">
    <div class="mb-2 flex items-center justify-between gap-3 text-[13px]">
      <span class="min-w-0 truncate text-ink-secondary">{{ event.message }}</span>
      <strong class="font-mono text-xs text-ink">{{ event.progress }}%</strong>
    </div>
    <ProgressBar :value="event.progress" :tone="event.state === 'failed' ? 'danger' : 'accent'" />
  </div>
</template>
