import { afterEach, describe, expect, it, vi } from 'vitest'

import { bootstrap, confirmMFA, enrollMFA, getIdentityStatus, IdentityRequestError, logout, verifyMFA } from './api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('identity API', () => {
  it('loads the public bootstrap status', async () => {
    const status = {
      bootstrapRequired: true,
      authenticated: false,
      mfaEnrollmentRequired: false,
      mfaChallengeRequired: false,
    }
    const fetchMock = vi.fn().mockResolvedValue(Response.json(status))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getIdentityStatus()).resolves.toEqual(status)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/status', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('creates the first administrator with JSON credentials', async () => {
    const user = { id: 'user-1', username: 'admin', role: 'admin' }
    const response = { user, next: 'mfa_enrollment' }
    const fetchMock = vi.fn().mockResolvedValue(Response.json(response, { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(bootstrap('admin', 'a-strong-password')).resolves.toEqual(response)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/bootstrap', {
      method: 'POST',
      body: JSON.stringify({ username: 'admin', password: 'a-strong-password' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('enrolls, confirms, and verifies MFA', async () => {
    const user = { id: 'user-1', username: 'admin', role: 'admin' }
    const enrollment = { secret: 'JBSWY3DPEHPK3PXP', provisioningUri: 'otpauth://totp/Nexa%20Panel:admin' }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json(enrollment))
      .mockResolvedValueOnce(Response.json({ user, recoveryCodes: ['AAAA-BBBB-CCCC-DDDD'] }))
      .mockResolvedValueOnce(Response.json({ user }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(enrollMFA()).resolves.toEqual(enrollment)
    await expect(confirmMFA('123456')).resolves.toEqual({ user, recoveryCodes: ['AAAA-BBBB-CCCC-DDDD'] })
    await expect(verifyMFA('AAAA-BBBB-CCCC-DDDD', true)).resolves.toEqual(user)
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/auth/mfa/verify', {
      method: 'POST',
      body: JSON.stringify({ recoveryCode: 'AAAA-BBBB-CCCC-DDDD' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('surfaces the server-safe error and supports empty logout responses', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        Response.json({ code: 'invalid_credentials', message: 'The username or password is incorrect.' }, { status: 401 }),
      ),
    )
    await expect(getIdentityStatus()).rejects.toEqual(
      new IdentityRequestError('The username or password is incorrect.', 401, 'invalid_credentials'),
    )

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
    await expect(logout()).resolves.toBeUndefined()
  })
})
