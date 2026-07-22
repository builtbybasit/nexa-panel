// @vitest-environment happy-dom

import { QueryClient, VueQueryPlugin } from '@tanstack/vue-query'
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SharedEnvCard from './SharedEnvCard.vue'

// vue-query seeds a query's data from the cache *synchronously* inside setup()
// (`reactive(observer.getCurrentResult())`), so a revisit within the default
// five-minute gcTime hands the card its data before the component body has
// finished running. The card's immediate watcher therefore reads `dirty` during
// setup, and `dirty` has to be declared above it — otherwise the read lands in
// its temporal dead zone and the whole Deployment page blanks out with a
// ReferenceError that only a hard reload clears.
describe('SharedEnvCard', () => {
  // The card is mounted for what setup() does with the cache, not for what the
  // network answers; a request that never settles keeps the query pending and
  // keeps the suite off the network.
  beforeEach(() => vi.stubGlobal('fetch', () => new Promise(() => {})))
  afterEach(() => vi.unstubAllGlobals())

  it('mounts against a warm query cache', () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(['shared-env', 's1'], {
      siteId: 's1',
      path: '/srv/nexa/sites/blog/app/shared/.env',
      present: true,
      content: 'APP_ENV=production\n',
      bytes: 19,
      sha256: 'abc',
    })

    const wrapper = mount(SharedEnvCard, {
      props: { siteId: 's1' },
      global: { plugins: [createPinia(), [VueQueryPlugin, { queryClient }]] },
    })

    expect(wrapper.text()).toContain('/srv/nexa/sites/blog/app/shared/.env')
    expect(wrapper.text()).toContain('In place')
    // The draft was seeded from the cached document, so nothing reads as unsaved.
    expect(wrapper.text()).not.toContain('Unsaved changes')
  })

  it('mounts against a cold cache without reading as dirty', () => {
    const wrapper = mount(SharedEnvCard, {
      props: { siteId: 's2' },
      global: { plugins: [createPinia(), [VueQueryPlugin, { queryClient: new QueryClient() }]] },
    })
    expect(wrapper.text()).not.toContain('Unsaved changes')
  })
})
