<script setup lang="ts">
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { computed, nextTick, onScopeDispose, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { useToasts } from '@/shared/composables/useToasts'
import { formatBytes } from '@/shared/formatters'
import { registerUnsavedChanges } from '@/shared/navigation/unsavedChanges'
import {
  AppAlert,
  AppButton,
  AppIcon,
  EmptyState,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  SkeletonRow,
} from '@/shared/ui'
import {
  Combobox,
  ComboboxAnchor,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxItemIndicator,
  ComboboxList,
  ComboboxTrigger,
} from '@/shared/ui/combobox'

import { listFiles, type FileEntry } from '../api'
import ChmodDialog from '../components/ChmodDialog.vue'
import ClipboardBar from '../components/ClipboardBar.vue'
import DeleteEntriesDialog from '../components/DeleteEntriesDialog.vue'
import EntryNameDialog from '../components/EntryNameDialog.vue'
import FilePathBar from '../components/FilePathBar.vue'
import FileTable from '../components/FileTable.vue'
import FileToolbar from '../components/FileToolbar.vue'
import JobTargetDialog from '../components/JobTargetDialog.vue'
import PasteConflictDialog from '../components/PasteConflictDialog.vue'
import UploadQueue from '../components/UploadQueue.vue'
import { useEntryTable } from '../composables/useEntryTable'
import { useFileClipboard, type PasteStrategy, type PasteSummary } from '../composables/useFileClipboard'
import { useFileJobs } from '../composables/useFileJobs'
import { useFilesRoute } from '../composables/useFilesRoute'
import { useFileUploads } from '../composables/useFileUploads'
import {
  countLabel,
  defaultArchiveName,
  displayPathOf,
  entryIcon,
  isWritablePath,
  joinPath,
  parentOf,
  type FileAction,
  type NameDialogKind,
} from '../lib'
import EditorPane from './EditorPane.vue'

const identity = useIdentityStore()
const toasts = useToasts()
const queryClient = useQueryClient()

// Site and directory both live in the route query; see useFilesRoute.
const { sitesQuery, activeSites, hasAnySites, siteId, selectedSite, siteSelection, path, setPath } = useFilesRoute()

// --- Directory listing ---

const listingQuery = useQuery({
  queryKey: ['files', siteId, path],
  queryFn: () => listFiles(siteId.value, path.value),
  enabled: computed(() => Boolean(selectedSite.value)),
  retry: false,
})
const items = computed(() => listingQuery.data.value?.entries ?? [])
const listingTruncated = computed(() => listingQuery.data.value?.truncated ?? false)
const listingError = computed(() => (listingQuery.error.value instanceof Error ? listingQuery.error.value.message : ''))

async function refetchListing() {
  await listingQuery.refetch()
}

/** A move empties a directory the user may still have cached; drop them all. */
async function refetchAllDirectories() {
  await queryClient.invalidateQueries({ queryKey: ['files'] })
}

const entryPath = (entry: FileEntry) => joinPath(path.value, entry.name)

// --- Write-zone guard: mutations only inside public/, private/, tmp/, backups/ ---
// The server enforces this; the UI hides mutation affordances at root depth and
// in read-only trees such as logs/.

const canWriteFiles = computed(() => identity.can('files.write'))
const canMutateHere = computed(() => canWriteFiles.value && path.value !== '.' && isWritablePath(path.value))
const readOnlyHint = computed(() =>
  canWriteFiles.value
    ? 'Navigate into public/, private/, tmp/ or backups/ to make changes'
    : 'Your role has read-only file access',
)

// --- Listing view state: filter, sort, selection ---

const table = useEntryTable(items)

// --- Long-running operations (archive / extract / size) follow durable jobs ---

const jobs = useFileJobs(siteId, refetchAllDirectories)

// --- Copy/cut/paste ---

const clipboard = useFileClipboard({ siteId, path, entries: items, canMutateHere })
const conflictOpen = ref(false)

/** Paste straight through unless names collide, which the user resolves first. */
function requestPaste() {
  if (!clipboard.canPaste.value || clipboard.busy.value) return
  if (clipboard.conflicts.value.length) conflictOpen.value = true
  else void runPaste('keep-both')
}

async function runPaste(strategy: PasteStrategy) {
  conflictOpen.value = false
  const mode = clipboard.clipboard.value?.mode
  const summary = await clipboard.paste(strategy)
  await refetchAllDirectories()
  reportPaste(summary, mode === 'cut' ? 'Moved' : 'Copied')
}

function reportPaste(summary: PasteSummary, verb: string) {
  const notes: string[] = []
  if (summary.renamed.length) notes.push(`kept both — added as ${summary.renamed.join(', ')}`)
  if (summary.skipped) notes.push(`${countLabel(summary.skipped)} skipped`)
  if (summary.failures.length) {
    toasts.push({ title: `${countLabel(summary.failures.length)} could not be pasted`, body: summary.failures.join('; '), tone: 'danger' })
    return
  }
  if (!summary.pasted && !summary.skipped) return
  toasts.push({
    title: summary.pasted ? `${verb} ${countLabel(summary.pasted)} to ${displayPathOf(path.value)}` : 'Nothing pasted',
    body: notes.join(' · ') || undefined,
    tone: 'success',
  })
}

// --- Uploads (chunked, sequential, with per-file progress) ---

const filePicker = ref<HTMLInputElement>()
const uploads = useFileUploads({
  siteId,
  path,
  canWriteFiles,
  canDrop: computed(() => canMutateHere.value && !editorOpen.value),
  onUploaded: refetchListing,
})

function onFilesChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const chosen = Array.from(input.files ?? [])
  input.value = ''
  if (chosen.length) void uploads.queue(chosen)
}

// --- Editor (in-page pane beside a slim file list, FastPanel-style) ---

const editorOpen = ref(false)
const editorPane = ref<InstanceType<typeof EditorPane>>()
const openTabPaths = ref<string[]>([])
const activeTabPath = ref('')

const unregisterUnsavedEditor = registerUnsavedChanges(() => editorPane.value?.hasDirtyTabs() ?? false)
onScopeDispose(unregisterUnsavedEditor)

const isPathReadOnly = (target: string) => !canWriteFiles.value || !isWritablePath(target)

async function openFileInEditor(entry: FileEntry) {
  table.selectedNames.value = [entry.name]
  editorOpen.value = true
  await nextTick()
  editorPane.value?.open(entryPath(entry))
}

function onEditorTabs(paths: string[], active: string) {
  openTabPaths.value = paths
  activeTabPath.value = active
}

function closeEditor() {
  editorOpen.value = false
  openTabPaths.value = []
  activeTabPath.value = ''
}

function openEntry(entry: FileEntry) {
  if (entry.kind === 'dir') setPath(entryPath(entry))
  else if (entry.kind === 'file') void openFileInEditor(entry)
}

watch(siteId, () => {
  table.reset()
  jobs.sizeResult.value = undefined
  // Editor buffers belong to the previous site; unmounting drops them.
  editorOpen.value = false
})
watch(path, () => table.reset())

// --- Dialogs ---

const nameDialog = ref<NameDialogKind>()
const chmodOpen = ref(false)
const jobDialog = ref<'archive' | 'extract'>()
/** Captured when the dialog opens so a background refetch cannot swap it out. */
const dialogEntry = ref<FileEntry>()
const deleteTargets = ref<FileEntry[]>([])

const jobDialogTitle = computed(() => {
  if (jobDialog.value === 'extract') return `Extract ${dialogEntry.value?.name ?? ''}`
  const solo = table.soloEntry.value
  return solo ? `Pack ${solo.name} into archive` : `Archive ${countLabel(table.selectedEntries.value.length)}`
})
const jobDialogTarget = computed(() =>
  jobDialog.value === 'extract'
    ? canMutateHere.value
      ? path.value
      : 'tmp'
    : `backups/${defaultArchiveName()}`,
)

function closeDialogs() {
  nameDialog.value = undefined
  chmodOpen.value = false
  jobDialog.value = undefined
  dialogEntry.value = undefined
}

function submitArchive(target: string) {
  const paths = table.selectedEntries.value.map(entryPath)
  closeDialogs()
  if (canWriteFiles.value) jobs.archive(paths, target, () => (table.selectedNames.value = []))
}

function submitExtract(targetDir: string) {
  const target = dialogEntry.value
  closeDialogs()
  if (target && canWriteFiles.value) jobs.extract(entryPath(target), targetDir)
}

// --- Delete (one entry or the whole selection through the same confirm) ---

async function onDeleted(names: string[]) {
  table.selectedNames.value = table.selectedNames.value.filter((name) => !names.includes(name))
  await refetchListing()
}

// --- Copy path (site-relative, leading slash) ---

async function copyPath(entry: FileEntry) {
  try {
    await navigator.clipboard.writeText(`/${entryPath(entry)}`)
    toasts.push({ title: 'Path copied', tone: 'success' })
  } catch {
    toasts.push({ title: 'The clipboard is not available', tone: 'danger' })
  }
}

// --- One dispatcher for the toolbar, the context menu and the shortcuts ---

function runAction(action: FileAction) {
  const solo = table.soloEntry.value
  switch (action) {
    case 'open':
      if (solo) openEntry(solo)
      break
    case 'edit':
      if (solo?.kind === 'file') void openFileInEditor(solo)
      break
    case 'copy-path':
      if (solo) void copyPath(solo)
      break
    case 'compute-size':
      if (solo?.kind === 'dir') jobs.computeSize(entryPath(solo))
      break
    case 'clipboard-copy':
      if (canWriteFiles.value) clipboard.copy(table.selectedNames.value)
      break
    case 'clipboard-cut':
      if (canMutateHere.value) clipboard.cut(table.selectedNames.value)
      break
    case 'paste':
      requestPaste()
      break
    case 'clipboard-clear':
      clipboard.clear()
      break
    case 'mkdir':
    case 'newfile':
      if (canMutateHere.value) {
        dialogEntry.value = undefined
        nameDialog.value = action
      }
      break
    case 'rename':
      if (canMutateHere.value && solo) {
        dialogEntry.value = solo
        nameDialog.value = 'rename'
      }
      break
    case 'chmod':
      if (canMutateHere.value && solo) {
        dialogEntry.value = solo
        chmodOpen.value = true
      }
      break
    case 'extract':
      if (canMutateHere.value && solo) {
        dialogEntry.value = solo
        jobDialog.value = 'extract'
      }
      break
    case 'archive':
      if (canWriteFiles.value && table.selectedEntries.value.length) jobDialog.value = 'archive'
      break
    case 'delete':
      if (canMutateHere.value && table.selectedEntries.value.length) deleteTargets.value = [...table.selectedEntries.value]
      break
    case 'upload':
      filePicker.value?.click()
      break
    case 'clear-selection':
      table.selectedNames.value = []
      break
  }
}

// --- Clipboard keyboard shortcuts over the listing ---

const anyDialogOpen = computed(
  () => Boolean(nameDialog.value) || chmodOpen.value || Boolean(jobDialog.value) || conflictOpen.value || deleteTargets.value.length > 0,
)

function onKeydown(event: KeyboardEvent) {
  if (!(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey) return
  if (editorOpen.value || anyDialogOpen.value || !selectedSite.value) return
  // Never shadow the browser's own copy of a text selection or a form field.
  const target = event.target
  if (target instanceof Element && target.closest('input, textarea, select, [contenteditable="true"]')) return
  if (window.getSelection()?.toString()) return

  const key = event.key.toLowerCase()
  const action: FileAction | undefined =
    key === 'c' ? 'clipboard-copy' : key === 'x' ? 'clipboard-cut' : key === 'v' ? 'paste' : undefined
  if (!action) return
  if (action !== 'paste' && !table.selectedNames.value.length) return
  if (action === 'paste' && !clipboard.canPaste.value) return
  event.preventDefault()
  runAction(action)
}

window.addEventListener('keydown', onKeydown)
onScopeDispose(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Files"
      description="Browse and manage files under each site's managed root. Changes are limited to public/, private/, tmp/, and backups/ — everything else is read-only."
    >
      <AppButton icon="refresh-cw" :loading="listingQuery.isFetching.value" :disabled="!selectedSite" @click="refetchListing">
        Refresh
      </AppButton>
    </PageHeader>

    <div v-if="sitesQuery.isPending.value" class="space-y-1">
      <SkeletonRow v-for="n in 3" :key="n" />
    </div>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">
      <p>The site list failed to load.</p>
      <AppButton size="sm" class="mt-2" @click="sitesQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!activeSites.length"
      icon="folder"
      title="No site to browse"
      :description="
        hasAnySites
          ? 'Files become browsable once a site reaches the active state.'
          : 'Every site gets its own managed root of files. Create a site to get started.'
      "
    >
      <template #action>
        <RouterLink
          v-if="!hasAnySites && identity.can('sites.write')"
          to="/sites/new"
          class="text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
        >
          Create a site →
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else>
      <!-- Site selector -->
      <div class="flex flex-wrap items-center gap-3">
        <div class="w-full sm:w-72">
          <Combobox v-model="siteSelection">
            <ComboboxAnchor as-child>
              <ComboboxTrigger
                aria-label="Site"
                placeholder="Select a site"
                :label="((id) => {
                  const site = activeSites.find((s) => s.id === id)
                  return site ? `${site.displayName} — ${site.primaryDomain}` : ''
                })(siteSelection)"
              />
            </ComboboxAnchor>
            <ComboboxList>
              <ComboboxInput placeholder="Search sites…" />
              <ComboboxEmpty>No sites match.</ComboboxEmpty>
              <ComboboxGroup>
                <ComboboxItem
                  v-for="site in activeSites"
                  :key="site.id"
                  :value="site.id"
                  :text-value="`${site.displayName} ${site.primaryDomain}`"
                >
                  {{ site.displayName }} — {{ site.primaryDomain }}<ComboboxItemIndicator />
                </ComboboxItem>
              </ComboboxGroup>
            </ComboboxList>
          </Combobox>
        </div>
      </div>

      <AppAlert v-if="siteId && sitesQuery.isSuccess.value && !selectedSite" tone="warning">
        The selected site is not active or not accessible. Choose another site.
      </AppAlert>

      <JobFailureNotice v-if="jobs.runner.error.value" v-bind="jobs.runner.failureProps.value" />
      <JobProgress
        v-if="jobs.runner.progress.value"
        :event="jobs.runner.progress.value"
        v-bind="jobs.runner.progressProps.value"
      />

      <AppAlert v-if="jobs.sizeResult.value" tone="info" :title="`Folder size — /${jobs.sizeResult.value.path}`">
        <p>
          {{ formatBytes(jobs.sizeResult.value.bytes) }} · {{ jobs.sizeResult.value.files.toLocaleString() }} files ·
          {{ jobs.sizeResult.value.dirs.toLocaleString() }} folders<template v-if="jobs.sizeResult.value.truncated">
            · count truncated</template
          >
        </p>
        <AppButton size="sm" class="mt-2" @click="jobs.sizeResult.value = undefined">Dismiss</AppButton>
      </AppAlert>

      <UploadQueue
        v-if="uploads.uploads.value.length"
        :uploads="uploads.uploads.value"
        :percent="uploads.percent"
        @retry="uploads.retry"
        @dismiss="uploads.dismiss"
        @clear-finished="uploads.clearFinished"
      />

      <!-- File manager surface: toolbar, clipboard tray, path bar, then listing or editor -->
      <div
        v-if="selectedSite"
        class="relative overflow-hidden rounded-2xl border border-outline bg-surface/40"
        :class="uploads.dragActive.value ? 'ring-2 ring-accent-400/60' : ''"
        v-on="uploads.dragHandlers"
      >
        <FileToolbar
          v-model:name-filter="table.nameFilter.value"
          :selected-entries="table.selectedEntries.value"
          :solo-entry="table.soloEntry.value"
          :site-id="siteId"
          :directory="path"
          :can-mutate-here="canMutateHere"
          :can-write-files="canWriteFiles"
          :read-only-hint="readOnlyHint"
          :visible-count="table.visibleItems.value.length"
          :total-count="items.length"
          @action="runAction"
        />

        <ClipboardBar
          v-if="clipboard.clipboard.value"
          :mode="clipboard.clipboard.value.mode"
          :label="clipboard.label.value"
          :source-directory="clipboard.clipboard.value.directory"
          :can-paste="clipboard.canPaste.value"
          :blocked-reason="clipboard.blockedReason.value"
          :busy="clipboard.busy.value"
          @paste="requestPaste"
          @clear="clipboard.clear"
        />

        <FilePathBar :path="path" @navigate="setPath" />

        <!-- Editor mode: slim file list beside the tabbed editor -->
        <div v-if="editorOpen" class="flex h-[calc(100dvh-16rem)] min-h-105">
          <aside class="w-56 shrink-0 overflow-y-auto border-r border-outline bg-surface/30">
            <button
              v-if="path !== '.'"
              class="flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] text-ink-secondary transition-colors hover:bg-white/[0.04] hover:text-ink"
              @click="setPath(parentOf(path))"
            >
              <AppIcon name="corner-left-up" :size="14" class="shrink-0 text-ink-muted" />
              ..
            </button>
            <button
              v-for="entry in table.visibleItems.value"
              :key="entry.name"
              class="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-white/[0.04]"
              :class="activeTabPath === entryPath(entry) ? 'bg-accent-500/[0.08]' : ''"
              :disabled="entry.kind === 'symlink' || entry.kind === 'other'"
              @click="openEntry(entry)"
            >
              <AppIcon
                :name="entryIcon(entry)"
                :size="14"
                class="shrink-0"
                :class="entry.kind === 'dir' ? 'text-amber-300/90' : 'text-ink-muted'"
              />
              <span
                class="truncate text-[13px]"
                :class="openTabPaths.includes(entryPath(entry)) ? 'font-medium text-accent-200' : 'text-ink-secondary'"
              >
                {{ entry.name }}
              </span>
            </button>
          </aside>
          <EditorPane
            ref="editorPane"
            :site-id="siteId"
            :is-path-read-only="isPathReadOnly"
            @close="closeEditor"
            @saved="refetchListing"
            @tabs="onEditorTabs"
          />
        </div>

        <!-- Listing mode -->
        <div v-else>
          <div v-if="listingQuery.isPending.value" class="space-y-1 p-3">
            <SkeletonRow v-for="n in 3" :key="n" />
          </div>
          <AppAlert v-else-if="listingQuery.isError.value" tone="danger" class="m-3">
            <p>The directory listing failed to load{{ listingError ? ` — ${listingError}` : '.' }}</p>
            <AppButton size="sm" class="mt-2" @click="refetchListing">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="!items.length"
            icon="folder"
            title="Empty directory"
            :description="canMutateHere ? 'Create a folder or file, or drop files here to upload.' : 'Nothing to show at this path.'"
            class="m-3"
          />
          <template v-else>
            <AppAlert v-if="listingTruncated" tone="warning" class="mx-3 mt-3">
              This directory holds more entries than the listing limit; only the first 5000 are shown.
            </AppAlert>

            <EmptyState
              v-if="!table.visibleItems.value.length"
              icon="search"
              title="No matching entries"
              :description="`No names in this directory match “${table.nameFilter.value.trim()}”.`"
              class="m-3"
            />
            <FileTable
              v-else
              :entries="table.visibleItems.value"
              :site-id="siteId"
              :directory="path"
              :selected-names="table.selectedNames.value"
              :all-selected="table.allSelected.value"
              :some-selected="table.someSelected.value"
              :sort-key="table.sortKey.value"
              :sort-ascending="table.sortAscending.value"
              :can-mutate-here="canMutateHere"
              :can-write-files="canWriteFiles"
              :can-paste="clipboard.canPaste.value"
              @action="runAction"
              @sort="table.toggleSort"
              @toggle-all="table.toggleAll"
              @toggle-select="table.toggleSelect"
              @row-click="table.onRowClick"
              @row-open="openEntry"
              @row-context="(entry) => entry && table.adopt(entry)"
            />
          </template>
        </div>

        <div
          v-if="uploads.dragActive.value"
          class="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-2xl border-2 border-dashed border-accent-400/70 bg-canvas/70 backdrop-blur-[2px]"
        >
          <p class="flex items-center gap-2 text-sm font-medium text-ink">
            <AppIcon name="upload" :size="18" class="text-accent-300" />
            Drop files to upload to {{ displayPathOf(path) }}
          </p>
        </div>
      </div>
    </template>

    <input ref="filePicker" type="file" multiple class="hidden" @change="onFilesChosen" />

    <EntryNameDialog
      :open="Boolean(nameDialog) && canMutateHere"
      :kind="nameDialog ?? 'mkdir'"
      :site-id="siteId"
      :directory="path"
      :entry="dialogEntry"
      @close="closeDialogs"
      @done="refetchListing"
    />

    <ChmodDialog
      :open="chmodOpen && canMutateHere"
      :site-id="siteId"
      :directory="path"
      :entry="dialogEntry"
      @close="closeDialogs"
      @done="refetchListing"
    />

    <JobTargetDialog
      :open="Boolean(jobDialog) && (jobDialog === 'archive' ? canWriteFiles : canMutateHere)"
      :kind="jobDialog ?? 'archive'"
      :site-id="siteId"
      :title="jobDialogTitle"
      :initial-target="jobDialogTarget"
      @close="closeDialogs"
      @submit="(target) => (jobDialog === 'archive' ? submitArchive(target) : submitExtract(target))"
    />

    <PasteConflictDialog
      :open="conflictOpen"
      :mode="clipboard.clipboard.value?.mode ?? 'copy'"
      :conflicts="clipboard.conflicts.value"
      :total="clipboard.count.value"
      :destination="path"
      @close="conflictOpen = false"
      @resolve="runPaste"
    />

    <DeleteEntriesDialog
      :open="canMutateHere && deleteTargets.length > 0"
      :site-id="siteId"
      :directory="path"
      :entries="deleteTargets"
      @close="deleteTargets = []"
      @deleted="onDeleted"
    />
  </section>
</template>
