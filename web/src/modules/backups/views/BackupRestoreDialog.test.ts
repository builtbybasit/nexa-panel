// @vitest-environment happy-dom

import { QueryClient, VueQueryPlugin } from '@tanstack/vue-query'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { BackupCopy } from '../api'
import BackupRestoreDialog from './BackupRestoreDialog.vue'

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('backup restore review', () => {
  it('does not guess a destination type for an unknown archive entry', async () => {
    const copy: BackupCopy = {
      id: 'copy-1',
      planId: 'plan-1',
      accountId: 'account-1',
      copyName: 'nightly-2026-07-22',
      remotePath: '/nightly-2026-07-22',
      sizeBytes: 10,
      entries: [{ name: 'mystery.bin', sizeBytes: 10, sha256: 'a'.repeat(64) }],
      status: 'complete',
      integrityState: 'passed',
      restoreTestState: 'passed',
      healthy: true,
      createdAt: '2026-07-22T00:00:00Z',
    }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ items: [] })))
    const wrapper = mount(BackupRestoreDialog, {
      props: { copy, busy: false },
      attachTo: document.body,
      global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient() }]] },
    })
    await flushPromises()

    expect(document.body.textContent).toContain('Unsupported backup entry')
    const review = [...document.body.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Review restore')
    expect(review?.disabled).toBe(true)
    wrapper.unmount()
  })

  it('renders the server-resolved plan before accepting the copy-specific confirmation', async () => {
    const copy: BackupCopy = {
      id: 'copy-2',
      planId: 'plan-1',
      accountId: 'account-1',
      copyName: 'nightly-2026-07-22',
      remotePath: '/nightly-2026-07-22',
      sizeBytes: 10,
      entries: [{ name: 'site-blog.tar.gz', sizeBytes: 10, sha256: 'b'.repeat(64) }],
      status: 'complete',
      integrityState: 'passed',
      restoreTestState: 'passed',
      healthy: true,
      createdAt: '2026-07-22T00:00:00Z',
    }
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url === '/api/v1/backups/copies/copy-2/restore/preview') {
        return Response.json({
          copyId: 'copy-2',
          copyName: 'nightly-2026-07-22',
          integrityState: 'passed',
          warnings: [],
          items: [{
            kind: 'site',
            sourceEntry: 'site-blog.tar.gz',
            destinationRef: 'site-1',
            destinationLabel: 'blog',
            clear: false,
            impact: 'Backup files may overwrite existing files; unrelated destination files are retained.',
          }],
          previewToken: 'bound-preview-token',
          expiresAt: '2026-07-23T12:05:00Z',
        })
      }
      return Response.json({
        items: url === '/api/v1/sites'
          ? [{ id: 'site-1', slug: 'blog', displayName: 'Blog', primaryDomain: 'example.test', status: 'active' }]
          : [],
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(BackupRestoreDialog, {
      props: { copy, busy: false },
      attachTo: document.body,
      global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient() }]] },
    })
    await flushPromises()
    const button = (label: string) => [...document.body.querySelectorAll('button')].find((entry) => entry.textContent?.trim() === label)

    expect(document.querySelector('input[placeholder="RESTORE nightly-2026-07-22"]')).toBeNull()
    expect(button('Review restore')?.disabled).toBe(false)
    button('Review restore')?.click()
    await flushPromises()

    expect(document.body.textContent).toContain('Reviewed restore plan')
    expect(document.body.textContent).toContain('site-blog.tar.gz')
    expect(document.body.textContent).toContain('blog')
    expect(document.body.textContent).toContain('unrelated destination files are retained')
    expect(button('Restore')?.disabled).toBe(true)
    const confirmation = document.querySelector<HTMLInputElement>('input[placeholder="RESTORE nightly-2026-07-22"]')
    if (!confirmation) throw new Error('Restore confirmation input was not rendered')
    confirmation.value = 'RESTORE nightly-2026-07-22'
    confirmation.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()

    expect(button('Restore')?.disabled).toBe(false)
    button('Restore')?.click()
    await flushPromises()

    expect(wrapper.emitted('restore')).toEqual([[
      {
        sites: [{ entry: 'site-blog.tar.gz', siteId: 'site-1', clear: false }],
        databases: [],
        allowUnverified: false,
        previewToken: 'bound-preview-token',
      },
    ]])
    wrapper.unmount()
  })

  it('invalidates the reviewed plan when a destructive choice changes', async () => {
    const copy: BackupCopy = {
      id: 'copy-3',
      planId: 'plan-1',
      accountId: 'account-1',
      copyName: 'nightly-2026-07-23',
      remotePath: '/nightly-2026-07-23',
      sizeBytes: 10,
      entries: [{ name: 'site-blog.tar.gz', sizeBytes: 10, sha256: 'c'.repeat(64) }],
      status: 'complete',
      integrityState: 'passed',
      restoreTestState: 'passed',
      healthy: true,
      createdAt: '2026-07-23T00:00:00Z',
    }
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: string) => {
      if (url.endsWith('/restore/preview')) {
        return Response.json({
          copyId: copy.id,
          copyName: copy.copyName,
          integrityState: 'passed',
          warnings: [],
          items: [{
            kind: 'site', sourceEntry: 'site-blog.tar.gz', destinationRef: 'site-1', destinationLabel: 'blog',
            clear: false, impact: 'Backup files may overwrite existing files.',
          }],
          previewToken: 'token', expiresAt: '2026-07-23T12:05:00Z',
        })
      }
      return Response.json({
        items: url === '/api/v1/sites'
          ? [{ id: 'site-1', slug: 'blog', displayName: 'Blog', primaryDomain: 'example.test', status: 'active' }]
          : [],
      })
    }))
    const wrapper = mount(BackupRestoreDialog, {
      props: { copy, busy: false },
      attachTo: document.body,
      global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient() }]] },
    })
    await flushPromises()
    const review = [...document.body.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Review restore')
    review?.click()
    await flushPromises()
    expect(document.body.textContent).toContain('Reviewed restore plan')

    const clear = [...document.body.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find((input) =>
      input.parentElement?.textContent?.includes("Clear the destination's current data first"),
    )
    if (!clear) throw new Error('Clear-first choice was not rendered')
    clear.click()
    await flushPromises()

    expect(document.body.textContent).not.toContain('Reviewed restore plan')
    expect(document.querySelector('input[placeholder="RESTORE nightly-2026-07-23"]')).toBeNull()
    expect([...document.body.querySelectorAll('button')].some((button) => button.textContent?.trim() === 'Review restore')).toBe(true)
    wrapper.unmount()
  })
})
