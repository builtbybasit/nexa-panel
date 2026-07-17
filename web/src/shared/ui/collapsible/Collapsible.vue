<script setup lang="ts">
import { CollapsibleRoot, type CollapsibleRootEmits, type CollapsibleRootProps, useForwardPropsEmits } from 'reka-ui'

/**
 * Root of the collapsible family. Forwards props (`open`, `defaultOpen`,
 * `disabled`) + emits (`update:open`) so `v-model:open` works and a parent can
 * drive the open state programmatically.
 */
const props = defineProps<CollapsibleRootProps>()
const emits = defineEmits<CollapsibleRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <CollapsibleRoot v-bind="forwarded">
    <slot />
  </CollapsibleRoot>
</template>
