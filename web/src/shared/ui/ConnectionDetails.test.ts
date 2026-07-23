// @vitest-environment happy-dom

import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import ConnectionDetails from './ConnectionDetails.vue'

function mountCard() {
  return mount(ConnectionDetails, {
    attachTo: document.body,
    props: {
      engine: 'postgres' as const,
      engineLabel: 'PostgreSQL 17 · main',
      port: 5432,
      database: 'app_db',
      users: [
        { value: 'app_owner', label: 'app_owner' },
        { value: 'app_reader', label: 'app_reader' },
      ],
      socketPath: '/var/run/postgresql',
    },
  })
}

describe('ConnectionDetails', () => {
  it('defaults to the tunnelled loopback target and copies it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const wrapper = mountCard()

    expect(wrapper.text()).toContain('ssh -N -L 15432:127.0.0.1:5432 root@localhost')
    expect(wrapper.text()).toContain('postgresql://app_owner@127.0.0.1:15432/app_db')

    const copyUri = wrapper
      .findAll('button')
      .find((button) => button.attributes('aria-label') === 'Copy Connection URI')
    await copyUri?.trigger('click')
    expect(writeText).toHaveBeenCalledWith('postgresql://app_owner@127.0.0.1:15432/app_db')

    vi.unstubAllGlobals()
  })

  it('switches to the node address when the tunnel is turned off', async () => {
    const wrapper = mountCard()
    const direct = wrapper.findAll('button').find((button) => button.text() === 'Direct')
    await direct?.trigger('click')

    expect(wrapper.text()).toContain('postgresql://app_owner@localhost:5432/app_db')
    expect(wrapper.text()).not.toContain('ssh -N -L')
  })
})
