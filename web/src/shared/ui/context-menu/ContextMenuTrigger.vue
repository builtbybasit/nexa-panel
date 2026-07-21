<script setup lang="ts">
import { ContextMenuTrigger, type ContextMenuTriggerProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * The area that opens the menu on right-click (or long-press on touch). Pass
 * `as-child` to make an existing element such as a table row the trigger.
 */
const props = defineProps<ContextMenuTriggerProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <ContextMenuTrigger v-bind="forwarded" :class="cn(props.class)">
    <slot />
  </ContextMenuTrigger>
</template>
