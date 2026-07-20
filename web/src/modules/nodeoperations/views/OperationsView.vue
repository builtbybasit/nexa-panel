<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { formatTime } from '@/shared/formatters'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppTextarea,
  EmptyState,
  FactList,
  FormField,
  JobFailureNotice,
  JobProgress,
  StatusPill,
  PageHeader,
} from '@/shared/ui'

import { applyPlan, observeProbe, planProbe, rollbackPlan, type OperationPlan } from '../api'

const desiredPresent = ref(true)
const desiredContent = ref('managed=true\n')
const plan = ref<OperationPlan>()
const applied = ref(false)
const planning = ref(false)
const planError = ref('')
const confirmRollbackOpen = ref(false)

const runner = useJobRunner()
const identity = useIdentityStore()
const canPlan = computed(() => identity.can('operations.plan'))
const canApply = computed(() => identity.can('operations.apply'))

// Spread-bound so the optional props are omitted (not passed as undefined),
// which exactOptionalPropertyTypes requires.
const runnerJobLink = computed(() => (runner.jobId.value === undefined ? {} : { jobId: runner.jobId.value }))
const runnerTiming = computed(() => (runner.startedAtMs.value === undefined ? {} : { startedAtMs: runner.startedAtMs.value }))

const observationQuery = useQuery({ queryKey: ['node', 'probe'], queryFn: observeProbe, enabled: canPlan, retry: false })
const current = computed(() => observationQuery.data.value)

function capitalize(value: string): string {
  return (value[0]?.toUpperCase() ?? '') + value.slice(1)
}

function shortDigest(value?: string): string {
  return value ? `${value.slice(0, 12)}…` : 'Not present'
}

const planTitle = computed(() => {
  const currentPlan = plan.value
  if (!currentPlan) return ''
  return currentPlan.action === 'none' ? 'No changes required' : `${capitalize(currentPlan.action)} managed probe`
})

const planFacts = computed(() =>
  plan.value
    ? [
        { label: 'Plan kind', value: plan.value.kind, mono: true },
        { label: 'Target path', value: plan.value.target, mono: true },
        { label: 'Action', value: capitalize(plan.value.action) },
        { label: 'Expires', value: formatTime(plan.value.expiresAt) },
      ]
    : [],
)

/** Before/desired snapshots labeled by state, with digests demoted to secondary mono text. */
const snapshotEntries = computed(() => {
  const currentPlan = plan.value
  if (!currentPlan) return []
  return [
    { label: 'Before', state: currentPlan.before.exists ? 'File present' : 'File absent', digest: currentPlan.before.digest },
    { label: 'Desired', state: currentPlan.desired.exists ? 'File present' : 'File absent', digest: currentPlan.desired.digest },
  ]
})

async function createPlan() {
  if (!canPlan.value) return
  planning.value = true
  planError.value = ''
  runner.error.value = ''
  runner.progress.value = undefined
  applied.value = false
  try {
    plan.value = await planProbe(desiredPresent.value ? { present: true, content: desiredContent.value } : { present: false })
  } catch (caught) {
    planError.value = caught instanceof Error ? caught.message : 'The operation could not be planned.'
  } finally {
    planning.value = false
  }
}

async function queueOperation(rollback: boolean) {
  const currentPlan = plan.value
  if (!currentPlan || !canApply.value) return
  await runner.run(async () => (rollback ? await rollbackPlan(currentPlan) : await applyPlan(currentPlan)).id, {
    onSettled: async (event) => {
      applied.value = event.state === 'succeeded' && !rollback
      if (rollback && event.state === 'succeeded') plan.value = undefined
      await observationQuery.refetch()
    },
    failureMessage: 'The node operation failed.',
  })
}

function confirmRollback() {
  if (!canApply.value) return
  confirmRollbackOpen.value = false
  void queueOperation(true)
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Reviewed privileged workflow"
      title="Node operation tracer"
      description="A fixed, Nexa-owned probe path exercises the plan, apply, observe, and rollback pipeline end to end."
    >
      <StatusPill tone="accent" label="Fixed target only" :pulse="false" />
    </PageHeader>

    <AppAlert v-if="observationQuery.isError.value" tone="warning">
      The privileged agent is unavailable. Start <code class="font-mono">nexa agent</code> before planning an operation.
    </AppAlert>
    <AppAlert v-if="!canPlan" tone="warning">
      Your account does not have permission to prepare node operations.
    </AppAlert>

    <div v-if="canPlan" class="grid gap-4 lg:grid-cols-2">
      <AppCard
        eyebrow="Desired state"
        title="Managed probe file"
        description="This tracer can affect only the Nexa-owned probe path configured on the agent."
      >
        <div class="space-y-4">
          <label class="flex cursor-pointer items-start gap-3 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
            <input v-model="desiredPresent" type="checkbox" class="mt-1 size-4 accent-teal-400" />
            <span>
              <strong class="block text-sm font-semibold text-ink">File should exist</strong>
              <small class="block text-xs text-ink-muted">Turn off to plan its removal.</small>
            </span>
          </label>

          <FormField v-if="desiredPresent" label="Managed content">
            <AppTextarea v-model="desiredContent" rows="5" maxlength="256" required />
          </FormField>

          <div class="flex items-center justify-between rounded-xl border border-outline bg-canvas/40 px-4 py-3">
            <span>
              <span class="block text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Observed now</span>
              <strong class="block text-sm font-semibold text-ink">{{ current?.exists ? 'Present' : 'Absent' }}</strong>
            </span>
            <code class="font-mono text-xs text-accent-200">{{ shortDigest(current?.digest) }}</code>
          </div>

          <AppButton
            variant="primary"
            :loading="planning"
            :disabled="runner.busy.value || observationQuery.isError.value"
            class="w-full"
            @click="createPlan"
          >
            Preview plan
          </AppButton>

          <AppAlert v-if="planError" tone="danger">{{ planError }}</AppAlert>
        </div>
      </AppCard>

      <AppCard v-if="plan" :eyebrow="`Plan ${plan.id.slice(0, 8)}`" :title="planTitle">
        <template #actions>
          <StatusPill :tone="plan.changed ? 'warning' : 'success'" :label="plan.changed ? 'Review required' : 'In sync'" />
        </template>

        <div class="space-y-4">
          <FactList :facts="planFacts" />

          <div class="grid gap-3 sm:grid-cols-2">
            <div v-for="entry in snapshotEntries" :key="entry.label" class="rounded-xl border border-outline bg-canvas/40 px-4 py-3">
              <span class="block text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">{{ entry.label }}</span>
              <strong class="mt-0.5 block text-sm font-semibold text-ink">{{ entry.state }}</strong>
              <code v-if="entry.digest" class="mt-1 block truncate font-mono text-[11px] text-ink-muted" :title="entry.digest">
                {{ entry.digest }}
              </code>
            </div>
          </div>

          <JobProgress
            v-if="runner.progress.value"
            :event="runner.progress.value"
            :messages="runner.messages.value"
            v-bind="runnerTiming"
          />
          <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="runnerJobLink" />
          <AppAlert v-if="!canApply" tone="info">
            You can prepare and review this plan, but only an administrator can apply it.
          </AppAlert>

          <div class="flex flex-wrap gap-2">
            <AppButton
              v-if="!applied && canApply"
              variant="primary"
              :loading="runner.busy.value"
              :disabled="!plan.changed"
              @click="queueOperation(false)"
            >
              Approve and apply
            </AppButton>
            <AppButton
              v-else-if="applied && canApply"
              variant="danger"
              icon="rotate-ccw"
              :loading="runner.busy.value"
              @click="confirmRollbackOpen = true"
            >
              Roll back this plan
            </AppButton>
          </div>
        </div>
      </AppCard>

      <AppCard v-else class="grid place-items-center">
        <EmptyState
          icon="zap"
          title="No plan prepared"
          description="Describe the desired state, then preview the exact change before approval."
        />
      </AppCard>
    </div>

    <AppConfirmDialog
      :open="canApply && confirmRollbackOpen"
      title="Roll back this plan"
      confirm-label="Roll back"
      :busy="runner.busy.value"
      @confirm="confirmRollback"
      @close="confirmRollbackOpen = false"
    >
      The probe file returns to the state captured before this plan was applied
      ({{ plan?.before.exists ? 'file present' : 'file absent' }}). A new job records the change.
    </AppConfirmDialog>
  </section>
</template>
