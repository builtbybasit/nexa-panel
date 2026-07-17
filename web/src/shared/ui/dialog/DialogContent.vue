<script setup lang="ts">
import { cn } from '@/shared/lib/utils'
import AppIcon from '@/shared/ui/AppIcon.vue'
import {
  DialogClose,
  DialogContent,
  type DialogContentEmits,
  type DialogContentProps,
  DialogOverlay,
  DialogPortal,
  useForwardPropsEmits,
} from 'reka-ui'
import { computed, type ComputedRef, type HTMLAttributes } from 'vue'

const props = defineProps<DialogContentProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<DialogContentEmits>()

const delegated = computed(() => {
  const { class: _class, ...rest } = props
  return rest
})
// reka-ui's `useForwardPropsEmits` marks non-boolean props as required-but-possibly-undefined,
// which its own exact-optional child props reject under `exactOptionalPropertyTypes`. The
// forwarded props/emit handlers are runtime-correct, so we relax the binding type for vue-tsc.
const forwarded = useForwardPropsEmits(delegated, emits) as unknown as ComputedRef<Record<string, unknown>>
</script>

<template>
  <DialogPortal>
    <DialogOverlay
      class="fixed inset-0 z-40 bg-canvas/70 backdrop-blur-sm data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0"
    />
    <DialogContent
      v-bind="forwarded"
      :class="
        cn(
          'fixed left-1/2 top-1/2 z-50 w-full max-w-lg -translate-x-1/2 -translate-y-1/2 rounded-2xl border border-outline bg-surface text-ink shadow-2xl shadow-black/50 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95',
          props.class,
        )
      "
    >
      <slot />
      <DialogClose
        class="absolute right-4 top-4 rounded-lg p-1.5 text-ink-muted transition-colors hover:bg-white/[0.05] hover:text-ink"
        aria-label="Close dialog"
      >
        <AppIcon name="x" :size="16" />
      </DialogClose>
    </DialogContent>
  </DialogPortal>
</template>
