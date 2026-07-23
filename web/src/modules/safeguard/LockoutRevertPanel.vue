<script setup lang="ts">
// The visible half of the server's confirm-or-revert window. It never owns the
// deadline — it renders the server's `expiresAt` and ticks a local clock against
// it — so a reloaded or reopened page shows the same countdown, and a page that
// is never reopened changes nothing about whether the revert fires.
import { computed, onBeforeUnmount, ref } from 'vue'

import { AppAlert, AppButton } from '@/shared/ui'

import type { LockoutRevert } from './types'

const props = defineProps<{ reverts: LockoutRevert[]; confirming?: string; disabled?: boolean }>()
const emit = defineEmits<{ confirm: [id: string] }>()

const nowMs = ref(Date.now())
const ticker = window.setInterval(() => (nowMs.value = Date.now()), 1000)
onBeforeUnmount(() => window.clearInterval(ticker))

const armed = computed(() => props.reverts.filter((revert) => revert.state === 'armed'))
// Only the most recent settled revert is worth showing: it answers "did my
// access actually come back?" without turning the page into a history log.
const settled = computed(() =>
  props.reverts.filter((revert) => revert.state === 'reverted' || revert.state === 'revert_failed').slice(0, 1),
)

function remaining(revert: LockoutRevert): string {
  const seconds = Math.max(0, Math.round((new Date(revert.expiresAt).getTime() - nowMs.value) / 1000))
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}

function overdue(revert: LockoutRevert): boolean {
  return new Date(revert.expiresAt).getTime() <= nowMs.value
}
</script>

<template>
  <div v-if="armed.length || settled.length" class="space-y-3">
    <AppAlert v-for="revert in armed" :key="revert.id" tone="warning">
      <div class="space-y-2">
        <p class="text-sm font-medium text-ink">
          Automatic rollback armed for {{ revert.subject }} —
          <span v-if="!overdue(revert)" class="font-mono">{{ remaining(revert) }}</span>
          <span v-else>rolling back now</span>
        </p>
        <p>{{ revert.summary }}</p>
        <ul v-if="revert.reasons.length" class="list-disc space-y-1 pl-5">
          <li v-for="reason in revert.reasons" :key="reason">{{ reason }}</li>
        </ul>
        <p class="text-xs text-ink-muted">
          Check that your SSH session and this panel still work, then confirm. If you do nothing — or close this tab —
          the change is undone automatically when the timer runs out.
        </p>
        <div>
          <AppButton
            size="sm"
            variant="primary"
            icon="check"
            :disabled="disabled"
            :loading="confirming === revert.id"
            @click="emit('confirm', revert.id)"
          >
            My access still works — keep the change
          </AppButton>
        </div>
      </div>
    </AppAlert>

    <AppAlert
      v-for="revert in settled"
      :key="revert.id"
      :tone="revert.state === 'reverted' ? 'info' : 'danger'"
    >
      <template v-if="revert.state === 'reverted'">
        No confirmation arrived in time, so the change to {{ revert.subject }} was rolled back automatically.
      </template>
      <template v-else>
        The automatic rollback for {{ revert.subject }} failed: {{ revert.failure }}. Restore access from a root console.
      </template>
    </AppAlert>
  </div>
</template>
