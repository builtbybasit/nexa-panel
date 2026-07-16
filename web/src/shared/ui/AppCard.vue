<script setup lang="ts">
defineProps<{
  title?: string
  eyebrow?: string
  description?: string
  flush?: boolean
}>()
</script>

<template>
  <article class="rounded-2xl border border-outline bg-surface/80" :class="flush ? '' : 'p-5 sm:p-6'">
    <header v-if="title || eyebrow || $slots.actions" class="mb-4 flex flex-wrap items-start justify-between gap-3" :class="flush ? 'px-5 pt-5 sm:px-6 sm:pt-6' : ''">
      <div class="min-w-0">
        <p v-if="eyebrow" class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">{{ eyebrow }}</p>
        <h3 v-if="title" class="mt-0.5 text-[15px] font-semibold text-ink">{{ title }}</h3>
        <p v-if="description" class="mt-1 text-[13px] leading-relaxed text-ink-secondary">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="flex shrink-0 items-center gap-2">
        <slot name="actions" />
      </div>
    </header>
    <slot />
  </article>
</template>
