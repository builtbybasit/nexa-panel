// @vitest-environment happy-dom

import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useIdentityStore } from '../store'
import AuthGate from './AuthGate.vue'

beforeEach(() => {
  setActivePinia(createPinia())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('initial administrator setup', () => {
  it('collects the server-issued setup token and renders the published password policy', async () => {
    const identity = useIdentityStore()
    identity.phase = 'bootstrap'
    identity.bootstrapTokenRequired = true
    identity.passwordPolicy = {
      minLength: 16,
      maxLength: 512,
      requiredClasses: 3,
      classExemptLength: 24,
      denylistApplied: true,
      rejectsUsername: true,
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json({
          user: { id: 'admin-1', username: 'operator', role: 'admin' },
          next: 'mfa_enrollment',
        }, { status: 201 }),
      )
      .mockResolvedValueOnce(
        Response.json({ secret: 'JBSWY3DPEHPK3PXP', provisioningUri: 'otpauth://totp/Nexa%20Panel:admin' }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(AuthGate)
    // Every published rule is rendered, including the two the client cannot infer.
    expect(wrapper.text()).toContain('16–512 characters')
    expect(wrapper.text()).toContain('at least 3 of lowercase, uppercase, digits and symbols')
    expect(wrapper.text()).toContain('24 characters or longer')
    expect(wrapper.text()).toContain('Common and predictable passwords are rejected.')
    expect(wrapper.text()).toContain('The password cannot contain your username.')
    await wrapper.get('input[name="username"]').setValue('operator')
    await wrapper.get('input[name="password"]').setValue('Strong-Password-2026!')
    await wrapper.get('input[name="confirm-password"]').setValue('Strong-Password-2026!')
    await wrapper.get('input[name="bootstrap-token"]').setValue('server-token')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/bootstrap', expect.objectContaining({
      body: JSON.stringify({
        username: 'operator',
        password: 'Strong-Password-2026!',
        bootstrapToken: 'server-token',
      }),
    }))
  })

  it('renders nothing about the policy until the server publishes one', () => {
    const identity = useIdentityStore()
    identity.phase = 'bootstrap'

    const wrapper = mount(AuthGate)

    expect(wrapper.find('input[name="password"]').attributes('minlength')).toBeUndefined()
    expect(wrapper.text()).not.toContain('characters.')
  })
})

describe('optional two-factor enrollment', () => {
  it('always offers to skip the enrollment step', async () => {
    const identity = useIdentityStore()
    identity.user = { id: 'admin-1', username: 'admin', role: 'admin' }
    identity.phase = 'enroll'
    identity.mfaEnrollmentRecommended = true
    identity.enrollment = { secret: 'JBSWY3DPEHPK3PXP', provisioningUri: 'otpauth://totp/Nexa%20Panel:admin' }

    const wrapper = mount(AuthGate)
    const skip = wrapper.findAll('button').find((button) => button.text().startsWith('Skip for now'))
    expect(skip).toBeDefined()
    await skip?.trigger('click')

    expect(identity.phase).toBe('authenticated')
  })
})
