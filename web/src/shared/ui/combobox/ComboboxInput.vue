<script setup lang="ts">
import {
  ComboboxInput,
  type ComboboxInputEmits,
  type ComboboxInputProps,
  useForwardPropsEmits,
} from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'

/** Search field pinned at the top of a `ComboboxList`; reka filters items as you type. */
defineOptions({ inheritAttrs: false })

const props = defineProps<ComboboxInputProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<ComboboxInputEmits>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <div class="flex h-11 items-center gap-2 border-b border-outline px-3">
    <AppIcon name="search" :size="15" class="shrink-0 text-ink-muted" />
    <ComboboxInput
      v-bind="{ ...forwarded, ...$attrs }"
      :class="
        cn(
          'h-full w-full bg-transparent text-sm text-ink outline-none placeholder:text-ink-muted disabled:cursor-not-allowed disabled:opacity-50',
          props.class,
        )
      "
    />
  </div>
</template>
