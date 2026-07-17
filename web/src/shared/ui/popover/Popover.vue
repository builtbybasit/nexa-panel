<script setup lang="ts">
import { PopoverRoot, type PopoverRootEmits, type PopoverRootProps, useForwardPropsEmits } from 'reka-ui'

/**
 * Root of the popover family. Forwards props + emits so `v-model:open` works and
 * a parent can force the panel closed (e.g. after a successful action).
 */
const props = defineProps<PopoverRootProps>()
const emits = defineEmits<PopoverRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <PopoverRoot v-bind="forwarded">
    <slot />
  </PopoverRoot>
</template>
