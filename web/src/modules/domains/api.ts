import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

export type DomainKind = 'primary' | 'subdomain' | 'alias' | 'redirect'
type DomainStatus = 'planning' | 'plan_ready' | 'activating' | 'active' | 'removing' | 'failed'

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

// Enqueues a `domain.remove` job (202). The 409 guards
// (domain_primary_protected, domain_busy) reject before a job is queued, so the
// caller's runner surfaces the server message as a queue-time failure.
export function deleteDomain(id: string): Promise<Job> {
  return request(`/api/v1/domains/${encodeURIComponent(id)}`, { method: 'DELETE' })
}
