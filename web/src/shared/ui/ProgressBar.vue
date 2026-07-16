<script setup lang="ts">
withDefaults(defineProps<{ value: number; tone?: 'accent' | 'danger' }>(), { tone: 'accent' })
</script>

<template>
  <div
    class="h-1.5 overflow-hidden rounded-full bg-white/[0.06]"
    role="progressbar"
    :aria-valuenow="value"
    aria-valuemin="0"
    aria-valuemax="100"
  >
    <div
      class="h-full rounded-full transition-[width] duration-300"
      :class="tone === 'danger' ? 'bg-rose-400' : 'bg-accent-400'"
      :style="{ width: `${Math.min(100, Math.max(0, value))}%` }"
    />
  </div>
</template>
