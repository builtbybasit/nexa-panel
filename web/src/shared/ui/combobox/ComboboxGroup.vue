<script setup lang="ts">
import { ComboboxGroup, type ComboboxGroupProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

const props = defineProps<ComboboxGroupProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <ComboboxGroup v-bind="forwarded" :class="cn(props.class)">
    <slot />
  </ComboboxGroup>
</template>
