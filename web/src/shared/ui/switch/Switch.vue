<script setup lang="ts">
import { SwitchRoot, type SwitchRootEmits, type SwitchRootProps, SwitchThumb, useForwardPropsEmits } from 'reka-ui'
import { type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

/**
 * shadcn-vue-style switch wrapping reka's `SwitchRoot`/`SwitchThumb`.
 *
 * Binds via reka's `v-model` (`modelValue` / `update:modelValue`) and toggles
 * the on/off styling off the `data-[state=checked]` attribute. Consumers pass
 * any `SwitchRootProps` (e.g. `disabled`, `name`, `value`) plus an optional
 * `class` that merges over the base styles through `cn`.
 */
const props = defineProps<SwitchRootProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<SwitchRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <SwitchRoot
    v-bind="forwarded"
    :class="
      cn(
        'peer inline-flex h-5 w-9 shrink-0 items-center rounded-full bg-overlay border border-outline transition-colors outline-none data-[state=checked]:bg-accent-500 disabled:cursor-not-allowed disabled:opacity-40',
        props.class,
      )
    "
  >
    <SwitchThumb
      class="pointer-events-none block size-4 translate-x-0.5 rounded-full bg-ink shadow transition-transform data-[state=checked]:translate-x-[18px]"
    />
  </SwitchRoot>
</template>
