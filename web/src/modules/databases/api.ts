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
/** Engine family key — how the backend routes a resource to its adapter. */
export type DatabaseEngine = 'mysql' | 'postgresql'
/** Concrete server flavor, shown to people. */
type ServerKind = 'mysql' | 'mariadb' | 'postgresql'
export type AccessLevel = 'connect' | 'read_only' | 'read_write'

export interface DatabaseServer {
  id: string
  engine: DatabaseEngine
  kind: ServerKind
  version: string
  versionText?: string
  cluster?: string
  port: number
  status: DatabaseStatus
  socketPath: string
  systemdUnit: string
  managedByNexa?: boolean
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface DatabaseUser {
  id: string
  serverId: string
  engine: DatabaseEngine
  name: string
  /** Client host scope; present on MySQL-family users only. */
  host?: string
  status: DatabaseStatus
  /** Counts applied password changes; the passwords live with whoever set them. */
  credentialVersion: number
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface ManagedDatabase {
  id: string
  serverId: string
  engine: DatabaseEngine
  name: string
  ownerUserId: string
  siteId?: string
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
  userId: string
  engine: DatabaseEngine
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
  engine: DatabaseEngine
  status: DatabaseStatus
  sha256?: string
  sizeBytes?: number
  verifiedAt?: string
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Databases request')
}

async function list<T>(path: string): Promise<T[]> {
  return (await request<{ items: T[] }>(path)).items
}

export function listServers(): Promise<DatabaseServer[]> {
  return list<DatabaseServer>('/api/v1/databases/servers')
}

export function listUsers(): Promise<DatabaseUser[]> {
  return list<DatabaseUser>('/api/v1/databases/users')
}

export function listDatabases(): Promise<ManagedDatabase[]> {
  return list<ManagedDatabase>('/api/v1/databases')
}

export function listGrants(): Promise<DatabaseGrant[]> {
  return list<DatabaseGrant>('/api/v1/databases/grants')
}

export function listRestorePoints(): Promise<RestorePoint[]> {
  return list<RestorePoint>('/api/v1/databases/restore-points')
}

export function createServer(input: {
  engine: DatabaseEngine
  version: string
  cluster: string
  port?: number
}): Promise<{ server: DatabaseServer; job: Job }> {
  return request('/api/v1/databases/servers', { method: 'POST', body: JSON.stringify(input) })
}

export function createUser(input: {
  serverId: string
  name: string
  host?: string
  password: string
}): Promise<{ user: DatabaseUser; job: Job }> {
  return request('/api/v1/databases/users', { method: 'POST', body: JSON.stringify(input) })
}

export function setUserPassword(
  id: string,
  password: string,
): Promise<{ user: DatabaseUser; job: Job }> {
  return request(`/api/v1/databases/users/${encodeURIComponent(id)}/password`, {
    method: 'POST',
    body: JSON.stringify({ password }),
  })
}

export function createDatabase(input: {
  serverId: string
  name: string
  ownerUserId: string
  siteId?: string
}): Promise<{ database: ManagedDatabase; job: Job }> {
  return request('/api/v1/databases', { method: 'POST', body: JSON.stringify(input) })
}

export function createGrant(input: {
  databaseId: string
  userId: string
  access: AccessLevel
}): Promise<{ grant: DatabaseGrant; job: Job }> {
  return request('/api/v1/databases/grants', { method: 'POST', body: JSON.stringify(input) })
}

export function dropUser(id: string): Promise<{ job: Job }> {
  return request(`/api/v1/databases/users/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function dropDatabase(id: string): Promise<{ job: Job }> {
  return request(`/api/v1/databases/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function dropGrant(id: string): Promise<{ job: Job }> {
  return request(`/api/v1/databases/grants/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function createBackup(databaseId: string): Promise<{ restorePoint: RestorePoint; job: Job }> {
  return request(`/api/v1/databases/${encodeURIComponent(databaseId)}/backups`, { method: 'POST' })
}

export function restoreBackup(
  restorePointId: string,
): Promise<{ restorePoint: RestorePoint; job: Job }> {
  return request(`/api/v1/databases/restore-points/${encodeURIComponent(restorePointId)}/restore`, {
    method: 'POST',
  })
}
