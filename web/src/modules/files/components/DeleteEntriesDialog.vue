<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { AppAlert, AppConfirmDialog } from '@/shared/ui'

import { deleteEntry, type FileEntry } from '../api'
import { countLabel, displayPathOf, joinPath } from '../lib'

const props = defineProps<{ open: boolean; siteId: string; directory: string; entries: FileEntry[] }>()
const emit = defineEmits<{ close: []; deleted: [names: string[]] }>()

/**
 * Copied off the selection when the dialog opens: entries that go are struck
 * from this list as they succeed, so a retry after a partial failure only
 * repeats what is actually left.
 */
const pending = ref<FileEntry[]>([])
const busy = ref(false)
const error = ref('')

watch(
  () => props.open,
  (open) => {
    if (!open) return
    pending.value = [...props.entries]
    error.value = ''
  },
  { immediate: true },
)

const hasDirectory = computed(() => pending.value.some((entry) => entry.kind === 'dir'))
const solo = computed(() => (pending.value.length === 1 ? pending.value[0] : undefined))

/** Typing the name is asked for whenever a delete would recurse into a folder. */
const typeToConfirm = computed(() => {
  if (!hasDirectory.value) return undefined
  return solo.value ? solo.value.name : 'delete'
})

async function confirm() {
  busy.value = true
  const removed: string[] = []
  const failures: string[] = []
  for (const entry of [...pending.value]) {
    try {
      await deleteEntry(props.siteId, { path: joinPath(props.directory, entry.name), recursive: entry.kind === 'dir' })
      removed.push(entry.name)
      pending.value = pending.value.filter((candidate) => candidate.name !== entry.name)
    } catch (caught) {
      failures.push(`${entry.name}: ${caught instanceof Error ? caught.message : 'failed'}`)
    }
  }
  busy.value = false
  error.value = failures.length ? `Some items failed — ${failures.join('; ')}` : ''
  emit('deleted', removed)
  if (!failures.length) emit('close')
}
</script>

<template>
  <AppConfirmDialog
    :open="open"
    :title="solo ? 'Delete entry' : `Delete ${countLabel(pending.length)}`"
    :confirm-label="solo ? 'Delete' : `Delete ${countLabel(pending.length)}`"
    v-bind="typeToConfirm ? { typeToConfirm } : {}"
    :busy="busy"
    @confirm="confirm"
    @close="emit('close')"
  >
    <p v-if="solo">
      Delete <strong class="font-mono font-semibold text-ink">{{ solo.name }}</strong
      ><template v-if="solo.kind === 'dir'"> and everything inside it</template>? This cannot be undone.
    </p>
    <p v-else>
      Delete the {{ countLabel(pending.length) }} selected in
      <span class="font-mono text-ink">{{ displayPathOf(directory) }}</span
      ><template v-if="hasDirectory">, including everything inside the selected folders</template>? This cannot be
      undone.
    </p>
    <AppAlert v-if="error" tone="danger" class="mt-3">{{ error }}</AppAlert>
  </AppConfirmDialog>
</template>
