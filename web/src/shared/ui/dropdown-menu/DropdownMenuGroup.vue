<script setup lang="ts">
import { DropdownMenuGroup, type DropdownMenuGroupProps, useForwardProps } from 'reka-ui'

const props = defineProps<DropdownMenuGroupProps>()

const forwarded = useForwardProps(props)
</script>

<template>
  <DropdownMenuGroup v-bind="forwarded">
    <slot />
  </DropdownMenuGroup>
</template>
