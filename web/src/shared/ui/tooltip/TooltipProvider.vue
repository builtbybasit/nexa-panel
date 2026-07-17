<script setup lang="ts">
import { TooltipProvider, type TooltipProviderProps, useForwardProps } from 'reka-ui'

/**
 * Provider for the tooltip family. Mount this ONCE at the app root so every
 * `<Tooltip>` shares delay/skip timing. Forwards props like `delayDuration`,
 * `skipDelayDuration` and `disableHoverableContent`.
 */
const props = defineProps<TooltipProviderProps>()

const forwarded = useForwardProps(props)
</script>

<template>
  <TooltipProvider v-bind="forwarded">
    <slot />
  </TooltipProvider>
</template>
