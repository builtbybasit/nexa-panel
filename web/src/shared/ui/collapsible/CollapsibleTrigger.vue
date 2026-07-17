<script setup lang="ts">
import { CollapsibleTrigger, type CollapsibleTriggerProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Trigger that toggles the collapsible. Consumers typically pass `as-child` and
 * provide their own button; the primitive stamps `data-state="open|closed"` on
 * that element so a nested chevron can rotate via `data-[state=open]:rotate-180`.
 */
const props = defineProps<CollapsibleTriggerProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <CollapsibleTrigger v-bind="forwarded" :class="cn(props.class)">
    <slot />
  </CollapsibleTrigger>
</template>
