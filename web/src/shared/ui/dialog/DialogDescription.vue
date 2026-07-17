<script setup lang="ts">
import { cn } from '@/shared/lib/utils'
import { DialogDescription, type DialogDescriptionProps, useForwardProps } from 'reka-ui'
import { computed, type ComputedRef, type HTMLAttributes } from 'vue'

const props = defineProps<DialogDescriptionProps & { class?: HTMLAttributes['class'] }>()

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
  <DialogDescription v-bind="forwarded" :class="cn('text-[13px] text-ink-secondary', props.class)">
    <slot />
  </DialogDescription>
</template>
