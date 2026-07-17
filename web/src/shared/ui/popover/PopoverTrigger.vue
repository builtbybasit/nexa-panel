<script setup lang="ts">
import { PopoverTrigger, type PopoverTriggerProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

const props = defineProps<PopoverTriggerProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <PopoverTrigger v-bind="forwarded" :class="cn(props.class)">
    <slot />
  </PopoverTrigger>
</template>
