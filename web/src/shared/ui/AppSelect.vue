<script setup lang="ts">
import { Comment, Fragment, useSlots, type VNode } from 'vue'

const model = defineModel<string | number>()

defineProps<{
  invalid?: boolean
  /** Disabled explanatory option shown when the caller renders no options. */
  emptyMessage?: string
}>()

defineOptions({ inheritAttrs: false })

const slots = useSlots()

function hasRenderedOptions(nodes: VNode[] | undefined): boolean {
  return (nodes ?? []).some((node) =>
    node.type === Fragment ? hasRenderedOptions(node.children as VNode[]) : node.type !== Comment,
  )
}

// Called during render so it re-evaluates whenever the slot content changes.
function slotIsEmpty(): boolean {
  return !hasRenderedOptions(slots.default?.())
}
</script>

<template>
  <div class="relative">
    <select
      v-bind="$attrs"
      v-model="model"
      :aria-invalid="invalid || undefined"
      class="h-10 w-full appearance-none rounded-lg border bg-canvas/60 px-3 pr-9 text-sm text-ink transition-colors focus:outline-none disabled:opacity-50 [&>option]:bg-raised"
      :class="invalid ? 'border-rose-500/60 focus:border-rose-400' : 'border-outline-strong focus:border-accent-500'"
    >
      <option v-if="emptyMessage && slotIsEmpty()" value="" disabled>{{ emptyMessage }}</option>
      <slot />
    </select>
    <svg
      class="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-ink-muted"
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <polyline points="6 9 12 15 18 9" />
    </svg>
  </div>
</template>
