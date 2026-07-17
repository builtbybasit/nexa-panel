<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import type { JobEvent } from '@/modules/jobs/api'

import AppIcon from './AppIcon.vue'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from './collapsible'
import ProgressBar from './ProgressBar.vue'

const props = defineProps<{
  event: JobEvent
  messages?: { text: string; at: number }[]
  startedAtMs?: number
}>()

const nowMs = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | undefined

const terminal = computed(() => props.event.state === 'succeeded' || props.event.state === 'failed')

// Tick once a second while the job is live so the elapsed counter advances,
// then freeze it at the terminal state.
watch(
  [() => props.startedAtMs, terminal],
  ([startedAt, done]) => {
    nowMs.value = Date.now()
    const shouldTick = startedAt !== undefined && !done
    if (shouldTick && ticker === undefined) {
      ticker = setInterval(() => {
        nowMs.value = Date.now()
      }, 1000)
    } else if (!shouldTick && ticker !== undefined) {
      clearInterval(ticker)
      ticker = undefined
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (ticker !== undefined) clearInterval(ticker)
})

function formatElapsed(milliseconds: number) {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`
}

const elapsed = computed(() =>
  props.startedAtMs === undefined ? '' : formatElapsed(nowMs.value - props.startedAtMs),
)
const stepBaseMs = computed(() => props.startedAtMs ?? props.messages?.[0]?.at ?? 0)
</script>

<template>
  <div class="rounded-xl border border-outline bg-raised/60 px-4 py-3">
    <div class="mb-2 flex items-center justify-between gap-3 text-[13px]" aria-live="polite">
      <span class="min-w-0 truncate text-ink-secondary">{{ event.message }}</span>
      <span class="flex shrink-0 items-baseline gap-2 font-mono text-xs">
        <span v-if="elapsed" class="text-ink-muted">{{ elapsed }}</span>
        <strong class="text-ink">{{ event.progress }}%</strong>
      </span>
    </div>
    <ProgressBar :value="event.progress" :tone="event.state === 'failed' ? 'danger' : 'accent'" />
    <p v-if="event.state === 'failed'" class="mt-2 text-[13px] text-rose-300" aria-live="polite">
      The job failed.
      <RouterLink
        :to="`/jobs?job=${event.jobId}`"
        class="font-medium text-rose-200 underline underline-offset-2 hover:text-rose-100"
      >
        View job #{{ event.jobId }}
      </RouterLink>
    </p>
    <Collapsible v-if="messages && messages.length > 0" class="mt-2">
      <CollapsibleTrigger as-child>
        <button
          type="button"
          class="group flex items-center gap-1 rounded text-xs text-ink-muted transition-colors hover:text-ink focus-visible:outline focus-visible:outline-accent-400"
        >
          <AppIcon name="chevron-down" :size="14" class="transition-transform group-data-[state=open]:rotate-180" />
          <span class="group-data-[state=open]:hidden">Show steps ({{ messages.length }})</span>
          <span class="hidden group-data-[state=open]:inline">Hide steps</span>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <ol class="mt-2 space-y-1 border-l border-outline pl-3">
          <li v-for="(step, index) in messages" :key="index" class="flex items-baseline gap-2 text-xs">
            <span class="shrink-0 font-mono text-ink-muted">+{{ formatElapsed(step.at - stepBaseMs) }}</span>
            <span class="min-w-0 text-ink-secondary">{{ step.text }}</span>
          </li>
        </ol>
      </CollapsibleContent>
    </Collapsible>
  </div>
</template>
