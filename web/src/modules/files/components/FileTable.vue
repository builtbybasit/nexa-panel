<script setup lang="ts">
import { computed, ref } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import {
  AppIcon,
  Checkbox,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from '@/shared/ui'

import { downloadUrl, type FileEntry } from '../api'
import type { SortKey } from '../composables/useEntryTable'
import {
  countLabel,
  entryAccess,
  entryIcon,
  entrySize,
  entryTypeLabel,
  isArchiveName,
  joinPath,
  type FileAction,
} from '../lib'

const props = defineProps<{
  entries: FileEntry[]
  siteId: string
  directory: string
  selectedNames: string[]
  allSelected: boolean
  someSelected: boolean
  sortKey: SortKey
  sortAscending: boolean
  canMutateHere: boolean
  canWriteFiles: boolean
  canPaste: boolean
}>()

const emit = defineEmits<{
  action: [action: FileAction]
  sort: [key: SortKey]
  'toggle-all': []
  'toggle-select': [name: string]
  'row-click': [entry: FileEntry, event: MouseEvent]
  'row-open': [entry: FileEntry]
  /** Raised before the menu opens so the view can adopt the row it landed on. */
  'row-context': [entry: FileEntry | undefined]
}>()

const sortableColumns: { key: SortKey; label: string }[] = [
  { key: 'name', label: 'Name' },
  { key: 'type', label: 'Type' },
  { key: 'size', label: 'Size' },
]

function ariaSort(key: SortKey): 'ascending' | 'descending' | 'none' {
  if (props.sortKey !== key) return 'none'
  return props.sortAscending ? 'ascending' : 'descending'
}

/** The row a right-click landed on; drives the single shared context menu. */
const activeEntry = ref<FileEntry>()

function onContextMenu(event: MouseEvent) {
  const row = (event.target as HTMLElement).closest('tr[data-name]')
  const name = row?.getAttribute('data-name')
  activeEntry.value = name ? props.entries.find((candidate) => candidate.name === name) : undefined
  emit('row-context', activeEntry.value)
}

/**
 * Right-clicking inside a multi-selection keeps that selection, so the menu
 * offers only what applies to all of it; single-entry actions reappear once the
 * selection is one row.
 */
const isBulk = computed(() => props.selectedNames.length > 1)
const menuTitle = computed(() =>
  isBulk.value ? `${countLabel(props.selectedNames.length)} selected` : (activeEntry.value?.name ?? ''),
)
const href = (entry: FileEntry) => downloadUrl(props.siteId, joinPath(props.directory, entry.name))
</script>

<template>
  <ContextMenu>
    <ContextMenuTrigger as-child>
      <div class="overflow-x-auto" @contextmenu="onContextMenu">
        <table class="w-full border-collapse text-left">
          <thead>
            <tr class="border-b border-outline">
              <th class="w-8 px-3 py-2.5">
                <Checkbox
                  :model-value="allSelected ? true : someSelected ? 'indeterminate' : false"
                  aria-label="Select all entries"
                  @update:model-value="() => emit('toggle-all')"
                />
              </th>
              <th v-for="column in sortableColumns" :key="column.key" class="px-3 py-2.5" :aria-sort="ariaSort(column.key)">
                <button
                  type="button"
                  class="flex items-center gap-1 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase transition-colors hover:text-ink"
                  @click="emit('sort', column.key)"
                >
                  {{ column.label }}
                  <AppIcon v-if="sortKey === column.key" :name="sortAscending ? 'chevron-up' : 'chevron-down'" :size="12" />
                </button>
              </th>
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Owner</th>
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Group</th>
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Access</th>
              <th class="px-3 py-2.5" :aria-sort="ariaSort('modified')">
                <button
                  type="button"
                  class="flex items-center gap-1 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase transition-colors hover:text-ink"
                  @click="emit('sort', 'modified')"
                >
                  Changed
                  <AppIcon v-if="sortKey === 'modified'" :name="sortAscending ? 'chevron-up' : 'chevron-down'" :size="12" />
                </button>
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-outline">
            <tr
              v-for="entry in entries"
              :key="entry.name"
              :data-name="entry.name"
              class="cursor-default transition-colors select-none"
              :class="selectedNames.includes(entry.name) ? 'bg-accent-500/[0.08]' : 'hover:bg-white/[0.03]'"
              @click="emit('row-click', entry, $event)"
              @dblclick="emit('row-open', entry)"
            >
              <td class="px-3 py-2.5" @click.stop>
                <Checkbox
                  :model-value="selectedNames.includes(entry.name)"
                  :aria-label="`Select ${entry.name}`"
                  @update:model-value="() => emit('toggle-select', entry.name)"
                />
              </td>
              <td class="max-w-xs px-3 py-2.5">
                <span class="flex w-full min-w-0 items-center gap-2.5">
                  <AppIcon
                    :name="entryIcon(entry)"
                    :size="16"
                    class="shrink-0"
                    :class="entry.kind === 'dir' ? 'text-amber-300/90' : 'text-ink-muted'"
                  />
                  <span class="truncate text-[13px] font-medium text-ink">{{ entry.name }}</span>
                </span>
              </td>
              <td class="px-3 py-2.5 text-xs whitespace-nowrap text-ink-secondary">{{ entryTypeLabel(entry) }}</td>
              <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary">{{ entrySize(entry) }}</td>
              <td class="px-3 py-2.5 text-xs whitespace-nowrap text-ink-secondary">{{ entry.owner }}</td>
              <td class="px-3 py-2.5 text-xs whitespace-nowrap text-ink-secondary">{{ entry.group }}</td>
              <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary" :title="entry.mode">
                {{ entryAccess(entry) }}
              </td>
              <td class="px-3 py-2.5 text-xs whitespace-nowrap text-ink-secondary">{{ formatDateTime(entry.modifiedAt) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </ContextMenuTrigger>

    <!-- Right-click menu — grouped FastPanel-style. -->
    <ContextMenuContent v-if="activeEntry">
      <ContextMenuLabel>{{ menuTitle }}</ContextMenuLabel>
      <ContextMenuSeparator />
      <template v-if="!isBulk">
        <ContextMenuItem v-if="activeEntry.kind === 'dir'" @select="emit('action', 'open')">Open</ContextMenuItem>
        <ContextMenuItem v-if="activeEntry.kind === 'file'" as-child>
          <a :href="href(activeEntry)">Download</a>
        </ContextMenuItem>
        <ContextMenuItem @select="emit('action', 'copy-path')">Copy path</ContextMenuItem>
        <ContextMenuItem v-if="activeEntry.kind === 'file'" @select="emit('action', 'edit')">Edit</ContextMenuItem>
        <ContextMenuItem v-if="activeEntry.kind === 'dir'" @select="emit('action', 'compute-size')">
          Compute size
        </ContextMenuItem>
      </template>
      <!-- Copying only reads the source, so it is offered in read-only trees
           too; the paste itself is what the writable-zone gate applies to. -->
      <template v-if="canWriteFiles">
        <ContextMenuSeparator />
        <ContextMenuItem @select="emit('action', 'clipboard-copy')">Copy</ContextMenuItem>
        <ContextMenuItem v-if="canMutateHere" @select="emit('action', 'clipboard-cut')">Cut</ContextMenuItem>
        <ContextMenuItem :disabled="!canPaste" @select="emit('action', 'paste')">Paste</ContextMenuItem>
      </template>
      <template v-if="canMutateHere">
        <ContextMenuSeparator />
        <template v-if="!isBulk">
          <ContextMenuItem @select="emit('action', 'chmod')">Change permissions…</ContextMenuItem>
          <ContextMenuItem @select="emit('action', 'rename')">Rename…</ContextMenuItem>
        </template>
        <ContextMenuItem @select="emit('action', 'archive')">Pack into archive…</ContextMenuItem>
        <ContextMenuItem
          v-if="!isBulk && activeEntry.kind === 'file' && isArchiveName(activeEntry.name)"
          @select="emit('action', 'extract')"
        >
          Extract…
        </ContextMenuItem>
        <ContextMenuSeparator />
        <ContextMenuItem
          class="text-rose-300 data-[highlighted]:bg-rose-500/10 data-[highlighted]:text-rose-300"
          @select="emit('action', 'delete')"
        >
          Delete
        </ContextMenuItem>
      </template>
    </ContextMenuContent>
  </ContextMenu>
</template>
