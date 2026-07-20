import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

type CertificateStatus =
  | 'planning'
  | 'plan_ready'
  | 'issuing'
  | 'active'
  | 'renewing'
  | 'revoking'
  | 'revoked'
  | 'failed'

export type CertificateOperation = 'issue' | 'renew' | 'revoke'

export interface Certificate {
  id: string
  siteId: string
  primaryDomain: string
  email: string
  status: CertificateStatus
  domains: string[]
  certificatePath?: string
  issuedAt?: string
  expiresAt?: string
  expiringSoon: boolean
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface CertificatePlan {
  operation: CertificateOperation
  agentPlan: {
    id: string
    certificatePath: string
    privateKeyPath: string
    expiresAt: string
    request: { domains: string[] }
  }
  dns: Record<string, string[]>
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Certificate request')
}

export async function listCertificates(siteId?: string): Promise<Certificate[]> {
  const query = siteId ? `?siteId=${encodeURIComponent(siteId)}` : ''
  return (await request<{ items: Certificate[] }>(`/api/v1/certificates${query}`)).items
}

export function createCertificate(siteId: string, email: string): Promise<{ certificate: Certificate; job: Job }> {
  return request('/api/v1/certificates', { method: 'POST', body: JSON.stringify({ siteId, email }) })
}

export function getCertificatePlan(id: string): Promise<{ plan: CertificatePlan; expiresAt: string }> {
  return request(`/api/v1/certificates/${encodeURIComponent(id)}/plan`)
}

export function prepareCertificate(id: string, operation: CertificateOperation): Promise<Job> {
  return request(`/api/v1/certificates/${encodeURIComponent(id)}/plans`, {
    method: 'POST',
    body: JSON.stringify({ operation }),
  })
}

export function applyCertificate(id: string): Promise<Job> {
  return request(`/api/v1/certificates/${encodeURIComponent(id)}/apply`, { method: 'POST' })
}
