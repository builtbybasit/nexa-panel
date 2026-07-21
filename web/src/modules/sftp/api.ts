import { apiRequest } from '@/shared/api/request'

export interface SftpAccess {
  siteId: string
  enabled: boolean
  username: string
  host: string
  port: number
  passwordSetAt?: string
}

// Returned only by enable and reset-password: the generated password appears
// once, in the response, and is never retrievable again.
export interface SftpCredential {
  enabled: boolean
  username: string
  host: string
  port: number
  password: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'SFTP request')
}

export function getSftpAccess(siteId: string): Promise<SftpAccess> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/sftp`)
}

export function enableSftp(siteId: string): Promise<SftpCredential> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/sftp/enable`, { method: 'POST' })
}

export function disableSftp(siteId: string): Promise<SftpAccess> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/sftp/disable`, { method: 'POST' })
}

export function resetSftpPassword(siteId: string): Promise<SftpCredential> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/sftp/reset-password`, { method: 'POST' })
}
