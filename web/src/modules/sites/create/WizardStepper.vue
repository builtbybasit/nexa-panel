<script setup lang="ts">
import { AppIcon } from '@/shared/ui'

const props = defineProps<{
  steps: string[]
  /** Index of the active step; earlier steps render as completed. */
  current: number
}>()

function circleClass(index: number): string {
  if (index < props.current) return 'border-accent-400/40 bg-accent-500/15 text-accent-200'
  if (index === props.current) return 'border-accent-400 bg-accent-500 text-accent-950'
  return 'border-outline bg-white/[0.02] text-ink-muted'
}

function labelClass(index: number): string {
  if (index === props.current) return 'text-ink'
  if (index < props.current) return 'text-ink-secondary'
  return 'text-ink-muted'
}
</script>

<template>
  <ol class="flex items-center justify-between gap-1 sm:gap-3" aria-label="Progress">
    <li v-for="(step, i) in steps" :key="step" class="flex flex-1 items-center gap-2 last:flex-none sm:gap-3">
      <div class="flex items-center gap-2.5">
        <span
          class="grid size-8 shrink-0 place-items-center rounded-full border text-[13px] font-semibold transition-colors"
          :class="circleClass(i)"
        >
          <AppIcon v-if="i < current" name="check" :size="16" />
          <span v-else>{{ i + 1 }}</span>
        </span>
        <span class="hidden text-[13px] font-medium sm:inline" :class="labelClass(i)">{{ step }}</span>
      </div>
      <span
        v-if="i < steps.length - 1"
        class="h-px flex-1 rounded-full transition-colors sm:min-w-6"
        :class="i < current ? 'bg-accent-400/50' : 'bg-outline'"
        aria-hidden="true"
      />
    </li>
  </ol>
</template>
