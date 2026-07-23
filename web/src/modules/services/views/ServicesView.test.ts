// @vitest-environment happy-dom

import { QueryClient, VueQueryPlugin } from '@tanstack/vue-query'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useIdentityStore } from '@/modules/identity/store'

import ServicesView from './ServicesView.vue'

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

const nginx = { name: 'nginx', systemdUnit: 'nginx.service', status: 'active', active: true, enabled: true }

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
    if (input === '/api/v1/services') return Promise.resolve(Response.json({ items: [nginx] }))
    if (input === '/api/v1/services/reverts') {
      return Promise.resolve(Response.json({ items: reverts(), windowSeconds: 120 }))
    }
    if (input === '/api/v1/services/action' && onAction) {
      return Promise.resolve(onAction(JSON.parse(String(init?.body)) as Record<string, unknown>))
    }
    if (input === '/api/v1/services/reverts/confirm') {
      return Promise.resolve(Response.json({ id: 'revert_1', state: 'confirmed' }))
    }
    return Promise.resolve(Response.json({}))
  })
}

function mountView(pinia: ReturnType<typeof signIn>) {
  vi.stubGlobal('EventSource', SilentEventSource)
  const wrapper = mount(ServicesView, {
    attachTo: document.body,
    global: {
      plugins: [pinia, [VueQueryPlugin, { queryClient: new QueryClient() }]],
      stubs: { StatusPill: true, RouterLink: true },
    },
  })
  mounted.push(wrapper)
  return wrapper
}

function buttonWith(text: string): HTMLButtonElement | undefined {
  return Array.from(document.body.querySelectorAll('button')).find((button) => button.textContent?.includes(text))
}

async function typeConfirmation(value: string) {
  const inputs = document.body.querySelectorAll('input')
  const typed = inputs[inputs.length - 1] as HTMLInputElement
  typed.value = value
  typed.dispatchEvent(new Event('input'))
  await flushPromises()
}

describe('service safety controls', () => {
  it('requires confirmation before stopping nginx', async () => {
    const pinia = signIn()
    vi.stubGlobal('fetch', stubApi(() => []))
    const wrapper = mountView(pinia)
    await flushPromises()

    await wrapper.get('button[title="Stop"]').trigger('click')

    expect(document.body.textContent).toContain('Stop nginx?')
  })

  it("surfaces the server's lockout refusal and retries with the acknowledgement", async () => {
    const pinia = signIn()
    const bodies: Record<string, unknown>[] = []
    vi.stubGlobal(
      'fetch',
      stubApi(
        () => [],
        (body) => {
          bodies.push(body)
          if (!body.confirmLockoutRisk) {
            return Response.json(
              {
                code: 'lockout_risk',
                message:
                  "Stopping this service can cut off your access to this server. Stopping nginx.service stops the panel's HTTP/HTTPS ingress.",
                reasons: ["Stopping nginx.service stops the panel's HTTP/HTTPS ingress."],
                revertWindowSeconds: 120,
              },
              { status: 409 },
            )
          }
          return Response.json({ job: { id: 5 } }, { status: 202 })
        },
      ),
    )
    const wrapper = mountView(pinia)
    await flushPromises()

    await wrapper.get('button[title="Stop"]').trigger('click')
    await typeConfirmation('nginx')
    buttonWith('Stop nginx')?.click()
    await flushPromises()

    // The server's own explanation is shown, not a generic failure.
    expect(document.body.textContent).toContain("stops the panel's HTTP/HTTPS ingress")
    expect(document.body.textContent).toContain('will cut off access')
    expect(bodies).toHaveLength(1)
    expect(bodies[0]).toMatchObject({ confirmLockoutRisk: false })

    await typeConfirmation('nginx')
    buttonWith('Stop it anyway')?.click()
    await flushPromises()

    expect(bodies).toHaveLength(2)
    expect(bodies[1]).toMatchObject({ unit: 'nginx.service', action: 'stop', confirmLockoutRisk: true })
  })

  it('shows the armed rollback, its deadline, and the confirm action', async () => {
    const pinia = signIn()
    const fetchMock = stubApi(() => [
      {
        id: 'revert_1',
        subject: 'nginx.service',
        summary: 'The service is started again automatically unless you confirm the panel and your server session still work.',
        reasons: ["Stopping nginx.service stops the panel's HTTP/HTTPS ingress."],
        armedAt: new Date(Date.now() - 20_000).toISOString(),
        expiresAt: new Date(Date.now() + 100_000).toISOString(),
        state: 'armed',
      },
    ])
    vi.stubGlobal('fetch', fetchMock)
    mountView(pinia)
    await flushPromises()

    expect(document.body.textContent).toContain('Automatic rollback armed for nginx.service')
    // The deadline is rendered from the server's expiresAt, not a local guess.
    expect(document.body.textContent).toMatch(/1:4\d/)
    expect(document.body.textContent).toContain('the change is undone automatically')

    const confirm = buttonWith('My access still works')
    expect(confirm).toBeTruthy()
    confirm?.click()
    await flushPromises()

    expect(fetchMock.mock.calls.some(([path]) => path === '/api/v1/services/reverts/confirm')).toBe(true)
  })

  it('reports a rollback that failed so the operator knows to use a console', async () => {
    const pinia = signIn()
    vi.stubGlobal(
      'fetch',
      stubApi(() => [
        {
          id: 'revert_2',
          subject: 'nginx.service',
          summary: '',
          reasons: [],
          armedAt: new Date(Date.now() - 200_000).toISOString(),
          expiresAt: new Date(Date.now() - 100_000).toISOString(),
          state: 'revert_failed',
          failure: 'node agent unreachable',
        },
      ]),
    )
    mountView(pinia)
    await flushPromises()

    expect(document.body.textContent).toContain('node agent unreachable')
    expect(document.body.textContent).toContain('Restore access from a root console')
  })
})
