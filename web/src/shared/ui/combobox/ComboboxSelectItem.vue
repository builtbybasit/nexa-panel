<script setup lang="ts">
import { ComboboxItem, type ComboboxItemEmits, type ComboboxItemProps, ComboboxItemIndicator, useForwardPropsEmits } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'

/**
 * Result row for a combobox used as a floating select (a checkmark tracks
 * the current value). The plain `ComboboxItem` stays checkmark-less for the
 * command palette, which treats every row as a navigation action rather than
 * a selectable value.
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
        'relative flex min-h-9 cursor-pointer scroll-my-1.5 items-center gap-2.5 rounded-lg py-2 pr-3 pl-8 text-[13.5px] font-medium text-ink-secondary outline-none transition-colors',
        'data-[highlighted]:bg-accent-400/[0.08] data-[highlighted]:text-ink data-[disabled]:pointer-events-none data-[disabled]:opacity-40',
        props.class,
      )
    "
  >
    <ComboboxItemIndicator class="absolute left-2.5 inline-flex items-center">
      <AppIcon name="check" :size="14" class="text-accent-400" />
    </ComboboxItemIndicator>
    <slot />
  </ComboboxItem>
</template>
