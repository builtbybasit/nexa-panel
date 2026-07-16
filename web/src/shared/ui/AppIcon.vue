<script setup lang="ts">
import { computed } from 'vue'

import { iconPaths } from './icons'

const props = withDefaults(defineProps<{ name: string; size?: number }>(), { size: 16 })

const markup = computed(() => iconPaths[props.name] ?? iconPaths.info)
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.75"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    v-html="markup"
  />
</template>
