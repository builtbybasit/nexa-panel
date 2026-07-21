<script setup lang="ts">
import { ListboxRoot, type ListboxRootEmits, type ListboxRootProps, useFilter, useForwardPropsEmits } from 'reka-ui'
import { computed, type HTMLAttributes, reactive, ref, watch } from 'vue'

import { cn } from '@/shared/lib/utils'

import { provideCommandContext } from './context'

/**
 * cmdk-style command menu, adapted from shadcn-vue onto reka's `Listbox`.
 * `CommandInput` drives the internal `filterState.search`; `v-model:searchTerm`
 * mirrors it so a host (e.g. the command palette) can react to the query — for
 * instance to only render expensive result groups once the user starts typing.
 */
const props = withDefaults(defineProps<ListboxRootProps & { class?: HTMLAttributes['class']; searchTerm?: string }>(), {
  modelValue: '',
  highlightOnHover: true,
})

const emits = defineEmits<ListboxRootEmits & { 'update:searchTerm': [value: string] }>()

const delegatedProps = computed(() => {
  const { class: _class, searchTerm: _searchTerm, ...delegated } = props
  return delegated
})

const forwarded = useForwardPropsEmits(delegatedProps, emits)

const allItems = ref<Map<string, string>>(new Map())
const allGroups = ref<Map<string, Set<string>>>(new Map())

const { contains } = useFilter({ sensitivity: 'base' })
const filterState = reactive({
  search: props.searchTerm ?? '',
  filtered: {
    /** The count of all visible items. */
    count: 0,
    /** Map from visible item id to its search score. */
    items: new Map<string, number>(),
    /** Set of groups with at least one visible item. */
    groups: new Set<string>(),
  },
})

function filterItems() {
  if (!filterState.search) {
    filterState.filtered.count = allItems.value.size
    // Each item knows to show itself because the search is empty.
    return
  }

  filterState.filtered.groups = new Set()
  let itemCount = 0

  for (const [id, value] of allItems.value) {
    const score = contains(value, filterState.search)
    filterState.filtered.items.set(id, score ? 1 : 0)
    if (score) itemCount++
  }

  for (const [groupId, group] of allGroups.value) {
    for (const itemId of group) {
      if ((filterState.filtered.items.get(itemId) ?? 0) > 0) {
        filterState.filtered.groups.add(groupId)
        break
      }
    }
  }

  filterState.filtered.count = itemCount
}

watch(
  () => filterState.search,
  (value) => {
    filterItems()
    emits('update:searchTerm', value)
  },
)

watch(
  () => props.searchTerm,
  (value) => {
    if (value !== undefined && value !== filterState.search) filterState.search = value
  },
)

provideCommandContext({ allItems, allGroups, filterState })
</script>

<template>
  <ListboxRoot v-bind="forwarded" :class="cn('flex h-full w-full flex-col overflow-hidden', props.class)">
    <slot />
  </ListboxRoot>
</template>
