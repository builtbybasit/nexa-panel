import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  applyTask,
  createTask,
  deleteTask,
  getTask,
  listTaskRuns,
  listTasks,
  rollbackTask,
  runTask,
  updateTask,
} from './api'

const task = {
  id: 'task_1',
  siteId: 'site_1',
  name: 'Nightly cleanup',
  cronExpression: '0 0 * * *',
  command: 'php artisan cleanup',
  timeoutSeconds: 300,
  enabled: true,
  status: 'plan_ready',
  pendingRemoval: false,
  lastJobId: 7,
  createdAt: '2026-07-16T00:00:00Z',
  updatedAt: '2026-07-16T00:00:00Z',
}

const job = { id: 7, kind: 'schedule.plan', state: 'queued' }

const input = {
  name: 'Nightly cleanup',
  cronExpression: '0 0 * * *',
  command: 'php artisan cleanup',
  timeoutSeconds: 300,
  enabled: true,
}

const getInit = { credentials: 'same-origin', headers: { Accept: 'application/json' } }

function jsonInit(method: string, body: unknown) {
  return {
    method,
    body: JSON.stringify(body),
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
  }
}

const bareInit = (method: string) => ({ method, credentials: 'same-origin', headers: { Accept: 'application/json' } })

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('schedules API', () => {
  it('lists tasks and unwraps the items envelope', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [task] }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listTasks('site_1')).resolves.toEqual([task])
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/tasks', getInit)
  })

  it('queues a create with the full desired-state body pinned', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ task, job }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(createTask('site_1', input)).resolves.toEqual({ task, job })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/tasks', jsonInit('POST', input))
  })

  it('fetches a task with its rendered plan when plan_ready', async () => {
    const plan = {
      id: 'plan_1',
      artifacts: [{ path: '/etc/cron.d/nexa-task-task_1', mode: 420, content: '# Managed by Nexa Panel.', remove: false }],
      warnings: ['The site user cannot write outside its root.'],
      expiresAt: '2026-07-16T00:15:00Z',
    }
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ task, plan }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getTask('site_1', 'task_1')).resolves.toEqual({ task, plan })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/tasks/task_1', getInit)
  })

  it('normalizes an absent plan and null plan arrays', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ task: { ...task, status: 'active' }, plan: null }))
      .mockResolvedValueOnce(
        Response.json({ task, plan: { id: 'plan_1', artifacts: null, warnings: null, expiresAt: '2026-07-16T00:15:00Z' } }),
      )
    vi.stubGlobal('fetch', fetchMock)

    await expect(getTask('site_1', 'task_1')).resolves.toEqual({ task: { ...task, status: 'active' }, plan: undefined })
    await expect(getTask('site_1', 'task_1')).resolves.toEqual({
      task,
      plan: { id: 'plan_1', artifacts: [], warnings: [], expiresAt: '2026-07-16T00:15:00Z' },
    })
  })

  it('queues an update (replan) via PUT with the same body shape as create', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ task, job }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(updateTask('site_1', 'task_1', { ...input, enabled: false })).resolves.toEqual({ task, job })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/tasks/task_1', jsonInit('PUT', { ...input, enabled: false }))
  })

  it('queues apply, rollback, and run as bodyless POSTs', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(Response.json({ job }, { status: 202 })))
    vi.stubGlobal('fetch', fetchMock)

    await expect(applyTask('site_1', 'task_1')).resolves.toEqual({ job })
    await expect(rollbackTask('site_1', 'task_1')).resolves.toEqual({ job })
    await expect(runTask('site_1', 'task_1')).resolves.toEqual({ job })

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/tasks/task_1/apply', bareInit('POST'))
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/sites/site_1/tasks/task_1/rollback', bareInit('POST'))
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/sites/site_1/tasks/task_1/run', bareInit('POST'))
  })

  it('lists run history rows including skipped-overlap markers', async () => {
    const runs = [
      { startedAt: '2026-07-16T00:00:00Z', durationSeconds: 12, exitCode: 0, trigger: 'cron' },
      { startedAt: '2026-07-16T00:01:00Z', durationSeconds: 0, exitCode: -1, trigger: 'cron' },
    ]
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: runs }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listTaskRuns('site_1', 'task_1')).resolves.toEqual(runs)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/tasks/task_1/runs', getInit)
  })

  it('queues a delete that returns the removal-plan task and job', async () => {
    const removing = { ...task, pendingRemoval: true }
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ task: removing, job }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(deleteTask('site_1', 'task_1')).resolves.toEqual({ task: removing, job })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/tasks/task_1', bareInit('DELETE'))
  })

  it('encodes site and task identifiers into the path', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [] }))
    vi.stubGlobal('fetch', fetchMock)

    await listTaskRuns('site/1', 'task 1')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site%2F1/tasks/task%201/runs', getInit)
  })

  it('surfaces the server error envelope as a typed error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(Response.json({ code: 'invalid_cron', message: 'The cron expression must have 5 fields.' }, { status: 422 })),
    )
    await expect(createTask('site_1', { ...input, cronExpression: '* *' })).rejects.toMatchObject({
      message: 'The cron expression must have 5 fields.',
      status: 422,
      code: 'invalid_cron',
    })
  })
})
