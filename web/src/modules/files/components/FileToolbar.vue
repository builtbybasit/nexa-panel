<script setup lang="ts">
import {
  AppIcon,
  AppInput,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  StatusPill,
} from '@/shared/ui'

import { downloadUrl, type FileEntry } from '../api'
import { countLabel, isArchiveName, joinPath, toolbarButton, type FileAction } from '../lib'

const props = defineProps<{
  selectedEntries: FileEntry[]
  soloEntry: FileEntry | undefined
  siteId: string
  directory: string
  canMutateHere: boolean
  canWriteFiles: boolean
  readOnlyHint: string
  nameFilter: string
  visibleCount: number
  totalCount: number
}>()

const emit = defineEmits<{ action: [action: FileAction]; 'update:nameFilter': [value: string] }>()

const downloadHref = () => downloadUrl(props.siteId, joinPath(props.directory, props.soloEntry?.name ?? ''))
</script>

<template>
  <div class="flex min-h-12 flex-wrap items-center gap-1 border-b border-outline bg-white/[0.03] px-3 py-1.5">
    <template v-if="selectedEntries.length">
      <span class="mr-2 min-w-0 truncate font-mono text-[13px] font-medium text-ink" aria-live="polite">
        {{ soloEntry ? soloEntry.name : `${countLabel(selectedEntries.length)} selected` }}
      </span>
      <span class="ml-auto flex flex-wrap items-center gap-1">
        <a v-if="soloEntry?.kind === 'file'" :class="toolbarButton" :href="downloadHref()">
          <AppIcon name="download" :size="15" />
          Download
        </a>
        <button v-if="soloEntry" :class="toolbarButton" @click="emit('action', 'copy-path')">
          <AppIcon name="link-2" :size="15" />
          Copy path
        </button>
        <button v-if="soloEntry?.kind === 'file'" :class="toolbarButton" @click="emit('action', 'edit')">
          <AppIcon name="pencil" :size="15" />
          Edit
        </button>
        <!-- Copy and Cut stage the selection; the destination is chosen by
             navigating there and pasting, never by typing a path. Copying only
             reads, so it stays available in read-only trees. -->
        <button v-if="canWriteFiles" :class="toolbarButton" @click="emit('action', 'clipboard-copy')">
          <AppIcon name="copy" :size="15" />
          Copy
        </button>
        <button v-if="canMutateHere" :class="toolbarButton" @click="emit('action', 'clipboard-cut')">
          <AppIcon name="scissors" :size="15" />
          Cut
        </button>
        <button v-if="canWriteFiles" :class="toolbarButton" @click="emit('action', 'archive')">
          <AppIcon name="archive" :size="15" />
          Archive
        </button>
        <DropdownMenu v-if="soloEntry">
          <DropdownMenuTrigger as-child>
            <button :class="toolbarButton" aria-label="More actions">
              <AppIcon name="more-horizontal" :size="15" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem v-if="soloEntry.kind === 'dir'" @select="emit('action', 'open')">Open</DropdownMenuItem>
            <DropdownMenuItem v-if="soloEntry.kind === 'dir'" @select="emit('action', 'compute-size')">
              Compute size
            </DropdownMenuItem>
            <template v-if="canMutateHere">
              <DropdownMenuItem @select="emit('action', 'chmod')">Change permissions…</DropdownMenuItem>
              <DropdownMenuItem @select="emit('action', 'rename')">Rename…</DropdownMenuItem>
              <DropdownMenuItem
                v-if="soloEntry.kind === 'file' && isArchiveName(soloEntry.name)"
                @select="emit('action', 'extract')"
              >
                Extract…
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                class="text-rose-300 data-[highlighted]:bg-rose-500/10 data-[highlighted]:text-rose-300"
                @select="emit('action', 'delete')"
              >
                Delete…
              </DropdownMenuItem>
            </template>
          </DropdownMenuContent>
        </DropdownMenu>
        <button
          v-else-if="canMutateHere"
          :class="toolbarButton"
          class="!text-rose-300 hover:!bg-rose-500/10"
          @click="emit('action', 'delete')"
        >
          <AppIcon name="trash" :size="15" />
          Delete
        </button>
        <button :class="toolbarButton" aria-label="Clear selection" @click="emit('action', 'clear-selection')">
          <AppIcon name="x" :size="15" />
        </button>
      </span>
    </template>
    <template v-else>
      <template v-if="canMutateHere">
        <button :class="toolbarButton" @click="emit('action', 'mkdir')">
          <AppIcon name="folder" :size="15" />
          New folder
        </button>
        <button :class="toolbarButton" @click="emit('action', 'newfile')">
          <AppIcon name="file-text" :size="15" />
          New file
        </button>
        <button :class="toolbarButton" @click="emit('action', 'upload')">
          <AppIcon name="upload" :size="15" />
          Upload
        </button>
      </template>
      <StatusPill v-else tone="neutral" label="Read-only path" :description="readOnlyHint" :pulse="false" />
      <span class="ml-auto flex items-center gap-2">
        <span v-if="nameFilter" class="text-xs text-ink-muted" aria-live="polite">
          {{ visibleCount }} of {{ totalCount }} shown
        </span>
        <div class="w-48 sm:w-56">
          <AppInput
            :model-value="nameFilter"
            type="search"
            placeholder="Filter by name"
            aria-label="Filter entries by name"
            @update:model-value="(value) => emit('update:nameFilter', String(value))"
          />
        </div>
      </span>
    </template>
  </div>
</template>
