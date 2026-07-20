import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

export type ToolKind = 'phpmyadmin' | 'pgadmin'
export type ToolAction = 'tool.deploy' | 'tool.start' | 'tool.stop'

export interface AdminTool {
  kind: ToolKind
  image: string
  containerName: string
  port: number
  memoryMb: number
  pidsLimit: number
  status: string
  systemdUnit: string
  onDemand: boolean
  lastJobId?: number
  failure?: string
}

export interface AdminToolPlan {
  id: string
  toolKind: ToolKind
  operation: ToolAction
  agentPlan: { steps: string[]; warnings: string[]; expiresAt: string }
  expiresAt: string
}

export interface LaunchInput {
  sourceEngine: 'postgresql' | 'mysql'
  databaseId: string
  accountId: string
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Admin tool request')
}

export async function listTools() {
  return (await request<{ items: AdminTool[] }>('/api/v1/admin-tools')).items
}

export function prepareChange(kind: ToolKind, action: ToolAction) {
  return request<{ tool: AdminTool; job: Job }>(`/api/v1/admin-tools/${kind}/plan`, {
    method: 'POST',
    body: JSON.stringify({ action }),
  })
}

export function getPlan(kind: ToolKind) {
  return request<{ plan: AdminToolPlan }>(`/api/v1/admin-tools/${kind}/plan`)
}

export function applyPlan(kind: ToolKind) {
  return request<Job>(`/api/v1/admin-tools/${kind}/apply`, { method: 'POST' })
}

export function launchTool(kind: ToolKind, input: LaunchInput) {
  return request<{ url: string }>(`/api/v1/admin-tools/${kind}/launch`, {
    method: 'POST',
    body: JSON.stringify(input),
  })
}
