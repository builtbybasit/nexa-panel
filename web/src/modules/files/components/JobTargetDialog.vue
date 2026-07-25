<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { AppButton, AppDialog, AppInput, FormField } from '@/shared/ui'

import { defaultArchiveName, joinPath } from '../lib'
import DirectoryPickerDialog from './DirectoryPickerDialog.vue'

/**
 * Destination prompt for the two operations that genuinely need one: packing an
 * archive names a new file, and extracting one names a folder that may not exist
 * yet — neither is a place the user can navigate to and paste into.
 */
const props = defineProps<{
  open: boolean
  kind: 'archive' | 'extract'
  siteId: string
  title: string
  initialTarget: string
}>()

const emit = defineEmits<{ close: []; submit: [target: string] }>()

const target = ref('')
const pickerOpen = ref(false)

watch(
  () => [props.open, props.initialTarget] as const,
  ([open, initial]) => {
    if (open) target.value = initial
  },
  { immediate: true },
)

const copy = computed(() =>
  props.kind === 'archive'
    ? { label: 'Target archive', hint: 'A .tar.gz path inside public/, private/, tmp/, or backups/.', action: 'Create archive' }
    : {
        label: 'Target directory',
        hint: 'Site-relative directory inside public/, private/, tmp/, or backups/. It is created if missing.',
        action: 'Extract',
      },
)

/** The picker returns a folder; an archive target also needs its file name back. */
function applyPicked(picked: string) {
  pickerOpen.value = false
  if (props.kind === 'archive') {
    const basename = target.value.trim().split('/').pop() || defaultArchiveName()
    target.value = joinPath(picked, basename)
  } else {
    target.value = picked === '.' ? '' : picked
  }
}

function onSubmit() {
  const trimmed = target.value.trim()
  if (trimmed) emit('submit', trimmed)
}
</script>

<template>
  <AppDialog :open="open" :title="title" @close="emit('close')">
    <form class="space-y-4" @submit.prevent="onSubmit">
      <FormField :label="copy.label" :hint="copy.hint">
        <div class="flex gap-2">
          <AppInput
            v-model="target"
            class="font-mono"
            autocomplete="off"
            :pattern="kind === 'archive' ? '.+\\.tar\\.gz' : undefined"
            required
          />
          <AppButton icon="folder-open" @click="pickerOpen = true">Browse…</AppButton>
        </div>
      </FormField>
      <div class="flex justify-end gap-2">
        <AppButton @click="emit('close')">Cancel</AppButton>
        <AppButton variant="primary" type="submit">{{ copy.action }}</AppButton>
      </div>
    </form>
  </AppDialog>
  <!-- A sibling, not a child: the picker is its own modal layer on top. -->
  <DirectoryPickerDialog :open="pickerOpen" :site-id="siteId" @close="pickerOpen = false" @select="applyPicked" />
</template>
