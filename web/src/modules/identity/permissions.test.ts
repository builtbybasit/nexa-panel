import { describe, expect, it } from 'vitest'

import { hasPermission } from './permissions'

describe('session permissions', () => {
  it('allows only administrators to apply reviewed operations and manage users', () => {
    for (const role of ['viewer', 'developer', 'operator'] as const) {
      expect(hasPermission(role, 'operations.apply')).toBe(false)
      expect(hasPermission(role, 'users.manage')).toBe(false)
      expect(hasPermission(role, 'system.update')).toBe(false)
    }
    expect(hasPermission('admin', 'operations.apply')).toBe(true)
    expect(hasPermission('admin', 'users.manage')).toBe(true)
    expect(hasPermission('admin', 'system.update')).toBe(true)
  })

  it('preserves the operator write capabilities without granting apply', () => {
    expect(hasPermission('operator', 'databases.write')).toBe(true)
    expect(hasPermission('operator', 'applications.write')).toBe(true)
    expect(hasPermission('operator', 'operations.apply')).toBe(false)
  })

  it('lets viewers read services but only operators act on them', () => {
    expect(hasPermission('viewer', 'services.read')).toBe(true)
    expect(hasPermission('viewer', 'services.write')).toBe(false)
    expect(hasPermission('operator', 'services.write')).toBe(true)
    expect(hasPermission('admin', 'services.write')).toBe(true)
  })

  it('denies missing and unknown sessions', () => {
    expect(hasPermission(undefined, 'system.read')).toBe(false)
    expect(hasPermission('unknown', 'system.read')).toBe(false)
  })
})
