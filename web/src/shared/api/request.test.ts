import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'

import { apiRequest } from './request'
import { registerMFAStepUpHandler } from './mfaStepUp'
import { registerUnauthorizedHandler } from './unauthorized'

// Spelled out rather than imported from the modules under test: these are the
// wire contract the server matches on, so a rename has to fail here.
const csrfHeaderName = 'X-CSRF-Token'
const backgroundHeaderName = 'X-Nexa-Background'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const headersOf = (fetchMock: Mock, call: number): Record<string, string> =>
  (fetchMock.mock.calls[call]?.[1] as RequestInit).headers as Record<string, string>

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

  it('attaches the double-submit CSRF token to unsafe methods only', async () => {
    vi.stubGlobal('document', { cookie: 'theme=dark; nexa_csrf=token-abc; nexa_other=x' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await apiRequest<void>('/api/v1/resource', { method: 'POST', body: '{}' })
    await apiRequest<void>('/api/v1/resource')

    expect(headersOf(fetchMock, 0)[csrfHeaderName]).toBe('token-abc')
    expect(headersOf(fetchMock, 1)).not.toHaveProperty(csrfHeaderName)
  })

  it('sends no CSRF header when no session has issued a token', async () => {
    vi.stubGlobal('document', { cookie: 'theme=dark' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await apiRequest<void>('/api/v1/resource', { method: 'DELETE' })

    expect(headersOf(fetchMock, 0)).not.toHaveProperty(csrfHeaderName)
  })

  it('marks a request as background only once the user has stopped interacting', async () => {
    vi.stubGlobal('document', { cookie: '' })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await apiRequest<void>('/api/v1/resource')
    vi.useFakeTimers()
    vi.setSystemTime(Date.now() + 10 * 60_000)
    await apiRequest<void>('/api/v1/resource')

    expect(headersOf(fetchMock, 0)).not.toHaveProperty(backgroundHeaderName)
    expect(headersOf(fetchMock, 1)[backgroundHeaderName]).toBe('1')
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

  it('expires the local session when an authenticated API request returns 401', async () => {
    const expireSession = vi.fn()
    const unregister = registerUnauthorizedHandler(expireSession)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        Response.json({ code: 'session_expired', message: 'Sign in again.' }, { status: 401 }),
      ),
    )

    await expect(apiRequest('/api/v1/sites')).rejects.toMatchObject({ status: 401 })

    expect(expireSession).toHaveBeenCalledOnce()
    unregister()
  })

  it('can suppress session expiry for expected authentication failures', async () => {
    const expireSession = vi.fn()
    const unregister = registerUnauthorizedHandler(expireSession)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        Response.json({ code: 'invalid_credentials', message: 'Incorrect credentials.' }, { status: 401 }),
      ),
    )

    await expect(
      apiRequest('/api/v1/auth/login', { method: 'POST' }, { handleUnauthorized: false }),
    ).rejects.toMatchObject({ status: 401 })

    expect(expireSession).not.toHaveBeenCalled()
    unregister()
  })
})
