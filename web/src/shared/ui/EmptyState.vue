<script setup lang="ts">
import AppIcon from './AppIcon.vue'

defineProps<{ icon?: string; title: string; description?: string }>()
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed border-outline-strong px-6 py-10 text-center">
    <span
      v-if="icon"
      class="mb-1 grid size-10 place-items-center rounded-xl border border-outline bg-white/[0.03] text-ink-muted"
    >
      <AppIcon :name="icon" :size="18" />
    </span>
    <p class="text-sm font-semibold text-ink">{{ title }}</p>
    <p v-if="description" class="max-w-sm text-[13px] leading-relaxed text-ink-muted">{{ description }}</p>
    <div v-if="$slots.action" class="mt-2 flex flex-wrap items-center justify-center gap-2">
      <slot name="action" />
    </div>
    <slot />
  </div>
</template>
