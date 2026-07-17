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
 * Styled result row. Forwards props + emits (including `@select`). reka manages
 * the active item — the highlight comes from `data-[highlighted]`.
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
        'flex cursor-pointer select-none items-center gap-3 rounded-lg px-3 py-2 text-[13.5px] font-medium text-ink-secondary outline-none',
        'data-[highlighted]:bg-accent-400/[0.08] data-[highlighted]:text-ink',
        props.class,
      )
    "
  >
    <slot />
  </ComboboxItem>
</template>
