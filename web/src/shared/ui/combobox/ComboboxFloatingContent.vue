<script setup lang="ts">
import {
  ComboboxContent,
  type ComboboxContentEmits,
  type ComboboxContentProps,
  ComboboxPortal,
  ComboboxViewport,
  useForwardPropsEmits,
} from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Popper-positioned, portaled results panel for a combobox used as a
 * floating select (anchored to a `ComboboxAnchor` trigger, elsewhere in a
 * dialog's overflow). The plain `ComboboxContent` stays `inline` for the
 * command palette, which lays itself out inside its own dialog.
 */
const props = withDefaults(defineProps<ComboboxContentProps & { class?: HTMLAttributes['class'] }>(), {
  position: 'popper',
  sideOffset: 8,
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
          'relative z-50 max-h-[min(20rem,var(--reka-combobox-content-available-height))] w-[var(--reka-combobox-trigger-width)] overflow-hidden rounded-xl border border-outline-strong bg-raised shadow-[0_16px_40px_-16px_rgba(0,0,0,0.7)] outline-none',
          'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95 data-[side=bottom]:slide-in-from-top-2 data-[side=top]:slide-in-from-bottom-2',
          props.class,
        )
      "
    >
      <ComboboxViewport class="max-h-80 overflow-y-auto p-1.5">
        <slot />
      </ComboboxViewport>
    </ComboboxContent>
  </ComboboxPortal>
</template>
