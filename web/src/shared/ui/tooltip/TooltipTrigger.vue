<script setup lang="ts">
import { TooltipTrigger, type TooltipTriggerProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Element that opens the tooltip on hover/focus. Pass `as-child` to project the
 * trigger behaviour onto your own element (button, icon, etc.).
 */
const props = defineProps<TooltipTriggerProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <TooltipTrigger v-bind="forwarded" :class="cn(props.class)">
    <slot />
  </TooltipTrigger>
</template>
