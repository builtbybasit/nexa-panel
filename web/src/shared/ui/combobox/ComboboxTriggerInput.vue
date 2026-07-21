<script setup lang="ts">
import { ComboboxInput, type ComboboxInputEmits, type ComboboxInputProps, ComboboxTrigger, useForwardPropsEmits } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'
import ComboboxAnchor from './ComboboxAnchor.vue'

/**
 * The box for a floating select-combobox: an anchor housing a search input
 * (shows the selected item's label via `display-value` when closed) plus a
 * trailing chevron that also toggles the panel, styled to match `SelectTrigger`.
 */
const props = defineProps<ComboboxInputProps & { class?: HTMLAttributes['class']; invalid?: boolean }>()
const emits = defineEmits<ComboboxInputEmits>()

const delegatedProps = computed(() => {
  const { class: _class, invalid: _invalid, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <ComboboxAnchor
    :class="
      cn(
        'relative flex h-10 w-full items-center rounded-lg border bg-canvas/60 pr-9 pl-3 text-sm text-ink transition-colors focus-within:border-accent-500',
        invalid ? 'border-rose-500/60' : 'border-outline-strong',
      )
    "
  >
    <ComboboxInput
      v-bind="forwarded"
      :aria-invalid="invalid || undefined"
      :class="
        cn(
          'h-full w-full min-w-0 truncate bg-transparent text-sm text-ink outline-none placeholder:text-ink-muted disabled:cursor-not-allowed disabled:opacity-50',
          props.class,
        )
      "
    />
    <ComboboxTrigger as-child>
      <button type="button" tabindex="-1" class="absolute right-2.5 inline-flex items-center text-ink-muted">
        <AppIcon name="chevron-down" :size="14" />
      </button>
    </ComboboxTrigger>
  </ComboboxAnchor>
</template>
