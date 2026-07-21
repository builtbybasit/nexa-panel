<script setup lang="ts">
import {
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
  VisuallyHidden,
} from 'reka-ui'
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { AppAlert, AppButton, AppConfirmDialog, AppIcon, AppSelect } from '@/shared/ui'

import { downloadUrl, FilesRequestError, readFileContent, writeFileContent } from '../api'

/** Heavy dependency — only pulled in (workers included) once a file is opened. */
const MonacoEditor = defineAsyncComponent(() => import('@/shared/ui/MonacoEditor.vue'))
/** Loaded only when a save conflict surfaces, to diff the server copy against local edits. */
const DiffEditor = defineAsyncComponent(() => import('@/shared/ui/DiffEditor.vue'))

const props = withDefaults(defineProps<{ siteId: string; path: string; readOnly?: boolean }>(), { readOnly: false })
const emit = defineEmits<{ close: []; saved: [] }>()

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

const filename = computed(() => props.path.split('/').pop() || props.path)

const detectedLanguage = computed(() => {
  const name = filename.value
  if (name === 'Dockerfile') return 'dockerfile'
  if (name.startsWith('.env')) return 'ini'
  const ext = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1).toLowerCase() : ''
  return languageByExtension[ext] ?? 'plaintext'
})

/** A manual pick from the language dropdown; empty means "follow the extension". */
const languageOverride = ref('')
const language = computed(() => languageOverride.value || detectedLanguage.value)

/** Curated Monaco languages for the picker; the active one is always included. */
const baseLanguages: [string, string][] = [
  ['plaintext', 'Plain Text'], ['javascript', 'JavaScript'], ['typescript', 'TypeScript'],
  ['json', 'JSON'], ['html', 'HTML'], ['css', 'CSS'], ['scss', 'SCSS'], ['less', 'LESS'],
  ['markdown', 'Markdown'], ['php', 'PHP'], ['python', 'Python'], ['ruby', 'Ruby'],
  ['go', 'Go'], ['rust', 'Rust'], ['java', 'Java'], ['c', 'C'], ['cpp', 'C++'], ['csharp', 'C#'],
  ['shell', 'Shell'], ['yaml', 'YAML'], ['ini', 'INI'], ['xml', 'XML'], ['sql', 'SQL'],
  ['dockerfile', 'Dockerfile'], ['lua', 'Lua'],
]
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
    languageOverride.value = value
  },
})

const loading = ref(true)
const loadError = ref('')
/** Set when the file cannot be edited inline; the editor offers a download instead. */
const uneditable = ref<'binary' | 'truncated'>()
const size = ref(0)
const content = ref('')
/** Last content loaded from or written to the server; drives the dirty flag. */
const savedContent = ref('')
const etag = ref('')
const saving = ref(false)
const conflict = ref(false)
/** The current server copy, shown as the read-only left pane of the conflict diff. */
const serverContent = ref('')
const conflictLoading = ref(false)
const saveError = ref('')
const justSaved = ref(false)
const discardAsk = ref(false)

const href = downloadUrl(props.siteId, props.path)

/** True once a live editor is on screen (not loading/errored/uneditable). */
const editorShown = computed(() => !loading.value && !loadError.value && !uneditable.value)
const dirty = computed(() => editorShown.value && !props.readOnly && content.value !== savedContent.value)

async function load() {
  loading.value = true
  loadError.value = ''
  uneditable.value = undefined
  languageOverride.value = ''
  try {
    const file = await readFileContent(props.siteId, props.path)
    size.value = file.size
    if (file.binary) uneditable.value = 'binary'
    else if (file.truncated) uneditable.value = 'truncated'
    else {
      content.value = file.content
      savedContent.value = file.content
      etag.value = file.etag
    }
  } catch (caught) {
    loadError.value = caught instanceof Error ? caught.message : 'The file could not be loaded.'
  } finally {
    loading.value = false
  }
}

async function write(expectedEtag: string, closeAfter: boolean) {
  if (props.readOnly) return
  saving.value = true
  saveError.value = ''
  try {
    const result = await writeFileContent(props.siteId, { path: props.path, content: content.value, expectedEtag })
    etag.value = result.etag
    savedContent.value = content.value
    conflict.value = false
    justSaved.value = true
    emit('saved')
    if (closeAfter) emit('close')
  } catch (caught) {
    if (caught instanceof FilesRequestError && caught.status === 409) {
      conflict.value = true
      void loadServerCopy()
    } else {
      saveError.value = caught instanceof Error ? caught.message : 'The file could not be saved.'
    }
  } finally {
    saving.value = false
  }
}

/** Fetch the current server copy for the conflict diff's read-only left pane. */
async function loadServerCopy() {
  conflictLoading.value = true
  try {
    const file = await readFileContent(props.siteId, props.path)
    // A binary/truncated server copy has no inline text to diff against.
    serverContent.value = file.binary || file.truncated ? '' : file.content
  } catch {
    // A 404 (deleted on the server) or any read failure leaves an empty left pane.
    serverContent.value = ''
  } finally {
    conflictLoading.value = false
  }
}

const save = () => write(etag.value, false)

/** Discard the local edits and load the current server copy. */
async function reloadServerCopy() {
  conflict.value = false
  await load()
}

/** Keep the local edits: fetch the fresh etag, then save over the server copy. */
async function overwriteServerCopy() {
  if (props.readOnly) return
  saving.value = true
  saveError.value = ''
  try {
    let expected = ''
    try {
      expected = (await readFileContent(props.siteId, props.path)).etag
    } catch (caught) {
      // A 404 means the server copy was deleted; save as a new file.
      if (!(caught instanceof FilesRequestError && caught.status === 404)) throw caught
    }
    await write(expected, false)
  } catch (caught) {
    saveError.value = caught instanceof Error ? caught.message : 'The file could not be saved.'
    saving.value = false
  }
}

/** Escape, the tab close button, and the discard guard all route through here. */
function requestClose() {
  if (saving.value) return
  if (dirty.value) {
    discardAsk.value = true
    return
  }
  emit('close')
}

function discard() {
  discardAsk.value = false
  emit('close')
}

/** Save via a keyboard shortcut, honouring every state that blocks a save. */
function triggerSave() {
  if (props.readOnly) return
  if (!editorShown.value || saving.value || conflict.value || discardAsk.value) return
  if (!dirty.value) return
  void save()
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

// --- Fullscreen (native): expands the editor surface to the whole display ---
const surface = ref<HTMLElement>()
const isFullscreen = ref(false)
async function toggleFullscreen() {
  try {
    if (document.fullscreenElement) await document.exitFullscreen()
    else await surface.value?.requestFullscreen()
  } catch {
    // Fullscreen may be unsupported or blocked by permissions policy; ignore.
  }
}
function onFullscreenChange() {
  isFullscreen.value = document.fullscreenElement === surface.value
}

const roundButton =
  'inline-flex h-9 w-9 items-center justify-center rounded-full border border-outline bg-white/[0.02] text-ink-secondary transition-colors hover:bg-white/[0.07] hover:text-ink disabled:cursor-not-allowed disabled:opacity-30'
const squareButtonBase = 'inline-flex h-9 w-9 items-center justify-center rounded-lg border transition-colors hover:bg-white/[0.07]'
const squareButtonOff = 'border-outline bg-white/[0.02] text-ink-secondary hover:text-ink'
const squareButtonOn = 'border-accent-500/30 bg-accent-500/10 text-accent-300'

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  document.addEventListener('fullscreenchange', onFullscreenChange)
  void load()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  if (document.fullscreenElement) void document.exitFullscreen().catch(() => undefined)
})
</script>

<template>
  <DialogRoot
    :open="true"
    @update:open="
      (value) => {
        if (!value) requestClose()
      }
    "
  >
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-40 bg-canvas/80 backdrop-blur-sm" />
      <DialogContent class="fixed inset-0 z-50 outline-none">
        <VisuallyHidden as-child><DialogTitle>Editing {{ path }}</DialogTitle></VisuallyHidden>
        <VisuallyHidden as-child><DialogDescription>Inline file editor</DialogDescription></VisuallyHidden>

        <div ref="surface" class="flex h-screen w-screen flex-col bg-canvas text-ink">
          <!-- Tab strip -->
          <div class="flex items-center border-b border-outline bg-surface/60 pl-2">
            <div class="flex items-center gap-2 border-b-2 border-accent-400 px-3 py-2.5">
              <AppIcon name="file-code-2" :size="14" class="text-accent-300" />
              <span class="font-mono text-[13px] text-ink">{{ filename }}</span>
              <button
                class="ml-1 rounded p-0.5 text-ink-muted transition-colors hover:bg-white/[0.08] hover:text-ink"
                aria-label="Close editor"
                @click="requestClose"
              >
                <AppIcon name="x" :size="14" />
              </button>
            </div>
          </div>

          <!-- Toolbar -->
          <div class="flex items-center gap-3 border-b border-outline bg-surface/40 px-3 py-2">
            <button
              v-if="!readOnly"
              class="inline-flex h-9 items-center gap-2 rounded-lg border border-accent-500/30 bg-accent-500/10 px-3 text-[13px] font-semibold text-accent-100 transition-colors hover:bg-accent-500/20 disabled:cursor-not-allowed disabled:opacity-40"
              :disabled="!dirty || saving || conflict"
              @click="save"
            >
              <AppIcon name="square-check" :size="15" />
              Save
            </button>

            <div class="flex items-center gap-1.5">
              <button :class="roundButton" :disabled="!editorShown" title="Find (Ctrl/Cmd+F)" @click="runFind">
                <AppIcon name="search" :size="15" />
              </button>
              <button :class="roundButton" :disabled="!editorShown || readOnly" title="Undo (Ctrl/Cmd+Z)" @click="runUndo">
                <AppIcon name="rotate-ccw" :size="15" />
              </button>
              <button :class="roundButton" :disabled="!editorShown || readOnly" title="Redo (Ctrl/Cmd+Y)" @click="runRedo">
                <AppIcon name="rotate-cw" :size="15" />
              </button>
            </div>

            <div class="ml-auto flex items-center gap-2">
              <div class="w-28">
                <AppSelect :model-value="'utf-8'" disabled title="Files are read and written as UTF-8">
                  <option value="utf-8">utf-8</option>
                </AppSelect>
              </div>
              <div class="w-40">
                <AppSelect v-model="languageSelect" title="Syntax highlighting language">
                  <option v-for="[id, label] in languageOptions" :key="id" :value="id">{{ label }}</option>
                </AppSelect>
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
                :title="isFullscreen ? 'Exit fullscreen' : 'Fullscreen'"
                @click="toggleFullscreen"
              >
                <AppIcon :name="isFullscreen ? 'minimize' : 'maximize'" :size="16" />
              </button>
            </div>
          </div>

          <!-- Conflict + error bars -->
          <AppAlert v-if="conflict" tone="warning" title="The file changed on the server" class="mx-3 mt-3">
            <p class="mb-2">
              Someone else saved this file after you opened it. The current server copy is on the left; your edits are on
              the right — merge them there if needed. Then reload the server copy, or overwrite it with your edits.
            </p>
            <span class="flex flex-wrap items-center gap-2">
              <AppButton size="sm" :disabled="saving" @click="reloadServerCopy">Reload server copy</AppButton>
              <AppButton size="sm" variant="danger" :loading="saving" :disabled="conflictLoading" @click="overwriteServerCopy">
                Overwrite server copy
              </AppButton>
              <span v-if="conflictLoading" class="text-[11px] text-ink-muted">Loading server copy…</span>
            </span>
          </AppAlert>
          <div v-if="saveError" class="px-3 pt-3">
            <AppAlert tone="danger">{{ saveError }}</AppAlert>
          </div>

          <!-- Body -->
          <div class="relative min-h-0 flex-1">
            <div v-if="loading" class="flex h-full items-center justify-center text-sm text-ink-muted">
              Loading file content…
            </div>
            <div v-else-if="loadError" class="flex h-full items-center justify-center p-6">
              <AppAlert tone="danger" class="max-w-md">{{ loadError }}</AppAlert>
            </div>
            <div v-else-if="uneditable" class="flex h-full flex-col items-center justify-center gap-4 p-6 text-center">
              <AppAlert tone="warning" class="max-w-md">
                {{
                  uneditable === 'binary'
                    ? 'This file contains binary data and cannot be edited inline.'
                    : `This file is larger than the inline editing limit (${formatBytes(size)}).`
                }}
                Download it instead.
              </AppAlert>
              <a
                :href="href"
                class="inline-flex h-10 items-center justify-center gap-2 rounded-lg bg-accent-500 px-4 text-sm font-semibold text-accent-950 transition-colors hover:bg-accent-400"
              >
                <AppIcon name="download" :size="16" />
                Download
              </a>
            </div>
            <template v-else>
              <DiffEditor
                v-if="conflict"
                ref="editorApi"
                v-model="content"
                :original="serverContent"
                :language="language"
                :minimap="minimapOn"
                :readonly="saving"
                @save="triggerSave"
              />
              <MonacoEditor
                v-else
                ref="editorApi"
                v-model="content"
                :language="language"
                :minimap="minimapOn"
                :readonly="readOnly || saving"
                @save="triggerSave"
              />
            </template>
          </div>

          <!-- Status bar -->
          <div class="flex items-center gap-4 border-t border-outline bg-surface/60 px-3 py-1.5 text-[11px] text-ink-muted">
            <span class="font-mono">{{ formatBytes(size) }}</span>
            <span>{{ languageLabel }}</span>
            <span aria-live="polite">
              <span v-if="readOnly" class="text-ink-secondary">Your account can view this file but cannot edit it.</span>
              <span v-else-if="dirty" class="text-amber-300">Unsaved changes</span>
              <span v-else-if="justSaved" class="inline-flex items-center gap-1 text-emerald-300">
                <AppIcon name="check" :size="12" />
                Saved
              </span>
            </span>
            <span v-if="!readOnly" class="ml-auto hidden sm:inline">Ctrl/Cmd+S saves</span>
          </div>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>

  <AppConfirmDialog
    :open="discardAsk"
    title="Discard unsaved changes?"
    confirm-label="Discard changes"
    @confirm="discard"
    @close="discardAsk = false"
  >
    <p>
      Your edits to <span class="font-mono text-ink">{{ path }}</span> are not saved. Closing discards them.
    </p>
  </AppConfirmDialog>
</template>
