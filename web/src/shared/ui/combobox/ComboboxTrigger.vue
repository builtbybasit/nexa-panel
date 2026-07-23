<script setup lang="ts">
import { ComboboxTrigger, type ComboboxTriggerProps, useForwardProps } from 'reka-ui'
import { computed, type HTMLAttributes, useAttrs } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'
import { useFormFieldContext } from '../formFieldContext'

defineOptions({ inheritAttrs: false })

/**
 * The select box for a floating combobox: a button styled like `SelectTrigger`
 * that shows the current `label` (or a muted `placeholder`) plus a chevron.
 * Wrap it in a `ComboboxAnchor as-child` so the `ComboboxList` popover anchors
 * to it. Pass a default slot to render richer trigger content than plain text.
 */
const props = defineProps<
  ComboboxTriggerProps & { class?: HTMLAttributes['class']; label?: string; placeholder?: string; invalid?: boolean }
>()

const delegatedProps = computed(() => {
  const { class: _class, label: _label, placeholder: _placeholder, invalid: _invalid, ...delegated } = props
  return delegated
})

const forwarded = useForwardProps(delegatedProps)
const attrs = useAttrs()
const field = useFormFieldContext()
const forwardedWithAttrs = computed(() => ({ ...forwarded.value, ...attrs }))
const labelledBy = computed(() => {
  if (attrs['aria-label']) return undefined
  return typeof attrs['aria-labelledby'] === 'string' ? attrs['aria-labelledby'] : field?.labelId
})
const describedBy = computed(() =>
  typeof attrs['aria-describedby'] === 'string' ? attrs['aria-describedby'] : field?.descriptionId.value,
)
</script>

<template>
  <ComboboxTrigger
    v-bind="forwardedWithAttrs"
    :aria-labelledby="labelledBy"
    :aria-describedby="describedBy"
    :aria-invalid="invalid || undefined"
    :class="
      cn(
        'flex h-10 w-full items-center justify-between gap-2 rounded-lg border bg-canvas/60 px-3 text-sm text-ink outline-none transition-colors focus:border-accent-500 data-[state=open]:border-accent-500 disabled:cursor-not-allowed disabled:opacity-50',
        invalid ? 'border-rose-500/60' : 'border-outline-strong',
        props.class,
      )
    "
  >
    <slot>
      <span class="min-w-0 flex-1 truncate text-left" :class="label ? 'text-ink' : 'text-ink-muted'">
        {{ label || placeholder }}
      </span>
    </slot>
    <AppIcon name="chevrons-up-down" :size="14" class="shrink-0 text-ink-muted" />
  </ComboboxTrigger>
</template>
