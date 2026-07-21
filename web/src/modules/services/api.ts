import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

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

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Services request')
}

export async function listServices(): Promise<Service[]> {
  return (await request<{ items: Service[] }>('/api/v1/services')).items
}

export function serviceAction(unit: string, action: ServiceAction) {
  return request<{ job: Job }>('/api/v1/services/action', {
    method: 'POST',
    body: JSON.stringify({ unit, action }),
  })
}
