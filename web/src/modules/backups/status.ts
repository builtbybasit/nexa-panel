import type { BackupCopy, BackupPlan } from './api'

type BackupStatusTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral'

export interface BackupStatusPresentation {
  label: string
  tone: BackupStatusTone
  description: string
}

export function backupScheduleStatus(plan: BackupPlan): BackupStatusPresentation {
  if (plan.scheduleState === 'error') {
    return {
      label: 'Schedule error',
      tone: 'danger',
      description: plan.scheduleError || 'The host schedule could not be reconciled.',
    }
  }
  if (plan.scheduleState === 'pending') {
    return {
      label: 'Reconciling',
      tone: 'warning',
      description: 'The desired schedule has not yet been confirmed on the host.',
    }
  }
  if (plan.scheduleState === 'installed' && plan.enabled) {
    return {
      label: 'Scheduled',
      tone: 'success',
      description: 'The host timer matches this enabled backup plan.',
    }
  }
  if (plan.scheduleState === 'disabled' && !plan.enabled) {
    return {
      label: 'Disabled',
      tone: 'neutral',
      description: 'The host timer is absent and automatic runs are disabled.',
    }
  }
  return {
    label: 'Out of sync',
    tone: 'warning',
    description: 'The desired state and observed host schedule do not match yet.',
  }
}

export function backupCopyHealth(copy: BackupCopy): BackupStatusPresentation {
  if (copy.healthError || copy.integrityState === 'failed' || copy.restoreTestState === 'failed') {
    return {
      label: 'Verification failed',
      tone: 'danger',
      description: copy.healthError || 'Integrity or restore verification failed.',
    }
  }
  if (copy.healthy) {
    return {
      label: 'Verified',
      tone: 'success',
      description: 'Integrity and restore verification both passed.',
    }
  }
  if (copy.status !== 'uploaded') {
    return {
      label: 'Incomplete',
      tone: 'danger',
      description: 'The backup upload did not finish successfully.',
    }
  }
  if (copy.integrityState === 'passed') {
    return {
      label: 'Restore test pending',
      tone: 'warning',
      description: 'Integrity passed, but a restore test has not passed yet.',
    }
  }
  return {
    label: 'Unverified',
    tone: 'warning',
    description: 'Uploaded, but integrity and restore verification have not run.',
  }
}
