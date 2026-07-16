<script setup lang="ts">
defineProps<{
  title: string
  eyebrow?: string
  description?: string
}>()
</script>

<template>
  <div class="flex flex-wrap items-end justify-between gap-4">
    <div class="min-w-0">
      <p v-if="eyebrow" class="text-[11px] font-bold tracking-[0.14em] text-accent-400 uppercase">{{ eyebrow }}</p>
      <h2 class="mt-1 text-xl font-semibold tracking-tight text-ink sm:text-2xl">{{ title }}</h2>
      <p v-if="description" class="mt-1.5 max-w-2xl text-sm leading-relaxed text-ink-secondary">{{ description }}</p>
    </div>
    <div v-if="$slots.default" class="flex shrink-0 flex-wrap items-center gap-2">
      <slot />
    </div>
  </div>
</template>
