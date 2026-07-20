<script setup lang="ts">
import { RouterLink } from 'vue-router'

import AppIcon from './AppIcon.vue'

defineProps<{
  label: string
  icon: string
  to: string
  /** Optional one-line description shown under the label. */
  description?: string
}>()
</script>

<template>
  <RouterLink
    :to="to"
    class="group relative flex flex-col items-center gap-2.5 rounded-2xl border border-outline bg-surface/80 p-4 text-center transition-colors hover:border-outline-strong hover:bg-white/[0.04] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-400"
  >
    <span
      class="grid size-11 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300 transition-colors group-hover:border-accent-400/30 group-hover:text-accent-200"
    >
      <AppIcon :name="icon" :size="20" />
    </span>
    <span class="min-w-0">
      <span class="block truncate text-[13px] font-semibold text-ink">{{ label }}</span>
      <span v-if="description" class="mt-0.5 block truncate text-[11px] text-ink-muted">{{ description }}</span>
    </span>
  </RouterLink>
</template>
