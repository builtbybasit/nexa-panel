<script setup lang="ts">
import { Primitive, type PrimitiveProps } from 'reka-ui'
import { computed, type HTMLAttributes } from 'vue'

import { cn } from '@/shared/lib/utils'

import { useCommand } from './context'

/** Rendered only while a query is active and nothing matches it. */
const props = defineProps<PrimitiveProps & { class?: HTMLAttributes['class'] }>()

const delegatedProps = computed(() => {
  const { class: _, ...delegated } = props
  return delegated
})

const { filterState } = useCommand()
const isRender = computed(() => !!filterState.search && filterState.filtered.count === 0)
</script>

<template>
  <Primitive v-if="isRender" v-bind="delegatedProps" :class="cn('p-3', props.class)">
    <slot />
  </Primitive>
</template>
