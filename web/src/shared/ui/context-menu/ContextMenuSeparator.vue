<script setup lang="ts">
import { ContextMenuSeparator, type ContextMenuSeparatorProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

const props = defineProps<ContextMenuSeparatorProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})
</script>

<template>
  <ContextMenuSeparator v-bind="delegatedProps" :class="cn('my-1 h-px bg-outline', props.class)" />
</template>
