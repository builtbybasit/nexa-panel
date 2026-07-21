<script setup lang="ts">
import { ListboxItem, type ListboxItemEmits, type ListboxItemProps, useForwardPropsEmits, useId } from 'reka-ui'
import { computed, type HTMLAttributes, onMounted, onUnmounted } from 'vue'

import { cn } from '@/shared/lib/utils'

import { useCommand, useCommandGroup } from './context'

/**
 * A selectable command row. Pass `text-value` with the text the filter should
 * match (defaults to the item's `value`); this lets a row match on more than its
 * visible label. Hides itself when the current query filters it out.
 */
const props = defineProps<ListboxItemProps & { class?: HTMLAttributes['class']; textValue?: string }>()
const emits = defineEmits<ListboxItemEmits>()

const delegatedProps = computed(() => {
  const { class: _class, textValue: _textValue, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)

const id = useId()
const { filterState, allItems, allGroups } = useCommand()
const groupContext = useCommandGroup()

const isRender = computed(() => {
  if (!filterState.search) return true
  const filtered = filterState.filtered.items.get(id)
  // undefined means the item is not yet in the map: render once so it registers.
  if (filtered === undefined) return true
  return filtered > 0
})

onMounted(() => {
  allItems.value.set(id, props.textValue ?? String(props.value ?? ''))

  const groupId = groupContext?.id
  if (groupId) {
    if (!allGroups.value.has(groupId)) allGroups.value.set(groupId, new Set([id]))
    else allGroups.value.get(groupId)?.add(id)
  }
})

onUnmounted(() => {
  allItems.value.delete(id)
})
</script>

<template>
  <ListboxItem
    v-if="isRender"
    v-bind="forwarded"
    :id="id"
    :class="
      cn(
        'relative flex cursor-pointer select-none items-center gap-3 rounded-lg px-3 py-2 text-[13.5px] font-medium text-ink-secondary outline-none transition-colors',
        'data-[highlighted]:bg-accent-400/[0.08] data-[highlighted]:text-ink data-[disabled]:pointer-events-none data-[disabled]:opacity-40',
        props.class,
      )
    "
    @select="() => { filterState.search = '' }"
  >
    <slot />
  </ListboxItem>
</template>
