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
 *
 * Defaults `open-on-click` on since most consumers use this as a floating
 * select (click to browse) rather than a type-to-search-only field; the
 * command palette forces `:open="true"` itself so the default is moot there.
 */
const props = withDefaults(defineProps<ComboboxRootProps>(), { openOnClick: true })
const emits = defineEmits<ComboboxRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <ComboboxRoot v-bind="forwarded">
    <slot />
  </ComboboxRoot>
</template>
