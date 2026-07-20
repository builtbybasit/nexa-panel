<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { formatDateTime, formatJobKind } from '@/shared/formatters'
import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppIcon,
  AppSelect,
  EmptyState,
  FactList,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  MetricCard,
  PageHeader,
  ProgressBar,
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'

import { getJob, listJobs, submitDiagnostics, type Job, type JobState } from '../api'

const route = useRoute()
const identity = useIdentityStore()

const jobsQuery = useQuery({ queryKey: ['jobs'], queryFn: listJobs, refetchInterval: 3_000 })
const jobs = computed(() => jobsQuery.data.value ?? [])
const activeJobs = computed(() => jobs.value.filter((job) => job.state === 'queued' || job.state === 'running').length)

// --- Filters + search over the fetched page (latest 50) ---

const stateFilter = ref<'' | JobState>('')
const kindFilter = ref('')
const failedOnly = ref(false)

const kinds = computed(() => [...new Set(jobs.value.map((job) => job.kind))].sort())

const filteredJobs = computed(() =>
  jobs.value.filter(
    (job) =>
      (!stateFilter.value || job.state === stateFilter.value) &&
      (!kindFilter.value || job.kind === kindFilter.value) &&
      (!failedOnly.value || job.state === 'failed'),
  ),
)

const collection = useCollection(() => filteredJobs.value, {
  searchText: (job) => `#${job.id} ${job.kind} ${job.title ?? ''} ${formatJobKind(job.kind)} ${job.failure ?? ''}`,
  pageSize: 50,
})

// --- Expandable rows ---

const expandedIds = ref(new Set<number>())
const expandedResults = ref(new Set<number>())

function toggleExpanded(id: number) {
  const next = new Set(expandedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedIds.value = next
}

function toggleResult(id: number) {
  const next = new Set(expandedResults.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedResults.value = next
}

function formatDuration(job: Job): string {
  if (!job.startedAt) return 'Not started'
  if (!job.completedAt) return 'Still running'
  const ms = new Date(job.completedAt).getTime() - new Date(job.startedAt).getTime()
  if (ms < 1_000) return `${ms} ms`
  const totalSeconds = Math.round(ms / 1_000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`
}

function jobFacts(job: Job): Fact[] {
  const facts: Fact[] = [
    { label: 'Duration', value: formatDuration(job) },
    { label: 'Started', value: job.startedAt ? formatDateTime(job.startedAt) : '—' },
    { label: 'Completed', value: job.completedAt ? formatDateTime(job.completedAt) : '—' },
  ]
  for (const [key, value] of Object.entries(job.request)) {
    if (typeof value === 'string') facts.push({ label: key, value, mono: true })
  }
  return facts
}

const RESULT_PREVIEW_LIMIT = 400

function resultText(job: Job): string {
  return job.result && Object.keys(job.result).length ? JSON.stringify(job.result, null, 2) : ''
}

function resultPreview(job: Job): string {
  const text = resultText(job)
  if (text.length <= RESULT_PREVIEW_LIMIT || expandedResults.value.has(job.id)) return text
  return `${text.slice(0, RESULT_PREVIEW_LIMIT)}…`
}

// --- /jobs?job=<id> deep link: expand + highlight + scroll; pin when older than the page ---

const highlightedId = ref<number>()
const pinnedJob = ref<Job>()
const pinnedError = ref('')
let handledDeepLinkId: number | undefined

const jobParam = computed(() => {
  const raw = route.query.job
  const id = typeof raw === 'string' ? Number(raw) : Number.NaN
  return Number.isInteger(id) && id > 0 ? id : undefined
})

async function scrollToJob(id: number) {
  await nextTick()
  document.getElementById(`job-row-${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' })
}

watch(
  [jobParam, () => jobsQuery.isSuccess.value],
  async ([id, loaded]) => {
    if (!id || !loaded || handledDeepLinkId === id) return
    handledDeepLinkId = id
    pinnedJob.value = undefined
    pinnedError.value = ''
    highlightedId.value = id
    expandedIds.value = new Set([...expandedIds.value, id])
    if (jobs.value.some((job) => job.id === id)) {
      void scrollToJob(id)
      return
    }
    try {
      pinnedJob.value = await getJob(id)
      void scrollToJob(id)
    } catch {
      pinnedError.value = `Job #${id} was not found.`
    }
  },
  { immediate: true },
)

/**
 * Rendered rows: the deep-linked job (when older than the page) pinned first.
 * Once a refetch brings the job into the list the live entry wins, so the same
 * job is never rendered twice under one id.
 */
const rows = computed(() => {
  const listed = collection.items.value.map((job) => ({ job, pinned: false }))
  const pinned = pinnedJob.value
  if (!pinned || jobs.value.some((job) => job.id === pinned.id)) return listed
  return [{ job: pinned, pinned: true }, ...listed]
})

// --- Diagnostics tracer ---

const runner = useJobRunner()

// Spread-bound so the optional props are omitted (not passed as undefined),
// which exactOptionalPropertyTypes requires.
const runnerJobLink = computed(() => (runner.jobId.value === undefined ? {} : { jobId: runner.jobId.value }))
const runnerTiming = computed(() => (runner.startedAtMs.value === undefined ? {} : { startedAtMs: runner.startedAtMs.value }))

async function runDiagnostics() {
	if (!identity.can('operations.apply')) return
	await runner.run(
    async () => {
      const job = await submitDiagnostics()
      await jobsQuery.refetch()
      return job.id
    },
    {
      onSettled: async () => {
        await jobsQuery.refetch()
      },
      failureMessage: 'Diagnostics failed',
      successToast: 'Diagnostics finished',
    },
  )
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Operations"
      title="Job history"
      description="Every operation runs as a recorded job with live progress and an explicit restart-recovery policy."
    >
      <AppButton v-if="identity.can('operations.apply')" variant="primary" icon="play" :loading="runner.busy.value" @click="runDiagnostics">
        Run diagnostics
      </AppButton>
    </PageHeader>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      <MetricCard label="Recorded jobs" icon="history" :value="jobs.length" detail="Latest 50 operations" />
      <MetricCard label="Active" icon="activity" :value="activeJobs" detail="Queued or running now" />
      <MetricCard label="Worker mode" icon="zap" value="Durable" detail="Single worker with restart recovery" />
    </div>

    <AppCard
      v-if="runner.progress.value"
      :eyebrow="runner.jobId.value !== undefined ? `Live progress · Job #${runner.jobId.value}` : 'Live progress'"
      title="Diagnostics"
    >
      <template #actions>
        <StatusPill :status="runner.progress.value.state" />
      </template>
      <JobProgress :event="runner.progress.value" :messages="runner.messages.value" v-bind="runnerTiming" />
    </AppCard>

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="runnerJobLink" />
    <AppAlert v-if="pinnedError" tone="danger">{{ pinnedError }}</AppAlert>

    <AppCard v-if="jobsQuery.isPending.value" flush>
      <div class="divide-y divide-outline px-4 py-1 sm:px-5">
        <SkeletonRow v-for="n in 3" :key="n" />
      </div>
    </AppCard>

    <AppAlert v-else-if="jobsQuery.isError.value" tone="danger">
      <p>Job history couldn't be loaded.</p>
      <AppButton size="sm" class="mt-2" @click="jobsQuery.refetch()">Retry</AppButton>
    </AppAlert>

    <template v-else>
      <div class="space-y-2">
        <ListToolbar
          :search="collection.search.value"
          :count="collection.matching.value"
          count-label="jobs"
          placeholder="Search jobs"
          @update:search="collection.search.value = $event"
        >
          <template #filters>
            <div class="w-36">
              <AppSelect v-model="stateFilter" aria-label="State">
                <option value="">All states</option>
                <option value="queued">Queued</option>
                <option value="running">Running</option>
                <option value="succeeded">Succeeded</option>
                <option value="failed">Failed</option>
              </AppSelect>
            </div>
            <div class="w-48">
              <AppSelect v-model="kindFilter" aria-label="Kind">
                <option value="">All kinds</option>
                <option v-for="kind in kinds" :key="kind" :value="kind">{{ formatJobKind(kind) }}</option>
              </AppSelect>
            </div>
            <AppButton
              size="sm"
              :variant="failedOnly ? 'danger' : 'ghost'"
              :aria-pressed="failedOnly"
              @click="failedOnly = !failedOnly"
            >
              Failed only
            </AppButton>
          </template>
        </ListToolbar>
        <p class="text-xs text-ink-muted">Showing the latest 50 operations.</p>
      </div>

      <AppCard flush>
        <div v-if="rows.length" class="divide-y divide-outline px-4 py-1 sm:px-5">
          <div
            v-for="row in rows"
            :id="`job-row-${row.job.id}`"
            :key="row.job.id"
            class="py-3"
            :class="row.job.id === highlightedId ? '-mx-2 my-1 rounded-xl bg-accent-400/[0.06] px-2 ring-1 ring-accent-400/30' : ''"
          >
            <div class="flex items-center gap-3">
              <button
                type="button"
                class="grid size-7 shrink-0 place-items-center rounded-lg text-ink-muted transition-colors hover:bg-white/[0.05] hover:text-ink"
                :aria-expanded="expandedIds.has(row.job.id)"
                :aria-controls="`job-details-${row.job.id}`"
                :aria-label="`Show details for job #${row.job.id}`"
                @click="toggleExpanded(row.job.id)"
              >
                <AppIcon :name="expandedIds.has(row.job.id) ? 'chevron-up' : 'chevron-down'" :size="15" />
              </button>
              <span class="w-10 shrink-0 font-mono text-xs text-ink-muted">#{{ row.job.id }}</span>
              <div class="min-w-0 flex-1">
                <strong class="block truncate text-[13px] font-semibold text-ink">
                  {{ row.job.title || formatJobKind(row.job.kind) }}
                </strong>
                <small class="block truncate text-xs text-ink-muted">
                  <template v-if="row.job.title">{{ formatJobKind(row.job.kind) }} · </template>{{ formatDateTime(row.job.createdAt) }}
                </small>
              </div>
              <div class="hidden w-36 items-center gap-2 sm:flex">
                <ProgressBar :value="row.job.progress" class="flex-1" :tone="row.job.state === 'failed' ? 'danger' : 'accent'" />
                <small class="w-9 text-right font-mono text-[11px] text-ink-muted">{{ row.job.progress }}%</small>
              </div>
              <StatusPill v-if="row.pinned" tone="accent" label="Pinned" description="Linked job, older than the latest 50" :pulse="false" />
              <StatusPill :status="row.job.state" />
            </div>

            <div v-if="expandedIds.has(row.job.id)" :id="`job-details-${row.job.id}`" class="mt-3 space-y-3 pl-10">
              <FactList :facts="jobFacts(row.job)" />
              <div v-if="resultText(row.job)">
                <p class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Result</p>
                <pre class="mt-1 overflow-x-auto rounded-lg border border-outline bg-canvas/60 p-3 font-mono text-xs leading-5 whitespace-pre-wrap break-all text-ink-secondary">{{ resultPreview(row.job) }}</pre>
                <button
                  v-if="resultText(row.job).length > RESULT_PREVIEW_LIMIT"
                  type="button"
                  class="mt-1 text-xs font-medium text-accent-300 transition-colors hover:text-accent-200"
                  @click="toggleResult(row.job.id)"
                >
                  {{ expandedResults.has(row.job.id) ? 'Show less' : 'Show full result' }}
                </button>
              </div>
              <AppAlert v-if="row.job.failure" tone="danger">{{ row.job.failure }}</AppAlert>
            </div>
          </div>
        </div>
        <EmptyState
          v-else-if="jobs.length"
          icon="filter"
          title="No jobs match"
          description="Clear the search or filters to see the rest of the page."
          class="m-5"
        />
        <EmptyState
          v-else
          icon="history"
          title="No operations yet"
          description="Run the safe diagnostics job to verify durable execution and live progress."
          class="m-5"
        />
      </AppCard>
    </template>
  </section>
</template>
