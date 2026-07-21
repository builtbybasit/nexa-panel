<script setup lang="ts">
import { SelectRoot, type SelectRootEmits, type SelectRootProps, useForwardPropsEmits } from 'reka-ui'

/**
 * Root of the select family. Forwards props + emits so `v-model`, `disabled`,
 * and `required` (which drives the hidden native `<select>` reka renders for
 * form participation) pass straight through.
 */
const props = defineProps<SelectRootProps>()
const emits = defineEmits<SelectRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <SelectRoot v-bind="forwarded">
    <slot />
  </SelectRoot>
</template>
