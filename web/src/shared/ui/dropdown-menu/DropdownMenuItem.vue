<script setup lang="ts">
import {
  DropdownMenuItem,
  type DropdownMenuItemEmits,
  type DropdownMenuItemProps,
  useForwardPropsEmits,
} from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * Styled menu item. Forwards props + emits (including `@select`). Pass
 * `as-child` to render a custom element such as a `RouterLink`.
 */
const props = defineProps<DropdownMenuItemProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<DropdownMenuItemEmits>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <DropdownMenuItem
    v-bind="forwarded"
    :class="
      cn(
        'relative flex min-h-9 cursor-pointer select-none items-center gap-2.5 rounded-lg px-3 text-[13px] font-medium text-ink-secondary outline-none transition-colors data-[highlighted]:bg-white/[0.04] data-[highlighted]:text-ink data-[disabled]:pointer-events-none data-[disabled]:opacity-40',
        props.class,
      )
    "
  >
    <slot />
  </DropdownMenuItem>
</template>
