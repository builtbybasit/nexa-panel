<script setup lang="ts">
import {
  ComboboxContent,
  type ComboboxContentEmits,
  type ComboboxContentProps,
  useForwardPropsEmits,
} from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Styled results container. Uses the default `inline` position so it flows
 * inside its own layout (here, inside a Dialog) rather than as a popper. Pass
 * `ignore-filter` on the root so every rendered item stays navigable.
 */
const props = defineProps<ComboboxContentProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<ComboboxContentEmits>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <ComboboxContent
    v-bind="forwarded"
    :class="cn('max-h-[50vh] overflow-y-auto p-2', props.class)"
  >
    <slot />
  </ComboboxContent>
</template>
