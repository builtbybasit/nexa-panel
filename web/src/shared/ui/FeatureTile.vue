<script setup lang="ts">
import { RouterLink } from 'vue-router'

import AppIcon from './AppIcon.vue'

const props = withDefaults(
  defineProps<{
    label: string
    icon: string
    /** Optional one-line description shown under the label. */
    description?: string
    /** When set (and not `soon`), the whole tile links to this route. */
    to?: string
    /** Render as a muted, non-interactive tile with a "Soon" badge. */
    soon?: boolean
    /** `danger` tints the icon for destructive launchers (Delete, Disable). */
    tone?: 'default' | 'danger'
  }>(),
  { tone: 'default' },
)

const emit = defineEmits<{ select: [] }>()

const tag = () => (props.soon ? 'div' : props.to ? RouterLink : 'button')

function onClick() {
  if (!props.soon && !props.to) emit('select')
}
</script>

<template>
  <component
    :is="tag()"
    :to="!soon && to ? to : undefined"
    :type="!soon && !to ? 'button' : undefined"
    :aria-disabled="soon ? 'true' : undefined"
    :tabindex="soon ? -1 : undefined"
    class="group relative flex flex-col items-center gap-2.5 rounded-2xl border border-outline bg-surface/80 p-4 text-center transition-colors"
    :class="
      soon
        ? 'cursor-default opacity-55'
        : 'hover:border-outline-strong hover:bg-white/[0.04] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-400'
    "
    @click="onClick"
  >
    <span
      v-if="soon"
      class="absolute right-2.5 top-2.5 rounded-full border border-outline bg-white/[0.04] px-1.5 py-0.5 text-[9px] font-bold tracking-[0.08em] text-ink-muted uppercase"
    >
      Soon
    </span>
    <span
      class="grid size-11 place-items-center rounded-xl border transition-colors"
      :class="
        tone === 'danger'
          ? 'border-rose-500/25 bg-rose-500/10 text-rose-300'
          : 'border-outline bg-white/[0.03] text-accent-300 group-hover:border-accent-400/30 group-hover:text-accent-200'
      "
    >
      <AppIcon :name="icon" :size="20" />
    </span>
    <span class="min-w-0">
      <span class="block truncate text-[13px] font-semibold text-ink">{{ label }}</span>
      <span v-if="description" class="mt-0.5 block truncate text-[11px] text-ink-muted">{{ description }}</span>
    </span>
  </component>
</template>
