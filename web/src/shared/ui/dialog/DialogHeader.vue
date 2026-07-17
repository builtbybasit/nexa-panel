<script setup lang="ts">
import { cn } from '@/shared/lib/utils'
import { type HTMLAttributes } from 'vue'

const props = defineProps<{ class?: HTMLAttributes['class'] }>()
</script>

<template>
  <div :class="cn('flex flex-col gap-1 px-5 pt-5 sm:px-6 sm:pt-6', props.class)">
    <slot />
  </div>
</template>
