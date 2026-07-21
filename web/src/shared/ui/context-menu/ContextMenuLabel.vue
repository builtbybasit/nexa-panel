<script setup lang="ts">
import { ContextMenuLabel, type ContextMenuLabelProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

const props = defineProps<ContextMenuLabelProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})
</script>

<template>
  <ContextMenuLabel
    v-bind="delegatedProps"
    :class="cn('truncate px-3 py-1.5 text-[11px] font-semibold tracking-[0.05em] text-ink-muted', props.class)"
  >
    <slot />
  </ContextMenuLabel>
</template>
