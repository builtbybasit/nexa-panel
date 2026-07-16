import { afterEach, describe, expect, it, vi } from 'vitest'

import { applyPlan, observeProbe, planProbe, type OperationPlan } from './api'

const plan: OperationPlan = {
  id: 'plan-1',
  kind: 'nexa.probe.v1',
  target: '/etc/nexa-panel/probe.conf',
  action: 'create',
  changed: true,
  before: { exists: false },
  desired: { exists: true, digest: 'abc', content: 'managed=true\n' },
  plannedAt: '2026-07-16T00:00:00Z',
  expiresAt: '2026-07-16T00:10:00Z',
  signature: 'signed-plan',
}

afterEach(() => vi.unstubAllGlobals())

describe('node operations API', () => {
  it('observes and plans the fixed probe', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ exists: false }))
      .mockResolvedValueOnce(Response.json(plan))
    vi.stubGlobal('fetch', fetchMock)

    await expect(observeProbe()).resolves.toEqual({ exists: false })
    await expect(planProbe({ present: true, content: 'managed=true\n' })).resolves.toEqual(plan)
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/node/probe/plan', {
      method: 'POST',
      body: JSON.stringify({ present: true, content: 'managed=true\n' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('queues an approved plan', async () => {
    const job = {
      id: 4,
      kind: 'node.probe.apply',
      state: 'queued',
      progress: 0,
      request: plan,
      createdAt: '2026-07-16T00:00:00Z',
      updatedAt: '2026-07-16T00:00:00Z',
    }
    const fetchMock = vi.fn().mockResolvedValue(Response.json(job, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(applyPlan(plan)).resolves.toEqual(job)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/node/probe/apply', {
      method: 'POST',
      body: JSON.stringify(plan),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })
})
