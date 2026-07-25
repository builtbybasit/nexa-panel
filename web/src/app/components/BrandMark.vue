<script setup lang="ts">
withDefaults(defineProps<{ subtitle?: string; size?: 'md' | 'lg' }>(), {
  subtitle: 'Server control panel',
  size: 'md',
})
</script>

<template>
  <span class="flex items-center gap-3">
    <span
      class="grid shrink-0 place-items-center rounded-xl bg-gradient-to-br from-accent-300 to-accent-600 font-black text-accent-950 shadow-[0_10px_28px_-6px] shadow-accent-500/40"
      :class="size === 'lg' ? 'size-12 text-xl' : 'size-10 text-lg'"
    >
      N
    </span>
    <span class="min-w-0">
      <strong class="block text-[15px] font-bold tracking-tight text-ink">Nexa Panel</strong>
      <small class="block truncate text-[11px] text-ink-muted">{{ subtitle }}</small>
    </span>
  </span>
</template>
