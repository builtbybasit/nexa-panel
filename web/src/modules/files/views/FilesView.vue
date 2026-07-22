<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, markRaw, nextTick, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { useToasts } from '@/shared/composables/useToasts'
import { formatBytes, formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppIcon,
  AppInput,
  Checkbox,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuTrigger,
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

import { getJob } from '../../jobs/api'
import { listSites } from '../../sites/api'
import {
  archiveEntries,
  changeMode,
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
  type FileEntry,
} from '../api'
import DirectoryPickerDialog from './DirectoryPickerDialog.vue'
import EditorPane from './EditorPane.vue'

// --- Site selection (persisted in the ?site= route query) ---

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const toasts = useToasts()

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
  // Editor buffers belong to the previous site; unmounting drops them.
  editorOpen.value = false
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
const parentPath = computed(() => {
  if (path.value === '.') return '.'
  const segments = path.value.split('/')
  return segments.length > 1 ? segments.slice(0, -1).join('/') : '.'
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

// --- Entry presentation: icon, type label, size, octal access ---

const archiveExtensions = new Set(['zip', 'gz', 'tgz', 'tar', 'bz2', 'xz', 'rar', '7z'])
const imageExtensions = new Set(['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico', 'avif', 'bmp'])
const codeExtensions = new Set([
  'js', 'mjs', 'cjs', 'jsx', 'ts', 'tsx', 'json', 'html', 'htm', 'vue', 'xml', 'css', 'scss', 'less',
  'php', 'py', 'rb', 'go', 'rs', 'java', 'c', 'h', 'cpp', 'cs', 'sh', 'bash', 'yml', 'yaml', 'sql', 'lua',
])

function extensionOf(name: string): string {
  return name.includes('.') ? (name.split('.').pop() ?? '').toLowerCase() : ''
}

function entryIcon(entry: FileEntry): string {
  if (entry.kind === 'dir') return 'folder'
  if (entry.kind === 'symlink') return 'external-link'
  if (entry.kind === 'other') return 'info'
  const ext = extensionOf(entry.name)
  if (isArchiveName(entry.name) || archiveExtensions.has(ext)) return 'archive'
  if (imageExtensions.has(ext)) return 'image'
  if (codeExtensions.has(ext)) return 'file-code-2'
  return 'file-text'
}

function entryTypeLabel(entry: FileEntry): string {
  if (entry.kind === 'dir') return 'Folder'
  if (entry.kind === 'symlink') return 'Symlink'
  if (entry.kind === 'other') return 'Special'
  if (isArchiveName(entry.name) || archiveExtensions.has(extensionOf(entry.name))) return 'Archive'
  if (imageExtensions.has(extensionOf(entry.name))) return 'Image'
  const ext = extensionOf(entry.name)
  return ext && ext.length <= 4 ? ext.toUpperCase() : 'File'
}

function entrySize(entry: FileEntry): string {
  if (entry.kind === 'dir') return '—'
  return entry.size === 0 ? '0 B' : formatBytes(entry.size)
}

/** Converts a symbolic permission string such as `rwxr-x---` to octal `750`. */
function symbolicToOctal(mode: string): string {
  if (mode.length !== 9) return ''
  const digit = (triplet: string) =>
    (triplet[0] !== '-' ? 4 : 0) + (triplet[1] !== '-' ? 2 : 0) + (triplet[2] !== '-' ? 1 : 0)
  return `${digit(mode.slice(0, 3))}${digit(mode.slice(3, 6))}${digit(mode.slice(6, 9))}`
}

const entryAccess = (entry: FileEntry) => symbolicToOctal(entry.mode) || entry.mode

function isArchiveName(name: string): boolean {
  return name.endsWith('.zip') || name.endsWith('.tar.gz') || name.endsWith('.tgz')
}

// --- In-directory name filter + column sort (client-side) ---

const nameFilter = ref('')

type SortKey = 'name' | 'type' | 'size' | 'modified'
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

const sortableColumns: { key: SortKey; label: string }[] = [
  { key: 'name', label: 'Name' },
  { key: 'type', label: 'Type' },
  { key: 'size', label: 'Size' },
]

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

// --- Selection: rows select on click (FastPanel-style); checkboxes multi-select ---

const lastClickedName = ref('')

const selectedEntries = computed(() =>
  items.value.filter((entry) => selectedNames.value.includes(entry.name)),
)
/** The single selected entry, when exactly one row is selected. */
const soloEntry = computed(() => (selectedEntries.value.length === 1 ? selectedEntries.value[0] : undefined))

const allSelected = computed(
  () => visibleItems.value.length > 0 && visibleItems.value.every((entry) => selectedNames.value.includes(entry.name)),
)
const someSelected = computed(
  () => !allSelected.value && visibleItems.value.some((entry) => selectedNames.value.includes(entry.name)),
)
const selectionHasDirectory = computed(() => selectedEntries.value.some((entry) => entry.kind === 'dir'))

function toggleSelect(name: string) {
  selectedNames.value = selectedNames.value.includes(name)
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

function onRowDoubleClick(entry: FileEntry) {
  openEntry(entry)
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

// --- Right-click context menu (single trigger + row delegation) ---

/** The row a right-click landed on; drives the single shared context menu. */
const activeEntry = ref<FileEntry>()
const activeMenuEntry = computed(() => activeEntry.value)

/** Resolve which row (if any) a right-click hit before Reka opens the menu. */
function onRowContextMenu(event: MouseEvent) {
  const row = (event.target as HTMLElement).closest('tr[data-name]')
  const name = row?.getAttribute('data-name')
  const entry = name ? visibleItems.value.find((candidate) => candidate.name === name) : undefined
  activeEntry.value = entry
  // Right-click adopts the row into the selection so the toolbar matches the menu.
  if (entry && !selectedNames.value.includes(entry.name)) {
    selectedNames.value = [entry.name]
    lastClickedName.value = entry.name
  }
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

// --- Editor (in-page pane beside a slim file list, FastPanel-style) ---

const editorOpen = ref(false)
const editorPane = ref<InstanceType<typeof EditorPane>>()
const openTabPaths = ref<string[]>([])
const activeTabPath = ref('')

const isPathReadOnly = (target: string) => !canWriteFiles.value || !isWritablePath(target)

async function openFileInEditor(entry: FileEntry) {
  selectedNames.value = [entry.name]
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

// --- Dialogs (mkdir / new file / rename / copy / move / chmod / delete / extract / archive / bulk) ---

type DialogKind =
  | 'mkdir'
  | 'newfile'
  | 'rename'
  | 'copy'
  | 'move'
  | 'chmod'
  | 'delete'
  | 'extract'
  | 'archive'
  | 'bulk-move'
  | 'bulk-delete'
const dialog = ref<DialogKind>()
const dialogTarget = ref<FileEntry>()
const dialogBusy = ref(false)
const dialogError = ref('')
const nameInput = ref('')
const destinationInput = ref('')
const overwriteInput = ref(false)
const modeInput = ref('644')

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
    case 'chmod':
      return `Permissions — ${dialogTarget.value?.name ?? ''}`
    case 'extract':
      return `Extract ${dialogTarget.value?.name ?? ''}`
    case 'archive':
      return dialogTarget.value ? `Pack ${dialogTarget.value.name} into archive` : `Archive ${countLabel(selectedNames.value.length)}`
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
  if (kind === 'chmod') modeInput.value = entry ? symbolicToOctal(entry.mode) || '644' : '644'
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

// --- Permissions (chmod): rwx grid <-> three octal digits ---

const chmodValid = computed(() => /^[0-7]{3}$/.test(modeInput.value))
const chmodRows: { label: string; index: number }[] = [
  { label: 'Owner', index: 0 },
  { label: 'Group', index: 1 },
  { label: 'Public', index: 2 },
]
const chmodBits: { label: string; bit: number }[] = [
  { label: 'Read', bit: 4 },
  { label: 'Write', bit: 2 },
  { label: 'Execute', bit: 1 },
]

function chmodBitChecked(index: number, bit: number): boolean {
  if (!chmodValid.value) return false
  return (parseInt(modeInput.value[index] ?? '0', 8) & bit) !== 0
}

function setChmodBit(index: number, bit: number, on: boolean | 'indeterminate') {
  if (!chmodValid.value) return
  const digits = modeInput.value.split('').map((digit) => parseInt(digit, 8))
  const current = digits[index] ?? 0
  digits[index] = on === true ? current | bit : current & ~bit
  modeInput.value = digits.map((digit) => digit.toString(8)).join('')
}

const submitChmod = () =>
  runDialog(async () => {
    const target = dialogTarget.value
    if (!target || !chmodValid.value) return
    await changeMode(siteId.value, { path: entryPath(target), mode: modeInput.value })
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
  const paths = dialogTarget.value ? [entryPath(dialogTarget.value)] : selectedPaths.value
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
const dragActive = computed(() => dragDepth.value > 0 && canMutateHere.value && !editorOpen.value)

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
  if (!canMutateHere.value || editorOpen.value) return
  const files = Array.from(event.dataTransfer?.files ?? [])
  if (files.length) void queueUploads(files)
}

const toolbarButton =
  'inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-[13px] font-medium text-ink-secondary transition-colors hover:bg-white/[0.06] hover:text-ink disabled:cursor-not-allowed disabled:opacity-40'
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

      <!-- File manager surface: toolbar, path bar, then listing or editor -->
      <div
        v-if="selectedSite"
        class="relative overflow-hidden rounded-2xl border border-outline bg-surface/40"
        :class="dragActive ? 'ring-2 ring-accent-400/60' : ''"
        @dragenter="onDragEnter"
        @dragover="onDragOver"
        @dragleave="onDragLeave"
        @drop="onDrop"
      >
        <!-- Toolbar: contextual actions for the current selection -->
        <div class="flex min-h-12 flex-wrap items-center gap-1 border-b border-outline bg-white/[0.03] px-3 py-1.5">
          <template v-if="selectedEntries.length">
            <span class="mr-2 min-w-0 truncate font-mono text-[13px] font-medium text-ink" aria-live="polite">
              {{ soloEntry ? soloEntry.name : `${countLabel(selectedEntries.length)} selected` }}
            </span>
            <span class="ml-auto flex flex-wrap items-center gap-1">
              <a v-if="soloEntry?.kind === 'file'" :class="toolbarButton" :href="downloadUrl(siteId, entryPath(soloEntry))">
                <AppIcon name="download" :size="15" />
                Download
              </a>
              <button v-if="soloEntry" :class="toolbarButton" @click="copyPath(soloEntry)">
                <AppIcon name="link-2" :size="15" />
                Copy path
              </button>
              <button v-if="soloEntry?.kind === 'file'" :class="toolbarButton" @click="openFileInEditor(soloEntry)">
                <AppIcon name="pencil" :size="15" />
                Edit
              </button>
              <template v-if="canMutateHere">
                <button v-if="soloEntry" :class="toolbarButton" @click="openDialog('move', soloEntry)">
                  <AppIcon name="folder-open" :size="15" />
                  Move
                </button>
                <button v-if="soloEntry" :class="toolbarButton" @click="openDialog('copy', soloEntry)">
                  <AppIcon name="copy" :size="15" />
                  Copy
                </button>
                <button v-if="!soloEntry" :class="toolbarButton" @click="openDialog('bulk-move')">
                  <AppIcon name="folder-open" :size="15" />
                  Move
                </button>
              </template>
              <button v-if="canWriteFiles" :class="toolbarButton" @click="openDialog('archive', soloEntry)">
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
                  <DropdownMenuItem v-if="soloEntry.kind === 'dir'" @select="openEntry(soloEntry!)">Open</DropdownMenuItem>
                  <DropdownMenuItem v-if="soloEntry.kind === 'dir'" @select="computeSize(soloEntry!)">Compute size</DropdownMenuItem>
                  <template v-if="canMutateHere">
                    <DropdownMenuItem @select="openDialog('chmod', soloEntry)">Change permissions…</DropdownMenuItem>
                    <DropdownMenuItem @select="openDialog('rename', soloEntry)">Rename…</DropdownMenuItem>
                    <DropdownMenuItem v-if="soloEntry.kind === 'file' && isArchiveName(soloEntry.name)" @select="openDialog('extract', soloEntry)">
                      Extract…
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      class="text-rose-300 data-[highlighted]:bg-rose-500/10 data-[highlighted]:text-rose-300"
                      @select="openDialog('delete', soloEntry)"
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
                @click="openDialog('bulk-delete')"
              >
                <AppIcon name="trash" :size="15" />
                Delete
              </button>
              <button :class="toolbarButton" aria-label="Clear selection" @click="selectedNames = []">
                <AppIcon name="x" :size="15" />
              </button>
            </span>
          </template>
          <template v-else>
            <template v-if="canMutateHere">
              <button :class="toolbarButton" @click="openDialog('mkdir')">
                <AppIcon name="folder" :size="15" />
                New folder
              </button>
              <button :class="toolbarButton" @click="openDialog('newfile')">
                <AppIcon name="file-text" :size="15" />
                New file
              </button>
              <button :class="toolbarButton" @click="filePicker?.click()">
                <AppIcon name="upload" :size="15" />
                Upload
              </button>
            </template>
            <StatusPill v-else tone="neutral" label="Read-only path" :description="readOnlyHint" :pulse="false" />
            <span class="ml-auto flex items-center gap-2">
              <span v-if="nameFilter" class="text-xs text-ink-muted" aria-live="polite">
                {{ visibleItems.length }} of {{ items.length }} shown
              </span>
              <div class="w-48 sm:w-56">
                <AppInput v-model="nameFilter" type="search" placeholder="Filter by name" aria-label="Filter entries by name" />
              </div>
            </span>
          </template>
        </div>

        <!-- Path bar -->
        <div class="flex items-center gap-1 border-b border-outline px-2 py-1.5">
          <button
            class="inline-flex h-7 w-7 items-center justify-center rounded-lg text-ink-secondary transition-colors hover:bg-white/[0.06] hover:text-ink disabled:cursor-not-allowed disabled:opacity-30"
            :disabled="path === '.'"
            aria-label="Up one directory"
            title="Up one directory"
            @click="setPath(parentPath)"
          >
            <AppIcon name="corner-left-up" :size="15" />
          </button>
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

        <!-- Editor mode: slim file list beside the tabbed editor -->
        <div v-if="editorOpen" class="flex h-[calc(100dvh-16rem)] min-h-105">
          <aside class="w-56 shrink-0 overflow-y-auto border-r border-outline bg-surface/30">
            <button
              v-if="path !== '.'"
              class="flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] text-ink-secondary transition-colors hover:bg-white/[0.04] hover:text-ink"
              @click="setPath(parentPath)"
            >
              <AppIcon name="corner-left-up" :size="14" class="shrink-0 text-ink-muted" />
              ..
            </button>
            <button
              v-for="entry in visibleItems"
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
              v-if="!visibleItems.length"
              icon="search"
              title="No matching entries"
              :description="`No names in this directory match “${nameFilter.trim()}”.`"
              class="m-3"
            />
            <ContextMenu v-else>
              <ContextMenuTrigger as-child>
                <div class="overflow-x-auto" @contextmenu="onRowContextMenu">
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
                        <th
                          v-for="column in sortableColumns"
                          :key="column.key"
                          class="px-3 py-2.5"
                          :aria-sort="ariaSort(column.key)"
                        >
                          <button
                            type="button"
                            class="flex items-center gap-1 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase transition-colors hover:text-ink"
                            @click="toggleSort(column.key)"
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
                            @click="toggleSort('modified')"
                          >
                            Changed
                            <AppIcon v-if="sortKey === 'modified'" :name="sortAscending ? 'chevron-up' : 'chevron-down'" :size="12" />
                          </button>
                        </th>
                      </tr>
                    </thead>
                    <tbody class="divide-y divide-outline">
                      <tr
                        v-for="entry in visibleItems"
                        :key="entry.name"
                        :data-name="entry.name"
                        class="cursor-default transition-colors select-none"
                        :class="selectedNames.includes(entry.name) ? 'bg-accent-500/[0.08]' : 'hover:bg-white/[0.03]'"
                        @click="onRowClick(entry, $event)"
                        @dblclick="onRowDoubleClick(entry)"
                      >
                        <td class="px-3 py-2.5" @click.stop>
                          <Checkbox
                            :model-value="selectedNames.includes(entry.name)"
                            :aria-label="`Select ${entry.name}`"
                            @update:model-value="() => toggleSelect(entry.name)"
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
              <ContextMenuContent v-if="activeMenuEntry">
                <ContextMenuLabel>{{ activeMenuEntry.name }}</ContextMenuLabel>
                <ContextMenuSeparator />
                <ContextMenuItem v-if="activeMenuEntry.kind === 'dir'" @select="openEntry(activeMenuEntry)">Open</ContextMenuItem>
                <ContextMenuItem v-if="activeMenuEntry.kind === 'file'" as-child>
                  <a :href="downloadUrl(siteId, entryPath(activeMenuEntry))">Download</a>
                </ContextMenuItem>
                <ContextMenuItem @select="copyPath(activeMenuEntry)">Copy path</ContextMenuItem>
                <ContextMenuItem v-if="activeMenuEntry.kind === 'file'" @select="openEntry(activeMenuEntry)">Edit</ContextMenuItem>
                <ContextMenuItem v-if="activeMenuEntry.kind === 'dir'" @select="computeSize(activeMenuEntry)">
                  Compute size
                </ContextMenuItem>
                <template v-if="canMutateHere">
                  <ContextMenuSeparator />
                  <ContextMenuItem @select="openDialog('move', activeMenuEntry)">Move…</ContextMenuItem>
                  <ContextMenuItem @select="openDialog('copy', activeMenuEntry)">Copy…</ContextMenuItem>
                  <ContextMenuSeparator />
                  <ContextMenuItem @select="openDialog('chmod', activeMenuEntry)">Change permissions…</ContextMenuItem>
                  <ContextMenuItem @select="openDialog('rename', activeMenuEntry)">Rename…</ContextMenuItem>
                  <ContextMenuItem @select="openDialog('archive', activeMenuEntry)">Pack into archive…</ContextMenuItem>
                  <ContextMenuItem
                    v-if="activeMenuEntry.kind === 'file' && isArchiveName(activeMenuEntry.name)"
                    @select="openDialog('extract', activeMenuEntry)"
                  >
                    Extract…
                  </ContextMenuItem>
                  <ContextMenuSeparator />
                  <ContextMenuItem
                    class="text-rose-300 data-[highlighted]:bg-rose-500/10 data-[highlighted]:text-rose-300"
                    @select="openDialog('delete', activeMenuEntry)"
                  >
                    Delete
                  </ContextMenuItem>
                </template>
              </ContextMenuContent>
            </ContextMenu>
          </template>
        </div>

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

      <form v-else-if="dialog === 'chmod' && dialogTarget" class="space-y-4" @submit.prevent="submitChmod">
        <div class="overflow-hidden rounded-xl border border-outline">
          <table class="w-full text-left">
            <thead>
              <tr class="border-b border-outline bg-white/[0.02]">
                <th class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase"></th>
                <th
                  v-for="bit in chmodBits"
                  :key="bit.bit"
                  class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase"
                >
                  {{ bit.label }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-outline">
              <tr v-for="row in chmodRows" :key="row.index">
                <td class="px-3 py-2.5 text-[13px] font-medium text-ink">{{ row.label }}</td>
                <td v-for="bit in chmodBits" :key="bit.bit" class="px-3 py-2.5">
                  <Checkbox
                    :model-value="chmodBitChecked(row.index, bit.bit)"
                    :aria-label="`${row.label} ${bit.label}`"
                    :disabled="!chmodValid"
                    @update:model-value="(value) => setChmodBit(row.index, bit.bit, value)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <FormField label="Octal value" hint="Three octal digits, such as 755. Setuid, setgid, and sticky bits are not allowed.">
          <AppInput v-model="modeInput" class="w-24 font-mono" autocomplete="off" pattern="[0-7]{3}" required />
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="dialogBusy" @click="closeDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="dialogBusy" :disabled="!chmodValid">Apply permissions</AppButton>
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
  </section>
</template>
