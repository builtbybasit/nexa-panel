<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useCollection } from '@/shared/composables/useCollection'
import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppSelect,
  EmptyState,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  PlanReviewDialog,
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'

import { getJob, type Job } from '../../jobs/api'
import { listSites } from '../../sites/api'
import {
  applyTask,
  deleteTask,
  getTask,
  listTaskRuns,
  listTasks,
  rollbackTask,
  runTask,
  updateTask,
  type ScheduledTask,
  type TaskPlan,
  type TaskRunResult,
} from '../api'
import { describe as describeCron } from '../cron'
import TaskFormDialog from './TaskFormDialog.vue'

// --- Site selection (persisted in the ?site= route query; mutations need active sites) ---

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('schedules.write'))

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const activeSites = computed(() => (sitesQuery.data.value ?? []).filter((site) => site.status === 'active'))

const siteId = computed(() => (typeof route.query.site === 'string' ? route.query.site : ''))
const selectedSite = computed(() => activeSites.value.find((site) => site.id === siteId.value))

function selectSite(id: string) {
  const query = { ...route.query }
  if (id) query.site = id
  else delete query.site
  delete query.selected
  void router.replace({ query })
}

const siteSelection = computed<string>({
  get: () => siteId.value,
  set: (value) => selectSite(value),
})

watch(
  activeSites,
  (sites) => {
    const first = sites[0]
    if (!siteId.value && first) selectSite(first.id)
  },
  { immediate: true },
)

// --- Task inventory ---

const tasksQuery = useQuery({
  queryKey: ['tasks', siteId],
  queryFn: () => listTasks(siteId.value),
  enabled: computed(() => Boolean(selectedSite.value)),
  retry: false,
})
const tasks = computed(() => tasksQuery.data.value ?? [])
const tasksError = computed(() => (tasksQuery.error.value instanceof Error ? tasksQuery.error.value.message : ''))

const TASKS_PAGE_SIZE = 25

const collection = useCollection(() => tasks.value, {
  searchText: (task) => `${task.name} ${task.command}`,
  pageSize: TASKS_PAGE_SIZE,
})

const runner = useJobRunner()

// exactOptionalPropertyTypes: optional props are only bound while they hold a value.
const runnerJobLink = computed(() => (runner.jobId.value === undefined ? {} : { jobId: runner.jobId.value }))
const runnerElapsed = computed(() => (runner.startedAtMs.value === undefined ? {} : { startedAtMs: runner.startedAtMs.value }))

const inFlightStatuses = new Set(['planning', 'activating'])
const canMutate = (task: ScheduledTask) => canWrite.value && !task.pendingRemoval && !inFlightStatuses.has(task.status)
const canRun = (task: ScheduledTask) =>
  identity.can('operations.apply') && task.status === 'active' && task.enabled && !task.pendingRemoval
const canReview = (task: ScheduledTask) => task.status === 'plan_ready' || task.status === 'failed'

/** The expression as a plain sentence; unknown shapes fall back to the raw text. */
function scheduleSentence(task: ScheduledTask): string {
  try {
    return describeCron(task.cronExpression)
  } catch {
    return task.cronExpression
  }
}

// --- Create / edit (dialog queues the change, the runner follows the planning job) ---

const form = ref<{ task?: ScheduledTask }>()

function onTaskQueued(task: ScheduledTask, job: Job) {
  if (!canWrite.value) return
  form.value = undefined
  void tasksQuery.refetch()
  void runner.run(async () => job.id, {
    onSettled: async () => {
      await tasksQuery.refetch()
    },
    onSuccess: () => openPlan(task),
    failureMessage: 'Task planning failed.',
  })
}

// --- Plan review (also chained after create/update/delete planning jobs) ---

const planDialog = ref<{ task: ScheduledTask; plan: TaskPlan | undefined; loading: boolean; error: string }>()

async function openPlan(task: ScheduledTask) {
  runner.error.value = ''
  planDialog.value = { task, plan: undefined, loading: true, error: '' }
  try {
    const response = await getTask(siteId.value, task.id)
    planDialog.value = { task: response.task, plan: response.plan, loading: false, error: '' }
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : 'The task plan could not be loaded.'
    planDialog.value = { task, plan: undefined, loading: false, error: message }
  }
}

function closePlan() {
  if (runner.busy.value) return
  planDialog.value = undefined
  runner.error.value = ''
}

const planFailed = computed(() => planDialog.value?.task.status === 'failed')

const planFacts = computed<Fact[]>(() => {
  const task = planDialog.value?.task
  if (!task) return []
  return [
    { label: 'Task', value: task.name },
    { label: 'Schedule', value: scheduleSentence(task) },
    { label: 'Expression', value: task.cronExpression, mono: true },
    { label: 'Command', value: task.command, mono: true },
    { label: 'Timeout', value: `${task.timeoutSeconds}s` },
    { label: 'Enabled', value: task.enabled ? 'Yes' : 'No' },
  ]
})

const planWarnings = computed(() => {
  const dialog = planDialog.value
  if (!dialog || dialog.loading || dialog.error) return []
  const warnings = [...(dialog.plan?.warnings ?? [])]
  if (dialog.task.pendingRemoval) {
    warnings.unshift("Applying this plan removes the task's cron entry and wrapper script from the host, then deletes the task.")
  }
  return warnings
})

// Plans expire server-side; a loaded dialog without one gets an already-expired
// deadline so PlanReviewDialog offers Regenerate instead of Approve.
const planExpiry = computed(() => {
  const dialog = planDialog.value
  if (!dialog || dialog.loading) return {}
  return { expiresAt: dialog.plan?.expiresAt ?? new Date(0).toISOString() }
})

const planFailureLink = computed(() => {
  const id = planDialog.value?.task.lastJobId
  return id === undefined ? {} : { jobId: id }
})

function applyPlan() {
  if (!identity.can('operations.apply')) return
  const dialog = planDialog.value
  if (!dialog?.plan) return
  const task = dialog.task
  void runner.run(async () => (await applyTask(siteId.value, task.id)).job.id, {
    onSettled: async () => {
      await tasksQuery.refetch()
      const current = planDialog.value
      if (!current || current.task.id !== task.id) return
      const updated = tasks.value.find((item) => item.id === task.id)
      if (updated) planDialog.value = { ...current, task: updated }
      // A successfully applied removal deletes the row; the dialog closes below.
      else planDialog.value = undefined
    },
    onSuccess: () => {
      planDialog.value = undefined
    },
    successToast: task.pendingRemoval ? 'Task removed' : 'Plan applied',
    failureMessage: 'Applying the plan failed.',
  })
}

/** Queues a fresh plan for the reviewed task: a replan for edits, a new removal plan for deletes. */
function regeneratePlan() {
  if (!canWrite.value) return
  const dialog = planDialog.value
  if (!dialog) return
  const task = dialog.task
  void runner.run(
    async () => {
      const result = task.pendingRemoval
        ? await deleteTask(siteId.value, task.id)
        : await updateTask(siteId.value, task.id, {
            name: task.name,
            cronExpression: task.cronExpression,
            command: task.command,
            timeoutSeconds: task.timeoutSeconds,
            enabled: task.enabled,
          })
      return result.job.id
    },
    {
      onSettled: async () => {
        await tasksQuery.refetch()
      },
      onSuccess: () => openPlan(task),
      failureMessage: 'Replanning failed.',
    },
  )
}

function rollbackPlan() {
  if (!identity.can('operations.apply')) return
  const dialog = planDialog.value
  if (!dialog) return
  const task = dialog.task
  void runner.run(async () => (await rollbackTask(siteId.value, task.id)).job.id, {
    onSettled: async () => {
      await tasksQuery.refetch()
    },
    onSuccess: () => openPlan(task),
    successToast: 'Rollback complete',
    failureMessage: 'The rollback failed.',
  })
}

// --- Manual runs (job result carries the RunResult payload) ---

const runResult = ref<{ task: ScheduledTask; result: TaskRunResult }>()

function runNow(task: ScheduledTask) {
  if (!identity.can('operations.apply')) return
  runResult.value = undefined
  void runner.run(async () => (await runTask(siteId.value, task.id)).job.id, {
    onSuccess: async (event) => {
      const raw = ((await getJob(event.jobId)).result ?? {}) as Partial<TaskRunResult>
      runResult.value = {
        task,
        result: {
          exitCode: Number(raw.exitCode ?? 0),
          durationMs: Number(raw.durationMs ?? 0),
          startedAt: typeof raw.startedAt === 'string' ? raw.startedAt : '',
          outputTail: typeof raw.outputTail === 'string' ? raw.outputTail : '',
          timedOut: Boolean(raw.timedOut),
        },
      }
      if (expandedTaskId.value === task.id) await runsQuery.refetch()
    },
    failureMessage: 'The task run failed.',
  })
}

function formatDurationMs(milliseconds: number): string {
  if (milliseconds < 1000) return `${milliseconds} ms`
  const seconds = milliseconds / 1000
  if (seconds < 60) return `${seconds.toFixed(1)} s`
  return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`
}

function formatDurationSeconds(seconds: number): string {
  if (seconds < 60) return `${seconds} s`
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

const relativeFormat = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

function formatRelative(timestamp: string): string {
  const deltaMs = new Date(timestamp).getTime() - Date.now()
  const magnitude = Math.abs(deltaMs)
  if (magnitude < 60_000) return relativeFormat.format(Math.round(deltaMs / 1000), 'second')
  if (magnitude < 3_600_000) return relativeFormat.format(Math.round(deltaMs / 60_000), 'minute')
  if (magnitude < 86_400_000) return relativeFormat.format(Math.round(deltaMs / 3_600_000), 'hour')
  return relativeFormat.format(Math.round(deltaMs / 86_400_000), 'day')
}

// --- Run history (fetched on expand, one task at a time; ?selected= restores it) ---

const expandedTaskId = ref(typeof route.query.selected === 'string' ? route.query.selected : '')
let scrollToSelection = expandedTaskId.value !== ''

const runsQuery = useQuery({
  queryKey: ['task-runs', siteId, expandedTaskId],
  queryFn: () => listTaskRuns(siteId.value, expandedTaskId.value),
  enabled: computed(() => Boolean(selectedSite.value && expandedTaskId.value)),
  retry: false,
})
const runs = computed(() => runsQuery.data.value ?? [])
const runsError = computed(() => (runsQuery.error.value instanceof Error ? runsQuery.error.value.message : ''))

function toggleRuns(task: ScheduledTask) {
  expandedTaskId.value = expandedTaskId.value === task.id ? '' : task.id
  const query = { ...route.query }
  if (expandedTaskId.value) query.selected = expandedTaskId.value
  else delete query.selected
  void router.replace({ query })
}

watch(tasks, async (list) => {
  if (!scrollToSelection) return
  const index = list.findIndex((task) => task.id === expandedTaskId.value)
  if (index === -1) return
  scrollToSelection = false
  // Pagination could hide the restored row; jump to the page holding it.
  collection.page.value = Math.floor(index / TASKS_PAGE_SIZE) + 1
  await nextTick()
  document.getElementById(`task-row-${expandedTaskId.value}`)?.scrollIntoView({ block: 'center' })
})

// --- Delete: queue a removal plan, then chain into the plan review for Apply ---

const deleteTarget = ref<ScheduledTask>()
const deleteBusy = ref(false)
const deleteError = ref('')

function openDelete(task: ScheduledTask) {
  if (!canMutate(task)) return
  deleteTarget.value = task
  deleteError.value = ''
}

function closeDelete() {
  if (deleteBusy.value) return
  deleteTarget.value = undefined
}

async function confirmDelete() {
  const task = deleteTarget.value
  if (!task || !canWrite.value) return
  deleteBusy.value = true
  deleteError.value = ''
  try {
    const result = await deleteTask(siteId.value, task.id)
    deleteTarget.value = undefined
    await tasksQuery.refetch()
    void runner.run(async () => result.job.id, {
      onSettled: async () => {
        await tasksQuery.refetch()
      },
      onSuccess: () => openPlan(result.task),
      failureMessage: 'Removal planning failed.',
    })
  } catch (caught) {
    deleteError.value = caught instanceof Error ? caught.message : 'The delete could not be queued.'
  } finally {
    deleteBusy.value = false
  }
}

// --- Reset transient state when the site changes ---

watch(siteId, () => {
  expandedTaskId.value = ''
  runResult.value = undefined
  planDialog.value = undefined
  deleteTarget.value = undefined
  form.value = undefined
})

// ?create=1 opens the create dialog once an active site is selected. This
// watcher is registered after the siteId reset above on purpose: watchers run
// in creation order, so when the first site is auto-selected the reset clears
// `form` first and this one may then open the dialog.
watch(
  [selectedSite, () => route.query.create],
  ([site, create]) => {
    if (!site || create !== '1') return
    form.value = {}
    const query = { ...route.query }
    delete query.create
    void router.replace({ query })
  },
  { immediate: true },
)
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Scheduled tasks"
      description="Run commands on a schedule as each site's Unix user. Every change is planned first so you can review it before it reaches the host."
    >
      <AppButton v-if="canWrite" variant="primary" icon="plus" :disabled="!selectedSite" @click="form = {}">New task</AppButton>
    </PageHeader>

    <div v-if="sitesQuery.isPending.value" class="space-y-1">
      <SkeletonRow v-for="index in 3" :key="index" />
    </div>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">
      <p>The site list could not be loaded.</p>
      <AppButton size="sm" class="mt-2" @click="sitesQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!activeSites.length"
      icon="clock"
      title="No active sites"
      description="Scheduled tasks run on active sites."
    >
      <template #action>
        <RouterLink
          to="/sites"
          class="text-[13px] font-medium text-accent-300 underline-offset-2 transition-colors hover:text-accent-200 hover:underline"
        >
          Create a site first →
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else>
      <div class="flex flex-wrap items-center gap-3">
        <div class="w-full sm:w-72">
          <AppSelect v-model="siteSelection" aria-label="Site">
            <option v-for="site in activeSites" :key="site.id" :value="site.id">
              {{ site.displayName }} — {{ site.primaryDomain }}
            </option>
          </AppSelect>
        </div>
        <AppButton size="sm" icon="refresh-cw" :loading="tasksQuery.isFetching.value" :disabled="!selectedSite" @click="tasksQuery.refetch()">
          Refresh
        </AppButton>
      </div>

      <AppAlert v-if="siteId && sitesQuery.isSuccess.value && !selectedSite" tone="warning">
        The selected site is not active or not accessible. Choose another site.
      </AppAlert>

      <JobFailureNotice v-if="runner.error.value && !planDialog" :message="runner.error.value" v-bind="runnerJobLink" />
      <JobProgress
        v-if="runner.progress.value && !planDialog"
        :event="runner.progress.value"
        :messages="runner.messages.value"
        v-bind="runnerElapsed"
      />

      <!-- Manual run result -->
      <AppCard v-if="runResult" eyebrow="Manual run" :title="runResult.task.name" aria-live="polite">
        <template #actions>
          <StatusPill
            :tone="runResult.result.exitCode === 0 ? 'success' : 'danger'"
            :label="`exit ${runResult.result.exitCode}`"
            :pulse="false"
          />
        </template>
        <div class="space-y-3">
          <p class="text-[13px] text-ink-secondary">
            Duration {{ formatDurationMs(runResult.result.durationMs) }}
            <template v-if="runResult.result.startedAt"> · started {{ formatDateTime(runResult.result.startedAt) }}</template>
          </p>
          <AppAlert v-if="runResult.result.timedOut" tone="warning">The run hit its timeout and was stopped.</AppAlert>
          <pre
            v-if="runResult.result.outputTail"
            class="max-h-64 overflow-auto rounded-xl border border-outline bg-canvas/60 p-3 font-mono text-xs leading-5 whitespace-pre-wrap text-ink-secondary"
            >{{ runResult.result.outputTail }}</pre
          >
          <p v-else class="text-[13px] text-ink-muted">The run produced no output.</p>
          <AppButton size="sm" @click="runResult = undefined">Dismiss</AppButton>
        </div>
      </AppCard>

      <!-- Task table -->
      <AppCard
        v-if="selectedSite"
        eyebrow="Tasks"
        :title="`${tasks.length} scheduled ${tasks.length === 1 ? 'task' : 'tasks'}`"
        flush
      >
        <div class="space-y-3 px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="tasksQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="index in 3" :key="index" />
          </div>
          <AppAlert v-else-if="tasksError" tone="danger">
            <p>The task list could not be loaded. {{ tasksError }}</p>
            <AppButton size="sm" class="mt-2" @click="tasksQuery.refetch()">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="!tasks.length"
            icon="clock"
            title="No scheduled tasks"
            description="Tasks run commands on a schedule as the site's Unix user. Create the first one to review its cron plan."
          >
            <template #action>
              <AppButton v-if="canWrite" variant="primary" icon="plus" @click="form = {}">New task</AppButton>
            </template>
          </EmptyState>
          <template v-else>
            <ListToolbar
              v-model:search="collection.search.value"
              :count="collection.matching.value"
              count-label="tasks"
              placeholder="Search by name or command"
            />
            <EmptyState
              v-if="!collection.items.value.length"
              icon="search"
              title="No matching tasks"
              description="No tasks match your search. Clear it to see every task."
            />
            <div v-else class="overflow-x-auto">
              <table class="w-full border-collapse text-left">
                <thead>
                  <tr class="border-b border-outline">
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Name</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Schedule</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Command</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Timeout</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Enabled</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                    <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-outline">
                  <template v-for="task in collection.items.value" :key="task.id">
                    <tr
                      :id="`task-row-${task.id}`"
                      :class="expandedTaskId === task.id ? 'bg-accent-500/[0.05]' : ''"
                      :aria-current="expandedTaskId === task.id ? 'true' : undefined"
                    >
                      <td class="max-w-[14rem] px-3 py-2.5">
                        <span class="block truncate text-[13px] font-medium text-ink" :title="task.name">{{ task.name }}</span>
                      </td>
                      <td class="max-w-[14rem] px-3 py-2.5">
                        <span class="block cursor-help truncate text-[13px] text-ink-secondary" :title="task.cronExpression">
                          {{ scheduleSentence(task) }}
                        </span>
                      </td>
                      <td class="max-w-[16rem] px-3 py-2.5">
                        <span class="block truncate font-mono text-xs text-ink-secondary" :title="task.command">{{ task.command }}</span>
                      </td>
                      <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary">{{ task.timeoutSeconds }}s</td>
                      <td class="px-3 py-2.5">
                        <StatusPill :tone="task.enabled ? 'success' : 'neutral'" :label="task.enabled ? 'Enabled' : 'Disabled'" :pulse="false" />
                      </td>
                      <td class="px-3 py-2.5">
                        <span class="flex flex-wrap items-center gap-1.5">
                          <StatusPill :status="task.status" />
                          <StatusPill v-if="task.pendingRemoval" tone="warning" label="Removal pending" :pulse="false" />
                        </span>
                        <p
                          v-if="task.status === 'failed' && task.failure"
                          class="mt-1 max-w-[18rem] truncate text-xs text-rose-300"
                          :title="task.failure"
                        >
                          {{ task.failure }}
                        </p>
                      </td>
                      <td class="px-3 py-2.5 text-right">
                        <span class="flex items-center justify-end gap-1">
                          <AppButton
                            v-if="canReview(task)"
                            size="sm"
                            :variant="task.status === 'failed' ? 'danger' : 'primary'"
                            :disabled="runner.busy.value"
                            @click="openPlan(task)"
                          >
                            {{ task.status === 'failed' ? 'Review failure' : 'Review plan' }}
                          </AppButton>
                          <AppButton
                            v-if="canRun(task)"
                            size="sm"
                            variant="ghost"
                            icon="play"
                            :disabled="runner.busy.value"
                            :aria-label="`Run ${task.name} now`"
                            @click="runNow(task)"
                          />
                          <AppButton
                            size="sm"
                            variant="ghost"
                            icon="history"
                            :aria-label="`Run history for ${task.name}`"
                            :aria-expanded="expandedTaskId === task.id"
                            @click="toggleRuns(task)"
                          />
                          <AppButton
                            size="sm"
                            variant="ghost"
                            icon="pencil"
                            :disabled="!canMutate(task)"
                            :aria-label="`Edit ${task.name}`"
                            @click="form = { task }"
                          />
                          <AppButton
                            size="sm"
                            variant="ghost"
                            icon="trash"
                            class="text-rose-300"
                            :disabled="!canMutate(task)"
                            :aria-label="`Delete ${task.name}`"
                            @click="openDelete(task)"
                          />
                        </span>
                      </td>
                    </tr>
                    <tr v-if="expandedTaskId === task.id">
                      <td colspan="7" class="bg-canvas/30 px-4 py-3">
                        <div v-if="runsQuery.isPending.value" class="space-y-1">
                          <SkeletonRow v-for="index in 3" :key="index" />
                        </div>
                        <AppAlert v-else-if="runsError" tone="danger">
                          <p>The run history could not be loaded. {{ runsError }}</p>
                          <AppButton size="sm" class="mt-2" @click="runsQuery.refetch()">Retry</AppButton>
                        </AppAlert>
                        <p v-else-if="!runs.length" class="text-[13px] text-ink-muted">
                          No runs yet. The task runs on its schedule once active, or right away with the run button.
                        </p>
                        <table v-else class="w-full border-collapse text-left">
                          <thead>
                            <tr class="border-b border-outline">
                              <th class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Started</th>
                              <th class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Duration</th>
                              <th class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Exit code</th>
                              <th class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Trigger</th>
                            </tr>
                          </thead>
                          <tbody class="divide-y divide-outline">
                            <tr v-for="(run, index) in runs" :key="`${run.startedAt}-${index}`">
                              <td
                                class="cursor-help px-3 py-2 text-xs whitespace-nowrap text-ink-secondary"
                                :title="formatDateTime(run.startedAt)"
                              >
                                {{ formatRelative(run.startedAt) }}
                              </td>
                              <td class="px-3 py-2 font-mono text-xs whitespace-nowrap text-ink-secondary">
                                {{ run.exitCode === -1 ? '—' : formatDurationSeconds(run.durationSeconds) }}
                              </td>
                              <td class="px-3 py-2">
                                <StatusPill v-if="run.exitCode === -1" tone="neutral" label="skipped (overlap)" :pulse="false" />
                                <StatusPill v-else :tone="run.exitCode === 0 ? 'success' : 'danger'" :label="`exit ${run.exitCode}`" :pulse="false" />
                              </td>
                              <td class="px-3 py-2 text-xs text-ink-secondary">{{ run.trigger }}</td>
                            </tr>
                          </tbody>
                        </table>
                      </td>
                    </tr>
                  </template>
                </tbody>
              </table>
            </div>
            <div v-if="collection.pageCount.value > 1" class="flex items-center justify-end gap-2">
              <AppButton
                size="sm"
                variant="ghost"
                icon="chevron-left"
                :disabled="collection.page.value <= 1"
                aria-label="Previous page"
                @click="collection.page.value -= 1"
              />
              <span class="text-xs text-ink-muted tabular-nums">Page {{ collection.page.value }} of {{ collection.pageCount.value }}</span>
              <AppButton
                size="sm"
                variant="ghost"
                icon="chevron-right"
                :disabled="collection.page.value >= collection.pageCount.value"
                aria-label="Next page"
                @click="collection.page.value += 1"
              />
            </div>
          </template>
        </div>
      </AppCard>
    </template>

    <!-- Create / edit dialog -->
    <TaskFormDialog v-if="form && selectedSite && canWrite" :site-id="siteId" :task="form.task" @close="form = undefined" @queued="onTaskQueued" />

    <!-- Failure review: the apply failed on the host; offer the rollback -->
    <AppDialog v-if="planDialog && planFailed" :open="true" title="Review failure" @close="closePlan">
      <div class="space-y-4">
        <JobFailureNotice
          :message="planDialog.task.failure ?? 'The last apply failed on the host.'"
          v-bind="planFailureLink"
        />
        <p class="text-[13px] leading-relaxed text-ink-secondary">
          Rolling back removes the partially applied cron entry and wrapper script from
          <strong class="font-semibold text-ink">{{ planDialog.task.name }}</strong> so the host returns to a clean state.
        </p>
        <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="runnerJobLink" />
        <JobProgress
          v-if="runner.progress.value"
          :event="runner.progress.value"
          :messages="runner.messages.value"
          v-bind="runnerElapsed"
        />
      </div>
      <template #footer>
        <AppButton :disabled="runner.busy.value" @click="closePlan">Close</AppButton>
        <AppButton v-if="identity.can('operations.apply')" variant="danger" icon="rotate-ccw" :loading="runner.busy.value" @click="rollbackPlan">Roll back</AppButton>
      </template>
    </AppDialog>

    <!-- Plan review dialog -->
    <PlanReviewDialog
      v-else-if="planDialog"
      :open="true"
      :title="planDialog.task.pendingRemoval ? 'Review removal plan' : 'Review plan'"
      :facts="planFacts"
      :warnings="planWarnings"
      v-bind="planExpiry"
      :busy="runner.busy.value || planDialog.loading"
      :can-approve="identity.can('operations.apply')"
      :can-regenerate="canWrite"
      :approve-label="planDialog.task.pendingRemoval ? 'Apply removal' : 'Apply plan'"
      @approve="applyPlan"
      @regenerate="regeneratePlan"
      @close="closePlan"
    >
      <div class="space-y-4">
        <div v-if="planDialog.loading" class="space-y-1">
          <SkeletonRow v-for="index in 2" :key="index" />
        </div>
        <AppAlert v-else-if="planDialog.error" tone="danger">
          <p>{{ planDialog.error }}</p>
          <AppButton size="sm" class="mt-2" @click="openPlan(planDialog.task)">Try again</AppButton>
        </AppAlert>
        <template v-else-if="planDialog.plan">
          <div v-for="artifact in planDialog.plan.artifacts" :key="artifact.path" class="rounded-xl border border-outline bg-canvas/40">
            <div class="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5" :class="artifact.remove ? '' : 'border-b border-outline'">
              <code class="min-w-0 font-mono text-xs break-all text-accent-200">{{ artifact.path }}</code>
              <span class="flex shrink-0 items-center gap-2">
                <small class="text-[11px] text-ink-muted">Mode {{ artifact.mode.toString(8) }}</small>
                <StatusPill v-if="artifact.remove" tone="danger" label="Remove" :pulse="false" />
              </span>
            </div>
            <pre
              v-if="artifact.content && !artifact.remove"
              class="max-h-56 overflow-auto p-4 font-mono text-xs leading-5 whitespace-pre text-ink-secondary"
              >{{ artifact.content }}</pre
            >
          </div>
        </template>

        <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="runnerJobLink" />
        <JobProgress
          v-if="runner.progress.value"
          :event="runner.progress.value"
          :messages="runner.messages.value"
          v-bind="runnerElapsed"
        />
      </div>
    </PlanReviewDialog>

    <!-- Delete confirmation -->
    <AppConfirmDialog
      :open="canWrite && Boolean(deleteTarget)"
      title="Delete scheduled task"
      confirm-label="Prepare removal plan"
      tone="danger"
      :busy="deleteBusy"
      @confirm="confirmDelete"
      @close="closeDelete"
    >
      <p>
        Deleting <strong class="font-semibold text-ink">{{ deleteTarget?.name }}</strong> prepares a removal plan for its cron
        entry and wrapper script. Nothing is removed from the host until you review and apply that plan.
      </p>
      <AppAlert v-if="deleteError" tone="danger" class="mt-3">{{ deleteError }}</AppAlert>
    </AppConfirmDialog>
  </section>
</template>
