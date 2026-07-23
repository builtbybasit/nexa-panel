// @vitest-environment happy-dom

import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { registerUnsavedChanges } from '@/shared/navigation/unsavedChanges'

import { createNexaRouter } from './createNexaRouter'

beforeEach(() => {
  setActivePinia(createPinia())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('direct route authorization', () => {
  it('sends a viewer to the forbidden page instead of mounting an admin view', async () => {
    const identity = useIdentityStore()
    identity.initialized = true
    identity.phase = 'authenticated'
    identity.user = { id: 'viewer-1', username: 'reader', role: 'viewer' }
    const router = createNexaRouter(createMemoryHistory())

    await router.push('/users')

    expect(router.currentRoute.value.name).toBe('forbidden')
  })

  it('allows every authenticated role to open its own account security page', async () => {
    const identity = useIdentityStore()
    identity.initialized = true
    identity.phase = 'authenticated'
    identity.user = { id: 'viewer-1', username: 'reader', role: 'viewer' }
    const router = createNexaRouter(createMemoryHistory())

    await router.push('/account/security')

    expect(router.currentRoute.value.name).toBe('account-security')
  })

  it('does not let a read-only role open a write-only create route directly', async () => {
    const identity = useIdentityStore()
    identity.initialized = true
    identity.phase = 'authenticated'
    identity.user = { id: 'viewer-1', username: 'reader', role: 'viewer' }
    const router = createNexaRouter(createMemoryHistory())

    await router.push('/sites/new')

    expect(router.currentRoute.value.name).toBe('forbidden')
  })

  it('keeps the editor mounted when a route change would discard unsaved buffers', async () => {
    const identity = useIdentityStore()
    identity.initialized = true
    identity.phase = 'authenticated'
    identity.user = { id: 'admin-1', username: 'admin', role: 'admin' }
    const router = createNexaRouter(createMemoryHistory())
    await router.push('/files?site=site-1&path=public')
    const unregister = registerUnsavedChanges(() => true)
    // See unsavedChanges.test.ts: confirm has to be installed, not spied on.
    const confirm = vi.fn(() => false)
    vi.stubGlobal('confirm', confirm)

    await router.push('/services')

    expect(router.currentRoute.value.path).toBe('/files')
    expect(confirm).toHaveBeenCalledOnce()
    unregister()
  })
})
