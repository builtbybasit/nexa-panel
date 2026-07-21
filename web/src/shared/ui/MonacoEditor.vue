<script setup lang="ts">
import * as monaco from 'monaco-editor'
import { nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'

import AppAlert from './AppAlert.vue'
import AppButton from './AppButton.vue'
import { NEXA_MONACO_THEME } from './monacoTheme'
import './monacoWorkers'

/**
 * Thin Monaco wrapper. Kept behind a lazy import so the (large) editor and its
 * language workers are only fetched when a file is actually opened. The web
 * workers are configured as a side effect of importing `./monacoWorkers`.
 *
 * Editor creation is guarded: if `monaco.editor.create` throws (a broken worker
 * bundle, an unsupported browser), the wrapper surfaces a styled error with a
 * Retry action instead of leaving a blank box behind. Pattern adapted from
 * bazingaedward/monaco-editor-vue3's error-boundary hook.
 */

const props = withDefaults(
  defineProps<{ modelValue: string; language?: string; readonly?: boolean; minimap?: boolean }>(),
  { language: 'plaintext', readonly: false, minimap: true },
)
const emit = defineEmits<{ 'update:modelValue': [string]; save: [] }>()

const container = ref<HTMLElement>()
const editor = shallowRef<monaco.editor.IStandaloneCodeEditor>()
/** Non-empty while the editor could not be created; drives the error overlay. */
const error = ref('')
/** Guards the change handler while we push external updates into the model. */
let applyingExternal = false

function createEditor() {
  if (!container.value) return
  try {
    const instance = monaco.editor.create(container.value, {
      value: props.modelValue,
      language: props.language,
      readOnly: props.readonly,
      theme: NEXA_MONACO_THEME,
      automaticLayout: true,
      fontSize: 13,
      lineNumbersMinChars: 3,
      tabSize: 2,
      scrollBeyondLastLine: false,
      minimap: { enabled: props.minimap },
      smoothScrolling: true,
      renderWhitespace: 'selection',
      fixedOverflowWidgets: true,
    })

    instance.onDidChangeModelContent(() => {
      if (applyingExternal) return
      emit('update:modelValue', instance.getValue())
    })
    // Monaco swallows keydown, so bind save inside the editor as well.
    instance.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => emit('save'))

    editor.value = instance
    error.value = ''
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'The editor could not be initialised.'
  }
}

/** Tear down any half-built editor, then rebuild from scratch. */
async function retry() {
  editor.value?.getModel()?.dispose()
  editor.value?.dispose()
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
    const ed = editor.value
    if (!ed || value === ed.getValue()) return
    applyingExternal = true
    // setValue resets undo history; only used for programmatic reloads.
    ed.setValue(value)
    applyingExternal = false
  },
)

watch(
  () => props.language,
  (language) => {
    const model = editor.value?.getModel()
    if (model) monaco.editor.setModelLanguage(model, language ?? 'plaintext')
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

// Imperative hooks for a host toolbar (undo/redo/find live inside Monaco).
defineExpose({
  undo: () => editor.value?.trigger('toolbar', 'undo', null),
  redo: () => editor.value?.trigger('toolbar', 'redo', null),
  openFind: () => {
    editor.value?.focus()
    void editor.value?.getAction('actions.find')?.run()
  },
  focus: () => editor.value?.focus(),
})

onBeforeUnmount(() => {
  editor.value?.getModel()?.dispose()
  editor.value?.dispose()
})
</script>

<template>
  <div class="relative h-full w-full">
    <div ref="container" class="h-full w-full" />
    <div v-if="error" class="absolute inset-0 flex items-center justify-center bg-surface/80 p-6">
      <AppAlert tone="danger" title="The editor failed to load" class="max-w-md">
        <p class="mb-3">{{ error }}</p>
        <AppButton size="sm" @click="retry">Retry</AppButton>
      </AppAlert>
    </div>
  </div>
</template>
