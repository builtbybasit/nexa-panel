import { apiRequest } from '@/shared/api/request'

export interface AuditEvent {
  id: number
  occurredAt: string
  actorUserId?: string
  action: string
  subject: string
  remoteAddress?: string
  metadata: Record<string, unknown>
}

/**
 * Latest events for the audit view, which polls them. It has to go through the
 * shared helper: a hand-rolled fetch omits the background marker and the poll
 * alone would renew the idle timeout.
 */
export async function listAuditEvents(): Promise<AuditEvent[]> {
  const body = await apiRequest<{ items: AuditEvent[] }>('/api/v1/audit/events?limit=100', undefined, 'Audit request')
  return body.items
}
