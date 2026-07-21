<script setup lang="ts">
import { ListboxContent, type ListboxContentProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

const props = defineProps<ListboxContentProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <ListboxContent
    v-bind="forwarded"
    :class="cn('max-h-[min(60vh,24rem)] scroll-py-2 overflow-x-hidden overflow-y-auto p-2', props.class)"
  >
    <div role="presentation">
      <slot />
    </div>
  </ListboxContent>
</template>
