import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiRequest } from './request'
import { registerMFAStepUpHandler } from './mfaStepUp'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('apiRequest', () => {
  it.each([204, 205])('accepts a successful %i response without a body', async (status) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status })))

    await expect(apiRequest<void>('/api/v1/resource', { method: 'DELETE' })).resolves.toBeUndefined()
  })

  it('accepts an empty successful response when the server omits a body', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 200 })))

    await expect(apiRequest<void>('/api/v1/resource')).resolves.toBeUndefined()
  })

  it('parses JSON success responses and surfaces the server-safe error message', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'resource-1' }))
      .mockResolvedValueOnce(Response.json({ message: 'The resource is locked.' }, { status: 409 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(apiRequest<{ id: string }>('/api/v1/resource')).resolves.toEqual({ id: 'resource-1' })
    await expect(apiRequest('/api/v1/resource', undefined, 'Resource request')).rejects.toMatchObject({
      message: 'The resource is locked.',
      status: 409,
    })
  })

  it('retries a protected request after the session completes MFA step-up', async () => {
    const stepUp = vi.fn().mockResolvedValue(undefined)
    const unregister = registerMFAStepUpHandler(stepUp)
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json({ code: 'mfa_step_up_required', message: 'Verify again to continue.' }, { status: 403 }),
      )
      .mockResolvedValueOnce(Response.json({ id: 42 }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(apiRequest<{ id: number }>('/api/v1/protected', { method: 'POST' })).resolves.toEqual({ id: 42 })
    expect(stepUp).toHaveBeenCalledOnce()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    unregister()
  })
})
