export type JobState = 'queued' | 'running' | 'succeeded' | 'failed'

export interface Job {
  id: number
  kind: string
  /** Human-readable summary set at submit time (e.g. "Install nginx"); may be empty. */
  title?: string
  state: JobState
  progress: number
  actorUserId?: string
  request: Record<string, unknown>
  result?: Record<string, unknown>
  failure?: string
  createdAt: string
  updatedAt: string
  startedAt?: string
  completedAt?: string
}

export interface JobEvent {
  sequence: number
  jobId: number
  state: JobState
  progress: number
  message: string
  occurredAt: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Jobs request')
}

export async function listJobs(): Promise<Job[]> {
  const response = await request<{ items: Job[] }>('/api/v1/jobs?limit=50')
  return response.items
}

export function getJob(id: number): Promise<Job> {
  return request(`/api/v1/jobs/${id}`)
}

export function submitDiagnostics(delayMilliseconds = 150): Promise<Job> {
  return request('/api/v1/jobs/diagnostics', {
    method: 'POST',
    body: JSON.stringify({ delayMilliseconds }),
  })
}

export function subscribeToJob(jobId: number, onEvent: (event: JobEvent) => void, onError?: () => void): () => void {
  const source = new EventSource(`/api/v1/jobs/${jobId}/events`)
  source.addEventListener('progress', (rawEvent) => {
    const event = rawEvent as MessageEvent<string>
    onEvent(JSON.parse(event.data) as JobEvent)
  })
  source.onerror = () => {
    source.close()
    onError?.()
  }
  return () => source.close()
}
import { apiRequest } from '@/shared/api/request'
