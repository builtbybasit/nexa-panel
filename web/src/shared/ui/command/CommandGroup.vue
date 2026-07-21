<script setup lang="ts">
import { ListboxGroup, ListboxGroupLabel, type ListboxGroupProps, useId } from 'reka-ui'
import { computed, type HTMLAttributes, onMounted, onUnmounted } from 'vue'

import { cn } from '@/shared/lib/utils'

import { provideCommandGroupContext, useCommand } from './context'

/** A titled section of command rows. Hidden entirely when no member matches the query. */
const props = defineProps<ListboxGroupProps & { class?: HTMLAttributes['class']; heading?: string }>()

const delegatedProps = computed(() => {
  const { class: _class, heading: _heading, ...delegated } = props
  return delegated
})

const { allGroups, filterState } = useCommand()
const id = useId()

const isRender = computed(() => (!filterState.search ? true : filterState.filtered.groups.has(id)))

provideCommandGroupContext({ id })

onMounted(() => {
  if (!allGroups.value.has(id)) allGroups.value.set(id, new Set())
})

onUnmounted(() => {
  allGroups.value.delete(id)
})
</script>

<template>
  <ListboxGroup
    v-bind="delegatedProps"
    :id="id"
    :class="cn('overflow-hidden', props.class)"
    :hidden="isRender ? undefined : true"
  >
    <ListboxGroupLabel
      v-if="heading"
      class="px-3 pt-2 pb-1 text-[10px] font-bold tracking-[0.14em] text-ink-muted uppercase"
    >
      {{ heading }}
    </ListboxGroupLabel>
    <slot />
  </ListboxGroup>
</template>
