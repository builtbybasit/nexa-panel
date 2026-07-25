<script setup lang="ts">
import { computed, ref, toRef, watch } from 'vue'

import { AppAlert, AppButton, AppDialog, AppInput, FormField } from '@/shared/ui'

import { makeDirectory, moveEntry, writeFileContent, type FileEntry } from '../api'
import { useDialogForm } from '../composables/useDialogForm'
import { displayPathOf, joinPath, type NameDialogKind } from '../lib'

const props = defineProps<{
  open: boolean
  kind: NameDialogKind
  siteId: string
  directory: string
  entry: FileEntry | undefined
}>()

const emit = defineEmits<{ close: []; done: [] }>()

const name = ref('')
const { busy, error, submit } = useDialogForm(
  toRef(props, 'open'),
  () => {
    emit('done')
    emit('close')
  },
)

watch(
  () => [props.open, props.kind, props.entry?.name] as const,
  ([open]) => {
    if (open) name.value = props.kind === 'rename' ? (props.entry?.name ?? '') : ''
  },
  { immediate: true },
)

const copy = computed(() => {
  switch (props.kind) {
    case 'mkdir':
      return {
        title: 'New folder',
        label: 'Name',
        hint: `Created inside ${displayPathOf(props.directory)}. Nested paths such as a/b are allowed.`,
        action: 'Create folder',
      }
    case 'newfile':
      return {
        title: 'New file',
        label: 'Name',
        hint: `An empty file is created inside ${displayPathOf(props.directory)}; click it to edit.`,
        action: 'Create file',
      }
    default:
      return {
        title: `Rename ${props.entry?.name ?? ''}`,
        label: 'New name',
        hint: `Stays inside ${displayPathOf(props.directory)}.`,
        action: 'Rename',
      }
  }
})

function onSubmit() {
  const trimmed = name.value.trim()
  if (!trimmed) return
  const target = joinPath(props.directory, trimmed)
  void submit(async () => {
    if (props.kind === 'mkdir') await makeDirectory(props.siteId, target)
    else if (props.kind === 'newfile') await writeFileContent(props.siteId, { path: target, content: '', expectedEtag: '' })
    else if (props.entry)
      await moveEntry(props.siteId, { from: joinPath(props.directory, props.entry.name), to: target, overwrite: false })
  })
}
</script>

<template>
  <AppDialog :open="open" :title="copy.title" @close="emit('close')">
    <form class="space-y-4" @submit.prevent="onSubmit">
      <FormField :label="copy.label" :hint="copy.hint">
        <AppInput v-model="name" autocomplete="off" required />
      </FormField>
      <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>
      <div class="flex justify-end gap-2">
        <AppButton :disabled="busy" @click="emit('close')">Cancel</AppButton>
        <AppButton variant="primary" type="submit" :loading="busy">{{ copy.action }}</AppButton>
      </div>
    </form>
  </AppDialog>
</template>
