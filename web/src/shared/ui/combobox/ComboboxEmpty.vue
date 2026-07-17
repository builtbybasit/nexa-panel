<script setup lang="ts">
import { ComboboxEmpty, type ComboboxEmptyProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

const props = defineProps<ComboboxEmptyProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <ComboboxEmpty v-bind="forwarded" :class="cn('p-3', props.class)">
    <slot />
  </ComboboxEmpty>
</template>
