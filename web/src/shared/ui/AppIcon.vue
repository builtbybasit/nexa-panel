<script setup lang="ts">
import { computed } from 'vue'

import { iconFor } from './icons'

const props = withDefaults(defineProps<{ name: string; size?: number }>(), { size: 16 })

const icon = computed(() => iconFor(props.name))
</script>

<template>
  <component :is="icon" :size="size" aria-hidden="true" focusable="false" />
</template>
