<script setup lang="ts">
import AppAlert from './AppAlert.vue'

defineProps<{ steps: string[]; warnings?: string[] }>()
</script>

<template>
  <div class="space-y-3">
    <AppAlert v-for="warning in warnings ?? []" :key="warning" tone="warning">{{ warning }}</AppAlert>
    <ol class="space-y-1.5">
      <li
        v-for="(step, index) in steps"
        :key="step"
        class="flex items-start gap-3 rounded-lg border border-outline bg-canvas/40 px-3 py-2 text-[13px] leading-relaxed text-ink-secondary"
      >
        <span class="mt-px shrink-0 font-mono text-xs font-bold text-accent-400">{{ String(index + 1).padStart(2, '0') }}</span>
        <span class="min-w-0">{{ step }}</span>
      </li>
    </ol>
  </div>
</template>
