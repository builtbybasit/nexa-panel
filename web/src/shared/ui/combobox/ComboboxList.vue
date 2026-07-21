<script setup lang="ts">
import {
  ComboboxContent,
  type ComboboxContentEmits,
  type ComboboxContentProps,
  ComboboxPortal,
  useForwardPropsEmits,
} from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Popper-positioned, portaled results panel. Place a `ComboboxInput`,
 * `ComboboxEmpty`, and one or more `ComboboxGroup`/`ComboboxItem` inside it; the
 * inner region scrolls while matching reka's filtering against each item's
 * `text-value`.
 */
const props = withDefaults(defineProps<ComboboxContentProps & { class?: HTMLAttributes['class'] }>(), {
  position: 'popper',
  sideOffset: 6,
})
const emits = defineEmits<ComboboxContentEmits>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <ComboboxPortal>
    <ComboboxContent
      v-bind="forwarded"
      :class="
        cn(
          'relative z-50 w-[var(--reka-combobox-trigger-width)] overflow-hidden rounded-xl border border-outline-strong bg-raised text-ink shadow-[0_16px_40px_-16px_rgba(0,0,0,0.7)] outline-none',
          'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95 data-[side=bottom]:slide-in-from-top-2 data-[side=top]:slide-in-from-bottom-2',
          props.class,
        )
      "
    >
      <div class="max-h-[min(20rem,var(--reka-combobox-content-available-height))] overflow-x-hidden overflow-y-auto">
        <slot />
      </div>
    </ComboboxContent>
  </ComboboxPortal>
</template>
