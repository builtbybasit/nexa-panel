// @vitest-environment happy-dom

import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import FileEditorDialog from './FileEditorDialog.vue'

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('FileEditorDialog authorization', () => {
  it('keeps a read-only session from issuing write requests', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      Response.json({ content: 'managed=true\n', etag: 'etag-1', size: 13, truncated: false, binary: false }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(FileEditorDialog, {
      attachTo: document.body,
      props: { siteId: 'site-1', path: 'public/index.txt', readOnly: true },
    })
    await flushPromises()

    expect(document.body.textContent).toContain('can view this file but cannot edit it')
    expect(document.querySelector('textarea')?.readOnly).toBe(true)
    expect([...document.querySelectorAll('button')].some((button) => button.textContent?.trim() === 'Save')).toBe(false)

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 's', ctrlKey: true }))
    await flushPromises()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sites/site-1/files/content?path=public%2Findex.txt',
      expect.objectContaining({ credentials: 'same-origin' }),
    )

    wrapper.unmount()
  })
})
