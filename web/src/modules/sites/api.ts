import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

export type SiteStatus = 'draft' | 'planning' | 'plan_ready' | 'activating' | 'active' | 'rolling_back' | 'rolled_back' | 'failed'

export interface Site {
  id: string
  slug: string
  displayName: string
  primaryDomain: string
  phpVersion: string
  unixUser: string
  rootPath: string
  socketPath: string
  status: SiteStatus
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface Runtime {
  engine: 'php'
  version: string
  installed: boolean
  enabled: boolean
  supportStatus: string
}

export interface CreateSiteRequest {
  slug: string
  displayName: string
  primaryDomain: string
  phpVersion: string
}

interface SiteArtifact {
  kind: 'site-root' | 'php-fpm-pool' | 'nginx-site'
  path: string
  mode: number
  content: string
}

export interface SitePlan {
  site: Site
  artifacts: SiteArtifact[]
  warnings: string[]
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Sites request')
}

export async function listSites(): Promise<Site[]> {
  return (await request<{ items: Site[] }>('/api/v1/sites')).items
}

export async function listRuntimes(): Promise<Runtime[]> {
  return (await request<{ items: Runtime[] }>('/api/v1/runtimes')).items
}

export function createSite(input: CreateSiteRequest): Promise<{ site: Site; job: Job }> {
  return request('/api/v1/sites', { method: 'POST', body: JSON.stringify(input) })
}

export function getSitePlan(siteId: string): Promise<{ plan: SitePlan; expiresAt: string }> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/plan`)
}

export function activateSite(siteId: string): Promise<Job> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/activate`, { method: 'POST' })
}

export function rollbackSite(siteId: string): Promise<Job> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/rollback`, { method: 'POST' })
}
export function prepareSitePlan(siteId: string): Promise<Job> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/plan`, { method: 'POST' })
}
