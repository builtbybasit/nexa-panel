<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, markRaw, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { formatBytes, formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppIcon,
  AppInput,
  AppSelect,
  Checkbox,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  EmptyState,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  ProgressBar,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

import { getJob } from '../../jobs/api'
import { listSites } from '../../sites/api'
import {
  archiveEntries,
  copyEntry,
  deleteEntry,
  directorySize,
  downloadUrl,
  extractEntry,
  listFiles,
  makeDirectory,
  moveEntry,
  uploadFile,
  writeFileContent,
  type DirectorySizeResult,
  type EntryKind,
  type FileEntry,
} from '../api'
import DirectoryPickerDialog from './DirectoryPickerDialog.vue'
import FileEditorDialog from './FileEditorDialog.vue'

// --- Site selection (persisted in the ?site= route query) ---

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const activeSites = computed(() => (sitesQuery.data.value ?? []).filter((site) => site.status === 'active'))
const hasAnySites = computed(() => (sitesQuery.data.value ?? []).length > 0)

const siteId = computed(() => (typeof route.query.site === 'string' ? route.query.site : ''))
const selectedSite = computed(() => activeSites.value.find((site) => site.id === siteId.value))

function selectSite(id: string, keepPath = false) {
  const query = { ...route.query }
  if (id) query.site = id
  else delete query.site
  if (!keepPath) delete query.path
  void router.replace({ query })
}

const siteSelection = computed<string>({
  get: () => siteId.value,
  set: (value) => selectSite(value),
})

watch(
  activeSites,
  (sites) => {
    const first = sites[0]
    if (!siteId.value && first) selectSite(first.id, true)
  },
  { immediate: true },
)

// --- Directory listing (the directory lives in the ?path= route query so deep
// links restore it and back/forward walks the browsing history) ---

const path = computed(() => (typeof route.query.path === 'string' && route.query.path ? route.query.path : '.'))
const selectedNames = ref<string[]>([])

function setPath(next: string) {
  if (next === path.value) return
  const query = { ...route.query }
  if (next === '.') delete query.path
  else query.path = next
  void router.push({ query })
}

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

function joinPath(directory: string, name: string): string {
  return directory === '.' ? name : `${directory}/${name}`
}

const entryPath = (entry: FileEntry) => joinPath(path.value, entry.name)

watch(siteId, () => {
  selectedNames.value = []
  sizeResult.value = undefined
  nameFilter.value = ''
})
watch(path, () => {
  selectedNames.value = []
  nameFilter.value = ''
})
// Entries can disappear underneath a selection (deletes, moves, refetches).
watch(items, (list) => {
  const names = new Set(list.map((entry) => entry.name))
  if (selectedNames.value.some((name) => !names.has(name))) {
    selectedNames.value = selectedNames.value.filter((name) => names.has(name))
  }
})

const displayPath = computed(() => (path.value === '.' ? '/' : `/${path.value}`))
const crumbs = computed(() => {
  if (path.value === '.') return [] as { name: string; path: string }[]
  const segments = path.value.split('/')
  return segments.map((name, index) => ({ name, path: segments.slice(0, index + 1).join('/') }))
})

// --- Write-zone guard: mutations only inside public/, private/, tmp/, backups/ ---
// The server enforces this; the UI hides mutation affordances at root depth and
// in read-only trees such as logs/.

const writeZones = new Set(['public', 'private', 'tmp', 'backups'])
const canWriteFiles = computed(() => identity.can('files.write'))
const isWritablePath = (target: string) => writeZones.has(target.split('/')[0] ?? '')
const canMutateHere = computed(
  () => canWriteFiles.value && path.value !== '.' && isWritablePath(path.value),
)
const readOnlyHint = computed(() =>
  canWriteFiles.value ? 'Navigate into public/, private/, tmp/ or backups/ to make changes' : 'Your role has read-only file access',
)

const kindIcons: Record<EntryKind, string> = { dir: 'folder', file: 'file-text', symlink: 'external-link', other: 'info' }

function entrySize(entry: FileEntry): string {
  if (entry.kind === 'dir') return '—'
  return entry.size === 0 ? '0 B' : formatBytes(entry.size)
}

function isArchiveName(name: string): boolean {
  return name.endsWith('.zip') || name.endsWith('.tar.gz') || name.endsWith('.tgz')
}

// --- In-directory name filter + column sort (client-side) ---

const nameFilter = ref('')

type SortKey = 'name' | 'size' | 'modified'
const sortKey = ref<SortKey>('name')
const sortAscending = ref(true)

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
    return a.name.localeCompare(b.name) * direction
  })
  return filtered
})

// --- Selection (bulk delete / move / archive) ---

const allSelected = computed(
  () => visibleItems.value.length > 0 && visibleItems.value.every((entry) => selectedNames.value.includes(entry.name)),
)
const someSelected = computed(
  () => !allSelected.value && visibleItems.value.some((entry) => selectedNames.value.includes(entry.name)),
)
const selectionHasDirectory = computed(() =>
  items.value.some((entry) => entry.kind === 'dir' && selectedNames.value.includes(entry.name)),
)

function toggleSelect(name: string) {
  selectedNames.value = selectedNames.value.includes(name)
    ? selectedNames.value.filter((selected) => selected !== name)
    : [...selectedNames.value, name]
}

/** Checks or unchecks only the visible (filtered) entries, preserving hidden selections. */
function toggleAll() {
  const visible = new Set(visibleItems.value.map((entry) => entry.name))
  if (allSelected.value) {
    selectedNames.value = selectedNames.value.filter((name) => !visible.has(name))
  } else {
    const merged = new Set([...selectedNames.value, ...visible])
    selectedNames.value = [...merged]
  }
}

const countLabel = (count: number) => `${count} ${count === 1 ? 'item' : 'items'}`

// --- Row menu ---

function hasActions(entry: FileEntry): boolean {
  return entry.kind === 'dir' || entry.kind === 'file' || canMutateHere.value
}

// --- Editor ---

const editorPath = ref<string>()
const editorReadOnly = computed(
  () => editorPath.value === undefined || !canWriteFiles.value || !isWritablePath(editorPath.value),
)

function openEntry(entry: FileEntry) {
  if (entry.kind === 'dir') setPath(entryPath(entry))
  else if (entry.kind === 'file') editorPath.value = entryPath(entry)
}

// --- Dialogs (mkdir / new file / rename / copy / move / delete / extract / archive / bulk) ---

type DialogKind = 'mkdir' | 'newfile' | 'rename' | 'copy' | 'move' | 'delete' | 'extract' | 'archive' | 'bulk-move' | 'bulk-delete'
const dialog = ref<DialogKind>()
const dialogTarget = ref<FileEntry>()
const dialogBusy = ref(false)
const dialogError = ref('')
const nameInput = ref('')
const destinationInput = ref('')
const overwriteInput = ref(false)

const formDialogOpen = computed(() => dialog.value !== undefined && dialog.value !== 'delete' && dialog.value !== 'bulk-delete')

const dialogTitle = computed(() => {
  switch (dialog.value) {
    case 'mkdir':
      return 'New folder'
    case 'newfile':
      return 'New file'
    case 'rename':
      return `Rename ${dialogTarget.value?.name ?? ''}`
    case 'copy':
      return `Copy ${dialogTarget.value?.name ?? ''}`
    case 'move':
      return `Move ${dialogTarget.value?.name ?? ''}`
    case 'extract':
      return `Extract ${dialogTarget.value?.name ?? ''}`
    case 'archive':
      return `Archive ${countLabel(selectedNames.value.length)}`
    case 'bulk-move':
      return `Move ${countLabel(selectedNames.value.length)}`
    default:
      return ''
  }
})

function defaultArchiveName(): string {
  const stamp = new Date().toISOString().replace(/[-:]/g, '').slice(0, 15)
  return `archive-${stamp}.tar.gz`
}

function openDialog(kind: DialogKind, entry?: FileEntry) {
  if (kind === 'archive' ? !canWriteFiles.value : !canMutateHere.value) return
  dialogTarget.value = entry
  dialogError.value = ''
  nameInput.value = kind === 'rename' ? (entry?.name ?? '') : ''
  destinationInput.value = ''
  overwriteInput.value = false
  if (kind === 'copy' || kind === 'move') destinationInput.value = entry ? entryPath(entry) : ''
  if (kind === 'extract') destinationInput.value = canMutateHere.value ? path.value : 'tmp'
  if (kind === 'archive') destinationInput.value = `backups/${defaultArchiveName()}`
  dialog.value = kind
}

function closeDialog() {
  if (dialogBusy.value) return
  dialog.value = undefined
  dialogTarget.value = undefined
}

async function runDialog(action: () => Promise<void>) {
  const kind = dialog.value
  if (!kind || (kind === 'archive' ? !canWriteFiles.value : !canMutateHere.value)) return
  dialogBusy.value = true
  dialogError.value = ''
  try {
    await action()
    dialog.value = undefined
    dialogTarget.value = undefined
    await refetchListing()
  } catch (caught) {
    dialogError.value = caught instanceof Error ? caught.message : 'The file operation failed.'
  } finally {
    dialogBusy.value = false
  }
}

const submitMkdir = () => runDialog(() => makeDirectory(siteId.value, joinPath(path.value, nameInput.value.trim())))

const submitNewFile = () =>
  runDialog(async () => {
    await writeFileContent(siteId.value, { path: joinPath(path.value, nameInput.value.trim()), content: '', expectedEtag: '' })
  })

const submitRename = () =>
  runDialog(async () => {
    const target = dialogTarget.value
    if (!target) return
    await moveEntry(siteId.value, { from: entryPath(target), to: joinPath(path.value, nameInput.value.trim()), overwrite: false })
  })

const submitCopy = () =>
  runDialog(async () => {
    const target = dialogTarget.value
    if (!target) return
    await copyEntry(siteId.value, { from: entryPath(target), to: destinationInput.value.trim() })
  })

const submitMove = () =>
  runDialog(async () => {
    const target = dialogTarget.value
    if (!target) return
    await moveEntry(siteId.value, { from: entryPath(target), to: destinationInput.value.trim(), overwrite: overwriteInput.value })
  })

const submitDelete = () =>
  runDialog(async () => {
    const target = dialogTarget.value
    if (!target) return
    await deleteEntry(siteId.value, { path: entryPath(target), recursive: target.kind === 'dir' })
  })

// --- Bulk operations: loop the per-item calls, keep failures selected ---

async function runBulk(perItem: (name: string) => Promise<void>) {
  if (!canMutateHere.value) return
  dialogBusy.value = true
  dialogError.value = ''
  const failures: string[] = []
  for (const name of [...selectedNames.value]) {
    try {
      await perItem(name)
      selectedNames.value = selectedNames.value.filter((selected) => selected !== name)
    } catch (caught) {
      failures.push(`${name}: ${caught instanceof Error ? caught.message : 'failed'}`)
    }
  }
  dialogBusy.value = false
  await refetchListing()
  if (failures.length) {
    dialogError.value = `Some items failed — ${failures.join('; ')}`
  } else {
    dialog.value = undefined
    dialogTarget.value = undefined
  }
}

function submitBulkDelete() {
  const directory = path.value
  const byName = new Map(items.value.map((entry) => [entry.name, entry]))
  void runBulk((name) =>
    deleteEntry(siteId.value, { path: joinPath(directory, name), recursive: byName.get(name)?.kind === 'dir' }),
  )
}

function submitBulkMove() {
  const directory = path.value
  const destination = destinationInput.value.trim().replace(/\/+$/, '')
  const overwrite = overwriteInput.value
  void runBulk((name) =>
    moveEntry(siteId.value, { from: joinPath(directory, name), to: joinPath(destination, name), overwrite }),
  )
}

// --- Destination folder picker (Browse… beside destination inputs) ---

const pickerOpen = ref(false)

function applyPickedDirectory(picked: string) {
  pickerOpen.value = false
  const kind = dialog.value
  if (kind === 'copy' || kind === 'move') {
    destinationInput.value = joinPath(picked, dialogTarget.value?.name ?? '')
  } else if (kind === 'archive') {
    const basename = destinationInput.value.trim().split('/').pop() || defaultArchiveName()
    destinationInput.value = joinPath(picked, basename)
  } else if (kind === 'extract' || kind === 'bulk-move') {
    destinationInput.value = picked === '.' ? '' : picked
  }
}

// --- Long-running operations (archive / extract / size) follow durable jobs ---

const runner = useJobRunner()
const sizeResult = ref<DirectorySizeResult & { path: string }>()

const selectedPaths = computed(() => selectedNames.value.map((name) => joinPath(path.value, name)))

function submitArchive() {
  if (!canWriteFiles.value) return
  const paths = selectedPaths.value
  const target = destinationInput.value.trim()
  closeDialog()
  void runner.run(async () => (await archiveEntries(siteId.value, { paths, target })).job.id, {
    onSuccess: async () => {
      selectedNames.value = []
      await refetchListing()
    },
    failureMessage: 'Archive failed',
    successToast: 'Archive created',
  })
}

function submitExtract() {
  const target = dialogTarget.value
  if (!target || !canMutateHere.value) return
  const from = entryPath(target)
  const targetDir = destinationInput.value.trim()
  closeDialog()
  void runner.run(async () => (await extractEntry(siteId.value, { path: from, targetDir })).job.id, {
    onSuccess: refetchListing,
    failureMessage: 'Extract failed',
    successToast: 'Archive extracted',
  })
}

function computeSize(entry: FileEntry) {
  const target = entryPath(entry)
  void runner.run(async () => (await directorySize(siteId.value, target)).job.id, {
    onSuccess: async (event) => {
      const result = ((await getJob(event.jobId)).result ?? {}) as Partial<DirectorySizeResult>
      sizeResult.value = {
        path: target,
        bytes: Number(result.bytes ?? 0),
        files: Number(result.files ?? 0),
        dirs: Number(result.dirs ?? 0),
        truncated: Boolean(result.truncated),
      }
    },
    failureMessage: 'Could not compute the folder size',
  })
}

// --- Uploads (chunked, sequential, with per-file progress) ---

interface UploadItem {
  file: File
  targetPath: string
  sent: number
  total: number
  status: 'uploading' | 'done' | 'failed'
  error: string
}

const uploads = ref<UploadItem[]>([])
const filePicker = ref<HTMLInputElement>()

async function queueUploads(files: File[]) {
  if (!canMutateHere.value) return
  const directory = path.value
  const queued: UploadItem[] = []
  for (const file of files) {
    uploads.value.push({
      file: markRaw(file),
      targetPath: joinPath(directory, file.name),
      sent: 0,
      total: file.size,
      status: 'uploading',
      error: '',
    })
    const item = uploads.value[uploads.value.length - 1]
    if (item) queued.push(item)
  }
  for (const item of queued) await startUpload(item, false)
}

function onFilesChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const chosen = Array.from(input.files ?? [])
  input.value = ''
  if (chosen.length) void queueUploads(chosen)
}

async function startUpload(item: UploadItem, overwrite: boolean) {
  if (!canWriteFiles.value || !isWritablePath(item.targetPath)) {
    item.status = 'failed'
    item.error = 'Your account cannot upload to this path.'
    return
  }
  item.status = 'uploading'
  item.error = ''
  item.sent = 0
  try {
    await uploadFile(siteId.value, item.file, item.targetPath, overwrite, (sent, total) => {
      item.sent = sent
      item.total = total
    })
    item.status = 'done'
    await refetchListing()
  } catch (caught) {
    item.status = 'failed'
    item.error = caught instanceof Error ? caught.message : 'The upload failed.'
  }
}

function uploadPercent(item: UploadItem): number {
  if (item.status === 'done') return 100
  return Math.round((item.sent / Math.max(item.total, 1)) * 100)
}

/** Removes everything that is no longer transferring, failed rows included. */
function clearFinishedUploads() {
  uploads.value = uploads.value.filter((item) => item.status === 'uploading')
}

function dismissUpload(item: UploadItem) {
  uploads.value = uploads.value.filter((candidate) => candidate !== item)
}

const uploadTones = { uploading: 'info', done: 'success', failed: 'danger' } as const
const uploadLabels = { uploading: 'Uploading', done: 'Uploaded', failed: 'Failed' } as const

// --- Drag-and-drop upload onto the listing ---

const dragDepth = ref(0)
const dragActive = computed(() => dragDepth.value > 0 && canMutateHere.value)

function dragHasFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes('Files')
}

function onDragEnter(event: DragEvent) {
  if (!dragHasFiles(event)) return
  event.preventDefault()
  dragDepth.value += 1
}

function onDragOver(event: DragEvent) {
  if (!dragHasFiles(event)) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = canMutateHere.value ? 'copy' : 'none'
}

function onDragLeave(event: DragEvent) {
  if (!dragHasFiles(event)) return
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

function onDrop(event: DragEvent) {
  if (!dragHasFiles(event)) return
  event.preventDefault()
  dragDepth.value = 0
  if (!canMutateHere.value) return
  const files = Array.from(event.dataTransfer?.files ?? [])
  if (files.length) void queueUploads(files)
}
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
          to="/sites?create=1"
          class="text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
        >
          Create a site →
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else>
      <!-- Site selector + breadcrumb path -->
      <div class="flex flex-wrap items-center gap-3">
        <div class="w-full sm:w-72">
          <AppSelect v-model="siteSelection" aria-label="Site">
            <option v-for="site in activeSites" :key="site.id" :value="site.id">
              {{ site.displayName }} — {{ site.primaryDomain }}
            </option>
          </AppSelect>
        </div>
        <nav class="flex min-w-0 flex-1 items-center gap-0.5 font-mono text-[13px]" aria-label="Path">
          <button
            class="rounded px-1.5 py-0.5 transition-colors hover:bg-white/[0.05] hover:text-ink"
            :class="path === '.' ? 'text-ink' : 'text-ink-secondary'"
            @click="setPath('.')"
          >
            /
          </button>
          <template v-for="(crumb, index) in crumbs" :key="crumb.path">
            <AppIcon v-if="index > 0" name="chevron-right" :size="12" class="shrink-0 text-ink-muted" />
            <button
              class="truncate rounded px-1.5 py-0.5 transition-colors hover:bg-white/[0.05] hover:text-ink"
              :class="index === crumbs.length - 1 ? 'text-ink' : 'text-ink-secondary'"
              @click="setPath(crumb.path)"
            >
              {{ crumb.name }}
            </button>
          </template>
        </nav>
      </div>

      <AppAlert v-if="siteId && sitesQuery.isSuccess.value && !selectedSite" tone="warning">
        The selected site is not active or not accessible. Choose another site.
      </AppAlert>

      <JobFailureNotice
        v-if="runner.error.value"
        :message="runner.error.value"
        v-bind="runner.jobId.value !== undefined ? { jobId: runner.jobId.value } : {}"
      />
      <JobProgress
        v-if="runner.progress.value"
        :event="runner.progress.value"
        :messages="runner.messages.value"
        v-bind="runner.startedAtMs.value !== undefined ? { startedAtMs: runner.startedAtMs.value } : {}"
      />

      <AppAlert v-if="sizeResult" tone="info" :title="`Folder size — /${sizeResult.path}`">
        <p>
          {{ formatBytes(sizeResult.bytes) }} · {{ sizeResult.files.toLocaleString() }} files ·
          {{ sizeResult.dirs.toLocaleString() }} folders<template v-if="sizeResult.truncated"> · count truncated</template>
        </p>
        <AppButton size="sm" class="mt-2" @click="sizeResult = undefined">Dismiss</AppButton>
      </AppAlert>

      <!-- Upload progress -->
      <AppCard v-if="uploads.length" eyebrow="Transfers" title="Uploads">
        <template #actions>
          <AppButton size="sm" @click="clearFinishedUploads">Clear finished</AppButton>
        </template>
        <ul class="space-y-3">
          <li v-for="(item, index) in uploads" :key="`${item.targetPath}-${index}`" class="space-y-1.5">
            <div class="flex items-center justify-between gap-3 text-[13px]">
              <span class="min-w-0 truncate font-mono text-ink">{{ item.targetPath }}</span>
              <span class="flex shrink-0 items-center gap-2">
                <span class="font-mono text-[11px] text-ink-muted">{{ formatBytes(item.sent) }} / {{ formatBytes(item.total) }}</span>
                <AppButton v-if="item.status === 'failed'" size="sm" @click="startUpload(item, true)">Retry and overwrite</AppButton>
                <StatusPill :tone="uploadTones[item.status]" :label="uploadLabels[item.status]" :pulse="item.status === 'uploading'" />
                <AppButton
                  v-if="item.status !== 'uploading'"
                  size="sm"
                  variant="ghost"
                  icon="x"
                  :aria-label="`Dismiss ${item.targetPath}`"
                  @click="dismissUpload(item)"
                />
              </span>
            </div>
            <ProgressBar :value="uploadPercent(item)" :tone="item.status === 'failed' ? 'danger' : 'accent'" />
            <p v-if="item.error" class="text-xs text-rose-300">{{ item.error }}</p>
          </li>
        </ul>
      </AppCard>

      <!-- Directory listing (also a drop target for uploads) -->
      <div v-if="selectedSite" class="relative" @dragenter="onDragEnter" @dragover="onDragOver" @dragleave="onDragLeave" @drop="onDrop">
        <AppCard eyebrow="Directory" :title="displayPath" flush :class="dragActive ? 'ring-2 ring-accent-400/60' : ''">
          <template #actions>
            <StatusPill v-if="!canMutateHere" tone="neutral" label="Read-only path" :description="readOnlyHint" :pulse="false" />
            <template v-else>
              <AppButton size="sm" icon="folder" @click="openDialog('mkdir')">New folder</AppButton>
              <AppButton size="sm" icon="file-text" @click="openDialog('newfile')">New file</AppButton>
              <AppButton size="sm" icon="upload" @click="filePicker?.click()">Upload</AppButton>
            </template>
          </template>

          <div class="px-3 pb-3 sm:px-4 sm:pb-4">
            <p v-if="!canMutateHere" class="mx-2 mb-3 text-xs text-ink-muted">{{ readOnlyHint }}.</p>

            <div v-if="listingQuery.isPending.value" class="space-y-1">
              <SkeletonRow v-for="n in 3" :key="n" />
            </div>
            <AppAlert v-else-if="listingQuery.isError.value" tone="danger" class="m-2">
              <p>The directory listing failed to load{{ listingError ? ` — ${listingError}` : '.' }}</p>
              <AppButton size="sm" class="mt-2" @click="refetchListing">Retry</AppButton>
            </AppAlert>
            <EmptyState
              v-else-if="!items.length"
              icon="folder"
              title="Empty directory"
              :description="canMutateHere ? 'Create a folder or file, or drop files here to upload.' : 'Nothing to show at this path.'"
              class="m-2"
            />
            <template v-else>
              <AppAlert v-if="listingTruncated" tone="warning" class="mx-2 mb-3">
                This directory holds more entries than the listing limit; only the first 5000 are shown.
              </AppAlert>

              <!-- Bulk actions -->
              <div
                v-if="selectedNames.length"
                class="mx-2 mb-3 flex flex-wrap items-center gap-2 rounded-xl border border-accent-400/25 bg-accent-500/[0.06] px-3 py-2"
              >
                <span class="text-[13px] font-medium text-ink" aria-live="polite">{{ selectedNames.length }} selected</span>
                <AppButton size="sm" variant="ghost" @click="selectedNames = []">Clear</AppButton>
                <span class="ml-auto flex flex-wrap items-center gap-2">
                  <AppButton v-if="canWriteFiles" size="sm" icon="archive" @click="openDialog('archive')">
                    Archive
                  </AppButton>
                  <AppButton v-if="canMutateHere" size="sm" icon="folder-open" @click="openDialog('bulk-move')">Move</AppButton>
                  <AppButton v-if="canMutateHere" size="sm" variant="danger" icon="trash" @click="openDialog('bulk-delete')">
                    Delete
                  </AppButton>
                </span>
              </div>

              <!-- Name filter -->
              <div class="mx-2 mb-3 flex flex-wrap items-center gap-2">
                <div class="w-full sm:w-64">
                  <AppInput v-model="nameFilter" type="search" placeholder="Filter by name" aria-label="Filter entries by name" />
                </div>
                <span v-if="nameFilter" class="text-xs text-ink-muted" aria-live="polite">
                  {{ visibleItems.length }} of {{ items.length }} shown
                </span>
              </div>

              <EmptyState
                v-if="!visibleItems.length"
                icon="search"
                title="No matching entries"
                :description="`No names in this directory match “${nameFilter.trim()}”.`"
                class="m-2"
              />
              <div v-else class="overflow-x-auto">
                <table class="w-full border-collapse text-left">
                  <thead>
                    <tr class="border-b border-outline">
                      <th class="w-8 px-3 py-2.5">
                        <Checkbox
                          :model-value="allSelected ? true : someSelected ? 'indeterminate' : false"
                          aria-label="Select all entries"
                          @update:model-value="() => toggleAll()"
                        />
                      </th>
                      <th class="px-3 py-2.5" :aria-sort="ariaSort('name')">
                        <button
                          type="button"
                          class="flex items-center gap-1 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase transition-colors hover:text-ink"
                          @click="toggleSort('name')"
                        >
                          Name
                          <AppIcon v-if="sortKey === 'name'" :name="sortAscending ? 'chevron-up' : 'chevron-down'" :size="12" />
                        </button>
                      </th>
                      <th class="px-3 py-2.5" :aria-sort="ariaSort('size')">
                        <button
                          type="button"
                          class="flex items-center gap-1 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase transition-colors hover:text-ink"
                          @click="toggleSort('size')"
                        >
                          Size
                          <AppIcon v-if="sortKey === 'size'" :name="sortAscending ? 'chevron-up' : 'chevron-down'" :size="12" />
                        </button>
                      </th>
                      <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Mode</th>
                      <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Owner</th>
                      <th class="px-3 py-2.5" :aria-sort="ariaSort('modified')">
                        <button
                          type="button"
                          class="flex items-center gap-1 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase transition-colors hover:text-ink"
                          @click="toggleSort('modified')"
                        >
                          Modified
                          <AppIcon v-if="sortKey === 'modified'" :name="sortAscending ? 'chevron-up' : 'chevron-down'" :size="12" />
                        </button>
                      </th>
                      <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-outline">
                    <tr v-for="entry in visibleItems" :key="entry.name">
                      <td class="px-3 py-2.5">
                        <Checkbox
                          :model-value="selectedNames.includes(entry.name)"
                          :aria-label="`Select ${entry.name}`"
                          @update:model-value="() => toggleSelect(entry.name)"
                        />
                      </td>
                      <td class="max-w-xs px-3 py-2.5">
                        <button
                          class="flex w-full min-w-0 items-center gap-2.5 text-left disabled:cursor-default"
                          :disabled="entry.kind === 'symlink' || entry.kind === 'other'"
                          @click="openEntry(entry)"
                        >
                          <AppIcon
                            :name="kindIcons[entry.kind]"
                            :size="16"
                            class="shrink-0"
                            :class="entry.kind === 'dir' ? 'text-accent-300' : 'text-ink-muted'"
                          />
                          <span class="truncate text-[13px] font-medium text-ink">{{ entry.name }}</span>
                        </button>
                      </td>
                      <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary">{{ entrySize(entry) }}</td>
                      <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary">{{ entry.mode }}</td>
                      <td class="px-3 py-2.5 text-xs whitespace-nowrap text-ink-secondary">{{ entry.owner }}:{{ entry.group }}</td>
                      <td class="px-3 py-2.5 text-xs whitespace-nowrap text-ink-secondary">{{ formatDateTime(entry.modifiedAt) }}</td>
                      <td class="px-3 py-2.5 text-right">
                        <DropdownMenu v-if="hasActions(entry)">
                          <DropdownMenuTrigger as-child>
                            <AppButton
                              size="sm"
                              variant="ghost"
                              icon="more-horizontal"
                              :aria-label="`Actions for ${entry.name}`"
                            />
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem v-if="entry.kind === 'dir'" @select="openEntry(entry)">Open</DropdownMenuItem>
                            <DropdownMenuItem v-if="entry.kind === 'file'" @select="openEntry(entry)">Edit</DropdownMenuItem>
                            <DropdownMenuItem v-if="entry.kind === 'file'" as-child>
                              <a :href="downloadUrl(siteId, entryPath(entry))">Download</a>
                            </DropdownMenuItem>
                            <DropdownMenuItem v-if="entry.kind === 'dir'" @select="computeSize(entry)">
                              Compute size
                            </DropdownMenuItem>
                            <template v-if="canMutateHere">
                              <DropdownMenuSeparator />
                              <DropdownMenuItem @select="openDialog('rename', entry)">Rename…</DropdownMenuItem>
                              <DropdownMenuItem @select="openDialog('copy', entry)">Copy to…</DropdownMenuItem>
                              <DropdownMenuItem @select="openDialog('move', entry)">Move to…</DropdownMenuItem>
                              <DropdownMenuItem
                                v-if="entry.kind === 'file' && isArchiveName(entry.name)"
                                @select="openDialog('extract', entry)"
                              >
                                Extract…
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                class="text-rose-300 data-[highlighted]:bg-rose-500/10 data-[highlighted]:text-rose-300"
                                @select="openDialog('delete', entry)"
                              >
                                Delete…
                              </DropdownMenuItem>
                            </template>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </template>
          </div>
        </AppCard>
        <div
          v-if="dragActive"
          class="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-2xl border-2 border-dashed border-accent-400/70 bg-canvas/70 backdrop-blur-[2px]"
        >
          <p class="flex items-center gap-2 text-sm font-medium text-ink">
            <AppIcon name="upload" :size="18" class="text-accent-300" />
            Drop files to upload to {{ displayPath }}
          </p>
        </div>
      </div>
    </template>

    <input ref="filePicker" type="file" multiple class="hidden" @change="onFilesChosen" />

    <!-- Form dialogs -->
    <AppDialog
      :open="formDialogOpen && (dialog === 'archive' ? canWriteFiles : canMutateHere)"
      :title="dialogTitle"
      @close="closeDialog"
    >
      <form v-if="dialog === 'mkdir'" class="space-y-4" @submit.prevent="submitMkdir">
        <FormField label="Name" :hint="`Created inside ${displayPath}. Nested paths such as a/b are allowed.`">
          <AppInput v-model="nameInput" autocomplete="off" required />
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Create folder</AppButton>
        </div>
      </form>

      <form v-else-if="dialog === 'newfile'" class="space-y-4" @submit.prevent="submitNewFile">
        <FormField label="Name" :hint="`An empty file is created inside ${displayPath}; click it to edit.`">
          <AppInput v-model="nameInput" autocomplete="off" required />
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Create file</AppButton>
        </div>
      </form>

      <form v-else-if="dialog === 'rename' && dialogTarget" class="space-y-4" @submit.prevent="submitRename">
        <FormField label="New name" :hint="`Stays inside ${displayPath}.`">
          <AppInput v-model="nameInput" autocomplete="off" required />
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Rename</AppButton>
        </div>
      </form>

      <form v-else-if="dialog === 'copy' && dialogTarget" class="space-y-4" @submit.prevent="submitCopy">
        <FormField label="Destination path" hint="Site-relative path inside public/, private/, tmp/, or backups/.">
          <div class="flex gap-2">
            <AppInput v-model="destinationInput" class="font-mono" autocomplete="off" required />
            <AppButton icon="folder-open" @click="pickerOpen = true">Browse…</AppButton>
          </div>
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Copy</AppButton>
        </div>
      </form>

      <form v-else-if="dialog === 'move' && dialogTarget" class="space-y-4" @submit.prevent="submitMove">
        <FormField label="Destination path" hint="Site-relative path inside public/, private/, tmp/, or backups/.">
          <div class="flex gap-2">
            <AppInput v-model="destinationInput" class="font-mono" autocomplete="off" required />
            <AppButton icon="folder-open" @click="pickerOpen = true">Browse…</AppButton>
          </div>
        </FormField>
        <label class="flex cursor-pointer items-center gap-2.5 text-[13px] text-ink-secondary">
          <Checkbox v-model="overwriteInput" />
          Overwrite the destination if it exists
        </label>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Move</AppButton>
        </div>
      </form>

      <form v-else-if="dialog === 'extract' && dialogTarget" class="space-y-4" @submit.prevent="submitExtract">
        <FormField
          label="Target directory"
          hint="Site-relative directory inside public/, private/, tmp/, or backups/. It is created if missing."
        >
          <div class="flex gap-2">
            <AppInput v-model="destinationInput" class="font-mono" autocomplete="off" required />
            <AppButton icon="folder-open" @click="pickerOpen = true">Browse…</AppButton>
          </div>
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Extract</AppButton>
        </div>
      </form>

      <form v-else-if="dialog === 'archive'" class="space-y-4" @submit.prevent="submitArchive">
        <FormField label="Target archive" hint="A .tar.gz path inside public/, private/, tmp/, or backups/.">
          <div class="flex gap-2">
            <AppInput v-model="destinationInput" class="font-mono" autocomplete="off" pattern=".+\.tar\.gz" required />
            <AppButton icon="folder-open" @click="pickerOpen = true">Browse…</AppButton>
          </div>
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Create archive</AppButton>
        </div>
      </form>

      <form v-else-if="dialog === 'bulk-move'" class="space-y-4" @submit.prevent="submitBulkMove">
        <FormField label="Destination folder" hint="Each selected item moves into this folder. Site-relative, inside public/, private/, tmp/, or backups/.">
          <div class="flex gap-2">
            <AppInput v-model="destinationInput" class="font-mono" autocomplete="off" required />
            <AppButton icon="folder-open" @click="pickerOpen = true">Browse…</AppButton>
          </div>
        </FormField>
        <label class="flex cursor-pointer items-center gap-2.5 text-[13px] text-ink-secondary">
          <Checkbox v-model="overwriteInput" />
          Overwrite destinations that already exist
        </label>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy">Move {{ countLabel(selectedNames.length) }}</AppButton>
        </div>
      </form>
    </AppDialog>

    <!-- Delete confirmations -->
    <AppConfirmDialog
      :open="canMutateHere && dialog === 'delete'"
      title="Delete entry"
      confirm-label="Delete"
      v-bind="dialogTarget?.kind === 'dir' ? { typeToConfirm: dialogTarget.name } : {}"
      :busy="dialogBusy"
      @confirm="submitDelete"
      @close="closeDialog"
    >
      <p>
        Delete <strong class="font-mono font-semibold text-ink">{{ dialogTarget?.name }}</strong
        ><template v-if="dialogTarget?.kind === 'dir'"> and everything inside it</template>? This cannot be undone.
      </p>
      <AppAlert v-if="dialogError" tone="danger" class="mt-3">{{ dialogError }}</AppAlert>
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="canMutateHere && dialog === 'bulk-delete'"
      :title="`Delete ${countLabel(selectedNames.length)}`"
      :confirm-label="`Delete ${countLabel(selectedNames.length)}`"
      v-bind="selectionHasDirectory ? { typeToConfirm: 'delete' } : {}"
      :busy="dialogBusy"
      @confirm="submitBulkDelete"
      @close="closeDialog"
    >
      <p>
        Delete the {{ countLabel(selectedNames.length) }} selected in
        <span class="font-mono text-ink">{{ displayPath }}</span
        ><template v-if="selectionHasDirectory">, including everything inside the selected folders</template>? This cannot be
        undone.
      </p>
      <AppAlert v-if="dialogError" tone="danger" class="mt-3">{{ dialogError }}</AppAlert>
    </AppConfirmDialog>

    <DirectoryPickerDialog :open="pickerOpen" :site-id="siteId" @close="pickerOpen = false" @select="applyPickedDirectory" />

    <FileEditorDialog
      v-if="editorPath && selectedSite"
      :site-id="siteId"
      :path="editorPath"
      :read-only="editorReadOnly"
      @close="editorPath = undefined"
      @saved="refetchListing"
    />
  </section>
</template>
