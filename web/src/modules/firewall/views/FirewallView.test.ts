// @vitest-environment happy-dom

import { QueryClient, VueQueryPlugin } from '@tanstack/vue-query'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useIdentityStore } from '@/modules/identity/store'

import FirewallView from './FirewallView.vue'

// Each mounted view keeps polling queries and a job subscription alive, so a
// view left mounted would still be patching the DOM during the next test.
const mounted: { unmount: () => void }[] = []

afterEach(() => {
  mounted.splice(0).forEach((wrapper) => wrapper.unmount())
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

// The job stream is not what these tests are about; happy-dom has no
// EventSource, and its absence otherwise fails the run with an unrelated error.
class SilentEventSource {
  close() {}
}

const sshRule = { port: '22', protocol: 'tcp', action: 'allow', from: '', v6: false, comment: 'SSH' }

function signIn() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const identity = useIdentityStore()
  identity.phase = 'authenticated'
  identity.user = { id: 'admin-1', username: 'admin', role: 'admin' }
  return pinia
}

/**
 * Routes the view's two polling queries. `reverts` is re-read on every call so a
 * test can change what the server reports between refetches.
 */
function stubApi(reverts: () => unknown[], onAction?: (body: Record<string, unknown>) => Response) {
  return vi.fn((input: string, init?: RequestInit) => {
    if (input === '/api/v1/firewall') {
      return Promise.resolve(Response.json({ installed: true, active: true, rules: [sshRule] }))
    }
    if (input === '/api/v1/firewall/reverts') {
      return Promise.resolve(Response.json({ items: reverts(), windowSeconds: 120 }))
    }
    if (input === '/api/v1/firewall/action' && onAction) {
      return Promise.resolve(onAction(JSON.parse(String(init?.body)) as Record<string, unknown>))
    }
    if (input === '/api/v1/firewall/reverts/confirm') {
      return Promise.resolve(Response.json({ id: 'revert_1', state: 'confirmed' }))
    }
    return Promise.resolve(Response.json({}))
  })
}

function mountView(pinia: ReturnType<typeof signIn>) {
  vi.stubGlobal('EventSource', SilentEventSource)
  const wrapper = mount(FirewallView, {
    attachTo: document.body,
    global: {
      plugins: [pinia, [VueQueryPlugin, { queryClient: new QueryClient() }]],
      stubs: {
        StatusPill: true,
        Select: true,
        SelectTrigger: true,
        SelectContent: true,
        SelectItem: true,
        RouterLink: true,
      },
    },
  })
  mounted.push(wrapper)
  return wrapper
}

describe('firewall access safety', () => {
  it('requires confirmation before deleting an allow rule', async () => {
    const pinia = signIn()
    vi.stubGlobal('fetch', stubApi(() => []))
    const wrapper = mountView(pinia)
    await flushPromises()

    await wrapper.get('button[title="Remove rule"]').trigger('click')

    expect(document.body.textContent).toContain('Remove access rule for 22/tcp?')
  })

  it("surfaces the server's lockout refusal and retries with the acknowledgement", async () => {
    const pinia = signIn()
    const bodies: Record<string, unknown>[] = []
    const fetchMock = stubApi(
      () => [],
      (body) => {
        bodies.push(body)
        if (!body.confirmLockoutRisk) {
          return Response.json(
            {
              code: 'lockout_risk',
              message: 'This change can cut off your access to this server. It is the only remaining rule admitting SSH (port 22).',
              reasons: ['It is the only remaining rule admitting SSH (port 22).'],
              revertWindowSeconds: 120,
            },
            { status: 409 },
          )
        }
        return Response.json({ job: { id: 5 } }, { status: 202 })
      },
    )
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountView(pinia)
    await flushPromises()

    await wrapper.get('button[title="Remove rule"]').trigger('click')
    // Clear the first typed confirmation, which the server refusal follows.
    const firstInput = document.body.querySelectorAll('input')
    const typed = firstInput[firstInput.length - 1] as HTMLInputElement
    typed.value = '22/tcp'
    typed.dispatchEvent(new Event('input'))
    await flushPromises()
    const buttons = () => Array.from(document.body.querySelectorAll('button'))
    buttons().find((button) => button.textContent?.includes('Remove access rule'))?.click()
    await flushPromises()

    // The server's own explanation is shown, not a generic failure.
    expect(document.body.textContent).toContain('only remaining rule admitting SSH')
    expect(document.body.textContent).toContain('will cut off access')
    expect(bodies).toHaveLength(1)
    expect(bodies[0]).toMatchObject({ confirmLockoutRisk: false })

    const inputs = document.body.querySelectorAll('input')
    const retype = inputs[inputs.length - 1] as HTMLInputElement
    retype.value = '22/tcp'
    retype.dispatchEvent(new Event('input'))
    await flushPromises()
    buttons().find((button) => button.textContent?.includes('Remove it anyway'))?.click()
    await flushPromises()

    expect(bodies).toHaveLength(2)
    expect(bodies[1]).toMatchObject({ confirmLockoutRisk: true })
  })

  it('shows the armed rollback, its deadline, and the confirm action', async () => {
    const pinia = signIn()
    const armed = {
      id: 'revert_1',
      subject: '22/tcp',
      summary: 'Access is restored automatically (allow 22/tcp) unless you confirm you are still connected.',
      reasons: ['It is the only remaining rule admitting SSH (port 22).'],
      armedAt: new Date(Date.now() - 20_000).toISOString(),
      expiresAt: new Date(Date.now() + 100_000).toISOString(),
      state: 'armed',
    }
    const fetchMock = stubApi(() => [armed])
    vi.stubGlobal('fetch', fetchMock)
    mountView(pinia)
    await flushPromises()

    expect(document.body.textContent).toContain('Automatic rollback armed for 22/tcp')
    // The deadline is rendered from the server's expiresAt, not a local guess.
    expect(document.body.textContent).toMatch(/1:4\d/)
    expect(document.body.textContent).toContain('the change is undone automatically')

    const confirm = Array.from(document.body.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('My access still works'),
    )
    expect(confirm).toBeTruthy()
    confirm?.click()
    await flushPromises()

    expect(fetchMock.mock.calls.some(([path]) => path === '/api/v1/firewall/reverts/confirm')).toBe(true)
  })

  it('reports a rollback that already fired', async () => {
    const pinia = signIn()
    vi.stubGlobal(
      'fetch',
      stubApi(() => [
        {
          id: 'revert_2',
          subject: '22/tcp',
          summary: '',
          reasons: [],
          armedAt: new Date(Date.now() - 200_000).toISOString(),
          expiresAt: new Date(Date.now() - 100_000).toISOString(),
          state: 'reverted',
        },
      ]),
    )
    mountView(pinia)
    await flushPromises()

    expect(document.body.textContent).toContain('was rolled back automatically')
  })
})
