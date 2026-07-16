<script setup lang="ts">
import { computed } from 'vue'

export type PillTone = 'success' | 'warning' | 'danger' | 'info' | 'accent' | 'neutral'

const props = defineProps<{
  /** Raw machine status such as `plan_ready`; humanized for display. */
  status?: string
  /** Explicit label overriding the humanized status. */
  label?: string
  /** Explicit tone overriding the status-derived tone. */
  tone?: PillTone
  pulse?: boolean
}>()

const toneByStatus: Record<string, PillTone> = {
  active: 'success',
  online: 'success',
  succeeded: 'success',
  verified: 'success',
  available: 'success',
  ready: 'success',
  failed: 'danger',
  rollback_failed: 'danger',
  revoked: 'danger',
  error: 'danger',
  unavailable: 'danger',
  plan_ready: 'warning',
  expiring: 'warning',
  pending: 'warning',
  stopped: 'neutral',
  inactive: 'neutral',
  queued: 'info',
  planning: 'info',
  running: 'info',
  activating: 'info',
  verifying: 'info',
  rolling_back: 'info',
  created: 'info',
  uploaded: 'info',
}

const tone = computed<PillTone>(() => props.tone ?? toneByStatus[props.status ?? ''] ?? 'neutral')
const text = computed(() => props.label ?? (props.status ?? '').replaceAll('_', ' '))
const busy = computed(
  () => props.pulse ?? ['queued', 'planning', 'running', 'activating', 'verifying', 'rolling_back'].includes(props.status ?? ''),
)

const toneClasses: Record<PillTone, string> = {
  success: 'bg-emerald-400/10 text-emerald-300 border-emerald-400/25',
  warning: 'bg-amber-400/10 text-amber-300 border-amber-400/25',
  danger: 'bg-rose-400/10 text-rose-300 border-rose-400/25',
  info: 'bg-sky-400/10 text-sky-300 border-sky-400/25',
  accent: 'bg-accent-400/10 text-accent-300 border-accent-400/25',
  neutral: 'bg-white/[0.05] text-ink-secondary border-outline-strong',
}
</script>

<template>
  <span
    class="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[11px] font-semibold whitespace-nowrap capitalize"
    :class="toneClasses[tone]"
  >
    <span class="size-1.5 rounded-full bg-current" :class="busy ? 'animate-pulse' : ''" aria-hidden="true" />
    {{ text }}
  </span>
</template>
