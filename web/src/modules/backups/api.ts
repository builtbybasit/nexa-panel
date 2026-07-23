import { apiRequest } from '@/shared/api/request'

export type BackupAccountType = 'local' | 's3' | 'sftp' | 'drive'

export interface BackupAccount {
  id: string
  name: string
  type: BackupAccountType
  path: string
  config: Record<string, string>
  hasSecret: boolean
  createdAt: string
  updatedAt: string
}

export interface BackupAccountRequest {
  name: string
  type: BackupAccountType
  path: string
  config: Record<string, string>
  /** Sensitive rclone params. Omitted on update leaves the stored secret intact. */
  secret?: Record<string, string>
}

export interface BackupTestResult {
  ok: boolean
  message: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Backup request')
}

export async function listBackupAccounts() {
  return (await request<{ items: BackupAccount[] }>('/api/v1/backups/accounts')).items
}

export function createBackupAccount(body: BackupAccountRequest) {
  return request<BackupAccount>('/api/v1/backups/accounts', { method: 'POST', body: JSON.stringify(body) })
}

export function updateBackupAccount(id: string, body: BackupAccountRequest) {
  return request<BackupAccount>(`/api/v1/backups/accounts/${id}`, { method: 'PUT', body: JSON.stringify(body) })
}

export function deleteBackupAccount(id: string) {
  return request<void>(`/api/v1/backups/accounts/${id}`, { method: 'DELETE' })
}

export function testBackupAccount(id: string) {
  return request<BackupTestResult>(`/api/v1/backups/accounts/${id}/test`, { method: 'POST' })
}

// --- plans -----------------------------------------------------------------

export interface BackupPlan {
  id: string
  name: string
  accountId: string
  copiesLimit: number
  siteIds: string[]
  /** Engine-qualified database references: "postgres:<id>" or "mysql:<id>". */
  databaseIds: string[]
  schedule: string
  enabled: boolean
  scheduleState: 'pending' | 'installed' | 'disabled' | 'error'
  scheduleError?: string
  scheduleSyncedAt?: string
  createdAt: string
  updatedAt: string
}

export interface BackupPlanRequest {
  name: string
  accountId: string
  copiesLimit: number
  siteIds: string[]
  databaseIds: string[]
  schedule: string
  enabled: boolean
}

export async function listBackupPlans() {
  return (await request<{ items: BackupPlan[] }>('/api/v1/backups/plans')).items
}

export function createBackupPlan(body: BackupPlanRequest) {
  return request<BackupPlan>('/api/v1/backups/plans', { method: 'POST', body: JSON.stringify(body) })
}

export function updateBackupPlan(id: string, body: BackupPlanRequest) {
  return request<BackupPlan>(`/api/v1/backups/plans/${id}`, { method: 'PUT', body: JSON.stringify(body) })
}

export function toggleBackupPlan(id: string, enabled: boolean) {
  return request<void>(`/api/v1/backups/plans/${id}/toggle`, { method: 'POST', body: JSON.stringify({ enabled }) })
}

export function deleteBackupPlan(id: string) {
  return request<void>(`/api/v1/backups/plans/${id}`, { method: 'DELETE' })
}

export function runBackupPlan(id: string) {
  return request<{ id: number }>(`/api/v1/backups/plans/${id}/run`, { method: 'POST' })
}

// --- copies ----------------------------------------------------------------

interface BackupCopyEntry {
  name: string
  sizeBytes: number
  sha256: string
}

export interface BackupCopy {
  id: string
  planId: string
  accountId: string
  copyName: string
  remotePath: string
  sizeBytes: number
  entries: BackupCopyEntry[]
  status: string
  integrityState: 'unverified' | 'passed' | 'failed'
  integrityCheckedAt?: string
  restoreTestState: 'not_tested' | 'passed' | 'failed'
  restoreTestedAt?: string
  healthy: boolean
  healthError?: string
  createdAt: string
}

export async function listBackupCopies(planId: string) {
  return (await request<{ items: BackupCopy[] }>(`/api/v1/backups/plans/${planId}/copies`)).items
}

export interface BackupRestoreChoices {
  sites: { entry: string; siteId: string; clear: boolean }[]
  databases: { entry: string; databaseRef: string; clear: boolean }[]
  allowUnverified: boolean
}

export interface BackupRestoreRequest extends BackupRestoreChoices {
  /** Short-lived server binding for the exact plan the operator reviewed. */
  previewToken: string
}

interface BackupRestorePreviewItem {
  kind: 'site' | 'database'
  sourceEntry: string
  destinationRef: string
  destinationLabel: string
  clear: boolean
  impact: string
}

export interface BackupRestorePreview {
  copyId: string
  copyName: string
  integrityState: BackupCopy['integrityState']
  warnings: string[]
  items: BackupRestorePreviewItem[]
  previewToken: string
  expiresAt: string
}

export function previewBackupRestore(copyId: string, body: BackupRestoreChoices) {
  return request<BackupRestorePreview>(`/api/v1/backups/copies/${copyId}/restore/preview`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function restoreBackupCopy(copyId: string, body: BackupRestoreRequest) {
  return request<{ id: number }>(`/api/v1/backups/copies/${copyId}/restore`, { method: 'POST', body: JSON.stringify(body) })
}

export function deleteBackupCopy(copyId: string) {
  return request<void>(`/api/v1/backups/copies/${copyId}`, { method: 'DELETE' })
}

// --- panel-state (system) backups ------------------------------------------
// These back up the panel's OWN state — control.db plus master.key — off-node,
// so a lost disk doesn't take every stored credential with it. Restore is a
// deliberate CLI step (see docs/runbooks/panel-state-restore.md); the archive
// is credential-equivalent, so it only belongs on a trusted, encrypted remote.

export interface SystemBackupCopy {
  id: string
  accountId: string
  copyName: string
  remotePath: string
  sizeBytes: number
  sha256: string
  createdAt: string
}

export async function listSystemBackupCopies() {
  return (await request<{ items: SystemBackupCopy[] }>('/api/v1/backups/system')).items
}

export function runSystemBackup(accountId: string) {
  return request<{ id: number }>('/api/v1/backups/system', { method: 'POST', body: JSON.stringify({ accountId }) })
}
