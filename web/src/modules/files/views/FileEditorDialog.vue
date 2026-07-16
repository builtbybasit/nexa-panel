<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, AppIcon, AppTextarea } from '@/shared/ui'

import { downloadUrl, FilesRequestError, readFileContent, writeFileContent } from '../api'

const props = defineProps<{ siteId: string; path: string }>()
const emit = defineEmits<{ close: []; saved: [] }>()

const loading = ref(true)
const loadError = ref('')
/** Set when the file cannot be edited inline; the dialog offers a download instead. */
const uneditable = ref<'binary' | 'truncated'>()
const size = ref(0)
const content = ref('')
const etag = ref('')
const saving = ref(false)
const conflict = ref(false)
const saveError = ref('')

const href = downloadUrl(props.siteId, props.path)

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
      etag.value = file.etag
    }
  } catch (caught) {
    loadError.value = caught instanceof Error ? caught.message : 'The file could not be loaded.'
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function write(expectedEtag: string) {
  saving.value = true
  saveError.value = ''
  try {
    const result = await writeFileContent(props.siteId, { path: props.path, content: content.value, expectedEtag })
    etag.value = result.etag
    conflict.value = false
    emit('saved')
    emit('close')
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

const save = () => write(etag.value)

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
    await write(expected)
  } catch (caught) {
    saveError.value = caught instanceof Error ? caught.message : 'The file could not be saved.'
    saving.value = false
  }
}

function close() {
  if (saving.value) return
  emit('close')
}
</script>

<template>
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true">
    <div class="absolute inset-0 bg-canvas/70 backdrop-blur-sm" aria-hidden="true" @click="close" />
    <div class="relative w-full max-w-3xl">
      <AppCard eyebrow="Edit file" :title="path">
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
              <AppButton @click="close">Close</AppButton>
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
            <AppTextarea v-model="content" rows="18" spellcheck="false" :disabled="saving || conflict" />

            <AppAlert v-if="conflict" tone="warning" title="The file changed on the server">
              <p class="mb-2">Someone else saved this file after you opened it. Reload the server copy, or overwrite it with your edits.</p>
              <span class="flex flex-wrap gap-2">
                <AppButton size="sm" :disabled="saving" @click="reloadServerCopy">Reload server copy</AppButton>
                <AppButton size="sm" variant="danger" :loading="saving" @click="overwriteServerCopy">Overwrite server copy</AppButton>
              </span>
            </AppAlert>
            <AppAlert v-if="saveError" tone="danger">{{ saveError }}</AppAlert>

            <div class="flex items-center justify-between gap-3">
              <span class="font-mono text-[11px] text-ink-muted">{{ formatBytes(size) }}</span>
              <span class="flex gap-2">
                <AppButton :disabled="saving" @click="close">Cancel</AppButton>
                <AppButton variant="primary" :loading="saving" :disabled="conflict" @click="save">Save file</AppButton>
              </span>
            </div>
          </template>
        </div>
      </AppCard>
    </div>
  </div>
</template>
