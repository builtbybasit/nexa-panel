import type { Job, JobEvent } from '../jobs/api'
import { subscribeToJob } from '../jobs/api'

export type DatabaseStatus = 'planning' | 'plan_ready' | 'applying' | 'active' | 'backing_up' | 'verified' | 'restoring' | 'failed' | 'online' | 'down'
export type AccessLevel = 'connect' | 'read_only' | 'read_write'
export type ResourceType = 'instances' | 'roles' | 'databases' | 'grants' | 'restore-points'

export interface PostgresInstance { id: string; version: string; cluster: string; port: number; status: DatabaseStatus; owner: string; dataPath: string; socketPath: string; logPath: string; configPath: string; systemdUnit: string; managedByNexa: boolean; lastJobId?: number; failure?: string; createdAt: string; updatedAt: string }
export interface DatabaseRole { id: string; instanceId: string; name: string; status: DatabaseStatus; credentialAvailable: boolean; credentialVersion: number; lastJobId?: number; failure?: string; createdAt: string; updatedAt: string }
export interface ManagedDatabase { id: string; instanceId: string; name: string; ownerRoleId: string; status: DatabaseStatus; lastJobId?: number; failure?: string; createdAt: string; updatedAt: string }
export interface DatabaseGrant { id: string; databaseId: string; roleId: string; access: AccessLevel; status: DatabaseStatus; lastJobId?: number; failure?: string; createdAt: string; updatedAt: string }
export interface RestorePoint { id: string; databaseId: string; status: DatabaseStatus; sha256?: string; sizeBytes?: number; verifiedAt?: string; lastJobId?: number; failure?: string; createdAt: string; updatedAt: string }
export interface PostgresPlan { id: string; resourceType: string; resourceId: string; operation: string; agentPlan: { id: string; steps: string[]; warnings: string[]; interruption: boolean; expiresAt: string; change: Record<string, unknown> }; createdAt: string; expiresAt: string }

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { ...init, credentials: 'same-origin', headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...init?.headers } })
  if (!response.ok) { const payload = await response.json().catch(() => null) as { message?: string } | null; throw new Error(payload?.message ?? `PostgreSQL request failed with status ${response.status}`) }
  return await response.json() as T
}

export async function listInstances(): Promise<PostgresInstance[]> { return (await request<{ items: PostgresInstance[] }>('/api/v1/postgresql/instances')).items }
export async function listRoles(): Promise<DatabaseRole[]> { return (await request<{ items: DatabaseRole[] }>('/api/v1/postgresql/roles')).items }
export async function listDatabases(): Promise<ManagedDatabase[]> { return (await request<{ items: ManagedDatabase[] }>('/api/v1/postgresql/databases')).items }
export async function listGrants(): Promise<DatabaseGrant[]> { return (await request<{ items: DatabaseGrant[] }>('/api/v1/postgresql/grants')).items }
export async function listRestorePoints(): Promise<RestorePoint[]> { return (await request<{ items: RestorePoint[] }>('/api/v1/postgresql/restore-points')).items }
export function createInstance(input: { version: '16' | '17' | '18'; cluster: string; port?: number }): Promise<{ instance: PostgresInstance; job: Job }> { return request('/api/v1/postgresql/instances', { method: 'POST', body: JSON.stringify(input) }) }
export function createRole(instanceId: string, name: string): Promise<{ role: DatabaseRole; job: Job }> { return request('/api/v1/postgresql/roles', { method: 'POST', body: JSON.stringify({ instanceId, name }) }) }
export function rotateRole(id: string): Promise<{ role: DatabaseRole; job: Job }> { return request(`/api/v1/postgresql/roles/${encodeURIComponent(id)}/rotate`, { method: 'POST' }) }
export async function revealCredential(id: string): Promise<string> { return (await request<{ credential: string }>(`/api/v1/postgresql/roles/${encodeURIComponent(id)}/credential`, { method: 'POST' })).credential }
export function createDatabase(input: { instanceId: string; name: string; ownerRoleId: string }): Promise<{ database: ManagedDatabase; job: Job }> { return request('/api/v1/postgresql/databases', { method: 'POST', body: JSON.stringify(input) }) }
export function createGrant(input: { databaseId: string; roleId: string; access: AccessLevel }): Promise<{ grant: DatabaseGrant; job: Job }> { return request('/api/v1/postgresql/grants', { method: 'POST', body: JSON.stringify(input) }) }
export function createBackup(databaseId: string): Promise<{ restorePoint: RestorePoint; job: Job }> { return request(`/api/v1/postgresql/databases/${encodeURIComponent(databaseId)}/backups`, { method: 'POST' }) }
export function prepareRestore(restorePointId: string): Promise<{ restorePoint: RestorePoint; job: Job }> { return request(`/api/v1/postgresql/restore-points/${encodeURIComponent(restorePointId)}/restore`, { method: 'POST' }) }
export function getPlan(resourceType: ResourceType, id: string): Promise<{ plan: PostgresPlan; expiresAt: string }> { return request(`/api/v1/postgresql/${resourceType}/${encodeURIComponent(id)}/plan`) }
export function applyPlan(resourceType: ResourceType, id: string): Promise<Job> { return request(`/api/v1/postgresql/${resourceType}/${encodeURIComponent(id)}/apply`, { method: 'POST' }) }
export function watchDatabaseJob(id: number, onEvent: (event: JobEvent) => void, onError?: () => void) { return subscribeToJob(id, onEvent, onError) }
