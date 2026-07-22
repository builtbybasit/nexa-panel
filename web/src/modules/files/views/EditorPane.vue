<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, reactive, ref } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { AppAlert, AppButton, AppConfirmDialog, AppIcon } from '@/shared/ui'
import { Combobox, ComboboxAnchor, ComboboxEmpty, ComboboxGroup, ComboboxInput, ComboboxItem, ComboboxItemIndicator, ComboboxList, ComboboxTrigger } from '@/shared/ui/combobox'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'

import { downloadUrl, FilesRequestError, readFileContent, writeFileContent } from '../api'

/** Heavy dependency — only pulled in (workers included) once a file is opened. */
const MonacoEditor = defineAsyncComponent(() => import('@/shared/ui/MonacoEditor.vue'))
/** Loaded only when a save conflict surfaces, to diff the server copy against local edits. */
const DiffEditor = defineAsyncComponent(() => import('@/shared/ui/DiffEditor.vue'))

const props = defineProps<{
  siteId: string
  /** Per-path write gate; a tab on a read-only path renders without save affordances. */
  isPathReadOnly: (path: string) => boolean
}>()
const emit = defineEmits<{ close: []; saved: []; tabs: [paths: string[], active: string] }>()

/** Maps a file extension to a Monaco language id for syntax highlighting. */
const languageByExtension: Record<string, string> = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript',
  ts: 'typescript', tsx: 'typescript',
  json: 'json', jsonc: 'json',
  html: 'html', htm: 'html', vue: 'html', xml: 'xml', svg: 'xml',
  css: 'css', scss: 'scss', sass: 'scss', less: 'less',
  md: 'markdown', markdown: 'markdown',
  php: 'php', py: 'python', rb: 'ruby', go: 'go', rs: 'rust',
  java: 'java', kt: 'kotlin', c: 'c', h: 'c', cpp: 'cpp', cc: 'cpp', hpp: 'cpp', cs: 'csharp',
  sh: 'shell', bash: 'shell', zsh: 'shell',
  yml: 'yaml', yaml: 'yaml', toml: 'ini', ini: 'ini', conf: 'ini', cfg: 'ini', env: 'ini',
  sql: 'sql', dockerfile: 'dockerfile', nginx: 'nginx', htaccess: 'apache', lua: 'lua',
}

function detectLanguage(name: string): string {
  if (name === 'Dockerfile') return 'dockerfile'
  if (name.startsWith('.env')) return 'ini'
  const ext = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1).toLowerCase() : ''
  return languageByExtension[ext] ?? 'plaintext'
}

/** Curated Monaco languages for the picker; the active one is always included. */
const baseLanguages: [string, string][] = [
  ['plaintext', 'Plain Text'], ['javascript', 'JavaScript'], ['typescript', 'TypeScript'],
  ['json', 'JSON'], ['html', 'HTML'], ['css', 'CSS'], ['scss', 'SCSS'], ['less', 'LESS'],
  ['markdown', 'Markdown'], ['php', 'PHP'], ['python', 'Python'], ['ruby', 'Ruby'],
  ['go', 'Go'], ['rust', 'Rust'], ['java', 'Java'], ['c', 'C'], ['cpp', 'C++'], ['csharp', 'C#'],
  ['shell', 'Shell'], ['yaml', 'YAML'], ['ini', 'INI'], ['xml', 'XML'], ['sql', 'SQL'],
  ['dockerfile', 'Dockerfile'], ['lua', 'Lua'],
]

interface EditorTab {
  path: string
  name: string
  loading: boolean
  loadError: string
  /** Set when the file cannot be edited inline; the tab offers a download instead. */
  uneditable?: 'binary' | 'truncated'
  size: number
  content: string
  /** Last content loaded from or written to the server; drives the dirty flag. */
  savedContent: string
  etag: string
  saving: boolean
  conflict: boolean
  /** The current server copy, shown as the read-only left pane of the conflict diff. */
  serverContent: string
  conflictLoading: boolean
  saveError: string
  justSaved: boolean
  /** A manual pick from the language dropdown; empty means "follow the extension". */
  languageOverride: string
}

const tabs = ref<EditorTab[]>([])
const activePath = ref('')
const active = computed(() => tabs.value.find((tab) => tab.path === activePath.value))

const activeReadOnly = computed(() => !active.value || props.isPathReadOnly(active.value.path))
const editorShown = computed(() => {
  const tab = active.value
  return Boolean(tab && !tab.loading && !tab.loadError && !tab.uneditable)
})
const isDirty = (tab: EditorTab) =>
  !tab.loading && !tab.loadError && !tab.uneditable && !props.isPathReadOnly(tab.path) && tab.content !== tab.savedContent
const activeDirty = computed(() => Boolean(active.value && isDirty(active.value)))

const language = computed(() => {
  const tab = active.value
  if (!tab) return 'plaintext'
  return tab.languageOverride || detectLanguage(tab.name)
})
const languageOptions = computed<[string, string][]>(() => {
  if (baseLanguages.some(([id]) => id === language.value)) return baseLanguages
  return [[language.value, language.value], ...baseLanguages]
})
const languageLabel = computed(
  () => languageOptions.value.find(([id]) => id === language.value)?.[1] ?? language.value,
)
const languageSelect = computed({
  get: () => language.value,
  set: (value) => {
    if (active.value) active.value.languageOverride = value
  },
})

/** Proxy the active tab's buffer into the single Monaco instance. */
const activeContent = computed({
  get: () => active.value?.content ?? '',
  set: (value) => {
    if (active.value) active.value.content = value
  },
})

const notifyTabs = () => emit('tabs', tabs.value.map((tab) => tab.path), activePath.value)

function open(path: string) {
  const existing = tabs.value.find((tab) => tab.path === path)
  if (existing) {
    activePath.value = path
    notifyTabs()
    return
  }
  const tab = reactive<EditorTab>({
    path,
    name: path.split('/').pop() || path,
    loading: true,
    loadError: '',
    size: 0,
    content: '',
    savedContent: '',
    etag: '',
    saving: false,
    conflict: false,
    serverContent: '',
    conflictLoading: false,
    saveError: '',
    justSaved: false,
    languageOverride: '',
  })
  tabs.value.push(tab)
  activePath.value = path
  notifyTabs()
  void load(tab)
}

async function load(tab: EditorTab) {
  tab.loading = true
  tab.loadError = ''
  tab.uneditable = undefined
  try {
    const file = await readFileContent(props.siteId, tab.path)
    tab.size = file.size
    if (file.binary) tab.uneditable = 'binary'
    else if (file.truncated) tab.uneditable = 'truncated'
    else {
      tab.content = file.content
      tab.savedContent = file.content
      tab.etag = file.etag
    }
  } catch (caught) {
    tab.loadError = caught instanceof Error ? caught.message : 'The file could not be loaded.'
  } finally {
    tab.loading = false
  }
}

async function write(tab: EditorTab, expectedEtag: string) {
  if (props.isPathReadOnly(tab.path)) return
  tab.saving = true
  tab.saveError = ''
  try {
    const result = await writeFileContent(props.siteId, { path: tab.path, content: tab.content, expectedEtag })
    tab.etag = result.etag
    tab.savedContent = tab.content
    tab.conflict = false
    tab.justSaved = true
    emit('saved')
  } catch (caught) {
    if (caught instanceof FilesRequestError && caught.status === 409) {
      tab.conflict = true
      void loadServerCopy(tab)
    } else {
      tab.saveError = caught instanceof Error ? caught.message : 'The file could not be saved.'
    }
  } finally {
    tab.saving = false
  }
}

/** Fetch the current server copy for the conflict diff's read-only left pane. */
async function loadServerCopy(tab: EditorTab) {
  tab.conflictLoading = true
  try {
    const file = await readFileContent(props.siteId, tab.path)
    // A binary/truncated server copy has no inline text to diff against.
    tab.serverContent = file.binary || file.truncated ? '' : file.content
  } catch {
    // A 404 (deleted on the server) or any read failure leaves an empty left pane.
    tab.serverContent = ''
  } finally {
    tab.conflictLoading = false
  }
}

function save() {
  const tab = active.value
  if (tab) void write(tab, tab.etag)
}

/** Discard the local edits and load the current server copy. */
async function reloadServerCopy(tab: EditorTab) {
  tab.conflict = false
  await load(tab)
}

/** Keep the local edits: fetch the fresh etag, then save over the server copy. */
async function overwriteServerCopy(tab: EditorTab) {
  if (props.isPathReadOnly(tab.path)) return
  tab.saving = true
  tab.saveError = ''
  try {
    let expected = ''
    try {
      expected = (await readFileContent(props.siteId, tab.path)).etag
    } catch (caught) {
      // A 404 means the server copy was deleted; save as a new file.
      if (!(caught instanceof FilesRequestError && caught.status === 404)) throw caught
    }
    await write(tab, expected)
  } catch (caught) {
    tab.saveError = caught instanceof Error ? caught.message : 'The file could not be saved.'
    tab.saving = false
  }
}

// --- Tab closing, with a discard guard for dirty buffers ---

const discardTab = ref<EditorTab>()

function requestCloseTab(tab: EditorTab) {
  if (tab.saving) return
  if (isDirty(tab)) {
    discardTab.value = tab
    return
  }
  closeTab(tab)
}

function closeTab(tab: EditorTab) {
  const index = tabs.value.indexOf(tab)
  tabs.value = tabs.value.filter((candidate) => candidate !== tab)
  if (activePath.value === tab.path) {
    const next = tabs.value[Math.min(index, tabs.value.length - 1)]
    activePath.value = next?.path ?? ''
  }
  notifyTabs()
  if (!tabs.value.length) emit('close')
}

function discardConfirmed() {
  const tab = discardTab.value
  discardTab.value = undefined
  if (tab) closeTab(tab)
}

function activate(tab: EditorTab) {
  activePath.value = tab.path
  notifyTabs()
}

/** Save via a keyboard shortcut, honouring every state that blocks a save. */
function triggerSave() {
  const tab = active.value
  if (!tab || props.isPathReadOnly(tab.path)) return
  if (!editorShown.value || tab.saving || tab.conflict || discardTab.value) return
  if (!isDirty(tab)) return
  void write(tab, tab.etag)
}

function onKeydown(event: KeyboardEvent) {
  if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== 's') return
  event.preventDefault()
  triggerSave()
}

// --- Toolbar: drive Monaco's own actions through the exposed editor handle ---
const editorApi = ref<{ undo(): void; redo(): void; openFind(): void; focus(): void } | null>(null)
const runFind = () => editorApi.value?.openFind()
const runUndo = () => editorApi.value?.undo()
const runRedo = () => editorApi.value?.redo()

const minimapOn = ref(true)

/** Expanded mode lifts the pane out of the page flow to fill the viewport. */
const expanded = ref(false)

const roundButton =
  'inline-flex h-9 w-9 items-center justify-center rounded-full border border-outline bg-white/[0.02] text-ink-secondary transition-colors hover:bg-white/[0.07] hover:text-ink disabled:cursor-not-allowed disabled:opacity-30'
const squareButtonBase = 'inline-flex h-9 w-9 items-center justify-center rounded-lg border transition-colors hover:bg-white/[0.07]'
const squareButtonOff = 'border-outline bg-white/[0.02] text-ink-secondary hover:text-ink'
const squareButtonOn = 'border-accent-500/30 bg-accent-500/10 text-accent-300'

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

defineExpose({ open, hasDirtyTabs: () => tabs.value.some((tab) => isDirty(tab)) })
</script>

<template>
  <div
    class="flex min-w-0 flex-col bg-canvas text-ink"
    :class="expanded ? 'fixed inset-0 z-50' : 'h-full flex-1'"
  >
    <!-- Tab strip -->
    <div class="flex items-center overflow-x-auto border-b border-outline bg-surface/60 pl-1">
      <button
        v-for="tab in tabs"
        :key="tab.path"
        class="group flex shrink-0 items-center gap-2 border-b-2 px-3 py-2.5 transition-colors"
        :class="tab.path === activePath ? 'border-accent-400' : 'border-transparent hover:bg-white/[0.03]'"
        :title="tab.path"
        @click="activate(tab)"
      >
        <AppIcon name="file-code-2" :size="14" :class="tab.path === activePath ? 'text-accent-300' : 'text-ink-muted'" />
        <span class="font-mono text-[13px]" :class="tab.path === activePath ? 'text-ink' : 'text-ink-secondary'">
          {{ tab.name }}</span
        >
        <span v-if="isDirty(tab)" class="h-1.5 w-1.5 rounded-full bg-amber-300" aria-label="Unsaved changes" />
        <span
          class="ml-0.5 rounded p-0.5 text-ink-muted transition-colors hover:bg-white/[0.08] hover:text-ink"
          role="button"
          :aria-label="`Close ${tab.name}`"
          @click.stop="requestCloseTab(tab)"
        >
          <AppIcon name="x" :size="14" />
        </span>
      </button>
    </div>

    <!-- Toolbar -->
    <div class="flex items-center gap-3 border-b border-outline bg-surface/40 px-3 py-2">
      <button
        v-if="!activeReadOnly"
        class="inline-flex h-9 items-center gap-2 rounded-lg border border-accent-500/30 bg-accent-500/10 px-3 text-[13px] font-semibold text-accent-100 transition-colors hover:bg-accent-500/20 disabled:cursor-not-allowed disabled:opacity-40"
        :disabled="!activeDirty || active?.saving || active?.conflict"
        @click="save"
      >
        <AppIcon name="square-check" :size="15" />
        Save
      </button>

      <div class="flex items-center gap-1.5">
        <button :class="roundButton" :disabled="!editorShown" title="Find (Ctrl/Cmd+F)" @click="runFind">
          <AppIcon name="search" :size="15" />
        </button>
        <button :class="roundButton" :disabled="!editorShown || activeReadOnly" title="Undo (Ctrl/Cmd+Z)" @click="runUndo">
          <AppIcon name="rotate-ccw" :size="15" />
        </button>
        <button :class="roundButton" :disabled="!editorShown || activeReadOnly" title="Redo (Ctrl/Cmd+Y)" @click="runRedo">
          <AppIcon name="rotate-cw" :size="15" />
        </button>
      </div>

      <div class="ml-auto flex items-center gap-2">
        <div class="hidden w-28 md:block">
          <Select model-value="utf-8" disabled>
            <SelectTrigger title="Files are read and written as UTF-8" />
            <SelectContent>
              <SelectItem value="utf-8">utf-8</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="hidden w-44 md:block">
          <Combobox v-model="languageSelect">
            <ComboboxAnchor as-child>
              <ComboboxTrigger
                title="Syntax highlighting language"
                :label="((id) => languageOptions.find(([optionId]) => optionId === id)?.[1] ?? '')(languageSelect)"
              />
            </ComboboxAnchor>
            <ComboboxList>
              <ComboboxInput placeholder="Search languages…" />
              <ComboboxEmpty>No matches.</ComboboxEmpty>
              <ComboboxGroup>
                <ComboboxItem v-for="[id, label] in languageOptions" :key="id" :value="id" :text-value="label">
                  {{ label }}<ComboboxItemIndicator />
                </ComboboxItem>
              </ComboboxGroup>
            </ComboboxList>
          </Combobox>
        </div>
        <button
          :class="[squareButtonBase, minimapOn ? squareButtonOn : squareButtonOff]"
          :title="minimapOn ? 'Hide minimap' : 'Show minimap'"
          @click="minimapOn = !minimapOn"
        >
          <AppIcon name="panel-right" :size="16" />
        </button>
        <button
          :class="[squareButtonBase, squareButtonOff]"
          :title="expanded ? 'Exit fullscreen' : 'Fullscreen'"
          @click="expanded = !expanded"
        >
          <AppIcon :name="expanded ? 'minimize' : 'maximize'" :size="16" />
        </button>
      </div>
    </div>

    <template v-if="active">
      <!-- Conflict + error bars -->
      <AppAlert v-if="active.conflict" tone="warning" title="The file changed on the server" class="mx-3 mt-3">
        <p class="mb-2">
          Someone else saved this file after you opened it. The current server copy is on the left; your edits are on
          the right — merge them there if needed. Then reload the server copy, or overwrite it with your edits.
        </p>
        <span class="flex flex-wrap items-center gap-2">
          <AppButton size="sm" :disabled="active.saving" @click="reloadServerCopy(active)">Reload server copy</AppButton>
          <AppButton
            size="sm"
            variant="danger"
            :loading="active.saving"
            :disabled="active.conflictLoading"
            @click="overwriteServerCopy(active)"
          >
            Overwrite server copy
          </AppButton>
          <span v-if="active.conflictLoading" class="text-[11px] text-ink-muted">Loading server copy…</span>
        </span>
      </AppAlert>
      <div v-if="active.saveError" class="px-3 pt-3">
        <AppAlert tone="danger">{{ active.saveError }}</AppAlert>
      </div>

      <!-- Body -->
      <div class="relative min-h-0 flex-1">
        <div v-if="active.loading" class="flex h-full items-center justify-center text-sm text-ink-muted">
          Loading file content…
        </div>
        <div v-else-if="active.loadError" class="flex h-full items-center justify-center p-6">
          <AppAlert tone="danger" class="max-w-md">{{ active.loadError }}</AppAlert>
        </div>
        <div v-else-if="active.uneditable" class="flex h-full flex-col items-center justify-center gap-4 p-6 text-center">
          <AppAlert tone="warning" class="max-w-md">
            {{
              active.uneditable === 'binary'
                ? 'This file contains binary data and cannot be edited inline.'
                : `This file is larger than the inline editing limit (${formatBytes(active.size)}).`
            }}
            Download it instead.
          </AppAlert>
          <a
            :href="downloadUrl(siteId, active.path)"
            class="inline-flex h-10 items-center justify-center gap-2 rounded-lg bg-accent-500 px-4 text-sm font-semibold text-accent-950 transition-colors hover:bg-accent-400"
          >
            <AppIcon name="download" :size="16" />
            Download
          </a>
        </div>
        <template v-else>
          <DiffEditor
            v-if="active.conflict"
            ref="editorApi"
            :key="`diff:${active.path}`"
            v-model="activeContent"
            :original="active.serverContent"
            :language="language"
            :minimap="minimapOn"
            :readonly="active.saving"
            @save="triggerSave"
          />
          <MonacoEditor
            v-else
            ref="editorApi"
            :key="active.path"
            v-model="activeContent"
            :language="language"
            :minimap="minimapOn"
            :readonly="activeReadOnly || active.saving"
            @save="triggerSave"
          />
        </template>
      </div>

      <!-- Status bar -->
      <div class="flex items-center gap-4 border-t border-outline bg-surface/60 px-3 py-1.5 text-[11px] text-ink-muted">
        <span class="hidden truncate font-mono sm:inline">/{{ active.path }}</span>
        <span class="font-mono">{{ formatBytes(active.size) }}</span>
        <span>{{ languageLabel }}</span>
        <span aria-live="polite">
          <span v-if="activeReadOnly" class="text-ink-secondary">Your account can view this file but cannot edit it.</span>
          <span v-else-if="activeDirty" class="text-amber-300">Unsaved changes</span>
          <span v-else-if="active.justSaved" class="inline-flex items-center gap-1 text-emerald-300">
            <AppIcon name="check" :size="12" />
            Saved
          </span>
        </span>
        <span v-if="!activeReadOnly" class="ml-auto hidden sm:inline">Ctrl/Cmd+S saves</span>
      </div>
    </template>

    <AppConfirmDialog
      :open="Boolean(discardTab)"
      title="Discard unsaved changes?"
      confirm-label="Discard changes"
      @confirm="discardConfirmed"
      @close="discardTab = undefined"
    >
      <p>
        Your edits to <span class="font-mono text-ink">{{ discardTab?.path }}</span> are not saved. Closing discards
        them.
      </p>
    </AppConfirmDialog>
  </div>
</template>
