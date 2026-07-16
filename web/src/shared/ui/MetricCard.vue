<script setup lang="ts">
import AppIcon from './AppIcon.vue'

defineProps<{ label: string; value: string | number; detail?: string; icon?: string }>()
</script>

<template>
  <article class="rounded-2xl border border-outline bg-surface/80 p-5">
    <div class="flex items-center justify-between gap-3">
      <p class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">{{ label }}</p>
      <span v-if="icon" class="grid size-8 place-items-center rounded-lg border border-outline bg-white/[0.03] text-accent-300">
        <AppIcon :name="icon" :size="15" />
      </span>
    </div>
    <p class="mt-2 text-2xl font-semibold tracking-tight text-ink">{{ value }}</p>
    <p v-if="detail" class="mt-1 text-[13px] text-ink-secondary">{{ detail }}</p>
    <slot />
  </article>
</template>
