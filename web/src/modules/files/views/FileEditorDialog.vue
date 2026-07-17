<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { AppAlert, AppButton, AppConfirmDialog, AppDialog, AppIcon, AppTextarea } from '@/shared/ui'

import { downloadUrl, FilesRequestError, readFileContent, writeFileContent } from '../api'

const props = defineProps<{ siteId: string; path: string }>()
const emit = defineEmits<{ close: []; saved: [] }>()

const loading = ref(true)
const loadError = ref('')
/** Set when the file cannot be edited inline; the dialog offers a download instead. */
const uneditable = ref<'binary' | 'truncated'>()
const size = ref(0)
const content = ref('')
/** Last content loaded from or written to the server; drives the dirty flag. */
const savedContent = ref('')
const etag = ref('')
const saving = ref(false)
const conflict = ref(false)
const saveError = ref('')
const justSaved = ref(false)
const discardAsk = ref(false)

const href = downloadUrl(props.siteId, props.path)

const dirty = computed(
  () => !loading.value && !loadError.value && !uneditable.value && content.value !== savedContent.value,
)

async function load() {
  loading.value = true
  loadError.value = ''
  uneditable.value = undefined
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
    } else {
      saveError.value = caught instanceof Error ? caught.message : 'The file could not be saved.'
    }
  } finally {
    saving.value = false
  }
}

const save = () => write(etag.value, false)
const saveAndClose = () => write(etag.value, true)

/** Discard the local edits and load the current server copy. */
async function reloadServerCopy() {
  conflict.value = false
  await load()
}

/** Keep the local edits: fetch the fresh etag, then save over the server copy. */
async function overwriteServerCopy() {
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

/** Escape, the close button, backdrop clicks, and Close all route through the dirty guard. */
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

function onKeydown(event: KeyboardEvent) {
  if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== 's') return
  event.preventDefault()
  if (loading.value || loadError.value || uneditable.value || saving.value || conflict.value || discardAsk.value) return
  void save()
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  void load()
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <AppDialog open size="lg" @close="requestClose">
    <template #title>
      <span class="font-mono text-[13px]">{{ path }}</span>
    </template>

    <div class="space-y-4">
      <AppAlert v-if="loading" tone="info">Loading file content…</AppAlert>
      <AppAlert v-else-if="loadError" tone="danger">{{ loadError }}</AppAlert>

      <template v-else-if="uneditable">
        <AppAlert tone="warning">
          {{
            uneditable === 'binary'
              ? 'This file contains binary data and cannot be edited inline.'
              : `This file is larger than the inline editing limit (${formatBytes(size)}).`
          }}
          Download it instead.
        </AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton @click="emit('close')">Close</AppButton>
          <a
            :href="href"
            class="inline-flex h-10 items-center justify-center gap-2 rounded-lg bg-accent-500 px-4 text-sm font-semibold text-accent-950 transition-colors hover:bg-accent-400"
          >
            <AppIcon name="download" :size="16" />
            Download
          </a>
        </div>
      </template>

      <template v-else>
        <AppTextarea v-model="content" rows="18" spellcheck="false" aria-label="File content" :disabled="saving || conflict" />

        <AppAlert v-if="conflict" tone="warning" title="The file changed on the server">
          <p class="mb-2">Someone else saved this file after you opened it. Reload the server copy, or overwrite it with your edits.</p>
          <span class="flex flex-wrap gap-2">
            <AppButton size="sm" :disabled="saving" @click="reloadServerCopy">Reload server copy</AppButton>
            <AppButton size="sm" variant="danger" :loading="saving" @click="overwriteServerCopy">Overwrite server copy</AppButton>
          </span>
        </AppAlert>
        <AppAlert v-if="saveError" tone="danger">{{ saveError }}</AppAlert>

        <div class="flex flex-wrap items-center justify-between gap-3">
          <span class="flex min-w-0 items-center gap-3 text-[11px]">
            <span class="font-mono text-ink-muted">{{ formatBytes(size) }}</span>
            <span aria-live="polite">
              <span v-if="dirty" class="text-amber-300">Unsaved changes</span>
              <span v-else-if="justSaved" class="inline-flex items-center gap-1 text-emerald-300">
                <AppIcon name="check" :size="12" />
                Saved
              </span>
            </span>
            <span class="hidden text-ink-muted sm:inline">Ctrl/Cmd+S saves</span>
          </span>
          <span class="flex flex-wrap gap-2">
            <AppButton :disabled="saving" @click="requestClose">Close</AppButton>
            <AppButton :loading="saving" :disabled="conflict" @click="save">Save</AppButton>
            <AppButton variant="primary" :loading="saving" :disabled="conflict" @click="saveAndClose">Save and close</AppButton>
          </span>
        </div>
      </template>
    </div>
  </AppDialog>

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
