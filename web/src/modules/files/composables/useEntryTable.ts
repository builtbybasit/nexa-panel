import { computed, ref, watch, type Ref } from 'vue'

import type { FileEntry } from '../api'
import { entryTypeLabel } from '../lib'

export type SortKey = 'name' | 'type' | 'size' | 'modified'

/**
 * Client-side view state of the listing table: the name filter, the column
 * sort, and the row selection they act on. Selection is kept as names rather
 * than entries so it survives a refetch, and is pruned whenever an entry it
 * points at disappears (a delete, a move, someone else's change).
 */
export function useEntryTable(items: Ref<FileEntry[]>) {
  const nameFilter = ref('')
  const sortKey = ref<SortKey>('name')
  const sortAscending = ref(true)
  const selectedNames = ref<string[]>([])
  const lastClickedName = ref('')

  function toggleSort(key: SortKey) {
    if (sortKey.value === key) {
      sortAscending.value = !sortAscending.value
    } else {
      sortKey.value = key
      sortAscending.value = true
    }
  }

  function ariaSort(key: SortKey): 'ascending' | 'descending' | 'none' {
    if (sortKey.value !== key) return 'none'
    return sortAscending.value ? 'ascending' : 'descending'
  }

  const visibleItems = computed(() => {
    const needle = nameFilter.value.trim().toLowerCase()
    const filtered = needle ? items.value.filter((entry) => entry.name.toLowerCase().includes(needle)) : [...items.value]
    const direction = sortAscending.value ? 1 : -1
    filtered.sort((a, b) => {
      // Folders stay grouped before files regardless of sort direction.
      const groupOrder = (a.kind === 'dir' ? 0 : 1) - (b.kind === 'dir' ? 0 : 1)
      if (groupOrder !== 0) return groupOrder
      if (sortKey.value === 'size') return (a.size - b.size) * direction
      if (sortKey.value === 'modified') return a.modifiedAt.localeCompare(b.modifiedAt) * direction
      if (sortKey.value === 'type') {
        const order = entryTypeLabel(a).localeCompare(entryTypeLabel(b)) * direction
        if (order !== 0) return order
      }
      return a.name.localeCompare(b.name) * direction
    })
    return filtered
  })

  watch(items, (list) => {
    const names = new Set(list.map((entry) => entry.name))
    if (selectedNames.value.some((name) => !names.has(name))) {
      selectedNames.value = selectedNames.value.filter((name) => names.has(name))
    }
  })

  const selectedEntries = computed(() => items.value.filter((entry) => selectedNames.value.includes(entry.name)))
  /** The single selected entry, when exactly one row is selected. */
  const soloEntry = computed(() => (selectedEntries.value.length === 1 ? selectedEntries.value[0] : undefined))
  const selectionHasDirectory = computed(() => selectedEntries.value.some((entry) => entry.kind === 'dir'))

  const allSelected = computed(
    () => visibleItems.value.length > 0 && visibleItems.value.every((entry) => selectedNames.value.includes(entry.name)),
  )
  const someSelected = computed(
    () => !allSelected.value && visibleItems.value.some((entry) => selectedNames.value.includes(entry.name)),
  )

  const isSelected = (name: string) => selectedNames.value.includes(name)

  function toggleSelect(name: string) {
    selectedNames.value = isSelected(name)
      ? selectedNames.value.filter((selected) => selected !== name)
      : [...selectedNames.value, name]
    lastClickedName.value = name
  }

  function onRowClick(entry: FileEntry, event: MouseEvent) {
    if (event.metaKey || event.ctrlKey) {
      toggleSelect(entry.name)
      return
    }
    if (event.shiftKey && lastClickedName.value) {
      const names = visibleItems.value.map((candidate) => candidate.name)
      const from = names.indexOf(lastClickedName.value)
      const to = names.indexOf(entry.name)
      if (from !== -1 && to !== -1) {
        selectedNames.value = names.slice(Math.min(from, to), Math.max(from, to) + 1)
        return
      }
    }
    // Plain click replaces the selection; clicking the only selected row clears it.
    selectedNames.value = soloEntry.value?.name === entry.name ? [] : [entry.name]
    lastClickedName.value = entry.name
  }

  /** Checks or unchecks only the visible (filtered) entries, preserving hidden selections. */
  function toggleAll() {
    const visible = new Set(visibleItems.value.map((entry) => entry.name))
    if (allSelected.value) {
      selectedNames.value = selectedNames.value.filter((name) => !visible.has(name))
    } else {
      selectedNames.value = [...new Set([...selectedNames.value, ...visible])]
    }
  }

  /** Right-click adopts the row into the selection so toolbar and menu agree. */
  function adopt(entry: FileEntry) {
    if (isSelected(entry.name)) return
    selectedNames.value = [entry.name]
    lastClickedName.value = entry.name
  }

  function reset() {
    selectedNames.value = []
    lastClickedName.value = ''
    nameFilter.value = ''
  }

  return {
    nameFilter,
    sortKey,
    sortAscending,
    toggleSort,
    ariaSort,
    visibleItems,
    selectedNames,
    selectedEntries,
    soloEntry,
    selectionHasDirectory,
    allSelected,
    someSelected,
    isSelected,
    toggleSelect,
    toggleAll,
    onRowClick,
    adopt,
    reset,
  }
}
