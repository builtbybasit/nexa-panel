<script setup lang="ts">
import { CollapsibleContent, type CollapsibleContentProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Collapsible body. Smoothly animates its height off reka's
 * `--reka-collapsible-content-height` CSS var, keyed on the `data-state` the
 * primitive stamps on this element. No padding is imposed here — leave spacing
 * to the consumer's own content.
 */
const props = defineProps<CollapsibleContentProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <CollapsibleContent v-bind="forwarded" :class="cn('nexa-collapsible-content overflow-hidden', props.class)">
    <slot />
  </CollapsibleContent>
</template>

<style>
.nexa-collapsible-content[data-state='open'] {
  animation: nexa-collapsible-down 200ms ease-out;
}

.nexa-collapsible-content[data-state='closed'] {
  animation: nexa-collapsible-up 200ms ease-out;
}

@keyframes nexa-collapsible-down {
  from {
    height: 0;
  }
  to {
    height: var(--reka-collapsible-content-height);
  }
}

@keyframes nexa-collapsible-up {
  from {
    height: var(--reka-collapsible-content-height);
  }
  to {
    height: 0;
  }
}
</style>
