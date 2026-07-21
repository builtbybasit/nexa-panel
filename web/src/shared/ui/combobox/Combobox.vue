<script setup lang="ts">
import {
  ComboboxRoot,
  type ComboboxRootEmits,
  type ComboboxRootProps,
  useForwardPropsEmits,
} from 'reka-ui'

/**
 * Root of the combobox family. Forwards props + emits so `v-model`, `:open`,
 * and `by` pass through. Used as a floating single-select: a `ComboboxTrigger`
 * button opens a `ComboboxList` popover whose `ComboboxInput` filters the items.
 */
const props = defineProps<ComboboxRootProps>()
const emits = defineEmits<ComboboxRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <ComboboxRoot v-bind="forwarded">
    <slot />
  </ComboboxRoot>
</template>
