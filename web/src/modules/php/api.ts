import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

export type PhpExtensionAction = 'install' | 'remove'

export interface PhpExtension {
  name: string
  package: string
  installed: boolean
}

export interface PhpDirective {
  name: string
  module: string
  value: string
  access: string
  managed: boolean
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'PHP request')
}

export async function listPhpVersions(): Promise<string[]> {
  return (await request<{ items: string[] }>('/api/v1/php/versions')).items
}

export async function listPhpExtensions(version: string): Promise<PhpExtension[]> {
  return (await request<{ items: PhpExtension[] }>(`/api/v1/php/extensions?version=${encodeURIComponent(version)}`)).items
}

export async function listPhpSettings(version: string): Promise<PhpDirective[]> {
  return (await request<{ items: PhpDirective[] }>(`/api/v1/php/settings?version=${encodeURIComponent(version)}`)).items
}

export function changePhpExtension(version: string, extension: string, action: PhpExtensionAction) {
  return request<{ job: Job }>('/api/v1/php/extensions', {
    method: 'POST',
    body: JSON.stringify({ version, extension, action }),
  })
}

export function savePhpSettings(version: string, set: Record<string, string>, reset: string[]) {
  return request<{ job: Job }>('/api/v1/php/settings', {
    method: 'PUT',
    body: JSON.stringify({ version, set, reset }),
  })
}

export interface SitePhpSettings {
  items: PhpDirective[]
  version: string
}

export async function getSitePhpSettings(siteId: string): Promise<SitePhpSettings> {
  return request<SitePhpSettings>(`/api/v1/php/sites/${encodeURIComponent(siteId)}/settings`)
}

export function saveSitePhpSettings(siteId: string, set: Record<string, string>, reset: string[]) {
  return request<{ job: Job }>(`/api/v1/php/sites/${encodeURIComponent(siteId)}/settings`, {
    method: 'PUT',
    body: JSON.stringify({ set, reset }),
  })
}
