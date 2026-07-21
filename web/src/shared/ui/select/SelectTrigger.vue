<script setup lang="ts">
import { SelectIcon, SelectTrigger, type SelectTriggerProps, SelectValue, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'

const props = defineProps<
  SelectTriggerProps & { class?: HTMLAttributes['class']; placeholder?: string; invalid?: boolean }
>()

const delegatedProps = computed(() => {
  const { class: _class, placeholder: _placeholder, invalid: _invalid, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
</script>

<template>
  <SelectTrigger
    v-bind="forwarded"
    :aria-invalid="invalid || undefined"
    :class="
      cn(
        'inline-flex h-10 w-full items-center justify-between gap-2 rounded-lg border bg-canvas/60 px-3 text-sm text-ink transition-colors outline-none data-[placeholder]:text-ink-muted disabled:cursor-not-allowed disabled:opacity-50',
        invalid ? 'border-rose-500/60 focus:border-rose-400' : 'border-outline-strong focus:border-accent-500',
        props.class,
      )
    "
  >
    <SelectValue class="truncate text-left" :placeholder="placeholder" />
    <SelectIcon as-child>
      <AppIcon name="chevron-down" :size="14" class="shrink-0 text-ink-muted" />
    </SelectIcon>
  </SelectTrigger>
</template>
