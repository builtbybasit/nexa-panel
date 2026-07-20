import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

type DatabaseStatus =
  | 'planning'
  | 'plan_ready'
  | 'applying'
  | 'active'
  | 'backing_up'
  | 'verified'
  | 'restoring'
  | 'deleting'
  | 'failed'
  | 'online'
  | 'down'
export type AccessLevel = 'connect' | 'read_only' | 'read_write'
export type ResourceType = 'instances' | 'roles' | 'databases' | 'grants' | 'restore-points'

export interface PostgresInstance {
  id: string
  version: string
  cluster: string
  port: number
  status: DatabaseStatus
  owner: string
  dataPath: string
  socketPath: string
  logPath: string
  configPath: string
  systemdUnit: string
  managedByNexa: boolean
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface DatabaseRole {
  id: string
  instanceId: string
  name: string
  status: DatabaseStatus
  credentialAvailable: boolean
  credentialVersion: number
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface ManagedDatabase {
  id: string
  instanceId: string
  name: string
  ownerRoleId: string
  status: DatabaseStatus
  /**
   * Measured on read and absent until first measured; zero means an empty
   * database, not an unknown one. `sizeObservedAt` is the observation time.
   */
  sizeBytes?: number
  sizeObservedAt?: string
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface DatabaseGrant {
  id: string
  databaseId: string
  roleId: string
  access: AccessLevel
  status: DatabaseStatus
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface RestorePoint {
  id: string
  databaseId: string
  status: DatabaseStatus
  sha256?: string
  sizeBytes?: number
  verifiedAt?: string
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface PostgresPlan {
  id: string
  resourceType: string
  resourceId: string
  operation: string
  agentPlan: {
    id: string
    steps: string[]
    warnings: string[]
    interruption: boolean
    expiresAt: string
    change: Record<string, unknown>
  }
  createdAt: string
  expiresAt: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'PostgreSQL request')
}

async function list<T>(resource: string): Promise<T[]> {
  return (await request<{ items: T[] }>(`/api/v1/postgresql/${resource}`)).items
}

export function listInstances(): Promise<PostgresInstance[]> {
  return list<PostgresInstance>('instances')
}

export function listRoles(): Promise<DatabaseRole[]> {
  return list<DatabaseRole>('roles')
}

export function listDatabases(): Promise<ManagedDatabase[]> {
  return list<ManagedDatabase>('databases')
}

export function listGrants(): Promise<DatabaseGrant[]> {
  return list<DatabaseGrant>('grants')
}

export function listRestorePoints(): Promise<RestorePoint[]> {
  return list<RestorePoint>('restore-points')
}

export function createInstance(input: {
  version: '16' | '17' | '18'
  cluster: string
  port?: number
}): Promise<{ instance: PostgresInstance; job: Job }> {
  return request('/api/v1/postgresql/instances', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function createRole(
  instanceId: string,
  name: string,
): Promise<{ role: DatabaseRole; job: Job }> {
  return request('/api/v1/postgresql/roles', {
    method: 'POST',
    body: JSON.stringify({ instanceId, name }),
  })
}

export function rotateRole(id: string): Promise<{ role: DatabaseRole; job: Job }> {
  return request(`/api/v1/postgresql/roles/${encodeURIComponent(id)}/rotate`, { method: 'POST' })
}

export async function revealCredential(id: string): Promise<string> {
  return (
    await request<{ credential: string }>(
      `/api/v1/postgresql/roles/${encodeURIComponent(id)}/credential`,
      { method: 'POST' },
    )
  ).credential
}

export function createDatabase(input: {
  instanceId: string
  name: string
  ownerRoleId: string
}): Promise<{ database: ManagedDatabase; job: Job }> {
  return request('/api/v1/postgresql/databases', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function createGrant(input: {
  databaseId: string
  roleId: string
  access: AccessLevel
}): Promise<{ grant: DatabaseGrant; job: Job }> {
  return request('/api/v1/postgresql/grants', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function dropRole(id: string): Promise<{ job: Job }> {
  return request(`/api/v1/postgresql/roles/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function dropDatabase(id: string): Promise<{ job: Job }> {
  return request(`/api/v1/postgresql/databases/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function dropGrant(id: string): Promise<{ job: Job }> {
  return request(`/api/v1/postgresql/grants/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function createBackup(databaseId: string): Promise<{ restorePoint: RestorePoint; job: Job }> {
  return request(`/api/v1/postgresql/databases/${encodeURIComponent(databaseId)}/backups`, {
    method: 'POST',
  })
}

export function prepareRestore(
  restorePointId: string,
): Promise<{ restorePoint: RestorePoint; job: Job }> {
  return request(
    `/api/v1/postgresql/restore-points/${encodeURIComponent(restorePointId)}/restore`,
    { method: 'POST' },
  )
}

export function getPlan(
  resourceType: ResourceType,
  id: string,
): Promise<{ plan: PostgresPlan; expiresAt: string }> {
  return request(`/api/v1/postgresql/${resourceType}/${encodeURIComponent(id)}/plan`)
}

export function applyPlan(resourceType: ResourceType, id: string): Promise<Job> {
  return request(`/api/v1/postgresql/${resourceType}/${encodeURIComponent(id)}/apply`, {
    method: 'POST',
  })
}
