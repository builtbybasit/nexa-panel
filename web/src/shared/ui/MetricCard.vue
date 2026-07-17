<script setup lang="ts">
import { RouterLink } from 'vue-router'

import AppIcon from './AppIcon.vue'

withDefaults(
  defineProps<{
    label: string
    value: string | number
    detail?: string
    icon?: string
    /** When set, the whole card becomes a link to this route. */
    to?: string
    tone?: 'default' | 'warning' | 'danger'
  }>(),
  { tone: 'default' },
)

const detailClasses: Record<'default' | 'warning' | 'danger', string> = {
  default: 'text-ink-secondary',
  warning: 'text-amber-300',
  danger: 'text-rose-300',
}
</script>

<template>
  <component
    :is="to ? RouterLink : 'article'"
    :to="to"
    class="block rounded-2xl border border-outline bg-surface/80 p-5"
    :class="
      to
        ? 'transition-shadow hover:ring-2 hover:ring-accent-400/30 focus-visible:ring-2 focus-visible:ring-accent-400/50 focus:outline-none'
        : ''
    "
  >
    <div class="flex items-center justify-between gap-3">
      <p class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">{{ label }}</p>
      <span v-if="icon" class="grid size-8 place-items-center rounded-lg border border-outline bg-white/[0.03] text-accent-300">
        <AppIcon :name="icon" :size="15" />
      </span>
    </div>
    <p class="mt-2 text-2xl font-semibold tracking-tight text-ink">{{ value }}</p>
    <p v-if="detail" class="mt-1 text-[13px]" :class="detailClasses[tone]">{{ detail }}</p>
    <slot />
  </component>
</template>
