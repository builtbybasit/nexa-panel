import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

function request<T>(path: string, init?: RequestInit, prefix = 'System request'): Promise<T> {
  return apiRequest<T>(path, init, prefix)
}

interface MemorySnapshot {
  supported: boolean
  totalBytes: number
  availableBytes: number
  swapTotalBytes: number
  swapFreeBytes: number
  usedPercent: number
  profile: 'unsupported' | 'compact' | 'standard' | 'pro'
}

interface PodmanStatus {
  available: boolean
  version?: string
  path?: string
}

export interface SystemOverview {
  observedAt: string
  memory?: MemorySnapshot
  podman: PodmanStatus
  warnings: string[]
}

export async function getSystemOverview(): Promise<SystemOverview> {
  const response = await fetch('/api/v1/system/overview', {
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`System overview request failed with status ${response.status}`)
  }
  return (await response.json()) as SystemOverview
}

interface SystemRelease {
  version: string
  tag: string
  notes?: string
  publishedAt?: string
}

export interface SystemUpdateAvailability {
  installedVersion: string
  latest?: SystemRelease
  updateAvailable: boolean
  checkedAt: string
}

/** Installed version and, when newer, the release this node could move to. */
export async function getSystemUpdates(): Promise<SystemUpdateAvailability> {
  return request<SystemUpdateAvailability>('/api/v1/system/updates', undefined, 'System update check')
}

/**
 * Queue a self-update. An empty version applies the latest release. This is a
 * sensitive action, so the request layer transparently prompts for MFA step-up
 * and retries when the server requires it.
 */
export async function applySystemUpdate(version?: string): Promise<{ job: Job }> {
  return request<{ job: Job }>('/api/v1/system/updates/apply', {
    method: 'POST',
    body: JSON.stringify({ version: version ?? '' }),
  })
}
