<script setup lang="ts">
import { cn } from '@/shared/lib/utils'
import { DialogTitle, type DialogTitleProps, useForwardProps } from 'reka-ui'
import { computed, type ComputedRef, type HTMLAttributes } from 'vue'

const props = defineProps<DialogTitleProps & { class?: HTMLAttributes['class'] }>()

const delegated = computed(() => {
  const { class: _class, ...rest } = props
  return rest
})
// reka-ui's `useForwardProps` marks non-boolean props as required-but-possibly-undefined,
// which its own exact-optional child props reject under `exactOptionalPropertyTypes`. The
// forwarded values are runtime-correct, so we relax the binding type to unblock vue-tsc.
const forwarded = useForwardProps(delegated) as unknown as ComputedRef<Record<string, unknown>>
</script>

<template>
  <DialogTitle v-bind="forwarded" :class="cn('text-[15px] font-semibold text-ink min-w-0', props.class)">
    <slot />
  </DialogTitle>
</template>
