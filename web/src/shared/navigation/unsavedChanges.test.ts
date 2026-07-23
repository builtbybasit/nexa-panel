// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from 'vitest'

import { registerUnsavedChanges, requestDiscardUnsavedChanges } from './unsavedChanges'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('unsaved-change boundary', () => {
  it('blocks a destructive transition until the operator confirms', () => {
    const unregister = registerUnsavedChanges(() => true)
    // No test environment ships window.confirm — happy-dom has never had it, and
    // Vitest stopped supplying a stub in v4 — so the dialog is installed here
    // rather than spied on. globalThis is what the module under test reads.
    const confirm = vi.fn(() => false)
    vi.stubGlobal('confirm', confirm)

    expect(requestDiscardUnsavedChanges()).toBe(false)

    confirm.mockReturnValue(true)
    expect(requestDiscardUnsavedChanges()).toBe(true)
    unregister()
    expect(requestDiscardUnsavedChanges()).toBe(true)
  })
})
