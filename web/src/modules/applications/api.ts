import { apiRequest } from '@/shared/api/request'

import type { Job, JobEvent } from '../jobs/api'
import { subscribeToJob } from '../jobs/api'

export type AppAction = 'package.install' | 'package.remove'

export interface Application {
  id: string
  app: string
  version?: string
  label: string
  summary: string
  category: string
  status: string
  installedVersion?: string
  managed: boolean
  manageHref?: string
  lastJobId?: number
  failure?: string
}

export interface ApplicationPlan {
  id: string
  applicationId: string
  operation: AppAction
  agentPlan: { packages: string[]; steps: string[]; warnings: string[]; expiresAt: string }
  expiresAt: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Application request')
}

export async function listApplications() {
  return (await request<{ items: Application[] }>('/api/v1/applications')).items
}

export function prepareChange(id: string, action: AppAction) {
  return request<{ application: Application; job: Job }>(`/api/v1/applications/${id}/plan`, {
    method: 'POST',
    body: JSON.stringify({ action }),
  })
}

export function getPlan(id: string) {
  return request<{ plan: ApplicationPlan }>(`/api/v1/applications/${id}/plan`)
}

export function applyPlan(id: string) {
  return request<Job>(`/api/v1/applications/${id}/apply`, { method: 'POST' })
}

export function watchApplicationJob(id: number, onEvent: (event: JobEvent) => void, onError?: () => void) {
  return subscribeToJob(id, onEvent, onError)
}
