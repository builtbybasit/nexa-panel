<script setup lang="ts">
import {
  CheckboxIndicator,
  CheckboxRoot,
  type CheckboxRootEmits,
  type CheckboxRootProps,
  useForwardPropsEmits,
} from 'reka-ui'
import { reactiveOmit } from '@vueuse/core'
import { type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import AppIcon from '../AppIcon.vue'

const props = defineProps<CheckboxRootProps & { class?: HTMLAttributes['class'] }>()
const emits = defineEmits<CheckboxRootEmits>()

const delegated = reactiveOmit(props, 'class')
const forwarded = useForwardPropsEmits(delegated, emits)
</script>

<template>
  <CheckboxRoot
    v-slot="{ state }"
    v-bind="(forwarded as CheckboxRootProps)"
    :class="
      cn(
        'grid size-4 shrink-0 place-items-center rounded border border-outline-strong bg-surface text-accent-950 outline-none transition-colors data-[state=checked]:border-accent-500 data-[state=checked]:bg-accent-500 data-[state=indeterminate]:border-accent-500 data-[state=indeterminate]:bg-accent-500 disabled:cursor-not-allowed disabled:opacity-40',
        props.class,
      )
    "
  >
    <CheckboxIndicator class="grid place-items-center text-current">
      <span v-if="state === 'indeterminate'" class="h-0.5 w-2.5 rounded-full bg-current" />
      <AppIcon v-else name="check" :size="12" />
    </CheckboxIndicator>
  </CheckboxRoot>
</template>
