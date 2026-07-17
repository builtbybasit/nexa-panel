<script setup lang="ts">
import { ComboboxLabel, type ComboboxLabelProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/** Group heading, styled to match the palette's section labels. */
const props = defineProps<ComboboxLabelProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})
</script>

<template>
  <ComboboxLabel
    v-bind="delegatedProps"
    :class="cn('px-3 pt-2 pb-1 text-[10px] font-bold tracking-[0.14em] text-ink-muted uppercase', props.class)"
  >
    <slot />
  </ComboboxLabel>
</template>
