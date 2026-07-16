<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useJobRunner } from '@/shared/composables/useJobRunner'
import { formatDateTime } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, AppSelect, EmptyState, JobProgress, PageHeader, StatusPill } from '@/shared/ui'

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
  type ScheduledTask,
  type TaskPlan,
  type TaskRunResult,
} from '../api'
import TaskFormDialog from './TaskFormDialog.vue'

// --- Site selection (persisted in the ?site= route query; mutations need active sites) ---

const route = useRoute()
const router = useRouter()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const activeSites = computed(() => (sitesQuery.data.value ?? []).filter((site) => site.status === 'active'))

const siteId = computed(() => (typeof route.query.site === 'string' ? route.query.site : ''))
const selectedSite = computed(() => activeSites.value.find((site) => site.id === siteId.value))

function selectSite(id: string) {
  const query = { ...route.query }
  if (id) query.site = id
  else delete query.site
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

const runner = useJobRunner()

const inFlightStatuses = new Set(['planning', 'activating'])
const canMutate = (task: ScheduledTask) => !task.pendingRemoval && !inFlightStatuses.has(task.status)
const canRun = (task: ScheduledTask) => task.status === 'active' && task.enabled && !task.pendingRemoval
const canReview = (task: ScheduledTask) => task.status === 'plan_ready' || task.status === 'failed'

// --- Create / edit (dialog queues the 202, the runner follows the planning job) ---

const form = ref<{ task?: ScheduledTask }>()

function onTaskQueued(task: ScheduledTask, job: Job) {
  form.value = undefined
  void tasksQuery.refetch()
  void runner.run(async () => job.id, {
    onSettled: async () => {
      await tasksQuery.refetch()
    },
    onSuccess: () => openPlan(task),
    failureMessage: 'Task planning failed. Open Jobs for the durable failure record.',
  })
}

// --- Plan preview (also chained after create/update/delete planning jobs) ---

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

// Plans expire server-side (15 minutes); tick a clock for the countdown pill.
const now = ref(Date.now())
const ticker = window.setInterval(() => {
  now.value = Date.now()
}, 1000)
onBeforeUnmount(() => window.clearInterval(ticker))

const planExpiry = computed(() => {
  const plan = planDialog.value?.plan
  if (!plan) return undefined
  const remainingMs = new Date(plan.expiresAt).getTime() - now.value
  if (remainingMs <= 0) return { expired: true, label: 'Plan expired' }
  const totalSeconds = Math.floor(remainingMs / 1000)
  return { expired: false, label: `Expires in ${Math.floor(totalSeconds / 60)}:${String(totalSeconds % 60).padStart(2, '0')}` }
})

function applyPlan() {
  const dialog = planDialog.value
  if (!dialog) return
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
    failureMessage: 'The apply failed on the host. Review the failure below, then roll back.',
  })
}

function rollbackPlan() {
  const dialog = planDialog.value
  if (!dialog) return
  const task = dialog.task
  void runner.run(async () => (await rollbackTask(siteId.value, task.id)).job.id, {
    onSettled: async () => {
      await tasksQuery.refetch()
    },
    onSuccess: () => openPlan(task),
    failureMessage: 'The rollback failed. Inspect Jobs for the durable failure record.',
  })
}

// --- Manual runs (job result carries the RunResult payload) ---

const runResult = ref<{ task: ScheduledTask; result: TaskRunResult }>()

function runNow(task: ScheduledTask) {
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
    failureMessage: 'The task run failed. Inspect Jobs for the durable failure record.',
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

// --- Run history (fetched on expand, one task at a time) ---

const expandedTaskId = ref('')
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
}

// --- Delete: queue a removal plan, then chain into the plan preview for Apply ---

const deleteTarget = ref<ScheduledTask>()
const deleteBusy = ref(false)
const deleteError = ref('')

function openDelete(task: ScheduledTask) {
  deleteTarget.value = task
  deleteError.value = ''
}

async function confirmDelete() {
  const task = deleteTarget.value
  if (!task) return
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
      failureMessage: 'Removal planning failed. Open Jobs for the durable failure record.',
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
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Scheduled tasks"
      description="Cron tasks run as each site's Unix user through a managed wrapper. Every change is planned first and applied after review."
    >
      <AppButton variant="primary" icon="plus" :disabled="!selectedSite" @click="form = {}">New task</AppButton>
    </PageHeader>

    <AppAlert v-if="sitesQuery.isPending.value" tone="info">Loading sites…</AppAlert>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">The site list is unavailable.</AppAlert>
    <EmptyState
      v-else-if="!activeSites.length"
      icon="clock"
      title="No active sites"
      description="Scheduled tasks become available once a site reaches the active state."
    />

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

      <AppAlert v-if="runner.error.value && !planDialog" tone="danger">{{ runner.error.value }}</AppAlert>
      <JobProgress v-if="runner.progress.value && !planDialog" :event="runner.progress.value" />

      <!-- Manual run result -->
      <AppCard v-if="runResult" eyebrow="Manual run" :title="runResult.task.name">
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
          <AppAlert v-if="runResult.result.timedOut" tone="warning">The run hit its timeout and was terminated.</AppAlert>
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
        eyebrow="Desired state"
        :title="`${tasks.length} scheduled ${tasks.length === 1 ? 'task' : 'tasks'}`"
        flush
      >
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <AppAlert v-if="tasksQuery.isPending.value" tone="info" class="m-2">Loading tasks…</AppAlert>
          <AppAlert v-else-if="tasksError" tone="danger" class="m-2">{{ tasksError }}</AppAlert>
          <div v-else-if="tasks.length" class="overflow-x-auto">
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
                <template v-for="task in tasks" :key="task.id">
                  <tr>
                    <td class="max-w-[14rem] px-3 py-2.5">
                      <span class="block truncate text-[13px] font-medium text-ink" :title="task.name">{{ task.name }}</span>
                    </td>
                    <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-accent-200">{{ task.cronExpression }}</td>
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
                      <AppAlert v-if="runsQuery.isPending.value" tone="info">Loading run history…</AppAlert>
                      <AppAlert v-else-if="runsError" tone="danger">{{ runsError }}</AppAlert>
                      <p v-else-if="!runs.length" class="text-[13px] text-ink-muted">No runs recorded yet for this task.</p>
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
                            <td class="px-3 py-2 text-xs whitespace-nowrap text-ink-secondary">{{ formatDateTime(run.startedAt) }}</td>
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
          <EmptyState
            v-else
            icon="clock"
            title="No scheduled tasks"
            description="Create the first task to generate its reviewed cron plan."
            class="m-2"
          />
        </div>
      </AppCard>
    </template>

    <!-- Create / edit dialog -->
    <TaskFormDialog v-if="form && selectedSite" :site-id="siteId" :task="form.task" @close="form = undefined" @queued="onTaskQueued" />

    <!-- Plan preview dialog -->
    <div v-if="planDialog" class="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true">
      <div class="absolute inset-0 bg-canvas/70 backdrop-blur-sm" aria-hidden="true" @click="closePlan" />
      <div class="relative w-full max-w-3xl">
        <AppCard :eyebrow="planDialog.task.pendingRemoval ? 'Removal plan' : 'Configuration plan'" :title="planDialog.task.name">
          <template #actions>
            <StatusPill v-if="planExpiry" :tone="planExpiry.expired ? 'danger' : 'warning'" :label="planExpiry.label" :pulse="false" />
            <StatusPill :status="planDialog.task.status" />
          </template>
          <div class="max-h-[70vh] space-y-4 overflow-y-auto">
            <AppAlert v-if="planDialog.loading" tone="info">Loading the task plan…</AppAlert>
            <AppAlert v-else-if="planDialog.error" tone="danger">{{ planDialog.error }}</AppAlert>
            <template v-else>
              <AppAlert v-if="planDialog.task.pendingRemoval" tone="warning">
                Applying this plan removes the task's cron entry and wrapper script from the host, then deletes the task.
              </AppAlert>

              <template v-if="planDialog.plan">
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
                <AppAlert v-if="planDialog.plan.warnings.length" tone="warning">
                  <ul class="list-inside list-disc space-y-1">
                    <li v-for="warning in planDialog.plan.warnings" :key="warning">{{ warning }}</li>
                  </ul>
                </AppAlert>
              </template>
              <AppAlert v-else-if="planDialog.task.status !== 'failed'" tone="info">
                No unexpired plan is available for this task. Save the task again to generate a fresh one.
              </AppAlert>

              <AppAlert v-if="planDialog.task.status === 'failed'" tone="danger" title="The last apply failed">
                {{ planDialog.task.failure ?? 'The host reported a failure. Inspect Jobs for the durable record.' }}
              </AppAlert>

              <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
              <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />

              <div class="flex flex-wrap justify-end gap-2">
                <AppButton :disabled="runner.busy.value" @click="closePlan">Close</AppButton>
                <AppButton
                  v-if="planDialog.task.status === 'failed'"
                  variant="danger"
                  icon="rotate-ccw"
                  :loading="runner.busy.value"
                  @click="rollbackPlan"
                >
                  Roll back
                </AppButton>
                <AppButton
                  v-if="planDialog.task.status === 'plan_ready' && planDialog.plan"
                  variant="primary"
                  :loading="runner.busy.value"
                  :disabled="planExpiry?.expired === true"
                  @click="applyPlan"
                >
                  {{ planDialog.task.pendingRemoval ? 'Apply removal' : 'Apply plan' }}
                </AppButton>
              </div>
            </template>
          </div>
        </AppCard>
      </div>
    </div>

    <!-- Delete confirmation -->
    <div v-if="deleteTarget" class="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true">
      <div class="absolute inset-0 bg-canvas/70 backdrop-blur-sm" aria-hidden="true" @click="deleteBusy ? undefined : (deleteTarget = undefined)" />
      <div class="relative w-full max-w-md">
        <AppCard eyebrow="Removal" title="Delete scheduled task">
          <div class="space-y-4">
            <p class="text-[13px] leading-relaxed text-ink-secondary">
              Deleting <strong class="font-semibold text-ink">{{ deleteTarget.name }}</strong> prepares a removal plan for its cron
              entry and wrapper script. The task is only removed from the host after you review and apply that plan.
            </p>
            <AppAlert v-if="deleteError" tone="danger">{{ deleteError }}</AppAlert>
            <div class="flex justify-end gap-2">
              <AppButton :disabled="deleteBusy" @click="deleteTarget = undefined">Cancel</AppButton>
              <AppButton variant="danger" :loading="deleteBusy" @click="confirmDelete">Prepare removal plan</AppButton>
            </div>
          </div>
        </AppCard>
      </div>
    </div>
  </section>
</template>
