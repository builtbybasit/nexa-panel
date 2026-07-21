<script setup lang="ts">
import { SelectItem, type SelectItemEmits, type SelectItemProps, SelectItemIndicator, SelectItemText, useForwardPropsEmits } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'

const props = defineProps<SelectItemProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<SelectItemEmits>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <SelectItem
    v-bind="forwarded"
    :class="
      cn(
        'relative flex min-h-9 cursor-pointer select-none items-center gap-2.5 rounded-lg py-2 pr-3 pl-8 text-[13px] font-medium text-ink-secondary outline-none transition-colors data-[highlighted]:bg-white/[0.04] data-[highlighted]:text-ink data-[disabled]:pointer-events-none data-[disabled]:opacity-40',
        props.class,
      )
    "
  >
    <SelectItemIndicator class="absolute left-2.5 inline-flex items-center">
      <AppIcon name="check" :size="14" class="text-accent-400" />
    </SelectItemIndicator>
    <SelectItemText>
      <slot />
    </SelectItemText>
  </SelectItem>
</template>
