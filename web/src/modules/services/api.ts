import { apiRequest } from '@/shared/api/request'

import { LockoutRiskError, type LockoutRevert } from '../safeguard/types'

import type { Job } from '../jobs/api'

export type { LockoutRevert }
export { LockoutRiskError }

export type ServiceAction = 'start' | 'stop' | 'restart' | 'enable' | 'disable'

export interface Service {
  /** Unit name with the ".service" suffix stripped, for display. */
  name: string
  /** Full systemd unit name used by actions. */
  systemdUnit: string
  /** Raw systemctl is-active word (active/inactive/failed/…). */
  status: string
  active: boolean
  enabled: boolean
}

export interface ServiceSubmission {
  job: Job
  revert?: LockoutRevert
  revertWindowSeconds?: number
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, {
    errorPrefix: 'Services request',
    createError: (message, _status, code) => (code === 'lockout_risk' ? new LockoutRiskError(message) : new Error(message)),
  })
}

export async function listServices(): Promise<Service[]> {
  return (await request<{ items: Service[] }>('/api/v1/services')).items
}

/**
 * Queues a service change. Stopping a panel-critical unit is refused with a
 * LockoutRiskError until `confirmLockoutRisk` is set; the accepted response then
 * carries the armed revert that starts the unit again unless it is confirmed.
 */
export function serviceAction(unit: string, action: ServiceAction, confirmLockoutRisk = false) {
  return request<ServiceSubmission>('/api/v1/services/action', {
    method: 'POST',
    body: JSON.stringify({ unit, action, confirmLockoutRisk }),
  })
}

export function getServiceReverts() {
  return request<{ items: LockoutRevert[]; windowSeconds: number }>('/api/v1/services/reverts')
}

export function confirmServiceRevert(id: string) {
  return request<LockoutRevert>('/api/v1/services/reverts/confirm', {
    method: 'POST',
    body: JSON.stringify({ id }),
  })
}
