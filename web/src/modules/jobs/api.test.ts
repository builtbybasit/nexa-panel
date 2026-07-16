import { afterEach, describe, expect, it, vi } from 'vitest'

import { listJobs, submitDiagnostics, type Job } from './api'

const job: Job = {
  id: 12,
  kind: 'platform.diagnostics',
  state: 'queued',
  progress: 0,
  request: { delayMilliseconds: 150 },
  createdAt: '2026-07-16T00:00:00Z',
  updatedAt: '2026-07-16T00:00:00Z',
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('jobs API', () => {
  it('lists durable jobs', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [job] }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listJobs()).resolves.toEqual([job])
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/jobs?limit=50', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('submits a diagnostic job', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json(job, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(submitDiagnostics(150)).resolves.toEqual(job)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/jobs/diagnostics', {
      method: 'POST',
      body: JSON.stringify({ delayMilliseconds: 150 }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })
})
