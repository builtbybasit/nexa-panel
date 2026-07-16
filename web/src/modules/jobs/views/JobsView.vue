<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { formatDateTime, formatJobKind } from '@/shared/formatters'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { AppAlert, AppButton, AppCard, EmptyState, JobProgress, MetricCard, PageHeader, ProgressBar, StatusPill } from '@/shared/ui'

import { listJobs, submitDiagnostics, type Job } from '../api'

const jobsQuery = useQuery({ queryKey: ['jobs'], queryFn: listJobs, refetchInterval: 3_000 })
const jobs = computed(() => jobsQuery.data.value ?? [])
const activeJobs = computed(() => jobs.value.filter((job) => job.state === 'queued' || job.state === 'running').length)

const runner = useJobRunner()
const runningJob = ref<Job>()

async function runDiagnostics() {
  await runner.run(
    async () => {
      runningJob.value = await submitDiagnostics()
      await jobsQuery.refetch()
      return runningJob.value.id
    },
    {
      onSettled: async () => {
        await jobsQuery.refetch()
      },
      failureMessage: 'The diagnostic tracer failed. Its durable failure record is listed below.',
    },
  )
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Durable operations"
      title="Job history"
      description="Interrupted jobs resume after a control-plane restart; every run keeps a durable record."
    >
      <AppButton variant="primary" icon="play" :loading="runner.busy.value" @click="runDiagnostics">
        Run diagnostics
      </AppButton>
    </PageHeader>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      <MetricCard label="Recorded jobs" icon="history" :value="jobs.length" detail="Latest 50 operations" />
      <MetricCard label="Active" icon="activity" :value="activeJobs" detail="Queued or running now" />
      <MetricCard label="Worker mode" icon="zap" value="Durable" detail="Single worker with restart recovery" />
    </div>

    <AppCard v-if="runningJob && runner.progress.value" :eyebrow="`Live progress · Job ${runningJob.id}`" title="Diagnostic tracer">
      <template #actions>
        <StatusPill :status="runner.progress.value.state" />
      </template>
      <JobProgress :event="runner.progress.value" />
    </AppCard>

    <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
    <AppAlert v-if="jobsQuery.isPending.value" tone="info">Loading durable job history…</AppAlert>
    <AppAlert v-else-if="jobsQuery.isError.value" tone="danger">Job history is unavailable.</AppAlert>

    <AppCard v-else flush>
      <div v-if="jobs.length" class="divide-y divide-outline px-4 py-1 sm:px-5">
        <div v-for="job in jobs" :key="job.id" class="flex items-center gap-4 py-3">
          <span class="w-10 shrink-0 font-mono text-xs text-ink-muted">#{{ job.id }}</span>
          <div class="min-w-0 flex-1">
            <strong class="block truncate text-[13px] font-semibold text-ink">{{ formatJobKind(job.kind) }}</strong>
            <small class="block text-xs text-ink-muted">{{ formatDateTime(job.createdAt) }}</small>
          </div>
          <div class="hidden w-36 items-center gap-2 sm:flex">
            <ProgressBar :value="job.progress" class="flex-1" :tone="job.state === 'failed' ? 'danger' : 'accent'" />
            <small class="w-9 text-right font-mono text-[11px] text-ink-muted">{{ job.progress }}%</small>
          </div>
          <StatusPill :status="job.state" />
        </div>
      </div>
      <EmptyState
        v-else
        icon="history"
        title="No operations yet"
        description="Run the safe diagnostic tracer to verify durable execution and live progress."
        class="m-5"
      />
    </AppCard>
  </section>
</template>
