<script setup lang="ts">
import { cn } from '@/shared/lib/utils'
import { type HTMLAttributes } from 'vue'

const props = defineProps<{ class?: HTMLAttributes['class'] }>()
</script>

<template>
  <div
    :class="
      cn('flex flex-wrap items-center justify-end gap-2 border-t border-outline px-5 py-4 sm:px-6', props.class)
    "
  >
    <slot />
  </div>
</template>
