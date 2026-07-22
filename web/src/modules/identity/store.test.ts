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
  it('does not force enrollment: an account without MFA is authenticated, and any role may defer', async () => {
    const user = { id: 'admin-1', username: 'admin', role: 'admin' }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        Response.json({
          bootstrapRequired: false,
          authenticated: true,
          mfaEnabled: false,
          mfaChallengeRequired: false,
          mfaEnrollmentRequired: false,
          user,
        }),
      ),
    )

    const identity = useIdentityStore()
    await identity.initialize()

    // MFA is optional, so an administrator without a second factor is signed in.
    expect(identity.phase).toBe('authenticated')
    expect(identity.authenticated).toBe(true)

    // Even if the enroll step is opened, any role (including admin) can defer it.
    identity.phase = 'enroll'
    identity.skipEnrollment()
    expect(identity.phase).toBe('authenticated')
  })

  it('exposes role capabilities from the current session', () => {
    const identity = useIdentityStore()
    identity.user = { id: 'operator-1', username: 'operator', role: 'operator' }
    identity.phase = 'authenticated'

    expect(identity.can('databases.write')).toBe(true)
    expect(identity.can('operations.apply')).toBe(false)
  })
})
