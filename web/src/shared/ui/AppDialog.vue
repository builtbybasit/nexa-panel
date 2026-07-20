<script setup lang="ts">
import { VisuallyHidden } from 'reka-ui'
import { nextTick } from 'vue'

import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './dialog'

const props = withDefaults(
  defineProps<{
    open: boolean
    title?: string
    description?: string
    size?: 'sm' | 'md' | 'lg'
  }>(),
  { size: 'md' },
)

const emit = defineEmits<{ close: [] }>()

const sizeClasses = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
}

/**
 * Reka traps focus, restores it, closes on Escape and locks body scroll on our
 * behalf, so this component only overrides the initial focus target: keyboard
 * users should land on the first form field, ready to type.
 */
function onOpenAutoFocus(event: Event) {
  event.preventDefault()
  void nextTick(() => {
    const root = event.target as HTMLElement | null
    const field = root?.querySelector<HTMLElement>(
      'input:not([type="hidden"]):not([disabled]), select:not([disabled]), textarea:not([disabled])',
    )
    ;(field ?? root)?.focus()
  })
}
</script>

<template>
  <Dialog
    :open="open"
    @update:open="
      (value) => {
        if (!value) emit('close')
      }
    "
  >
    <DialogContent
      :class="[sizeClasses[size], 'flex max-h-[85vh] flex-col']"
      @open-auto-focus="onOpenAutoFocus"
    >
      <DialogHeader>
        <DialogTitle v-if="title || $slots.title">
          <slot name="title">{{ title }}</slot>
        </DialogTitle>
        <VisuallyHidden v-else as-child>
          <DialogTitle>Dialog</DialogTitle>
        </VisuallyHidden>
        <DialogDescription v-if="description || $slots.description">
          <slot name="description">{{ description }}</slot>
        </DialogDescription>
        <VisuallyHidden v-else as-child>
          <DialogDescription>Dialog content</DialogDescription>
        </VisuallyHidden>
      </DialogHeader>
      <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 sm:px-6">
        <slot />
      </div>
      <DialogFooter v-if="$slots.footer">
        <slot name="footer" />
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
