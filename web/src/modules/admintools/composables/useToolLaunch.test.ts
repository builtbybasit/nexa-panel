// @vitest-environment happy-dom

import { VueQueryPlugin, QueryClient } from '@tanstack/vue-query'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useIdentityStore } from '@/modules/identity/store'

import { useToolLaunch } from './useToolLaunch'

function mountHarness() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const identity = useIdentityStore(pinia)
  identity.user = { id: 'admin-1', username: 'admin', role: 'admin' }
  identity.phase = 'authenticated'
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const Harness = defineComponent({
    setup: () => useToolLaunch(),
    template: `
      <p data-testid="availability">{{ availability('pgadmin') }}</p>
      <button data-testid="launch" @click="launch('pgadmin', 'postgresql', 'database-1')">Launch</button>
    `,
  })
  return mount(Harness, { global: { plugins: [pinia, [VueQueryPlugin, { queryClient }]] } })
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('useToolLaunch', () => {
  it('opens a tab during the click and navigates it after launch authorization succeeds', async () => {
    let finishLaunch: ((response: Response) => void) | undefined
    const launchResponse = new Promise<Response>((resolve) => {
      finishLaunch = resolve
    })
    vi.stubGlobal(
      'fetch',
      vi.fn((input: string | URL | Request) => {
        const path = String(input)
        if (path === '/api/v1/admin-tools') {
          return Promise.resolve(Response.json({ items: [{ kind: 'pgadmin', status: 'active' }] }))
        }
        return launchResponse
      }),
    )
    const replace = vi.fn()
    const popup = { opener: window, location: { replace }, close: vi.fn() }
    const open = vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window)
    const wrapper = mountHarness()
    await flushPromises()

    await wrapper.get('[data-testid="launch"]').trigger('click')
    expect(open).toHaveBeenCalledWith('about:blank', '_blank')
    expect(popup.opener).toBeNull()
    expect(replace).not.toHaveBeenCalled()

    finishLaunch?.(Response.json({ url: '/tools/pgadmin/' }, { status: 201 }))
    await flushPromises()
    expect(replace).toHaveBeenCalledWith('/tools/pgadmin/')
  })

  it('distinguishes a failed tool query from an inactive tool', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    const wrapper = mountHarness()
    await flushPromises()

    expect(wrapper.get('[data-testid="availability"]').text()).toBe('error')
  })

  it('treats an idle on-demand tool as launchable — the launch bootstrap restarts it', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(Response.json({ items: [{ kind: 'pgadmin', status: 'idle' }] }))),
    )
    const wrapper = mountHarness()
    await flushPromises()

    expect(wrapper.get('[data-testid="availability"]').text()).toBe('ready')
  })
})
