export type JobState = 'queued' | 'running' | 'succeeded' | 'failed'

export interface Job {
  id: number
  kind: string
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
  if (!response.ok) throw new Error(`Jobs request failed with status ${response.status}`)
  return (await response.json()) as T
}

export async function listJobs(): Promise<Job[]> {
  const response = await request<{ items: Job[] }>('/api/v1/jobs?limit=50')
  return response.items
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
