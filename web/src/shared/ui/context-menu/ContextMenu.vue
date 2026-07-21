<script setup lang="ts">
import {
  ContextMenuRoot,
  type ContextMenuRootEmits,
  type ContextMenuRootProps,
  useForwardPropsEmits,
} from 'reka-ui'

/**
 * Root of the context-menu family (right-click / long-press menu). Forwards
 * props + emits so `v-model:open` works and a parent can force it closed.
 */
const props = defineProps<ContextMenuRootProps>()
const emits = defineEmits<ContextMenuRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <ContextMenuRoot v-bind="forwarded">
    <slot />
  </ContextMenuRoot>
</template>
