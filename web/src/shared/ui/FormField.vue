<script setup lang="ts">
defineProps<{
  label: string
  hint?: string
  error?: string
}>()
</script>

<template>
  <label class="block">
    <span class="mb-1.5 block text-[13px] font-medium text-ink-secondary">{{ label }}</span>
    <slot />
    <span v-if="error" role="alert" class="mt-1.5 block text-xs leading-relaxed text-rose-300">{{ error }}</span>
    <span v-else-if="hint" class="mt-1.5 block text-xs leading-relaxed text-ink-muted">{{ hint }}</span>
  </label>
</template>
