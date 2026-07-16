import type { Job, JobEvent } from '../jobs/api'
import { subscribeToJob } from '../jobs/api'

export interface ProbeChange {
  present: boolean
  content?: string
}

export interface Snapshot {
  exists: boolean
  digest?: string
  content?: string
}

export interface OperationPlan {
  id: string
  kind: 'nexa.probe.v1'
  target: string
  action: 'none' | 'create' | 'update' | 'remove'
  changed: boolean
  before: Snapshot
  desired: Snapshot
  plannedAt: string
  expiresAt: string
  signature: string
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  if (!response.ok) {
    const error = (await response.json().catch(() => null)) as { message?: string } | null
    throw new Error(error?.message ?? `Node operation failed with status ${response.status}`)
  }
  return (await response.json()) as T
}

export function observeProbe(): Promise<Snapshot> {
  return request('/api/v1/node/probe/observe')
}

export function planProbe(change: ProbeChange): Promise<OperationPlan> {
  return request('/api/v1/node/probe/plan', { method: 'POST', body: JSON.stringify(change) })
}

export function applyPlan(plan: OperationPlan): Promise<Job> {
  return request('/api/v1/node/probe/apply', { method: 'POST', body: JSON.stringify(plan) })
}

export function rollbackPlan(plan: OperationPlan): Promise<Job> {
  return request('/api/v1/node/probe/rollback', { method: 'POST', body: JSON.stringify(plan) })
}

export function watchOperation(jobId: number, onEvent: (event: JobEvent) => void, onError?: () => void): () => void {
  return subscribeToJob(jobId, onEvent, onError)
}
