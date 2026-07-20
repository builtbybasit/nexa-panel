import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useIdentityStore } from './store'

beforeEach(() => {
  setActivePinia(createPinia())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('identity session state', () => {
  it('requires an administrator without MFA to finish enrollment before authentication', async () => {
    const user = { id: 'admin-1', username: 'admin', role: 'admin' }
    const enrollment = { secret: 'secret', provisioningUri: 'otpauth://totp/Nexa:admin' }
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(
          Response.json({
            bootstrapRequired: false,
            authenticated: false,
            mfaEnabled: false,
            mfaChallengeRequired: false,
            mfaEnrollmentRequired: true,
            user,
          }),
        )
        .mockResolvedValueOnce(Response.json(enrollment)),
    )

    const identity = useIdentityStore()
    await identity.initialize()

    expect(identity.phase).toBe('enroll')
    expect(identity.authenticated).toBe(false)
    expect(identity.enrollment).toEqual(enrollment)
    identity.skipEnrollment()
    expect(identity.phase).toBe('enroll')
  })

  it('exposes role capabilities from the current session', () => {
    const identity = useIdentityStore()
    identity.user = { id: 'operator-1', username: 'operator', role: 'operator' }
    identity.phase = 'authenticated'

    expect(identity.can('databases.write')).toBe(true)
    expect(identity.can('operations.apply')).toBe(false)
  })
})
