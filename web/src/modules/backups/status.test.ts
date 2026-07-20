import { describe, expect, it } from 'vitest'

import { backupCopyHealth, backupScheduleStatus } from './status'
import type { BackupCopy, BackupPlan } from './api'

const plan = {
  id: 'plan-1',
  name: 'Nightly',
  accountId: 'account-1',
  copiesLimit: 7,
  siteIds: [],
  databaseIds: [],
  schedule: '0 3 * * *',
  enabled: true,
  scheduleState: 'installed',
  createdAt: '2026-07-19T00:00:00Z',
  updatedAt: '2026-07-19T00:00:00Z',
} satisfies BackupPlan

const copy = {
  id: 'copy-1',
  planId: plan.id,
  accountId: plan.accountId,
  copyName: '20260719T030000Z',
  remotePath: 'plan-1/20260719T030000Z',
  sizeBytes: 128,
  entries: [],
  status: 'uploaded',
  integrityState: 'unverified',
  restoreTestState: 'not_tested',
  healthy: false,
  createdAt: '2026-07-19T03:00:00Z',
} satisfies BackupCopy

describe('backup status presentation', () => {
  it('shows schedule reconciliation failures instead of desired enabled state', () => {
    expect(backupScheduleStatus({ ...plan, scheduleState: 'error', scheduleError: 'systemd is unavailable' })).toEqual({
      label: 'Schedule error',
      tone: 'danger',
      description: 'systemd is unavailable',
    })
  })

  it('does not report an uploaded but unverified copy as healthy', () => {
    expect(backupCopyHealth(copy)).toEqual({
      label: 'Unverified',
      tone: 'warning',
      description: 'Uploaded, but integrity and restore verification have not run.',
    })
  })

  it('surfaces a recorded health error', () => {
    expect(backupCopyHealth({ ...copy, healthError: 'checksum mismatch' })).toEqual({
      label: 'Verification failed',
      tone: 'danger',
      description: 'checksum mismatch',
    })
  })
})
