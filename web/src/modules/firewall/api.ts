import { apiRequest } from '@/shared/api/request'

import { LockoutRiskError, type LockoutRevert } from '../safeguard/types'

import type { Job } from '../jobs/api'

export type { LockoutRevert }
export { LockoutRiskError }

export type FirewallAction = 'enable' | 'disable' | 'allow' | 'deny' | 'delete'

export interface FirewallRule {
  /** A single port ("80") or an inclusive range ("8000:8100"). */
  port: string
  /** "tcp", "udp", or "" for both. */
  protocol: string
  /** "allow" or "deny" (parsed status may also report "reject"/"limit"). */
  action: string
  /** Optional source IP or CIDR; empty means anywhere. */
  from: string
  /** Optional label attached when the rule is created. */
  comment?: string
  /** True for the IPv6 half of a rule as reported by ufw status. */
  v6: boolean
}

export interface FirewallStatus {
  /** Whether the ufw binary is present on the node at all. */
  installed: boolean
  /** Whether the firewall is enabled and enforcing. */
  active: boolean
  /** The incoming allow/deny table as ufw reports it. */
  rules: FirewallRule[]
}

export interface FirewallSubmission {
  job: Job
  revert?: LockoutRevert
  revertWindowSeconds?: number
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, {
    errorPrefix: 'Firewall request',
    createError: (message, _status, code) => (code === 'lockout_risk' ? new LockoutRiskError(message) : new Error(message)),
  })
}

export async function getFirewallStatus(): Promise<FirewallStatus> {
  return request<FirewallStatus>('/api/v1/firewall')
}

/**
 * Queues a firewall change. A lockout-capable change is refused with a
 * LockoutRiskError until `confirmLockoutRisk` is set; the accepted response then
 * carries the armed revert the operator has to confirm before it fires.
 */
export function firewallAction(action: FirewallAction, rule?: Partial<FirewallRule>, confirmLockoutRisk = false) {
  return request<FirewallSubmission>('/api/v1/firewall/action', {
    method: 'POST',
    body: JSON.stringify({ action, rule: rule ?? {}, confirmLockoutRisk }),
  })
}

export function getFirewallReverts() {
  return request<{ items: LockoutRevert[]; windowSeconds: number }>('/api/v1/firewall/reverts')
}

export function confirmFirewallRevert(id: string) {
  return request<LockoutRevert>('/api/v1/firewall/reverts/confirm', {
    method: 'POST',
    body: JSON.stringify({ id }),
  })
}
