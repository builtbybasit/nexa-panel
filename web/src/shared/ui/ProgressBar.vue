<script setup lang="ts">
withDefaults(defineProps<{ value: number; tone?: 'accent' | 'warning' | 'danger' }>(), { tone: 'accent' })

const toneClasses: Record<'accent' | 'warning' | 'danger', string> = {
  accent: 'bg-accent-400',
  warning: 'bg-amber-400',
  danger: 'bg-rose-400',
}
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
      :class="toneClasses[tone]"
      :style="{ width: `${Math.min(100, Math.max(0, value))}%` }"
    />
  </div>
</template>
