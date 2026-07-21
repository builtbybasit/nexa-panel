<script setup lang="ts">
import { ComboboxItemIndicator, type ComboboxItemIndicatorProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'

/** Trailing checkmark that reka renders only for the selected item. */
const props = defineProps<ComboboxItemIndicatorProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <ComboboxItemIndicator v-bind="forwarded" :class="cn('ml-auto inline-flex items-center', props.class)">
    <slot>
      <AppIcon name="check" :size="14" class="text-accent-400" />
    </slot>
  </ComboboxItemIndicator>
</template>
