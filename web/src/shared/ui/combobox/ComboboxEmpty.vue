<script setup lang="ts">
import { ComboboxEmpty, type ComboboxEmptyProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

const props = defineProps<ComboboxEmptyProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})
</script>

<template>
  <ComboboxEmpty v-bind="delegatedProps" :class="cn('px-3 py-6 text-center text-[13px] text-ink-muted', props.class)">
    <slot />
  </ComboboxEmpty>
</template>
