<script setup lang="ts">
import { formatBytes } from '@/shared/formatters'
import { AppButton, AppCard, ProgressBar, StatusPill } from '@/shared/ui'

import type { UploadItem } from '../composables/useFileUploads'

defineProps<{ uploads: UploadItem[]; percent: (item: UploadItem) => number }>()
const emit = defineEmits<{ retry: [item: UploadItem]; dismiss: [item: UploadItem]; 'clear-finished': [] }>()

const tones = { uploading: 'info', done: 'success', failed: 'danger' } as const
const labels = { uploading: 'Uploading', done: 'Uploaded', failed: 'Failed' } as const
</script>

<template>
  <AppCard eyebrow="Transfers" title="Uploads">
    <template #actions>
      <AppButton size="sm" @click="emit('clear-finished')">Clear finished</AppButton>
    </template>
    <ul class="space-y-3">
      <li v-for="(item, index) in uploads" :key="`${item.targetPath}-${index}`" class="space-y-1.5">
        <div class="flex items-center justify-between gap-3 text-[13px]">
          <span class="min-w-0 truncate font-mono text-ink">{{ item.targetPath }}</span>
          <span class="flex shrink-0 items-center gap-2">
            <span class="font-mono text-[11px] text-ink-muted">
              {{ formatBytes(item.sent) }} / {{ formatBytes(item.total) }}
            </span>
            <AppButton v-if="item.status === 'failed'" size="sm" @click="emit('retry', item)">
              Retry and overwrite
            </AppButton>
            <StatusPill :tone="tones[item.status]" :label="labels[item.status]" :pulse="item.status === 'uploading'" />
            <AppButton
              v-if="item.status !== 'uploading'"
              size="sm"
              variant="ghost"
              icon="x"
              :aria-label="`Dismiss ${item.targetPath}`"
              @click="emit('dismiss', item)"
            />
          </span>
        </div>
        <ProgressBar :value="percent(item)" :tone="item.status === 'failed' ? 'danger' : 'accent'" />
        <p v-if="item.error" class="text-xs text-rose-300">{{ item.error }}</p>
      </li>
    </ul>
  </AppCard>
</template>
