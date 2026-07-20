import type { Job } from '../jobs/api'
import { apiRequest } from '@/shared/api/request'

type TaskStatus = 'draft' | 'planning' | 'plan_ready' | 'activating' | 'active' | 'failed'

export interface ScheduledTask {
  id: string
  siteId: string
  name: string
  /** Five-field cron expression: minute hour day-of-month month day-of-week. */
  cronExpression: string
  command: string
  timeoutSeconds: number
  enabled: boolean
  status: TaskStatus
  /** True while a delete is in flight; the removal plan still needs an apply. */
  pendingRemoval: boolean
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface TaskInput {
  name: string
  cronExpression: string
  command: string
  timeoutSeconds: number
  enabled: boolean
}

interface TaskPlanArtifact {
  path: string
  /** Numeric file mode; render as octal. */
  mode: number
  content: string
  /** True when applying the plan removes this artifact (removal plans). */
  remove: boolean
}

export interface TaskPlan {
  id: string
  artifacts: TaskPlanArtifact[]
  warnings: string[]
  expiresAt: string
}

/** Result payload of a succeeded `schedule.run` job (fetched via the jobs API). */
export interface TaskRunResult {
  exitCode: number
  durationMs: number
  startedAt: string
  outputTail: string
  timedOut: boolean
}

/** One row of a task's run history. `exitCode: -1` marks a skipped overlapping run. */
export interface TaskRunRecord {
  startedAt: string
  durationSeconds: number
  exitCode: number
  trigger: string
}

class ScheduleRequestError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message)
  }
}

function base(siteId: string): string {
  return `/api/v1/sites/${encodeURIComponent(siteId)}/tasks`
}

function taskPath(siteId: string, taskId: string): string {
  return `${base(siteId)}/${encodeURIComponent(taskId)}`
}

function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  return apiRequest<T>(path, init, {
    errorPrefix: 'Schedules request',
    createError: (message, status, code) => new ScheduleRequestError(message, status, code),
  })
}

export async function listTasks(siteId: string): Promise<ScheduledTask[]> {
  return (await request<{ items: ScheduledTask[] }>(base(siteId))).items
}

/** Queue a create; the returned job plans the task's cron.d + wrapper artifacts. */
export function createTask(siteId: string, input: TaskInput): Promise<{ task: ScheduledTask; job: Job }> {
  return request(base(siteId), { method: 'POST', body: JSON.stringify(input) })
}

/** The plan (with rendered artifacts) is present only while the task is `plan_ready`. */
export async function getTask(siteId: string, taskId: string): Promise<{ task: ScheduledTask; plan?: TaskPlan }> {
  const response = await request<{ task: ScheduledTask; plan?: TaskPlan | null }>(taskPath(siteId, taskId))
  if (!response.plan) return { task: response.task }
  const plan = { ...response.plan, artifacts: response.plan.artifacts ?? [], warnings: response.plan.warnings ?? [] }
  return { task: response.task, plan }
}

/** Queue an update; the returned job replans the task's artifacts. */
export function updateTask(siteId: string, taskId: string, input: TaskInput): Promise<{ task: ScheduledTask; job: Job }> {
  return request(taskPath(siteId, taskId), { method: 'PUT', body: JSON.stringify(input) })
}

export function applyTask(siteId: string, taskId: string): Promise<{ job: Job }> {
  return request(`${taskPath(siteId, taskId)}/apply`, { method: 'POST' })
}

export function rollbackTask(siteId: string, taskId: string): Promise<{ job: Job }> {
  return request(`${taskPath(siteId, taskId)}/rollback`, { method: 'POST' })
}

/** Manual run; requires an active, enabled task. The job result is a `TaskRunResult`. */
export function runTask(siteId: string, taskId: string): Promise<{ job: Job }> {
  return request(`${taskPath(siteId, taskId)}/run`, { method: 'POST' })
}

export async function listTaskRuns(siteId: string, taskId: string): Promise<TaskRunRecord[]> {
  return (await request<{ items: TaskRunRecord[] }>(`${taskPath(siteId, taskId)}/runs`)).items
}

/** Queue a delete; the returned job prepares a REMOVAL plan the user must apply. */
export function deleteTask(siteId: string, taskId: string): Promise<{ task: ScheduledTask; job: Job }> {
  return request(taskPath(siteId, taskId), { method: 'DELETE' })
}
