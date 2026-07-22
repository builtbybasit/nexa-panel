import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

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

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Firewall request')
}

export async function getFirewallStatus(): Promise<FirewallStatus> {
  return request<FirewallStatus>('/api/v1/firewall')
}

export function firewallAction(action: FirewallAction, rule?: Partial<FirewallRule>) {
  return request<{ job: Job }>('/api/v1/firewall/action', {
    method: 'POST',
    body: JSON.stringify({ action, rule: rule ?? {} }),
  })
}
