<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { AppAlert, AppButton, AppCard, JobFailureNotice, JobProgress, PageHeader, SkeletonCard, StatusPill } from '@/shared/ui'

import { applySystemUpdate, getSystemUpdates } from '../api'

const identity = useIdentityStore()
const canUpdate = computed(() => identity.can('system.update'))

const { data, isPending, isError, isFetching, refetch } = useQuery({
  queryKey: ['system', 'updates'],
  queryFn: getSystemUpdates,
  retry: false,
  refetchInterval: 60_000,
})

const installed = computed(() => data.value?.installedVersion ?? '—')
const latest = computed(() => data.value?.latest)
const updateAvailable = computed(() => data.value?.updateAvailable ?? false)

const runner = useJobRunner()

// The self-update job records success just before the agent bounces both units,
// so the live SSE stream almost always drops mid-restart. That is expected, not
// a failure: treat a disconnect during a running update as "restarting".
const restarting = computed(
  () => Boolean(runner.jobId.value) && !runner.busy.value && !runner.error.value && Boolean(runner.progress.value) && runner.progress.value?.state !== 'failed',
)

async function update() {
  if (!canUpdate.value || runner.busy.value) return
  const target = latest.value?.version
  await runner.run(async () => (await applySystemUpdate(target)).job.id, {
    successToast: `Nexa Panel is updating to ${target ?? 'the latest release'}`,
    failureMessage: 'The panel update could not be applied',
    onSettled: async () => {
      await refetch()
    },
  })
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader eyebrow="Panel maintenance" title="Updates">
      <AppButton icon="refresh-cw" :loading="isFetching" @click="refetch()">Check again</AppButton>
    </PageHeader>

    <div v-if="isPending" class="grid gap-4">
      <SkeletonCard />
    </div>

    <div v-else-if="isError" class="flex flex-col items-start gap-3">
      <AppAlert tone="danger" class="w-full">
        The update service is unavailable. Ensure the node agent is running and try again.
      </AppAlert>
      <AppButton icon="refresh-cw" :loading="isFetching" @click="refetch()">Retry</AppButton>
    </div>

    <template v-else-if="data">
      <AppCard
        eyebrow="Installed"
        title="Nexa Panel"
        description="The panel updates itself from its published GitHub releases: the node downloads the new binary, verifies its checksum, swaps it in atomically, and restarts."
      >
        <template #actions>
          <StatusPill
            :tone="updateAvailable ? 'warning' : 'success'"
            :label="updateAvailable ? 'Update available' : 'Up to date'"
            :description="updateAvailable ? `Version ${latest?.version} is ready to install` : 'You are running the latest release'"
          />
        </template>

        <dl class="grid gap-x-6 gap-y-3 text-[13px] sm:grid-cols-2">
          <div>
            <dt class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Current version</dt>
            <dd class="mt-1 font-mono text-accent-200">{{ installed }}</dd>
          </div>
          <div v-if="latest">
            <dt class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Latest release</dt>
            <dd class="mt-1 font-mono" :class="updateAvailable ? 'text-amber-300' : 'text-ink'">{{ latest.version }}</dd>
          </div>
        </dl>

        <div v-if="updateAvailable && latest?.notes" class="mt-4 rounded-lg border border-outline bg-white/[0.02] p-3">
          <p class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Release notes</p>
          <p class="mt-1.5 whitespace-pre-wrap text-[13px] text-ink-secondary">{{ latest.notes }}</p>
        </div>

        <div v-if="updateAvailable" class="mt-5 flex flex-wrap items-center gap-3">
          <AppButton
            v-if="canUpdate"
            icon="refresh-cw"
            variant="primary"
            :loading="runner.busy.value"
            :disabled="runner.busy.value || restarting"
            @click="update"
          >
            Install {{ latest?.version }}
          </AppButton>
          <p v-else class="text-[13px] text-ink-secondary">Only administrators can install panel updates.</p>
          <p v-if="canUpdate" class="text-xs text-ink-muted">
            The panel restarts automatically once the new binary is verified and installed.
          </p>
        </div>
      </AppCard>

      <AppAlert v-if="restarting" tone="info">
        The update was installed. The panel is restarting now — this page will reconnect in a few seconds.
      </AppAlert>

      <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
      <JobProgress
        v-if="runner.progress.value && !restarting"
        :event="runner.progress.value"
        :messages="runner.messages.value"
        :started-at-ms="runner.startedAtMs.value"
      />
    </template>
  </section>
</template>
