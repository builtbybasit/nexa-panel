<script setup lang="ts">
import {
  ComboboxRoot,
  type ComboboxRootEmits,
  type ComboboxRootProps,
  useForwardPropsEmits,
} from 'reka-ui'

/**
 * Root of the combobox family. Forwards props + emits so `v-model`, `:open`,
 * and `:ignore-filter` pass through. Consumers that do their own filtering
 * should set `ignore-filter` so reka keeps every rendered item navigable.
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
