// @vitest-environment happy-dom

import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'

import FileEditorDialog from './FileEditorDialog.vue'

// Monaco needs a real browser (canvas, layout); stub it with a textarea that
// still reflects the read-only prop so the authorization contract is testable.
const MonacoStub = defineComponent({
  props: { modelValue: { type: String, default: '' }, language: String, readonly: Boolean, minimap: Boolean },
  emits: ['update:modelValue', 'save'],
  template: '<textarea :readonly="readonly" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
})

// The diff editor is likewise browser-only; reflect both panes as data-* so the
// conflict flow (server copy vs. local edits) is assertable.
const DiffStub = defineComponent({
  props: {
    modelValue: { type: String, default: '' },
    original: { type: String, default: '' },
    language: String,
    readonly: Boolean,
    minimap: Boolean,
  },
  emits: ['update:modelValue', 'save'],
  template: '<div class="diff-stub" :data-original="original" :data-modified="modelValue" />',
})

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
      global: { stubs: { MonacoEditor: MonacoStub } },
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

  it('shows a diff of the server copy when a save conflicts', async () => {
    const fetchMock = vi
      .fn()
      // Initial load, then the conflicting save, then the server-copy re-fetch.
      .mockResolvedValueOnce(Response.json({ content: 'local-base\n', etag: 'etag-1', size: 11, truncated: false, binary: false }))
      .mockResolvedValueOnce(Response.json({ message: 'The file changed.' }, { status: 409 }))
      .mockResolvedValueOnce(Response.json({ content: 'server-new\n', etag: 'etag-2', size: 11, truncated: false, binary: false }))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(FileEditorDialog, {
      attachTo: document.body,
      props: { siteId: 'site-1', path: 'public/index.txt' },
      global: { stubs: { MonacoEditor: MonacoStub, DiffEditor: DiffStub } },
    })
    await flushPromises()

    // Edit the file so it is dirty (Save is disabled otherwise), then save.
    const textarea = document.querySelector('textarea') as HTMLTextAreaElement
    textarea.value = 'local-edit\n'
    textarea.dispatchEvent(new Event('input'))
    await flushPromises()

    const saveButton = [...document.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Save')
    saveButton?.click()
    await flushPromises()

    expect(document.body.textContent).toContain('The file changed on the server')
    const diff = document.querySelector('.diff-stub') as HTMLElement | null
    expect(diff).not.toBeNull()
    // Left pane = the fetched server copy; right pane = the local edits.
    expect(diff?.dataset.original).toBe('server-new\n')
    expect(diff?.dataset.modified).toBe('local-edit\n')
    expect(fetchMock).toHaveBeenCalledTimes(3)

    wrapper.unmount()
  })
})
