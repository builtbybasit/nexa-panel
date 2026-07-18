<script setup lang="ts">
import { Icon } from '@iconify/vue'
import { computed } from 'vue'

import { lucideName } from './icons'

const props = withDefaults(defineProps<{ name: string; size?: number }>(), { size: 16 })

const icon = computed(() => lucideName(props.name))
</script>

<template>
  <Icon :icon="icon" :width="size" :height="size" aria-hidden="true" />
</template>
