import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { appQueryClient } from '@/shared/query/client'
import { registerUnsavedChanges } from '@/shared/navigation/unsavedChanges'

import { useIdentityStore } from './store'

beforeEach(() => {
  setActivePinia(createPinia())
})

afterEach(() => {
  appQueryClient.clear()
  vi.unstubAllGlobals()
})

describe('identity session state', () => {
  it('admits an unenrolled administrator and only recommends a second factor', async () => {
    const user = { id: 'admin-1', username: 'admin', role: 'admin' }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        Response.json({
          bootstrapRequired: false,
          authenticated: true,
          mfaEnabled: false,
          mfaChallengeRequired: false,
          mfaEnrollmentRecommended: true,
          bootstrapTokenRequired: false,
          passwordPolicy: {
            minLength: 12,
            maxLength: 1024,
            requiredClasses: 3,
            classExemptLength: 20,
            denylistApplied: true,
            rejectsUsername: true,
          },
          user,
        }),
      ),
    )

    const identity = useIdentityStore()
    await identity.initialize()

    expect(identity.phase).toBe('authenticated')
    expect(identity.authenticated).toBe(true)
    expect(identity.mfaEnrollmentRecommended).toBe(true)
    expect(identity.passwordPolicy?.minLength).toBe(12)
  })

  it('lets an administrator skip the enrollment offer that follows bootstrap', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(
          Response.json({ user: { id: 'admin-1', username: 'admin', role: 'admin' }, next: 'mfa_enrollment' }, { status: 201 }),
        )
        .mockResolvedValueOnce(
          Response.json({ secret: 'JBSWY3DPEHPK3PXP', provisioningUri: 'otpauth://totp/Nexa%20Panel:admin' }),
        ),
    )

    const identity = useIdentityStore()
    await identity.bootstrap('admin', 'Strong-Password-2026!')

    expect(identity.phase).toBe('enroll')
    expect(identity.mfaEnrollmentRecommended).toBe(true)
    identity.skipEnrollment()
    expect(identity.phase).toBe('authenticated')
    expect(identity.authenticated).toBe(true)
  })

  it('leaves an enrolled account in the challenge it cannot skip', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        Response.json({
          bootstrapRequired: false,
          authenticated: false,
          mfaEnabled: true,
          mfaChallengeRequired: true,
          mfaEnrollmentRecommended: false,
          bootstrapTokenRequired: false,
          passwordPolicy: {
            minLength: 12,
            maxLength: 1024,
            requiredClasses: 3,
            classExemptLength: 20,
            denylistApplied: true,
            rejectsUsername: true,
          },
          user: { id: 'admin-1', username: 'admin', role: 'admin' },
        }),
      ),
    )

    const identity = useIdentityStore()
    await identity.initialize()

    expect(identity.phase).toBe('challenge')
    expect(identity.authenticated).toBe(false)
  })

  it('clears user-scoped query data on logout', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
    appQueryClient.setQueryData(['sites'], [{ id: 'site-from-previous-user' }])
    const identity = useIdentityStore()
    identity.user = { id: 'admin-1', username: 'admin', role: 'admin' }
    identity.phase = 'authenticated'

    await identity.logout()

    expect(appQueryClient.getQueryData(['sites'])).toBeUndefined()
    expect(identity.phase).toBe('login')
  })

  it('does not sign out when that would discard an edited file without confirmation', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(false))
    const unregister = registerUnsavedChanges(() => true)
    const identity = useIdentityStore()
    identity.user = { id: 'admin-1', username: 'admin', role: 'admin' }
    identity.phase = 'authenticated'

    await identity.logout()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(identity.authenticated).toBe(true)
    unregister()
  })

  it('signs an administrator in with a recovery code', async () => {
    const user = { id: 'admin-1', username: 'admin', role: 'admin' } as const
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ user }))
    vi.stubGlobal('fetch', fetchMock)
    const identity = useIdentityStore()
    identity.user = user
    identity.phase = 'challenge'
    identity.mfaEnabled = true

    await identity.verify('AAAA-BBBB-CCCC-DDDD', true)

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/mfa/verify', expect.objectContaining({
      body: JSON.stringify({ recoveryCode: 'AAAA-BBBB-CCCC-DDDD' }),
    }))
    expect(identity.phase).toBe('authenticated')
    expect(identity.mfaEnabled).toBe(true)
  })

  it('moves a recovering administrator from the challenge into fresh enrollment', async () => {
    const user = { id: 'admin-1', username: 'admin', role: 'admin' } as const
    const fetchMock = vi
      .fn()
      .mockResolvedValue(Response.json({ secret: 'JBSWY3DPEHPK3PXP', provisioningUri: 'otpauth://totp/Nexa%20Panel:admin' }))
    vi.stubGlobal('fetch', fetchMock)
    const identity = useIdentityStore()
    identity.user = user
    identity.phase = 'challenge'
    identity.mfaEnabled = true

    await identity.recoverEnrollment('AAAA-BBBB-CCCC-DDDD')

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/mfa/recover', expect.objectContaining({
      body: JSON.stringify({ recoveryCode: 'AAAA-BBBB-CCCC-DDDD' }),
    }))
    expect(identity.phase).toBe('enroll')
    expect(identity.mfaEnabled).toBe(false)
    expect(identity.enrollment?.secret).toBe('JBSWY3DPEHPK3PXP')
    expect(identity.authenticated).toBe(false)
  })

  it('exposes role capabilities from the current session', () => {
    const identity = useIdentityStore()
    identity.user = { id: 'operator-1', username: 'operator', role: 'operator' }
    identity.phase = 'authenticated'

    expect(identity.can('databases.write')).toBe(true)
    expect(identity.can('operations.apply')).toBe(false)
  })
})
