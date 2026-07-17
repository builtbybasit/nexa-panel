import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  bootstrap,
  changePassword,
  confirmMFA,
  createUser,
  deleteUser,
  disableMFA,
  enrollMFA,
  getIdentityStatus,
  IdentityRequestError,
  listUsers,
  logout,
  replaceUserSites,
  updateUser,
  verifyMFA,
} from './api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('identity API', () => {
  it('loads the public bootstrap status', async () => {
    const status = {
      bootstrapRequired: true,
      authenticated: false,
      mfaEnabled: false,
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

  it('disables MFA with the current password', async () => {
    const user = { id: 'user-1', username: 'admin', role: 'admin' }
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ user, mfaEnabled: false }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(disableMFA('a-strong-password')).resolves.toEqual({ user, mfaEnabled: false })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/mfa/disable', {
      method: 'POST',
      body: JSON.stringify({ password: 'a-strong-password' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('changes the account password with the current and new passwords', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(changePassword('old-password-1', 'new-password-1')).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword: 'old-password-1', newPassword: 'new-password-1' }),
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

describe('user management API', () => {
  const managed = {
    id: 'user-2',
    username: 'dev',
    role: 'developer',
    createdAt: '2026-07-01T00:00:00Z',
    lastLoginAt: null,
    mfaConfirmed: false,
    siteIds: ['site-1'],
  }

  it('lists managed users', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [managed] }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listUsers()).resolves.toEqual([managed])
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/users', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('creates a user with username, password, and role', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json(managed, { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(createUser({ username: 'dev', password: 'a-strong-password', role: 'developer' })).resolves.toEqual(managed)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/users', {
      method: 'POST',
      body: JSON.stringify({ username: 'dev', password: 'a-strong-password', role: 'developer' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('patches role and password changes independently', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(updateUser('user-2', { role: 'operator' })).resolves.toBeUndefined()
    await expect(updateUser('user-2', { password: 'a-new-strong-password' })).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/users/user-2', {
      method: 'PATCH',
      body: JSON.stringify({ role: 'operator' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/users/user-2', {
      method: 'PATCH',
      body: JSON.stringify({ password: 'a-new-strong-password' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('deletes a user without a body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(deleteUser('user-2')).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/users/user-2', {
      method: 'DELETE',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('replaces developer site grants with a PUT of siteIds', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(replaceUserSites('user-2', ['site-1', 'site-2'])).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/users/user-2/sites', {
      method: 'PUT',
      body: JSON.stringify({ siteIds: ['site-1', 'site-2'] }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('surfaces the server-safe error envelope for guarded operations', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        Response.json({ code: 'last_admin', message: 'The last administrator cannot be deleted.' }, { status: 400 }),
      ),
    )
    await expect(deleteUser('user-1')).rejects.toEqual(
      new IdentityRequestError('The last administrator cannot be deleted.', 400, 'last_admin'),
    )
  })
})
