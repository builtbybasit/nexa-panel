<script setup lang="ts">
import { computed } from 'vue'

import { AppButton, AppDialog } from '@/shared/ui'

import type { ClipboardMode, PasteStrategy } from '../composables/useFileClipboard'
import { countLabel, displayPathOf } from '../lib'

const props = defineProps<{
  open: boolean
  mode: ClipboardMode
  conflicts: string[]
  total: number
  destination: string
}>()

const emit = defineEmits<{ close: []; resolve: [strategy: PasteStrategy] }>()

const remaining = computed(() => props.total - props.conflicts.length)
/** Copy has no overwrite on the wire, so only a move can replace what is there. */
const canReplace = computed(() => props.mode === 'cut')
</script>

<template>
  <AppDialog
    :open="open"
    :title="`${countLabel(conflicts.length)} already exist here`"
    :description="`Pasting into ${displayPathOf(destination)}.`"
    size="sm"
    @close="emit('close')"
  >
    <div class="space-y-3">
      <ul class="max-h-40 space-y-1 overflow-y-auto rounded-xl border border-outline bg-canvas/40 p-2">
        <li v-for="name in conflicts" :key="name" class="truncate font-mono text-[13px] text-ink">{{ name }}</li>
      </ul>
      <p class="text-[13px] text-ink-secondary">
        <template v-if="remaining > 0">The other {{ countLabel(remaining) }} paste either way. </template>
        Choose what happens to the names above.
      </p>
    </div>
    <template #footer>
      <AppButton class="mr-auto" @click="emit('close')">Cancel</AppButton>
      <AppButton @click="emit('resolve', 'skip')">Skip them</AppButton>
      <AppButton v-if="canReplace" variant="danger" @click="emit('resolve', 'replace')">Replace</AppButton>
      <AppButton variant="primary" @click="emit('resolve', 'keep-both')">Keep both</AppButton>
    </template>
  </AppDialog>
</template>
