// @vitest-environment happy-dom

import { QueryClient, VueQueryPlugin } from '@tanstack/vue-query'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'

import { createNexaRouter } from '../createNexaRouter'
import CommandPalette from './CommandPalette.vue'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('command palette resource search', () => {
  it('does not request resource catalogs the signed-in role cannot read', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const identity = useIdentityStore()
    identity.initialized = true
    identity.phase = 'authenticated'
    identity.user = { id: 'developer-1', username: 'dev', role: 'developer' }
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [] }))
    vi.stubGlobal('fetch', fetchMock)
    const router = createNexaRouter(createMemoryHistory())
    await router.push('/')
    const wrapper = mount(CommandPalette, {
      props: { open: false },
      global: { plugins: [pinia, router, [VueQueryPlugin, { queryClient: new QueryClient() }]] },
    })

    await wrapper.setProps({ open: true })
    await flushPromises()

    const urls = fetchMock.mock.calls.map(([url]) => url)
    expect(urls).toContain('/api/v1/sites')
    expect(urls).not.toContain('/api/v1/domains')
    expect(urls).not.toContain('/api/v1/certificates')
    expect(urls.some((url) => String(url).includes('/api/v1/postgresql'))).toBe(false)
    expect(urls.some((url) => String(url).includes('/api/v1/mysql-family'))).toBe(false)
  })
})
