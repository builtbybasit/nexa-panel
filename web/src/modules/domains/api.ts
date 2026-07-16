import { apiRequest } from '@/shared/api/request'

import type { Job, JobEvent } from '../jobs/api'
import { subscribeToJob } from '../jobs/api'

export type DomainKind = 'primary' | 'subdomain' | 'alias' | 'redirect'
export type DomainStatus = 'planning' | 'plan_ready' | 'activating' | 'active' | 'failed'

export interface Domain {
  id: string
  siteId: string
  hostname: string
  kind: DomainKind
  redirectTarget?: string
  status: DomainStatus
  resolvedAddresses: string[]
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface DomainPlan {
  domainId: string
  nodePlan: {
    id: string
    expiresAt: string
    signature: string
    site: {
      id: string
      slug: string
      primaryDomain: string
      routes?: Array<{ hostname: string; kind: string; redirectTarget?: string }>
    }
    artifacts: Array<{ kind: string; path: string; mode: number; content: string }>
    warnings: string[]
  }
  resolvedAddresses: string[]
  warnings: string[]
}

export interface CreateDomainRequest {
  siteId: string
  hostname: string
  kind: Exclude<DomainKind, 'primary'>
  redirectTarget?: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Domains request')
}

export async function listDomains(siteId?: string): Promise<Domain[]> {
  const query = siteId ? `?siteId=${encodeURIComponent(siteId)}` : ''
  return (await request<{ items: Domain[] }>(`/api/v1/domains${query}`)).items
}

export function createDomain(input: CreateDomainRequest): Promise<{ domain: Domain; job: Job }> {
  return request('/api/v1/domains', { method: 'POST', body: JSON.stringify(input) })
}

export function getDomainPlan(id: string): Promise<{ plan: DomainPlan; expiresAt: string }> {
  return request(`/api/v1/domains/${encodeURIComponent(id)}/plan`)
}

export function activateDomain(id: string): Promise<Job> {
  return request(`/api/v1/domains/${encodeURIComponent(id)}/activate`, { method: 'POST' })
}

export function watchDomainJob(id: number, onEvent: (event: JobEvent) => void, onError?: () => void) {
  return subscribeToJob(id, onEvent, onError)
}
