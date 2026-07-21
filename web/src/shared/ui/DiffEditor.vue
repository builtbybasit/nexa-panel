<script setup lang="ts">
import * as monaco from 'monaco-editor'
import { nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'

import AppAlert from './AppAlert.vue'
import AppButton from './AppButton.vue'
import { NEXA_MONACO_THEME } from './monacoTheme'
import './monacoWorkers'

/**
 * Side-by-side diff wrapper over Monaco's diff editor. `original` is the
 * read-only left pane; `modelValue` is the editable right pane (v-model), so
 * the two panes can be hand-merged before saving. Kept behind a lazy import
 * like MonacoEditor, and guarded with the same error-boundary + Retry pattern
 * (adapted from bazingaedward/monaco-editor-vue3's useDiffEditor hook).
 */
const props = withDefaults(
  defineProps<{ modelValue: string; original: string; language?: string; readonly?: boolean; minimap?: boolean }>(),
  { language: 'plaintext', readonly: false, minimap: false },
)
const emit = defineEmits<{ 'update:modelValue': [string]; save: [] }>()

const container = ref<HTMLElement>()
const editor = shallowRef<monaco.editor.IStandaloneDiffEditor>()
/** Non-empty while the editor could not be created; drives the error overlay. */
const error = ref('')
/** Guards the change handler while we push external updates into the model. */
let applyingExternal = false

function createEditor() {
  if (!container.value) return
  try {
    const instance = monaco.editor.createDiffEditor(container.value, {
      readOnly: props.readonly,
      originalEditable: false,
      theme: NEXA_MONACO_THEME,
      automaticLayout: true,
      fontSize: 13,
      lineNumbersMinChars: 3,
      renderSideBySide: true,
      scrollBeyondLastLine: false,
      minimap: { enabled: props.minimap },
      smoothScrolling: true,
    })
    instance.setModel({
      original: monaco.editor.createModel(props.original, props.language),
      modified: monaco.editor.createModel(props.modelValue, props.language),
    })

    const modified = instance.getModifiedEditor()
    modified.onDidChangeModelContent(() => {
      if (applyingExternal) return
      emit('update:modelValue', modified.getValue())
    })
    // Monaco swallows keydown, so bind save inside the editor as well.
    modified.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => emit('save'))

    editor.value = instance
    error.value = ''
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'The diff editor could not be initialised.'
  }
}

/** Dispose both models and the editor. */
function dispose() {
  const model = editor.value?.getModel()
  model?.original.dispose()
  model?.modified.dispose()
  editor.value?.dispose()
}

/** Tear down any half-built editor, then rebuild from scratch. */
async function retry() {
  dispose()
  editor.value = undefined
  error.value = ''
  // Let the container re-appear (it is hidden under the overlay) before Monaco
  // measures it, otherwise the rebuilt editor lays out against a 0×0 box.
  await nextTick()
  createEditor()
}

onMounted(createEditor)

watch(
  () => props.modelValue,
  (value) => {
    const model = editor.value?.getModel()
    if (!model || value === model.modified.getValue()) return
    applyingExternal = true
    // setValue resets undo history; only used for programmatic reloads.
    model.modified.setValue(value)
    applyingExternal = false
  },
)

watch(
  () => props.original,
  (value) => {
    const model = editor.value?.getModel()
    if (model && value !== model.original.getValue()) model.original.setValue(value)
  },
)

watch(
  () => props.language,
  (language) => {
    const model = editor.value?.getModel()
    if (!model) return
    monaco.editor.setModelLanguage(model.original, language ?? 'plaintext')
    monaco.editor.setModelLanguage(model.modified, language ?? 'plaintext')
  },
)

watch(
  () => props.readonly,
  (readOnly) => editor.value?.updateOptions({ readOnly }),
)

watch(
  () => props.minimap,
  (enabled) => editor.value?.updateOptions({ minimap: { enabled } }),
)

// Imperative hooks for a host toolbar; they act on the editable (modified) pane.
defineExpose({
  undo: () => editor.value?.getModifiedEditor().trigger('toolbar', 'undo', null),
  redo: () => editor.value?.getModifiedEditor().trigger('toolbar', 'redo', null),
  openFind: () => {
    const modified = editor.value?.getModifiedEditor()
    modified?.focus()
    void modified?.getAction('actions.find')?.run()
  },
  focus: () => editor.value?.getModifiedEditor().focus(),
})

onBeforeUnmount(dispose)
</script>

<template>
  <div class="relative h-full w-full">
    <div ref="container" class="h-full w-full" />
    <div v-if="error" class="absolute inset-0 flex items-center justify-center bg-surface/80 p-6">
      <AppAlert tone="danger" title="The diff editor failed to load" class="max-w-md">
        <p class="mb-3">{{ error }}</p>
        <AppButton size="sm" @click="retry">Retry</AppButton>
      </AppAlert>
    </div>
  </div>
</template>
