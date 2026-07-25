// @vitest-environment happy-dom

import { QueryClient, VueQueryPlugin } from '@tanstack/vue-query'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { TooltipProvider } from '@/shared/ui'

import type { FileEntry } from '../api'
import FilesView from './FilesView.vue'

// Each mounted view keeps a polling query and a job subscription alive, so a
// view left mounted would still be patching the DOM during the next test.
const mounted: { unmount: () => void }[] = []

afterEach(() => {
  mounted.splice(0).forEach((wrapper) => wrapper.unmount())
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

/** The job stream is irrelevant here and happy-dom has no EventSource. */
class SilentEventSource {
  close() {}
}

const site = {
  id: 'site-1',
  slug: 'example',
  displayName: 'Example',
  primaryDomain: 'example.test',
  status: 'active',
  settings: {},
}

function file(name: string, overrides: Partial<FileEntry> = {}): FileEntry {
  return {
    name,
    kind: 'file',
    size: 21,
    mode: 'rw-r--r--',
    owner: 'example_usr',
    group: 'example_usr',
    modifiedAt: '2026-01-28T15:06:00Z',
    ...overrides,
  }
}

/** Directory listings keyed by site-relative path, as the API returns them. */
type Tree = Record<string, FileEntry[]>

interface Call {
  url: string
  body: Record<string, unknown>
}

function stubApi(tree: Tree, calls: Call[], failWith?: (url: string) => Response | undefined) {
  const fetchMock = vi.fn((input: string, init?: RequestInit) => {
    const [url, query = ''] = input.split('?')
    if (url === '/api/v1/sites') return Promise.resolve(Response.json({ items: [site] }))
    if (url === '/api/v1/sites/site-1/files') {
      const path = decodeURIComponent(new URLSearchParams(query).get('path') ?? '.')
      return Promise.resolve(Response.json({ entries: tree[path] ?? [], truncated: false }))
    }
    if (init?.body) calls.push({ url: url ?? '', body: JSON.parse(String(init.body)) as Record<string, unknown> })
    const failure = failWith?.(url ?? '')
    return Promise.resolve(failure ?? Response.json({}))
  })
  vi.stubGlobal('fetch', fetchMock)
  vi.stubGlobal('EventSource', SilentEventSource)
  return fetchMock
}

function signIn() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const identity = useIdentityStore()
  identity.phase = 'authenticated'
  identity.user = { id: 'admin-1', username: 'admin', role: 'admin' }
  return pinia
}

async function mountView(path: string) {
  const pinia = signIn()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/files', name: 'files', component: FilesView }],
  })
  await router.push({ path: '/files', query: { site: 'site-1', path } })
  await router.isReady()

  // Tooltips inject a provider the real app installs at its root; without it
  // any StatusPill in the tree aborts the render.
  const host = defineComponent({ render: () => h(TooltipProvider, null, { default: () => h(FilesView) }) })
  const wrapper = mount(host, {
    attachTo: document.body,
    global: {
      plugins: [pinia, router, [VueQueryPlugin, { queryClient: new QueryClient() }]],
      stubs: { Combobox: true, ComboboxAnchor: true, ComboboxList: true, ComboboxTrigger: true },
    },
  })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, router }
}

const buttons = () => [...document.querySelectorAll('button')]
const buttonNamed = (label: string) => buttons().find((button) => button.textContent?.trim() === label)

/** Selects a row the way a user does — a plain click anywhere on it. */
async function selectRow(name: string) {
  const row = document.querySelector(`tr[data-name="${name}"]`) as HTMLElement
  row.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flushPromises()
}

async function clickButton(label: string) {
  buttonNamed(label)?.click()
  await flushPromises()
}

describe('FilesView clipboard', () => {
  it('pastes into the folder the user navigated to instead of asking for a path', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('report.txt')], 'public/archive': [] }, calls)
    const { router } = await mountView('public')

    await selectRow('report.txt')
    await clickButton('Copy')

    // Staged, not applied: nothing has been sent and no destination was typed.
    expect(calls).toHaveLength(0)
    expect(document.body.textContent).toContain('1 item ready to copy')
    expect(document.querySelector('input[aria-label="Destination path"]')).toBeNull()

    await router.push({ query: { site: 'site-1', path: 'public/archive' } })
    await flushPromises()

    await clickButton('Paste here')
    expect(calls).toEqual([
      { url: '/api/v1/sites/site-1/files/copy', body: { from: 'public/report.txt', to: 'public/archive/report.txt' } },
    ])
    // A clean paste empties the tray.
    expect(document.body.textContent).not.toContain('ready to copy')
  })

  it('moves every selected entry on one paste', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('a.txt'), file('b.txt')], tmp: [] }, calls)
    const { router } = await mountView('public')

    await selectRow('a.txt')
    const rowB = document.querySelector('tr[data-name="b.txt"]') as HTMLElement
    rowB.dispatchEvent(new MouseEvent('click', { bubbles: true, metaKey: true }))
    await flushPromises()
    expect(document.body.textContent).toContain('2 items selected')

    await clickButton('Cut')
    await router.push({ query: { site: 'site-1', path: 'tmp' } })
    await flushPromises()
    await clickButton('Paste here')

    expect(calls).toEqual([
      { url: '/api/v1/sites/site-1/files/move', body: { from: 'public/a.txt', to: 'tmp/a.txt', overwrite: false } },
      { url: '/api/v1/sites/site-1/files/move', body: { from: 'public/b.txt', to: 'tmp/b.txt', overwrite: false } },
    ])
  })

  it('refuses to paste a cut back into the folder it came from', async () => {
    stubApi({ public: [file('report.txt')] }, [])
    await mountView('public')

    await selectRow('report.txt')
    await clickButton('Cut')

    expect(document.body.textContent).toContain('These items are already in this folder')
    expect(buttonNamed('Paste here')?.disabled).toBe(true)
  })

  it('asks before a paste would land on an existing name, and can keep both', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('report.txt')], tmp: [file('report.txt'), file('report (copy).txt')] }, calls)
    const { router } = await mountView('public')

    await selectRow('report.txt')
    await clickButton('Copy')
    await router.push({ query: { site: 'site-1', path: 'tmp' } })
    await flushPromises()
    await clickButton('Paste here')

    // Nothing is sent until the collision is resolved.
    expect(calls).toHaveLength(0)
    expect(document.body.textContent).toContain('1 item already exist here')
    // Copy cannot overwrite on the wire, so replacing is not offered for it.
    expect(buttonNamed('Replace')).toBeUndefined()

    await clickButton('Keep both')
    expect(calls).toEqual([
      { url: '/api/v1/sites/site-1/files/copy', body: { from: 'public/report.txt', to: 'tmp/report (copy 2).txt' } },
    ])
  })

  it('replaces on a move only when the user chooses to', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('report.txt')], tmp: [file('report.txt')] }, calls)
    const { router } = await mountView('public')

    await selectRow('report.txt')
    await clickButton('Cut')
    await router.push({ query: { site: 'site-1', path: 'tmp' } })
    await flushPromises()
    await clickButton('Paste here')
    await clickButton('Replace')

    expect(calls).toEqual([
      { url: '/api/v1/sites/site-1/files/move', body: { from: 'public/report.txt', to: 'tmp/report.txt', overwrite: true } },
    ])
  })

  it('keeps a failed entry on the clipboard so it can be retried', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('report.txt')], tmp: [] }, calls, (url) =>
      url.endsWith('/copy') ? Response.json({ message: 'Quota exceeded.' }, { status: 507 }) : undefined,
    )
    const { router } = await mountView('public')

    await selectRow('report.txt')
    await clickButton('Copy')
    await router.push({ query: { site: 'site-1', path: 'tmp' } })
    await flushPromises()
    await clickButton('Paste here')

    expect(calls).toHaveLength(1)
    expect(document.body.textContent).toContain('1 item ready to copy')
  })

  it('offers no paste target in a read-only tree', async () => {
    stubApi({ public: [file('report.txt')], logs: [file('access.log')] }, [])
    const { router } = await mountView('public')

    await selectRow('report.txt')
    await clickButton('Copy')
    await router.push({ query: { site: 'site-1', path: 'logs' } })
    await flushPromises()

    expect(document.body.textContent).toContain('Read-only path')
    expect(document.body.textContent).toContain('This folder is read-only')
    expect(buttonNamed('Paste here')?.disabled).toBe(true)
  })

  it('copies and pastes from the keyboard', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('report.txt')], tmp: [] }, calls)
    const { router } = await mountView('public')

    await selectRow('report.txt')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'c', ctrlKey: true, bubbles: true }))
    await flushPromises()
    expect(document.body.textContent).toContain('1 item ready to copy')

    await router.push({ query: { site: 'site-1', path: 'tmp' } })
    await flushPromises()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'v', ctrlKey: true, bubbles: true }))
    await flushPromises()

    expect(calls).toEqual([
      { url: '/api/v1/sites/site-1/files/copy', body: { from: 'public/report.txt', to: 'tmp/report.txt' } },
    ])
  })
})

describe('FilesView delete', () => {
  it('deletes every selected file behind one confirmation', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('a.txt'), file('b.txt')] }, calls)
    await mountView('public')

    await selectRow('a.txt')
    const rowB = document.querySelector('tr[data-name="b.txt"]') as HTMLElement
    rowB.dispatchEvent(new MouseEvent('click', { bubbles: true, metaKey: true }))
    await flushPromises()

    await clickButton('Delete')
    expect(calls).toHaveLength(0)
    expect(document.body.textContent).toContain('Delete the 2 items selected in /public')

    await clickButton('Delete 2 items')
    expect(calls).toEqual([
      { url: '/api/v1/sites/site-1/files/delete', body: { path: 'public/a.txt', recursive: false } },
      { url: '/api/v1/sites/site-1/files/delete', body: { path: 'public/b.txt', recursive: false } },
    ])
  })

  it('makes a delete that recurses into a folder type-to-confirm', async () => {
    const calls: Call[] = []
    stubApi({ public: [file('assets', { kind: 'dir' }), file('a.txt')] }, calls)
    await mountView('public')

    await selectRow('assets')
    const rowFile = document.querySelector('tr[data-name="a.txt"]') as HTMLElement
    rowFile.dispatchEvent(new MouseEvent('click', { bubbles: true, metaKey: true }))
    await flushPromises()
    await clickButton('Delete')

    expect((buttonNamed('Delete 2 items') as HTMLButtonElement).disabled).toBe(true)

    const typed = document.querySelector('input[placeholder="delete"]') as HTMLInputElement
    typed.value = 'delete'
    typed.dispatchEvent(new Event('input'))
    await flushPromises()

    await clickButton('Delete 2 items')
    expect(calls).toEqual([
      { url: '/api/v1/sites/site-1/files/delete', body: { path: 'public/assets', recursive: true } },
      { url: '/api/v1/sites/site-1/files/delete', body: { path: 'public/a.txt', recursive: false } },
    ])
  })
})

describe('FilesView selection', () => {
  it('filters the listing by name through the toolbar field', async () => {
    stubApi({ public: [file('report.txt'), file('notes.md')] }, [])
    const { wrapper } = await mountView('public')

    const filter = document.querySelector('input[aria-label="Filter entries by name"]') as HTMLInputElement
    filter.value = 'report'
    filter.dispatchEvent(new Event('input'))
    await flushPromises()

    expect(wrapper.find('tr[data-name="report.txt"]').exists()).toBe(true)
    expect(wrapper.find('tr[data-name="notes.md"]').exists()).toBe(false)
    expect(document.body.textContent).toContain('1 of 2 shown')
  })

  it('drops entries that vanish from the listing out of the selection', async () => {
    const tree: Tree = { public: [file('a.txt'), file('b.txt')] }
    stubApi(tree, [])
    const { wrapper } = await mountView('public')

    await selectRow('a.txt')
    expect(document.body.textContent).toContain('a.txt')

    tree.public = [file('b.txt')]
    await clickButton('Refresh')
    await flushPromises()

    // The toolbar is back to its no-selection form.
    expect(buttonNamed('New folder')).toBeDefined()
    expect(wrapper.find('tr[data-name="a.txt"]').exists()).toBe(false)
  })
})
