export interface AuditEvent {
  id: number
  occurredAt: string
  actorUserId?: string
  action: string
  subject: string
  remoteAddress?: string
  metadata: Record<string, unknown>
}

export async function listAuditEvents(): Promise<AuditEvent[]> {
  const response = await fetch('/api/v1/audit/events?limit=100', {
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) throw new Error(`Audit request failed with status ${response.status}`)
  const body = (await response.json()) as { items: AuditEvent[] }
  return body.items
}
