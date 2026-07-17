<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import AppAlert from './AppAlert.vue'
import AppButton from './AppButton.vue'
import AppDialog from './AppDialog.vue'
import FactList, { type Fact } from './FactList.vue'
import PlanSteps from './PlanSteps.vue'
import StatusPill from './StatusPill.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    steps?: string[]
    facts?: Fact[]
    warnings?: string[]
    expiresAt?: string
    busy?: boolean
    approveLabel?: string
    /** Set false when the consumer has no way to produce a fresh plan. */
    canRegenerate?: boolean
  }>(),
  { approveLabel: 'Approve plan', canRegenerate: true },
)

const emit = defineEmits<{ approve: []; regenerate: []; close: [] }>()

const now = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | undefined

const expiresAtMs = computed(() => {
  if (!props.expiresAt) return undefined
  const parsed = Date.parse(props.expiresAt)
  return Number.isFinite(parsed) ? parsed : undefined
})

const remainingMs = computed(() => (expiresAtMs.value === undefined ? undefined : expiresAtMs.value - now.value))

const expired = computed(() => remainingMs.value !== undefined && remainingMs.value <= 0)

const countdown = computed(() => {
  const remaining = remainingMs.value
  if (remaining === undefined) return ''
  if (remaining <= 0) return 'Plan expired'
  const totalSeconds = Math.ceil(remaining / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  if (minutes >= 60) return `Expires in ${Math.floor(minutes / 60)}h ${minutes % 60}m`
  return `Expires in ${minutes}:${String(totalSeconds % 60).padStart(2, '0')}`
})

const pillTone = computed(() => ((remainingMs.value ?? Infinity) < 60_000 ? 'danger' : 'warning'))

// Tick once a second while the dialog is open and a deadline exists so the
// countdown advances and the expiry flip happens without a re-render trigger.
watch(
  [() => props.open, expiresAtMs],
  ([open, deadline]) => {
    const shouldTick = open && deadline !== undefined
    if (shouldTick) now.value = Date.now()
    if (shouldTick && ticker === undefined) {
      ticker = setInterval(() => {
        now.value = Date.now()
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
</script>

<template>
  <AppDialog :open="open" size="lg" @close="emit('close')">
    <template #title>
      <span class="flex flex-wrap items-center gap-2.5">
        {{ title }}
        <StatusPill v-if="countdown" :label="countdown" :tone="pillTone" class="normal-case" />
      </span>
    </template>

    <div class="space-y-5">
      <AppAlert v-if="expired" tone="danger">
        This plan has expired and can no longer be applied.<template v-if="canRegenerate">
          Regenerate it to continue.</template>
      </AppAlert>
      <FactList v-if="facts?.length" :facts="facts" />
      <PlanSteps v-if="steps?.length || warnings?.length" :steps="steps ?? []" :warnings="warnings ?? []" />
      <slot />
    </div>

    <template #footer>
      <AppButton :disabled="busy" @click="emit('close')">Cancel</AppButton>
      <AppButton
        v-if="expired && canRegenerate"
        variant="primary"
        icon="refresh-cw"
        :loading="busy"
        @click="emit('regenerate')"
      >
        Regenerate plan
      </AppButton>
      <AppButton v-else-if="!expired" variant="primary" :loading="busy" @click="emit('approve')">
        {{ approveLabel }}
      </AppButton>
    </template>
  </AppDialog>
</template>
