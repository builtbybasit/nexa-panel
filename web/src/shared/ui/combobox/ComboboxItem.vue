<script setup lang="ts">
import {
  ComboboxItem,
  type ComboboxItemEmits,
  type ComboboxItemProps,
  useForwardPropsEmits,
} from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Result row for the floating select. Forwards props + emits (including
 * `@select`); the highlight comes from `data-[highlighted]`. Drop a
 * `ComboboxItemIndicator` inside to show a checkmark on the current value.
 */
const props = defineProps<ComboboxItemProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<ComboboxItemEmits>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <ComboboxItem
    v-bind="forwarded"
    :class="
      cn(
        'relative flex min-h-9 cursor-pointer scroll-my-1.5 select-none items-center gap-2.5 rounded-lg px-3 py-2 text-[13.5px] font-medium text-ink-secondary outline-none transition-colors',
        'data-[highlighted]:bg-accent-400/[0.08] data-[highlighted]:text-ink data-[disabled]:pointer-events-none data-[disabled]:opacity-40',
        props.class,
      )
    "
  >
    <slot />
  </ComboboxItem>
</template>
