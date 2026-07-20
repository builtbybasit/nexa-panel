import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

type Status =
  | 'planning'
  | 'plan_ready'
  | 'applying'
  | 'active'
  | 'backing_up'
  | 'verified'
  | 'restoring'
  | 'failed'
  | 'online'

export type Access = 'connect' | 'read_only' | 'read_write'
export type ResourceType = 'accounts' | 'databases' | 'grants' | 'restore-points'

export interface Engine {
  id: string
  kind: 'mysql' | 'mariadb'
  version: string
  versionText: string
  socketPath: string
  port: number
  status: Status
  systemdUnit: string
}

export interface Account {
  id: string
  engineId: string
  name: string
  host: string
  status: Status
  credentialAvailable: boolean
  credentialVersion: number
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface Database {
  id: string
  engineId: string
  name: string
  ownerAccountId: string
  status: Status
  /**
   * Measured on read and absent until first measured; zero means an empty
   * database, not an unknown one — render it with `formatMeasuredBytes`.
   * `sizeObservedAt` says when the measurement was taken.
   */
  sizeBytes?: number
  sizeObservedAt?: string
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface Grant {
  id: string
  databaseId: string
  accountId: string
  access: Access
  status: Status
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface RestorePoint {
  id: string
  databaseId: string
  status: Status
  sha256?: string
  sizeBytes?: number
  verifiedAt?: string
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface Plan {
  id: string
  resourceType: string
  resourceId: string
  operation: string
  agentPlan: { steps: string[]; warnings: string[]; interruption: boolean; expiresAt: string }
  expiresAt: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'MySQL-family request')
}

export async function listEngines() {
  return (await request<{ items: Engine[] }>('/api/v1/mysql-family/engines')).items
}

export async function listAccounts() {
  return (await request<{ items: Account[] }>('/api/v1/mysql-family/accounts')).items
}

export async function listDatabases() {
  return (await request<{ items: Database[] }>('/api/v1/mysql-family/databases')).items
}

export async function listGrants() {
  return (await request<{ items: Grant[] }>('/api/v1/mysql-family/grants')).items
}

export async function listRestorePoints() {
  return (await request<{ items: RestorePoint[] }>('/api/v1/mysql-family/restore-points')).items
}

export function createAccount(input: { engineId: string; name: string; host?: string }) {
  return request<{ account: Account; job: Job }>('/api/v1/mysql-family/accounts', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function rotateAccount(id: string) {
  return request<{ account: Account; job: Job }>(`/api/v1/mysql-family/accounts/${encodeURIComponent(id)}/rotate`, {
    method: 'POST',
  })
}

export async function revealCredential(id: string) {
  return (
    await request<{ credential: string }>(`/api/v1/mysql-family/accounts/${encodeURIComponent(id)}/credential`, {
      method: 'POST',
    })
  ).credential
}

export function createDatabase(input: { engineId: string; name: string; ownerAccountId: string }) {
  return request<{ database: Database; job: Job }>('/api/v1/mysql-family/databases', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function createGrant(input: { databaseId: string; accountId: string; access: Access }) {
  return request<{ grant: Grant; job: Job }>('/api/v1/mysql-family/grants', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function createBackup(id: string) {
  return request<{ restorePoint: RestorePoint; job: Job }>(
    `/api/v1/mysql-family/databases/${encodeURIComponent(id)}/backups`,
    { method: 'POST' },
  )
}

export function prepareRestore(id: string) {
  return request<{ restorePoint: RestorePoint; job: Job }>(
    `/api/v1/mysql-family/restore-points/${encodeURIComponent(id)}/restore`,
    { method: 'POST' },
  )
}

export function getPlan(type: ResourceType, id: string) {
  return request<{ plan: Plan; expiresAt: string }>(`/api/v1/mysql-family/${type}/${encodeURIComponent(id)}/plan`)
}

export function applyPlan(type: ResourceType, id: string) {
  return request<Job>(`/api/v1/mysql-family/${type}/${encodeURIComponent(id)}/apply`, { method: 'POST' })
}
