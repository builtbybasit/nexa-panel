<script setup lang="ts">
import { ListboxFilter, type ListboxFilterProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'
import { useCommand } from './context'

/** Search box that drives the command menu's filtering. Use `#suffix` for a trailing hint. */
defineOptions({ inheritAttrs: false })

const props = defineProps<ListboxFilterProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)

const { filterState } = useCommand()
</script>

<template>
  <div class="flex items-center gap-3 border-b border-outline px-4">
    <AppIcon name="search" :size="16" class="shrink-0 text-ink-muted" />
    <ListboxFilter
      v-bind="{ ...forwarded, ...$attrs }"
      v-model="filterState.search"
      auto-focus
      :class="
        cn(
          'h-12 w-full bg-transparent text-sm text-ink outline-none placeholder:text-ink-muted disabled:cursor-not-allowed disabled:opacity-50',
          props.class,
        )
      "
    />
    <slot name="suffix" />
  </div>
</template>
