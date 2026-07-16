<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { formatTime } from '@/shared/formatters'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppTextarea,
  EmptyState,
  FactList,
  FormField,
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

const runner = useJobRunner()

const observationQuery = useQuery({ queryKey: ['node', 'probe'], queryFn: observeProbe, retry: false })
const current = computed(() => observationQuery.data.value)

function shortDigest(value?: string): string {
  return value ? `${value.slice(0, 12)}…` : 'Not present'
}

const planFacts = computed(() =>
  plan.value
    ? [
        { label: 'Fixed target', value: plan.value.target, mono: true },
        { label: 'Before', value: shortDigest(plan.value.before.digest) },
        { label: 'Desired', value: shortDigest(plan.value.desired.digest) },
        { label: 'Expires', value: formatTime(plan.value.expiresAt) },
      ]
    : [],
)

async function createPlan() {
  planning.value = true
  runner.error.value = ''
  runner.progress.value = undefined
  applied.value = false
  try {
    plan.value = await planProbe(desiredPresent.value ? { present: true, content: desiredContent.value } : { present: false })
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The operation could not be planned.'
  } finally {
    planning.value = false
  }
}

async function queueOperation(rollback: boolean) {
  const currentPlan = plan.value
  if (!currentPlan) return
  await runner.run(async () => (rollback ? await rollbackPlan(currentPlan) : await applyPlan(currentPlan)).id, {
    onSettled: async (event) => {
      applied.value = event.state === 'succeeded' && !rollback
      if (rollback && event.state === 'succeeded') plan.value = undefined
      await observationQuery.refetch()
    },
    failureMessage: 'The node operation failed. Check the Jobs page for the durable failure record.',
  })
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

    <div class="grid gap-4 lg:grid-cols-2">
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
        </div>
      </AppCard>

      <AppCard v-if="plan" :eyebrow="`Plan ${plan.id.slice(0, 8)}`" :title="plan.action === 'none' ? 'No changes required' : `${plan.action} managed probe`" class="capitalize-first">
        <template #actions>
          <StatusPill :tone="plan.changed ? 'warning' : 'success'" :label="plan.changed ? 'Review required' : 'In sync'" />
        </template>

        <div class="space-y-4">
          <FactList :facts="planFacts" />
          <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />
          <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>

          <div class="flex flex-wrap gap-2">
            <AppButton
              v-if="!applied"
              variant="primary"
              :loading="runner.busy.value"
              :disabled="!plan.changed"
              @click="queueOperation(false)"
            >
              Approve and apply
            </AppButton>
            <AppButton v-else variant="danger" icon="rotate-ccw" :loading="runner.busy.value" @click="queueOperation(true)">
              Rollback this plan
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
  </section>
</template>
