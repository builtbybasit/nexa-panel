// @vitest-environment happy-dom

import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { requestMFAStepUp } from '@/shared/api/mfaStepUp'

import MFAStepUpDialog from './MFAStepUpDialog.vue'

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('MFA step-up dialog', () => {
  it('verifies the current session and releases the protected request', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const fetchMock = vi.fn().mockResolvedValue(
      Response.json({ user: { id: 'admin-1', username: 'admin', role: 'admin' } }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(MFAStepUpDialog, { attachTo: document.body, global: { plugins: [pinia] } })

    const verified = requestMFAStepUp()
    await flushPromises()
    expect(document.body.textContent).toContain('Verify this privileged action')

    const input = document.querySelector<HTMLInputElement>('input[name="step-up-code"]')
    if (!input) throw new Error('Step-up input did not render')
    input.value = '123456'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    document.querySelector<HTMLFormElement>('#mfa-step-up-form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))

    await expect(verified).resolves.toBe(true)
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/auth/mfa/verify',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ code: '123456' }) }),
    )
    wrapper.unmount()
  })
})
