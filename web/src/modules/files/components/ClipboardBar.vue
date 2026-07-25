<script setup lang="ts">
import { AppButton, AppIcon } from '@/shared/ui'

import { displayPathOf } from '../lib'
import type { ClipboardMode } from '../composables/useFileClipboard'

defineProps<{
  mode: ClipboardMode
  label: string
  sourceDirectory: string
  canPaste: boolean
  blockedReason: string
  busy: boolean
}>()

const emit = defineEmits<{ paste: []; clear: [] }>()
</script>

<template>
  <div
    class="flex flex-wrap items-center gap-2 border-b border-accent-500/25 bg-accent-500/[0.07] px-3 py-2"
    aria-live="polite"
  >
    <AppIcon :name="mode === 'cut' ? 'scissors' : 'copy'" :size="15" class="shrink-0 text-accent-300" />
    <span class="text-[13px] text-ink">{{ label }}</span>
    <span class="min-w-0 truncate font-mono text-xs text-ink-muted">from {{ displayPathOf(sourceDirectory) }}</span>
    <span class="ml-auto flex items-center gap-2">
      <span v-if="blockedReason" class="text-xs text-amber-300">{{ blockedReason }}</span>
      <AppButton variant="primary" size="sm" icon="clipboard-paste" :disabled="!canPaste" :loading="busy" @click="emit('paste')">
        Paste here
      </AppButton>
      <AppButton size="sm" variant="ghost" icon="x" aria-label="Cancel the pending paste" @click="emit('clear')" />
    </span>
  </div>
</template>
