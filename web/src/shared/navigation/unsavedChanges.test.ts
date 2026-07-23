// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from 'vitest'

import { registerUnsavedChanges, requestDiscardUnsavedChanges } from './unsavedChanges'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('unsaved-change boundary', () => {
  it('blocks a destructive transition until the operator confirms', () => {
    const unregister = registerUnsavedChanges(() => true)
    vi.spyOn(window, 'confirm').mockReturnValue(false)

    expect(requestDiscardUnsavedChanges()).toBe(false)

    vi.mocked(window.confirm).mockReturnValue(true)
    expect(requestDiscardUnsavedChanges()).toBe(true)
    unregister()
    expect(requestDiscardUnsavedChanges()).toBe(true)
  })
})
