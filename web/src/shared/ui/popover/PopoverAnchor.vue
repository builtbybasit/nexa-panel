<script setup lang="ts">
import { PopoverAnchor, type PopoverAnchorProps, useForwardProps } from 'reka-ui'

/** Optional positioning anchor: passes props straight through to the primitive. */
const props = defineProps<PopoverAnchorProps>()

const forwarded = useForwardProps(props)
</script>

<template>
  <PopoverAnchor v-bind="forwarded">
    <slot />
  </PopoverAnchor>
</template>
